# pvefand — Design Contract

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
10. `/sys` root is overridable via `PVEFAND_SYSFS` (tests use a fake tree under `testdata/`).
11. All user-facing strings English. Comments English. Logs to stdout (journald).

## Layout

```
cmd/pvefand/main.go            entry: subcommands serve|status|set|auto|curve|log|check|detect|version
cmd/pvefand/wiring.go          the only file in cmd that calls into internal/* (adapters, stores)
internal/config/               TOML config: load, validate (defaults on error), write, presets
internal/hwmon/                sysfs discovery + read/write helpers (root from PVEFAND_SYSFS)
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
tools/                         remote-go.ps1 (build/test via Docker on Builder)
testdata/sysfs/n5pro/          fake /sys tree mirroring n5host (hwmon names, temp/fan/pwm files)
```

## Config (`/etc/pvefand/config.toml`)

```toml
[daemon]
interval = "10s"        # 2s..120s
step_up = 40            # max duty increase per cycle (1..255)
step_down = 15          # max duty decrease per cycle (1..255)
stall_min_duty = 60     # RPM 0 at or above this duty counts as stall
stall_cycles = 2
stale_cycles = 18       # identical raw sensor value this many cycles → sensor frozen
alert_cooldown = "30m"
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
stop = "auto"           # "auto" (profile returns channel to EC/BIOS) or fixed duty 0..255
```

Presets: `/etc/pvefand/presets/<name>.toml` containing only `[[channel]]` tables.
Runtime state: `/run/pvefand/` (state.json, overrides, alert stamps). Unix socket:
`/run/pvefand/pvefand.sock`.

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
per cycle: sd_notify WATCHDOG=1 → read sensors (error → failsafe all, alert `sensor`)
→ targets from curve (interpolate) → overrides → critical → stall check → slew (first
cycle direct; manual direct) → write (rewrite unchanged every 6th cycle) → verify →
state snapshot → history ring (2h at interval) → periodic log line.
Failsafe = duty 255 on all managed channels. Stop = SafeStop per channel.
Overrides via `Controller.SetOverride(ch, duty)` / `ClearOverride(ch)` (from API).
Config reload: `Controller.Reload(cfg)` swaps curves/sensors atomically; channel set
changes require restart (return error).

State snapshot (JSON, also written to /run/pvefand/state.json):
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
GET  /api/log?lines=100                  → {"lines": ["..."]} (journalctl -u pvefand, newest last)
GET  /api/profiles                       → [{"name","title","verified","notes","active"}]
GET  /api/version                        → {"name":"pvefand","version":"..."}
```
Errors are `{"error": "..."}`; PUT /api/config adds `"errors": [...]` (parse warnings).
413 on bodies over 256 KiB (config) / 4 KiB (override).

TCP handler, in order: Host header must be an IP literal, `localhost`, the listen host
or an `allowed_hosts` entry (else 421, DNS-rebinding guard); state-changing methods
need `X-Pvefand-Csrf: 1` (else 403); with `auth=basic`, state-changing methods plus
`GET /api/config` and `GET /api/log` need basic auth (else 401, no challenge header;
failures throttled per IP: 5 free, then 250 ms doubling to 2 s, reset after 10 min).
Unix socket handler: none of the three checks; the hash is redacted there as well.
Startup logs a warning when the TCP listener is non-loopback and auth is none.

## CLI (cmd/pvefand)

```
pvefand serve [--config PATH] [--dry-run]
pvefand status               table like n5fan status
pvefand set <ch> <duty|NN%>  ; pvefand auto <ch|all>
pvefand curve                ; pvefand log [n] ; pvefand check ; pvefand detect ; pvefand version
pvefand test <ch>            channel verification run (like n5pro-ec 06 script), refuses while serve is regulating that channel unless --force
```

## Web UI (internal/web/static)

Tabs: Overview (cards per channel, live canvas charts, hardware details, last alerts),
Curves (points editor + chart per channel, sensor dropdown, critical, apply),
Manual (slider + auto toggle per channel), Presets, Log, Compatibility (profiles with
verified flag). Dark theme, layout inspired by ProxFansX; no framework; fetch + canvas.

## Deploy

`deploy/pvefand.service`: Type=notify, NotifyAccess=main, WatchdogSec=60,
ExecStartPre=/usr/bin/pvefand check --quiet, ExecStopPost=/usr/bin/pvefand failsafe,
Restart=always, RestartSec=5, StartLimitBurst=5, OnFailure=pvefand-onfailure.service,
RuntimeDirectory=pvefand, RuntimeDirectoryMode=0750 (the socket carries no auth).
`pvefand failsafe` = SafeStop all channels using the config,
works without the daemon.

## Testing

`go vet ./... && go test ./...` must pass in Docker (`tools/remote-go.ps1`). Fake sysfs
under `testdata/sysfs/n5pro` mirrors n5host. Controller tests use a fake Device.

## Integration notes (15.09.2026)

Deviations between this contract and the merged packages, as found while wiring
`cmd/pvefand`. The code is the reference; this list says where the text above
is loose.

- `config.Default()` has **no channels**; the N5 Pro set is `config.N5ProChannels()`.
  A missing config file therefore starts the daemon in monitoring-only mode (rule 8).
- `config.Load` returns `(Config, []Warning, error)`: missing file → defaults + one
  warning + `err == nil`; read error or TOML syntax error → defaults + warning + `err`.
  `config.Parse` also returns an error (syntax only). `Warning` is `{Field, Msg}`.
- `hwmon.FS.FindByName` returns `[]Device` (may be empty), not `(Device, error)`.
- `sensor.Known` returns `[]sensor.Info{ID, Description}`, not `[]string`; the
  description carries the live reading and the list ends with the two generic
  patterns (`hwmon:<name>:tempN`, `ec:<label>`) which do not parse as ids.
  `hwmon:<name>:tempN` always binds to the first device of that name (on n5host
  `hwmon:nvme:temp1` is one SSD, `nvme:max` is the hottest).
- `control.SensorFactory` returns `control.SensorReader`; `sensor.Source` satisfies
  it but the func types differ, so wiring adapts (nil-interface safe).
- `control.Options.Notify`/`Status` default to sd_notify already; wiring sets them
  explicitly. `Run(ctx)` calls `Stop()` itself; `Stop()` is idempotent (sync.Once).
  serve waits for `Run` to return (15 s cap) before calling `Stop` again.
- The controller's initial snapshot already has `ts > 0` with `status: "starting"`;
  READY=1 waits for `status != "starting"` (or 2×interval+5 s).
- In dry-run the snapshot `status` is `"dry-run"`, not `"ok"`. Duty in the
  snapshot is the computed target (the controller assumes 255 at start and writes
  nothing), not the hardware `pwmN` value.
- `alert.New(logger)` returns `alert.Sink` (`Alert(kind, msg)`, `Name()`); the
  controller's `Alerter` interface is satisfied by it. Cooldown lives in the
  controller; alerts sent directly from `serve`/`alert` bypass it.
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
- CLI socket/run-dir override: `PVEFAND_RUN_DIR` (default `/run/pvefand`) or
  `serve --run-dir`; there is no separate socket variable.
- `pvefand check` opens `pwmN`/`pwmN_enable` O_WRONLY without writing (permission
  probe); everything else in `check`/`detect` is read-only.
