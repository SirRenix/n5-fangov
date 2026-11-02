# Alerts and guards

What the daemon does when something fails, which alert it raises, and how alerts leave
the box.

## What the daemon guards against

| Failure | Response |
|---|---|
| Controller hangs | systemd watchdog (60 s) → kill → failsafe → restart |
| Controller dies (crash, OOM, kill) | `ExecStopPost=n5-fangov failsafe` → profile-defined safe state |
| Kernel update without the DKMS module, or with an old build of it | apt hook `check --after-update` alerts before the reboot; `ExecStartPre=n5-fangov check` fails loudly after it; fans stay in EC/BIOS mode ([kernel gate](02-kernel-driver.md#the-kernel-update-gate)) |
| Sensor unreadable, implausible, frozen or absent | that channel at its safe duty (fixed stop duty, else 255), alert naming it, re-resolve; the other channels keep regulating |
| Fan stalls (0 RPM at duty ≥ threshold) | channel 255, alert, auto-recovery |
| Write fails or read-back differs | 255, alert |
| EC duty register differs from the written duty (only with the driver option `debug_regs=1`) | no write error: regulation continues, journal warning `EC register deviation: <channel>`, at most one per channel per 5 minutes with the count of the suppressed ones |
| N5 driver loaded read-only (pwm nodes hidden: `force`, no `experimental_write`, board not in the write allowlist), or missing (not installed, not built for this kernel, not loaded), on a board the driver writes on (N5 PRO/F8NAA, N5/F8NAA) | no start, also with `profile = "auto"`: `check` (`ExecStartPre`) fails with the reason and alerts `driver`; after 5 failed starts the unit stays `failed` and the onfailure unit alerts `failed`; fans stay under EC control ([Driver options](02-kernel-driver.md#driver-options-and-autoload)) |
| The same on a board the driver keeps read-only (N5 Air, N5 PRO/F8NAB, or a DMI `sys_vendor` other than the driver's) | `profile = "auto"` runs as `monitor` (sensors only), as in 0.4.2; no alert |
| Writer lock cannot be taken for another reason than a running `validate`/`test` (run directory not writable) | the daemon starts without it (cooling first), journal warning and alert `start`; a `validate` or `test` started then is not refused |
| Driver read error (`ETIMEDOUT`, `EAGAIN`, `ENODATA`, `ENODEV` on a temperature or tach) | journal warning `driver read error: <source>: <errno>`, at most one per source per 5 minutes with the count of the suppressed ones (when the errors stop, `driver read recovered: <source>: N more error(s)` after the 5 minutes); the sensor rules above decide what happens to the channel |
| Somebody else writes to `/sys` | set-point re-asserted every minute, warning |
| Broken config | built-in defaults + warning + alert; daemon still starts |
| Critical temperature | 255 immediately, also in manual mode |
| Reading at the built-in **ceiling** of the sensor kind (HDD 65 / SSD 85 / CPU 100 °C, or a lower configured one; a composite per part), whatever `critical` says | 255 immediately, mode `critical`, alert `ceiling`, until the reading is 3 °C below ([Ceilings](06-configuration.md#ceilings-and-the-emergency-action)) |
| Channel stays at its ceiling with a stalled fan (`emergency_cycles`), or 3 × as long regardless | the emergency hook `/etc/n5-fangov/emergency.sh` runs once (`emergency = true`, off by default), alert `emergency` with the exit status |
| Fan controller vanishes (driver reload) | daemon exits after 6 failed cycles, systemd restarts it with a fresh detection |
| Scheduled preset switch fails | previous curves stay, alert, retry at the next transition |

This is what the daemon does; it was exercised on one machine
([Hardware status](../README.md#hardware-status)).

Every alert is written to the journal, whatever the transport: `ALERT[<kind>]: <text>`
when the daemon raises it (priority err), then `ALERT[<kind>] sent via <transport>:
<text>` when the sink has handed it on. With transport `off` the second line reads
`suppressed (transport off)`; a failed delivery adds `alert: …` after it. An alert
inside its cooldown writes nothing (a start alert: `suppressed (last one … ago,
cooldown …)`). Test alerts go to the sink directly and produce only the `sent via` line.

## Transports

```toml
[alert]
transport = "auto"     # auto | pve | mail | webhook | log | off
mail_to = "root"       # mail transport only: local user or address
webhook_url = ""       # webhook transport only: absolute http(s) URL
webhook_format = "json"   # json | text
```

`auto` (the default) takes `PVE::Notify` (template `n5-fangov`) when
`/usr/share/perl5/PVE/Notify.pm` and perl are present, else `mail(1)` to `mail_to`,
else the journal only. `pve` or `mail` without their tool degrade in that same order,
and `n5-fangov check` says so. `webhook` is never chosen by `auto` and degrades to `log`
only when `webhook_url` is empty or invalid. `off` drops every alert but still writes
the `suppressed` line. `mail_to` is a local user or an address without spaces or
quotes that never starts with `-`; it is passed to `mail(1)` after `--`.

Delivery runs from inside the unit's sandbox and is bounded to 30 s. PVE targets of type
SMTP are verified; the `mail(1)`/sendmail path needs one extra capability
([Alert delivery under the sandbox](08-https-security.md#alert-delivery-under-the-sandbox)).
The webhook needs only outbound TCP.

## Webhook

`transport = "webhook"` sends one `POST` per alert to `webhook_url`: for non-PVE hosts
without a mail set-up and for ntfy, Gotify and Home Assistant.

- **URL:** absolute `http` or `https`, with a host, no `user:pass@`, at most 2048
  characters. Redirects are **not** followed; give the final URL. TLS uses the system
  CA pool, there is no "insecure" switch: a receiver with a self-signed certificate
  needs its CA in `/usr/local/share/ca-certificates/` on this box
  (`update-ca-certificates`), or plain `http` on a trusted LAN.
- **Success** is any 2xx answer. Anything else, a timeout or a transport error is the
  delivery error `webhook: <status or error>`, returned by *Send test alert* and logged
  (`alert: … failed`).
- **Headers** on every request: `Content-Type` (by format), `User-Agent:
  n5-fangov/<version>`, `X-N5-Fangov-Kind: <kind>` and `Title: n5-fangov <kind> on
  <host>` (ntfy reads `Title`).
- **Redaction:** the journal, `n5-fangov check`, `n5-fangov alerts status` and
  `GET /api/alerts` for a **token** caller show the URL without query and userinfo
  (`https://gotify.example.test/message`), because Gotify carries its key in the query.
  The full URL is in the config file, in `GET /api/alerts` for a session or Basic auth,
  and in the dashboard.

`webhook_format = "json"` (default) sends `Content-Type: application/json`:

```json
{"type":"n5-fangov","kind":"stall","severity":"warning","hostname":"n5host",
 "title":"n5-fangov stall on n5host","message":"<alert text>","ts":1789500000}
```

`title` and `message` are what Gotify and Home Assistant read; ntfy shows the JSON text
as the message and takes the title from the header. `webhook_format = "text"` sends
`Content-Type: text/plain; charset=utf-8` with the alert text as the body.

A test alert as a receiver sees it (format `json`): headers `User-Agent:
n5-fangov/<version>`, `X-N5-Fangov-Kind: test`, `Title: n5-fangov test on n5host`, body

```json
{"type":"n5-fangov","kind":"test","severity":"warning","hostname":"n5host","title":"n5-fangov test on n5host","message":"test alert from n5-fangov <version> on n5host at 2026-09-18 02:04:34 - delivery works if you can read this.","ts":1789689874}
```

To try the transport without a real receiver, any HTTP listener on the LAN that answers
`200` will do (a few lines of python3 `http.server` with a `do_POST` that prints headers
and body), pointed at with `webhook_url = "http://192.0.2.50:8080/"`.

| Receiver | `webhook_url` | `webhook_format` | Notes |
|---|---|---|---|
| ntfy | `https://ntfy.example.test/n5` | `text` | topic in the path; the `Title` header becomes the notification title. No `Authorization` header is sent and userinfo is refused; a protected topic takes its access token as the `auth` query parameter (ntfy docs, *Authentication → Query param*), redacted in logs like the Gotify key |
| Gotify | `https://gotify.example.test/message?token=<app token>` | `json` | the app token is the query: redacted in logs, status and for token callers |
| Home Assistant | `http://ha.example.test:8123/api/webhook/<id>` | `json` | webhook trigger in an automation; the payload is `trigger.json` (`.kind`, `.title`, `.message`). Use a long random `<id>`; HA webhooks carry no other auth |

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

*Save* in Settings → Alert transport writes the four `[alert]` keys in place and applies
them at once; `PUT /api/alerts` answers 400 for an invalid URL, format or `mail_to`, and
for `webhook` without a URL. Switching the transport to anything but `webhook`
**clears `webhook_url`** from the file, so a receiver key does not linger; enter it
again when you come back (a settings export made before the switch still carries it).
On a PVE host `transport = "auto"` plus a matcher ([The PVE side](#the-pve-side)) keeps
the routing in one place; the PVE stack has its own webhook and Gotify targets.

## Alert kinds

| Kind | Trigger | Reaction | Cooldown |
|---|---|---|---|
| `sensor` | channel sensor unresolved, unreadable, implausible or frozen | that channel at its safe duty, mode `sensor-error`; the others keep regulating | `alert_cooldown`, sent on the transition only |
| `stall` | 0 RPM at duty ≥ `stall_min_duty` for `stall_cycles` | channel 255 until RPM is back for 3 cycles | `alert_cooldown` |
| `temp` | critical temperature reached | 255 immediately, also under a manual override | `alert_cooldown` |
| `ceiling` | a part of the channel's sensor at/above its kind's ceiling (HDD 65 / SSD 85 / CPU 100 °C, or a lower `[[channel]] ceiling`) | 255, mode `critical`, until every part is 3 °C below its ceiling; the message names the part, reading, ceiling and the configured critical | `alert_cooldown`, sent on the transition only; per kind, so a second channel entering its episode inside the cooldown is a log line only |
| `emergency` | a channel stayed at its ceiling for `emergency_cycles` cycles with a stalled fan, or 3 × as long regardless, and `emergency = true` | the hook `/etc/n5-fangov/emergency.sh` ran once for this episode; the message carries the reason and its exit status. Never raised with `emergency = false` or a hook the daemon refused (a log line) | `alert_cooldown` per kind; the hook itself runs for every channel |
| `write` | pwm write or read-back failed in 2 consecutive cycles | every channel 255 (failsafe) | `alert_cooldown` |
| `config` | config file has problems | built-in defaults for those values, daemon runs | 30 min (start alert) |
| `config-channels` | channel set corrected (N5 Pro channel added, forced stop duty, pwm the device lacks) | corrected set in effect | `alert_cooldown` |
| `device` | fan controller unreachable for 6 cycles (driver reload) | daemon exits, systemd restarts it with a fresh detection | `alert_cooldown` |
| `profile` | no fan controller detected at start (sent by `check` in `ExecStartPre`, by `serve` outside the unit) | no regulation, fans stay with EC/BIOS | 30 min (start alert) |
| `driver` | on a board the driver writes on (N5 PRO/F8NAA, N5/F8NAA), the driver is loaded read-only (pwm nodes hidden; the message names the reason from `/sys/module/minisforum_n5_it5571/parameters`) or missing (module not installed, not built for this kernel, or not loaded). Sent by `check` in `ExecStartPre` (under the unit `serve` never starts then), by `serve` outside the unit | no regulation, fans stay with the EC; the daemon does not fall back to monitoring | 30 min (start alert, shared by `check` and `serve`) |
| `start` | controller could not start, or the daemon started without its pwm writer lock (lock error other than a running `validate`/`test`) | see message; without the lock regulation runs | 30 min (start alert) |
| `web` | TLS set-up or listener failed | dashboard disabled, regulation continues | 30 min (start alert) |
| `tls` | `tls = "file"` pair unreadable | automatic certificate served, mode `auto (fallback from file)` | 30 min (start alert) |
| `kernel` | DKMS module missing for a bootable kernel, or its version differs from the installed package (`check --after-update`) | printed on the apt output; nothing changes until the reboot | 30 min (own stamp) |
| `restart` / `failed` | unit failed and came back / stayed down (onfailure unit) | — | 30 min (own stamp) |
| `schedule` | a scheduled preset switch failed: preset missing or invalid, write or reload error ([Schedules](06-configuration.md#schedules)) | previous curves stay; retried at the next transition | 30 min (own stamp) |
| `test` | *Send test alert*, `n5-fangov alerts test` | — | none; one at a time, 20 s bound |

`alert_cooldown` is `[daemon] alert_cooldown` (`60s`..`24h`, default `30m`) per alert
kind. Start alerts are cooled 30 minutes regardless; `kernel`, `restart`, `failed` and
`schedule` are raised outside the controller and keep their own 30-minute stamps
(`alert.<kind>` under `/run/n5-fangov/`). `restart` and `failed` come from
`n5-fangov-onfailure.service`: when the main unit fails, it reads
`Result`/`ExecMainStatus` and the status of the `ExecStartPre` (`check`) and
`ExecStopPost` commands, waits 8 s and reports `restart` (daemon back) or `failed`
(still down) with the real cause: a failed `check` is named as `start check failed:
ExecStartPre … (exited with code N)`, not as an exit of the daemon.

### Stall alarm chain, simulated sensor

TESTSPEC 0.4 §5a (#26): a hardware stall cannot be produced on a closed, running host
(`N/A`, "not testable without intervention"). What can be tested is the chain
detection → 255 → notification against a simulated sensor:
`TestStallAlarmChainSimulated` (`cmd/n5-fangov/stall_chain_test.go`, part of `go test`)
runs the real daemon against a fake sysfs tree (`N5FANGOV_SYSFS`) with `fan2_input` = 0
at a duty ≥ `stall_min_duty`; it passes when the daemon writes 255 to the fake `pwm2`
after `stall_cycles` cycles and the `stall` alert arrives at the configured webhook. By
hand, with your own `[alert]` path: copy `testdata/sysfs/n5pro` to a temporary
directory, set its `class/hwmon/hwmon14/fan2_input` to `0`, and start
`N5FANGOV_SYSFS=<copy> n5-fangov serve --config <copy of your config> --run-dir <tmp>
--state-dir <tmp>` with `[web] listen` on a free port; the running daemon is not
touched (own run dir, own writer lock). Report the result as "alarm chain, simulated
sensor" — **it is not a hardware result**: it says nothing about whether the EC reports
0 rpm for a stopped fan or which HDD fan carries fan3.

## The Alerts page and the transport section

The **Alerts page** shows the effective transport, which tools the box has, the
cooldown, every alert kind with its last delivery, and the recent alerts (the last 50,
kept in `/var/lib/n5-fangov/alerts.json`). The transport form and the PVE template
card are **Settings → Alert transport** ([Dashboard](04-dashboard.md#alerts)).

- **Save**: transport and its fields, written to the config file in place and
  hot-applied. A `PUT /api/config`, a settings import or a preset apply re-applies
  whatever `[alert]` the written file contains.
- **Send test alert** (both pages): kind `test`, no cooldown, through the real
  transport, sent by the daemon from inside its sandbox. The response carries the
  delivery error when perl, mail or the webhook receiver fail. One test at a time (a
  second click answers `409 test in progress`), bounded to 20 s. What arrives:

  ```
  test alert from n5-fangov <version> on n5host at 2026-09-18 02:01:54 - delivery works if you can read this.
  ```

  Alert texts are plain ASCII.
- **Install / Update template**: below.

### Acknowledging

Open alerts stand at the top of *Recent alerts*; **Ack** acknowledges one, **Acknowledge
all** every open one up to the newest the page shows (one that arrives meanwhile stays
open). Acknowledged alerts fold into *N acknowledged* with who did it (`user:<name>`,
`token:<name>`, `cli`). A new alert is always open, also of a kind acknowledged before.
The Alerts entry in the navigation shows the number of open alerts. Acknowledging
changes the display only: delivery, cooldown and failsafe do not look at it. The state
is kept in `alerts.json` and survives a restart; an alert leaves the list after 50 newer
ones as before. API: `POST /api/alerts/ack` (scope `control`).

## The PVE template

The two template files (`n5-fangov-subject.txt.hbs`, `n5-fangov-body.txt.hbs`) are
embedded in the binary and installed to `/etc/pve/notification-templates/default/` by
`install.sh`, the deb postinst, *Install template* in Settings → Alert transport or
`n5-fangov alerts template`. The button is disabled with the reason when the directory
is missing or not writable: the daemon's sandbox may write *into* that directory but
cannot create it, so on a fresh box `install.sh` or `n5-fangov alerts template` (root,
outside the sandbox) create it. *Current* compares the installed files with the
embedded ones. The writable probe (a temp file created and removed in the directory)
runs at most every 10 minutes and right after *Install* or *Save*.

## The PVE side

Alerts arrive as severity *warning* with the fields `type = n5-fangov`, `hostname` and
`kind = <alert kind>`. Without a matcher they follow the default matcher (mail to
root). To route them: *Datacenter → Notifications → Notification Matchers → Add*, match
field `type` = `n5-fangov` (or `kind` = `stall`, `temp`, …) and pick the target (SMTP,
Gotify, webhook). The template gives the mail its subject `[<host>] n5-fangov: <kind>`
and body.

## From the shell

```
n5-fangov alerts status          transport, tools, template, last alert per kind, recent alerts
n5-fangov alerts test            send a test alert now
n5-fangov alerts template        install/update the PVE template pair
n5-fangov alerts ack 41 42       acknowledge alerts by id (status lists the ids)
n5-fangov alerts ack --all       acknowledge every open alert
```

`status` shows the configured and the effective transport, `mail_to`, and for the
webhook the URL (query redacted) and the format; the recent alerts with id and `open` or
who acknowledged them. `ack` goes through the socket when the daemon runs and writes
`alerts.json` itself when it does not. `status` and `test` go through the
daemon's socket when it runs (the test then shows in the dashboard), otherwise they
work on the config file. `template` asks the daemon first and writes the files itself
when that fails for anything but "not a PVE host". A test sent with the daemon stopped
runs outside the sandbox and proves nothing about delivery from the daemon.
