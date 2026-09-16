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
    // PEM blocks are iterated: every CERTIFICATE block (leaf first, chain kept), the first *PRIVATE KEY block
    // (PKCS#8, PKCS#1, SEC 1; EC PARAMETERS etc. skipped); ENCRYPTED PRIVATE KEY / Proc-Type: 4,ENCRYPTED → ErrEncryptedKey
    // errors: no PEM block, encrypted key, key type crypto/tls cannot sign with (ECDSA ∉ {P-256,P-384,P-521}, RSA < 1024),
    //         key does not match, expired, not yet valid, CheckUsable failed ("certificate/key cannot be used by this server: …")
    // warnings: SAN list lacks host <h> (per host), expires in N days (< 30, DaysLeft = ceil), no SANs, weak key (RSA < 2048), SHA-1/MD5 signature
func ServerConfig(getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config
    // the listener's tls.Config: TLS 1.2+, AEAD suites, h2, SessionTicketsDisabled (a swap is what every new connection sees)
func CheckUsable(cert tls.Certificate) error        // one in-process handshake over net.Pipe against ServerConfig
func Warnings(info InfoData, hosts []string, now time.Time) []string   // GET /api/tls: expiry, no SANs, MissingHosts (loopback excluded)
func MissingHosts(info InfoData, hosts []string) []string
func DaysLeft(notAfter, now time.Time) int          // ceil; the UI uses the same rounding
func Reissue(o Options) (tls.Certificate, kept bool, error)  // new certificate, key from o.Dir kept; kept=false when it was not loadable (new pair)
func PEM(cert) / DER(cert) ([]byte, error)         // leaf only
func WritePrivate(path string, data []byte) error  // 0600, temp file + rename
```

`web.Server.ServeTLSStore(ctx, ln, *tlscert.Store)` is the listener, built on
`tlscert.ServerConfig(store.Get)` — the same config ValidatePair handshakes against, so a
pair that validates is a pair the listener can serve; `ServeTLS(ctx, ln, cert)` wraps it
with a store that is never swapped.

### TLSMgr (interface in internal/web, implemented by cmd `tlsManager`)

```go
type TLSMgr interface {
    Info() (tlscert.InfoData, mode string, err error)      // mode: auto | file | off | "auto (fallback from file)"
    ExportPEM() ([]byte, error)
    ExportDER() ([]byte, error)
    Regenerate(keepKey bool) (tlscert.InfoData, kept bool, error)  // auto only; file → web.ErrTLSFileMode (409); kept=false: new pair despite keepKey
    Upload(certPEM, keyPEM []byte) (tlscert.InfoData, []string, error)  // validation failure → web.ValidationError (400)
    ResetAuto() (tlscert.InfoData, error)
}
type TLSFallback interface{ Fallback() bool }               // optional; GET /api/tls "fallback"
```
`web.Deps.TLSMgr` (nil → 501) and `web.Deps.TLSHosts` (the SAN hosts, reported by GET).
Manager semantics (cmd): the tls directory is `<config dir>/tls` (config path made
absolute); the auto pair stays `cert.pem`/`key.pem`, an upload lands as
`custom-cert.pem`/`custom-key.pem` (0600) and sets `[web] tls = "file"`, `cert_file`,
`key_file` via `config.SetKey` (comments kept), config first, then `Store.Set`. A config
write that fails after the custom files were written restores them (previous content or
absent) so the file system never disagrees with the config. ResetAuto: `EnsureAuto`
(reuses the auto pair), config `tls = "auto"` with empty paths, custom files deleted; a
`cert_file`/`key_file` outside the tls directory is left alone. Mode `off` (also: no TCP
listener) → `web.ErrTLSOff` from every method except Info. Regenerate/Upload/ResetAuto
serialise on a mutex; every certificate passes `tlscert.CheckUsable` before it reaches
the store, so a failed write or an unusable pair never changes what is served.

**The manager owns the three `[web]` tls keys.** Every config text written through the
API — `PUT /api/config` (`fileConfigStore`), settings import (`fileBundle`), preset apply
(`dirPresetStore`) — goes through `tlsManager.pinConfig` (cmd wiring, `webDeps.ConfigPin`),
which re-applies the manager's mode and paths; a stale copy of the file in the curve
editor cannot revert an upload or a reset. Text that already yields the same values is
left byte-for-byte. Not applied while the effective mode came from a `--listen` override
or there is no TCP listener (`ownsConfig=false`).

**Fallback (serve):** `tls = "file"` whose pair cannot be loaded or served does not
disable the listener: `loadForServe` ensures the auto pair, serves it, logs the reason and
sends the cooled alert `tls` ("custom certificate unreadable, serving the automatic
certificate"). The config keeps `tls = "file"` and its paths; Info reports mode
`auto (fallback from file)`, `Fallback()` is true, Regenerate is allowed (it is the auto
pair being served), Upload or ResetAuto end the fallback. The CLI offline path
(`cert reset|upload|regen` without a daemon) tolerates an unloadable pair — it prints a
note and repairs; `cert info|export` need a loaded pair.

### API

```
GET  /api/tls                 → {"mode":"auto|file|off|auto (fallback from file)","info":{InfoData}|null,"hosts":[...],
                                 "warnings":[...],"fallback":bool}                                   public
                                 warnings = tlscert.Warnings(info, TLSHosts): expiry, no SANs, uncovered hosts (loopback excluded)
GET  /api/tls/cert.crt        → application/x-pem-file, attachment n5-fangov-<host>.crt        public
GET  /api/tls/cert.cer        → application/pkix-cert, attachment n5-fangov-<host>.cer          public
POST /api/tls/regenerate      body {"keep_key":true} (default true, empty body ok)             auth+CSRF
                              → {"ok","keep_key","kept","info"} + "warning" when keep_key=false or kept=false
POST /api/tls/upload          multipart parts cert/key (file or field) [+ force] or JSON {"cert","key","force"?}; 64 KiB   auth+CSRF
                              → {"ok","mode":"file","info","warnings":[...]}; 400 on a pair that does not validate;
                                400 {"error","host","force_required":true} when the request came over TLS and the leaf
                                does not cover the name the client used (SNI, else Host) — HSTS lock-out guard; force=true overrides
POST /api/tls/reset           → {"ok","mode":"auto","info"}                                     auth+CSRF
```
Mode `off`: everything except `GET /api/tls` answers `409 {"error":"tls is off"}`. Each
state change logs `web: tls <regenerate|upload|reset> by <ip>` (upload with the subject `%q`).

### UI

Header lock (`#h-sec`, now a button) — tooltip carries mode and expiry, click opens the
`<dialog id="cert">` (also *Settings → Certificate…*): mode badge (`automatic (fallback)`
in warn colour during a fallback), subject/issuer, SAN chips, validity (highlight < 30
days, days = ceil like the server), fingerprint + copy, Download .crt/.cer, Regenerate…
(inline confirm with "generate a new key" checkbox), Upload own certificate… (two file
inputs that fill two PEM textareas, `autocomplete="off"`, cleared on cancel/close; a
`force_required` 400 reveals the *install anyway* checkbox with the HSTS warning and the
name), Back to auto (mode file or fallback), collapsible "How to trust this certificate"
(Windows/macOS/Firefox/Android). The notice shows the server's `warnings` (no SAN matching
in the UI; "under HSTS the browser will refuse that name") and the fallback explanation.
After every certificate action the panel re-reads `/api/tls` and the config
(`loadConfig()`), so the curve editor holds the rewritten `[web]` keys. Mock:
`?mock=1&tls=off|file|soon|fallback`. JS budget 56 KB (the panel markup lives in
index.html, the JS only binds data).

### CLI

```
n5-fangov cert info | export [--der] [FILE] | regen [--new-key] | upload CERT KEY | reset
```
Socket API when the daemon answers (hot swap), else the same `tlsManager` on the files
plus a restart hint. `cert regen` keeps the key by default (old behaviour was a new key).

## v0.3.0-beta contract — sessions, visibility split, alerts panel, about, sensors, curves, presets (16.09.2026)

Findings of the operator's first multi-hour review of the dashboard: everything was
visible without signing in (settings, certificate panel, hardware details, alerts), no
logout, no "remember me", no password change, no About/licence, curve points always
appended at the end, only the three channel temperatures charted, no shipped presets.
This release fixes all of it. Version string `0.3.0-beta.1`; the UI shows the
pre-release tag as a badge until the operator lifts it.

Ownership (three builders in parallel, each in its own worktree):

- **WEBAPI builder** owns `internal/web/*.go` (+ tests). Nothing else.
- **UI builder** owns `internal/web/static/*` (index.html, app.js, app.css, mock).
  Nothing else.
- **CMD builder** owns `cmd/n5-fangov`, `internal/config`, `internal/alert`,
  `internal/control`, `internal/version`, `deploy/`, `README.md`, `Makefile`.

The interfaces below are the contract; the integrator wires them in `cmd/n5-fangov`.

### Visibility model (enforced server-side, mirrored by the UI)

With `[web] auth = "basic"`:

| Class | Endpoints | Anonymous | Signed in (session cookie or Basic) |
|---|---|---|---|
| **public** | `GET /api/version`, `GET /api/about`, `GET /api/session`, `POST /api/login`, `POST /api/logout` | full | full |
| **public, filtered** | `GET /api/state`, `GET /api/history` | reduced (see below) | full |
| **protected** | everything else under `/api/` (config, sensors, profiles, presets, log, tls incl. the cert downloads, alerts, dashboard, account, overrides) | 401 | full |
| static files | `/`, `app.js`, `app.css` | full | full |

Reduced `GET /api/state`: `ts`, `status`, `profile`, `verified`, `uptime_s`, `dry_run`,
`channels[]` with `name, pwm, sensor, temp, duty, target, rpm, mode` — **no**
`hwmon_path`, `extra_temps`, `alerts`, `watched`. Reduced `GET /api/history`: the points
without `extra`. With `auth = "none"` every request counts as signed in (`/api/session`
→ `{"authenticated":true,"mode":"none"}`); the unix socket handler always counts as
signed in. State-changing methods keep needing the CSRF header (`X-N5-Fangov-Csrf: 1`) —
that header is the CSRF defence for the cookie session (custom header + `SameSite=Strict`).

### Sessions (WEBAPI builder, `internal/web/session.go`)

```
POST /api/login    {"user","password","remember":bool}   public, CSRF, rate-limited like Basic (same limiter, per IP)
                   → 200 {"ok":true,"user","expires":unix,"remember"} + Set-Cookie
                   → 401 {"error":"invalid user or password"}; 429 while throttled; body limit 4 KiB
POST /api/logout   → 204; clears the cookie and revokes the session (also fine when there is none)
GET  /api/session  → {"authenticated":bool,"mode":"none|basic","user","expires"?,"remember"?,"via":"cookie|basic|none"}
```
Cookie `n5fangov_session=<token>; Path=/; HttpOnly; SameSite=Strict; Max-Age=<remaining>`
plus `Secure` when the request arrived over TLS. Token: 32 random bytes, base64url.
Lifetime 12 h, with remember 30 days; `LastSeen` is refreshed at most once a minute.

```go
type Session struct { ID, User, IP string; Created, Expires, LastSeen time.Time; Remember bool }
type SessionStore interface {
    Create(user string, remember bool, ip string) (token string, s Session, err error)
    Lookup(token string) (Session, bool)          // expired → false (and pruned)
    Revoke(token string)
    RevokeAll(keepToken string)                   // "" = every session
    List() []Session                              // ID is the first 8 hex chars of sha256(token), never the token
}
func NewSessionStore(path string, logf func(string, ...any)) SessionStore
```
The store keeps `sha256(token) → Session` in memory and, when `path != ""`, mirrors it
to that JSON file (0600, temp file + rename) on every change; it loads the file at start
so a restart or a config-triggered restart keeps "remember me" sessions. Cap 50 sessions
(oldest dropped). `Deps.SessionFile string` — cmd passes `<state dir>/sessions.json`
(see Deploy); `""` = memory only. A file that cannot be written is logged once and the
store continues in memory.

`guard` resolves the caller once per request: cookie → store lookup; else Basic → the
current `AuthConfig`; the result (`authenticated bool`, `user`, `via`) rides in the
request context (`web.CallerFrom(ctx)`) for the filtered handlers. Basic auth stays
accepted on every protected request (CLI, curl, scripts). A failed Basic credential or
login counts against the limiter exactly as today; an anonymous request to a protected
endpoint is a silent 401 (no log, no count).

### Account (WEBAPI builder ↔ CMD builder)

```go
// AccountStore (Deps.Account, implemented in cmd): the credentials in effect and their update.
type AccountStore interface {
    Current() AuthConfig
    // Update rewrites [web] user and/or password_hash in the config file
    // (config.SetKey: comments kept; the tls pin still applies) and returns
    // the credentials now in effect. "" keeps the current value.
    Update(user, passwordHash string) (AuthConfig, error)
}
```
The server keeps the effective `AuthConfig` behind an atomic pointer (initial value
`Deps.Auth`, replaced after every successful `Update`); `authorized()` reads it there.

```
GET  /api/account                  → {"user","mode","sessions":[{id,created,expires,last_seen,remember,ip,current}]}   protected
POST /api/account/password         {"current_password","new_password"}  → 200 {"ok"}                                  protected
                                   new: 8..128 chars; current wrong → 403 {"error":"current password wrong"} (limiter counts it)
                                   writes web.PasswordHash(user, new), swaps AuthConfig, RevokeAll(keep = the caller's cookie)
POST /api/account/user             {"current_password","user"}          → 200 {"ok","user"}                           protected
                                   user ^[A-Za-z0-9_.-]{1,32}$; same verification and revocation as the password change
POST /api/account/sessions/revoke  {"others":true}                       → 200 {"ok","revoked":n}                      protected
```
With `auth = "none"` the three POSTs answer `409 {"error":"auth is none"}`. Every change
logs `web: account <password|user> changed by <ip>`.

### Alerts panel (CMD builder implements `AlertMgr`, WEBAPI serves it)

Config addition:
```toml
[alert]
transport = "auto"     # auto | pve | mail | log | off   (auto = pve-notify if PVE::Notify + perl, else mail(1), else log)
mail_to = "root"       # mail transport only
```
`config.Alert{Transport, MailTo}`; unknown transport → warning + "auto"; `mail_to` must be
a local user or an address without spaces/quotes (else warning + "root"). The alert sink
becomes swappable (`alert.Swappable` wrapping the current `Sink`, `Set(Sink)`), so
`Configure` and `PUT /api/config` (via `Service.Reload` → an `OnAlertConfig` callback in
cmd) apply it without a restart. Concrete sinks gain `Send(kind, msg string) error`
(delivery error returned, still logged); `Alert` keeps its fire-and-forget contract.

```go
type AlertRecord struct { TS int64 `json:"ts"`; Kind string `json:"kind"`; Msg string `json:"msg"` }
type AlertKind   struct { Kind string `json:"kind"`; Description string `json:"description"` }
type TemplateStatus struct { Installed, Current, Writable bool; Path string; Reason string `json:",omitempty"` }
type AlertStatus struct {
    Transport string `json:"transport"`; Effective string `json:"effective"`   // configured | pve-notify|mail|log|off
    MailTo string `json:"mail_to"`; PVEAvailable bool `json:"pve_available"`; MailAvailable bool `json:"mail_available"`
    Template TemplateStatus `json:"template"`; Cooldown string `json:"cooldown"`; Kinds []AlertKind `json:"kinds"`
}
type AlertMgr interface {
    Status() AlertStatus
    Recent(n int) []AlertRecord                    // newest first; ring of 50, mirrored to <state dir>/alerts.json
    Test() (transport string, err error)           // sends kind "test" now, no cooldown; err = delivery failure
    InstallTemplate() (path string, err error)     // writes the two .hbs (embedded in internal/alert) to
                                                   // /etc/pve/notification-templates/default/; errors.ErrUnsupported without PVE
    Configure(transport, mailTo string) (AlertStatus, error)  // validates, config.SetKey [alert], hot-applies
}
```
```
GET  /api/alerts            → AlertStatus + {"last":{kind:ts},"recent":[AlertRecord…]}     protected
POST /api/alerts/test       → 200 {"ok","transport"} | 502 {"error":"<delivery error>","transport"}   protected
POST /api/alerts/template   → 200 {"ok","path"} | 501 (no PVE) | 500 {"error"} with the CLI hint      protected
PUT  /api/alerts            {"transport","mail_to"} → 200 {"ok","status":AlertStatus} | 400           protected
```
`Template.Current` compares the installed files with the embedded ones; `Writable` is a
probe (create and remove a temp file in the directory). Kinds: sensor, stall, temp,
write, config, config-channels, restart, failed, kernel, tls, test — with one-line
descriptions. The ring records every alert that passes the cooldown (the controller's
and serve's), so the panel is the history the operator asked for. CLI:
`n5-fangov alerts status | test | template` (socket when the daemon runs, else the same
manager on the files). The hidden `alert <kind> <msg>` stays as it is (onfailure unit).

### About (public)

```
GET /api/about → {"name":"n5-fangov","version","prerelease":"beta.1"|"","license":"GPL-2.0-only",
                  "license_url":"https://www.gnu.org/licenses/old-licenses/gpl-2.0.html",
                  "repo":"https://github.com/SirRenix/n5-fangov","author":"SirRenix","author_url":"https://github.com/SirRenix",
                  "go":runtime.Version(),"credits":[{"name","url","note"}]}
```
`Deps.About web.About` (struct, filled in cmd from `internal/version`). Credits:
`ltdstudio/minisforum-n5-it5571` (the kernel driver), `Sl0thC0der/proxfansx` (dashboard
idea). No host name, no addresses, no paths. `internal/version`: `Version =
"0.3.0-beta.1"`, `Prerelease() string` (text after the first `-`, "" for a release),
`-X` override unchanged. `GET /api/version` adds `"prerelease"` and `"auth":"none|basic"`.

### Dashboard sensors (CMD builder: control + config; WEBAPI serves)

Config addition:
```toml
[dashboard]
sensors = ["hwmon:amdgpu:temp1", "hwmon:nic1:temp1"]   # extra sensors recorded in the history for the dashboard chart, 0..8 ids
```
`config.Dashboard{Sensors []string}`; ids must parse (`sensor.Parse`) — an id that does
not resolve is kept in the config but reported as a warning and skipped. The controller
reads the watched sensors once per cycle (after the channel sensors, errors ignored →
value absent) and records them in `HistoryPoint.Extra map[string]float64`
(`json:"extra,omitempty"`), and in `Snapshot.Watched map[string]float64`
(`json:"watched"`, live value). Reload applies a changed list without a restart.

```go
type DashboardStore interface {          // Deps.Dashboard, implemented in cmd
    Sensors() []string
    SetSensors(ids []string) (warnings []string, err error)   // validates count/syntax, config.SetKey [dashboard], hot-applies
}
```
```
GET /api/dashboard → {"sensors":[…]}                       protected
PUT /api/dashboard {"sensors":[…]} → {"ok","sensors","warnings"}   protected; > 8 or unparsable id → 400
```
`GET /api/sensors` stays the catalogue (every readable temperature with its live
value); the UI groups it (see below).

### Presets: built-in N5 Pro sets (CMD builder)

`web.Preset` gains `Builtin bool` and `Description string`. Built-in presets are TOML
texts embedded in `internal/config/presets/*.toml` (`config.BuiltinPresets(profile)`),
listed only for the active profile, applied like a file preset, never saved over
(`PUT /api/presets/<builtin>` → 409) and never deleted. `DELETE /api/presets/{name}`
(new, protected; `PresetStore.Delete(name) error`, `web.ErrPresetBuiltin` → 409,
`fs.ErrNotExist` → 404) removes a user preset. The three N5 Pro sets (duty 0..255;
measured duty→RPM in the file header; HDD `stop = 140` in every set):

| Preset | cpu (k10temp, crit 88) | ssd (nvme:max, crit 72) | hdd (drivetemp:max) |
|---|---|---|---|
| `n5pro-quiet` — the operator's tuned set (16.09.): lowest noise, HDDs around 45 °C | `[[30,25],[61,163],[85,255]]` | `[[35,55],[65,255]]` | `[[26,63],[55,92],[56,255]]` crit 66 |
| `n5pro-balanced` — **recommended**: HDDs held near 40 °C, audible under load only | `[[35,60],[60,150],[80,255]]` | `[[35,74],[55,160],[68,255]]` | `[[30,87],[42,140],[50,200],[55,255]]` crit 60 |
| `n5pro-cool` — drives first, noise second | `[[30,85],[55,170],[75,255]]` | `[[30,90],[50,180],[65,255]]` | `[[28,105],[38,150],[45,210],[50,255]]` crit 58 |

`install.sh` no longer needs preset files. The UI marks built-ins with a badge and
shows the description; the recommended one gets a second badge.

### UI (UI builder)

- **Header:** version with a `beta` badge when `prerelease != ""`; when anonymous a
  **Sign in** button (no automatic login popup — the reduced Overview is the landing
  page); when signed in the user name, a **Sign out** button, the lock button and the
  settings gear. Settings gear, lock/certificate button and every tab except Overview
  and About are hidden while anonymous. Login form: user, password, **Remember me**
  checkbox (hint: "30 days on this browser"), inline error on 401.
- **Overview anonymous:** channel cards + the two charts. Nothing else.
- **Overview signed in:** adds *Sensors* (the `/api/sensors` catalogue, polled with the
  state interval, grouped CPU / SSD·NVMe / HDD / GPU / NIC / EC·board / other by id
  prefix and hwmon name — k10temp|coretemp → CPU, nvme → SSD, drivetemp → HDD,
  amdgpu|nouveau|i915|radeon → GPU, nic|eth|mlx|igc|ixgbe|r8169|atlantic → NIC,
  ec:*|minisforum|acpitz|spd5118 → EC·board; each row: label, live value, a *chart*
  toggle that adds/removes the id via `PUT /api/dashboard`), an **Extra sensors** chart
  card (only when the watched list is non-empty; series from `history[].extra`, legend
  with a remove ×), *System* (profile, hwmon path, verified, notes, daemon interval,
  version, log file) and *Recent alerts* (from `/api/alerts`, newest first, kind badge,
  relative time, message). The three channel cards and their two charts stay fixed.
- **Curves:** *+ add point* opens a two-field row (temp, duty; prefilled with the middle
  of the widest temperature gap and the interpolated duty) and inserts at the sorted
  position; editing a temperature in the table re-sorts the rows when the field loses
  focus; the drag on the canvas is unchanged. Validation as before.
- **Presets:** built-in badge, *recommended* badge, description; *Delete* for user
  presets (confirm); the save form refuses built-in names client-side too.
- **Alerts tab (new):** transport (select auto/pve/mail/log/off + mail_to, Save),
  effective transport and availability, PVE template status with *Install / Update
  template* (disabled with reason when not writable), *Send test alert*, cooldown,
  kind list with last-sent time, recent alerts list.
- **About tab (new, public):** name, version + pre-release badge, one-paragraph
  description, licence (GPL-2.0-only, link), repository and author links
  (`github.com/SirRenix`), credits, "Dashboard mock: `?mock=1`".
- **Account (in the settings popover, signed in):** *Change password…* and *Change
  user…* forms (current password required), *Sign out other sessions*, session list.
- **Mock:** `?mock=1` starts anonymous; `&auth=none` = auth off; `&user=1` = signed in.
  The mock implements every endpoint above (login accepts admin/admin).
- JS budget 76 KB (raised from 72 with the releases link), no framework, CSP unchanged.

### Deploy (CMD builder)

- Unit: `StateDirectory=n5-fangov`, `StateDirectoryMode=0700` (`/var/lib/n5-fangov`:
  sessions.json, alerts.json); `ReadWritePaths` gains `-/etc/pve/notification-templates`
  (verified on the reference host: a process with this sandbox writes and removes a file
  there). `serve --state-dir` (default `$STATE_DIRECTORY`, else `/var/lib/n5-fangov`,
  env `N5FANGOV_STATE_DIR`); an unwritable state dir logs once and the daemon runs
  without persistence.
- `install.sh`: daemon-reload after the unit copy (unchanged path). `uninstall.sh`
  removes `/var/lib/n5-fangov`.
- README: Sign-in/visibility, sessions, account, alerts panel, presets, dashboard
  sensors, About/licence.

### v0.3.0-beta integration notes (16.09.2026)

- `SessionStore` gained `RevokeAllRename(keepToken, newUser)` and `SetEpoch(epoch)`;
  `web.New` binds the store to `CredentialEpoch(user, hash)` of `Deps.Auth`, so a
  password rotated outside the dashboard (`passwd`, config edit) drops every persisted
  session at the next start (R-M1). The account handlers move the epoch before revoking.
- `alertManager` caches the template probe for 10 minutes (R-M2): `GET /api/alerts`
  no longer writes to pmxcfs per poll. `Test` is bounded (20 s) and single-flight
  (`alert.ErrTestBusy` → 409).
- `mail_to` may not start with `-`; the Mail sink passes `--` before the recipient (R-M3).
- All config read-modify-write paths in cmd serialise on `configFileMu`, except
  `tlsManager.writeMode` (lock order with `pinConfig`).
- `config.SetKey` and `config.Parse` handle the dotted `web.user = …` layout; inline
  tables are refused by the stores with a clear error.
- Built-in presets of another profile are 404 on apply; `PUT` on a built-in name is 409.
- `GET /api/alerts` `last` = controller stamps overlaid by the ring (`Last()`), so
  serve-side kinds (config, kernel, tls, test) appear too.
- `http.AllowQuerySemicolons` wraps both handlers (Go 1.25 no longer logs the warning;
  kept as an explicit guard).
- Live on the reference host 16.09. 13:20: reduced anonymous state/history, 401 on every
  protected path, login/cookie attributes, remember-me 30 d, logout, dashboard sensors →
  `watched`/`extra`, template install into pmxcfs from inside the sandbox, test alert
  delivered via PVE::Notify, 403 on a wrong current password, sessions/alerts mirrors
  0600 in `/var/lib/n5-fangov`, RSS ~8 MB.
