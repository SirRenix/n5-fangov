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
internal/config/               TOML config: load, validate (defaults on error), write, presets
internal/hwmon/                sysfs discovery + read/write helpers (root from PVEFAND_SYSFS)
internal/profile/              Profile interface + n5pro, nct67xx, it87xx, monitor; Detect()
internal/sensor/               sensor sources: k10temp, coretemp, nvme:max, drivetemp:max, ec:*, hwmon:<name>:tempN
internal/control/              controller loop: curve, slew, override, critical, stall, plausibility, failsafe, history ring
internal/alert/                alert sink: PVE::Notify (perl) → mail(1) → log; cooldown per type
internal/ipc/                  unix socket server/client for CLI (same HTTP mux as web, no auth)
internal/web/                  HTTP API + embedded static UI (web/static/*), basic auth, CSRF
internal/sdnotify/             READY=1, WATCHDOG=1
internal/version/              version string
web/static/                    index.html, app.js, app.css (no build step, vanilla JS, canvas charts)
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
GET  /api/state                → snapshot
GET  /api/history?minutes=120  → [{ts, temps{}, duty{}, rpm{}}]
GET  /api/config               → current TOML as JSON + raw text
PUT  /api/config               → body: raw TOML text; validate; write file; reload (or "restart required")
PUT  /api/override/{name}      → {"duty":191} ; DELETE → back to curve
GET  /api/presets              → [{name, channels}] ; POST /api/presets/{name}/apply ; PUT /api/presets/{name} (save current)
GET  /api/log?lines=100        → journal lines (journalctl -u pvefand -o json) or internal ring if unavailable
GET  /api/profiles             → all profiles with verified flag + notes
GET  /api/version
```
Writes on TCP require auth when `auth=basic` and header `X-Pvefand-Csrf: 1`.
Unix socket: no auth.

## CLI (cmd/pvefand)

```
pvefand serve [--config PATH] [--dry-run]
pvefand status               table like n5fan status
pvefand set <ch> <duty|NN%>  ; pvefand auto <ch|all>
pvefand curve                ; pvefand log [n] ; pvefand check ; pvefand detect ; pvefand version
pvefand test <ch>            channel verification run (like n5pro-ec 06 script), refuses while serve is regulating that channel unless --force
```

## Web UI (web/static)

Tabs: Overview (cards per channel, live canvas charts, hardware details, last alerts),
Curves (points editor + chart per channel, sensor dropdown, critical, apply),
Manual (slider + auto toggle per channel), Presets, Log, Compatibility (profiles with
verified flag). Dark theme, layout inspired by ProxFansX; no framework; fetch + canvas.

## Deploy

`deploy/pvefand.service`: Type=notify, NotifyAccess=main, WatchdogSec=60,
ExecStartPre=/usr/bin/pvefand check --quiet, ExecStopPost=/usr/bin/pvefand failsafe,
Restart=always, RestartSec=5, StartLimitBurst=5, OnFailure=pvefand-onfailure.service,
RuntimeDirectory=pvefand. `pvefand failsafe` = SafeStop all channels using the config,
works without the daemon.

## Testing

`go vet ./... && go test ./...` must pass in Docker (`tools/remote-go.ps1`). Fake sysfs
under `testdata/sysfs/n5pro` mirrors n5host. Controller tests use a fake Device.
