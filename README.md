# n5-fangov — guarded fan control for Proxmox VE and Debian

n5-fangov is a fan governor in one static binary — regulation daemon, CLI and embedded web
dashboard. It takes over the PWM channels a kernel hwmon driver exposes and adds
multi-sensor curves, guards (critical temperature, stall, sensor loss, write errors,
crash, kernel update without the driver) and alerts through the Proxmox notification
stack. **Hardware-verified on the Minisforum N5 Pro** (ITE IT5571 embedded controller via
the community driver [`ltdstudio/minisforum-n5-it5571`](https://github.com/ltdstudio/minisforum-n5-it5571));
generic profiles for Nuvoton NCT67xx and ITE IT87xx ship as *from documentation, untested*
and the dashboard says so per profile.

> Status: **pre-release.** `n5-fangov version` prints the build; the dashboard header shows
> a `beta` badge until a release tag drops the suffix (`GET /api/version` and `/api/about`
> carry it as `prerelease`). Changes per version: [CHANGELOG.md](CHANGELOG.md). Validation
> data, the Bash predecessor `n5-fand` and the measurement scripts live in
> [`minisforum-n5pro-fan-proxmox`](https://github.com/SirRenix/minisforum-n5pro-fan-proxmox).

Contents: [Why](#why) · [What it guards against](#what-it-guards-against) ·
[Prerequisites](#prerequisites) · [Install](#install) · [Setup](#setup) ·
[Dashboard](#dashboard) · [CLI reference](#cli-reference) ·
[Configuration reference](#configuration-reference) · [Dashboard access](#dashboard-access) ·
[HTTPS](#https) · [Security](#security) · [Presets](#presets) ·
[Extra sensors](#extra-sensors-on-the-dashboard) · [Alerts](#alerts) · [Logs](#logs) ·
[Backup / Restore](#backup--restore) · [Updates](#updates) · [Uninstall](#uninstall) ·
[Troubleshooting](#troubleshooting) · [Hardening](#hardening) ·
[Compatibility](#compatibility) · [Build](#build) · [About and license](#about-and-license)

## Why

- The N5 Pro exposes no fan control to Linux at all; the BIOS "silent" curve lets the
  drives sit at 40–42 °C and offers only fixed PWM.
- `fancontrol` regulates one channel from one sensor. The HDD group needs the hottest
  of four drives, the SSD fan the hottest of three NVMe.
- The IT5571 EC **does not resume automatic regulation of the HDD channel after any
  write** (measured 2026-09-14). A controller that hands control back to the EC on exit
  leaves the drives unregulated. n5-fangov knows that and stops to a fixed safe duty instead.

## What it guards against

| Failure | Response |
|---|---|
| Controller hangs | systemd watchdog (60 s) → kill → failsafe → restart |
| Controller dies (crash, OOM, kill) | `ExecStopPost=n5-fangov failsafe` → profile-defined safe state |
| Kernel update without the DKMS module | apt hook `check --after-update` alerts before the reboot; `ExecStartPre=n5-fangov check` fails loudly after it; fans stay in EC/BIOS mode |
| Sensor unreadable, implausible, frozen or absent | that channel at its safe duty (fixed stop duty, else 255), alert naming it, re-resolve; the other channels keep regulating |
| Fan stalls (0 RPM at duty ≥ threshold) | channel 255, alert, auto-recovery |
| Write fails or read-back differs | 255, alert |
| Somebody else writes to `/sys` | set-point re-asserted every minute, warning |
| Broken config | built-in defaults + warning + alert; daemon still starts |
| Critical temperature | 255 immediately, also in manual mode |
| Fan controller vanishes (driver reload) | daemon exits after 6 failed cycles, systemd restarts it with a fresh detection |

Alerts go to the Proxmox notification stack (`PVE::Notify`, template `n5-fangov`) when
running on PVE, otherwise `mail(1)`; always to the journal. The transport is configurable
and testable from the dashboard (see [Alerts](#alerts)).

## Prerequisites

| | |
|---|---|
| OS | Proxmox VE 9.x (Debian 13 "trixie") or Debian 13 with systemd; tested platform in [Compatibility](#compatibility). Everything runs as root. |
| Packages | `dkms`, the kernel headers for the running kernel (`proxmox-headers-$(uname -r)` on PVE, `linux-headers-$(uname -r)` on Debian), `pciutils` (`lspci`, for device names in the System tab; optional), `lm-sensors` (optional, `sensors` for cross-checks). |
| **N5 Pro: the EC driver** | The fan outputs of the Minisforum N5 Pro are exposed by the out-of-tree kernel module `minisforum_n5_it5571` (upstream [`ltdstudio/minisforum-n5-it5571`](https://github.com/ltdstudio/minisforum-n5-it5571)). The sibling repo [`SirRenix/minisforum-n5pro-fan-proxmox`](https://github.com/SirRenix/minisforum-n5pro-fan-proxmox) packages it as DKMS `minisforum-n5-it5571/0.2.0` (rebuilt automatically for every new kernel) and ships the two autoload files. Its `deploy/install.sh` installs the module **and** the older Bash regulator `n5-fand`; n5-fangov's installer disables `n5-fand` again, so running both installers in this order is fine. |
| N5 Pro: driver options | `/etc/modprobe.d/minisforum-n5-it5571.conf` must contain `options minisforum_n5_it5571 experimental_write=1` — without it the `pwm*` nodes stay invisible and `setup` finds no profile. Loading the module writes nothing to the EC (`pwm*_enable` starts at 2 = EC automatic); only the regulator writes. `/etc/modules-load.d/minisforum-n5-it5571.conf` loads it at boot. |
| Other boards | `nct6775` / `it87` from the distribution kernel; no extra package. |
| Check | `n5-fangov detect` (after the install below) lists the hwmon devices and the profile it would use; `dkms status minisforum-n5-it5571` must show `installed` for the running kernel. |

## Install

Two ways; both end with `n5-fangov setup`.

**From a GitHub release** (no Go toolchain needed). The release carries the static
`linux/amd64` binary and its sha256; the units, scripts and templates come from the
repository at the same tag:

```
# as root; VER = the tag you want without the leading v, see the releases page
VER=X.Y.Z
git clone --branch v$VER --depth 1 https://github.com/SirRenix/n5-fangov.git
cd n5-fangov && mkdir -p dist
curl -fsSL -o dist/n5-fangov        https://github.com/SirRenix/n5-fangov/releases/download/v$VER/n5-fangov-$VER-linux-amd64
curl -fsSL -o dist/n5-fangov.sha256 https://github.com/SirRenix/n5-fangov/releases/download/v$VER/n5-fangov-$VER-linux-amd64.sha256
(cd dist && sed "s/n5-fangov-$VER-linux-amd64/n5-fangov/" n5-fangov.sha256 | sha256sum -c -)
chmod 0755 dist/n5-fangov
./deploy/install.sh
n5-fangov setup
```

**From a checkout with a build** (`make build` needs the Go version pinned in `go.mod`, or
`tools/remote-go.ps1 -Fetch` builds in Docker on another machine):

```
make build && ./deploy/install.sh        # or: make deb && apt install ./dist/n5-fangov_<version>_amd64.deb
n5-fangov setup
```

The installer puts the binary, the units, the apt hook, the log directory and (on PVE)
the notification template pair in place and enables the unit. It writes **no** config and
starts nothing — that is `setup`. `install.sh` looks for the binary at `dist/n5-fangov`
(or `./n5-fangov`) and refuses to run without one. N5 Pro: the kernel module from the
prerequisites must be loaded first; `setup` refuses without a detected profile. The file
layout is in [deploy/README-DEPLOY.md](deploy/README-DEPLOY.md).

Updating: same steps with the new tag; `install.sh` replaces binary and units and keeps
the config. Rollback and removal: [Updates](#updates), [Uninstall](#uninstall).

## Setup

`n5-fangov setup` writes `/etc/n5-fangov/config.toml` for this machine:

1. **Profile**: detected on the box (`n5pro` → the verified three-channel set;
   `nct67xx`/`it87xx` → one conservative channel per PWM output, sensor `k10temp` /
   `coretemp` / first hwmon temperature; `monitor` → no channels).
2. **Scope** of the web UI:
   - `local` — `127.0.0.1:8010`, no auth, plain HTTP. Reach it with `ssh -L 8010:127.0.0.1:8010 <host>`
     or put a reverse proxy in front.
   - `lan` — the primary LAN address, **basic auth** (user + password asked twice
     without echo) and **HTTPS** with an automatically created self-signed certificate.
   - `HOST:PORT` — explicit address; non-loopback implies auth + HTTPS like `lan`.
3. An existing config is backed up (`config.toml.bak-<timestamp>`) before it is replaced.

Non-interactive: `n5-fangov setup --yes --listen lan --user admin --password-file /root/pw`
(all needed flags must be present). The password comes from `--password-file F` (first
line, mode 0600 recommended) or `--password -` (one line on stdin) and must have 8..128
characters; there is no literal form, so it never lands in `ps` or the shell history.
Afterwards:

```
n5-fangov check                  # what serve will do with this config
systemctl enable --now n5-fangov
n5-fangov status
```

Change the user or password later in the dashboard (settings gear → *Account…*, takes
effect at once) or with `n5-fangov passwd` (edits the file in place, restart to apply).
The reference with every key explained is `/usr/share/doc/n5-fangov/config.example.toml`;
the table is in [Configuration reference](#configuration-reference).

## Dashboard

`http://127.0.0.1:8010` (`local`) or `https://<host>:8010` (`lan`). The screenshots are
from the built-in mock (`?mock=1`, example values); the full set is in
[docs/screenshots/](docs/screenshots/README.md).

**Header.** Brand, profile title and the *verified on hardware* badge on the left; on the
right the daemon status chip (`ok`, `sensor-error`, `write-error`, `dry-run`), uptime, the
version with a `beta` badge on pre-releases, the lock (transport and certificate, see
below), `live`/`paused` (paused while the tab is hidden), the signed-in user, *Sign in* or
*Sign out*, and the settings gear. Below it the tab bar — what a tab needs and cannot get
answers `401` on the server, so the tabs that need a login are hidden until you have one.

![header](docs/screenshots/05-header.png)

**Overview** is the landing page. Anonymous visitors (with `auth = "basic"`) see the
channel cards — temperature coloured relative to the channel's critical value, the duty
bar with the target marker (the slew is still moving there), the mode badge (`AUTO`,
`MANUAL`, `CRITICAL`, `STALL`, `SENSOR-ERROR`, `FAILSAFE`), RPM — and the two charts of
the last two hours (temperature; fan speed with an RPM/duty toggle). Signed in, the page
adds the *Sensors* card (every readable temperature grouped CPU / SSD / HDD / GPU / NIC /
EC / other, each with a *chart* toggle that records it in the history, up to eight), the
*Extra sensors* chart once something is toggled, the *System* card (machine, CPU, RAM,
GPU, NPU, NICs, disks, OS, fan-controller module — the short form of the System tab) and
*Recent alerts*.

![overview anonymous](docs/screenshots/01-overview-anonymous.png)
![overview signed in](docs/screenshots/03-overview-signed-in-top.png)
![overview signed in, lower half](docs/screenshots/04-overview-signed-in-bottom.png)

**Curves** is one editor per channel. Terms: **duty** is the PWM value 0..255 the daemon
writes (the bar shows it as a percentage); **critical** is the temperature at which the
channel goes to 255 at once, also under a manual override; **stop** is what the channel
gets when the daemon stops — `auto` hands it back to the chip's own regulation, a number
is a fixed duty (never below 60; the N5 Pro HDD channel is always fixed, because the EC no
longer regulates it after a write); **stall** is the guard that raises a channel to 255
when the fan reports 0 RPM at a duty that should turn it. Drag the points on the canvas
or edit the table; *+ add point* inserts a point at the middle of the widest temperature
gap and keeps the table sorted; editing a temperature re-sorts the rows when the field
loses focus. The dashed line is `crit`, the dotted one the live reading (`now`), and on
the N5 Pro the measured duty→RPM pairs sit under the table. The editor refuses what the
daemon would replace by a default (2..8 points, temperatures ascending, duties not
descending, critical above the last point, stop `auto` or 60..255) before it sends
anything; *Apply to daemon* rewrites the `[[channel]]` tables of the config file and
reloads without a restart, *Revert* reloads the daemon's curves. A changed channel set
answers *restart required* (the notice tells you the command). Unsaved edits are marked;
the editor keeps them across a session expiry until you sign in again.

![curves](docs/screenshots/07-curves.png)
![curves, validation error](docs/screenshots/08-curves-error.png)

**Manual** holds a fixed duty per channel until *Back to auto*; critical and stall still
apply on top. HDD-like channels (fixed stop, N5 Pro pwm3) refuse values below 60 — the
EC stops regulating them after the first write and a low manual duty would be permanent
for disks whose temperature reacts minutes later.

![manual](docs/screenshots/10-manual.png)

**Presets** lists the built-in N5 Pro sets (badge *built-in*, the recommended one marked)
and your own files from `/etc/n5-fangov/presets/`. *Apply* replaces the channel set of the
config file and reloads; *Details* shows the preset's channel tables (sensor, curve
points, critical, stop); *Rename* and *Delete* work on user presets only; *Save current
as…* stores the curves the daemon runs now (not the unsaved editor state). Values in
[Presets](#presets).

![presets](docs/screenshots/11-presets.png)

**Alerts** shows the configured and the effective transport, which tools the box has, the
PVE template state with *Install / Update template*, the cooldown, every alert kind with
its last delivery and the recent alerts; *Send test alert* goes through the real
transport and reports a delivery error. Details in [Alerts](#alerts).

![alerts](docs/screenshots/12-alerts.png)

**System** is the hardware inventory in full: host, machine/board/BIOS, CPU, fan
controller, memory with the installed modules (from the SMBIOS table, no `dmidecode`),
GPU · NPU, network, storage. A notice lists sources that could not be read (typically
`lspci` missing → `apt install pciutils`). See [System](#system).

![system](docs/screenshots/18a-system.png)

**Log** is the daemon's log file (journal when no file is configured) with a text filter,
auto-scroll, *Refresh*, *Export* (whole current file) and *Clear* (with confirmation;
rotated files and the journal stay). **Compatibility** lists the profiles with the active
one and the verified/untested badges. **About** (public) carries name, version, licence,
repository, author and credits.

![log](docs/screenshots/20-log.png)

**Settings gear** (signed in): temperature unit °C/°F, refresh interval 5/10/30 s, theme
dark/light/system (stored in this browser), *Export settings* / *Import settings…* (config
plus presets as one JSON, see [Backup / Restore](#backup--restore)), *Certificate…* and
*Account…* (change password, change user, session list, *Sign out other sessions*).

![settings](docs/screenshots/06-settings-popover.png)
![account](docs/screenshots/17-account.png)

**Lock** in the header: `🔒 TLS` or `🔓 HTTP`; the tooltip carries certificate mode and
expiry, the click opens the certificate panel — download `.crt`/`.cer`, fingerprint,
regenerate, upload your own pair, back to auto, and the "How to trust this certificate"
recipes. Details in [HTTPS](#https).

![certificate](docs/screenshots/14-certificate-auto.png)

On a phone the cards stack, the header is one line and the tab bar stays sticky; the
curve points can be dragged by touch.

## CLI reference

`n5-fangov <subcommand> [flags]`. Every subcommand accepts `--config PATH` (default
`/etc/n5-fangov/config.toml`) where a config is involved. Commands marked *socket* talk
to the running daemon over `/run/n5-fangov/n5-fangov.sock` (root only) and fall back to
the files or `state.json` when it is down.

| Subcommand | Flags | Does |
|---|---|---|
| `setup` | `--listen local\|lan\|HOST:PORT`, `--user U`, `--password-file F` \| `--password -`, `--profile auto\|n5pro\|nct67xx\|it87xx\|monitor`, `--yes` | write the config for this machine (interactive when flags are missing; `--yes` asks nothing and needs every flag; existing file backed up) |
| `serve` | `--dry-run`, `--run-dir DIR`, `--state-dir DIR`, `--listen ADDR` (`none` = no TCP listener) | run the daemon (the unit's `ExecStart`); `--dry-run` reads sensors and logs decisions without writing pwm |
| `status` | — | channels, temperatures, duty, RPM, mode (*socket*, else `state.json`) |
| `set <ch> <duty\|NN%>` | — | manual override for one channel (*socket*; limits and stall guard still apply) |
| `auto <ch\|all>` | — | back to the curve (*socket*) |
| `curve` | — | print the configured curves |
| `log` | `-n N` (default 50), `--export FILE` (`-` = stdout), `--clear` | log file; journal when no file is configured (`--clear` then impossible) |
| `check` | `--quiet` (failures only), `--after-update` | self-check (`ExecStartPre`): config, profile, pwm writable, sensors, tls, log, dkms; `--after-update` is the apt hook's kernel gate |
| `detect` | — | list profiles with their detection result (read-only) |
| `system` | `--json` | hardware inventory (*socket*, else local collection) |
| `test <ch>` | `--force`, `--hold D`, `--sample D` (fixtures only) | channel verification run: writes duty steps and samples the tach; refuses while the daemon is running unless `--force`; restores the channel's stop value |
| `failsafe` | — | put every configured channel into its safe state (`ExecStopPost`; works without the daemon) |
| `passwd` | `--user U`, `--password-file F` \| `--password -` | set the web user/password (`auth = basic`), restart to apply; the dashboard changes both live |
| `cert` | `info` \| `export [--der] [FILE]` \| `regen [--new-key]` \| `upload CERT KEY` \| `reset` | dashboard certificate (*socket* = hot swap, else on the files plus a restart hint) |
| `alerts` | `status` \| `test` \| `template` | alert transport, test alert, PVE notification template (*socket*, else on the files) |
| `export [FILE]` | `--presets DIR` | settings bundle (config + presets, hash redacted) as JSON; `-`/no file = stdout |
| `import FILE` | `--presets DIR` | restore a bundle (everything validated first), reload the daemon |
| `version` | (also `-v`, `--version`) | print the version |
| `help` | (also `-h`, `--help`) | usage |

Exit codes: `0` ok, `1` failure (`check`: serve could not run with this config; `test`:
channel not verified), `2` usage error (unknown subcommand, bad flag). Environment:
`N5FANGOV_RUN_DIR` (default `/run/n5-fangov`: socket, `state.json`, override and alert
stamps), `N5FANGOV_STATE_DIR` (default `$STATE_DIRECTORY` from systemd, else
`/var/lib/n5-fangov`: `sessions.json`, `alerts.json`), `N5FANGOV_SYSFS` (default `/sys`;
tests point it at `testdata/sysfs/n5pro`). Relative paths are taken from the working
directory — under the unit that is `/`.

## Configuration reference

`/etc/n5-fangov/config.toml`. Invalid values never prevent a start: each one falls back
to its default with a warning in the journal and a `config` alert (`n5-fangov check`
prints them). *Reload* = applied by `PUT /api/config`, the curve editor, a preset apply
or an import without a restart; *restart* = read once at start (the account and
certificate panels edit the file and apply their keys live).

| Key | Range | Default | Applies |
|---|---|---|---|
| `[daemon] interval` | `2s`..`30s` (above 30 s clamped: the unit's `WatchdogSec=60` needs two cycles) | `10s` | reload |
| `step_up` / `step_down` | 1..255 duty per cycle | 40 / 15 | reload |
| `stall_min_duty` | 1..255 — 0 RPM at or above this duty counts as a stall | 60 | reload |
| `stall_cycles` | 1..20 consecutive cycles | 2 | reload |
| `stale_cycles` | 6..600 — first channel's sensor bit-identical this many cycles = frozen (checked on `k10temp` only) | 18 | reload |
| `alert_cooldown` | `60s`..`24h` per alert kind | `30m` | reload |
| `log_every` | status line every N cycles, 0 = never | 30 | reload |
| `profile` | `auto` \| `n5pro` \| `nct67xx` \| `it87xx` \| `monitor` | `auto` | restart |
| `[web] listen` | `host:port` | `127.0.0.1:8010` | restart |
| `auth` | `none` \| `basic` (needs `user` + `password_hash`, otherwise `none` **and** loopback) | `none` | restart |
| `user` | web user (`[A-Za-z0-9_.-]{1,32}` when set through the API) | `""` | restart / live via Account |
| `password_hash` | `pbkdf2$<iter>$<salt>$<key>` from `passwd`/`setup`; legacy 64-hex sha256 of `user:password` still accepted | `""` | restart / live via Account |
| `allowed_hosts` | extra `Host` header values (reverse-proxy names); `"*"` disables the check | `[]` | restart |
| `tls` | `auto` \| `off` \| `file`; `off` on a non-loopback listen is replaced by `auto` | `off` loopback / `auto` else | restart / live via the certificate panel |
| `cert_file`, `key_file` | PEM paths for `tls = "file"` | `""` | restart / live via the certificate panel |
| `behind_tls_proxy` | `true` when a reverse proxy terminates TLS in front of a plain listener: the session cookie gets `Secure` | `false` | restart |
| `[log] file` | absolute clean path under `/var/log/`, `""` = journal only; not a symlink, device or directory | `/var/log/n5-fangov/n5-fangov.log` | restart |
| `max_size_mb` | 1..100 | 5 | restart |
| `max_files` | 1..20 rotated files `.1`..`.N` | 5 | restart |
| `[alert] transport` | `auto` \| `pve` \| `mail` \| `log` \| `off` | `auto` | reload |
| `mail_to` | local user or address, no spaces or quotes, never starts with `-` | `root` | reload |
| `[dashboard] sensors` | 0..8 sensor ids charted on the Overview | `[]` | reload |
| `[[channel]] name` | unique, `[a-z0-9_]+` | — | restart when the set changes |
| `pwm` | 1..8, `pwmN` of the profile's hwmon device, unique | — | restart when the set changes |
| `sensor` | `k10temp` \| `coretemp` \| `nvme:max` \| `drivetemp:max` \| `hwmon:<name>:tempN` \| `ec:<label>` | — | reload |
| `curve` | 2..8 `[temp_c, duty]` points, temps −20..120 ascending, duties 0..255 not descending; else `[[45,85],[80,255]]` | — | reload |
| `critical` | last curve temperature + 1 .. 150 → 255 immediately | last + 10 | reload |
| `stop` | `"auto"` (back to the chip) or a fixed duty 60..255 (`drivetemp:max` channels default to `140`; N5 Pro pwm3 never `auto`) | by sensor | reload |

Example channel:

```toml
[[channel]]
name = "hdd"
pwm = 3
sensor = "drivetemp:max"
curve = [[36,105],[46,255]]
critical = 56
stop = 140            # fixed stop duty: the EC won't regulate this channel after a write
```

## System

The signed-in Overview carries a *System* card with the box at a glance — product, board
and BIOS, CPU model with cores/threads and top clock, RAM used/total and installed modules,
GPU, NPU with driver version, physical NICs with link state, disk count and capacity,
OS/kernel, fan-controller profile, hwmon path and driver module. The *System* tab shows
the full tables (memory modules, PCI addresses, drivers, MACs, MTU, load, uptime), and
`n5-fangov system` prints the same from the shell (`--json` for the document; without a
running daemon it collects locally). `GET /api/system` is protected like every other
endpoint — the inventory names the operator's hardware.

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

## Dashboard access

With `auth = "basic"` the dashboard has two faces, enforced by the server (the UI only
mirrors it):

| | Anonymous | Signed in |
|---|---|---|
| Overview | channel cards and the two charts (`GET /api/state` and `/api/history` in a **reduced** form: name, pwm, sensor, temp, duty, target, rpm, mode — no hwmon path, no EC temperatures, no alert stamps, no extra sensors) | full: plus the Sensors card, the extra-sensor chart, System details, recent alerts |
| About tab, version | full | full |
| Curves, Manual, Presets, Alerts, System, Log, Compatibility, certificate panel, settings gear | hidden; the API answers 401 | full |

Nothing pops up for an anonymous visitor: the reduced Overview is the landing page, the
**Sign in** button in the header opens the form. Basic auth stays accepted on every
protected request (CLI, curl, scripts), the cookie session is for browsers:

- **Remember me** keeps the session for 30 days on that browser, otherwise 12 hours.
  Sessions survive a daemon restart — including the restart a config change may
  require — because they are mirrored to `/var/lib/n5-fangov/sessions.json` (0600, tokens
  stored hashed; at most 50, oldest dropped).
- **Sign out** revokes the session and clears the cookie. The Account dialog lists the
  active sessions (id, created, last seen, expiry, IP, remember) and offers *Sign out other
  sessions*. A browser that still holds Basic credentials sends them with every request
  and counts as signed in ("via basic"); *Sign out* cannot clear those — close the browser
  or clear the site data.
- A password or user change made outside the dashboard (`n5-fangov passwd`, editing the
  file) drops every persisted session at the next start; changes made through the
  Account dialog keep the session that made them.
- **Change password… / Change user…** ask for the current password, write the new
  `password_hash` (or `user`) into the config file in place — comments and every other
  key untouched — apply it at once and sign every *other* session out. User names are
  `[A-Za-z0-9_.-]{1,32}`, passwords 8..128 characters. A wrong current password counts as
  a failed login for the rate limiter. The in-place edit handles the `[web]` header and
  the dotted `web.user = …` layout; an inline table `web = { … }` is refused with a
  message (edit the file by hand) — the same applies to `[alert]` and `[dashboard]`.
  `n5-fangov passwd` still works from the shell (restart to apply) — for a forgotten
  password, for instance.
- With `auth = "none"` every visitor counts as signed in; the account forms answer
  `409 auth is none`.

The cookie is `HttpOnly; SameSite=Strict` (`Secure` over TLS or with
`behind_tls_proxy = true`); state-changing requests additionally need the
`X-N5-Fangov-Csrf: 1` header the UI always sends, which is the CSRF defence for the cookie
session. Failed logins throttle exactly like failed basic auth (5 free, then 250 ms
doubling to 2 s per client IP).

## HTTPS

`[web].tls` is `auto` | `off` | `file`. The default follows the listener: `off` on
loopback, `auto` everywhere else, and **a non-loopback listener never runs plain HTTP** —
`tls = "off"` there is replaced by `auto` with a warning (basic auth would otherwise
cross the LAN in clear text).

### The dashboard flow

The lock icon in the header shows the transport; its tooltip carries the certificate
mode and expiry, and clicking it (or *Settings → Certificate…*) opens the certificate
panel: subject, issuer, SANs, validity (highlighted below 30 days), key type, SHA-256
fingerprint with a copy button, and the actions below. Making the browser warning go
away takes three steps:

1. **Download** — *Download .crt* (PEM: Firefox, macOS, Linux) or *Download .cer* (DER:
   Windows, Android). Both are the certificate only, never the key; like the rest of the
   panel they need a signed-in session (`n5-fangov cert export` writes the same file from
   the shell).
2. **Trust** — the panel's *How to trust this certificate* lists the four recipes:
   Windows: double-click the .cer → Local Machine → Trusted Root Certification
   Authorities; macOS: Keychain Access → System → Always Trust; Firefox: Settings →
   Certificates → Authorities → Import; Android: Settings → Security → Install a
   certificate → CA certificate. Linux CLI: copy the .crt to
   `/usr/local/share/ca-certificates/` and run `update-ca-certificates`.
3. **Reload** — the connection is now verified; the fingerprint in the panel is the one
   to compare against the browser's certificate viewer.

The certificate is marked as a CA (browser stores accept a self-signed anchor only in
that form) but carries **name constraints** limited to exactly its own names and
`pathlen 0`: even with the key, nothing signed by it is valid for any other host.

### Modes and actions

- **`auto`** — the daemon creates an ECDSA P-256 self-signed certificate (10 years) in
  `/etc/n5-fangov/tls/` at the first start and reuses it. SANs: the listen host (for
  `0.0.0.0`/`[::]`: the primary IPv4 and IPv6 address, i.e. the source address of the
  default route — not every interface), the host name, `localhost`, and
  `[web].allowed_hosts`. When the names change it is reissued **with the same key**, so a
  certificate you trusted stays trusted (log: `certificate regenerated (SANs changed),
  key unchanged`).
  - **Regenerate** reissues it for the current names. The private key is kept by default;
    the checkbox *generate a new key* makes a fresh pair — every store that trusts the old
    certificate then has to import the new one, and the response says so. The same
    warning appears when the key was meant to be kept but could not be read
    (`"kept": false` in the response): a new pair was generated.
- **`file`** — your own certificate. **Upload own certificate…** takes a PEM certificate
  (chain allowed: leaf first, intermediates after it; other PEM blocks such as
  `EC PARAMETERS` are skipped) and its key (PKCS#8, PKCS#1 `RSA PRIVATE KEY` or SEC 1
  `EC PRIVATE KEY`), as files or pasted text (64 KiB max). The pair is validated first:
  PEM, key matches, not expired, a key type this server can actually sign with (ECDSA
  P-256/P-384/P-521, RSA ≥ 1024, Ed25519 — anything else is refused, as is an encrypted
  key: decrypt it with `openssl pkey -in key.pem -out key-plain.pem` first), and one test
  handshake against the listener's own TLS config, so a pair that is accepted is a pair
  the listener serves. Warnings (not errors) for a SAN list that misses a listen host, an
  expiry within 30 days, a weak key (RSA < 2048). The pair is stored as
  `/etc/n5-fangov/tls/custom-cert.pem` / `custom-key.pem` (0600), and the config is set to
  `tls = "file"` with the two paths (comments and everything else untouched). Pointing
  `cert_file`/`key_file` at files of your own by hand works the same way; they must be
  readable inside the unit's sandbox (see Hardening — `/etc/n5-fangov/` is the simple
  place). A key file readable by group or others is logged as a warning at start
  (`chmod 0600`).
  - **Lock-out guard.** The upload is refused (400) when the certificate does not cover
    the name your browser session uses (SNI, else the Host header): after the swap the
    browser would see a name mismatch and, **under HSTS, refuse the connection** — no
    warning page, no "proceed anyway", and this panel would be out of reach. The panel
    then offers an *install anyway* checkbox (`force=true` in the API); use it only when
    you can reach the dashboard by another covered name or by IP (browsers ignore HSTS
    for IP literals). Uploads through the CLI socket are not guarded.
  - **Unreadable pair at start.** When `tls = "file"` and the pair cannot be loaded or
    served (file gone, key/cert mismatch, unusable key type), the daemon does **not**
    take the dashboard down: it serves the automatic certificate instead, logs
    `FALLBACK to the automatic certificate`, sends the alert `tls` (cooled like the other
    start alerts) and reports mode `auto (fallback from file)` — the lock turns
    warn-coloured, the panel shows an *automatic (fallback)* badge, `GET /api/tls` carries
    `"fallback": true`. The config keeps `tls = "file"` and its paths. Repair from the panel
    (upload again or *Back to auto*) or with `n5-fangov cert upload CERT KEY` /
    `cert reset`, which work offline on a broken pair too (`cert info`/`cert export` need
    a loadable one).
  - **Back to auto** returns to the automatic certificate (the auto pair is kept on disk,
    so this is instant), sets `tls = "auto"` and deletes the uploaded pair; a
    `cert_file`/`key_file` of your own outside `/etc/n5-fangov/tls/` is left where it is.
  - **The daemon owns `[web] tls`, `cert_file` and `key_file` while it runs.** Every
    config write that goes through the API — curves applied from the editor, a settings
    import, a preset — gets the three keys re-applied from the certificate manager, so an
    editor that still holds the pre-upload text cannot silently revert an upload or a
    reset. Change the mode through the panel or `n5-fangov cert …`; a hand edit of the
    file takes effect at the next start (with `--listen` overriding the file, the keys are
    left as they are).
- **`off`** — plain HTTP, loopback only. A reverse proxy (Caddy, nginx, the PVE proxy)
  terminating TLS in front of `127.0.0.1:8010` is the alternative to `auto`; list its
  public name in `allowed_hosts` or let it rewrite `Host`, and set
  `behind_tls_proxy = true` so the session cookie carries `Secure`. The panel then only
  says so; the certificate endpoints answer `409 tls is off`.

Every change from the panel is **hot-swapped**: the new certificate serves the next
handshake, open connections and the fan controller are untouched, no restart. The
listener issues no TLS session tickets, so a browser that reconnects sees the new
certificate at once instead of resuming an old session. Changes are logged as
`web: tls <regenerate|upload|reset> by <ip>` and need the same login as any other write
(`auth = "basic"`). The panel's notice lists what the server finds worth knowing about
the active certificate (`warnings` in `GET /api/tls`): an expiry within 30 days, no
SANs, and listen hosts the SAN list does not cover — under HSTS the browser will refuse
such a name, so a certificate for the LAN name should carry every name you use.

### CLI equivalents

```
n5-fangov cert info                    mode, subject, SANs, validity, fingerprint
n5-fangov cert export [--der] [FILE]   certificate only, PEM (or DER with --der); "-" = stdout
n5-fangov cert regen [--new-key]       reissue; key kept unless --new-key
n5-fangov cert upload CERT KEY         install an own PEM pair (tls = "file")
n5-fangov cert reset                   back to the automatic certificate
```

With the daemon running the commands go through the unix socket and take effect at once
(same code path as the panel). Without it they work on the files and the config directly
and print the `systemctl restart n5-fangov` that applies the change.

### Transport

TLS 1.2 minimum, modern cipher suites, HSTS header (`max-age=31536000`, no
`includeSubDomains`, no preload). **HSTS scope:** browsers apply it to the whole host
*name*, all ports — after one visit to `https://n5.lan:8010` the browser also
rewrites `http://n5.lan/` (port 80) to HTTPS for a year. Browsers ignore HSTS for
IP literals, so `https://192.0.2.10:8010` affects nothing else. Reach the UI by IP, or
make sure every service on that name speaks HTTPS; a reverse proxy in front of
`tls = "off"` sets its own policy (n5-fangov sends the header only on its own TLS
listener). `listen`, `auth` and `allowed_hosts` still need a restart; the certificate
does not.

## Security

The API changes fan duties, so treat the port like a management interface.

- **Default is loopback only** (`[web].listen = "127.0.0.1:8010"`, no auth). The CLI
  uses the unix socket in `/run/n5-fangov` (root only, `RuntimeDirectoryMode=0750`).
- **LAN access means auth + TLS.** `setup --listen lan` configures both; by hand:

  ```toml
  [web]
  listen = "192.0.2.10:8010"
  auth = "basic"
  user = "admin"
  password_hash = "pbkdf2$210000$<salt hex>$<key hex>"   # written by: n5-fangov passwd
  tls = "auto"                                         # or "file" with cert_file/key_file
  allowed_hosts = ["fans.example.internal"]            # names a proxy passes in Host
  ```

- **Password hashes.** `n5-fangov passwd` and `setup` store salted PBKDF2-HMAC-SHA256
  (210 000 iterations, 16-byte random salt) as `pbkdf2$<iter>$<salt>$<key>`. The
  earlier form — the plain `sha256` hex of `user:password` (64 characters, as produced
  by `printf 'admin:password' | sha256sum`) — **keeps working**; the daemon accepts
  both. Run `n5-fangov passwd` once to upgrade an old hash (a restart applies it). A
  config file that carries a hash is written `0600`; an existing wider mode is
  tightened and logged.
- **Fail closed.** `auth = "basic"` with a missing or unusable `password_hash`, or a
  typo in `auth`, never degrades to an open LAN listener: the daemon forces
  `listen` to `127.0.0.1:8010` and logs `auth misconfigured — web bound to loopback`.
  `auth = "none"` on a non-loopback address is allowed (still HTTPS) but logged as a
  warning at every start and by `n5-fangov check`.
- **What auth covers.** With `auth = "basic"`, everything under `/api/` needs
  credentials (cookie session or Basic) except `GET /api/version`, `/api/about`,
  `/api/session`, the login/logout endpoints and the **reduced** `GET /api/state` /
  `/api/history` (channel temperatures, duties, RPM and modes — see Dashboard access).
  Config, sensors, presets, profiles, log, certificate panel and downloads, alerts,
  account, system and every write are protected. Failed logins are throttled per client
  IP (5 free, then 250 ms doubling to 2 s, reset after 10 min or a success) and logged
  with user name and IP — only when a credential was actually presented; the anonymous
  401 the UI gets before login is not a failure. At most 4 delayed attempts per IP are in
  flight at once; further ones get an immediate `429` without a hash computation, so
  parallel requests cannot side-step the delay or burn CPU on PBKDF2.
- **The hash never leaves the daemon.** `GET /api/config` and the settings export show
  `password_hash = "<unchanged>"`; sending that text back keeps the stored hash.
- **Host header check (DNS rebinding).** Requests are only served for IP literals,
  `localhost`, the listen host and `allowed_hosts`; anything else gets 421. A reverse
  proxy must either rewrite `Host` to the upstream (nginx does by default, Caddy:
  `header_up Host {upstream_hostport}`) or its public name must be listed in
  `allowed_hosts`. `"*"` disables the check.
- **CSRF.** Every write needs the header `X-N5-Fangov-Csrf: 1`; a browser form or
  cross-site fetch cannot add it without CORS, which the API does not offer.
- Changing `[web]` or `[log]` settings takes a restart (except user/password through the
  account forms and the tls keys through the certificate panel); `PUT /api/config`
  reloads curves, sensors, `[daemon]`, `[alert]` and `[dashboard]` values.

Reporting a vulnerability: [SECURITY.md](SECURITY.md).

## Presets

`/etc/n5-fangov/presets/<name>.toml` holds only `[[channel]]` tables; *Save current
curves as…* writes one, *Apply* replaces the channel set of the config file with it and
reloads (a changed channel set or profile answers "restart required"), *Delete* removes a
user preset. Three N5 Pro sets are **built in** (embedded in the binary, listed and
applicable for the `n5pro` profile only — on another profile the API answers 404 —, never
saved over or deleted — the API answers 409):

| Preset | cpu (`k10temp`, critical 88) | ssd (`nvme:max`, critical 72) | hdd (`drivetemp:max`, stop 140) |
|---|---|---|---|
| `n5pro-quiet` — lowest noise, HDD group settles around 45 °C | `[[30,25],[61,163],[85,255]]` | `[[35,55],[65,255]]` | `[[26,63],[55,92],[56,255]]`, critical 66 |
| `n5pro-balanced` — **recommended**: HDD group held near 40 °C, audible under load only | `[[35,60],[60,150],[80,255]]` | `[[35,74],[55,160],[68,255]]` | `[[30,87],[42,140],[50,200],[55,255]]`, critical 60 |
| `n5pro-cool` — drives first, noise second | `[[30,85],[55,170],[75,255]]` | `[[30,90],[50,180],[65,255]]` | `[[28,105],[38,150],[45,210],[50,255]]`, critical 58 |

Duty → RPM on the N5 Pro (measured): CPU 85→2000, 140→3120, 255→5073; SSD 74→2130,
255→4687; HDD 87→1237, 105→1650, 140→2250, 179→2725, 255→3540. The HDD channel keeps
`stop = 140` in every set because the EC does not regulate it after a write. A user
preset file with a built-in name is shadowed by the built-in (logged when listing).

## Extra sensors on the dashboard

```toml
[dashboard]
sensors = ["hwmon:amdgpu:temp1", "ec:system"]   # 0..8 ids, same forms as channel sensors
```

The signed-in Overview lists every readable temperature (`GET /api/sensors`, grouped
CPU / SSD / HDD / GPU / NIC / EC / other); the *chart* toggle per row adds or removes the id
here (`PUT /api/dashboard`). Watched sensors are read once per cycle after the channel
sensors, recorded in the history (`history[].extra`) and drawn in the *Extra sensors*
chart card; they never influence regulation. An id whose device is absent right now is
kept with a warning and charted once it appears; an id that is not a known form is
refused. The list applies without a restart, also when edited in the config file.

## Alerts

```toml
[alert]
transport = "auto"     # auto | pve | mail | log | off
mail_to = "root"       # mail transport only: local user or address
```

`auto` (the default) takes `PVE::Notify` when `/usr/share/perl5/PVE/Notify.pm` and perl
are present, else `mail(1)` to `mail_to`, else the journal only. `pve` or `mail` without
their tool degrade in that same order and `n5-fangov check` says so; `off` drops every
alert but still writes a `suppressed (transport off)` line to the journal.

| Kind | Trigger | Reaction | Cooldown |
|---|---|---|---|
| `sensor` | channel sensor unresolved, unreadable, implausible or frozen | that channel at its safe duty, mode `sensor-error`; the others keep regulating | `alert_cooldown`, sent on the transition only |
| `stall` | 0 RPM at duty ≥ `stall_min_duty` for `stall_cycles` | channel 255 until RPM is back for 3 cycles | `alert_cooldown` |
| `temp` | critical temperature reached | 255 immediately, also under a manual override | `alert_cooldown` |
| `write` | pwm write or read-back failed in 2 consecutive cycles | every channel 255 (failsafe) | `alert_cooldown` |
| `config` | config file has problems | built-in defaults for those values, daemon runs | 30 min (start alert) |
| `config-channels` | channel set corrected (N5 Pro channel added, forced stop duty, pwm the device lacks) | corrected set in effect | `alert_cooldown` |
| `device` | fan controller unreachable for 6 cycles (driver reload) | daemon exits, systemd restarts it with a fresh detection | `alert_cooldown` |
| `profile` | no fan controller detected at start | no regulation, fans stay with EC/BIOS | 30 min (start alert) |
| `start` | controller could not start | see message | 30 min (start alert) |
| `web` | TLS set-up or listener failed | dashboard disabled, regulation continues | 30 min (start alert) |
| `tls` | `tls = "file"` pair unreadable | automatic certificate served, mode `auto (fallback from file)` | 30 min (start alert) |
| `kernel` | DKMS module missing for a bootable kernel (`check --after-update`) | printed on the apt output; nothing changes until the reboot | 30 min (own stamp) |
| `restart` / `failed` | unit failed and came back / stayed down (onfailure unit) | — | 30 min (own stamp) |
| `test` | *Send test alert*, `n5-fangov alerts test` | — | none; one at a time, 20 s bound |

The **Alerts tab** shows the configured and the effective transport, which tools the box
has, the PVE template state, the cooldown, every alert kind with its last delivery, and
the recent alerts (newest first, the last 50, kept across restarts in
`/var/lib/n5-fangov/alerts.json`). Actions:

- **Save** transport and `mail_to` — written to the config file in place and hot-applied;
  no restart. A `PUT /api/config`, a settings import or a preset apply re-applies whatever
  `[alert]` the written file contains.
- **Send test alert** — kind `test`, no cooldown, through the real transport; the response
  carries the delivery error when perl/mail fail. It also lands in the recent list. One
  test at a time (a second click while one runs answers `409 test in progress`), bounded
  to 20 s. `mail_to` is a local user or an address without spaces or quotes and never
  starts with `-`; the recipient is passed to `mail(1)` after `--`.
- **Install / Update template** — writes the two PVE notification template files
  (`n5-fangov-subject.txt.hbs`, `n5-fangov-body.txt.hbs`, embedded in the binary) to
  `/etc/pve/notification-templates/default/`. The button is disabled with the reason when
  the directory is missing or not writable: the daemon's sandbox may write *into* that
  directory but cannot create it, so on a fresh box `install.sh` or `n5-fangov alerts
  template` (root, outside the sandbox) create it. *Current* compares the installed files
  with the embedded ones after an upgrade. The writable probe (a temp file created and
  removed in the directory, a write on pmxcfs) runs at most every 10 minutes and right
  after *Install* or *Save*, not on every poll of the panel.

**The PVE side.** Alerts arrive as severity *warning* with the fields `type = n5-fangov`,
`hostname` and `kind = <alert kind>`. Without a matcher they follow the default matcher
(mail to root). To route them: *Datacenter → Notifications → Notification Matchers →
Add*, match field `type` = `n5-fangov` (or `kind` = `stall`, `temp`, …) and pick the
target (SMTP, Gotify, webhook). The template gives the mail its subject
`[<host>] n5-fangov: <kind>` and body.

```
n5-fangov alerts status          transport, tools, template, last alert per kind, recent alerts
n5-fangov alerts test            send a test alert now
n5-fangov alerts template        install/update the PVE template pair
```

`status` and `test` go through the daemon's socket when it runs (the test then shows in
the dashboard), otherwise they work on the config file. `template` asks the daemon first
and writes the files itself when that fails for anything but "not a PVE host".

## Logs

The journal (`journalctl -u n5-fangov`) is always written and never touched by
n5-fangov. In addition the daemon keeps its own file:

```toml
[log]
file = "/var/log/n5-fangov/n5-fangov.log"   # "" = journal only
max_size_mb = 5                             # rotate above this size (1..100)
max_files = 5                               # keep .1 .. .5 (1..20)
```

Lines in the file carry their own timestamp; rotation renames `n5-fangov.log` to `.1`,
shifts older files up and drops the oldest. Directory `0750`, file `0640` (the unit
runs with `UMask=0077`). `file` must be a plain absolute path under `/var/log/` (no
`..`); anything else falls back to the default with a warning, and an existing target
that is not a regular file (symlink, device, directory) is refused — the daemon appends
as root and must not be pointed at `/dev/sda` or its own config.

```
n5-fangov log -n 200                 newest lines of the current file
n5-fangov log --export today.log     whole current file (rotated files not included)
n5-fangov log --clear                truncate the current file; rotated files and the journal stay
```

The dashboard's Log tab uses the same file (`GET /api/log`, `/api/log/export`,
`DELETE /api/log`); without a file it falls back to the journal and cannot clear.

## Backup / Restore

```
n5-fangov export settings.json       # {"format":1, config: <toml>, presets: {name: <toml>}}
n5-fangov import settings.json       # validates everything, then writes and reloads
```

The export is the config file text (comments included) plus every user preset (the
built-in ones travel with the binary), with the password hash redacted to `<unchanged>`.
`import` refuses the whole bundle when any part fails to parse — nothing is written in
that case. `<unchanged>` is resolved from the password stored on the importing machine;
on a fresh box run `n5-fangov passwd` first (or put a real hash into the bundle). Presets
that exist locally but not in the bundle stay. A running daemon reloads curves and
`[daemon]` values; a changed channel set or profile, or any `[web]`/`[log]` change, needs
`systemctl restart n5-fangov` (the command says so). The API offers the same
(`GET /api/config/export`, `POST /api/config/import`, auth + CSRF), and the settings gear
has both as buttons.

## Updates

**Package update** (`apt upgrade` of n5-fangov): postinst restarts a running daemon;
`ExecStopPost=failsafe` puts the fans into the safe state between the old and the new
process, `check --quiet` gates the new start.

**Kernel update** (N5 Pro): the EC driver is a DKMS module. If DKMS did not build it for
the new kernel, the next boot has no `pwm` files, `check` fails and the fans stay under
BIOS/EC control (safe, but unregulated for the drives). Two gates catch this:

1. `/etc/apt/apt.conf.d/90n5-fangov` runs `n5-fangov check --after-update` after every
   dpkg run. It requires `updates/dkms/minisforum_n5_it5571.ko` for every kernel the
   box can **boot into**: the running one plus what `proxmox-boot-tool kernel list`
   selects (manually, automatically, pinned); without that tool the running one plus
   the newest installed. Missing → `kernel X: fan driver module missing — run: dkms
   install minisforum-n5-it5571/<ver> -k X` on the apt output plus a `kernel` alert
   (30 min cooldown). Older kernels that are merely still installed (apt keeps two)
   get an `info only` line and no alert. The apt run never fails because of it.
   Non-N5-Pro boxes: no-op.
2. `ExecStartPre=n5-fangov check` reports `dkms` for the running kernel at every start.

What a Proxmox upgrade **can** affect: the kernel (above), `dkms` itself, perl/
`PVE::Notify` for alerts. What it **cannot**: the config, presets, TLS certificate and
logs live under `/etc/n5-fangov` and `/var/log/n5-fangov` and are never touched by
package scripts (purge removes them). The state directory `/var/lib/n5-fangov`
(sessions, alert history) is removed with the package; nothing in it is worth keeping.

**Rollback** to the previous version: put the old binary back and restart —
`install -m0755 /path/to/old/n5-fangov /usr/bin/n5-fangov && systemctl restart n5-fangov`
(keep a copy before an update: `cp /usr/bin/n5-fangov /root/n5-fangov-$(n5-fangov version | cut -d' ' -f2).bak`).
The config is forward-compatible: a newer daemon reads an older file; an older daemon
warns about unknown keys and ignores them (rule: config errors never prevent a start).
Sessions are dropped when the credential epoch changes, nothing else is versioned.
If `setup` replaced the config, the previous one is `config.toml.bak-<timestamp>` next to it.

## Uninstall

```
./deploy/uninstall.sh            # stops and removes the unit, binary, apt hook, PVE template, state
./deploy/uninstall.sh --purge    # additionally /etc/n5-fangov (config, presets, certificate) and /var/log/n5-fangov
```

or `apt remove n5-fangov` / `apt purge n5-fangov` for the package. `ExecStopPost=failsafe`
leaves the fans in the profile's safe state (N5 Pro: CPU/SSD back to EC automatic, HDD at
the fixed stop duty until the next boot). The kernel module is not touched — it belongs to
the DKMS package from the prerequisites.

## Troubleshooting

| Symptom | Cause | Check / fix |
|---|---|---|
| `n5-fangov check` exits 1: `profile "…" not detected` / `setup` finds no profile | the hwmon device is not there: module not loaded, or (N5 Pro) `experimental_write=1` missing so the `pwm*` nodes are invisible | `n5-fangov detect`; `lsmod \| grep minisforum`; `cat /etc/modprobe.d/minisforum-n5-it5571.conf`; `modprobe minisforum_n5_it5571`; `ls /sys/class/hwmon/*/pwm1` |
| `check` says `dkms: module missing for <kernel>` / after a reboot the fans run the BIOS curve | DKMS did not build the module for the new kernel (headers missing, build failed) | `dkms status`; `apt install proxmox-headers-$(uname -r)`; `dkms install minisforum-n5-it5571/<ver> -k $(uname -r)`; then `systemctl restart n5-fangov` |
| `check` exits 1: `channel <name>: … pwmN …: permission denied` / journal `permission denied` on `/sys/...` | the sandbox forbids the write: `ProtectKernelTunables` set to `yes` in a drop-in, or `ReadWritePaths` lost the hwmon path | `systemctl cat n5-fangov`; `make verify-deploy`; keep `ProtectKernelTunables=no` and `-/sys/class/hwmon -/sys/devices` in `ReadWritePaths` |
| `status` shows a channel in `sensor-error` | its sensor is unresolved or unreadable (N5 Pro without SATA drives: `drivetemp:max` has no device) | expected on a box without HDDs; the channel sits at its stop duty. Otherwise `sensors`, `ls /sys/class/hwmon/*/name`, and the sensor id in the config |
| Browser shows a certificate warning | self-signed automatic certificate not yet trusted | lock icon → *Download .crt/.cer* → trust it per the panel's recipe → reload; or `n5-fangov cert export` |
| Browser refuses the LAN name outright (no warning page) after a certificate change | HSTS: the served certificate does not cover that name any more (upload without the name, or the `tls` fallback) | open the dashboard by IP (`https://<ip>:8010`), then *Back to auto* or upload a pair that covers the name; `n5-fangov cert info` |
| `421 host header not allowed` | the `Host` a reverse proxy passes is neither an IP, `localhost`, the listen host nor listed | add the public name to `[web].allowed_hosts` or let the proxy rewrite `Host` to the upstream; restart |
| Journal: `auth misconfigured — web bound to loopback` | `auth = "basic"` without usable `user`/`password_hash`, or a typo in `auth` — the daemon fails closed | `n5-fangov check` prints the field; `n5-fangov passwd`; restart |
| Dashboard on the LAN answers plain HTTP or refuses to start with `tls` | non-loopback listen with `tls = "off"` is forced to `auto`; `tls = "file"` without both paths falls back | `n5-fangov check`; `n5-fangov cert info`; fix `[web]` |
| Alerts do not arrive | transport degraded (`pve` without `PVE::Notify`, `mail` without `mail(1)`), cooldown active, PVE matcher routes elsewhere, or (mail path) the sandbox blocks `postdrop` | Alerts tab → *Send test alert* (reports the delivery error); `n5-fangov alerts status`; `journalctl -u n5-fangov \| grep -i alert`; see Hardening for the `mail(1)` case |
| Toast `Session expired` / `401` while working | the cookie session ran out (12 h, 30 d with *Remember me*) or the password was changed outside the dashboard | sign in again; the editor keeps unsaved curve edits |
| Notice `restart required` after Apply/Import/Preset | the channel set or the profile changed; `[web]`/`[log]` keys changed by import | `systemctl restart n5-fangov` (the daemon keeps running the old set until then) |
| Dashboard banner `Connection to the daemon lost` | daemon restarting or down; fans stay on the daemon side (failsafe on exit) | `systemctl status n5-fangov`; `journalctl -u n5-fangov -n 50` |
| `n5-fangov test` refuses: `daemon socket answers` | the daemon regulates that channel | `systemctl stop n5-fangov` first (or `--force` after stopping it yourself) |
| Unit keeps restarting, `n5-fangov-onfailure` mails `failed` | `check` fails at every start (see the first rows) or the controller cannot write | `journalctl -u n5-fangov -b`; `n5-fangov check` by hand |

## Hardening

The unit runs sandboxed (`deploy/n5-fangov.service`); the list is the contract, verify
on real hardware after every change — `/sys` writes are what most sandboxes forbid.

| Setting | Effect |
|---|---|
| `NoNewPrivileges=yes`, `LockPersonality=yes`, `RestrictRealtime=yes` | no privilege escalation from the daemon |
| `ProtectSystem=strict` + `ReadWritePaths=-/etc/n5-fangov -/run/n5-fangov -/var/log/n5-fangov -/var/lib/n5-fangov -/sys/class/hwmon -/sys/devices -/var/spool/postfix/maildrop -/etc/pve/notification-templates` | whole file system read-only except config/presets/tls, runtime dir, logs, state dir, the hwmon attributes, the postfix maildrop (alerts via `mail`) and the PVE template directory (the Alerts tab's install button; verified on the reference host — the daemon may write into it but cannot create it); `-` = a missing path does not fail the start |
| `ProtectKernelTunables=no` | **must stay `no`**: the pwm files are kernel tunables |
| `ProtectHome=yes`, `PrivateTmp=yes`, `PrivateDevices=yes`, `ProtectControlGroups=yes` | no access to home, private /tmp, no physical devices in /dev, cgroups read-only |
| `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_NETLINK`, `RestrictNamespaces=yes` | socket, TCP/HTTP(S), netlink for the interface list (`net.Interfaces()` — the fallback when the TLS certificate needs the box's addresses), nothing else |
| `MemoryDenyWriteExecute=yes`, `SystemCallArchitectures=native`, `SystemCallFilter=@system-service` | no JIT/exec tricks, native syscalls only, no module loading |
| `CapabilityBoundingSet=` (empty) | the daemon runs as uid 0 but holds no capability: it never loads modules (modules-load.d does), never chowns, and root's own files and the root-owned sysfs attributes need none |
| `UMask=0077`, `LogsDirectory=n5-fangov` (`0750`), `RuntimeDirectory=n5-fangov` (`0750`), `StateDirectory=n5-fangov` (`0700`) | files private by default; `/var/lib/n5-fangov` holds `sessions.json` (hashed session tokens — secrets, hence 0700) and `alerts.json`. `serve --state-dir DIR` / `N5FANGOV_STATE_DIR` move it; an unwritable one is logged once and the daemon runs without persistence (sessions and history in memory) |

**Alert delivery under the sandbox.** Alerts run `perl -MPVE::Notify` or `mail(1)`
from inside this sandbox. Verified: **PVE notification targets of type SMTP** (the
Proxmox stack talks to the mail server itself; nothing on the local file system is
written). The `mail(1)`/sendmail path on non-PVE hosts, and a PVE *sendmail* target,
hand the message to postfix's `postdrop`, which writes into
`/var/spool/postfix/maildrop` — that directory is in `ReadWritePaths`, but it is
`0730 postfix:postdrop` and `NoNewPrivileges` suppresses the setgid bit `postdrop`
relies on, so this path additionally needs `CAP_DAC_OVERRIDE`. Not verified; if you
need it, add a drop-in (`systemctl edit n5-fangov`) with
`CapabilityBoundingSet=CAP_DAC_OVERRIDE` and test with the Alerts tab's *Send test
alert* — that one is sent by the daemon from inside the sandbox and reports the
delivery error (`n5-fangov alert` or `alerts test` from a shell with the daemon stopped
run outside the sandbox and prove nothing). A failed delivery is logged
(`alert: ... failed`), the alert text is always in the journal.

`make verify-deploy` runs `systemd-analyze verify` over the units and `apt-config`
over the apt hook (on a host that has them; the Docker build container skips both
with a note). Run it on the target box after editing the unit.

## Compatibility

| Platform | Status |
|---|---|
| Proxmox VE 9.2 / Debian 13 (trixie), kernel 7.0.12-1-pve (DKMS module also built for 7.0.0-2/7.0.0-3-pve), driver `minisforum-n5-it5571` 0.2.0, Minisforum N5 Pro BIOS 1.05 | **verified** (channel mapping, stop behaviour, load tests, multi-hour runs; every release is verified on this box before it is tagged) |
| Debian/Ubuntu with NCT67xx (`nct6775`) or IT87xx (`it87`) | from documentation, untested — please report |
| Any Linux with hwmon, no PWM | monitoring only |
| Unraid, TrueNAS, non-systemd | binary runs; the guard chain relies on systemd |

## Build

No Go toolchain needed locally: `tools/remote-go.ps1` builds in a `golang:<version>-alpine`
container (static, `CGO_ENABLED=0`; the version is the one pinned in `go.mod`). Or
plainly: `CGO_ENABLED=0 go build ./cmd/n5-fangov`. `make deb` builds the Debian package.
`make release NOTES="…"` (on a clean tag, gh CLI signed in) creates the GitHub release
with the static binary and its sha256 — the releases page linked from the About tab is
maintained this way, one entry per tag. Tests, the race run and the mock:
[CONTRIBUTING.md](CONTRIBUTING.md); the package contract: [DESIGN.md](DESIGN.md).

## About and license

GPL-2.0-only; the full text is in [LICENSE](LICENSE). The only dependency,
[`BurntSushi/toml`](https://github.com/BurntSushi/toml), is MIT-licensed (text in
`deploy/debian/copyright`). The dashboard's About tab (public, `GET /api/about`) carries
name, version with the pre-release tag, license, repository and author links, the Go
version the binary was built with, and the credits:
[`ltdstudio/minisforum-n5-it5571`](https://github.com/ltdstudio/minisforum-n5-it5571)
(the kernel driver for the IT5571 EC) and [`Sl0thC0der/proxfansx`](https://github.com/Sl0thC0der/proxfansx)
(the dashboard idea and the generic NCT67xx/IT87xx handling the `nct67xx`/`it87xx`
profiles follow; no code shared). A UI mock for screenshots and layout work runs with
`?mock=1` (`&auth=none`, `&user=1` for the signed-in variants; login `admin`/`admin`).

Versioning: `internal/version/version.go` holds the only version literal — the current
pre-release, shown by `n5-fangov version` and listed in [CHANGELOG.md](CHANGELOG.md);
`make` overrides it with `git describe` (`vX.Y.Z-3-gabcdef` on commits after a tag). The
Debian package version maps `-alpha`/`-beta`/`-rc` to `~` (so `X.Y.Z~beta.1` sorts before
`X.Y.Z`) and every other `-` to `+` (`X.Y.Z~beta.1+3+gabcdef` sorts after the tag it is
based on).
