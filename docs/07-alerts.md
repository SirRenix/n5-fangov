# Alerts and guards

What this page covers: what the daemon does when something fails, which alert each
failure raises, how alerts leave the box (Proxmox notification stack, mail, journal),
the PVE side, cooldowns, the test alert and the alert history.

- [What the daemon guards against](#what-the-daemon-guards-against)
- [Transports](#transports)
- [Alert kinds](#alert-kinds)
- [The Alerts tab](#the-alerts-tab)
- [The PVE template](#the-pve-template)
- [The PVE side](#the-pve-side)
- [From the shell](#from-the-shell)

## What the daemon guards against

| Failure | Response |
|---|---|
| Controller hangs | systemd watchdog (60 s) → kill → failsafe → restart |
| Controller dies (crash, OOM, kill) | `ExecStopPost=n5-fangov failsafe` → profile-defined safe state |
| Kernel update without the DKMS module | apt hook `check --after-update` alerts before the reboot; `ExecStartPre=n5-fangov check` fails loudly after it; fans stay in EC/BIOS mode ([kernel gate](02-kernel-driver.md#the-kernel-update-gate)) |
| Sensor unreadable, implausible, frozen or absent | that channel at its safe duty (fixed stop duty, else 255), alert naming it, re-resolve; the other channels keep regulating |
| Fan stalls (0 RPM at duty ≥ threshold) | channel 255, alert, auto-recovery |
| Write fails or read-back differs | 255, alert |
| Somebody else writes to `/sys` | set-point re-asserted every minute, warning |
| Broken config | built-in defaults + warning + alert; daemon still starts |
| Critical temperature | 255 immediately, also in manual mode |
| Fan controller vanishes (driver reload) | daemon exits after 6 failed cycles, systemd restarts it with a fresh detection |

Every alert is always written to the journal, whatever the transport.

## Transports

```toml
[alert]
transport = "auto"     # auto | pve | mail | log | off
mail_to = "root"       # mail transport only: local user or address
```

`auto` (the default) takes `PVE::Notify` (template `n5-fangov`) when
`/usr/share/perl5/PVE/Notify.pm` and perl are present, else `mail(1)` to `mail_to`, else
the journal only. `pve` or `mail` without their tool degrade in that same order and
`n5-fangov check` says so; `off` drops every alert but still writes a
`suppressed (transport off)` line to the journal. `mail_to` is a local user or an
address without spaces or quotes and never starts with `-`; the recipient is passed to
`mail(1)` after `--`.

Delivery runs from inside the unit's sandbox. PVE targets of type SMTP are verified;
the `mail(1)`/sendmail path needs one extra capability, see
[Alert delivery under the sandbox](08-https-security.md#alert-delivery-under-the-sandbox).

## Alert kinds

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

`alert_cooldown` is `[daemon] alert_cooldown` (`60s`..`24h`, default `30m`) per alert
kind ([Configuration](06-configuration.md#configuration-reference)). The start alerts
are cooled 30 minutes regardless; `kernel`, `restart` and `failed` are raised outside
the daemon and keep their own 30-minute stamps (`alert.<kind>` under `/run/n5-fangov/`).

`restart` and `failed` come from `n5-fangov-onfailure.service`: when the main unit
fails, it reads `Result`/`ExecMainStatus`, waits 8 s and reports `restart` (daemon back)
or `failed` (still down) with the real cause.

## The Alerts tab

Shows the configured and the effective transport, which tools the box has, the PVE
template state, the cooldown, every alert kind with its last delivery, and the recent
alerts (newest first, the last 50, kept across restarts in
`/var/lib/n5-fangov/alerts.json`). Screenshot in [Dashboard](04-dashboard.md#alerts).
Actions:

- **Save** transport and `mail_to` — written to the config file in place and
  hot-applied; no restart. A `PUT /api/config`, a settings import or a preset apply
  re-applies whatever `[alert]` the written file contains.
- **Send test alert** — kind `test`, no cooldown, through the real transport; the
  response carries the delivery error when perl/mail fail. It also lands in the recent
  list. One test at a time (a second click while one runs answers `409 test in
  progress`), bounded to 20 s. This is the test that proves delivery: it is sent by the
  daemon from inside its sandbox.
- **Install / Update template** — see below.

## The PVE template

The two PVE notification template files (`n5-fangov-subject.txt.hbs`,
`n5-fangov-body.txt.hbs`) are embedded in the binary and installed to
`/etc/pve/notification-templates/default/` by `install.sh`, the deb postinst, the
Alerts tab button or `n5-fangov alerts template`. The button is disabled with the reason
when the directory is missing or not writable: the daemon's sandbox may write *into*
that directory but cannot create it, so on a fresh box `install.sh` or `n5-fangov alerts
template` (root, outside the sandbox) create it. *Current* compares the installed files
with the embedded ones after an upgrade. The writable probe (a temp file created and
removed in the directory, a write on pmxcfs) runs at most every 10 minutes and right
after *Install* or *Save*, not on every poll of the panel.

## The PVE side

Alerts arrive as severity *warning* with the fields `type = n5-fangov`, `hostname` and
`kind = <alert kind>`. Without a matcher they follow the default matcher (mail to root).
To route them: *Datacenter → Notifications → Notification Matchers → Add*, match field
`type` = `n5-fangov` (or `kind` = `stall`, `temp`, …) and pick the target (SMTP, Gotify,
webhook). The template gives the mail its subject `[<host>] n5-fangov: <kind>` and body.

## From the shell

```
n5-fangov alerts status          transport, tools, template, last alert per kind, recent alerts
n5-fangov alerts test            send a test alert now
n5-fangov alerts template        install/update the PVE template pair
```

`status` and `test` go through the daemon's socket when it runs (the test then shows in
the dashboard), otherwise they work on the config file. `template` asks the daemon first
and writes the files itself when that fails for anything but "not a PVE host". A test
sent from a shell with the daemon stopped runs outside the sandbox and proves nothing
about delivery from the daemon.

Next: [Troubleshooting](10-troubleshooting.md) · [HTTPS and security](08-https-security.md) ·
[Configuration](06-configuration.md)
