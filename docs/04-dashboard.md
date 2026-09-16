# Dashboard

What this page covers: every part of the web UI — header, tabs, settings gear, lock,
account — with a screenshot each. The screenshots come from the built-in mock
(example values, `n5host`, `192.0.2.x`); the complete set is listed in
[screenshots/README.md](screenshots/README.md).

- [Reaching it](#reaching-it)
- [Header and tab bar](#header-and-tab-bar)
- [Overview](#overview)
- [Curves](#curves)
- [Manual](#manual)
- [Presets](#presets)
- [Alerts](#alerts)
- [System](#system)
- [Log](#log)
- [Compatibility and About](#compatibility-and-about)
- [Settings gear](#settings-gear)
- [Account](#account)
- [Lock and certificate panel](#lock-and-certificate-panel)
- [Extra sensors](#extra-sensors)
- [On a phone](#on-a-phone)

## Reaching it

`http://127.0.0.1:8010` (scope `local`) or `https://n5host:8010` (`lan`), as chosen in
[Setup](03-setup.md). With `auth = "basic"` the page has two faces: an anonymous visitor
gets the reduced Overview and the About tab, everything else appears after *Sign in*.
The server enforces this, the UI only mirrors it — the full table is in
[Who sees what](08-https-security.md#who-sees-what).

![Sign-in dialog with the Remember me option](screenshots/02-login-dialog.png)

## Header and tab bar

Brand, profile title and the *verified on hardware* badge on the left; on the right the
daemon status chip (`ok`, `sensor-error`, `write-error`, `dry-run`), uptime, the version
with a `beta` badge on pre-releases, the lock (transport and certificate), `live`/`paused`
(paused while the tab is hidden), the signed-in user, *Sign in* or *Sign out*, and the
settings gear. Below it the tab bar — what a tab needs and cannot get answers `401` on
the server, so the tabs that need a login are hidden until you have one.

![Header: status chip, uptime, version with beta badge, lock, live, user, gear](screenshots/05-header.png)

A banner *Connection to the daemon lost* appears after two failed polls; the fans stay
on the daemon side (failsafe on exit), see [Troubleshooting](10-troubleshooting.md).

![Connection-lost banner above the Overview](screenshots/23-connection-lost.png)

## Overview

The landing page. Anonymous visitors see the channel cards — temperature coloured
relative to the channel's critical value, the duty bar with the target marker (the slew
is still moving there), the mode badge (`AUTO`, `MANUAL`, `CRITICAL`, `STALL`,
`SENSOR-ERROR`, `FAILSAFE`), RPM — and the two charts of the last two hours (temperature;
fan speed with an RPM/duty toggle).

![Overview as an anonymous visitor: channel cards and the two charts](screenshots/01-overview-anonymous.png)

Signed in, the page adds the *Sensors* card (every readable temperature grouped
CPU / SSD / HDD / GPU / NIC / EC / other, each with a *chart* toggle, see
[Extra sensors](#extra-sensors)), the *Extra sensors* chart once something is toggled,
the *System* card (the short form of the [System](#system) tab) and *Recent alerts*.

![Overview signed in: channel cards, charts](screenshots/03-overview-signed-in-top.png)
![Overview signed in, lower half: extra sensors chart, Sensors card, System card, recent alerts](screenshots/04-overview-signed-in-bottom.png)

## Curves

One editor per channel. Terms used everywhere in the UI:

- **duty** — the PWM value 0..255 the daemon writes; the bar shows it as a percentage.
- **critical** — the temperature at which the channel goes to 255 at once, also under a
  manual override.
- **stop** — what the channel gets when the daemon stops: `auto` hands it back to the
  chip's own regulation, a number is a fixed duty (never below 60; the N5 Pro HDD
  channel is always fixed, because the EC no longer regulates it after a write — see
  [Kernel driver](02-kernel-driver.md#why-an-out-of-tree-module)).
- **stall** — the guard that raises a channel to 255 when the fan reports 0 RPM at a
  duty that should turn it.

Drag the points on the canvas or edit the table; *+ add point* inserts a point at the
middle of the widest temperature gap and keeps the table sorted; editing a temperature
re-sorts the rows when the field loses focus. The dashed line is `crit`, the dotted one
the live reading (`now`), and on the N5 Pro the measured duty→RPM pairs sit under the
table.

![Curves editor: canvas, point table, add point, crit line, now marker, duty to RPM reference](screenshots/07-curves.png)

The editor refuses what the daemon would replace by a default — 2..8 points,
temperatures ascending, duties not descending, critical above the last point, stop
`auto` or 60..255 ([curve rules](06-configuration.md#curve-rules)) — before it sends
anything. *Apply to daemon* rewrites the `[[channel]]` tables of the config file and
reloads without a restart, *Revert* reloads the daemon's curves. A changed channel set
answers *restart required* (the notice tells you the command). Unsaved edits are marked;
the editor keeps them across a session expiry until you sign in again.

![Curves editor with a validation error: red notice, nothing sent](screenshots/08-curves-error.png)
![Curves editor after Apply: restart required notice](screenshots/09-curves-restart-required.png)

## Manual

Holds a fixed duty per channel until *Back to auto*; critical and stall still apply on
top. HDD-like channels (fixed stop, N5 Pro pwm3) refuse values below 60 — the EC stops
regulating them after the first write and a low manual duty would be permanent for disks
whose temperature reacts minutes later. The same from the shell: `n5-fangov set` /
`auto` ([CLI](05-cli.md)).

![Manual tab: sliders per channel, minimum-60 hint on the HDD channel](screenshots/10-manual.png)

## Presets

Lists the built-in N5 Pro sets (badge *built-in*, the recommended one marked) and your
own files from `/etc/n5-fangov/presets/`. *Apply* replaces the channel set of the config
file and reloads; *Details* shows the preset's channel tables (sensor, curve points,
critical, stop); *Rename* and *Delete* work on user presets only; *Save current as…*
stores the curves the daemon runs now (not the unsaved editor state). The values of the
three built-in sets and the file format: [Presets](06-configuration.md#presets).

![Presets tab: built-in and recommended badges, Details with the channel tables](screenshots/11-presets.png)

## Alerts

Shows the configured and the effective transport, which tools the box has, the PVE
template state with *Install / Update template*, the cooldown, every alert kind with its
last delivery and the recent alerts; *Send test alert* goes through the real transport
and reports a delivery error. What each button does and the PVE side:
[Alerts](07-alerts.md).

![Alerts tab: transport form, template card, kinds table, recent alerts](screenshots/12-alerts.png)
![Toast after Send test alert](screenshots/13-alerts-test-toast.png)

## System

The hardware inventory in full: host, machine/board/BIOS, CPU (model, cores/threads, top
clock), fan controller (profile, hwmon path, driver module), memory with the installed
modules, GPU · NPU (with driver version), network (physical NICs, link state, MACs, MTU),
storage, load and uptime. The signed-in Overview carries the same as a card at a glance;
`n5-fangov system` prints it from the shell (`--json` for the document; without a
running daemon it collects locally). `GET /api/system` is protected like every other
endpoint — the inventory names the operator's hardware.

![System tab, top: Host, Machine, CPU, Fan controller, memory modules](screenshots/18a-system.png)
![System tab, scrolled: GPU and NPU, Network, Storage](screenshots/18b-system-scrolled.png)

Everything is read from files the daemon can reach inside its sandbox: `/sys/class/dmi/id`
(machine), `/proc/cpuinfo` and cpufreq (CPU), `/proc/meminfo` (memory), `/sys/bus/pci/devices`
with the driver links (GPU, NPU, NICs, storage controllers), `/sys/class/accel` and
`/sys/class/drm`, `/sys/class/net` (interfaces with a device link — bridges, veth, tap and
`lo` are skipped), `/sys/block` (zvols, loop, dm and ram devices skipped), `/sys/module`
(driver versions), `/etc/os-release`, `/proc/uptime`, `/proc/loadavg`. The **memory
modules** (size, type, speed, manufacturer, part number, ECC) come from the SMBIOS
structure table the kernel exports as `/sys/firmware/dmi/tables/DMI` — parsed by the
daemon itself, no `dmidecode` and no `/dev/mem` needed. PCI device **names** need `lspci`
(package `pciutils`, present on Proxmox VE); without it the entries carry
`PCI device <vendor>:<device>` ids and the tab shows a note. The static parts are cached
for 10 minutes, memory usage, load, uptime and NIC link state are read on every request
(the tab refreshes every 30 s). No serial numbers are read.

![System tab with a source notice: lspci missing](screenshots/19-system-error.png)

## Log

The daemon's log file (journal when no file is configured) with a text filter,
auto-scroll, *Refresh*, *Export* (whole current file) and *Clear* (with confirmation;
rotated files and the journal stay). File, rotation and the CLI form:
[Logs](10-troubleshooting.md#logs).

![Log tab: filter, export, clear](screenshots/20-log.png)

## Compatibility and About

**Compatibility** lists the profiles with the active one and the verified/untested
badges. **About** (public) carries name, version with the pre-release tag, licence,
repository and author links, the Go version the binary was built with, and the credits:
[`ltdstudio/minisforum-n5-it5571`](https://github.com/ltdstudio/minisforum-n5-it5571)
(the kernel driver for the IT5571 EC) and
[`Sl0thC0der/proxfansx`](https://github.com/Sl0thC0der/proxfansx) (the dashboard idea
and the generic NCT67xx/IT87xx handling the `nct67xx`/`it87xx` profiles follow; no code
shared). The releases page linked there is maintained with the release workflow
([Development](11-development.md#release-workflow)).

![Compatibility tab: ACTIVE, verified and untested badges](screenshots/21-compatibility.png)
![About tab: licence, links, credits](screenshots/22-about.png)

## Settings gear

Signed in: temperature unit °C/°F, refresh interval 5/10/30 s, theme dark/light/system
(stored in this browser), *Export settings* / *Import settings…* (config plus presets as
one JSON, see [Backup and restore](09-updates.md#backup-and-restore)), *Certificate…*
and *Account…*.

![Settings popover: unit, interval, theme, export and import, Certificate, Account](screenshots/06-settings-popover.png)
![Overview in the light theme](screenshots/24-overview-light.png)

## Account

*Account…* opens the dialog with *Change password…*, *Change user…* and the list of
active sessions (id, created, last seen, expiry, IP, remember) with *Sign out other
sessions*. The current one is marked.

![Account dialog: change password form and the sessions table](screenshots/17-account.png)

- **Change password… / Change user…** ask for the current password, write the new
  `password_hash` (or `user`) into the config file in place — comments and every other
  key untouched — apply it at once and sign every *other* session out. User names are
  `[A-Za-z0-9_.-]{1,32}`, passwords 8..128 characters. A wrong current password counts as
  a failed login for the [rate limiter](08-https-security.md#login-throttling).
- The in-place edit handles the `[web]` header and the dotted `web.user = …` layout; an
  inline table `web = { … }` is refused with a message (edit the file by hand) — the same
  applies to `[alert]` and `[dashboard]`.
- `n5-fangov passwd` still works from the shell (restart to apply) — for a forgotten
  password, for instance ([Setup](03-setup.md#change-user-or-password-later)).
- With `auth = "none"` every visitor counts as signed in; the account forms answer
  `409 auth is none`.

Session lifetime, persistence and what *Sign out* can and cannot do:
[Sessions](08-https-security.md#sessions).

## Lock and certificate panel

`🔒 TLS` or `🔓 HTTP` in the header; the tooltip carries certificate mode and expiry,
the click (or *Settings → Certificate…*) opens the certificate panel: subject, issuer,
SANs, validity, key type, fingerprint with a copy button, download `.crt`/`.cer`,
regenerate, upload your own pair, back to auto, and the *How to trust this certificate*
recipes. What each action does: [HTTPS](08-https-security.md#the-certificate).

![Certificate panel, automatic mode: fingerprint, SAN chips, downloads, How to trust opened](screenshots/14-certificate-auto.png)
![Certificate upload with the install anyway checkbox and the HSTS warning](screenshots/15-certificate-upload.png)
![Certificate panel in fallback mode: warn badge automatic (fallback)](screenshots/16a-certificate-fallback.png)
![Certificate panel with an expiry warning](screenshots/16b-certificate-soon.png)
![Certificate panel with TLS off: loopback explanation](screenshots/16c-certificate-off.png)

## Extra sensors

The signed-in Overview lists every readable temperature (`GET /api/sensors`, grouped
CPU / SSD / HDD / GPU / NIC / EC / other); the *chart* toggle per row adds or removes the
sensor id in `[dashboard] sensors` (`PUT /api/dashboard`, 0..8 ids, same forms as channel
sensors — [Configuration](06-configuration.md#configuration-reference)). Watched sensors
are read once per cycle after the channel sensors, recorded in the history
(`history[].extra`) and drawn in the *Extra sensors* chart card; they never influence
regulation. An id whose device is absent right now is kept with a warning and charted
once it appears; an id that is not a known form is refused. The list applies without a
restart, also when edited in the config file.

## On a phone

The cards stack, the header is one line and the tab bar stays sticky; the curve points
can be dragged by touch.

![Mobile Overview, anonymous](screenshots/25a-mobile-overview-anonymous.png)
![Mobile Overview, signed in](screenshots/25b-mobile-overview-signed-in.png)
![Mobile Curves editor with touch drag](screenshots/26-mobile-curves.png)

Next: [CLI reference](05-cli.md) · [Configuration](06-configuration.md) ·
[HTTPS and security](08-https-security.md)
