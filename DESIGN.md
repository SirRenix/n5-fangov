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
behaviour is described in [README.md](README.md), the deploy file layout in
[deploy/README-DEPLOY.md](deploy/README-DEPLOY.md).

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
  serve.go           the daemon: config → log file → profile → controller → TLS → web/socket → signals
  setup.go           setup, passwd (interactive prompts, password from file/stdin)
  cli.go             status, set, auto, curve, log (socket client, state.json fallback)
  check.go           check [--quiet] [--after-update]   (ExecStartPre, apt hook)
  detect.go          detect
  testrun.go         test <ch> [--force]
  failsafe.go        failsafe (ExecStopPost)
  cert.go/cert_cli.go, tlsmgr.go   certificate manager (TLSMgr) and `cert` CLI
  alerts_cli.go      alerts status|test|template
  system_cli.go      system [--json]
  bundle.go          export, import (settings bundle)
  wiring*.go         adapters between internal/* and web.Deps: stores (config, presets,
                     account, alerts, dashboard), log store, alert manager, system collector
  common.go, prompt.go   socket client, helpers, no-echo prompts
internal/config/     TOML config: Parse (defaults on error), Load, Save, SetKey (in-place edit), presets, built-in presets
internal/hwmon/      sysfs discovery + read/write helpers (root from N5FANGOV_SYSFS); hwmontest fixtures
internal/profile/    Profile/Device interfaces; n5pro, nct67xx, it87xx, monitor; Detect()
internal/sensor/     sensor sources: k10temp, coretemp, nvme:max, drivetemp:max, ec:*, hwmon:<name>:tempN
internal/control/    controller loop: curve, slew, override, critical, stall, plausibility, failsafe,
                     watched sensors, history ring, snapshot, alert stamps
internal/alert/      alert sinks (PVE::Notify, mail, log, off), Swappable, Ring (history), PVE templates
internal/ipc/        unix socket listener/client for the CLI (same mux as web, no auth)
internal/logfile/    rotating log file (O_NOFOLLOW, size rotation, Lines/Export/Clear)
internal/tlscert/    certificates: EnsureAuto, Store (hot swap), ValidatePair, ServerConfig, Info
internal/web/        HTTP API, guard (Host → CSRF → auth), sessions, rate limiter, TLS listener; embeds static/
internal/web/static/ index.html, app.js, app.css — no build step, vanilla JS, canvas charts, mock (?mock=1)
internal/sysinfo/    hardware inventory from /sys, /proc, SMBIOS table, lspci
internal/fsutil/     WriteAtomic(path, data, perm): temp file in the target dir, sync, chmod, rename, clean-up
internal/sdnotify/   READY=1, WATCHDOG=1
internal/version/    Version (the only version literal), Prerelease()
deploy/              systemd units, onfailure script, install/uninstall, apt hook, config example, debian/, PVE templates
tools/remote-go.ps1  build/test in a Docker container on a Linux host over ssh (pwsh)
testdata/sysfs/n5pro fake /sys tree mirroring the reference host (generic names)
docs/                AUDIT.md, DESIGN-AUDIT.md, screenshots/
```

Subcommands (18, `main.go` `order`): `setup serve status set auto curve log check detect
system test failsafe passwd cert alerts export import version`; hidden: `help`, `alert
<kind> <msg>` (used by the onfailure unit). Exit codes: 0 ok, 1 failure, 2 usage.
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
refused). `Default()` has **no channels**; `N5ProChannels()` is the verified set, added by
`control.SanitizeChannels` on the N5 Pro when the file lacks them.

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
| `[alert] transport` | `auto` \| `pve` \| `mail` \| `log` \| `off` (lower-cased) | `auto` | reload |
| `mail_to` | `^[A-Za-z0-9_][A-Za-z0-9._%+-]*(@[A-Za-z0-9.-]+)?$`, ≤ 254 (`R-M3`: never starts with `-`) | `root` | reload |
| `[dashboard] sensors` | 0..8 distinct sensor ids (shape only; resolution is the controller's business) | `[]` | reload |
| `[[channel]] name` | `^[a-z0-9_]+$`, unique; else the channel is dropped | — | restart when the set changes |
| `pwm` | 1..8, unique | — | restart when the set changes |
| `sensor` | non-empty sensor id (section 5) | — | reload |
| `curve` | 2..8 points `[temp, duty]`, temp −20..120 strictly ascending, duty 0..255 non-decreasing; else `[[45,85],[80,255]]` | — | reload |
| `critical` | last curve temp + 1 .. 150; missing → last + 10 (≤ 150) | last + 10 | reload |
| `stop` | `"auto"` or fixed duty; below 60 raised to 60; invalid → `DefaultStop(sensor)` (`140` for `drivetemp:max`, else `auto`) | by sensor | reload |

"reload" = applied by `PUT /api/config`, preset apply and import without a restart;
"restart" = read once at start (the account and certificate APIs edit the file and apply
their keys live). A changed channel set or profile makes `Service.Reload` return
`ErrRestartRequired` (HTTP 202).

Presets: name `^[a-z0-9_-]{1,64}$`. Built-in N5 Pro sets (listed for the `n5pro` profile
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
`hwmon:<name>:tempN` (first device of that name), `ec:<label>` (profile `ExtraTemps`).
Values in millidegrees; plausible range −20000..120000. `max` sources ignore devices that
vanished as long as one remains; zero devices → error. `Known()` returns
`[]Info{ID, Description}` with the live reading in the description, ending with the two
generic patterns that do not parse as ids. `Parse(id)` yields a `Source`; the controller's
`SensorReader` is satisfied by it (wiring adapts the func types).

## 6. Controller (internal/control)

Per cycle: `sd_notify WATCHDOG=1` → read channel sensors (unresolved / unreadable /
implausible / frozen → **that channel** at its safe duty, mode `sensor-error`, alert
`sensor` on the transition only) → read watched sensors (errors → value absent) → targets
from curve (linear interpolation) → overrides → critical → stall check → slew (first cycle
and manual direct) → write (unchanged duties rewritten every 6th cycle to undo external
writes) → verify → snapshot → history ring (2 h) → periodic log line (`log_every`).
Sensors are re-resolved every 60 cycles.

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
  `Stop()` is idempotent, takes only the hardware mutex and makes a running cycle skip its
  writes. serve waits for `Run` (15 s cap). `Failsafe(cfg, dev)` = SafeStop of every channel
  without a controller (`n5-fangov failsafe`).
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
`duty` is the hardware `pwmN` value, `target` the computed one before slew. History point:
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
| `test` | Alerts tab, `alerts test` | no cooldown, single-flight, 20 s bound |

serve routes its start-up alerts through `sendAlertCooled` (same stamp files, 30 min), so
a restart loop cannot spam PVE.

## 7. Alerts (internal/alert)

```go
type Sink interface { Alert(kind, msg string); Name() string }      // fire-and-forget, never panics
type Sender interface { Send(kind, msg string) error }               // delivery error returned, still logged
type ContextSender interface { SendCtx(ctx, kind, msg string) error }
func NewFor(transport, mailTo string, logger Logger) (Sink, string)  // configured → effective sink + its name
func Available() (pve, mail bool)
type Swappable struct{…}   // Set(Sink)/Get(); the controller and serve hold the Swappable, Configure swaps the inner sink
type Ring struct{…}        // NewRing(inner, path, logger): records every alert that passed the cooldown (ring of 50,
                           // mirrored to <state dir>/alerts.json), Recent(n) newest first, Last() kind → ts
var ErrTestBusy            // second test while one runs (409)
func TemplateFiles() map[string][]byte; TemplateStatus(); InstallTemplate() (path, error)
```

Sinks: `PVE` (`perl -MPVE::Notify`, payload over the environment, template `n5-fangov`,
severity warning, fields `type`, `hostname`, `kind`), `Mail` (`mail(1)`, recipient after
`--`), `Log`, `Off` (journal line `suppressed (transport off)`), `Multi`. `auto` = pve when
`/usr/share/perl5/PVE/Notify.pm` and perl exist, else mail, else log; `pve`/`mail` without
their tool degrade in the same order. Delivery is bounded (30 s + wait delay). The PVE
template pair exists twice — `deploy/pve-notification/` (installer) and
`internal/alert/templates/` (daemon/CLI) — and `make verify-deploy` checks they are identical.

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
be an IP literal, `localhost`, the listen host or an `allowed_hosts` entry (else 421,
`M1`); state-changing methods need `X-N5-Fangov-Csrf: 1` (else 403); then the caller is
resolved once (cookie session → `AuthConfig` Basic) and rides in the request context
(`web.CallerFrom`). Anonymous access to a protected path is a silent 401 (no log, no
limiter count, no `WWW-Authenticate`). A presented Basic credential that fails is counted
and logged (`web: auth failure from <ip> (user …)`); limiter per client IP: 5 free, then
250 ms doubling to 2 s, reset after 10 min or a success; at most 4 delayed attempts in
flight per IP, further ones `429` before any PBKDF2 (`M3`). `http.AllowQuerySemicolons`
wraps both handlers. Body limits: 256 KiB config/import, 64 KiB certificate upload, 4 KiB
override/login/account JSON; 413 above. Errors are `{"error": "..."}` (plus `"errors":
[...]` where a list exists). Every write to the config file is serialised in cmd
(`configFileMu`).

`Deps` members and the 501 rule: `Service` (control), `Config` (`ConfigStore`), `Validate`
(`config.Parse`), `Presets` (`PresetStore` + optional `PresetDetailer`, `PresetRenamer`),
`Log LogStore`, `Bundle`, `Profiles`, `Sensors`, `Version`, `Auth AuthConfig`,
`AllowedHosts`, `TLS`, `TLSMgr`, `TLSHosts`, `Logf`, `SessionFile`, `Account`, `Alerts`,
`Dashboard`, `About`, `System func() any`. A nil store makes its endpoints answer 501.

Visibility with `auth = "basic"` (the UI mirrors it; with `auth = "none"` every caller
counts as signed in, `via: "none"`):

| Class | Anonymous | Signed in (cookie or Basic) |
|---|---|---|
| public | full | full |
| public, filtered | reduced (`state`: `ts, status, profile, verified, uptime_s, dry_run, channels[name,pwm,sensor,temp,duty,target,rpm,mode]`; `history`: points without `extra`) | full |
| protected | 401 | full |
| static (`/`, `app.js`, `app.css`) | full | full |

| Endpoint | Class | Request | Answer |
|---|---|---|---|
| `GET /api/version` | public | — | `{name, version, prerelease, tls, auth, limits{…}}`; `limits` = validation bounds for the UI (curve points 2..8, temp −20..120, critical ≤ 150, stop ≥ 60, hdd override ≥ 60, password 8..128, user/preset/channel name rules, dashboard sensors ≤ 8) |
| `GET /api/about` | public | — | `{name, version, prerelease, license, license_url, repo, author, author_url, go, credits[{name,url,note}]}` |
| `GET /api/session` | public | — | `{authenticated, mode: none\|basic, user, expires?, remember?, via: cookie\|basic\|none}` |
| `POST /api/login` | public, CSRF, limiter | `{user, password, remember}` ≤ 4 KiB | 200 `{ok, user, expires, remember}` + `Set-Cookie`; 401 `invalid user or password`; 429 |
| `POST /api/logout` | public, CSRF | — | 204; cookie cleared, session revoked |
| `GET /api/state` | filtered | — | snapshot (section 6) |
| `GET /api/history?minutes=120&since=TS` | filtered | — | `[{ts, temp{}, duty{}, rpm{}, extra{}}]`, only `ts > since` |
| `GET /api/config` | protected | — | `{raw, config{daemon, web, log, alert, dashboard, channel[], warnings[]}}`; `password_hash` is `<unchanged>` in both |
| `PUT /api/config[?strict=1]` | protected | raw TOML ≤ 256 KiB; `<unchanged>` restores the stored hash | 400 `{error, errors[]}` on a syntax error (nothing written); with `strict=1` also on any value that would be replaced by a default; else write (tls keys pinned, `[alert]` re-applied) → reload: 200 `{ok, restart_required:false, warnings[]}` or 202 `{restart_required:true}` |
| `GET /api/config/export` | protected | — | JSON attachment `n5-fangov-settings-<ts>.json`: `{format:1, version, exported, config, presets{}}`, hash redacted |
| `POST /api/config/import` | protected | the bundle | everything validated first; 200 / 202 / 400 `{error: "import rejected: …", errors[]}` |
| `PUT /api/override/{name}` | protected | `{duty}` or `{percent}` ≤ 4 KiB | 200 `{ok, channel, duty, mode}` (applies next cycle; critical/stall win); 400 below `MinHDDOverride` |
| `DELETE /api/override/{name}` | protected | — | 200 `{ok, channel, mode}` |
| `GET /api/presets` | protected | — | `[{name, channels[names], builtin, description}]` |
| `GET /api/presets/{name}` | protected | — | `{name, builtin, description, channels[{name,pwm,sensor,curve,critical,stop}]}`; 404 |
| `POST /api/presets/{name}/apply` | protected | — | 200 `{ok, applied}` / 202 `{restart_required}`; 404 unknown or built-in of another profile |
| `PUT /api/presets/{name}` | protected | empty | saves the current `[[channel]]` tables; 200 `{ok, saved}`; 409 built-in |
| `POST /api/presets/{name}/rename` | protected | `{name}` | 200 `{ok, name}`; 409 built-in or target exists; 404 |
| `DELETE /api/presets/{name}` | protected | — | 200; 409 built-in; 404 |
| `GET /api/sensors` | protected | — | `[{id, description, temp?}]` (catalogue with live readings) |
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
| `GET /api/alerts` | protected | — | `AlertStatus{transport, effective, mail_to, pve_available, mail_available, template{installed,current,writable,path,reason}, cooldown, kinds[]} + {last{kind:ts}, recent[{ts,kind,msg}]}` |
| `PUT /api/alerts` | protected | `{transport, mail_to}` | `{ok, status}`; 400 |
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
`Recent(n)`, `Test()`, `InstallTemplate()`, `Configure(transport, mailTo)` — the template
probe is cached 10 min (`R-M2`). `DashboardStore`: `Sensors()`, `SetSensors(ids)`.
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
(controller classes nvme/sata/sas/scsi/raid; `/sys/block/*` minus virtual devices),
FanController (module link + version). Static parts cached `CacheTTL` (10 min) under a
mutex; the sysfs root follows `N5FANGOV_SYSFS`. No serial numbers. cmd wires
`newSystemCollector(fs, dev)` into `Deps.System`; `n5-fangov system` asks the socket
first and collects locally otherwise.

## 11. Dashboard (internal/web/static)

No framework, no build step; `app.js` ≤ **96 KB** (test `web_test.go`), CSP
`script-src 'self'`, no `innerHTML`, no inline handlers. Colours, spacing, radii and type
scale are tokens in one `:root` block (dark; light overrides under
`[data-theme="light"]` and `system`) and a JS constant block after `cssVar`; the mock
version string is `internal/version` (served by `/api/version`), never a literal.

- **Header:** brand, profile title, verified badge; status chip, uptime, version with
  `beta` badge when `prerelease != ""`, lock button (`🔒 TLS` / `🔓 HTTP`; warn colour
  when plain HTTP off loopback, during a certificate fallback or below 30 days to expiry),
  live/paused indicator, user name, *Sign in* / *Sign out*, settings gear. Gear, lock and
  every tab except Overview and About carry `data-auth` and are hidden while anonymous.
  Below 700 px the header is one line and the tab bar is sticky.
- **Tabs** (`role="tablist"`, keyboard): Overview, Curves, Manual, Presets, Alerts, System,
  Log, Compatibility, About. Every tab id has a dispatch entry (`TestTabsHaveHandlers`).
- **Overview:** channel cards (temperature coloured by the channel's critical, duty bar with
  target marker, mode badge, RPM), temperature and fan-speed charts (RPM/duty toggle);
  signed in: Sensors card (catalogue grouped CPU / SSD·NVMe / HDD / GPU / NIC / EC·board /
  other by id prefix and hwmon name, *chart* toggle → `PUT /api/dashboard`), Extra sensors
  chart (when the watched list is non-empty), System card (at-a-glance inventory,
  refreshed every 30 s), Recent alerts.
- **Curves:** one editor per channel — sensor select, critical, stop, canvas with drag
  points, `crit` line and `now` marker, point table with *remove* and *+ add point*
  (inserted at the middle of the widest gap, table kept sorted), measured duty→RPM
  reference on the N5 Pro. Client validation before the PUT: 2..8 points, temperatures
  ascending, duties non-decreasing, critical above the last point, stop `auto` or 60..255;
  errors in a `role="alert"` notice. *Apply to daemon* sends `PUT /api/config?strict=1`;
  202 shows the restart notice, warnings stay visible until *Revert*. A dirty indicator
  marks unsaved edits and a confirm dialog guards tab changes and session loss.
- **Manual:** slider + *Set* / *Back to auto* per channel; HDD-like channels show the
  minimum-60 hint and refuse lower values client-side.
- **Presets:** cards with built-in/recommended badges and description, *Apply*, *Details*
  (channel tables via `GET /api/presets/{name}`), *Rename*, *Delete* (user presets), *Save
  current as…* (client-side name rule, built-in names refused).
- **Alerts:** transport form (select + `mail_to`, *Save*), effective transport and tool
  availability, template card with *Install / Update template* (disabled with reason),
  *Send test alert*, cooldown, kinds table with last delivery, recent alerts.
- **System:** Host / Machine / CPU / Fan controller cards, Memory with module table,
  GPU · NPU, Network, Storage (with sum row), notice for `errors`, "live … · static … ago",
  *Refresh*; polled every 30 s while current.
- **Log:** filter, auto-scroll, *Refresh* / *Export* / *Clear* (confirm), WARN/ERROR colouring.
- **Compatibility:** profiles with ACTIVE and verified/untested badges. **About:** name,
  version, description, licence, repository, author, credits, releases link (the mock hint
  appears in mock mode only).
- **Dialogs** (`<dialog>`, focus trap, Escape): Login (user, password, *Remember me*,
  inline error), Account (*Change password…*, *Change user…*, *Sign out other sessions*,
  sessions table with THIS SESSION), Certificate (badge, key/value block, SAN chips,
  fingerprint + copy, notice with the server's warnings, Download .crt/.cer, Regenerate…
  with *generate a new key*, Upload own certificate… with file inputs + PEM textareas and
  the *install anyway* checkbox after `force_required`, Back to auto, collapsible "How to
  trust this certificate"), Confirm/Prompt (generic, replaces the native dialogs).
- **Settings popover** (gear): unit °C/°F, refresh interval 5/10/30 s, theme
  dark/light/system, Export/Import settings (fetch + blob, so the credential and the CSP
  stay), Certificate…, Account…. Persisted in `localStorage` (`n5-fangov`, whitelisted values).
- **Connection banner** after two failed polls; a 401 on a protected call shows "Session
  expired" and returns to the anonymous Overview (unsaved curve edits are kept for the next
  sign-in).
- **Mock:** `?mock=1` anonymous, `&user=1` signed in, `&auth=none`, `&tls=off|file|soon|fallback`,
  `&tab=<id>`, `&syserr=1`; every endpoint above is implemented (login `admin`/`admin`);
  names and addresses are documentation values (`n5host`, `192.0.2.x`, `n5.lan`).

## 12. Deploy

`deploy/n5-fangov.service`: `Type=notify`, `NotifyAccess=main`, `WatchdogSec=60`,
`ExecStartPre=/usr/bin/n5-fangov check --quiet`, `ExecStopPost=/usr/bin/n5-fangov failsafe`,
`Restart=always`, `RestartSec=5`, `StartLimitBurst=5` / `StartLimitIntervalSec=300`,
`OnFailure=n5-fangov-onfailure.service`, `Conflicts=n5-fand.service`,
`After=systemd-modules-load.service`, `TimeoutStopSec=20`, `Nice=-5`. Directories:
`RuntimeDirectory=n5-fangov` (0750, preserved), `LogsDirectory=n5-fangov` (0750),
`StateDirectory=n5-fangov` (0700), `UMask=0077`. Sandbox: `NoNewPrivileges`,
`ProtectSystem=strict` + `ReadWritePaths=-/etc/n5-fangov -/run/n5-fangov -/var/log/n5-fangov
-/var/lib/n5-fangov -/sys/class/hwmon -/sys/devices -/var/spool/postfix/maildrop
-/etc/pve/notification-templates`, `ProtectHome`, `PrivateTmp`, `PrivateDevices`,
`ProtectKernelTunables=no` (**must stay** — pwm files are kernel tunables),
`ProtectControlGroups`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK`,
`RestrictNamespaces`, `LockPersonality`, `MemoryDenyWriteExecute`, `RestrictRealtime`,
`SystemCallArchitectures=native`, `SystemCallFilter=@system-service`,
`CapabilityBoundingSet=` (empty). Verified on the reference host: the sandbox writes pwm
files, reads the DMI tables, runs `lspci`, writes into pmxcfs. The `mail(1)` path on
non-PVE hosts additionally needs `CAP_DAC_OVERRIDE` (README "Hardening").

Watchdog: the loop sends `WATCHDOG=1` every cycle; serve pings every 10 s from a ticker
**only while the loop is alive** (`LastCycle()` within 3 × interval); `interval` is capped
at 30 s so two cycles fit into 60 s. `n5-fangov-onfailure` reads
`Result/ExecMainCode/ExecMainStatus`, sleeps 8 s, polls `is-active` up to 25 s while
`activating`; `active` → `restart` alert, still `activating` → "restart in progress" (same
kind), else `failed`; cooldown 30 min via `/run/n5-fangov/alert.<kind>`.

Runtime files: `/run/n5-fangov/n5-fangov.sock` (CLI, root only), `state.json`,
`override.<name>`, `alert.<kind>`; `/var/lib/n5-fangov/sessions.json`, `alerts.json`
(0600; an unwritable state dir is logged once, the daemon runs without persistence);
`/etc/n5-fangov/{config.toml, presets/, tls/}`; `/var/log/n5-fangov/n5-fangov.log[.N]`.

Install (`deploy/install.sh`, needs `dist/n5-fangov` or `./n5-fangov`): stops and disables
`n5-fand.service`, installs binary, onfailure script (`/usr/libexec/n5-fangov/`), both
units (`/etc/systemd/system/`), `/etc/n5-fangov/{,presets}`, the log directory, the config
example and the deploy README (`/usr/share/doc/n5-fangov/`), the PVE templates
(`/usr/share/n5-fangov/pve-notification/` and, when `/etc/pve` exists and pmxcfs is
writable, `/etc/pve/notification-templates/default/`), the apt hook
(`/etc/apt/apt.conf.d/90n5-fangov`), `daemon-reload`, enable; writes no config and starts
nothing. `uninstall.sh [--purge]`: disable, failsafe, remove units/binary/hook/templates/
`/run` and `/var/lib` state; `--purge` also `/etc/n5-fangov` and `/var/log/n5-fangov`. The
`.deb` (`make deb`, no debhelper, no conffile) does the same through `postinst`/`prerm`/
`postrm`; `deploy/debian/copyright` (DEP-5) names GPL-2.0 and the MIT text of toml.

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
before the release), every other `-` → `+` (`0.3.0~beta.4+3+gabcdef`). `make release`
(clean tag, gh CLI) uploads the static binary and its sha256; `-` in the version marks a
pre-release.

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
Frontend: `web_test.go` checks the JS budget, the tab/handler list, the CSP and the
contrast of the primary buttons; the mock (`?mock=1`) is the manual test bed. No test
writes into `testdata/`.

## Review tags in the code

Comments and test names carry tags from the review rounds: `M1`–`M7`, `H1`–`H4`, `L1`–`L9`
= safety/security/polish reviews of v0.1 and v0.2 (the same id means a different finding
in each package); `R-M1`–`R-M3`, `R-L4`–`R-L11` = review of v0.3.0-beta. The tags stay;
what they fixed is in [CHANGELOG.md](CHANGELOG.md), the current open list in
[docs/AUDIT.md](docs/AUDIT.md) and [docs/DESIGN-AUDIT.md](docs/DESIGN-AUDIT.md).
