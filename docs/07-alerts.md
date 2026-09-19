# Alerts and guards

What this page covers: what the daemon does when something fails, which alert each
failure raises, how alerts leave the box (Proxmox notification stack, mail, webhook,
journal), the PVE side, cooldowns, the test alert and the alert history.

- [What the daemon guards against](#what-the-daemon-guards-against)
- [Transports](#transports)
- [Webhook](#webhook)
- [Alert kinds](#alert-kinds)
- [The Alerts page and the transport section](#the-alerts-page-and-the-transport-section)
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
| Reading at the built-in **ceiling** of the sensor kind (HDD 65 / SSD 85 / CPU 100 °C, or a lower configured one; a composite per part) — whatever `critical` says | 255 immediately, mode `critical`, alert `ceiling`, until the reading is 3 °C below ([Ceilings](06-configuration.md#ceilings-and-the-emergency-action)) |
| Channel stays at its ceiling with a stalled fan (`emergency_cycles`), or 3 × as long regardless | the emergency hook `/etc/n5-fangov/emergency.sh` runs once (`emergency = true`, off by default), alert `emergency` with the exit status |
| Fan controller vanishes (driver reload) | daemon exits after 6 failed cycles, systemd restarts it with a fresh detection |
| Scheduled preset switch fails | previous curves stay, alert, retry at the next transition |

Every alert is always written to the journal, whatever the transport.

## Transports

```toml
[alert]
transport = "auto"     # auto | pve | mail | webhook | log | off
mail_to = "root"       # mail transport only: local user or address
webhook_url = ""       # webhook transport only: absolute http(s) URL
webhook_format = "json"   # json | text
```

`auto` (the default) takes `PVE::Notify` (template `n5-fangov`) when
`/usr/share/perl5/PVE/Notify.pm` and perl are present, else `mail(1)` to `mail_to`, else
the journal only. `pve` or `mail` without their tool degrade in that same order and
`n5-fangov check` says so; `webhook` is never chosen by `auto` and degrades to `log`
only when `webhook_url` is empty or invalid (with a config warning); `off` drops every
alert but still writes a `suppressed (transport off)` line to the journal. `mail_to`
is a local user or an address without spaces or quotes and never starts with `-`; the
recipient is passed to `mail(1)` after `--`.

Delivery runs from inside the unit's sandbox and is bounded to 30 s. PVE targets of
type SMTP are verified; the `mail(1)`/sendmail path needs one extra capability, see
[Alert delivery under the sandbox](08-https-security.md#alert-delivery-under-the-sandbox).
The webhook needs nothing beyond outbound TCP, which the sandbox allows.

## Webhook

`transport = "webhook"` sends one `POST` per alert to `webhook_url` — the transport for
non-PVE hosts without a mail set-up and for ntfy, Gotify and Home Assistant.

- **URL rules:** absolute `http` or `https`, with a host, no `user:pass@` part, at most
  2048 characters. Redirects are **not** followed — give the final URL. TLS uses the
  system CA pool, there is no "insecure" switch: a receiver with a self-signed
  certificate needs its CA in `/usr/local/share/ca-certificates/` on this box
  (`update-ca-certificates`), or plain `http` on a trusted LAN.
- **Success** is any 2xx answer. Anything else, a timeout or a transport error is the
  delivery error `webhook: <status or error>` — returned by *Send test alert* and logged
  (`alert: … failed`); the alert text is in the journal regardless.
- **Headers** on every request: `Content-Type` (by format), `User-Agent:
  n5-fangov/<version>`, `X-N5-Fangov-Kind: <kind>` and `Title: n5-fangov <kind> on
  <host>` (ntfy reads `Title`).
- **Redaction:** the journal, `n5-fangov check`, `n5-fangov alerts status` and
  `GET /api/alerts` for a **token** caller show the URL **without query and userinfo**
  (`https://gotify.example.test/message`) because Gotify carries its key in the query.
  The full URL is in the config file and in `GET /api/alerts` for a browser session or
  Basic auth — the dashboard shows it in full; a `read` token gets the redacted form
  ([tokens](08-https-security.md#api-tokens)).

`webhook_format = "json"` (default) sends `Content-Type: application/json`:

```json
{"type":"n5-fangov","kind":"stall","severity":"warning","hostname":"n5host",
 "title":"n5-fangov stall on n5host","message":"<alert text>","ts":1789500000}
```

`title` and `message` are what Gotify and Home Assistant read; ntfy shows the JSON text
as the message and takes the title from the header. `webhook_format = "text"` sends
`Content-Type: text/plain; charset=utf-8` with the alert text as the body — for ntfy
and any receiver that wants a plain line.

A test alert as a receiver sees it (`Send test alert`, format `json`), headers
`User-Agent: n5-fangov/0.3.1`, `X-N5-Fangov-Kind: test`,
`Title: n5-fangov test on n5host`, body:

```json
{"type":"n5-fangov","kind":"test","severity":"warning","hostname":"n5host","title":"n5-fangov test on n5host","message":"test alert from n5-fangov 0.3.1 on n5host at 2026-09-18 02:04:34 - delivery works if you can read this.","ts":1789689874}
```

To try the transport without a real receiver, any HTTP listener on the LAN that answers
`200` will do — a few lines of python3 `http.server` with a `do_POST` that prints the
headers and body — pointed at with `webhook_url = "http://192.0.2.50:8080/"`.

| Receiver | `webhook_url` | `webhook_format` | Notes |
|---|---|---|---|
| ntfy | `https://ntfy.example.test/n5` | `text` | topic in the path; the `Title` header becomes the notification title. n5-fangov sends no `Authorization` header and refuses userinfo in the URL — a protected topic takes its access token as the `auth` query parameter (ntfy docs, *Authentication → Query param*), which is redacted in logs like the Gotify key |
| Gotify | `https://gotify.example.test/message?token=<app token>` | `json` | the app token is the query — redacted in logs, status and for token callers, present in the file and in `GET /api/alerts` for a session |
| Home Assistant | `http://ha.example.test:8123/api/webhook/<id>` | `json` | webhook trigger in an automation; the payload is `trigger.json` (`trigger.json.kind`, `.title`, `.message`). Use a long random `<id>`; HA webhooks carry no other auth |

A Home Assistant automation on the receiving side:

```yaml
automation:
  - alias: n5-fangov alert
    triggers:
      - trigger: webhook
        webhook_id: "<id>"
        allowed_methods: [POST]
        local_only: true
    actions:
      - action: notify.mobile_app_phone
        data:
          title: "{{ trigger.json.title }}"
          message: "{{ trigger.json.kind }}: {{ trigger.json.message }}"
```

*Save* in Settings → Alert transport writes the four `[alert]` keys in place and applies them at
once; `PUT /api/alerts` answers 400 for an invalid URL, format or `mail_to`, and for
`webhook` without a URL. Switching the transport to anything but `webhook` **clears
`webhook_url`** from the file (0.4.1): a receiver key does not linger after the switch —
enter it again when you come back to the webhook (a settings export made before the
switch still carries it). The PVE notification stack has its own webhook and Gotify
targets — on a PVE host `transport = "auto"` plus a matcher ([The PVE side](#the-pve-side))
is the alternative that keeps the routing in one place.

## Alert kinds

| Kind | Trigger | Reaction | Cooldown |
|---|---|---|---|
| `sensor` | channel sensor unresolved, unreadable, implausible or frozen | that channel at its safe duty, mode `sensor-error`; the others keep regulating | `alert_cooldown`, sent on the transition only |
| `stall` | 0 RPM at duty ≥ `stall_min_duty` for `stall_cycles` | channel 255 until RPM is back for 3 cycles | `alert_cooldown` |
| `temp` | critical temperature reached | 255 immediately, also under a manual override | `alert_cooldown` |
| `ceiling` | a part of the channel's sensor at/above its kind's ceiling (HDD 65 / SSD 85 / CPU 100 °C, or a lower `[[channel]] ceiling`) | 255, mode `critical`, until every part is 3 °C below its ceiling; the message names the part, reading, ceiling and the configured critical | `alert_cooldown`, sent on the transition only — per kind, so a second channel entering its episode inside the cooldown is a log line only |
| `emergency` | a channel stayed at its ceiling for `emergency_cycles` cycles with a stalled fan, or 3 × as long regardless, and `emergency = true` | the hook `/etc/n5-fangov/emergency.sh` ran once for this episode; the message carries the reason and its exit status (never raised with `emergency = false` or a hook the daemon refused — that is a log line) | `alert_cooldown` (per kind, like `ceiling`; the hook itself runs for every channel) |
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
| `schedule` | a scheduled preset switch failed: preset missing or invalid, write or reload error ([Schedules](06-configuration.md#schedules)) | previous curves stay; retried at the next transition | 30 min (own stamp) |
| `test` | *Send test alert*, `n5-fangov alerts test` | — | none; one at a time, 20 s bound |

`alert_cooldown` is `[daemon] alert_cooldown` (`60s`..`24h`, default `30m`) per alert
kind ([Configuration](06-configuration.md#configuration-reference)). The start alerts
are cooled 30 minutes regardless; `kernel`, `restart`, `failed` and `schedule` are
raised outside the controller and keep their own 30-minute stamps (`alert.<kind>`
under `/run/n5-fangov/`).

`restart` and `failed` come from `n5-fangov-onfailure.service`: when the main unit
fails, it reads `Result`/`ExecMainStatus`, waits 8 s and reports `restart` (daemon back)
or `failed` (still down) with the real cause.

## The Alerts page and the transport section

The **Alerts page** shows the effective transport, which tools the box has, the cooldown,
every alert kind with its last delivery, and the recent alerts (newest first, the last
50, kept across restarts in `/var/lib/n5-fangov/alerts.json`); the transport form and
the PVE template card are **Settings → Alert transport**. Screenshots in
[Dashboard](04-dashboard.md#alerts). Actions:

- **Save** (Settings → Alert transport) transport and its fields — `mail_to` for
  `auto`/`mail`, `webhook_url` and `webhook_format` for `webhook` — written to the config
  file in place and hot-applied; no restart. A `PUT /api/config`, a settings import or a preset apply
  re-applies whatever `[alert]` the written file contains.
- **Send test alert** (Alerts page and the transport section) — kind `test`, no cooldown, through the real transport; the
  response carries the delivery error when perl, mail or the webhook receiver fail. It
  also lands in the recent list. One test at a time (a second click while one runs
  answers `409 test in progress`), bounded to 20 s. This is the test that proves
  delivery: it is sent by the daemon from inside its sandbox. What arrives (mail body,
  PVE notification, webhook `message`):

  ```
  test alert from n5-fangov 0.3.1 on n5host at 2026-09-18 02:01:54 - delivery works if you can read this.
  ```

  Alert texts are plain ASCII since 0.3.1 — the em dash used before was mangled by
  mail clients.
- **Install / Update template** — see below.

## The PVE template

The two PVE notification template files (`n5-fangov-subject.txt.hbs`,
`n5-fangov-body.txt.hbs`) are embedded in the binary and installed to
`/etc/pve/notification-templates/default/` by `install.sh`, the deb postinst, the
*Install template* in Settings → Alert transport or `n5-fangov alerts template`. The button is disabled with the reason
when the directory is missing or not writable: the daemon's sandbox may write *into*
that directory but cannot create it, so on a fresh box `install.sh` or `n5-fangov alerts
template` (root, outside the sandbox) create it. *Current* compares the installed files
with the embedded ones after an upgrade. The writable probe (a temp file created and
removed in the directory, a write on pmxcfs) runs at most every 10 minutes and right
after *Install* or *Save*, not on every poll of the page.

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

`status` shows the configured and the effective transport, `mail_to`, and for the
webhook the URL (query redacted) and the format. `status` and `test` go through the
daemon's socket when it runs (the test then shows in the dashboard), otherwise they
work on the config file. `template` asks the daemon first and writes the files itself
when that fails for anything but "not a PVE host". A test sent from a shell with the
daemon stopped runs outside the sandbox and proves nothing about delivery from the
daemon.

Next: [Troubleshooting](10-troubleshooting.md) · [HTTPS and security](08-https-security.md) ·
[Configuration](06-configuration.md)
