# n5-fangov — Design Contract

Guarded fan control for Proxmox VE and Debian. One static Go binary: daemon (regulation
loop + HTTP API + embedded dashboard), CLI over a unix socket, hardware profiles.
Hardware-verified on the Minisforum N5 Pro (IT5571 EC via `minisforum_n5_it5571`); the
generic hwmon profiles (NCT67xx, IT87xx, monitoring-only) ship as "from documentation,
untested".

This file is the contract between packages as it stands for the current pre-release
(`n5-fangov version`; history in [CHANGELOG.md](CHANGELOG.md)). Every builder follows it;
deviations are written here first, then implemented. Where the text and the code
disagree, the code is a bug or the text is — never a third state; fix one. Operator-facing
behaviour is described in [docs/](docs/README.md), the deploy file layout in
[docs/01-install.md](docs/01-install.md#file-layout).

## 1. Non-negotiable rules

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
9. Go toolchain as pinned in `go.mod`, `CGO_ENABLED=0`, dependencies:
   `github.com/BurntSushi/toml` only. No cgo, no systemd library (sd_notify is a 20-line
   unix datagram write).
10. `/sys` root is overridable via `N5FANGOV_SYSFS` (tests use a fake tree under `testdata/`).
11. All user-facing strings English. Comments English. Logs to stdout (journald).

## 2. Layout

```
cmd/n5-fangov/
  main.go            dispatch only: subcommand registry, usage, exit codes 0/1/2
  serve.go           the daemon, split by stage: serveConfig (config, log file, alerts),
                     serveDevice (profile, controller), serveWeb (TLS, listeners, stores),
                     serveRun (goroutines, watchdog ticker, READY, wait, stop); cmdServe glues them
  scheduler.go       preset schedules ([[schedule]]): ticker, transition → preset apply, `schedule` alert
  setup.go           setup, passwd (interactive prompts, password from file/stdin)
  cli.go             status, set, auto, curve, log (socket client, state.json fallback)
  check.go           check [--quiet] [--after-update]   (ExecStartPre, apt hook)
  detect.go          detect
  testrun.go         test <ch> [--force]
  failsafe.go        failsafe (ExecStopPost)
  cert.go/cert_cli.go, tlsmgr.go   certificate manager (TLSMgr) and `cert` CLI
  alerts_cli.go      alerts status|test|template
  token_cli.go       token create|list|revoke (socket; the daemon owns tokens.json)
  system_cli.go      system [--json]
  bundle.go          export, import (settings bundle)
  wiring.go          core adapters: config specs, sensor factory, controller, web server, log store
  wiring_presets.go  dirPresetStore (list, apply = merge by pwm, save, delete, detail, rename)
  wiring_alerts.go   alertManager (transport swap, ring, template probe, test)
  wiring_account.go  accountStore, editConfig helper, state dir paths
  wiring_dashboard.go dashboardStore, hookedService (reload re-applies [alert] and the schedules)
  wiring_tls.go      TLS deps, serveTLS adapter
  wiring_sysinfo.go  system collector
  about.go           aboutInfo (GET /api/about: name, version, licence, credits)
  common.go, prompt.go   socket client, helpers, no-echo prompts
internal/config/     TOML config: Parse (defaults on error), Load, Save, SetKey (in-place edit), presets, built-in presets
internal/hwmon/      sysfs discovery + read/write helpers (root from N5FANGOV_SYSFS); block-device helpers
                     (BlockDevices, DiskHwmon, DiskTemp, VirtualBlock) shared by sensor and sysinfo; hwmontest fixtures
internal/profile/    Profile/Device interfaces; n5pro, nct67xx, it87xx, monitor; Detect()
internal/sensor/     sensor sources: k10temp, coretemp, nvme:max, drivetemp:max, disk:<dev>, ec:*,
                     hwmon:<name>:tempN, composite "a,b" (maximum)
internal/control/    controller loop: curve, curve post-processing (hysteresis, min_on), slew, override,
                     critical, stall, plausibility, failsafe, watched sensors, snapshot, alert stamps;
                     cycle split into readSensors / computeTargets / checkStall / writeAndFinish
internal/history/    tiered history store (raw 2 h, 1-min 24 h, 5-min 7 d), JSON persistence, CSV
internal/schedule/   [[schedule]] evaluation: active entry for a time, next switch
internal/alert/      alert sinks (PVE::Notify, mail, webhook, log, off), Swappable, Ring (history), PVE templates
internal/ipc/        unix socket listener/client for the CLI (same mux as web, no auth)
internal/logfile/    rotating log file (O_NOFOLLOW, size rotation, Lines/Export/Clear)
internal/tlscert/    certificates: EnsureAuto, Store (hot swap), ValidatePair, ServerConfig, Info
internal/web/        HTTP API, guard (Host → auth → CSRF → scope), sessions, API tokens, rate limiter,
                     TLS listener, OpenAPI from the route table; embeds static/
internal/web/static/ index.html, app.js, app.css, mock.js — no build step, vanilla JS, canvas charts;
                     the mock is loaded only with ?mock=1 and is not part of the production bundle
internal/sysinfo/    hardware inventory from /sys, /proc, SMBIOS table, lspci; live disk temperatures
internal/fsutil/     WriteAtomic(path, data, perm): temp file in the target dir, sync, chmod, rename, clean-up
internal/sdnotify/   READY=1, WATCHDOG=1
internal/version/    Version (the only version literal), Prerelease()
deploy/              systemd units, onfailure script, install/uninstall, apt hook, config example, debian/, PVE templates
tools/remote-go.ps1  build/test in a Docker container on a Linux host over ssh (pwsh)
testdata/sysfs/n5pro fake /sys tree mirroring the reference host (generic names)
docs/                operator pages 01–11, AUDIT.md, DESIGN-AUDIT.md, RELEASE-GATE.md, REVIEW-TAGS.md,
                     openapi/ (HA recipe), screenshots/
```

Files are named by topic, never by version or review round (`wiring_v3.go`, `v4_test.go`,
`*_fix_test.go` are gone); a test file carries the name of the file it tests
(`session_test.go`, `token_test.go`) or of its topic (`api_bounds_test.go`).

Subcommands (19, `main.go` `order`): `setup serve status set auto curve log check detect
system test failsafe passwd cert alerts token export import version`; hidden: `help`, `alert
<kind> <msg>` (used by the onfailure unit). Exit codes: 0 ok, 1 failure, 2 usage.
`token create NAME [--scope read|control|admin] [--ttl DAYS]` prints the secret once
(stdout, nothing else on stdout so it can be captured), `token list` (table: id, name,
scope, created, expires, last used, last address) and `token revoke ID` talk to the
daemon over the socket (the daemon owns `tokens.json`; without a running daemon: exit 1
with the hint). `alerts status` shows `webhook_url` (redacted, section 7) and format.
Environment: `N5FANGOV_RUN_DIR` (default `/run/n5-fangov`), `N5FANGOV_STATE_DIR` (default
`$STATE_DIRECTORY`, else `/var/lib/n5-fangov`), `N5FANGOV_SYSFS` (default `/sys`);
`N5FANGOV_LOG_ROOT` moves the `[log].file` root for tests only.

## 3. Config (internal/config)

File: `/etc/n5-fangov/config.toml`. Presets: `/etc/n5-fangov/presets/<name>.toml` with
only `[[channel]]` tables. Built-in presets: `internal/config/presets/*.toml`, embedded.

`Parse(raw) (Config, []Warning, error)`: only a TOML syntax/structure error returns
`err` (with `Default()`); every invalid value is replaced by its default and reported as
`Warning{Field, Msg}`; unknown sections and keys are warnings. `Load(path)`: missing file →
defaults + warning + `err == nil`; unreadable → defaults + warning + `err` wrapping
`ErrUnreadable`. `SetKey(raw, section, key, value)` edits one key in place (comments kept;
`[web]` header and dotted `web.key = …` layouts handled; an inline table `web = {…}` is
refused). `Default()` has **no channels**; `N5ProChannels()` is the built-in preset `n5pro-balanced`
(the recommended set, parsed from the embedded TOML — one source), written by `setup` and
added by `control.SanitizeChannels` on the N5 Pro when the file lacks them (until 0.3.1
a separate literal set that matched no preset; release-gate finding 5b).

| Key | Range / values | Default | Applies |
|---|---|---|---|
| `[daemon] interval` | `2s`..`30s`; below → default, above → clamped to 30 s (WatchdogSec=60 needs two cycles) | `10s` | reload |
| `step_up`, `step_down` | 1..255 duty per cycle | 40, 15 | reload |
| `stall_min_duty` | 1..255 | 60 | reload |
| `stall_cycles` | 1..20 | 2 | reload |
| `stale_cycles` | 6..600 (checked on `k10temp` only) | 18 | reload |
| `alert_cooldown` | `60s`..`24h` | `30m` | reload |
| `log_every` | 0..1000000 cycles (0 = never) | 30 | reload |
| `profile` | `auto` \| `n5pro` \| `nct67xx` \| `it87xx` \| `monitor` | `auto` | restart |
| `[web] listen` | `host:port`; forced to `127.0.0.1:8010` when auth is misconfigured (`H2`) | `127.0.0.1:8010` | restart |
| `auth` | `none` \| `basic`; `basic` needs `user` + parsable `password_hash`, else `none` + loopback | `none` | restart |
| `user` | string (API rule for changes: `^[A-Za-z0-9_.-]{1,32}$`) | `""` | restart (live via account API) |
| `password_hash` | `pbkdf2$<iter>$<salt hex>$<key hex>` (iter 1000..10000000, salt ≥ 8 B, key 32 B) or legacy 64-hex sha256(`user:password`) | `""` | restart (live via account API) |
| `allowed_hosts` | array of Host values; `"*"` disables the check | `[]` | restart |
| `tls` | `auto` \| `off` \| `file`; default by listener; non-loopback + `off` → `auto`; `file` needs both paths | `off` loopback / `auto` else | restart (live via certificate API) |
| `cert_file`, `key_file` | PEM paths (tls = file) | `""` | restart (live via certificate API) |
| `behind_tls_proxy` | bool: set the cookie `Secure` flag although the listener is plain HTTP (TLS terminated by a reverse proxy) | `false` | restart |
| `[log] file` | absolute clean path under `/var/log/`, `""` = journal only; not a symlink/device/directory | `/var/log/n5-fangov/n5-fangov.log` | restart |
| `max_size_mb` | 1..100 | 5 | restart |
| `max_files` | 1..20 | 5 | restart |
| `[alert] transport` | `auto` \| `pve` \| `mail` \| `webhook` \| `log` \| `off` (lower-cased) | `auto` | reload |
| `mail_to` | `^[A-Za-z0-9_][A-Za-z0-9._%+-]*(@[A-Za-z0-9.-]+)?$`, ≤ 254 (never starts with `-`) | `root` | reload |
| `webhook_url` | absolute `http`/`https` URL with a host, no userinfo, ≤ 2048; required for `transport = "webhook"` (missing/invalid → transport `auto` + warning) | `""` | reload |
| `webhook_format` | `json` \| `text` (section 7) | `json` | reload |
| `[dashboard] sensors` | 0..8 distinct sensor ids (shape only; resolution is the controller's business) | `[]` | reload |
| `[[channel]] name` | `^[a-z0-9_]{1,32}$`, unique; else the channel is dropped | — | restart when the set changes |
| `pwm` | 1..8, unique | — | restart when the set changes |
| `sensor` | one sensor id (section 5) as a string, **or an array of 1..4 ids** (each non-empty, no `,`, distinct) — stored as the composite id `a,b` (comma-joined, no spaces); a string with commas is accepted in the same form | — | reload |
| `curve` | 2..8 points `[temp, duty]`, temp −20..120 strictly ascending, duty 0..255 non-decreasing; else `[[45,85],[80,255]]` | — | reload |
| `critical` | last curve temp + 1 .. 150; missing → last + 10 (≤ 150) | last + 10 | reload |
| `stop` | `"auto"` or fixed duty; below 60 raised to 60; invalid → `DefaultStop(sensor)` (`140` for `drivetemp:max` and for every composite that contains it, else `auto`) | by sensor | reload |
| `hysteresis` | 0..10 °C (integer); 0 = off (section 6 "Curve post-processing") | 0 | reload |
| `min_on` | `0s`..`1h` duration; `0s` = off | `0s` | reload |
| `[[schedule]] preset` | preset name `^[a-z0-9_-]{1,64}$` (existence is checked when the switch happens, not at parse) | — | reload |
| `from`, `to` | `HH:MM` strings (24 h, local time of the host; a one-digit hour is accepted and stored as `HH:MM`), both or neither (empty strings count as absent; a value that is not a string — a bare TOML time literal `22:00:00`, an integer — drops the entry with a warning, it never reads as absent); `from == to` → entry dropped; `to < from` = the window crosses midnight | — | reload |
| `days` | subset of `mon tue wed thu fri sat sun` (lower-cased, distinct); the day of the window is the day `from` falls in; missing = every day; on the fallback the key is ignored with a warning (`days ignored on the fallback`) and cleared | all | reload |

Schedule rules: at most 16 entries; an entry without `from`/`to` is the **fallback** that
applies whenever no window matches — at most one, a second one is dropped with a warning;
an invalid entry is dropped with a warning (rule 8), the others stay. An entry with an
unknown key is a warning. Config JSON (`GET /api/config`) carries `schedule[]` with
`{preset, from, to, days[]}` (`from`/`to` empty for the fallback).

"reload" = applied by `PUT /api/config`, preset apply and import without a restart;
"restart" = read once at start (the account and certificate APIs edit the file and apply
their keys live). A changed channel set or profile makes `Service.Reload` return
`ErrRestartRequired` (HTTP 202); the controller then applies nothing, but `[alert]` and
`[[schedule]]` are still taken from the written file (the reload hook in cmd hands them to
the alert manager and the scheduler on success and on 202 alike — the file is the truth
for the two sections that need no restart). `[[schedule]]` tables are read by the
scheduler (cmd) from every reload.

Presets: name `^[a-z0-9_-]{1,64}$`. A preset file holds `[[channel]]` tables with every
channel key; `hysteresis` and `min_on` are written only when set (`Marshal` omits them at
their defaults, like the dashboard does), so a preset written before 0.3.1 simply reads as
the defaults. A user preset is written either from the running tables (`PUT /api/presets/{name}`
with an empty body, `config.SavePreset` of the file's channels) or from a composed channel list
(the same call with a JSON body — the dashboard's preset editor; `dirPresetStore.SaveChannels`
after the web layer validated the list with the config rules, see section 9). User preset
files carry no description. The parser flags a channel table that carries either key
(`Channel.PostSet`, `toml:"-"`; `Clone` copies it, `Marshal` ignores it). **Apply merges
by pwm:** a channel of the preset replaces the config channel with the same `pwm` (the
config channel's `name` is kept when the preset uses another name for that pwm; its
`hysteresis`/`min_on` are kept when the preset table sets neither key), config channels
the preset does not name are kept unchanged — so an optional `pwm4` channel survives a
built-in preset and the apply never needs a restart for it. A preset channel whose pwm the
config lacks is added (that is the case that still returns 202); when its name collides
with a kept config channel it is added as `pwm<N>`, `pwm<N>_2`, … (the first unused) so
the file stays valid. The apply (API and scheduler) splices the merged tables into the
config **text** in place (`config.ReplaceChannels`: every `[[channel]]` block from its
header to its last key line is removed, `MarshalChannels` goes where the first one was or
at the end; comments, `[[schedule]]`, `[alert]`, `password_hash` and every other byte
stay) and falls back to a full `Marshal` only when the spliced text does not parse (an
inline `channel = [{…}]`). Built-in N5 Pro sets (listed for the `n5pro` profile
only; `PUT` on a built-in name → 409, apply on another profile → 404; a user file with a
built-in name is shadowed and logged):

| Preset | cpu (`k10temp`, crit 88) | ssd (`nvme:max`, crit 72) | hdd (`drivetemp:max`, stop 140) |
|---|---|---|---|
| `n5pro-quiet` — lowest noise, HDDs around 45 °C | `[[30,25],[61,163],[85,255]]` | `[[35,55],[65,255]]` | `[[26,63],[55,92],[56,255]]`, crit 66 |
| `n5pro-balanced` — **recommended**: HDDs near 40 °C | `[[35,60],[60,150],[80,255]]` | `[[35,74],[55,160],[68,255]]` | `[[30,87],[42,140],[50,200],[55,255]]`, crit 60 |
| `n5pro-cool` — drives first | `[[30,85],[55,170],[75,255]]` | `[[30,90],[50,180],[65,255]]` | `[[28,105],[38,150],[45,210],[50,255]]`, crit 58 |

## 4. Profiles (internal/profile)

```go
type Channel struct { Index int; Label string; HasTach bool }
type Profile interface {
    Name() string                      // "n5pro"
    Title() string                     // "Minisforum N5 Pro (IT5571 EC)"
    Verified() bool                    // true only for n5pro
    Notes() string
    Detect(fs *hwmon.FS) (Device, error)
}
type Device interface {
    Profile() Profile
    HwmonPath() string
    Channels() []Channel
    ReadRPM(ch int) (int, error)
    ReadDuty(ch int) (int, error)
    EnterManual(ch int) error           // pwmN_enable=1 (n5pro: the driver sets 255 internally)
    WriteDuty(ch int, duty int) error   // write + read back
    SafeStop(ch int, stop string) error // "auto" → profile default (n5pro: enable=2), else fixed duty
    ExtraTemps() map[string]string      // ec:<label> → path (n5pro exposes temp1..4)
}
```

Detection: `n5pro` = hwmon name `minisforum_n5_it5571` (DMI check log only); `nct67xx` =
`^nct67\d\d$`, auto mode value 5 (SmartFan IV), the original `pwmN_enable` captured at
start is restored; `it87xx` = `^it8[67]\d\d$`, restore original; `monitor` = no pwm.
`hwmon.FS.FindByName` returns `[]Device` (may be empty). On the N5 Pro pwm3 is never
`auto` (the EC does not regulate it after a write) — enforced in `control.SanitizeChannels`.

## 5. Sensors (internal/sensor)

Ids: `k10temp` (Tctl = temp1), `coretemp` (max of tempN), `nvme:max`, `drivetemp:max`,
`disk:<dev>` (one block device, see below), `hwmon:<name>:tempN` (first device of that
name), `ec:<label>` (profile `ExtraTemps`), and the **composite** `id1,id2[,…]` (2..4
parts, each one of the above, no nesting) = maximum of its parts.
Values in millidegrees; plausible range −20000..120000. `max` sources ignore devices that
vanished as long as one remains; zero devices → error. `Known()` returns
`[]Info{ID, Description}` with the live reading in the description, ending with the two
generic patterns that do not parse as ids. `Parse(id)` yields a `Source`; the controller's
`SensorReader` is satisfied by it (wiring adapts the func types).

- `disk:<dev>`: `<dev>` is `^[a-z0-9]{1,32}$` and a directory under `<root>/block/`;
  the temperature is `temp1_input` of the device's hwmon: `block/<dev>/device/hwmon/hwmon*/`
  (SATA/SAS, drivetemp) or `block/<dev>/device/hwmon*/` (NVMe controller). No hwmon →
  `ErrNoDevice`. `Known()` lists one `disk:<dev>` per block device that has one (sorted by
  name, virtual devices `dm-*`, `loop*`, `zd*`, `md*`, `sr*`, `ram*`, `zram*`, `nbd*`,
  `drbd*`, `rbd*`, `fd*` skipped — `hwmon.VirtualBlock`, the same rule sysinfo uses), description
  `"<model> (<dev>, <hwmon name>)"` with `model` from `block/<dev>/device/model`
  (trimmed; NVMe: same path) or `"disk"`, plus the live reading. `SensorInfo` gains
  `Kind` = `"ssd"` for NVMe / non-rotational, `"hdd"` for rotational (`queue/rotational`),
  `""` otherwise, so the dashboard groups without guessing from the id.
- Composite: `Parse` splits on `,`, trims, parses every part; a part that does not resolve
  now is skipped, at least one must resolve (else the error of the first part); `Read` is the
  maximum of the readable parts (`maximum` semantics). Because the controller re-resolves
  every channel sensor every 60 cycles, a part whose device appears later is picked up then.
  `ID()` is the canonical form (parts in the configured order, joined by `,`).

## 6. Controller (internal/control)

Per cycle: `sd_notify WATCHDOG=1` → read channel sensors (unresolved / unreadable /
implausible / frozen → **that channel** at its safe duty, mode `sensor-error`, alert
`sensor` on the transition only) → read watched sensors (errors → value absent) → targets
from curve (linear interpolation) → **curve post-processing** (hysteresis, `min_on`) →
overrides → critical → stall check → slew (first cycle and manual direct) → write
(unchanged duties rewritten every 6th cycle to undo external writes) → verify → snapshot →
history store (section 6a) → periodic log line (`log_every`). Sensors are re-resolved
every 60 cycles. In code the cycle is four steps — `readSensors`, `computeTargets`,
`checkStall`, `writeAndFinish` — called in that order by `cycle`; the safety rules 4–6 live
in `computeTargets`/`checkStall`/`writePhase` unchanged.

**Curve post-processing** (0.3.1; only the curve output is touched — override, critical,
stall, failsafe, slew and the safe duty are exactly as before):

- *Hysteresis* (`hysteresis = N`, °C): the curve is evaluated at a **held temperature**
  per channel, not at the raw reading. The held value follows the reading only when the
  reading differs from it by ≥ N °C (`|raw − held| ≥ N·1000` m°C); N = 0 → held = raw
  every cycle. The held value is initialised with the first good reading, reset on a
  sensor error (`tempOK` false → next good reading is taken as is) and on a sensor id
  change. Critical is judged on the **raw** reading. The snapshot's `temp` is the raw
  reading; `ChannelState.HeldTemp` (`held_temp`, omitted when equal to `temp` or when
  hysteresis is 0) shows the held one.
- *Minimum on-time* (`min_on = D`): when the curve target (after hysteresis) rises above
  the previous cycle's curve target, that higher value is **held** for D: until
  `now < holdUntil` the curve target is `max(curve target, held target)`; a further rise
  **above the held value** replaces it and restarts the timer (a rise that stays below a
  running hold changes nothing — the fan already runs faster). The first curve value after
  start, a sensor error or a sensor change is never a rise. `Options.Now` is the clock
  (monotonic in production). The hold is cleared by a sensor error, a manual override (an
  override replaces the target anyway) and by `min_on = 0s`; `hold_until` is reported while
  the channel is in mode `auto`.
- Order: raw → held temperature → `Interpolate` → min_on → override → critical → stall.
  Both mechanisms are per channel and reload-safe: `Apply` swaps the values with the rest
  of the channel config; a reduced `hysteresis` takes effect on the next reading, a
  reduced `min_on` shortens a running hold (`holdUntil = max(holdStart + newD, now)`).

`pwm4` on the N5 Pro (PCIe header, no tachometer) is an **optional** channel: the config
may carry `[[channel]] pwm = 4` (any name, any sensor; `stop = "auto"` writes
`pwm4_enable = 2` like pwm1/2 — observed idle state on the reference host 16.09.2026:
enable 2, duty 255, fan4 0 RPM. **Measured 17.09.2026:** the driver refuses a `pwm4`
write while `pwm4_enable = 2` (EBUSY, so the regulator's enable-first order is required);
after `enable = 1`, duty 100 and `enable = 2` again the EC left duty 100 in place for
90 s — like pwm3, the EC does not reassert its own duty after a write. `stop = "auto"` on
pwm4 therefore means "the last written duty stays"; an operator who wants a defined state
sets a fixed `stop`);
`SanitizeChannels` neither adds nor corrects it; a channel without tach has `rpm = -1`
and never enters the stall check. The built-in presets do not name pwm4; the merge rule
above keeps it across an apply.

### 6a. History (internal/history)

```go
type Point struct{…}                         // {ts, temp{}, duty{}, rpm{}, extra{}}; control.HistoryPoint is an alias of it
                                             // (history must not import control: control holds the store)
type Store struct{…}
func New(path string, interval time.Duration, now func() time.Time, logf func(string, ...any)) *Store
func (s *Store) Push(p Point)                // called by the controller once per cycle (under its lock is fine: O(1))
func (s *Store) Range(span time.Duration, since int64) []Point   // tier by span: ≤ 2 h raw, ≤ 24 h 1-min, else 5-min; ts ≥ now−span and ts > since
func (s *Store) Save() error                 // atomic JSON, 0600; called every 10 min by the controller loop and from Stop
func (s *Store) Load()                       // at New: file missing/corrupt → empty + one log line; points older than their tier are dropped
func (s *Store) SetInterval(d time.Duration) // reload changed [daemon].interval: raw capacity recomputed, tier trimmed
func RawCapacity(interval time.Duration) int // 2h / interval, at least 1
func CSV(w io.Writer, pts []Point, channels []string, extras []string) error
```

Tiers: **raw** = one point per cycle, 2 h (`historySpan`, capacity `2h / interval` as
before); **1-min** = 1440 buckets of 60 s, 24 h; **5-min** = 2016 buckets of 300 s, 7 d.
A bucket is the arithmetic mean of the raw points that fall into it (temp per channel and
extra id, duty and rpm rounded to int; a value absent in every raw point stays absent; a
negative duty — the controller's `-1` for "unknown after a failed write" — stays in the
raw point but is skipped by the means, so a channel unknown throughout a bucket has no
duty there); `ts` = bucket start; temperatures are rounded to three decimals. Buckets
close when the
first point of the next bucket arrives; the open bucket is included in `Range` with its
running mean and is written to the file as a plain point — `Load` rebuilds it from the
raw points that fall into it, so a restart inside a bucket continues the mean. File
`<state dir>/history.json` (`{format:1, interval, raw[], min1[], min5[]}`, `interval` as a
duration string), ~0.5 MB at 10 s; an unwritable state dir is logged once and the store
runs in memory. `Range` returns copies. The controller's `History(since)` stays (raw tier;
`since` capped at 2 h) and `HistoryRange(span, since)` is added to `Service`; the state
file `state.json` is unchanged.

- Safe duty of a channel = its fixed stop duty when configured (N5 Pro HDD: 140), else 255;
  overrides do not apply while the temperature is unknown. Status becomes `sensor-error`
  only when every channel is affected.
- Stall ends after 3 cycles with RPM > 0. Two consecutive cycles with a write error trigger
  the all-channel failsafe (255). Six consecutive cycles in which the failsafe could not
  write a single channel → `Run` returns `ErrDeviceLost` (`M2`), serve exits 1, systemd
  restarts with a fresh detection.
- `MinHDDOverride = 60`: a manual override below 60 is refused (400) on a channel with a
  fixed stop duty or pwm3 on the N5 Pro.
- `Run(ctx)` calls `Stop()` itself (also after a recovered panic, returned as an error);
  `Stop()` is idempotent, takes only the hardware mutex for the safe-state writes (never
  `c.mu`), makes a running cycle skip its writes and saves the history after the mutex is
  released. serve waits for `Run` (15 s cap). `Failsafe(cfg, dev)` = SafeStop of every
  channel without a controller (`n5-fangov failsafe`).
- `SanitizeChannels` (in `New`, `Apply`, `Failsafe`): drops channels whose pwm the device
  lacks, adds the missing N5 Pro channels, forces `stop = 140` on N5 Pro pwm3; notes go out
  as one `config-channels` alert (deliberately not `config`: serve stamps that kind for
  parse warnings a moment earlier).
- `Reload(rawTOML)` swaps curves, sensors, `[daemon]` and `[dashboard]` values atomically;
  a changed channel set or profile returns `ErrRestartRequired` and applies nothing.
- Alert cooldown lives here: `raise(kind, msg)` checks the stamp `RunDir/alert.<kind>`
  (and its in-memory copy) against `alert_cooldown`; alerts are sent asynchronously.
  The initial snapshot has `status: "starting"`; READY=1 waits for a different status (or
  2 × interval + 5 s).

Snapshot (`GET /api/state`, `/run/n5-fangov/state.json`):

```json
{"ts":1789500000,"status":"ok","profile":"n5pro","verified":true,"hwmon_path":"/sys/class/hwmon/hwmon14",
 "dry_run":false,"uptime_s":435723,
 "channels":[{"name":"cpu","pwm":1,"sensor":"k10temp","temp":36.0,"duty":85,"target":85,"rpm":2000,"mode":"auto"}],
 "extra_temps":{"ec:system":32.0},"watched":{"hwmon:amdgpu:temp1":48.5},"alerts":{"stall":1789490000}}
```

`status`: `starting` | `ok` | `sensor-error` | `write-error` | `dry-run`. Channel `mode`
(`control.Mode`): `auto` | `manual` | `critical` | `stall` | `sensor-error` | `failsafe`.
`temp` is −999 when unknown, `duty` −1 while unknown (write failed), `rpm` −1 without tach;
`duty` is the hardware `pwmN` value, `target` the computed one before slew; `held_temp`
(optional) is the hysteresis-held temperature, `hold_until` (optional, unix ts) the end of
a running `min_on` hold. History point:
`{ts, temp{}, duty{}, rpm{}, extra{}}` maps by channel name (`extra` by sensor id,
omitted when empty).

Alert kinds (one line each, listed by the Alerts tab; cooldown = `alert_cooldown` unless
noted):

| Kind | Raised by | Trigger → reaction |
|---|---|---|
| `sensor` | controller | channel sensor unresolved/unreadable/implausible/frozen → channel at safe duty |
| `stall` | controller | 0 RPM at duty ≥ `stall_min_duty` for `stall_cycles` → channel 255 |
| `temp` | controller | critical temperature → 255 immediately |
| `write` | controller | repeated write/read-back errors → every channel 255 (failsafe) |
| `config` | serve (start) | parse warnings → built-in defaults in effect |
| `config-channels` | controller | channel set corrected (N5 Pro channel added, forced stop, pwm the device lacks) |
| `device` | controller | fan controller unreachable for 6 cycles → daemon exits, systemd restarts |
| `profile` | serve (start) | no fan controller detected → no regulation, fans stay with EC/BIOS |
| `start` | serve (start) | controller could not start |
| `web` | serve | TLS set-up or listener failed → dashboard disabled, regulation continues |
| `tls` | serve (start) | `tls = "file"` pair unreadable → automatic certificate served |
| `kernel` | `check --after-update` | DKMS module missing for a bootable kernel (own 30-min stamp) |
| `restart`, `failed` | onfailure unit | unit failed and came back / stayed down (own 30-min stamps) |
| `schedule` | scheduler (cmd) | a scheduled preset switch failed (preset missing, invalid, write or reload error); the previous curves stay (own 30-min stamp) |
| `test` | Alerts tab, `alerts test` | no cooldown, single-flight, 20 s bound |

serve routes its start-up alerts through `sendAlertCooled` (same stamp files, 30 min), so
a restart loop cannot spam PVE.

### 6b. Schedules (internal/schedule, cmd scheduler.go)

```go
type Entry struct { Preset string; From, To string; Days []time.Weekday; Fallback bool }
func FromConfig(in []config.Schedule) []Entry                    // parsed [[schedule]] tables → entries
func Equal(a, b Entry) bool                                      // same preset, window and days (Days as a set: order and repeats do not count)
func Active(entries []Entry, now time.Time) (idx int, ok bool)   // first windowed entry that contains now, else the fallback, else ok=false
func Next(entries []Entry, now time.Time) (at time.Time, idx int, ok bool)   // next moment the active entry changes (≤ 8 days ahead);
                                                                              // idx = entry active from then (−1 none); ok=false when nothing changes
```

A window contains `now` when the wall clock of `now`'s location (the daemon passes
`time.Local`) is in `[from, to)` at minute granularity on a listed day; a window crossing
midnight (`to < from`) belongs to the day `from` falls in and also matches the early hours
of the following day. `Next` evaluates `Active` at every window boundary and every full
hour of the coming 8 days, so a boundary inside a DST gap or repeated hour is resolved at
the hour mark. The scheduler in cmd ticks every 30 s and evaluates once at start (after
READY): the first evaluation applies the active entry when there is one (a daemon start
inside a window is a transition — after a reboot the window's preset is in effect; a start
with no entries or outside every window is not, nothing is applied or logged); afterwards
it applies a preset only when the active entry (compared by value, `Equal`, or `ok`)
differs from the previous evaluation, through the same `dirPresetStore.Apply` the API uses
(merge by pwm, config text spliced, reload); a switch that leaves every window without a
fallback applies nothing and only logs. A scheduler wired without an apply function logs
the wiring error and applies nothing. Success: one log line `schedule: preset "night"
applied (22:00–07:00)` (`(fallback)` for the fallback). Failure (including
`ErrRestartRequired`): log with the full error + `schedule` alert (cooled 30 min,
`sendAlertCooled`), the previous curves stay, the scheduler retries at the next transition
(not every tick); the alert and `last.error` of the API carry only the failure **class**
— `preset missing`, `preset invalid`, `write failed`, `reload failed`, `restart required`
(else `apply failed`) — never a path or parser text. A manual preset apply or curve
edit during a window is respected — the scheduler acts on **transitions only**, never
re-applies within a window. `Reload` (config PUT, import, preset apply) hands the new
`[[schedule]]` list to the scheduler (`Set(entries)`), which re-evaluates at the next
tick; a changed list that changes the active entry counts as a transition, an unchanged
active entry does not. State for the API: `Status()` → `{entries[{preset, from, to,
days[], fallback, active}], active: idx|-1, next{ts, preset}|null (preset "" when no entry
is active after the switch; null when nothing changes within 8 days), last{ts, preset,
ok, error}|null, timezone}` with `timezone` = zone abbreviation and offset (`CEST +02:00`).

## 7. Alerts (internal/alert)

```go
type Sink interface { Alert(kind, msg string); Name() string }      // fire-and-forget, never panics
type Sender interface { Send(kind, msg string) error }               // delivery error returned, still logged
type ContextSender interface { SendCtx(ctx, kind, msg string) error }
func NewFor(transport, mailTo string, logger Logger) (Sink, string)  // configured → effective sink + its name
func NewForConfig(c Config, logger Logger) (Sink, string)             // Config{Transport, MailTo, WebhookURL, WebhookFormat}; NewFor is the mail-only shorthand
func Available() (pve, mail bool)
type Swappable struct{…}   // Set(Sink)/Get(); the controller and serve hold the Swappable, Configure swaps the inner sink
type Ring struct{…}        // NewRing(inner, path, logger): records every alert that passed the cooldown (ring of 50,
                           // mirrored to <state dir>/alerts.json), Recent(n) newest first, Last() kind → ts
var ErrTestBusy            // second test while one runs (409)
func TemplateFiles() map[string][]byte; TemplateStatus(); InstallTemplate() (path, error)
```

Sinks: `PVE` (`perl -MPVE::Notify`, payload over the environment, template `n5-fangov`,
severity warning, fields `type`, `hostname`, `kind`), `Mail` (`mail(1)`, recipient after
`--`), `Webhook` (below), `Log`, `Off` (journal line `suppressed (transport off)`),
`Multi`. `auto` = pve when `/usr/share/perl5/PVE/Notify.pm` and perl exist, else mail,
else log; `pve`/`mail` without their tool degrade in the same order; `webhook` is never
chosen by `auto` and degrades to `log` (with the effective name `log`) only when its URL
is empty. Delivery is bounded (30 s + wait delay). The PVE template pair exists twice —
`deploy/pve-notification/` (installer) and `internal/alert/templates/` (daemon/CLI) — and
`make verify-deploy` checks they are identical. **Alert texts are ASCII** (kind, title,
message, the `Title` header): the PVE mail path delivers the body without a charset and a
mail client renders `—` as `â€”` (seen 2026-09-18). `alert.ASCII` is applied at the sink
boundary (PVE, Mail, Webhook, Log — `TestAlertTextsASCII`), so whatever a caller composes
is delivered ASCII; the Ring (dashboard, `/api/alerts`) keeps the composed text, and the
literals the daemon composes are kept ASCII anyway (no `—`, `°` in alert-bearing strings).

`Webhook` (effective name `webhook`): one `POST` per alert to `webhook_url` with
`http.Client{Timeout: Timeout}` (system CA pool, no insecure option; redirects not
followed), `User-Agent: n5-fangov/<version>`, headers `X-N5-Fangov-Kind: <kind>` and
`Title: n5-fangov <kind> on <host>` (ntfy reads `Title`). Body by `webhook_format`:
`json` → `Content-Type: application/json`,
`{"type":"n5-fangov","kind":"stall","severity":"warning","hostname":"n5host","title":"n5-fangov stall on n5host","message":"<msg>","ts":1789500000}`
(`title` + `message` are what Gotify and Home Assistant read; ntfy shows the JSON text as
the message); `text` → `Content-Type: text/plain; charset=utf-8`, body `<msg>` (ntfy and
plain receivers). A 2xx answer is success; anything else or a transport error is the
delivery error `webhook: <status or error>` (returned by `Send`, logged by `Alert`). Log
lines, `check` and the CLI name the URL **redacted** (`RedactURL`: scheme, host and the
first path segment — `https://ntfy.example/n5`, `https://ha.example/api/…` —, never the
userinfo, the query or a deeper path; a string that does not parse is cut by the same
rules) because Gotify puts its key into the query and Home Assistant an id into the path;
the full URL appears only in the config file and in `GET`/`PUT /api/alerts` `webhook_url`
for the operator's cookie, Basic and socket callers — a **token caller** of any scope gets
it redacted there as well. The parser's warning about an invalid `webhook_url` names the
value's origin only (`https://h.example/… must not carry user:password (userinfo), ignored`;
`value is not a URL, ignored` when there is no scheme and host), never the value.
`Available()` is unchanged; the panel's "tool available" for webhook is always true.

## 8. Certificates (internal/tlscert) and TLSMgr (cmd)

```go
type Options struct { Dir string; Hosts []string; Org string; Logf func(string, ...any) }
func EnsureAuto(o Options) (tls.Certificate, string, error)   // ECDSA P-256, self-signed, 10 y, IsCA + KeyUsageCertSign,
                                                              // name constraints = exactly the SANs (IPs /32, /128), MaxPathLen 0;
                                                              // SANs always include localhost, 127.0.0.1, ::1; reissued with the
                                                              // same key when the requested SANs are not covered, expired or unloadable
func Reissue(o Options) (tls.Certificate, kept bool, error)   // new certificate, key kept when loadable
func LoadFiles(certFile, keyFile string) (tls.Certificate, error)
func ValidatePair(certPEM, keyPEM []byte, hosts []string) (tls.Certificate, []string, error)
    // every CERTIFICATE block (leaf first), the first *PRIVATE KEY block (PKCS#8, PKCS#1, SEC 1);
    // errors: no PEM, ErrEncryptedKey, key type the server cannot sign with (ECDSA ∉ {P-256,P-384,P-521}, RSA < 1024),
    //         key mismatch, expired, not yet valid, CheckUsable failed
    // warnings: SAN list lacks host <h>, expires in N days (< 30), no SANs, weak key (RSA < 2048), SHA-1/MD5 signature
func ServerConfig(getCert) *tls.Config   // TLS 1.2+, AEAD suites, h2, SessionTicketsDisabled
func CheckUsable(cert) error              // one in-process handshake over net.Pipe
func Info(cert) InfoData                  // Subject, Issuer, DNSNames, IPs, NotBefore/After, FingerprintSHA256, IsCA, KeyAlgo, SerialHex
func Warnings(info, hosts, now) []string; MissingHosts(info, hosts) []string; DaysLeft(notAfter, now) int  // ceil
func PEM(cert) / DER(cert) ([]byte, error); WritePrivate(path, data) error   // 0600, atomic
type Store struct{…}  // NewStore(cert); Get(*tls.ClientHelloInfo) for tls.Config.GetCertificate; Set(cert) → next handshake; Current()
```

`web.Server.ServeTLSStore(ctx, ln, *Store)` is the listener (`ServerConfig(store.Get)`,
HSTS `max-age=31536000` on every answer); `ServeTLS(ctx, ln, cert)` wraps a fixed store.

```go
type TLSMgr interface {          // web.Deps.TLSMgr, implemented by cmd tlsManager
    Info() (tlscert.InfoData, mode string, err error)       // auto | file | off | "auto (fallback from file)"
    ExportPEM() ([]byte, error); ExportDER() ([]byte, error)
    Regenerate(keepKey bool) (tlscert.InfoData, kept bool, error)   // auto only; file → ErrTLSFileMode (409)
    Upload(certPEM, keyPEM []byte) (tlscert.InfoData, []string, error)   // validation failure → ValidationError (400)
    ResetAuto() (tlscert.InfoData, error)
}
type TLSFallback interface{ Fallback() bool }   // optional
```

Manager semantics: tls directory `<config dir>/tls`; auto pair `cert.pem`/`key.pem`; an
upload lands as `custom-cert.pem`/`custom-key.pem` (0600) and sets `[web] tls = "file"`,
`cert_file`, `key_file` via `config.SetKey` — config first, then `Store.Set`; a failed
config write restores the previous files. `ResetAuto` reuses the auto pair, sets
`tls = "auto"` with empty paths and deletes the custom files (paths outside the tls
directory are left alone). Mode `off` (or no TCP listener) → `ErrTLSOff` from everything
but `Info`. Regenerate/Upload/ResetAuto serialise on a mutex; every certificate passes
`CheckUsable` before it reaches the store. **The manager owns the three `[web]` tls keys:**
every cmd store that rewrites the file (config, bundle, presets, alerts, account,
dashboard) is built with `tlsManager.pinConfig func([]byte) []byte`, which re-applies mode
and paths to the text before it is written (byte-identical text is left alone; not applied
under a `--listen` override or without a TCP listener). Fallback: `tls =
"file"` whose pair cannot be served keeps the dashboard up on the auto pair, logs
`FALLBACK`, sends the cooled `tls` alert; Info reports `auto (fallback from file)`, Upload
or ResetAuto end it. CLI `cert reset|upload|regen` work offline on a broken pair;
`cert info|export` need a loadable one.

## 9. Web and API (internal/web)

`web.New(Deps)` yields `Handler()` (TCP: guard) and `SocketHandler()` (unix socket: no
guard, caller `via: "socket"`, hash redacted as well). Guard order on TCP: Host header must
be an IP literal, `localhost`, the listen host or an `allowed_hosts` entry (else 421);
then the caller is resolved once — cookie session → `Authorization: Bearer` API token →
`AuthConfig` Basic — and rides in the request context (`web.CallerFrom`); state-changing
methods need `X-N5-Fangov-Csrf: 1` (else 403) **unless the caller is a Bearer token** (a
browser cannot attach that header cross-site, the CSRF header guards cookie and cached
Basic credentials); then visibility (401 anonymous on a protected path) and, for a token
caller, the **scope** check (403). Anonymous access to a protected path is a silent 401
(no log, no limiter count, no `WWW-Authenticate`). A presented Basic credential or Bearer
token that fails is counted and logged (`web: auth failure from <ip> (user …)` /
`web: bearer token rejected from <ip>: <unknown|expired>`); limiter in three layers: the
delay counter per IPv4 address or IPv6 /64 prefix (zone stripped) — 5 free, then 250 ms
doubling to 2 s, reset after 10 min or a success —, the concurrency cap per full address
— at most 4 delayed attempts in flight, further ones `429` before any PBKDF2, so one host
cannot lock its whole /64 out —, and process-wide at most 4 password verifications at the
same time, whatever the source (`429` beyond). Bearer lookups are one sha256 and go
through the concurrency cap and a delay counter **of their own** (separate buckets per
prefix: a valid token resets only the bearer bucket, a password success only the password
bucket — neither shortens the other's delay), not the PBKDF2 semaphore.
`http.AllowQuerySemicolons` wraps both handlers. Body limits: 256 KiB config/import, 64
KiB certificate upload, 4 KiB override/login/account/token JSON; 413 above. Errors are
`{"error": "..."}` (plus `"errors": [...]` where a list exists). Every write to the config
file is serialised in cmd (`configFileMu`).

**API tokens** (`internal/web/token.go`, store `Deps.TokenFile` = `<state dir>/tokens.json`,
0600, atomic, format 1, cap 50): a token is `n5t_` + 32 random bytes base64url (43
chars), shown once at creation; the store keeps `sha256(token) → Token{ID, Name, Scope,
Created, Expires (zero = never), LastUsed, LastIP}` and mirrors every change to the file;
`ID` = first 8 hex of the hash. `LastUsed`/`LastIP` are refreshed at most once a minute.
Tokens are **not** bound to the credential epoch: a password change, "Sign out other
sessions" and a user rename leave them valid — revocation is explicit. With
`auth = "none"` a Bearer header is ignored (everyone is signed in, `via: "none"`) and `POST /api/tokens` answers 409 `auth is none`.
Name rule `^[A-Za-z0-9][A-Za-z0-9 ._-]{0,31}$`, unique among stored tokens (409).
Scopes, cumulative:

| Scope | Allows |
|---|---|
| `read` (default) | `GET` on `version`, `about`, `session`, `openapi.json`, `state`, `history`, `history.csv`, `system`, `sensors`, `profiles`, `presets`, `presets/{name}`, `alerts`, `dashboard`, `schedules`, `tls` (info only, not the certificate downloads) |
| `control` | read + `PUT`/`DELETE /api/override/{name}`, `POST /api/presets/{name}/apply`, `PUT /api/dashboard` |
| `admin` | everything the dashboard can do **except** token and account management: `/api/tokens*`, `/api/account/*`, `/api/login`, `/api/logout` answer 403 to any token (a leaked admin token cannot mint tokens or change the password) |

Out of scope → 403 `{"error":"token scope read does not allow PUT /api/override/cpu","scope":"read","required":"control"}`.
The scope of every endpoint is a column of the route table (`openapi.go`) — one source
for the guard, the OpenAPI document and the docs. A per-token request limit of 20 req/s
sustained, burst 40 (token bucket per `ID`, in memory) answers 429
`{"error":"token rate limit"}` above; the limiter never blocks cookie or Basic callers.

**OpenAPI** (`GET /api/openapi.json`, public, `Cache-Control: no-store`, ETag = sha256 of
the body, 304 on `If-None-Match`): an OpenAPI 3.1 document rendered once at start from the
route table `(*Server).routeTable() []route{Method, Path, Handler, Class, Scope, Summary,
Body, Response, Responses, Params}` that `routes()` also registers from — the table is the
only place a route is declared (`Response` names the 2xx component schema, `Params` the
query parameters). `info.version` = the daemon version,
`servers: [{url: "/"}]`, security schemes `bearer` (http/bearer), `basic`, `cookie`
(apiKey in cookie `n5fangov_session`); every operation carries `summary`, its parameters
(path `{name}`, query `minutes`/`since`/`lines`/`strict`), `requestBody` with the content
type and, where the body is JSON, a schema from `components.schemas`, the response codes
listed in the endpoint table with descriptions, `x-class` (`public`, `filtered`,
`protected`) and `x-scope`. Component schemas: `Error`, `State`, `Channel`,
`HistoryPoint`, `Override`, `Version`, `Session`, `Token`, `TokenCreate`, `TokenCreated`,
`Alerts`, `AlertsUpdate`, `Schedules`, `Dashboard`. Test `TestOpenAPICoversRoutes`: every
registered pattern appears in the document and vice versa; `TestOpenAPIShape`: version,
summaries, responses, scope values.

`Deps` members and the 501 rule: `Service` (control), `Config` (`ConfigStore`), `Validate`
(`config.Parse`), `Presets` (`PresetStore` + optional `PresetDetailer`, `PresetRenamer`, `PresetChannelSaver`),
`Log LogStore`, `Bundle`, `Profiles`, `Sensors`, `Version`, `Auth AuthConfig`,
`AllowedHosts`, `TLS`, `TLSMgr`, `TLSHosts`, `Logf`, `SessionFile`, `TokenFile`,
`Account`, `Alerts`, `Dashboard`, `Schedules` (`ScheduleStatus func() any`; nil → 501),
`About`, `System func() any`, `Channels func() []string` (history CSV column order; nil →
the snapshot's channel order). A nil store makes its endpoints answer 501.

Visibility with `auth = "basic"` (the UI mirrors it; with `auth = "none"` every caller
counts as signed in, `via: "none"`):

| Class | Anonymous | Signed in (cookie, Basic or token within scope) |
|---|---|---|
| public | full | full |
| public, filtered | reduced (`state`: `ts, status, profile, verified, uptime_s, dry_run, channels[name,pwm,sensor,temp,duty,target,rpm,mode]`; `history`: points without `extra`) | full |
| protected | 401 | full |
| static (`/`, `app.js`, `app.css`, `mock.js`) | full | full |

| Endpoint | Class | Request | Answer |
|---|---|---|---|
| `GET /api/version` | public | — | `{name, version, prerelease, tls, auth, limits{…}}`; `limits` = the validation bounds the UI takes from the daemon: `min_hdd_override`, `critical_min`, `critical_max`, `curve_points_max`, `dashboard_sensors_max`, `password_min`, `password_max`, `hysteresis_max`, `min_on_max_s` (temperature range, point minimum and the name rules are UI constants); the OpenAPI `Version.limits` schema lists exactly these keys (test) |
| `GET /api/about` | public | — | `{name, version, prerelease, license, license_url, repo, author, author_url, go, credits[{name,url,note}]}` |
| `GET /api/session` | public | — | `{authenticated, mode: none\|basic, user, expires?, remember?, via: cookie\|basic\|bearer\|none, scope?, token_id?}` (token caller: `user` = token name) |
| `GET /api/openapi.json` | public | — | OpenAPI 3.1 document (above) |
| `POST /api/login` | public, CSRF, limiter | `{user, password, remember}` ≤ 4 KiB | 200 `{ok, user, expires, remember}` + `Set-Cookie`; 401 `invalid user or password`; 429; 403 for a token caller |
| `POST /api/logout` | public, CSRF | — | 204; cookie cleared, session revoked; 403 for a token caller |
| `GET /api/state` | filtered | — | snapshot (section 6) |
| `GET /api/history?minutes=120&since=TS` | filtered | `minutes` 1..10080 (default 120); tier by span (section 6a) | `[{ts, temp{}, duty{}, rpm{}, extra{}}]`, only `ts > since` |
| `GET /api/history.csv?minutes=N` | protected, `read` | `minutes` as above | text/csv attachment `n5-fangov-history-<host>-<ts>.csv` (`<ts>` = `YYYYMMDD-HHMMSS` in the host's **local** time, the same clock as the `time` column; the log export and the settings bundle names use the same stamp): header `ts,time,<ch>_temp,<ch>_duty,<ch>_rpm,…,<extra id>…` (channels in daemon order, then extra ids sorted), `time` RFC 3339 local, empty cell = absent |
| `GET /api/tokens` | protected, session only | — | `{tokens[{id, name, scope, created, expires (null = never), last_used, last_ip, expired}]}`; 403 for a token caller |
| `POST /api/tokens` | protected, session only, CSRF | `{name, scope: read\|control\|admin, ttl_days: 0..3650}` (scope default `read`, ttl default 90, 0 = never) ≤ 4 KiB | 201 `{ok, token, id, name, scope, expires, warning?}` (`warning` = "token never expires" for 0); 400 rule; 409 `auth is none`, name taken or 50 tokens; 403 token caller (list and revoke work with `auth = "none"`, create does not) |
| `DELETE /api/tokens/{id}` | protected, session only, CSRF | `{id}` = 8 hex | 200 `{ok, revoked}`; 404 |
| `GET /api/schedules` | protected, `read` | — | `{entries[{preset, from, to, days[], fallback, active}], active: idx\|-1, next: {ts, preset}\|null, last: {ts, preset, ok, error}\|null, timezone}`; 501 without a scheduler |
| `GET /api/config` | protected | — | `{raw, config{daemon, web, log, alert, dashboard, channel[], warnings[]}}`; `password_hash` is `<unchanged>` in both |
| `PUT /api/config[?strict=1]` | protected | raw TOML ≤ 256 KiB; `<unchanged>` restores the stored hash | 400 `{error, errors[]}` on a syntax error (nothing written); with `strict=1` also on any `[[channel]]` value that would be replaced by a default (warnings on the other tables stay `warnings[]`); else write (tls keys pinned, `[alert]` re-applied) → reload: 200 `{ok, restart_required:false, warnings[]}` or 202 `{restart_required:true}` |
| `GET /api/config/export` | protected | — | JSON attachment `n5-fangov-settings-<ts>.json`: `{format:1, version, exported, config, presets{}}`, hash redacted |
| `POST /api/config/import` | protected | the bundle | everything validated first; 200 / 202 / 400 `{error: "import rejected: …", errors[]}` |
| `PUT /api/override/{name}` | protected | `{duty}` or `{percent}` ≤ 4 KiB | 200 `{ok, channel, duty, mode}` (applies next cycle; critical/stall win); 400 below `MinHDDOverride` |
| `DELETE /api/override/{name}` | protected | — | 200 `{ok, channel, mode}` |
| `GET /api/presets` | protected | — | `[{name, channels[names], builtin, description}]` |
| `GET /api/presets/{name}` | protected | — | `{name, builtin, description, channels[{name,pwm,sensor,curve,critical,stop,hysteresis,min_on}]}`; 404 |
| `POST /api/presets/{name}/apply` | protected | — | 200 `{ok, applied}` / 202 `{restart_required}`; 404 unknown or built-in of another profile |
| `PUT /api/presets/{name}` | protected | empty, or JSON `{channels[{name,pwm,sensor,curve,critical,stop?,hysteresis?,min_on?}]}` ≤ 256 KiB | empty body: saves the current `[[channel]]` tables; JSON body (`PresetChannelSaver`, else 501): stores the composed channels **without applying** — rendered as `[[channel]]` TOML and parsed with the config rules, every warning the parser would substitute a default for is an error, and the channel names must be the running config's (a preset is applied to this host); 200 `{ok, saved}`; 400 `{error, errors[]}`; 409 built-in; 413 |
| `POST /api/presets/{name}/rename` | protected | `{name}` | 200 `{ok, name}`; 409 built-in or target exists; 404 |
| `DELETE /api/presets/{name}` | protected | — | 200; 409 built-in; 404 |
| `GET /api/sensors` | protected | — | `[{id, description, temp?, kind?}]` (catalogue with live readings; `kind` `ssd`/`hdd` for `disk:*`, section 5) |
| `GET /api/dashboard` / `PUT /api/dashboard` | protected | `{sensors[]}` | `{sensors}` / `{ok, sensors, warnings[]}`; 400 above 8 or unparsable id |
| `GET /api/profiles` | protected | — | `[{name, title, verified, notes, active}]` |
| `GET /api/log?lines=N` | protected | 1..5000 | `{lines[], source: file\|journal}` |
| `GET /api/log/export` | protected | — | text attachment `n5-fangov-<host>-<ts>.log` (current file; journal: newest 100000 lines) |
| `DELETE /api/log` | protected | — | `{cleared:true, note}`; 501 without a file |
| `GET /api/tls` | protected | — | `{mode, info\|null, hosts[], warnings[], fallback}` |
| `GET /api/tls/cert.crt` / `cert.cer` | protected | — | PEM / DER attachment (certificate only) |
| `POST /api/tls/regenerate` | protected | `{keep_key}` (default true) | `{ok, keep_key, kept, info}` + `warning` when a new key; 409 in mode file |
| `POST /api/tls/upload` | protected | multipart `cert`/`key`[/`force`] or JSON `{cert, key, force?}` ≤ 64 KiB | `{ok, mode:"file", info, warnings[]}`; 400 invalid pair; 400 `{error, host, force_required:true}` when the leaf does not cover the session's name (HSTS guard) |
| `POST /api/tls/reset` | protected | — | `{ok, mode:"auto", info}` |
| all `/api/tls/*` except GET | — | — | 409 `tls is off` in mode off |
| `GET /api/account` | protected | — | `{user, mode, sessions[{id, created, expires, last_seen, remember, ip, current}]}` |
| `POST /api/account/password` | protected | `{current_password, new_password}` (8..128) | `{ok}`; 403 `current password wrong` (counted); 409 `auth is none` |
| `POST /api/account/user` | protected | `{current_password, user}` | `{ok, user}`; same errors |
| `POST /api/account/sessions/revoke` | protected | `{others:true}` | `{ok, revoked}` |
| `GET /api/alerts` | protected | — | `AlertStatus{transport, effective, mail_to, webhook_url, webhook_format, pve_available, mail_available, template{installed,current,writable,path,reason}, cooldown, kinds[]} + {last{kind:ts}, recent[{ts,kind,msg}]}`; `webhook_url` full for cookie, Basic and socket callers, redacted (`RedactURL`) for a token caller of any scope |
| `PUT /api/alerts` | protected | `{transport, mail_to, webhook_url?, webhook_format?}` (omitted keys keep the value **in the file** — the merge basis is the `[alert]` section read under the file lock, only the keys sent are written) | `{ok, status}` (`webhook_url` redacted for a token caller as in `GET`); 400 (invalid URL, format, mail_to; `webhook` without URL) |
| `POST /api/alerts/test` | protected | — | `{ok, transport}`; 502 `{error, transport}` delivery failed; 409 test in progress |
| `POST /api/alerts/template` | protected | — | `{ok, path}`; 501 no PVE; 500 with the CLI hint |
| `GET /api/system` | protected | — | `sysinfo.Info` verbatim (section 10), `Cache-Control: no-store` |
| other `/api/*` | — | — | 404 `{error}` |

Sessions: cookie `n5fangov_session=<token>; Path=/; HttpOnly; SameSite=Strict;
Max-Age=<remaining>`, `Secure` when the request came over TLS or `behind_tls_proxy` is
set. Token 32 random bytes base64url; lifetime 12 h, 30 days with remember; `LastSeen`
refreshed at most once a minute. `SessionStore` keeps `sha256(token) → Session{ID, User,
IP, Created, Expires, LastSeen, Remember}` in memory and mirrors it to `SessionFile`
(0600, atomic) on every change; cap 50, oldest dropped; `ID` = first 8 hex of the hash.
`Create`, `Lookup`, `Revoke`, `RevokeAll(keep)`, `RevokeAllRename(keep, newUser)`,
`SetEpoch(epoch)`, `List`. `web.New` binds the store to `CredentialEpoch(user, hash)`: a
password rotated outside the dashboard drops every persisted session at the next start
(`R-M1`). Account handlers move the epoch, then revoke everything but the caller.

`AccountStore.Update(user, passwordHash)` rewrites `[web]` in place and returns the
`AuthConfig` now in effect (atomic pointer in the server). `AlertMgr`: `Status()`,
`Recent(n)`, `Test()`, `InstallTemplate()`, `Configure(AlertSettings{Transport, MailTo,
WebhookURL, WebhookFormat})` (`*string` members, nil keeps the value in the file — the basis is the `[alert]` section parsed from the file under `configFileMu`, not the section in effect, and only the set keys are written; the merged
section is validated as a whole) — the template probe is cached 10 min. `DashboardStore`:
`Sensors()`, `SetSensors(ids)`. `TokenStore` (`NewTokenStore(path, logf)`): `Create(name,
scope string, expires time.Time) (secret string, t Token, err)`, `Lookup(secret, ip) (Token,
ok, expired bool)` (expired → not ok, `expired` names the reason for the log line; a hit
touches LastUsed/LastIP), `Revoke(id) bool`, `List() []Token` (expired tokens stay listed
until revoked).
`Bundle`: `Export()`, `Import(b) (restartRequired, error)` with `Errors() []string` on the
error. `LogStore`: `Lines(n)`, `Export(w)`, `Clear()`, `Path()`.

## 10. System inventory (internal/sysinfo)

```go
type Info struct { Host; Machine; CPU; Memory; GPUs []GPU; NPUs []NPU; NICs []NIC; Storage; FanController; Collected, StaticAt int64; Errors []string }
type Options struct { Sysfs, Proc, OSRelease, LSPCI string; Hostname func() (string, error); Fan FanController; Now func() time.Time; CacheTTL time.Duration }
func NewCollector(Options) *Collector; func (*Collector) Collect() Info
```

JSON keys snake_case; slices never null. Every source is read on its own; a failure adds
one `errors` line ("source: reason") and leaves the section empty. Sources: Host
(hostname, `/etc/os-release`, `/proc/sys/kernel/osrelease`; live `/proc/uptime`,
`/proc/loadavg`), Machine (`/sys/class/dmi/id/*`), CPU (`/proc/cpuinfo`, `cpufreq`),
Memory (live `/proc/meminfo`; modules from SMBIOS type 17 in
`/sys/firmware/dmi/tables/DMI`, ECC from type 16, version from `smbios_entry_point`),
PCI (`/sys/bus/pci/devices/*`, names from `lspci -mm -D` with a 5 s timeout, `Options.LSPCI
= "-"` never; without lspci `PCI device vvvv:dddd`), GPUs (`/sys/class/drm/card*` +
class 0x03), NPUs (`/sys/class/accel/*`, amdxdna/intel_vpu/ivpu, `/sys/module/<drv>/version`),
NICs (`/sys/class/net/*` with a `device` link; live operstate/duplex/speed/mtu), Storage
(controller classes nvme/sata/sas/scsi/raid; `/sys/block/*` minus virtual devices; per
disk the **live** `temp_c` from the same hwmon `disk:<dev>` uses, `null` without one, and
`sensor` = `disk:<dev>` when readable), FanController (module link + version). Static
parts cached `CacheTTL` (10 min) under a mutex, disk temperatures re-read on every
`Collect`; the sysfs root follows `N5FANGOV_SYSFS`. No serial numbers. cmd wires
`newSystemCollector(fs, dev)` into `Deps.System`; `n5-fangov system` asks the socket
first and collects locally otherwise.

## 11. Dashboard (internal/web/static)

No framework, no build step; `app.js` ≤ **96 KB** and `mock.js` ≤ **40 KB** (test
`web_test.go`), CSP `script-src 'self'`, no `innerHTML`, no inline handlers. Colours,
spacing, radii and type scale are tokens in one `:root` block (dark; light overrides under
`[data-theme="light"]` and `system`) and a JS constant block after `cssVar`; the mock's
version string is one constant in `mock.js`, bumped with the release.

**Mock split:** the mock lives in `mock.js` (served by the static handler as the fourth
file, never referenced by `index.html`). With `?mock=1` `app.js` inserts
`<script src="mock.js">` and boots on its `load` event; `mock.js` publishes one function
`window.n5mock(path, opt) → Promise<{status, body, filename?}>` that `api()` calls
instead of `fetch`. Without the parameter the production page never requests `mock.js`.
The mock implements every endpoint of section 9 including tokens (`n5t_mock…`),
schedules, history tiers (24 h / 7 d synthesised), CSV, webhook status and `disk:*`
sensors.

- **Header:** brand, profile title, verified badge; status chip, uptime, version with
  `beta` badge when `prerelease != ""`, lock button (`🔒 TLS` / `🔓 HTTP`; warn colour
  when plain HTTP off loopback, during a certificate fallback or below 30 days to expiry),
  live/paused indicator, user name, *Sign in* / *Sign out*, settings gear. Every tab
  except Overview and About carries `data-auth` and is hidden while anonymous; gear, lock,
  user name, *Sign in* / *Sign out* and the CSV button are toggled by the id list in
  `applyAuth` (no `data-auth`).
  Below 700 px the header is one line and the tab bar is sticky.
- **Tabs** (`role="tablist"`, keyboard): Overview, Curves, Manual, Presets, Alerts, System,
  Log, Compatibility, About. Every tab id has a dispatch entry (`TestTabsHaveHandlers`).
- **Overview:** channel cards (temperature coloured by the channel's critical, duty bar with
  target marker, mode badge, RPM — `no tach` for `rpm = -1`; a small `held` value when
  `held_temp` differs, a `hold` badge with the remaining time while `hold_until` is set),
  temperature and fan-speed charts (RPM/duty toggle) with a **range selector** `2 h · 24 h ·
  7 d` (persisted in `localStorage`; 2 h polls `since` every 30 s, 24 h/7 d reload the
  averaged tier every 60 s; x-axis shows `HH:MM` for 2 h, `Www HH:MM` for 24 h, `dd.mm
  HH:MM` for 7 d) and a *CSV* button (signed in; `download('/api/history.csv?minutes=…')`);
  signed in: Sensors card (catalogue grouped CPU / SSD·NVMe / HDD / GPU / NIC / EC·board /
  other by `kind`, id prefix and hwmon name — `disk:*` goes to SSD·NVMe or HDD by `kind` —,
  *chart* toggle → `PUT /api/dashboard`), Extra sensors chart (when the watched list is
  non-empty), System card (at-a-glance inventory, refreshed every 30 s), Recent alerts.
- **Curves:** one editor per channel — sensor select (a composite id is listed as one
  option `a,b (max of 2)` so the editor never drops it; the catalogue's single ids follow),
  critical, stop, **hysteresis** (0..10) and **min_on** (select `off · 30 s · 1 min · 2 min ·
  5 min · 10 min · 30 min · 1 h`), canvas with drag points, `crit` line and `now` marker,
  point table with *remove* and *+ add point* (inserted at the middle of the widest gap,
  table kept sorted), **keyboard**: a focused point (points are focusable, `tabindex`)
  moves with the arrow keys by 1 °C / 5 duty, Shift × 5, measured duty→RPM reference on
  the N5 Pro. Client validation before the PUT: 2..8 points, temperatures ascending,
  duties non-decreasing, critical above the last point, stop `auto` or 60..255,
  hysteresis 0..10; errors in a `role="alert"` notice (no toast on top: one
  announcement). *Apply to daemon* sends `PUT /api/config?strict=1`; the `[[channel]]`
  tables it writes carry `sensor` (array form for a composite), `hysteresis` and
  `min_on` (omitted at their defaults). 202 shows the restart notice; the notice and
  the warnings survive the editor reload after Apply and stay until *Revert*, the next
  Apply or a session change. A dirty indicator on the tab and in the panel marks unsaved
  edits; the edits persist across tab changes (no dialog), *Sign out* asks before
  discarding them, a session loss stashes them for the next sign-in (below), and the
  browser's `beforeunload` prompt covers a page close.
- **Manual:** slider + *Set* / *Back to auto* per channel; HDD-like channels show the
  minimum-60 hint and refuse lower values client-side.
- **Presets:** until 0.4.0 a card list; now the Presets row of the Fans page (section 11a):
  chips with built-in/recommended badges and description, *Apply*, *Details / Edit* (the
  preset editor dialog, read-only for a built-in), *Delete* (user presets), *Save current
  as…* (client-side name rule, built-in names refused; the editor composes the channels and
  saves them with `PUT /api/presets/{name}` + JSON body, nothing is applied).
- **Schedules:** editor card, one row per entry of `GET /api/schedules` — preset select
  (from `/api/presets`; a missing name stays selectable as `<name> (missing)`, flagged),
  *From* / *To* (`<input type=time>`), day toggles Mo…Su (`aria-pressed`, none = every
  day), fallback row (*fallback — outside every window*, *add window* / *make fallback*),
  ACTIVE badge, *Remove*; *Add entry*, *Revert*, *Save* (disabled while clean; dirty
  indicator, leave-page / sign-out / `beforeunload` guards). Client validation: preset
  set, `HH:MM` both on a windowed entry, from ≠ to, one fallback, ≤ 16 entries; errors in
  a `role="alert"` notice. Save = raw config → `stripSchedules` (`[[schedule]]` blocks
  dropped up to the next header of any kind, `TOML_HDR`) + the editor's tables (`from`/`to`
  only when windowed, `days` only when not every day) → `PUT /api/config?strict=1`; 200
  toast, 202 restart notice, 400 the server's errors (`TestScheduleEditorKeepsOtherTables`).
  Status card: active entry, next switch (`<time>` absolute + relative), last switch with
  its error in `--crit`, timezone; polled every 60 s while current, a dirty editor is not
  rebuilt. 501 → "scheduler unavailable"; no entries → "No schedule — the daemon keeps
  the curves it has …". Editor and status side by side from 1400 px; below 700 px every
  entry is a stacked block (preset · remove / from · to / days), no sideways scrolling.
- **Alerts:** transport form (select incl. `webhook`; `mail_to` for auto/mail,
  `webhook_url` + `webhook_format` for webhook, *Save*), effective transport and tool
  availability, template card with *Install / Update template* (disabled with reason),
  *Send test alert*, cooldown, kinds table with last delivery, recent alerts.
- **System:** Host / Machine / CPU / Fan controller cards, Memory with module table,
  GPU · NPU, Network, Storage (with a temperature column from `temp_c` and a sum row),
  notice for `errors`, "live … · static … ago", *Refresh*; polled every 30 s while current.
- **Log:** filter, auto-scroll, *Refresh* / *Export* / *Clear* (confirm), WARN/ERROR colouring.
- **Compatibility:** profiles with ACTIVE and verified/untested badges. **About:** name,
  version, description, licence, repository, author, credits, releases link (the mock hint
  appears in mock mode only).
- **Dialogs** (`<dialog>`, focus trap, Escape): Login (user, password, *Remember me*,
  inline error, the recovery hint *Forgot the password? On the host, as root:
  `n5-fangov passwd`* — root on the box is the only recovery path, by design), Account (*Change password…*, *Change user…*, *Sign out other sessions*,
  sessions table with THIS SESSION, **API tokens** section: table name · scope · created ·
  expires · last used · last address · *Revoke* (confirm), *Create token…* form (name,
  scope select with one-line explanations, expiry select `30 d · 90 d · 1 y · never`),
  after creation the secret once in a read-only field with *Copy* and the notice "shown
  only now", a `warn` notice for *never*), Certificate (badge, key/value block, SAN chips,
  fingerprint + copy, notice with the server's warnings, Download .crt/.cer, Regenerate…
  with *generate a new key*, Upload own certificate… with file inputs + PEM textareas and
  the *install anyway* checkbox after `force_required`, Back to auto, collapsible "How to
  trust this certificate"), Confirm/Prompt (generic, replaces the native dialogs).
- **Settings popover** (gear): unit °C/°F, refresh interval 5/10/30 s, theme
  dark/light/system, Export/Import settings (fetch + blob, so the credential and the CSP
  stay), Certificate…, Account…. Persisted in `localStorage` (`n5-fangov`, whitelisted values).
- **Connection banner** after two failed polls; a 401 on a protected call shows "Session
  expired" and returns to the anonymous Overview (unsaved curve edits are kept for the next
  sign-in). **Toasts:** at most 3 visible per region, the oldest is dropped when a fourth
  arrives.
- **Mock:** `?mock=1` anonymous, `&user=1` signed in, `&auth=none`, `&tls=off|file|soon|fallback`,
  `&tab=<id>`, `&syserr=1`, `&reject=1` (strict PUT 400), `&restart=1` (PUT /api/config
  answers 202), `&expire=1` (session dies after 15 s), `&schedfail=1` (last schedule
  switch failed), `&pwm4=1` (fourth channel `pcie`,
  pwm 4, no tach); every endpoint above is implemented (login `admin`/`admin`); names and
  addresses are documentation values (`n5host`, `192.0.2.x`, `n5.lan`).

### 11a. Target structure for 0.4.0 (redesign contract)

Decided 2026-09-18 from `docs/design/REDESIGN-CONCEPT.md` and the 0.3.1 release-gate
findings. Until 0.4.0 ships, the bullets above describe the running dashboard; this
subsection is the contract the redesign is built and reviewed against. What the concept
lists under *What does not change* holds: anonymous view, API, mock, CSP, no framework,
visibility model, keyboard and screen-reader behaviour, the backend packages.

**Budgets.** `app.js` ≤ **128 KiB**, `mock.js` ≤ **48 KiB**, `app.css` ≤ **48 KiB**
(`web_test.go`; the 96 KiB budget is exhausted at 93 KB and the mock is already a
separate file, so the room goes to the shell, the Fans page, the preset editor and the
schedule editor). `index.html` carries the SVG sprite and has no budget.

**Shell.** One `nav` (sidebar) + one page header + one `main` with one `section` per
page. Pages and groups:

| Group | Page id | Content | Anonymous |
|---|---|---|---|
| Monitor | `overview` | tiles, charts, sensors, system glance, recent alerts | yes (tiles + charts only) |
| Monitor | `system` | the inventory of the System tab | no |
| Control | `fans` | Curves + Manual + Presets, channel-centric | no |
| Control | `schedules` | editable schedule table + status | no |
| Operate | `alerts` | effective transport, kinds with last delivery, recent alerts, *Send test alert* | no |
| Operate | `log` | the Log tab | no |
| Settings | `settings` | Display · Account & sessions · API tokens · Certificate · Alert transport · Backup · Danger zone | no |
| Info | `about` | the About tab + the Compatibility card (profiles table; deep link `#about/compat`) | yes (the card is signed-in only) |

- Sidebar (decided 2026-09-18: sidebar, expanded by default; no top-bar variant): 220 px
  expanded, 56 px icon rail (state in `localStorage`, key `nav`), group
  captions in 11 px caps; the current page carries `aria-current="page"` and a 2 px
  accent bar; every entry is a `button` in a `ul` (Tab order, Enter/Space), no roving
  tabindex. Below 700 px the sidebar is a fixed **bottom bar** with Overview · Fans ·
  Alerts · Settings · *More* (a sheet listing the remaining pages); `main` gets the bar's
  height as bottom padding. Entries except Overview and About carry `data-auth`.
- Routing: `location.hash = '#' + pageId`; the 0.3 mock parameter `&tab=` maps
  `curves|manual|presets → fans` and is kept for the screenshot script. Every nav entry
  has a page section and a dispatch entry (`TestNavHasPages` replaces `TestTabsHaveHandlers`).
  Eight entries: Overview, System, Fans, Schedules, Alerts, Log, Settings, About.
- Page header: page title (20/600) left; right: status chip, uptime, version + `beta`
  badge, live indicator, **certificate warning chip** only while the certificate is in
  fallback, expires in < 30 days, is expired, or the listener is plain HTTP off loopback
  (click → Settings → Certificate), user name, *Sign in* / *Sign out*. The gear popover
  is gone (its content is Settings → Display / Backup). Below 700 px: title, status chip,
  live dot, user button.
- Icons: one inline SVG sprite at the top of `index.html` (`<svg hidden><symbol id="i-…">`),
  used as `<svg class="ic" aria-hidden="true"><use href="#i-gear"/></svg>`; 16 px,
  `currentColor`, `stroke-width 1.75`. Icon-only controls carry `aria-label`; icons next
  to text are `aria-hidden`. Set: gauge, chip, fan, clock, bell, list, gear, check-list,
  info, lock, unlock, warn, close, external, chart, plus, trash, edit, copy, chevron-left,
  chevron-down, more, user, sign-in, sign-out, play, check.

**Overview.** Tiles keep temperature (32/700 on the redesign, coloured by critical), mode
badge, duty bar with target marker, RPM, held/hold; a 2-hour temperature **sparkline**
(canvas, `--spark-h`) sits between value and bars (decided 2026-09-18: yes).
Charts and range switch as today; the extra-sensor chart is a third chart of the same
kind. The Sensors card lists every disk individually under its group with the *chart*
toggle. Empty states name the next step.

**Fans page.** One card per channel, header `name · pwmN · sensor` + mode badge + the
name of the preset whose channel values match (`chKey`) as a badge. Left: the curve
editor of the Curves tab (fields sensor / critical / stop / hysteresis / min_on, canvas,
point table). Right, *Live & override*: now-temperature → duty target, an explicit
**Auto / Manual switch** (`role="switch"`, gate finding: switching to Manual holds the
duty the channel runs at that moment — `PUT /api/override/{name}` with the snapshot duty,
raised to the HDD minimum where it applies —, the slider + number field and *Set* then
change it; switching back to Auto is the `DELETE /api/override/{name}`; the slider block
is dimmed while the switch is off), the HDD minimum hint. The live block refreshes with
every state poll without rebuilding the editor. Below the cards a **Presets row** of
chips (active ●, recommended ★, built-in badge, description, *Apply* with confirm,
icon buttons *Details* (built-in, read-only) / *Edit* (user preset) and *Delete*; rename
happens in the editor's name field → `POST …/rename` before the save); *Save current as…*
opens the **preset editor** dialog (*Start from*: the editor's unsaved values, the
daemon's running curves or any preset; name; per-channel critical / stop / hysteresis /
min_on and an editable point table, 2..8 points, validated with the curve editor's rules;
no description — user preset files carry none; *Save* = `PUT /api/presets/{name}` with the
composed channels — the editor's values, not the daemon's — nothing is applied). Sticky action bar with the dirty indicator, *Revert*,
*Apply to daemon* (unchanged semantics, `PUT /api/config?strict=1`, `[[channel]]` splice).
Decided 2026-09-18: all channels stacked on desktop; below 700 px a channel selector
(segmented control above the card) shows one channel at a time — a breakpoint, not a setting.
The dirty indicator then sits on the selector entry of the edited channel as well.

**Schedules page.** The read-only card becomes an editor: one row per entry — preset
(select from `/api/presets`), from / to (`<input type=time>`), days (seven toggle
buttons, none = every day), *fallback* when the window is empty, ACTIVE badge —, *Add
entry*, *Remove*, *Save* (`PUT /api/config?strict=1` after splicing the `[[schedule]]`
tables the same way *Apply* splices `[[channel]]`; the daemon reloads schedules without a
restart). Status block: active entry, next switch, last switch with its error, timezone.
No backend change: the config PUT already accepts the tables.

**Alerts page.** Status (effective transport, tool availability, cooldown) with a link
to Settings → Alert transport, kinds table with last delivery, recent alerts, *Send
test alert*. **Settings page** (`section` per block with `id="st-…"`, sub-navigation on
the left ≥ 900 px, sticky; below that a stacked page with the sub-navigation as chips on
top): *Display* (unit, interval, theme), *Account & sessions* (the account dialog's
content), *API tokens* (the tokens panel), *Certificate* (the certificate dialog's
content incl. *How to trust*), *Alert transport* (transport form, mail_to, webhook URL +
format, *Save*, *Send test alert*, PVE template card), *Backup* (export / import bundle),
*Danger zone* (clear log, reset certificate, regenerate with new key). Login,
Confirm/Prompt and the preset editor remain `<dialog>`s; Account and Certificate dialogs
are removed. Deep link `#settings/cert` scrolls to the section.

**Tokens added to `:root`.** `--nav-w:220px; --rail-w:56px; --bottom-h:56px;
--spark-h:28px; --fs-20:20px; --fs-32:32px; --z-nav:15; --nav-active:color-mix(in srgb,
var(--info) var(--tint-bg),transparent); --ic:16px; --ic-stroke:1.75`. Light theme
overrides nothing new. The `--hdr-h` measurement stays for the sticky action bar.

**Acceptance.** Every task of `docs/DESIGN-AUDIT.md` §4 in the same or fewer clicks
(curve change 2, manual set 2 + switch, preset apply 2, password change 2 from the
Settings page, certificate download 2); contrast table §5 recomputed for every new
pair (nav text, active entry, sparkline stroke); 375 px pass with the bottom bar; no
console messages in every mock state; `docs/screenshots/` regenerated for every page.

## 12. Deploy

`deploy/n5-fangov.service`: `Type=notify`, `NotifyAccess=main`, `WatchdogSec=60`,
`ExecStartPre=/usr/bin/n5-fangov check --quiet`, `ExecStopPost=/usr/bin/n5-fangov failsafe`,
`Restart=always`, `RestartSec=5`, `StartLimitBurst=5` / `StartLimitIntervalSec=300`,
`OnFailure=n5-fangov-onfailure.service`, `Conflicts=n5-fand.service`,
`After=systemd-modules-load.service`, `TimeoutStopSec=20`, `Nice=-5`. Directories:
`RuntimeDirectory=n5-fangov` (0750, preserved), `LogsDirectory=n5-fangov` (0750),
`StateDirectory=n5-fangov` (0700), `UMask=0077`. Sandbox: `NoNewPrivileges`,
`ProtectSystem=strict` + `ReadWritePaths=-/etc/n5-fangov -/run/n5-fangov -/var/log/n5-fangov
-/var/lib/n5-fangov -/sys/class/hwmon -/sys/devices/platform -/var/spool/postfix/maildrop
-/etc/pve/notification-templates` (the pwm files of every supported chip live under
`/sys/devices/platform/<driver>/hwmon/hwmonN` — it5571, nct6775, it87 are platform
drivers; `/sys/class/hwmon` holds the symlinks the daemon opens; everything else under
`/sys` is read-only, which is all the sensors, DMI and disk temperatures need — narrowed
from `-/sys/devices` in 0.3.1), `ProtectHome`, `PrivateTmp`, `PrivateDevices`,
`ProtectKernelTunables=no` (**must stay** — pwm files are kernel tunables),
`ProtectControlGroups`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK`,
`RestrictNamespaces`, `LockPersonality`, `MemoryDenyWriteExecute`, `RestrictRealtime`,
`SystemCallArchitectures=native`, `SystemCallFilter=@system-service`,
`CapabilityBoundingSet=` (empty). Verified on the reference host: the sandbox writes pwm
files, reads the DMI tables, runs `lspci`, writes into pmxcfs. The `mail(1)` path on
non-PVE hosts additionally needs `CAP_DAC_OVERRIDE` (docs/08-https-security.md "Hardening").

Watchdog: the loop sends `WATCHDOG=1` every cycle; serve pings every 10 s from a ticker
**only while the loop is alive** (`LastCycle()` within 3 × interval); `interval` is capped
at 30 s so two cycles fit into 60 s. `n5-fangov-onfailure` reads
`Result/ExecMainCode/ExecMainStatus`, sleeps 8 s, polls `is-active` up to 25 s while
`activating`; `active` → `restart` alert, still `activating` → "restart in progress" (same
kind), else `failed`; cooldown 30 min via `/run/n5-fangov/alert.<kind>`.

Runtime files: `/run/n5-fangov/n5-fangov.sock` (CLI, root only), `state.json`,
`override.<name>`, `alert.<kind>`; `/var/lib/n5-fangov/sessions.json`, `tokens.json`,
`alerts.json`, `history.json` (0600; an unwritable state dir is logged once, the daemon
runs without persistence; `.gitignore` and the pre-commit file guard list all four);
`/etc/n5-fangov/{config.toml, presets/, tls/}`; `/var/log/n5-fangov/n5-fangov.log[.N]`.
The settings bundle (`export`/`import`) carries config and presets only — never tokens,
sessions or history. The password hash is redacted (`<unchanged>`, restored on import from
the running config); **the bundle carries the webhook URL** as it stands in the config
(with the receiver's key): it is a setting the bundle exists to move between hosts, and the
placeholder mechanism is the password's — treat the file like the config.

Install (`deploy/install.sh`, needs `dist/n5-fangov` or `./n5-fangov`; refuses on a host where the
package is installed, as does `uninstall.sh` — apt maintains those): stops and disables
`n5-fand.service`, installs binary, onfailure script (`/usr/libexec/n5-fangov/`), both
units (`/etc/systemd/system/`), `/etc/n5-fangov/{,presets}`, the log directory, the config
example and the deploy README (`/usr/share/doc/n5-fangov/`), the PVE templates
(`/usr/share/n5-fangov/pve-notification/` and, when `/etc/pve` exists and pmxcfs is
writable, `/etc/pve/notification-templates/default/`), the apt hook
(`/etc/apt/apt.conf.d/90n5-fangov`), `daemon-reload`, enable; writes no config and starts
nothing. `uninstall.sh [--purge]`: disable, failsafe, remove units/binary/hook/templates/
`/run` and `/var/lib` state; `--purge` also `/etc/n5-fangov` and `/var/log/n5-fangov`. The
`.deb` (`make deb`, no debhelper, no conffile, `DEBIAN/md5sums` for `dpkg -V`) does the
same through `postinst`/`prerm`/`postrm`; `deploy/debian/copyright` (DEP-5) names GPL-2.0
and the MIT text of toml. **Switching from `install.sh` to the package** (release-gate
finding 2026-09-18): `install.sh` puts the units under `/etc/systemd/system/`, the package
under `/lib/systemd/system/`; the `/etc` copy would shadow every later package update and
survives `apt remove`. `postinst` therefore removes `/etc/systemd/system/n5-fangov.service`
and `n5-fangov-onfailure.service` when they are byte-identical to the package's units (and
says so), restarts a running daemon so the packaged binary takes over, and only warns — naming the file — when they differ (an operator's edit is never
deleted; a drop-in under `n5-fangov.service.d/` is untouched either way).

apt hook: `DPkg::Post-Invoke { "if [ -x /usr/bin/n5-fangov ]; then /usr/bin/n5-fangov check
--after-update || true; fi"; };` — the gate requires `updates/dkms/minisforum_n5_it5571.ko`
for every kernel the box can **boot into** (running + `proxmox-boot-tool kernel list`
selection; without the tool: running + newest installed); other installed kernels are
info lines; missing → printed + `kernel` alert; the apt run never fails. `check`
(ExecStartPre) is fatal (exit 1) only when serve itself cannot run: config exists but is
unreadable, no device for the profile, a managed `pwmN`/`pwmN_enable` does not open for
writing (`O_WRONLY` probe, nothing written). Everything else is a `warn` line with exit 0.

Versioning: `internal/version.Version` is the only literal; `make` overrides it with `git
describe` (`-X`). Debian version: leading `v` stripped, `-alpha|-beta|-rc` → `~` (sorts
before the release), every other `-` → `+` (`X.Y.Z~beta.1+3+gabcdef`). `make release`
(clean tag, gh CLI) uploads the static binary and its sha256; `-` in the version marks a
pre-release. **One release, one binary:** the release workflow builds `dist/n5-fangov` once
with `Version=<tag without v>` and packages that file (`make deb-only`, which does not
depend on `build`); the `.deb`'s binary is asserted byte-identical to the uploaded asset
(sha256) before the release is created. The version literal never carries a leading `v` —
`n5-fangov version`, `/api/version`, the `User-Agent` and the alert texts print `0.3.1`;
the dashboard adds the `v` for display.

## 13. Testing

`go vet ./... && go test ./...` must pass in the Docker builder (`tools/remote-go.ps1`,
image `golang:<go.mod version>-alpine`, `CGO_ENABLED=0`). `make test-race` runs
`CGO_ENABLED=1 go test -race -count=1 ./...` in a cgo-capable image
(`tools/remote-go.ps1 -Image golang:<version>-bookworm -Cmd "make test-race"`) — once per
release. Fake sysfs `testdata/sysfs/n5pro` mirrors the reference host with generic names;
the PCI directories and symlinks the sysinfo tests need are created in a temp copy (a
Windows checkout cannot hold them). Controller tests use a fake `Device`
(`fakes_test.go`); the safety rules are pinned in `safety_test.go`. `TestServeSmoke`
starts the real `cmdServe` (dry-run, fake sysfs, TCP + socket, override, SIGTERM).
Frontend: `web_test.go` checks the JS budgets (`app.js`, `mock.js`), that `index.html`
does not reference `mock.js`, the tab/handler list, the CSP and the contrast of the
primary buttons; the mock (`?mock=1`) is the manual test bed. `TestOpenAPICoversRoutes`
pins the route table to the document. `internal/history` is tested with a fake clock
(bucket means, tier cut-offs, save/load round trip, corrupt file); `internal/schedule`
with fixed times (midnight crossing, days, fallback, next switch, DST day). Disk sensors
and the sysinfo temperatures are tested in a temp copy of the fake sysfs with
`block/<dev>/device/hwmon/hwmonN/temp1_input` (plain directories; a Windows checkout cannot
hold the symlinks). No test writes into `testdata/`.

## Review tags

Code comments and test names carry **no** review tags any more (0.3.1). What the review
rounds of v0.1–v0.3.0-beta found and where it was fixed is the legend
[docs/REVIEW-TAGS.md](docs/REVIEW-TAGS.md) (`M1`–`M7`, `H1`–`H4`, `L1`–`L9` per package;
`R-M1`–`R-M3`, `R-L4`–`R-L11`); the comments keep the *reason*, not the tag. The audit
lists are [docs/AUDIT.md](docs/AUDIT.md) and [docs/DESIGN-AUDIT.md](docs/DESIGN-AUDIT.md).
