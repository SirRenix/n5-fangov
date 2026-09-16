# Changelog

All notable changes to n5-fangov. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow [Semantic Versioning](https://semver.org/). The version string of a build is
`n5-fangov version` (source: `internal/version/version.go`); the Debian package maps a
pre-release suffix to `~` (`0.3.0~beta.4`).

Review findings referenced as `M1`…`M7`, `H1`…`H4`, `L1`…`L9` (reviews of v0.1/v0.2) and
`R-M*`/`R-L*` (review of v0.3.0-beta) are the tags that remain in code comments and test
names; the audit that drove the *Unreleased* work is `docs/AUDIT.md` (code) and
`docs/DESIGN-AUDIT.md` (dashboard).

## [Unreleased]

### Planned (0.4.0) — decided 2026-09-16, not started

Scope rule for 0.4: the regulator (`internal/control`) is verified and stays as it is;
everything below is API, dashboard, alerts and packaging. Attack surface stays small: no
MQTT/discovery, no multi-host management, no new dependencies. The UI stays English.

**1. Interface (foundation for automation and AI agents)**
- **API tokens** instead of the admin password in scripts (Home Assistant, monitoring,
  scripts, local LLM agents): named, with a **scope** (`read` = state/history/system/
  sensors; `control` = overrides, presets, dashboard sensors; `admin` = everything the
  dashboard can do), optional **expiry** (e.g. 90 days; unlimited allowed with a warning),
  individual **revocation**, list with created / expires / last used / last address.
  Storage like the sessions (`/var/lib/n5-fangov/tokens.json`, sha256, 0600), transport
  `Authorization: Bearer <token>`, rate limit per token, `read` as the default scope.
  Dashboard: *Tokens* section in the settings; CLI `n5-fangov token create|list|revoke`.
  Basic auth and the unix socket stay as they are.
- **OpenAPI description** `GET /api/openapi.json` (public), generated from the route
  table and kept in sync by a test, so an agent can use the API without the README.
- **Home Assistant without tinkering**: README recipe with REST sensors (temperature, RPM,
  mode per channel) and an automation that applies a preset through a `control` token.
- **Webhook alert transport** (generic JSON POST; works for ntfy, Gotify, Home Assistant
  webhooks) next to PVE::Notify and `mail(1)` — non-PVE hosts have only mail today.

**2. Regulation add-ons (curve post-processing only; failsafe, stall and critical untouched)**
- **Hysteresis and minimum on-time per channel** (`hysteresis = 2`, `min_on = "60s"`):
  drive fans oscillate around a curve point at 44/45 °C today.
- **Several sensors per channel** (`sensor = ["drivetemp:max", "ec:hdd"]` → maximum).
- **Schedules** (`[[schedule]] preset = "n5pro-quiet" from = "22:00" to = "07:00"`) as
  preset switches with an alert when a switch fails.
- **pwm4** (PCIe header, no tachometer) as an optional fourth channel.

**3. Dashboard**
- **Longer history**: persist the ring in the state directory, views 2 h / 24 h / 7 d
  (downsampled), CSV export.
- **Per-device sensors**: today only `nvme:max`, `drivetemp:max` and the *first* device of a
  name (`hwmon:nvme:temp1`) are addressable — a box with three NVMe SSDs and four HDDs
  shows two temperatures. New ids per device (`disk:sda`, `disk:nvme1n1`, resolved through
  `/sys/block/<dev>/device/hwmon`) for the Sensors card, the extra charts and the curve
  sensor selection; the System tab lists every disk with its temperature.
- Split the mock out of the production bundle (22 % of `app.js`) so the budget stops
  binding; remaining low design findings (toast limit, keyboard for curve points).

**4. Maintenance debt from the audit (before 1.0)**
- Files named by topic instead of version (`wiring_v2/v3`, `v3_test`, `*_fix_test`),
  split `cmdServe` and `cycle`, review tags out of the code into a legend,
  `ReadWritePaths` narrowed to the profile's sysfs path.

**5. Community and release**
- Repository public after the history rewrite; upstream issues (driver validation data,
  ProxFansX compatibility note); DKMS `.deb` in the sibling repository with the header
  meta-package as dependency (user path: two `apt install` + `setup`); release workflow
  uploads the `.deb`.
- Reboot proof on the reference host (DKMS + daemon together) — the last open operations
  question, no code.

### Planned (after 0.4.0, separate session) — dashboard redesign

Structural redesign of the dashboard (sidebar navigation, channel-centric Fans page,
consolidated Settings page, SVG icons, sparklines) — concept and hand-over in
[`docs/design/REDESIGN-CONCEPT.md`](docs/design/REDESIGN-CONCEPT.md). Deliberately after
0.4.0 so tokens, schedules and the longer history have their place in the new structure;
prototype in the mock first, operator decides on screenshots.

### Before the public release (documentation)

- **Split the README**: a short landing page (what it is, one screenshot, three-step
  install, links) and a `docs/` set with one page per topic (install, kernel driver on the
  N5 Pro, setup, dashboard guide with screenshots, CLI, configuration, alerts, HTTPS and
  security, updates and rollback, troubleshooting, development). The 880-line README is
  complete but tiring; a reader needs a table of contents and separation.
- **Kernel driver page**: what the EC driver is, what DKMS does for it, install from the
  sibling repository (later its `.deb`), verification (`dkms status`, `sensors`,
  `n5-fangov detect`), the kernel-update gate, removal — the topic first-time users
  stumble over.

**Not planned**: MQTT/discovery (REST + token is enough and smaller), a German UI
(audience is GitHub), multi-host management, a frontend framework.

## [0.3.0-rc1] — 2026-09-16

Pre-release hardening after the acceptance audit of `v0.3.0-beta.4` (16 September 2026).

### Added

- `[web] behind_tls_proxy = true`: the session cookie gets the `Secure` flag although the
  daemon itself serves plain HTTP behind a TLS-terminating reverse proxy.
- `PUT /api/config?strict=1`: values the daemon would otherwise replace by defaults are
  rejected with `400` and the warning list; the curve editor uses it, so an operator's
  curve is never silently swapped for the built-in default.
- `GET /api/version` carries `limits` — the validation bounds (curve points, temperatures,
  critical, stop, HDD override minimum, password length, preset/user name rules, dashboard
  sensor cap) the UI validates against instead of hard-coding them.
- Alert kinds `device` (fan controller unreachable, daemon restarts), `profile` (no fan
  controller detected at start), `start` (controller could not start) and `web` (listener
  or TLS set-up failed) are listed in the Alerts tab and `n5-fangov alerts status`; they
  were raised before but had no description and no "last" row.
- `internal/fsutil`: one atomic write helper (temp file in the target directory, sync,
  chmod, rename, clean-up on failure) for every file the daemon writes.
- `deploy/debian/copyright` in DEP-5 form with the GPL-2.0 reference and the MIT text of
  `github.com/BurntSushi/toml`.
- Dashboard: design tokens (colours, spacing, radii, type scale) in one `:root` block and a
  JS constant block; own confirm/prompt dialogs in the theme instead of `confirm()` /
  `prompt()`; sticky tab bar and a one-line header below 700 px; a dirty indicator on the
  curve editor and a warning before unsaved edits are dropped (tab change, session loss);
  the editor checks duty monotonicity, `critical` above the last curve point and
  `stop ≥ 60` before it sends anything.
- `CHANGELOG.md`, `CONTRIBUTING.md`, `SECURITY.md`, `docs/screenshots/` (dashboard views
  from the mock, with the driver script to regenerate them).
- README: Dashboard (one paragraph per tab), CLI reference (all 18 subcommands, exit codes,
  environment), configuration reference table, alerts table, troubleshooting.
- `make test-race` and `tools/remote-go.ps1 -Image` for a race-detector run in a cgo-capable
  builder; end-to-end `TestServeSmoke` (real `cmdServe` against the fake sysfs, dry-run,
  TCP + socket, override, SIGTERM).

### Changed

- Toolchain Go 1.26 (`go.mod`, builder image `golang:1.26-alpine`), `BurntSushi/toml`
  1.6.0.
- `setup`/`passwd`: the password comes from `--password-file F` or `--password -` (stdin)
  only; the literal form is gone (it was visible in `ps` and the shell history). Minimum
  length 8 in the CLI, as in the dashboard.
- `web.Deps.Log` is typed `LogStore`; the pre-v0.2 log function adapter and its `501`
  fallback are removed.
- `DESIGN.md` is one consolidated contract per package (rules, layout, config, profile,
  sensor, control, alert, tlscert, web/API, sysinfo, UI, deploy, testing); the release
  history moved here.
- `LICENSE` carries the full GPL-2.0 text behind the copyright header.
- Version literals live in `internal/version/version.go` only; README and DESIGN refer to
  the current pre-release without spelling it out.

### Fixed

- Manual tab: switching to it threw a `TypeError` (missing dispatch entry) and `?tab=manual`
  never started polling; every tab now has a handler and a test checks the list.
- Primary buttons in the dark theme were below WCAG AA contrast.
- Web tests called `t.Fatal` from goroutines; the concurrency-cap test waits bounded.
- README: install path from a GitHub release (binary + sha256 into `dist/`, `install.sh`),
  prerequisites (DKMS driver package, headers, `experimental_write=1`, `pciutils`),
  rollback and uninstall.
- Deploy README: the kernel gate covers the kernels the box can boot into, not every
  directory under `/lib/modules` (the README already said so).
- `PUT /api/config?strict=1` refuses only warnings on the `[[channel]]` tables (what the
  curve editor writes); a pre-existing warning elsewhere in the file (an unknown key in
  `[web]`, say) no longer blocks the editor and comes back under `warnings` as before.
- Auth limiter: the concurrency cap (four delayed attempts in flight, then `429`) is
  counted per address, not per IPv6 /64 — one misbehaving host no longer locks its whole
  LAN prefix out; the delay counter stays per /64. A link-local zone (`fe80::1%vmbr0`)
  is stripped before bucketing.
- A legacy `sha256` hash hit by two successful verifications at the same time was
  rewritten twice; the upgrade is serialised.
- The cooldown stamp of a start-up alert is written before the delivery goroutine
  starts, so an early exit of `serve` cannot lose it.
- Bundle import and `PUT /api/config` refuse a config whose `password_hash` placeholder
  sits in an inline `web = { … }` table (the restore does not reach it; the placeholder
  would have become the stored hash).
- Preset apply: a preset that was written but not taken by the daemon answers `500`
  "preset written, reload failed" instead of `400`.
- `tools/remote-go.ps1` picks `golang:1.26-bookworm` on its own when `-Cmd` contains
  `-race` (the race detector needs cgo and glibc) unless `-Image` is given.
- Curve editor: a rejected apply no longer announces twice to screen readers (the
  `role="alert"` notice stays, the assertive toast is gone).

## [0.3.0-beta.4] — 2026-09-16

### Added

- System inventory: `internal/sysinfo` reads machine/board/BIOS (`/sys/class/dmi/id`), CPU
  (`/proc/cpuinfo`, cpufreq), memory (live from `/proc/meminfo`, installed modules from
  the SMBIOS table in `/sys/firmware/dmi/tables/DMI` — size, type, speed, ECC, manufacturer,
  part number; no `dmidecode`, no `/dev/mem`), GPU and NPU with driver versions, physical
  NICs (model, driver, speed, state, MAC, MTU), storage controllers and disks, OS, kernel,
  uptime, load, fan-controller module. Every source failure is one `errors` line, never
  fatal; static parts cached for 10 minutes.
- `GET /api/system` (protected), `n5-fangov system [--json]` (socket first, local
  collection without a daemon), a *System* card on the signed-in Overview and a *System*
  tab with the full tables (`&syserr=1` in the mock shows the notice for a missing `lspci`).
- Verified from inside the unit's sandbox on the reference host (DMI tables readable as
  uid 0 without capabilities, `lspci -mm -D` runs).

### Changed

- JS budget 84 KB.

## [0.3.0-beta.3] — 2026-09-16

### Added

- Presets tab: *Details* shows a preset's channel tables (sensor, curve points, critical,
  stop) for built-in and user presets; *Rename* for user presets (never over an existing
  or built-in name).
- `GET /api/presets/{name}`, `POST /api/presets/{name}/rename` (optional store interfaces
  `web.PresetDetailer`, `web.PresetRenamer`).
- `make release`: GitHub release from a clean tag with the static binary and its sha256.

## [0.3.0-beta.2] — 2026-09-16

### Changed

- About tab credits `Sl0thC0der/proxfansx` for the generic NCT67xx/IT87xx chip handling
  the untested profiles follow, and links the releases page.
- README: tested platform row (Proxmox VE 9.2 / Debian 13, kernel 7.0.12-1-pve, driver
  `minisforum-n5-it5571` 0.2.0, N5 Pro BIOS 1.05).

## [0.3.0-beta.1] — 2026-09-16

First pre-release of the 0.3 line, after the operator's first multi-hour review of the
dashboard. Two adversarial reviews (0 high, 5 medium, 18 low) fixed with regression tests
(`R-M1`…`R-M3`, `R-L4`…`R-L11`); verified live on the reference host.

### Added

- Visibility model enforced by the server: with `auth = "basic"` an anonymous visitor gets
  the channel cards, the two charts (reduced `GET /api/state` / `/api/history`: no hwmon
  path, EC temperatures, alert stamps or extra sensors) and the About tab; everything else
  answers `401`.
- Cookie sessions: `POST /api/login` (*Remember me* = 30 days, else 12 h), `POST /api/logout`,
  `GET /api/session`; sessions persisted hashed in `/var/lib/n5-fangov/sessions.json` (at
  most 50), survive a daemon restart, dropped when the credential epoch changes (password
  or user changed outside the dashboard).
- Account panel: change password, change user (both write the config file in place, keep
  the caller's session, sign every other session out), session list, *Sign out other
  sessions*. `GET /api/account`, `POST /api/account/{password,user,sessions/revoke}`.
- `[alert]` section (`transport = auto|pve|mail|log|off`, `mail_to`), swappable alert sink
  applied without restart; Alerts tab with transport form, effective transport, tool
  availability, PVE template state with *Install / Update template* (writes the two
  `.hbs` files into `/etc/pve/notification-templates/default/` from inside the sandbox),
  *Send test alert*, cooldown, kind list with last delivery, recent alerts (ring of 50,
  persisted in `/var/lib/n5-fangov/alerts.json`). `GET/PUT /api/alerts`,
  `POST /api/alerts/{test,template}`; CLI `n5-fangov alerts status|test|template`.
- `[dashboard] sensors` (0..8 ids): extra sensors read once per cycle, recorded as
  `history[].extra` and `snapshot.watched`, charted in the *Extra sensors* card; the
  Sensors card on the Overview lists every readable temperature grouped CPU / SSD / HDD /
  GPU / NIC / EC / other with a *chart* toggle. `GET/PUT /api/dashboard`.
- Built-in N5 Pro presets `n5pro-quiet`, `n5pro-balanced` (recommended), `n5pro-cool`
  (embedded, listed for the `n5pro` profile only, never overwritten or deleted);
  `DELETE /api/presets/{name}` for user presets.
- About tab (public): name, version with pre-release badge, licence GPL-2.0-only,
  repository, author, credits. `GET /api/about`; `GET /api/version` adds `prerelease` and
  `auth`.
- Curve editor: *+ add point* inserts at the middle of the widest temperature gap and keeps
  the table sorted.
- Unit: `StateDirectory=n5-fangov` (0700), `ReadWritePaths` gains
  `/etc/pve/notification-templates`; `serve --state-dir`, `N5FANGOV_STATE_DIR`.
- `config.SetKey` handles the dotted `web.user = …` layout; inline tables are refused with
  a message (`R-L11`).

### Changed

- Version string carries the pre-release suffix; the header shows a `beta` badge.
- Alert delivery is context-bounded (30 s + wait delay); the test alert is single-flight
  (`409 test in progress`) and bounded to 20 s; the template probe is cached for 10 minutes
  (`R-M2`); `mail_to` never starts with `-` and is passed after `--` (`R-M3`).
- All config read-modify-write paths in `cmd` serialise on one mutex.

## [0.2.1] — 2026-09-16

### Added

- Certificate management in the dashboard (lock icon and *Settings → Certificate…*):
  subject, issuer, SANs, validity, key type, fingerprint with copy, *Download .crt/.cer*,
  *Regenerate…* (key kept by default, so imported trust survives), *Upload own
  certificate…* (PEM pair validated: key matches, not expired, key type the server can
  sign with, one test handshake; HSTS lock-out guard with *install anyway*), *Back to
  auto*, collapsible "How to trust this certificate".
- `GET /api/tls`, `GET /api/tls/cert.{crt,cer}`, `POST /api/tls/{regenerate,upload,reset}`;
  hot swap through `tlscert.Store` (no restart, no session tickets); fallback to the
  automatic certificate with a `tls` alert when a configured pair is unreadable.
- CLI `n5-fangov cert info|export [--der]|regen [--new-key]|upload CERT KEY|reset` — via
  the socket when the daemon runs, on the files otherwise.
- The daemon owns `[web] tls`, `cert_file`, `key_file` while it runs: every config write
  through the API gets the three keys re-applied.

### Fixed

- Review findings `M1`–`M4`, `L1`–`L9` of the certificate UI (P-224 refused, name
  constraints, key-file permission warning, fingerprint rounding, …).

## [0.2.0] — 2026-09-16

### Added

- `n5-fangov setup` (profile detection, scope `local`/`lan`/`HOST:PORT`, admin user and
  password, backup of an existing config) and `n5-fangov passwd`.
- Built-in HTTPS: `[web] tls = auto|off|file`; automatic ECDSA P-256 certificate (10 years,
  CA flag with name constraints and `pathlen 0`, regenerated with the same key when the
  SANs change); a non-loopback listener never runs plain HTTP; TLS 1.2+, HSTS.
- PBKDF2-HMAC-SHA256 password hashes (`pbkdf2$210000$…`); the legacy `sha256("user:password")`
  form keeps working. Failed logins throttled per IP (5 free, then 250 ms doubling to 2 s),
  at most 4 delayed attempts in flight per IP (`429` beyond).
- Sandboxed unit: `ProtectSystem=strict` with explicit `ReadWritePaths`, empty capability
  set, `PrivateDevices`, `RestrictAddressFamilies`, `SystemCallFilter=@system-service`,
  `UMask=0077`, `LogsDirectory`.
- Rotating log file `[log] file/max_size_mb/max_files` under `/var/log/` only; Log tab with
  export and clear; `n5-fangov log [-n N] [--export FILE] [--clear]`.
- Settings bundle: `n5-fangov export|import`, `GET /api/config/export`,
  `POST /api/config/import` (everything validated before anything is written; password hash
  redacted to `<unchanged>`).
- apt hook `/etc/apt/apt.conf.d/90n5-fangov` → `n5-fangov check --after-update`: the DKMS
  fan module must exist for every kernel the box can boot into (running kernel plus the
  `proxmox-boot-tool` selection); missing → `kernel` alert, the apt run never fails.
- Debian package (`make deb`, no conffile), `make verify-deploy`.

### Fixed

- 20 findings of the v0.2 security review (`H1`–`H4`, `M1`–`M7`, `L1`–`L9`): fail-closed
  auth (misconfigured `auth = basic` binds to loopback), log path confined to `/var/log/`,
  `O_NOFOLLOW`, netlink only for the interface list, hash redaction in every output, …

## [0.1.0] — 2026-09-15

Port of the Bash regulator `n5-fand` to Go (developed as `pvefand`, then `ventula`, renamed
`n5-fangov` before the first tag). Not released as a GitHub release; the code is the base
of `v0.2.0`.

### Added

- Regulation loop with curve interpolation, slew (`step_up`/`step_down`), manual override,
  critical temperature (255 at once, also under override), stall detection (0 RPM at duty ≥
  `stall_min_duty` for `stall_cycles`), sensor plausibility and frozen-sensor check,
  read-back verification, per-channel safe duty, failsafe (all channels 255), profile-defined
  stop (N5 Pro: CPU/SSD back to EC automatic, HDD at a fixed duty because the EC does not
  regulate that channel after a write — measured 14 September 2026).
- Profiles `n5pro` (hardware-verified), `nct67xx`, `it87xx`, `monitor`; sensor sources
  `k10temp`, `coretemp`, `nvme:max`, `drivetemp:max`, `hwmon:<name>:tempN`, `ec:<label>`.
- TOML config with rule 8 (invalid values → defaults + warning + alert, never a failed
  start); presets as `[[channel]]` files.
- HTTP API and embedded dashboard (Overview, Curves, Manual, Presets, Log, Compatibility);
  unix socket for the CLI; Host-header guard (421), CSRF header, basic auth.
- systemd unit with `Type=notify`, watchdog, `ExecStartPre=check`, `ExecStopPost=failsafe`,
  onfailure unit with real cause and 30-min cooldown; PVE notification template pair.
- Fake sysfs fixture `testdata/sysfs/n5pro`; Docker build helper `tools/remote-go.ps1`.

### Fixed

- Safety review `H1`, `H2`, `M1`–`M4`, `L1`–`L7` (pwm3 never `auto`, missing N5 Pro
  channels added, device-lost restart, watchdog only while the loop is alive, …) and
  security review `H1`–`H3`, `M1`–`M4`, `L1`–`L6` (Host header, CSRF, socket mode 0750,
  body limits, …).

[Unreleased]: https://github.com/SirRenix/n5-fangov/compare/v0.3.0-rc1...HEAD
[0.3.0-rc1]: https://github.com/SirRenix/n5-fangov/releases/tag/v0.3.0-rc1
[0.3.0-beta.4]: https://github.com/SirRenix/n5-fangov/releases/tag/v0.3.0-beta.4
[0.3.0-beta.3]: https://github.com/SirRenix/n5-fangov/releases/tag/v0.3.0-beta.3
[0.3.0-beta.2]: https://github.com/SirRenix/n5-fangov/releases/tag/v0.3.0-beta.2
[0.3.0-beta.1]: https://github.com/SirRenix/n5-fangov/releases/tag/v0.3.0-beta.1
[0.2.1]: https://github.com/SirRenix/n5-fangov/releases/tag/v0.2.1
[0.2.0]: https://github.com/SirRenix/n5-fangov/releases/tag/v0.2.0
[0.1.0]: https://github.com/SirRenix/n5-fangov/commit/22bf33f
