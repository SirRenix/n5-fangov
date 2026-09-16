# Changelog

All notable changes to n5-fangov. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions follow [Semantic Versioning](https://semver.org/). The version string of a build is
`n5-fangov version` (source: `internal/version/version.go`); the Debian package maps a
pre-release suffix to `~` (`0.3.0~beta.4`).

Review findings referenced as `M1`…`M7`, `H1`…`H4`, `L1`…`L9` (reviews of v0.1/v0.2) and
`R-M*`/`R-L*` (review of v0.3.0-beta) are the tags of the review rounds; since 0.3.1 they
live in [`docs/REVIEW-TAGS.md`](docs/REVIEW-TAGS.md), not in the code. The audit that
drove the *Unreleased* work is `docs/AUDIT.md` (code) and `docs/DESIGN-AUDIT.md`
(dashboard).

## [Unreleased]

### Version plan (operator decision 2026-09-16, 22:30)

- **0.3.x** (`0.3.1-rc*` → `0.3.1`, then `0.3.2`…; 0.3.0 stayed an rc): every feature of the
  plan (interface, regulation add-ons, dashboard history and per-device sensors, maintenance
  debt), as pre-releases on the private repository, each verified on the reference host.
- **0.4.0**: the dashboard redesign (`docs/design/REDESIGN-CONCEPT.md`) with new documentation
  screenshots — the release that goes **public**. The repository stays private until every
  test has passed, at the latest when all 0.3.x features are in.
- Each rc becomes a release only through the release gate below.

### Still open from the 0.3.x plan (decided 2026-09-16)

Points 1–4 of the plan (interface, regulation add-ons, dashboard, maintenance debt) shipped
in 0.3.1-rc1 below. Open:

- Repository public after the history rewrite; upstream issues (driver validation data,
  ProxFansX compatibility note); DKMS `.deb` in the sibling repository with the header
  meta-package as dependency (user path: two `apt install` + `setup`).
- Reboot proof on the reference host (DKMS + daemon together) — the last open operations
  question, no code; part of the release-gate test.
- Measure whether the EC regulates pwm4 again after a write (DESIGN §6 says "not measured").

### Planned (0.4.0, separate session) — dashboard redesign, public release

Structural redesign of the dashboard (sidebar navigation, channel-centric Fans page,
consolidated Settings page, SVG icons, sparklines) — concept and hand-over in
[`docs/design/REDESIGN-CONCEPT.md`](docs/design/REDESIGN-CONCEPT.md). Deliberately after
the 0.3.x features so tokens, schedules and the longer history have their place in the new
structure; prototype in the mock first, operator decides on screenshots; every documentation
screenshot is regenerated for the new pages. 0.4.0 is the version that switches the
repository to public.

### Before the public release (documentation) — done 2026-09-16

- **Split the README** (done): a short landing page (what it is, one screenshot, three-step
  install, links) and a `docs/` set with one page per topic (install, kernel driver on the
  N5 Pro, setup, dashboard guide with screenshots, CLI, configuration, alerts, HTTPS and
  security, updates and rollback, troubleshooting, development). The 880-line README is
  complete but tiring; a reader needs a table of contents and separation.
- **Kernel driver page** (done, `docs/02-kernel-driver.md`): what the EC driver is, what DKMS does for it, install from the
  sibling repository (later its `.deb`), verification (`dkms status`, `sensors`,
  `n5-fangov detect`), the kernel-update gate, removal — the topic first-time users
  stumble over.

### Release gate (decided 2026-09-16)

A version loses its `-rc` suffix only after the operator has performed a **manual
installation test on the real host following the documentation alone** — uninstall,
kernel-driver check, install by release path and by package, setup, dashboard, update
hook, reboot, troubleshooting, uninstall/reinstall. Checklist: [`docs/RELEASE-GATE.md`](docs/RELEASE-GATE.md).
Applies to every 0.3.x release and to 0.4.0. Preconditions: docs split, repository hardening
merged, history rewritten (all done). While the repository is private the tester downloads
the release assets with `gh release download <tag>` instead of the public URL; the public
curl path is re-run once at 0.4.0.

**Not planned**: MQTT/discovery (REST + token is enough and smaller), a German UI
(audience is GitHub), multi-host management, a frontend framework.

## [0.3.1-rc1] — 2026-09-17

The 0.3.x feature set in one release candidate (0.3.0-rc1 was never released — the gate
test runs on this one): API tokens, OpenAPI, webhook alerts, curve post-processing,
composite and per-disk sensors, pwm4, schedules, tiered history with CSV, the dashboard
for all of it, the maintenance debt from the audit, and the findings of the three review
rounds that followed the merge. Verified in the Docker builder (`go vet`, `go test`,
`-race`); the live verification on the reference host is recorded in the release notes.

### Added

- **API tokens** for scripts, Home Assistant and agents: `Authorization: Bearer n5t_…`
  next to the session cookie and Basic auth. A token has a name (unique,
  `^[A-Za-z0-9][A-Za-z0-9 ._-]{0,31}$`), a cumulative **scope** — `read` (state, history,
  system, sensors, profiles, presets, alerts, dashboard, tls info), `control` (read +
  overrides, preset apply, dashboard sensors), `admin` (everything the dashboard can do) —
  and an optional expiry (default 90 days, `0` = never with a warning). Stored like the
  sessions: `sha256(secret) → {id, name, scope, created, expires, last_used, last_ip}` in
  `/var/lib/n5-fangov/tokens.json` (0600, atomic, cap 50); last use and address refreshed
  at most once a minute; a password change or "sign out other sessions" leaves tokens
  valid, revocation is explicit. Endpoints `GET/POST /api/tokens`, `DELETE
  /api/tokens/{id}` and the CLI `n5-fangov token create NAME [--scope S] [--ttl DAYS] |
  list | revoke ID` (over the socket; the secret alone on stdout). Rules: a token never
  reaches `/api/tokens*`, `/api/account/*`, `/api/login`, `/api/logout` (403, so a leaked
  admin token cannot mint tokens or change the password); out of scope → 403 with
  `scope` and `required`; a bearer caller needs no CSRF header (cookie and Basic callers
  still do); a rejected token is counted and logged like a wrong password
  (`web: bearer token rejected from <ip>: unknown|expired`) without touching the PBKDF2
  semaphore; 20 req/s sustained, burst 40 per token (429 `token rate limit`); with
  `auth = "none"` a Bearer header is ignored. `GET /api/session` reports `via: "bearer"`,
  `scope`, `token_id` and the token name as `user`.
- **OpenAPI** `GET /api/openapi.json` (public, `Cache-Control: no-store`, ETag = sha256 of
  the body, 304 on `If-None-Match`): an OpenAPI 3.1 document rendered once at start from
  the route table in `internal/web/openapi.go` — the only place a route is declared;
  `routes()` registers from it and the guard takes visibility and scope from it.
  Security schemes `bearer`/`basic`/`cookie`, per operation summary, parameters,
  request body schema, responses (error codes reference `Error`), `x-class`, `x-scope`;
  component schemas `Error, State, Channel, HistoryPoint, Override, Version, Session,
  Token, TokenCreate, TokenCreated, Alerts, AlertsUpdate, Schedules, Dashboard`.
  `TestOpenAPICoversRoutes` pins the table to the document in both directions.
- **Webhook alert transport** `[alert] transport = "webhook"` with `webhook_url` (absolute
  http/https, host, no userinfo, ≤ 2048; without a valid URL the transport falls back to
  `auto` with a warning) and `webhook_format = "json" | "text"`: one POST per alert,
  `User-Agent: n5-fangov/<version>`, headers `X-N5-Fangov-Kind` and `Title`, JSON body
  `{type, kind, severity, hostname, title, message, ts}` (Gotify, Home Assistant) or the
  bare message as `text/plain` (ntfy); 30 s bound, redirects not followed, 2xx = delivered,
  anything else the delivery error `webhook: …`. Never chosen by `auto`. `PUT /api/alerts`
  takes `webhook_url` and `webhook_format` (omitted keys keep their value), `GET
  /api/alerts` carries the full URL for the operator's cookie, Basic and socket callers
  (redacted for a token caller); log lines, `check` and `n5-fangov alerts status` show it
  as scheme, host and first path segment only.
- **Hysteresis and minimum on-time per channel** (`[[channel]] hysteresis = 0..10`,
  `min_on = "0s".."1h"`, both off by default): the curve is evaluated at a held temperature
  that follows the reading only on a move of `hysteresis` degrees or more, and a rise of
  the curve target is held for `min_on` (a further rise above the held value restarts the
  timer). Only the curve output is touched — override, critical (judged on the raw
  reading), stall, failsafe, slew and the safe duty are unchanged and the safety tests pin
  that. Both keys reload live; a reduced `min_on` shortens a running hold. The snapshot
  carries `held_temp` (when it differs from `temp`) and `hold_until` (unix time) per
  channel; `GET /api/version` `limits` gains `hysteresis_max` and `min_on_max_s`; preset
  details and `GET /api/config` carry the two keys per channel.
- **Several sensors per channel**: `sensor = ["drivetemp:max", "ec:hdd"]` (1..4 ids, also
  accepted as `"a,b"`) regulates on the maximum of the parts; a part whose device is absent
  is skipped as long as one resolves (picked up at the next re-resolve). The stored id is
  the comma-joined form; a composite that contains `drivetemp:max` defaults to `stop = 140`
  like the plain id.
- **Per-disk sensors** `disk:<dev>` (`disk:sda`, `disk:nvme0n1`): the temperature of one
  block device from its hwmon (`/sys/block/<dev>/device/hwmon/hwmonN` for SATA/SAS,
  `/sys/block/<dev>/device/hwmonN` for NVMe). `GET /api/sensors` lists one per disk with
  the model, the hwmon name and `kind` (`ssd`/`hdd`); `GET /api/system` reports the live
  `temp_c` (null without a sensor) and the matching `sensor` id per disk, re-read on every
  call. The dashboard groups them by `kind`; the System tab shows the column.
- **pwm4** on the N5 Pro (PCIe header, no tachometer) as an optional fourth channel: a
  `[[channel]] pwm = 4` is managed as configured (`rpm = -1`, never in the stall check,
  `stop = "auto"` hands it back to the EC); `SanitizeChannels` neither adds nor corrects it
  and a built-in preset keeps it (see the merge rule below). `test`, `detect` and `check`
  treat it as a channel without tach.
- **Schedules**: `[[schedule]] preset = "n5pro-quiet" from = "22:00" to = "07:00"
  days = ["fri", "sat"]` switches presets by local time; an entry without `from`/`to` is
  the fallback outside every window (at most 16 entries, one fallback, invalid entries
  dropped with a warning). The scheduler (`cmd/n5-fangov/scheduler.go`, `internal/schedule`)
  evaluates every 30 s and once after READY, applies the preset on transitions only (a
  manual apply or curve edit inside a window stands until the next switch) through the same
  merge-by-pwm apply the API uses, and raises the new `schedule` alert (cooled 30 min) when a
  switch fails — the previous curves stay, the retry happens at the next transition. Every
  reload (config PUT, import, preset apply) hands the list to the scheduler. New endpoint
  `GET /api/schedules` (`{entries, active, next, last, timezone}`), `GET /api/config` carries
  `schedule[]`. The Presets tab shows the Schedules card (below).
- **Longer history**: the chart history is a tiered store (`internal/history`):
  raw points for 2 h, 1-minute means for 24 h, 5-minute means for 7 days, persisted to
  `/var/lib/n5-fangov/history.json` (0600, every 10 min and on stop; an unwritable state dir
  keeps it in memory) and reloaded at start. `GET /api/history?minutes=N` accepts up to 10080
  and answers from the tier that matches the span; `GET /api/history.csv?minutes=N`
  (protected) exports it as CSV (`ts,time,<ch>_temp,<ch>_duty,<ch>_rpm,…,<extra id>…`,
  `time` RFC 3339 local). The Overview has the range selector and the CSV button (below).

- Dashboard, Overview: a **range selector** `2 h · 24 h · 7 d` for the temperature, fan and
  extra-sensor charts (persisted in `localStorage`; 2 h keeps the incremental `since`
  poll every 30 s, 24 h and 7 d reload the averaged tier every 60 s; grid and axis labels
  per range — `HH:MM`, `Www HH:MM`, `dd.mm HH:MM` — aligned to local time) and a **CSV**
  button (`GET /api/history.csv?minutes=…`, signed in). Channel cards show the
  hysteresis-held temperature (`held …`) and a `hold` badge with the remaining `min_on`
  time; a channel without tachometer reads `no tach`. The Sensors card groups `disk:*`
  ids into SSD · NVMe or HDD by the catalogue's `kind`.
- Dashboard, Curves: **hysteresis** (0..`hysteresis_max`) and **min on** (`off · 30 s · 1 min
  · 2 min · 5 min · 10 min · 30 min · 1 h`, Go duration strings both ways) next to
  critical/stop; a composite sensor is one option `a,b (max of 2)`; the written
  `[[channel]]` tables carry `sensor = ["a", "b"]`, `hysteresis` and `min_on` (omitted at
  their defaults); client validation covers hysteresis. **Keyboard**: every curve point is
  focusable (`role="slider"`, value text) and moves with the arrow keys by 1 °C / 5 duty,
  Shift × 5. Preset details list hysteresis and min on.
- Dashboard, Presets: a read-only **Schedules card** (`GET /api/schedules`, polled every
  60 s while the tab is current): preset, window or *fallback*, days, ACTIVE, next switch
  (relative, absolute on hover), last switch with a failed one as warn notice, timezone,
  the hint that `[[schedule]]` is edited in the config; 501 → `schedules: unavailable`.
- Dashboard, Alerts: transport **webhook** with URL (required) and format (`json` / `text`);
  `mail_to` is shown for auto/mail only; the status lists the configured webhook URL.
- Dashboard, System: the storage table has a **temperature** column (`temp_c`, unit aware,
  `—` without a sensor).
- Dashboard, Account dialog: **API tokens** section (cookie/basic sessions only): table
  name · scope · created · expires · last used · last address, *Revoke* with confirm,
  *Create token…* with scope explanations and expiry `30 d · 90 d · 1 y · never`
  (`ttl_days` 30/90/365/0, warn notice for never); the secret appears once in a read-only
  field with *Copy* (clipboard, fallback selects the text).
- Mock flags `&schedfail=1` (last schedule switch failed) and `&pwm4=1` (fourth channel
  `pcie` without tachometer); the mock implements tokens, schedules, history tiers with a
  day/night shape, CSV, webhook alerts, `openapi.json`, `disk:*` sensors with `kind`,
  storage `temp_c`, `hysteresis`/`min_on` and the new `/api/version` limits.
- Documentation for the 0.3.x features: new page [`docs/12-api.md`](docs/12-api.md)
  (authentication and tokens, scopes, OpenAPI, endpoint overview, history/CSV, the
  Home Assistant recipe with the YAML in `docs/openapi/`); tokens, webhook, hysteresis
  and `min_on`, sensor arrays and `disk:<dev>`, pwm4, `[[schedule]]`, history ranges and
  the new state files on the existing pages; `deploy/config.example.toml` carries every
  new key with a commented pwm4 channel and schedule pair.

### Changed

- Preset apply **merges by pwm** instead of replacing every `[[channel]]` table: a preset
  channel replaces the config channel with the same pwm (the config channel's name is
  kept), config channels the preset does not name stay, a pwm the config lacks is added
  (that case still answers 202). The scheduler uses the same path.
- Dashboard: the mock is split out of the production bundle into `mock.js` (fourth static
  file, `window.n5mock`), inserted by `app.js` only behind `?mock=1`; `index.html` never
  references it. Budgets: `app.js` ≤ 96 KB, `mock.js` ≤ 40 KB (`web_test.go`). Settings in
  `localStorage` are whitelisted per key (unit, interval, theme, range).
- Dashboard: at most three toasts per live region; the oldest is dropped when a fourth
  arrives (DESIGN-AUDIT B30).

- Files are named by topic, never by version or review round. cmd: `wiring_v2.go` →
  `wiring_tls.go`; `wiring_v3.go` → `wiring_presets.go` (with the `dirPresetStore` parts
  of `wiring.go`), `wiring_alerts.go`, `wiring_account.go`, `wiring_dashboard.go`,
  `about.go`. web: `v3.go` → `session_api.go`, `account_api.go`, `alerts_api.go`,
  `dashboard_api.go`, `presets_api.go` (`decodeJSON` joins the helpers in `web.go`);
  `v3_types.go` → `types.go`. Tests: `v2_test`, `v3_test`, `v3_fix_test`, `v4_test`,
  `v4_fix_test`, `tlsmgr_fix_test` in cmd, alert, config, control, logfile, sysinfo and
  web are regrouped into files named after the file or topic they test (cmd: `bundle`,
  `setup`, `cert`, `cli`, `wiring`, `wiring_account`, `wiring_alerts`, `wiring_dashboard`,
  `wiring_presets`, `about`, merged into `check`, `common`, `alert_cooldown`, `tlsmgr`;
  web: `session`, `account`, `alerts`, `dashboard`, `presets`, `ratelimit`, `config_api`,
  `errlog`, `log_api`, `bundle`, merged into `web`, `tls`; alert: `template`, `ring`,
  merged into `alert`; config: `presets`, merged into `config`; control: `alert`, merged
  into `controller`; logfile and sysinfo merged into their existing test files). Test
  bodies are unchanged.
- `cmdServe` is split into the stages `serveConfig` (config, warnings, run dir, state dir,
  alert manager, log file), `serveDevice` (profile detection, controller), `serveWeb`
  (TLS manager, listener decision, stores, web server) and `serveRun` (goroutines,
  watchdog ticker, READY, wait, stop), sharing a `serveState`; same log lines, exit codes
  and order of side effects.
- The controller `cycle` is split into `readSensors`, `computeTargets`, `checkStall` and
  `writeAndFinish`, called in that order; the regulation logic is unchanged (the safety
  tests pin it).
- Review tags (`H1`…`H4`, `M1`…`M7`, `L1`…`L9`, `R-M1`…`R-M3`, `R-L4`…`R-L11`, `R-U8`)
  are gone from code comments and test names; the comments keep the reason, the legend
  is [`docs/REVIEW-TAGS.md`](docs/REVIEW-TAGS.md) (tag, finding, where fixed, release,
  per package). The safety tests are named after what they pin
  (`TestN5ProMissingChannelsAdded`, `TestDeviceLostEndsRun`, …).
- Unit: `ReadWritePaths` carries `-/sys/devices/platform` instead of `-/sys/devices` —
  the pwm files of it5571, nct6775 and it87 live under
  `/sys/devices/platform/<driver>/hwmon/hwmonN`, `/sys/class/hwmon` holds the symlinks;
  the rest of `/sys` (sensors, DMI, disk temperatures) is read-only. To be verified on the
  reference host with the next install test (release gate).

### Fixed

- `GET`/`PUT /api/alerts` handed the full webhook URL — with the receiver's key in its
  query or path — to any API token, even a `read` one; a token caller now gets it
  redacted, the operator's cookie, Basic and socket callers still see it in full.
- A valid API token reset the auth limiter's delay counter of its address, so a
  password guesser with one working token could clear its delay between attempts; bearer
  failures live in buckets of their own now, a token success resets only those (and a
  password success only the password bucket); the concurrency cap stays shared.
- The config warning about an invalid `webhook_url` quoted the value into the journal and
  the dashboard, userinfo and key included; it names scheme and host only.
- Preset apply (dashboard and scheduler) rewrote the whole config from the parsed struct,
  losing every comment and reordering the file; the merged `[[channel]]` tables are now
  spliced into the file text in place, everything else stays byte-identical (fallback to
  the full rewrite only for an inline `channel = [{…}]` table the splice cannot replace).
- The merge by pwm reset a channel's `hysteresis` and `min_on` to the defaults whenever the
  preset did not carry them (every preset written before 0.3.1, every built-in); the config
  channel's values are kept unless the preset table sets the keys explicitly.
- The collision rename of an added preset channel (`pwm<N>`) could itself collide; it
  counts on (`pwm<N>_2`, …) until the name is free.
- `RedactURL` kept the whole path, so a Home Assistant webhook id (`/api/webhook/<id>`)
  reached the log and the CLI; only the first path segment stays, and a URL that does not
  parse is cut by the same rules — never with its userinfo.
- `PUT /api/alerts` merged the request onto the section in effect, not the file: a URL or
  recipient that `PUT /api/config` (answered 202) or a hand edit had put into the file was
  overwritten or missed. The merge basis is now the file's `[alert]` section read under
  the file lock, and only the keys the request sets are written.
- A reload that returned `ErrRestartRequired` skipped the alert manager and the scheduler,
  so the file's `[alert]` and `[[schedule]]` were not in effect until the restart; both are
  taken from the written file on 202 as well.
- `GET /api/schedules` `last.error` carried the full apply error (file paths, parser
  text); it carries the class (`preset missing`, `preset invalid`, `write failed`,
  `reload failed`, `restart required`), the full text goes to the log.
- The scheduler's first evaluation with no entries, or outside every window without a
  fallback, counted as a transition and logged "no window active"; it is no transition.
  A scheduler wired without a preset applier logs the wiring error instead of panicking.
- A `[[schedule]]` entry whose `from`/`to` were TOML time literals instead of strings
  read as absent and became the fallback; the entry is dropped with a warning. A fallback
  with `days` keeps the days silently; they are cleared with a warning.
- `schedule.Equal` compared the day lists by position, so a reordered `days` counted as a
  new entry and re-applied the preset on the next tick; the days compare as a set.
- The 1-minute and 5-minute history means took the controller's `-1` ("duty unknown after
  a failed write") as a value and pulled the mean below every real duty; the marker stays
  in the raw point and is skipped by the means.
- `Stop` saved the history file while holding the hardware mutex; the save runs after the
  safe-state writes have released it.
- `POST /api/tokens` minted tokens with `auth = "none"`, where anyone reaching the listener
  could and the guard ignored them anyway; it answers 409 `auth is none` like the account
  changes (list and revoke stay).
- The OpenAPI `Version.limits` schema lacked `hysteresis_max` and `min_on_max_s`; the
  shape test now pins the schema's properties to the JSON keys of `GET /api/version`.
- The pre-commit hook used the deprecated `gitleaks protect --staged`; it probes for `git
  --staged` (gitleaks 8.19+) and falls back to the old spelling.
- The settings bundle keeps the webhook URL in full (a setting the bundle exists to carry;
  only the password hash is redacted) — stated in DESIGN §12 rather than changed.
- Curve editor: *Apply* dropped every `[[schedule]]` table that followed a `[[channel]]`
  table — the rewrite ended a channel block at `[section]` headers only, so the next
  header `[[schedule]]` was swallowed with the channel and the daemon ran without
  schedules after the first apply. The block now ends at any table header; a multi-line
  `curve` array no longer leaks its element lines either. The mock parses the
  `[[schedule]]` tables back on `PUT /api/config`, so a dropped table is visible there
  (`TestCurveEditorKeepsOtherTables`).
- Curve editor: the *restart required* notice and the warnings of an apply were cleared
  by the editor reload right after the answer; they now stay until *Revert*, the next
  apply or a session change. Screenshot 09 is taken through a real 202 (mock flag
  `&restart=1`).
- Overview: the 2 h history kept at most 720 points, which assumed a 10 s interval — with
  `interval = "2s"` the chart showed 24 minutes. The cap follows `[daemon] interval` from
  the config (anonymous: the window alone trims).
- About: *built with* appears after signing in and disappears after signing out; the page
  was fetched once at boot only.
- About: the mock hint lists `&restart=1`.
- DESIGN §11: curve edits persist across tab changes (no dialog), *Sign out* asks, a
  session loss stashes them; gear and lock are toggled by the id list in `applyAuth`,
  not by `data-auth`.
- Docs: `limits` in `GET /api/version` lists its real keys (configuration); the webhook
  URL is redacted for token callers and shown in full to a browser session only (alerts,
  security, API); `starting` in the `/api/state` status list (API); behind a reverse proxy
  the throttling buckets, the per-address cap, `last_ip` and the log see the proxy's
  address — rate-limit at the proxy (security); a preset apply or scheduled switch
  rewrites the `[[channel]]` tables only and keeps `hysteresis`/`min_on` the preset does
  not set, the scheduler applies the active entry once after every start, `days` on the
  fallback is ignored with a warning (configuration); mock flags listed completely
  (development).

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
