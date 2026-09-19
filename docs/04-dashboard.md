# Dashboard

The web UI, page by page. The screenshots come from the built-in mock with example
values (`n5host`, `192.0.2.x`); the complete set: [screenshots/README.md](screenshots/README.md).

## Reaching it

`http://127.0.0.1:8010` (scope `local`) or `https://n5host:8010` (`lan`), as chosen in
[Setup](03-setup.md). With `auth = "basic"` an anonymous visitor gets the reduced
Overview and the About page; everything else appears after *Sign in*. The server
enforces this ([Who sees what](08-https-security.md#who-sees-what)). With
`auth = "none"` every visitor counts as signed in and there is no *Sign in*.

![Overview as an anonymous visitor: tiles and the two charts, sidebar with Overview and About](screenshots/01-overview-anonymous.png)

## The shell

**Sidebar** (220 px): the brand row with the **sidebar toggle** (panel-left icon), then
the pages in five groups: *Monitor* (Overview, System), *Control* (Fans, Schedules),
*Operate* (Alerts, Log), *Settings*, *Info* (About). The toggle, the `[` key and
*Navigation* in Settings → Display switch to the 56 px **icon rail** and back; the
choice is stored in this browser. Between 700 and 1099 px the rail is forced. The
footer shows the daemon's version.

![Sidebar collapsed to the icon rail](screenshots/05-sidebar-rail.png)

**Page header**: page title and profile title; on the right the status chip (`ok`,
`sensor-error`, `write-error`, `dry-run`), uptime, the version (`beta` badge on a
pre-release), the **certificate warning chip** while something is wrong (`plain HTTP`
off loopback, `certificate fallback`, `certificate expires in N d` below 30 days,
`certificate expired`; it leads to Settings → Certificate), `live`/`paused` (paused
while the tab is hidden; red after two failed polls), the user and *Sign in* / *Sign
out*. Narrow windows drop uptime and version (below 1000 px), then the chip text
(900 px), then everything but title, status, live dot and user button (700 px).

![Header: status chip, uptime, version, live, user, Sign out](screenshots/04-header.png)
![Header with the certificate warning chip](screenshots/27-header-cert-warning.png)

Below the header two banners can appear: *Connection to the daemon lost — retrying*
([below](#connection-loss-and-session-expiry)) and the restart notice of a settings
import. Toasts show at most three; a fourth drops the oldest.

**Phone** (below 700 px): a **bottom bar** with Overview · Fans · Alerts · Settings ·
*More*; *More* opens a sheet with System, Schedules, Log, About. Anonymous: Overview ·
More (About).

## Overview

**Tiles**, one per channel: name, `pwmN · sensor`, mode badge (`AUTO`, `MANUAL`,
`CRITICAL`, `STALL`, `SENSOR-ERROR`, `FAILSAFE`), the temperature (coloured by its
share of the lower of `critical` and the [ceiling](06-configuration.md#ceilings-and-the-emergency-action)),
`held …` while a [hysteresis](06-configuration.md#hysteresis-and-minimum-on-time) holds
the curve at another value, a `hold` badge with the remaining minimum on-time, a
**sparkline** of the last 2 h, the duty bar with a target marker while the slew is
moving (`33 % (85 → 107)`), and the RPM (`no tach` without tachometer, `0 rpm` in red).

**History**: range switch `2 h · 24 h · 7 d` (remembered in this browser) and, signed
in, *CSV*. 2 h is one point per cycle, polled every 30 s; 24 h and 7 d are one-minute
and five-minute means, reloaded every 60 s. The history survives restarts
(`/var/lib/n5-fangov/history.json`, saved every 10 minutes and at stop). *CSV*
downloads the selected range ([History and CSV](12-api.md#history-and-csv)).

**Charts**: *Temperature*, *Fan speed* (`RPM` / `Duty` switch) and, signed in and once
a sensor is watched, *Extra sensors* for the ids in `[dashboard] sensors`.

![Overview with the 24 h range: averaged charts, Www HH:MM axis, CSV button](screenshots/26-overview-24h.png)

Signed in, three cards follow:

- **Sensors** — every readable temperature (`GET /api/sensors`), grouped CPU / SSD ·
  NVMe / HDD / GPU / NIC / EC · board / other, each heading with count and live
  maximum. A group starts open when it holds a channel's or a charted sensor. Each
  row: description, id, live value and the *chart* toggle, which adds or removes the
  id in `[dashboard] sensors` (`PUT /api/dashboard`, at most 8). Watched sensors are
  read once per cycle, recorded in the history (`history[].extra`) and drawn in *Extra
  sensors*; they never influence regulation. An id whose device is absent is kept with
  a warning and charted once it appears ([Sensor ids](06-configuration.md#sensor-ids)).
- **System** — the inventory at a glance; *Details* opens the [System](#system) page.
- **Recent alerts** — kind, message, `not delivered: …` when the transport failed;
  *All* opens the [Alerts](#alerts) page.

![Overview signed in: tiles with sparklines, three charts, Sensors, System, Recent alerts](screenshots/03-overview-signed-in.png)

The page polls `/api/state` every 5 s (Settings → Display: 5, 10 or 30 s), the history
per range, alerts every 60 s and the system glance every 30 s; nothing while the tab is
hidden.

## System

The hardware inventory: host (hostname, OS, kernel, uptime, load), machine (vendor,
product, board, BIOS), CPU, fan controller (profile, hwmon path, module, version),
memory with the module table (slot, bank, size, type, speed, ECC, manufacturer, part
number), GPU · NPU (driver, version), network (physical NICs with state, speed,
duplex, model, driver, MAC, MTU, PCI address), storage (controllers; disks with size,
type, transport, model and the **live temperature** the `disk:<dev>` sensor uses).
`live HH:MM · static N ago` says when the live parts were read and how old the cached
static parts are (10 minutes); *Refresh* re-reads. `n5-fangov system` prints the same
from the shell (`--json`).

![System page: Host, Machine, CPU, Fan controller, Memory, GPU · NPU, Network, Storage](screenshots/06-system.png)

Everything comes from files the daemon can reach inside its sandbox (`/sys/class/dmi/id`,
`/proc/cpuinfo`, `/proc/meminfo`, `/sys/bus/pci/devices`, `/sys/class/accel`,
`/sys/class/drm`, `/sys/class/net`, `/sys/block`, `/sys/module`, `/etc/os-release`,
`/proc/uptime`, `/proc/loadavg`). Bridges, veth, tap, `lo`, zvols, loop, dm and ram
devices are skipped. The memory modules come from the SMBIOS table at
`/sys/firmware/dmi/tables/DMI`, parsed by the daemon: no `dmidecode`, no `/dev/mem`.
PCI device names need `lspci` (package `pciutils`); without it the entries carry
`PCI device <vendor>:<device>` ids and a notice says so. No serial numbers are read.

## Fans

Curves, manual override and presets: a **card per channel**, the **Presets** row, a
**sticky action bar**. The terms (`duty`, `critical`, `ceiling`, `stop`, `stall`,
`hysteresis`, `min on`) are explained under *More about duty, critical, stall, stop* on
the page and in [Configuration](06-configuration.md#curve-rules).

![Fans page: a card per channel with curve editor and Live & override, the Presets row, the sticky action bar](screenshots/07-fans.png)

### Channel card

Header: channel name, `pwmN · sensor`, the **preset badge** and the mode badge. The
preset badge lists **every** preset whose values this channel runs (`✓ alternative ·
n5pro-balanced` when a user preset shares the channel with a built-in), `custom` when
none matches. It shows what the daemon runs, not the editor's unsaved values.

**Curve editor** (left): *sensor* (the catalogue's ids with their live reading; a
channel with several sensors appears as one option `a,b (max of 2)`, the array itself
is edited in the file), *critical °C*, *stop*, *hysteresis °C*, *min on* (`off · 30 s ·
1 min · 2 min · 5 min · 10 min · 30 min · 1 h`); the canvas
with the curve, the dashed `crit` line, the dimmer dashed `ceiling` line and the dotted
`now` marker; the point table (*remove* disabled at two points, *+ add point* disabled
at eight). Drag the points on the canvas (touch works) or edit the table. Temperatures
are edited in °C whatever the display unit. On the N5 Pro the measured *Duty → RPM*
pairs sit under the table.

**Live & override** (right): reading → duty, RPM, `curve target N · mode` (or `held N
· mode` under an override), the **Manual override** switch, slider, number field and
*Set*.

- Switching **on** holds the duty the channel runs at that moment (`PUT
  /api/override/{name}`, raised to the HDD minimum where it applies); the toast reads
  `hdd: manual — holding 199 (78 %)`. Then *Set* sends the value slider and field show.
- Switching **off** is `DELETE /api/override/{name}`: back to the curve. While off,
  slider, number and *Set* are disabled.
- The daemon's snapshot follows one cycle later, so the switch shows the client's state
  for two regulation intervals, then the daemon's `mode`. `CRITICAL` and `STALL` do not
  touch the switch; the override persists underneath. Critical temperature and the
  stall guard override any manual value.
- **HDD-like channels** (a fixed `stop` duty, or pwm3 on the N5 Pro) refuse values
  below 60: the slider starts there. The daemon answers 400 to anything lower from
  any client.

The same from the shell: `n5-fangov set` / `auto` ([CLI](05-cli.md)).

![Fans, Manual override switched on for the HDD channel: held duty, slider at the minimum 60](screenshots/10-fans-manual-switch.png)

### Apply, Revert, validation

The action bar shows *no unsaved changes* or *unsaved changes* (the dot also sits on
the Fans entry of the sidebar), *Revert*, *Apply to daemon* and, with unsaved changes,
**Save as preset…**. Edits persist across page switches; *Sign out* asks before
discarding them, a session loss stashes them
([below](#connection-loss-and-session-expiry)).

*Save as preset…* opens the [preset editor](#presets) filled with the edited curves.
Saving stores the preset; the editor keeps its unsaved changes and nothing is written
to the daemon.

*Apply to daemon* checks the [curve rules](06-configuration.md#curve-rules) first —
2..8 points, temperatures whole numbers −20..120 °C strictly ascending, duties 0..255
never falling, critical above the last point and at most 150, stop `auto` or 60..255,
hysteresis 0..10, min on at most 1 h — and lists failures in a red notice, sending
nothing. When they pass, the editor re-reads the config file, replaces the
`[[channel]]` tables and writes `PUT /api/config?strict=1`; everything else in the file
stays byte-identical. 200 reloads without a restart. A changed channel set answers 202
and the notice *Written — restart required (channel set or profile changed): systemctl
restart n5-fangov* stays until *Revert*, the next apply or a page switch. *Revert*
reloads the daemon's curves.

While the page is open it re-reads config and presets every 30 s, so a preset applied
from another browser, the CLI or the scheduler shows up in the badges and the Presets
row. An editor with unsaved changes keeps them.

![Fans with a validation error: red notice, nothing sent](screenshots/08-fans-validation-error.png)
![Fans after Apply: restart required notice](screenshots/09-fans-restart-required.png)

### Presets

The **Presets** card lists the built-in N5 Pro sets and the files in
`/etc/n5-fangov/presets/`. Its first line is the **active set**: *Active set:
n5pro-balanced — the daemon runs exactly these values* when every channel matches one
preset, *Active set: custom (matches no preset)* otherwise. A chip per preset: active
dot, ★ *recommended*, name, *built-in* badge, description, *Apply* (disabled while
active), *Details* (built-in, read-only) or *Edit* (user preset) and *Delete* (user
preset, with confirmation).

**A channel can belong to several presets.** *Apply* switches only the channels whose
values differ. After applying a user preset that took two of its three channels from
`n5pro-balanced`, those two channels carry both names in their badge and the active
set names the user preset.

- *Apply* asks first (unsaved curve edits are discarded), merges the preset into the
  config file by pwm — channels the preset does not name stay — and reloads; 202 shows
  a restart notice. The built-in sets, the file format and the merge rule:
  [Presets](06-configuration.md#presets).
- *New preset…* opens the **preset editor**. *Start from* chooses the values: *the
  daemon (running curves)* (default), *the editor (unsaved values)* or any preset. Then
  the name (`a-z 0-9 _ -`, at most 64 characters; a built-in name is refused) and, per
  channel, critical / stop / hysteresis / min on and an editable point table. *Save
  preset* stores exactly the values shown (`PUT /api/presets/{name}`,
  [API](12-api.md#endpoints)); **nothing is applied**. An existing name asks before it
  is overwritten.
- *Edit* opens the same dialog for a user preset; changing the name renames the file
  (`POST …/rename`). Escape, the backdrop and *Cancel* ask before unsaved edits are dropped.
- A channel set that differs from the running config (names or pwms) is refused: a
  preset is applied to this host.

![Preset editor: Start from, name, per-channel fields and point tables](screenshots/11-preset-editor.png)

### Fans on a phone

Below 700 px a **channel selector** above the cards shows one channel at a time; the
dirty dot marks the edited channel.

![Mobile Fans: channel selector, one card](screenshots/24-mobile-fans.png)

## Schedules

The `[[schedule]]` tables as an editor plus a status card. One row per entry: the
**preset** select (a name the store lacks reads `<name> (missing)` and is flagged),
**From** / **To** (`to` before `from` crosses midnight), the **days** as seven toggles
Mo…Su (none pressed = every day), an `ACTIVE` mark on the entry in effect, *make
fallback* (the entry without a window; disabled while another fallback exists) / *add
window*, and remove. *Add entry* appends a row (disabled without a scheduler or without
presets); *Revert* reloads; *Save* is enabled once something changed.

![Schedules: editable rows, fallback, ACTIVE, Add entry, status card](screenshots/12-schedules.png)

*Save* checks first — every entry has a preset, `from` and `to` both set and different
on a windowed entry, at most one fallback, at most 16 entries — and lists what fails.
Then it re-reads the config file, replaces the `[[schedule]]` tables and writes
`PUT /api/config?strict=1`. The daemon takes the list without a restart. Editing the
file by hand writes the same tables ([Schedules](06-configuration.md#schedules)).

The **Status** card: **now**, the daemon's clock in the host's zone (`14:03:22 · CEST
+02:00`; `browser time` until the first snapshot), the active entry (`n5pro-balanced
(fallback)` or the window; `none (outside every window)`), the next switch (`in …` and
the preset; `no preset (no fallback)`; `none within 8 days`), the last switch with `ok`
or `failed: <reason>`, which also appears as a warning notice. The card refreshes every
60 s; a dirty editor is never rebuilt. Without a scheduler it reads `unavailable (501)`.

![Schedules with a failed last switch: preset missing flagged in the row, warning notice, error in the status](screenshots/13-schedules-last-switch-failed.png)

Leaving the page or signing out with unsaved edits asks; a session loss stashes them.
From 1400 px editor and status sit side by side.

## Alerts

What the daemon delivers and did deliver; the transport is configured in
[Settings → Alert transport](#alert-transport). The **Delivery** card: effective
transport, `pve-notify` and `mail(1)` availability, the webhook URL and format, the
cooldown, *Configure* and *Send test alert* (kind `test` through the real transport,
no cooldown; the toast reads `Test alert sent via <transport>` or the delivery error).
**Recent alerts**: the last 50, kept across restarts, `not delivered: …` in red. The
**kinds** table lists every alert kind with its description and last delivery. What
each kind means: [Alerts](07-alerts.md).

![Alerts page: Delivery card, recent alerts, kinds table](screenshots/14-alerts.png)
![Toast after Send test alert](screenshots/15-alerts-test-toast.png)

## Log

The daemon's log file (the journal when no file is configured): the last 200 lines,
re-read every 10 s. *Filter* (`n of m match`), *auto-scroll*, `source: file|journal`,
*Refresh*, *Export* (the whole current file). WARN and ERROR lines are coloured. *Clear
log* lives in Settings → Danger zone. File, rotation and the CLI form:
[Logs](10-troubleshooting.md#logs).

![Log page: filter, auto-scroll, Refresh, Export](screenshots/16-log.png)

## Settings

One page, seven sections, a **sub-navigation** on the left (chips above the sections
below 900 px). Each section has a deep link `#settings/<id>` ([Deep links](#deep-links)).

![Settings: Display, Account & sessions, API tokens, Certificate, Alert transport, Backup, Danger zone](screenshots/17-settings.png)

### Display

*Temperature unit* °C / °F (the curve editor stays in °C), *Refresh interval* 5 / 10 /
30 s, *Theme* system / dark / light, *Navigation* sidebar / icon rail (from 1100 px).
Stored in this browser only.

![Overview in the light theme](screenshots/22-overview-light.png)

### Account & sessions

`signed in as <user>`, *Change password…*, *Change user…*, *Sign out other sessions*,
and the **Sessions** table (id, created, last seen, expires with `· remembered`, IP;
the current one is marked `THIS SESSION`).

- *Change password…* / *Change user…* ask for the current password, write the new
  `password_hash` (or `user`) into the config file in place, apply it at once and sign
  every *other* session out. User names are `[A-Za-z0-9_.-]{1,32}`, passwords 8..128
  characters. A wrong current password counts as a failed login for the
  [rate limiter](08-https-security.md#login-throttling).
- The in-place edit handles the `[web]` header and the dotted `web.user = …` layout.
  An inline table `web = { … }` is refused: edit the file by hand. The same applies to
  `[alert]` and `[dashboard]`.
- `n5-fangov passwd` works from the shell, also for a forgotten password
  ([Setup](03-setup.md#change-user-or-password-later)).
- With `auth = "none"` the account forms answer `409 auth is none`.

Session lifetime and what *Sign out* can and cannot do: [Sessions](08-https-security.md#sessions).

### API tokens

Shown to browser sessions and Basic auth only; a token can never manage tokens. With
`auth = "none"` the section is hidden. The table lists every token (name and id, scope,
created, expires, last used, last address) with *Revoke*. *Create token…* takes a name,
the scope (`read`, `control`, `admin`) and the expiry (`30 d · 90 d · 1 y · never`).
After *Create* the secret is shown **once** with *Copy*; *Done* hides it. What a token
can never do, storage, throttling: [API tokens](08-https-security.md#api-tokens). Using
it: [API and integrations](12-api.md).

![Settings, API tokens: the token table, the secret shown once with Copy](screenshots/19-settings-tokens.png)

### Certificate

The badge reads *automatic*, *own certificate*, *automatic (fallback)* or *TLS off*.
With TLS off the section is one sentence. Otherwise: subject, issuer, validity (`— in N
days` below 30 days, `— expired`), key type, serial, the SAN chips, the SHA-256
fingerprint with *copy*, the server's warnings, *Download .crt* (PEM: Firefox, macOS,
Linux), *Download .cer* (DER: Windows, Android), *Regenerate…* (with *generate a new
key (breaks imported trust)*; automatic mode only, hidden in file mode and in
fallback), *Upload own certificate…* (files or PEM text; after a 400 with
`force_required` the *install anyway* checkbox appears), *Back to auto* (file mode and
fallback only), and the collapsible **How to trust this certificate** with the recipes
for Windows, macOS, Firefox, Android and Linux. What each action does:
[The certificate](08-https-security.md#the-certificate).

![Certificate section, automatic mode: fingerprint, SAN chips, downloads, How to trust opened](screenshots/18a-settings-certificate-auto.png)
![Certificate section in fallback mode: warn badge and the header chip](screenshots/18b-certificate-fallback.png)
![Certificate section with an expiry warning](screenshots/18c-certificate-soon.png)
![Certificate section with TLS off](screenshots/18d-certificate-off.png)

### Alert transport

*Transport* (`auto`, `pve`, `mail`, `webhook`, `log`, `off`; transports the box lacks
are disabled), *Mail to* for `auto`/`mail`, *URL* and *Format* (`json` / `text`) for
`webhook`, *Save*: written to the config file in place and applied without a restart.
Below: the **PVE notification template** card (*Install template* / *Update template*;
only where PVE notifications are available) and *Send test alert*. Payloads and the PVE
side: [Alerts](07-alerts.md).

### Backup

*Export settings* downloads config plus presets as one JSON bundle; *Import settings…*
replaces both after validation (asks first; 202 shows a restart notice). Tokens,
sessions and the history are never part of it
([Backup and restore](09-updates.md#backup-and-restore)).

### Danger zone

*Clear log* (truncates the current log file; disabled with the journal as source),
*Regenerate with new key* (breaks the trust imported on every client), *Back to auto*
(removes an uploaded certificate and its key). The two certificate buttons are enabled
only where they apply: *Regenerate* in automatic mode, *Back to auto* in file mode
and fallback. Each asks first.

## About

Public: name, version, description, licence, repository, author, the releases page and
the credits: [`ltdstudio/minisforum-n5-it5571`](https://github.com/ltdstudio/minisforum-n5-it5571)
(the kernel driver) and [`Sl0thC0der/proxfansx`](https://github.com/Sl0thC0der/proxfansx)
(the dashboard idea and the generic NCT67xx/IT87xx handling; no code shared). Signed
in, the line *built with* (the Go version) appears and the **Compatibility** card
lists the profiles this build knows: name with `ACTIVE`,
title, *verified on hardware* / *from documentation · untested*, notes; deep link
`#about/compat`.

![About with the Compatibility card](screenshots/20-about-compatibility.png)

## Dialogs

- **Sign in** — *User*, *Password*, *Remember me* (30 days on this browser, else 12 h),
  an inline error (`invalid user or password`, `too many attempts`) and the hint
  *Forgot the password? On the host, as root: `n5-fangov passwd`*. Root on the box is
  the only recovery path.
- **Confirm** — title, text, *Cancel* and the action; a destructive one is red and the
  focus starts on *Cancel*. Escape and the backdrop answer *no*.
- **Preset editor** — [above](#presets).
- **More** — the phone's page sheet.

![Sign-in dialog with Remember me and the recovery hint](screenshots/02-login-dialog.png)

## Keyboard and screen readers

- *Skip to content* is the first focusable element.
- Sidebar and bottom bar are `nav` elements with a `button` per page; the current page
  carries `aria-current="page"`. A page switch moves the focus to the page heading.
- Every curve point is focusable (`role="slider"`); the arrow keys move it by 1 °C /
  5 duty, Shift × 5, within its neighbours.
- The override is a `role="switch"`; while a change is in flight it is `aria-busy`,
  never disabled, so the focus stays.
- Day toggles, the *chart* toggles and the range/metric switches carry `aria-pressed`.
- Validation notices are `role="alert"`; the status chip is `role="status"`; toasts use
  a polite region, errors an assertive one.
- Icon-only controls carry `aria-label`; every table has column headers.

## Theme and motion

Dark by default when the system reports no preference. Colours, spacing, radii and the
type scale are tokens in `app.css`. The live dot pulses unless the browser reports
*prefers-reduced-motion* (Windows: *Accessibility → Visual effects → Animation effects*
off, or the energy-saver mode); the same preference stops the duty-bar and switch
transitions and the toast animation.

## Deep links

`#overview`, `#system`, `#fans`, `#schedules`, `#alerts`, `#log`, `#settings`, `#about`;
`#settings/<section>` with `st-display`, `st-account`, `st-tokens`, `st-cert`,
`st-alerts`, `st-backup`, `st-danger`; `#about/compat`. A page that needs a login, or an
unknown hash, falls back to the Overview; Back and Forward follow the hash. In mock
mode `?tab=` is accepted once and maps `curves`, `manual`, `presets` to `fans` and
`compat` to `about`.

## The mock

`index.html?mock=1` renders the dashboard against `mock.js`, a separate script the
production page never requests, with documentation values (`n5host`, `192.0.2.x`,
`n5.lan`; login `admin`/`admin`). Flags:

| Flag | Effect |
|---|---|
| `&user=1` | signed in |
| `&auth=none` | `auth = "none"`: everyone signed in, no user, no Sign in/out |
| `&tls=off\|file\|soon\|fallback` | certificate states: TLS off on loopback, own certificate, expiring in 12 days, fallback to the automatic one |
| `&tab=curves\|manual\|presets\|compat\|<page>` | mapped to the page ([Deep links](#deep-links)) |
| `&syserr=1` | System: source notice for a missing `lspci` |
| `&reject=1` | `PUT /api/config?strict=1` answers 400 |
| `&restart=1` | `PUT /api/config` answers 202 (restart notice) |
| `&expire=1` | the session dies 15 s after the boot |
| `&schedfail=1` | the last schedule switch failed; the first entry names a preset the store lacks |
| `&pwm4=1` | a fourth channel `pcie` (pwm 4, no tachometer) |
| `&lag=1` | an override PUT/DELETE shows in `/api/state` only two polls later, the daemon's next-cycle lag |
| `&down=1` | state, history and sensors unreachable from 2 s after the boot: the connection banner |

The mock implements every endpoint of the API, including tokens (`n5t_mock…`),
schedules, the history tiers, CSV, the webhook status, `disk:*` sensors and the preset
body; it validates curves, stop and min on with the daemon's rules. Its presets are the
three built-ins, `summer` and `alternative` (cpu and ssd from `n5pro-balanced`, its own
hdd curve). Regenerating the screenshots: [screenshots/README.md](screenshots/README.md);
the Fans control paths: [fans-matrix.mjs](11-development.md#fans-scenario-matrix).

## Connection loss and session expiry

- **Connection loss** — after two consecutive failed polls (network error or a 5xx) the
  banner *Connection to the daemon lost — retrying. Fans keep running on the daemon
  side.* appears and the live dot turns red. Polling continues; the banner goes with
  the next answer ([Troubleshooting](10-troubleshooting.md)).
- **Session expiry** — a 401 on a protected call while signed in means the session is
  gone (12 h, 30 d with *Remember me*, or the password was changed outside the
  dashboard). The page returns to the anonymous Overview and toasts *Session expired —
  sign in again*, with *— curve edits kept* or *— schedule edits kept* when there were
  unsaved edits. Those edits are restored after the next sign-in, with a notice on the
  page.
- *Sign out* with unsaved edits asks first; closing the tab triggers the browser's own
  prompt.

![Connection-lost banner above the Overview](screenshots/21-connection-lost.png)

## On a phone

Below 700 px: bottom bar and *More* sheet instead of the sidebar, one-line header,
tiles and cards stacked, the Fans page with its channel selector, schedule entries as
stacked blocks, settings sub-navigation as chips; curve points drag by touch.

![Mobile Overview, anonymous: bottom bar Overview · More](screenshots/23a-mobile-overview-anonymous.png)
![Mobile Overview, signed in: bottom bar Overview · Fans · Alerts · Settings · More](screenshots/23b-mobile-overview-signed-in.png)
![Mobile More sheet: the remaining pages with their group](screenshots/25-mobile-more-sheet.png)
