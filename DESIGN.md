# n5-fangov — Design Contract

Guarded fan control for Proxmox VE and Debian. Single static Go binary: daemon
(regulation + HTTP API + embedded web UI), CLI, hardware profiles. Hardware-verified
on the Minisforum N5 Pro (IT5571 EC via `minisforum_n5_it5571`); generic hwmon
profiles (NCT67xx, IT87xx, monitoring-only) ship as "from documentation, untested".

This file is the contract between packages. Every builder follows it; deviations are
documented here first, then implemented.

## Non-negotiable rules

1. **The daemon is the only thing that touches hardware.** Web UI and CLI only send
   requests; the daemon validates and decides. If the UI dies, fans do not change.
2. **Never write `pwmN` without `pwmN_enable` in manual mode first** (profile knows how).
3. **Every write is read back and verified.** Mismatch → write error path.
4. **Any failure that leaves a channel's state unknown → that channel goes to 255.**
5. **Critical temperature → 255 immediately, no slew, even in manual override.**
6. **Stall (RPM 0 at duty ≥ stall_min_duty for N cycles) → 255, alert, auto-recovery.**
7. **On stop: profile-defined safe state** (N5 Pro: CPU/SSD → EC auto, HDD → fixed stop duty,
   because the EC no longer regulates the HDD channel after a write — measured 14.09.2026).
8. **Config errors never prevent start**: invalid values → built-in defaults + warning + alert.
9. Go 1.25, `CGO_ENABLED=0`, dependencies: `github.com/BurntSushi/toml` only. No cgo, no
   systemd library (sd_notify is a 20-line unix datagram write).
10. `/sys` root is overridable via `N5FANGOV_SYSFS` (tests use a fake tree under `testdata/`).
11. All user-facing strings English. Comments English. Logs to stdout (journald).

## Layout

```
cmd/n5-fangov/main.go          entry: subcommands serve|status|set|auto|curve|log|check|detect|version
cmd/n5-fangov/wiring.go        the only file in cmd that calls into internal/* (adapters, stores)
internal/config/               TOML config: load, validate (defaults on error), write, presets
internal/hwmon/                sysfs discovery + read/write helpers (root from N5FANGOV_SYSFS)
internal/profile/              Profile interface + n5pro, nct67xx, it87xx, monitor; Detect()
internal/sensor/               sensor sources: k10temp, coretemp, nvme:max, drivetemp:max, ec:*, hwmon:<name>:tempN
internal/control/              controller loop: curve, slew, override, critical, stall, plausibility, failsafe, history ring
internal/alert/                alert sink: PVE::Notify (perl) → mail(1) → log; cooldown per type
internal/ipc/                  unix socket server/client for CLI (same HTTP mux as web, no auth)
internal/web/                  HTTP API, basic auth, CSRF; embeds static/ via go:embed
internal/web/static/           index.html, app.js, app.css (no build step, vanilla JS, canvas charts)
internal/sdnotify/             READY=1, WATCHDOG=1
internal/version/              version string
deploy/                        systemd unit, onfailure unit, failsafe script, postinst, config example
tools/                         remote-go.ps1 (build/test via Docker on a Linux host over ssh)
testdata/sysfs/n5pro/          fake /sys tree mirroring the N5 Pro test host (hwmon names, temp/fan/pwm files)
```

## Config (`/etc/n5-fangov/config.toml`)

```toml
[daemon]
interval = "10s"        # 2s..30s; above 30s clamped with a warning (WatchdogSec=60: two cycles must fit)
step_up = 40            # max duty increase per cycle (1..255)
step_down = 15          # max duty decrease per cycle (1..255)
stall_min_duty = 60     # RPM 0 at or above this duty counts as stall
stall_cycles = 2
stale_cycles = 18       # identical raw sensor value this many cycles → sensor frozen (6..600; only checked when the first channel's sensor is k10temp)
alert_cooldown = "30m"  # 60s..24h
log_every = 30          # status line every N cycles (0 = never)
profile = "auto"        # auto | n5pro | nct67xx | it87xx | monitor

[web]
listen = "127.0.0.1:8010"
auth = "none"           # none | basic
user = ""
password_hash = ""      # bcrypt not available without deps → sha256 hex of "user:password" (documented limitation)
allowed_hosts = []      # extra Host header values (reverse-proxy names); IP literals, localhost and the listen host always pass; "*" disables the check

[[channel]]
name = "cpu"            # unique, [a-z0-9_]
pwm = 1                 # pwmN index in the profile's hwmon device
sensor = "k10temp"      # see sensor sources
curve = [[45,85],[80,255]]   # points [temp_c, duty], ascending temp, 2..8 points, linear between
critical = 88           # temp → 255 immediately
stop = "auto"           # "auto" (profile returns channel to EC/BIOS) or fixed duty 60..255; default "140" for drivetemp:max; n5pro pwm3 never "auto"
```

Presets: `/etc/n5-fangov/presets/<name>.toml` containing only `[[channel]]` tables.
Runtime state: `/run/n5-fangov/` (state.json, overrides, alert stamps). Unix socket:
`/run/n5-fangov/n5-fangov.sock`.

## Profile interface (internal/profile)

```go
type Channel struct { Index int; Label string; HasTach bool }
type Profile interface {
    Name() string                      // "n5pro"
    Title() string                     // "Minisforum N5 Pro (IT5571 EC)"
    Verified() bool                    // true only for n5pro
    Notes() string
    Detect(fs *hwmon.FS) (Device, error)      // find device, or error
}
type Device interface {
    Profile() Profile
    HwmonPath() string
    Channels() []Channel
    ReadRPM(ch int) (int, error)
    ReadDuty(ch int) (int, error)
    EnterManual(ch int) error           // pwmN_enable=1 (n5pro: driver sets 255 internally)
    WriteDuty(ch int, duty int) error   // write + read back verify
    SafeStop(ch int, stop string) error // "auto" → profile default (n5pro: enable=2), else fixed duty
    ExtraTemps() map[string]string      // e.g. ec:cpu → path (n5pro exposes temp1..4)
}
```

n5pro detection: hwmon name `minisforum_n5_it5571` (DMI check optional, log only).
nct67xx: hwmon name matches `^nct67\d\d$`; auto mode value 5 (SmartFan IV) — restore
original `pwmN_enable` value captured at start. it87xx: `^it8[67]\d\d$`; restore original.
monitor: no pwm; only sensors.

## Sensor sources (internal/sensor)

`k10temp` (Tctl = temp1), `coretemp` (max of tempN), `nvme:max`, `drivetemp:max`,
`hwmon:<name>:tempN`, `ec:<label>` (profile ExtraTemps). Values in millidegrees;
plausibility −20000..120000. `max` sources ignore devices that vanished if at least
one remains; zero devices → error.

## Controller (internal/control)

Port of the Bash `n5-fand` logic (n5pro-ec/deploy/n5-fand):
per cycle: sd_notify WATCHDOG=1 → read sensors (per channel: unresolved / unreadable /
implausible / frozen → **that channel** at its safe duty, mode `sensor-error`, alert
`sensor` naming the channel; the others keep regulating)
→ targets from curve (interpolate) → overrides → critical → stall check → slew (first
cycle direct; manual direct) → write (rewrite unchanged every 6th cycle) → verify →
state snapshot → history ring (2h at interval) → periodic log line.
Safe duty of a channel = its fixed stop duty when configured (N5 Pro HDD: 140), else 255;
overrides do not apply while the temperature is unknown. Status becomes `sensor-error`
only when every channel is affected. The `sensor` alert goes out on the transition
into the bad state (cooldown applies), not every cycle — a sensor that is simply absent
(no HDDs → no `drivetemp`) is one notification, and `n5-fangov status` shows the mode.
Failsafe (write errors) = duty 255 on all managed channels. Stop = SafeStop per channel.
Overrides via `Controller.SetOverride(ch, duty)` / `ClearOverride(ch)` (from API).
Config reload: `Controller.Reload(cfg)` swaps curves/sensors atomically; channel set
changes require restart (return error).

State snapshot (JSON, also written to /run/n5-fangov/state.json):
```json
{"ts":1789500000,"status":"ok","profile":"n5pro","verified":true,
 "channels":[{"name":"cpu","pwm":1,"temp":36.0,"duty":85,"rpm":2000,"mode":"auto","target":85}],
 "extra_temps":{"ec:system":32.0},"alerts":{"stall":1789490000}}
```

## HTTP API (internal/web) — served on TCP (web) and unix socket (CLI)

```
GET  /api/state                          → snapshot (see above)
GET  /api/history?minutes=120&since=TS   → [{ts, temp{}, duty{}, rpm{}}] maps by channel name; since (unix s, optional)
                                           returns only points with ts > since (incremental polling)
GET  /api/config                         → {"raw": "<toml>", "config": {daemon{}, web{}, channel[], warnings[]}}
                                           password_hash is "<unchanged>" in both when set
PUT  /api/config                         → body: raw TOML; "<unchanged>" restores the stored hash; validate
                                           (syntax error → 400 {"error","errors":[...]}, nothing written) → write file
                                           → reload: 200 {"ok","restart_required":false,"warnings":[...]} or
                                           202 {"restart_required":true} when the channel set/profile changed
PUT  /api/override/{name}                → {"duty":191} or {"percent":75} (0..100 → duty rounded);
                                           200 {"ok","channel","duty","mode"} — mode is the channel's mode in the
                                           current cycle (the override applies on the next one; critical/stall win)
DELETE /api/override/{name}              → back to curve, 200 {"ok","channel","mode"}
GET  /api/presets                        → [{"name","channels":[<channel names>]}]
POST /api/presets/{name}/apply           → 200 {"ok","applied"} or 202 {"restart_required":true}
PUT  /api/presets/{name}                 → empty body; saves the current [[channel]] tables; 200 {"ok","saved"}
                                           name: ^[a-z0-9_-]{1,64}$ (same rule as config)
GET  /api/sensors                        → [{"id","description","temp"?}] temp = live reading in °C when readable
GET  /api/log?lines=100                  → {"lines": ["..."]} (journalctl -u n5-fangov, newest last)
GET  /api/profiles                       → [{"name","title","verified","notes","active"}]
GET  /api/version                        → {"name":"n5-fangov","version":"..."}
```
Errors are `{"error": "..."}`; PUT /api/config adds `"errors": [...]` (parse warnings).
413 on bodies over 256 KiB (config) / 4 KiB (override).

TCP handler, in order: Host header must be an IP literal, `localhost`, the listen host
or an `allowed_hosts` entry (else 421, DNS-rebinding guard); state-changing methods
need `X-N5-Fangov-Csrf: 1` (else 403); with `auth=basic`, state-changing methods plus
`GET /api/config` and `GET /api/log` need basic auth (else 401, no challenge header;
failures throttled per IP: 5 free, then 250 ms doubling to 2 s, reset after 10 min).
Unix socket handler: none of the three checks; the hash is redacted there as well.
Startup logs a warning when the TCP listener is non-loopback and auth is none.

## CLI (cmd/n5-fangov)

```
n5-fangov serve [--config PATH] [--dry-run]
n5-fangov status             table like n5fan status
n5-fangov set <ch> <duty|NN%>  ; n5-fangov auto <ch|all>
n5-fangov curve              ; n5-fangov log [n] ; n5-fangov check ; n5-fangov detect ; n5-fangov version
n5-fangov test <ch>          channel verification run (like n5pro-ec 06 script), refuses while serve is regulating that channel unless --force;
                             restores the channel with the stop of the sanitized channel set (n5pro pwm3 never "auto"), unowned pwm → "auto"
```

`n5-fangov check` (ExecStartPre) follows rule 8: **fatal** (exit 1) only when serve itself
cannot run — config file exists but is unreadable, no device for the profile, a `pwmN`/
`pwmN_enable` of a managed channel does not open for writing. Everything else is a
`warn` line with exit 0: missing config, TOML syntax error (serve uses the defaults),
invalid values, a pwm the profile lacks (serve ignores the channel), an unresolvable or
unreadable sensor (serve isolates the channel at its safe duty — this includes the
built-in N5 Pro channels, so a box without HDDs starts), and a non-loopback `[web].listen`
with `auth = "none"`. check evaluates the **sanitized** channel set (same
`control.SanitizeChannels` as serve).

## Web UI (internal/web/static)

Tabs: Overview (cards per channel, live canvas charts, hardware details, last alerts),
Curves (points editor + chart per channel, sensor dropdown, critical, apply),
Manual (slider + auto toggle per channel), Presets, Log, Compatibility (profiles with
verified flag). Dark theme, layout inspired by ProxFansX; no framework; fetch + canvas.

## Deploy

`deploy/n5-fangov.service`: Type=notify, NotifyAccess=main, WatchdogSec=60,
ExecStartPre=/usr/bin/n5-fangov check --quiet, ExecStopPost=/usr/bin/n5-fangov failsafe,
Restart=always, RestartSec=5, StartLimitBurst=5, OnFailure=n5-fangov-onfailure.service,
RuntimeDirectory=n5-fangov, RuntimeDirectoryMode=0750 (the socket carries no auth).
`n5-fangov failsafe` = SafeStop all channels using the config,
works without the daemon.

Watchdog: the loop sends WATCHDOG=1 at the start of every cycle; in addition serve
pings every 10 s from a ticker **only while the loop is alive** (`Controller.LastCycle()`
within 3×interval). `daemon.interval` is capped at 30 s so two cycles always fit into
WatchdogSec=60; a stuck loop silences the ticker and the watchdog fires as intended.

`n5-fangov-onfailure` reads Result/ExecMainCode/ExecMainStatus first (they describe the
new process once the restart began), sleeps 8 s (RestartSec=5), then polls `is-active`
for up to 25 s while the unit is `activating` (ExecStartPre check + first cycle before
READY=1). `active` → "restart" alert, still `activating` → "restart in progress" alert
(same kind), anything else → "failed" alert. Cooldown 30 min per kind via
`/run/n5-fangov/alert.<kind>`.

## Testing

`go vet ./... && go test ./...` must pass in Docker (`tools/remote-go.ps1`). Fake sysfs
under `testdata/sysfs/n5pro` mirrors the N5 Pro test host. Controller tests use a fake Device.

## Integration notes (15.09.2026)

Deviations between this contract and the merged packages, as found while wiring
`cmd/n5-fangov`. The code is the reference; this list says where the text above
is loose.

- `config.Default()` has **no channels**; the N5 Pro set is `config.N5ProChannels()`.
  A missing config file therefore starts the daemon in monitoring-only mode (rule 8) —
  **except on the N5 Pro**: `control.SanitizeChannels` adds pwm1..3 from `N5ProChannels()`
  when the config lacks them and forces `stop=140` on pwm3 (warning + `config-channels`
  alert, together with channels dropped for a pwm the device lacks), in `control.New`,
  `Apply` and `control.Failsafe` alike. A channel the daemon wrote once and then ignored
  would stay at its last duty (the EC does not regulate pwm3 after a write). The alert
  kind is deliberately not `config`: serve stamps `config` for parse warnings a moment
  earlier and the shared 30-min stamp would swallow the controller's text.
- `config.Load` returns `(Config, []Warning, error)`: missing file → defaults + one
  warning + `err == nil`; read error → defaults + warning + `err` wrapping
  `config.ErrUnreadable`; TOML syntax error → defaults + warning + `err`.
  `config.Parse` also returns an error (syntax only). `Warning` is `{Field, Msg}`.
- `hwmon.FS.FindByName` returns `[]Device` (may be empty), not `(Device, error)`.
- `sensor.Known` returns `[]sensor.Info{ID, Description}`, not `[]string`; the
  description carries the live reading and the list ends with the two generic
  patterns (`hwmon:<name>:tempN`, `ec:<label>`) which do not parse as ids.
  `hwmon:<name>:tempN` always binds to the first device of that name (on the reference host
  `hwmon:nvme:temp1` is one SSD, `nvme:max` is the hottest).
- `control.SensorFactory` returns `control.SensorReader`; `sensor.Source` satisfies
  it but the func types differ, so wiring adapts (nil-interface safe).
- `control.Options.Notify`/`Status` default to sd_notify already; wiring sets them
  explicitly. `Run(ctx)` calls `Stop()` itself (also after a recovered panic, which
  Run returns as an error); `Stop()` is idempotent (sync.Once), takes only the
  hardware mutex (never the loop mutex) and makes a running cycle skip its writes.
  serve waits for `Run` to return (15 s cap) before calling `Stop` again.
  `Run` returns `control.ErrDeviceLost` after 6 consecutive cycles in which the
  failsafe could not write a single channel (driver reload → hwmonN renumbered);
  serve exits 1, systemd restarts (ExecStopPost failsafe, fresh detection).
- The controller's initial snapshot already has `ts > 0` with `status: "starting"`;
  READY=1 waits for `status != "starting"` (or 2×interval+5 s).
- In dry-run the snapshot `status` is `"dry-run"`, not `"ok"`. Duty in the
  snapshot is the hardware `pwmN` value (`ReadDuty`, refreshed every cycle); the
  computed value is `target`. In normal mode duty starts from `ReadDuty` as well and
  is `-1` while unknown (a write failed; the channel is rewritten next cycle).
- `alert.New(logger)` returns `alert.Sink` (`Alert(kind, msg)`, `Name()`); the
  controller's `Alerter` interface is satisfied by it. Cooldown lives in the
  controller (stamps `RunDir/alert.<kind>`); `serve` routes its start-up alerts
  (config/profile/start) through `sendAlertCooled`, which uses the same stamp files
  (30 min), so a restart loop cannot spam PVE. `n5-fangov alert` (onfailure) has no cooldown.
  `GET /api/config` redacts `password_hash` in both quote styles, with any spacing, in
  the dotted `web.password_hash` form, and — via the parsed value — wherever else a
  hash of 32+ characters appears in the text (inline table); `PUT` restores the stored
  hash for the `<unchanged>` placeholder in the same forms.
- `web.Deps` takes closures (`Profiles func() []ProfileInfo`, `Sensors func()
  []SensorInfo`, `Log func(int) ([]string, error)`), `AuthConfig` (not `Auth`),
  and `PresetStore{List() ([]Preset, error); Apply(name) error; Save(name) error}`
  where `Preset.Channels` is a list of channel **names**. `web.New` yields two
  handlers: `Handler()` (TCP, CSRF + auth) and `SocketHandler()` (unix socket,
  none); the socket must not get the TCP handler or the CLI's PUT/DELETE fail 403.
- `GET /api/log` answers `{"lines": [...]}`, `GET /api/config` answers
  `{"raw": "...", "config": {...}}` where `config` mirrors the TOML layout
  (lowercase keys, durations as strings, plus `warnings`).
- `web.Deps` also takes `Validate func([]byte) ([]string, error)` (wired to
  `config.Parse`; PUT /api/config refuses on error before anything is written),
  `AllowedHosts []string` (listen host + `[web].allowed_hosts`) and `Logf`.
  `SensorInfo.Temp *float64` is filled by wiring via `sensor.Parse(...).Read()`.
- `[web]` is read once at start; `PUT /api/config` reloads daemon/channel values
  only. Auth, listen and allowed_hosts changes need a restart.
- `config.Parse` forces `listen` to loopback when auth is misconfigured (H2);
  `config.IsLoopbackListen` is the shared predicate.
- `ipc.Listen` sets umask 0117 around `net.Listen` (process-wide for a few
  microseconds; a state.json written concurrently in that window gets 0660
  instead of 0644 — harmless). Build tags: `umask_unix.go` / `umask_other.go`.
- Presets: `Apply` loads `<dir>/<name>.toml`, replaces `Channels` of the parsed
  config **file**, marshals, saves and calls `Service.Reload` (which returns
  `ErrRestartRequired` when names/pwm changed → HTTP 202). The rewrite drops
  comments from the config file. `Save` stores the channels of the config file.
- CLI socket/run-dir override: `N5FANGOV_RUN_DIR` (default `/run/n5-fangov`) or
  `serve --run-dir`; there is no separate socket variable.
- `n5-fangov check` opens `pwmN`/`pwmN_enable` O_WRONLY without writing (permission
  probe); everything else in `check`/`detect` is read-only.

## v0.2 contract — setup, TLS, hardening, logs, settings bundle

Ownership: **WEB builder** owns internal/web, internal/web/static, internal/tlscert (new).
**CMD builder** owns cmd/n5-fangov, internal/config, internal/logfile (new), deploy/, README.

### Config additions (internal/config)

```toml
[web]
tls = "auto"            # auto | off | file. Default: "auto" when listen is non-loopback, "off" on loopback.
                        # Non-loopback + "off" → warning, forced to "auto" (LAN traffic is never plain HTTP).
cert_file = ""          # tls = "file": PEM paths
key_file = ""
[log]
file = "/var/log/n5-fangov/n5-fangov.log"   # "" disables the file (journal only)
max_size_mb = 5
max_files = 5
```

### internal/tlscert (WEB builder)

```go
type Options struct { Dir string; Hosts []string /* SANs: IPs + DNS names */; Org string }
func EnsureAuto(o Options) (tls.Certificate, string /*cert path*/, error)  // creates ECDSA P-256 self-signed (10y) if missing; reuses existing; regenerates when SANs changed
func LoadFiles(certFile, keyFile string) (tls.Certificate, error)
func ExportPEM(dir string) ([]byte, error)                                 // certificate only
func Regenerate(o Options) (tls.Certificate, error)
```
(v0.3 adds Store, Info, ValidatePair, Reissue — see "v0.3 contract".)
`web.Server.ServeTLS(ctx, ln net.Listener, cert tls.Certificate) error` (min TLS 1.2, modern ciphers,
HSTS header when TLS). Existing `Serve` stays for plain HTTP.

### Log store (interface in internal/web, implemented by internal/logfile)

```go
type LogStore interface {
    Lines(n int) ([]string, error)   // newest n lines of the file (falls back to journal when file disabled)
    Export(w io.Writer) error        // whole current file
    Clear() error                    // truncate current file (rotated files untouched); journal untouched
    Path() string
}
```
API: `GET /api/log?lines=N` (unchanged shape `{"lines":[...],"source":"file"|"journal"}`),
`GET /api/log/export` (text/plain attachment `n5-fangov-<host>-<ts>.log`),
`DELETE /api/log` (auth + CSRF; response `{"cleared":true,"note":"journal untouched"}`).
`internal/logfile.New(path, maxSizeMB, maxFiles) (*Writer, error)` — io.Writer with size rotation
(`.1`..`.N`), used by serve as `io.MultiWriter(os.Stdout, file)` for the standard logger.

### Settings bundle (interface in internal/web, implemented in cmd wiring)

```go
type Bundle interface {
    Export() ([]byte, error)                     // JSON {"format":1,"version":..,"exported":ts,"config":rawTOML,"presets":{name:rawTOML}}
    Import(b []byte) (restartRequired bool, err error)   // validate ALL parts (config.Parse, preset parse) before writing anything; password_hash "<unchanged>" keeps current
}
```
API: `GET /api/config/export` (application/json attachment `n5-fangov-settings-<ts>.json`, hash
redacted to `<unchanged>`), `POST /api/config/import` (auth+CSRF; 200 / 202 restart_required /
400 with errors). CLI: `n5-fangov export [FILE]`, `n5-fangov import FILE`.

### Auth logging

Log a failed basic-auth attempt only when an `Authorization` header was presented. Anonymous
401s (UI before login) are not failures and must not count towards the rate limit.

### CLI additions (CMD builder)

```
n5-fangov setup [--listen local|lan] [--user U] [--password P] [--yes]   interactive when flags missing:
      detect profile → write /etc/n5-fangov/config.toml from N5ProChannels() (or generic from detected pwm)
      → scope local/lan → admin user+password (hash computed here) → tls auto for lan → enable+start hint
n5-fangov passwd [--user U]        set/replace web password (prompts twice, no echo)
n5-fangov cert export [FILE]       PEM of the auto cert (to trust in browser/OS);  cert regen
n5-fangov export [FILE] / import FILE
n5-fangov log [-n N] [--export FILE] [--clear]
n5-fangov check --after-update     DKMS module present for EVERY installed kernel (/lib/modules/*/updates/dkms/minisforum_n5_it5571.ko or dkms status per kernel); missing → non-zero, alert "kernel" (cooldown), printed for apt
```

### Deploy (CMD builder)

- Unit hardening (must be verified on real hardware — `/sys` writes need `ProtectKernelTunables=no`):
  `NoNewPrivileges=yes`, `ProtectSystem=strict`, `ReadWritePaths=-/etc/n5-fangov -/run/n5-fangov -/var/log/n5-fangov -/sys/class/hwmon -/sys/devices -/var/spool/postfix/maildrop`
  (`-` = missing path does not fail the start), `ProtectHome=yes`, `PrivateTmp=yes`, `PrivateDevices=yes`,
  `ProtectKernelTunables=no`, `ProtectControlGroups=yes`,
  `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK` (netlink: `net.Interfaces()`), `RestrictNamespaces=yes`,
  `LockPersonality=yes`, `MemoryDenyWriteExecute=yes`, `RestrictRealtime=yes`, `SystemCallArchitectures=native`,
  `SystemCallFilter=@system-service`, `CapabilityBoundingSet=` (empty: the daemon never loads modules or chowns;
  the module comes from modules-load.d), `UMask=0077`, `LogsDirectory=n5-fangov`, `LogsDirectoryMode=0750`.
  `make verify-deploy` runs `systemd-analyze verify` / `apt-config` over the deploy files where available.
- `[log].file` must be a clean absolute path under `/var/log/` (`N5FANGOV_LOG_ROOT` moves the root for tests);
  `logfile.New` refuses an existing target that is not a regular file (symlink, device, directory) and
  opens with `O_NOFOLLOW`. `Lines`/`Export` hold the writer lock only to open the file, never while reading.
- `[web].password_hash` is either the legacy `sha256("user:password")` hex or
  `pbkdf2$<iter>$<salt hex>$<key hex>` (PBKDF2-HMAC-SHA256, 210000 iterations, 16-byte salt); `setup`/`passwd`
  write the latter, `web.VerifyPassword` accepts both. Failed logins: 5 free, then 250 ms doubling to 2 s,
  and at most 4 delayed attempts per IP in flight — further ones get 429 without a hash computation.
- Auto certificate: name constraints (critical, exactly the SANs; IPs as /32 or /128) and `MaxPathLen 0`;
  a regeneration caused by a SAN change keeps the private key. For an unspecified listen the SANs are the
  host name plus the primary IPv4/IPv6 (route to 1.1.1.1 / 2606:4700::1111), not every interface address.
- `check --after-update` alerts only for kernels the box can boot into (running kernel + `proxmox-boot-tool
  kernel list` selection; without the tool: running + newest installed); other installed kernels are info lines.
- apt hook `/etc/apt/apt.conf.d/90n5-fangov`: `DPkg::Post-Invoke { "if [ -x /usr/bin/n5-fangov ]; then /usr/bin/n5-fangov check --after-update || true; fi"; };`
- install.sh: creates log dir, installs hook, ends with "run: n5-fangov setup". uninstall.sh removes hook, keeps /var/log unless --purge. deb: same via postinst/postrm.

### v0.2 integration notes (15.09.2026)

Where the merged v0.2 packages deviate from the contract text above. Code is the reference.

- `web.Deps.Log` is `any`, not `LogStore`: web accepts a `LogStore` or — for one release —
  the pre-v0.2 `func(int) ([]string, error)` (adapted to a journal-only store: `source:
  "journal"`, export = newest 100000 lines, `DELETE /api/log` → 501). Any other type is
  logged once at `web.New` and the log endpoints answer 501. cmd passes its `logStore`
  (`*logfile.Writer` or `journalLogStore`), which has the same method set.
- `tlscert.Options` has a fourth member `Logf` (nil → `log.Printf`); cmd passes `log.Printf`.
  The automatic certificate is marked **IsCA** with `KeyUsageCertSign` (self-signed +
  BasicConstraints CA=true): browsers and OS stores accept a self-signed leaf as a trust
  anchor only in that form. It is still one certificate that serves directly; no chain.
  SANs always include `localhost`, `127.0.0.1`, `::1`; `EnsureAuto` regenerates when the
  requested SANs are not covered, when expired, or when the pair does not load.
- Bundle import errors: cmd's `fileBundle.Import` returns `*bundleError` whose `Error()`
  joins with `"; "` and whose `Errors() []string` holds the items. web extracts the list
  via `errors.As` on `interface{ Errors() []string }` and falls back to splitting
  `Error()` at newlines; `{"error": "import rejected: <first>", "errors": [...]}`.
- UI downloads (`/api/log/export`, `/api/config/export`) go through `fetch` + blob anchor,
  not a plain `<a href>`: the in-memory basic-auth credential rides along and the CSP stays
  strict. Curl/wget get the same attachments with `Content-Disposition`.
- No dpkg conffile: `/etc/n5-fangov/config.toml` is written by `n5-fangov setup` only,
  never by install.sh/postinst; an existing file is left alone (setup backs it up first).
- `cmd/n5-fangov/wiring_v2.go` is merged (build tag and `wiring_v2_stub.go` removed); it and
  `tlsmgr.go` (v0.3 certificate manager) are the cmd files that import `internal/tlscert`.
## v0.3 contract — certificate management in the dashboard (16.09.2026)

The operator sees the active certificate, downloads it to trust it, regenerates it or
uploads an own pair — without a restart. Ownership as in v0.2: internal/tlscert and
internal/web are the WEB side, the manager and the CLI live in cmd.

### internal/tlscert additions

```go
type Store struct{ /* atomic *tls.Certificate */ }
func NewStore(cert tls.Certificate) *Store
func (s *Store) Get(*tls.ClientHelloInfo) (*tls.Certificate, error)  // tls.Config.GetCertificate
func (s *Store) Set(cert tls.Certificate)                            // next handshake; open connections untouched
func (s *Store) Current() tls.Certificate

type InfoData struct {
    Subject, Issuer string; DNSNames, IPs []string; NotBefore, NotAfter time.Time
    FingerprintSHA256 string /* colon-hex, upper */; IsCA bool; KeyAlgo string /* "ECDSA P-256" */; SerialHex string
}
func Info(cert tls.Certificate) InfoData
func ValidatePair(certPEM, keyPEM []byte, hosts []string) (tls.Certificate, []string /*warnings*/, error)
    // errors: no PEM block, key does not match, expired, not yet valid
    // warnings: SAN list lacks host <h> (per host), expires in N days (< 30), no SANs, weak key (RSA < 2048), SHA-1/MD5 signature
func Reissue(o Options) (tls.Certificate, error)   // new certificate, key from o.Dir kept (Regenerate: new key)
func PEM(cert) / DER(cert) ([]byte, error)         // leaf only
func WritePrivate(path string, data []byte) error  // 0600, temp file + rename
```

`web.Server.ServeTLSStore(ctx, ln, *tlscert.Store)` is the listener; `ServeTLS(ctx, ln, cert)`
wraps it with a store that is never swapped.

### TLSMgr (interface in internal/web, implemented by cmd `tlsManager`)

```go
type TLSMgr interface {
    Info() (tlscert.InfoData, mode string, err error)      // mode: auto | file | off
    ExportPEM() ([]byte, error)
    ExportDER() ([]byte, error)
    Regenerate(keepKey bool) (tlscert.InfoData, error)     // auto only; file → web.ErrTLSFileMode (409)
    Upload(certPEM, keyPEM []byte) (tlscert.InfoData, []string, error)  // validation failure → web.ValidationError (400)
    ResetAuto() (tlscert.InfoData, error)
}
```
`web.Deps.TLSMgr` (nil → 501) and `web.Deps.TLSHosts` (the SAN hosts, reported by GET).
Manager semantics (cmd): the tls directory is `<config dir>/tls`; the auto pair stays
`cert.pem`/`key.pem`, an upload lands as `custom-cert.pem`/`custom-key.pem` (0600) and
sets `[web] tls = "file"`, `cert_file`, `key_file` via `config.SetKey` (comments kept),
config first, then `Store.Set`. ResetAuto: `EnsureAuto` (reuses the auto pair), config
`tls = "auto"` with empty paths, custom files deleted. Mode `off` (also: no TCP listener)
→ `web.ErrTLSOff` from every method except Info. Regenerate/Upload/ResetAuto serialise
on a mutex; the store swap is the last step, so a failed write never changes what is served.

### API

```
GET  /api/tls                 → {"mode":"auto|file|off","info":{InfoData}|null,"hosts":[...]}   public
GET  /api/tls/cert.crt        → application/x-pem-file, attachment n5-fangov-<host>.crt        public
GET  /api/tls/cert.cer        → application/pkix-cert, attachment n5-fangov-<host>.cer          public
POST /api/tls/regenerate      body {"keep_key":true} (default true, empty body ok)             auth+CSRF
                              → {"ok","keep_key","info"} + "warning" when keep_key=false
POST /api/tls/upload          multipart parts cert/key (file or field) or JSON {"cert","key"}; 64 KiB   auth+CSRF
                              → {"ok","mode":"file","info","warnings":[...]}; 400 on a pair that does not validate
POST /api/tls/reset           → {"ok","mode":"auto","info"}                                     auth+CSRF
```
Mode `off`: everything except `GET /api/tls` answers `409 {"error":"tls is off"}`. Each
state change logs `web: tls <regenerate|upload|reset> by <ip>`.

### UI

Header lock (`#h-sec`, now a button) — tooltip carries mode and expiry, click opens the
`<dialog id="cert">` (also *Settings → Certificate…*): mode badge, subject/issuer, SAN
chips, validity (highlight < 30 days), fingerprint + copy, Download .crt/.cer,
Regenerate… (inline confirm with "generate a new key" checkbox), Upload own
certificate… (two file inputs that fill two PEM textareas; warnings shown), Back to auto
(mode file), collapsible "How to trust this certificate" (Windows/macOS/Firefox/Android).
In file mode a notice lists listen hosts the certificate does not cover. Mock:
`?mock=1&tls=off|file|soon`. JS budget raised to 52 KB (the panel markup lives in
index.html, the JS only binds data).

### CLI

```
n5-fangov cert info | export [--der] [FILE] | regen [--new-key] | upload CERT KEY | reset
```
Socket API when the daemon answers (hot swap), else the same `tlsManager` on the files
plus a restart hint. `cert regen` keeps the key by default (old behaviour was a new key).
