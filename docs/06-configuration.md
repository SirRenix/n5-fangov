# Configuration

What this page covers: every key of `/etc/n5-fangov/config.toml`, the curve rules and
the curve post-processing (hysteresis, minimum on-time), the sensor ids, the preset
files and the three built-in N5 Pro sets, the optional pwm4 channel, preset schedules,
and what "strict" means for the editor. The annotated example is
`deploy/config.example.toml` (installed as `/usr/share/doc/n5-fangov/config.example.toml`).

- [How the file is read](#how-the-file-is-read)
- [Configuration reference](#configuration-reference)
- [Curve rules](#curve-rules)
- [Hysteresis and minimum on-time](#hysteresis-and-minimum-on-time)
- [Sensor ids](#sensor-ids)
- [Presets](#presets)
- [pwm4 on the N5 Pro](#pwm4-on-the-n5-pro)
- [Schedules](#schedules)
- [Strict writes](#strict-writes)

## How the file is read

Invalid values never prevent a start: each one falls back to its default with a
warning in the journal and a `config` alert (`n5-fangov check` prints them). In the
table, *reload* = applied by `PUT /api/config`, the curve editor, a preset apply, a
scheduled preset switch or an import without a restart; *restart* = read once at start
(the account and certificate panels edit the file and apply their keys live).

The daemon writes the file itself in some cases — curve editor, preset apply (by hand or
by a schedule), import, account and certificate panels, Alerts tab *Save* — always in
place: comments and every other key stay. The curve editor and a preset apply replace
the `[[channel]]` tables as a block (comments *inside* a channel table are lost;
everything else — comments elsewhere, `[[schedule]]`, `[alert]`, `[web]` — stays
byte-identical); the other writers change single keys. While `[[schedule]]` entries
exist the channel tables are machine-managed: the scheduler applies the active entry
once after every daemon start (a fallback counts), so a hand edit inside a window
lasts until the next restart or transition. A config file that carries a `password_hash`
is written `0600`; an existing wider mode is tightened and logged. The file is
forward-compatible: a newer daemon reads an older file, an older daemon warns about
unknown keys and ignores them.

## Configuration reference

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
| `password_hash` | `pbkdf2$<iter>$<salt>$<key>` from `passwd`/`setup`; legacy 64-hex sha256 of `user:password` still accepted ([hashes](08-https-security.md#password-hashes)) | `""` | restart / live via Account |
| `allowed_hosts` | extra `Host` header values (reverse-proxy names); `"*"` disables the check | `[]` | restart |
| `tls` | `auto` \| `off` \| `file`; `off` on a non-loopback listen is replaced by `auto` ([HTTPS](08-https-security.md#modes-and-actions)) | `off` loopback / `auto` else | restart / live via the certificate panel |
| `cert_file`, `key_file` | PEM paths for `tls = "file"` | `""` | restart / live via the certificate panel |
| `behind_tls_proxy` | `true` when a reverse proxy terminates TLS in front of a plain listener: the session cookie gets `Secure` | `false` | restart |
| `[log] file` | absolute clean path under `/var/log/`, `""` = journal only; not a symlink, device or directory ([Logs](10-troubleshooting.md#logs)) | `/var/log/n5-fangov/n5-fangov.log` | restart |
| `max_size_mb` | 1..100 | 5 | restart |
| `max_files` | 1..20 rotated files `.1`..`.N` | 5 | restart |
| `[alert] transport` | `auto` \| `pve` \| `mail` \| `webhook` \| `log` \| `off` ([Alerts](07-alerts.md#transports)) | `auto` | reload |
| `mail_to` | local user or address, no spaces or quotes, never starts with `-` | `root` | reload |
| `webhook_url` | absolute `http`/`https` URL with a host, no userinfo, ≤ 2048 characters; required for `transport = "webhook"` — missing or invalid → transport `auto` with a warning ([Webhook](07-alerts.md#webhook)) | `""` | reload |
| `webhook_format` | `json` \| `text` | `json` | reload |
| `[dashboard] sensors` | 0..8 distinct sensor ids charted on the Overview ([Extra sensors](04-dashboard.md#extra-sensors)) | `[]` | reload |
| `[[channel]] name` | unique, `[a-z0-9_]{1,32}`; else the channel is dropped | — | restart when the set changes |
| `pwm` | 1..8, `pwmN` of the profile's hwmon device, unique | — | restart when the set changes |
| `sensor` | one [sensor id](#sensor-ids) as a string, **or an array of 1..4 ids** (`["drivetemp:max", "ec:hdd"]` = maximum of the parts; each non-empty, distinct, without `,`) — stored and reported as the composite id `a,b` (comma-joined, no spaces); a string with commas is accepted in the same form | — | reload |
| `curve` | 2..8 `[temp_c, duty]` points, temps −20..120 ascending, duties 0..255 not descending; else `[[45,85],[80,255]]` | — | reload |
| `critical` | last curve temperature + 1 .. 150 → 255 immediately | last + 10 | reload |
| `stop` | `"auto"` (back to the chip) or a fixed duty 60..255 (`drivetemp:max` channels — and every composite that contains it — default to `140`; N5 Pro pwm3 never `auto`) | by sensor | reload |
| `hysteresis` | 0..10 °C, integer; 0 = off ([below](#hysteresis-and-minimum-on-time)) | 0 | reload |
| `min_on` | duration `0s`..`1h`; `0s` = off ([below](#hysteresis-and-minimum-on-time)) | `0s` | reload |
| `[[schedule]] preset` | preset name `[a-z0-9_-]{1,64}` (existence is checked when the switch happens, not when the file is read) | — | reload |
| `from`, `to` | `HH:MM` (24 h, local time of the host), both or neither; `from == to` → entry dropped; `to < from` = the window crosses midnight | — | reload |
| `days` | subset of `mon tue wed thu fri sat sun` (lower-cased, distinct); the day of a window is the day `from` falls in; missing = every day | all | reload |

Changing `[web]` or `[log]` settings takes a restart (except user/password through the
account forms and the tls keys through the certificate panel); `PUT /api/config`
reloads curves, sensors, `[daemon]`, `[alert]`, `[dashboard]` values and the
`[[schedule]]` list. A changed channel set or profile answers *restart required* and
the daemon keeps running the old set until then.

Example channel:

```toml
[[channel]]
name = "hdd"
pwm = 3
sensor = "drivetemp:max"
curve = [[36,105],[46,255]]
critical = 56
stop = 140            # fixed stop duty: the EC won't regulate this channel after a write
hysteresis = 2        # the curve follows the reading in 2 °C steps
min_on = "60s"        # a rise in the curve target is held for at least a minute
```

## Curve rules

- 2..8 points `[temp_c, duty]`; temperatures −20..120 and strictly ascending; duties
  0..255 and never descending. Between points the duty is interpolated linearly. A curve
  that breaks a rule is replaced by `[[45,85],[80,255]]` with a warning.
- `critical` must lie above the last curve temperature (up to 150); reaching it sets 255
  immediately, also under a manual override. Critical is judged on the **raw** reading,
  never on the hysteresis-held one.
- `stop` is `"auto"` or a fixed duty 60..255 — the minimum protects disks whose
  temperature reacts minutes after the fan slows. Channels on `drivetemp:max` (alone or
  as part of a composite) default to `140`; on the N5 Pro pwm3 is forced to a fixed
  value because the EC does not regulate it after a write
  ([Kernel driver](02-kernel-driver.md#why-an-out-of-tree-module)). On stop, every
  channel goes to its `stop` value; `ExecStopPost=failsafe` repeats this after crashes
  and kills.
- The daemon moves towards the target by at most `step_up` / `step_down` per cycle; the
  Overview shows the target marker while the slew is still moving.
- Readings outside −20..120 °C count as implausible; the channel then sits at its safe
  duty (fixed stop duty, else 255) in mode `sensor-error`.

## Hysteresis and minimum on-time

Both keys post-process the **curve output only**. Override, critical, stall, failsafe,
slew and the safe duty behave exactly as without them. Order per cycle: raw reading →
held temperature → curve → `min_on` → override → critical → stall.

- **`hysteresis = N`** (°C, 0..10): the curve is evaluated at a *held temperature*, not
  at the raw reading. The held value follows the reading only when the reading differs
  from it by at least N °C; N = 0 means held = raw every cycle. The held value starts
  with the first good reading and is reset by a sensor error (the next good reading is
  taken as is) and by a change of the channel's sensor id. Critical still uses the raw
  reading. The Overview shows the held value next to the temperature when the two
  differ (`held_temp` in `GET /api/state`).
- **`min_on = D`** (`0s`..`1h`): when the curve target (after hysteresis) rises above
  the previous cycle's target, the higher value is *held* for D — until then the curve
  target is the maximum of the live curve value and the held one; a further rise
  replaces the held value and restarts the timer. The hold is cleared by a sensor
  error, by a manual override (an override replaces the target anyway) and by
  `min_on = "0s"`. The Overview shows a `hold` badge with the remaining time
  (`hold_until` in `GET /api/state`).
- Both are per channel and reload-safe: a reduced `hysteresis` takes effect on the next
  reading, a reduced `min_on` shortens a running hold to the new duration (never below
  now).

Typical use: a drive fan that toggles between two curve points at 44/45 °C every few
cycles — `hysteresis = 2` makes the curve step only at ±2 °C, `min_on = "60s"` keeps a
step-up for a minute so the disks actually cool before the fan drops again.

## Sensor ids

One id per channel, or 1..4 of them as an array (maximum of the parts). Every id is
also valid in `[dashboard] sensors`; `GET /api/sensors` and the Sensors card list what
the box has, with live readings.

| Id | Reads |
|---|---|
| `k10temp` | AMD Tctl (`temp1` of `k10temp`) |
| `coretemp` | Intel: maximum of its `tempN` inputs |
| `nvme:max` | hottest NVMe device; a vanished device is ignored as long as one remains |
| `drivetemp:max` | hottest SATA/SAS drive (`drivetemp`); same rule |
| `disk:<dev>` | **one block device** — `disk:sda`, `disk:nvme1n1`: `<dev>` is `[a-z0-9]{1,32}` under `/sys/block/`, the reading is `temp1_input` of the device's hwmon (`/sys/block/<dev>/device/hwmon/hwmon*/` for SATA/SAS, `/sys/block/<dev>/device/hwmon*/` for an NVMe controller). No hwmon → error. The catalogue names them `<model> (<dev>, <hwmon name>)` and tags them `ssd` or `hdd`; virtual devices (`dm-*`, `loop*`, `zd*`, `md*`, `sr*`) are skipped |
| `hwmon:<name>:tempN` | input N of the first hwmon device with that name (`hwmon:amdgpu:temp1`) |
| `ec:<label>` | a temperature the profile's EC driver exports (N5 Pro: `temp1..4` by label, e.g. `ec:system`) |
| `a,b[,c[,d]]` | **composite**: 2..4 of the above, no nesting, comma-joined without spaces = maximum of the readable parts. In the config write it as an array (`sensor = ["drivetemp:max", "ec:hdd"]`); the daemon, `GET /api/config` and `GET /api/state` report the canonical `drivetemp:max,ec:hdd`. A part whose device is absent right now is skipped (at least one must resolve, else the error of the first part); the daemon re-resolves every channel sensor every 60 cycles, so a device that appears later is picked up then |

Values are read in millidegrees; the plausible range is −20..120 °C. With several
disks of one kind, `disk:<dev>` ids chart each one on the Overview; a channel that
should follow the hottest of a mixed group is a composite (`["nvme:max", "disk:sda"]`).

## Presets

`/etc/n5-fangov/presets/<name>.toml` holds only `[[channel]]` tables — every channel
key including `hysteresis` and `min_on` (a preset written by an earlier release lacks them:
the defaults apply). *Save current curves as…* writes one, *Apply* merges it into the
config file and reloads, *Delete* removes a user preset
([Presets tab](04-dashboard.md#presets)). Three N5 Pro sets are **built in** (embedded
in the binary, listed and applicable for the `n5pro` profile only — on another profile
the API answers 404 —, never saved over or deleted — the API answers 409). A user
preset file with a built-in name is shadowed by the built-in (logged when listing).

**Apply merges by pwm.** A channel of the preset replaces the config channel with the
same `pwm` (the config channel's `name` is kept when the preset uses another name for
that pwm; `hysteresis` and `min_on` of the config channel are kept when the preset does
not set them); config channels the preset does not name are kept unchanged. The apply
rewrites the `[[channel]]` tables only — comments inside them are lost, the rest of the
file stays byte-identical ([How the file is read](#how-the-file-is-read)). So an optional
[pwm4 channel](#pwm4-on-the-n5-pro) survives a built-in preset, and an apply never
needs a restart for it. A preset channel whose pwm the config lacks is added — that is
the one case that still answers *restart required* (202).

| Preset | cpu (`k10temp`, critical 88) | ssd (`nvme:max`, critical 72) | hdd (`drivetemp:max`, stop 140) |
|---|---|---|---|
| `n5pro-quiet` — lowest noise, HDD group settles around 45 °C | `[[30,25],[61,163],[85,255]]` | `[[35,55],[65,255]]` | `[[26,63],[55,92],[56,255]]`, critical 66 |
| `n5pro-balanced` — **recommended**: HDD group held near 40 °C, audible under load only | `[[35,60],[60,150],[80,255]]` | `[[35,74],[55,160],[68,255]]` | `[[30,87],[42,140],[50,200],[55,255]]`, critical 60 |
| `n5pro-cool` — drives first, noise second | `[[30,85],[55,170],[75,255]]` | `[[30,90],[50,180],[65,255]]` | `[[28,105],[38,150],[45,210],[50,255]]`, critical 58 |

Duty → RPM on the N5 Pro (measured): CPU 85→2000, 140→3120, 255→5073; SSD 74→2130,
255→4687; HDD 87→1237, 105→1650, 140→2250, 179→2725, 255→3540. The HDD channel keeps
`stop = 140` in every set because the EC does not regulate it after a write. The
built-in sets do not name pwm4.

## pwm4 on the N5 Pro

The PCIe fan header (`pwm4`) has **no tachometer**. It is an optional fourth channel:
add `[[channel]] pwm = 4` with any name and sensor; the daemon neither adds nor corrects
it, `rpm` is reported as −1 (`no tach` on the Overview) and the channel never enters
the stall check. `stop = "auto"` writes `pwm4_enable = 2` like pwm1/pwm2 — the idle
state observed on the reference host is enable 2, duty 255, 0 RPM — but **whether the EC
regulates pwm4 again after a write is not measured**; for a defined state after a stop
set a fixed `stop`.

```toml
[[channel]]
name = "pcie"
pwm = 4
sensor = "k10temp"
curve = [[45,85],[80,255]]
critical = 88
stop = 140            # not measured whether the EC takes pwm4 back after a write
```

A changed channel set needs a restart; after that the built-in presets leave the
channel alone (merge by pwm).

## Schedules

`[[schedule]]` tables switch presets by time of day. The scheduler ticks every 30 s
and acts on **transitions only**: when the active entry changes it applies that
entry's preset through the same path as *Apply* on the Presets tab (merge by pwm,
config written, reload). It also applies the active entry **once after every daemon
start** — the fallback counts as active —, so with schedules configured the channel
tables are the scheduler's: a manual preset apply or a curve edit inside a window is
respected until the next transition or restart. A switch that fails
(preset missing, invalid, write or reload error) keeps the previous curves, logs, raises
the `schedule` alert (30-min cooldown) and is retried at the next transition, not every
tick.

```toml
[[schedule]]
preset = "n5pro-quiet"
from = "22:00"
to = "07:00"          # to < from: the window crosses midnight
days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]   # optional, default every day

[[schedule]]
preset = "n5pro-balanced"    # no from/to: the fallback, active whenever no window matches
```

- A window contains the moment when the host's local clock is in `[from, to)` on a
  listed day; a window across midnight belongs to the day `from` falls in and also
  matches the early hours of the next day. The first windowed entry that matches wins.
- The entry without `from`/`to` is the **fallback** — at most one (a second is dropped
  with a warning); `days` on the fallback is ignored with a warning. Without a fallback,
  leaving every window applies nothing: the curves of the last switch stay until the
  next window.
- At most 16 entries. An invalid entry is dropped with a warning, the others stay; an
  unknown key is a warning. The preset's existence is checked at the switch, not when
  the file is read — a typo shows up as a `schedule` alert at the first transition.
- The list applies on reload (`PUT /api/config`, import); a changed list that changes
  the active entry counts as a transition.
- The Presets tab's *Schedules* card and `GET /api/schedules` show the entries, the
  active one, the next switch, the last switch with its error and the timezone the
  host uses ([Dashboard](04-dashboard.md#presets), [API](12-api.md#endpoints)). The
  list is edited in the file only.

Time-of-day is the only trigger. For anything else (presence, a noise sensor, guests)
a Home Assistant automation applies a preset through the API with a `control` token
([Home Assistant](12-api.md#home-assistant)).

## Strict writes

`PUT /api/config?strict=1` rejects values the daemon would otherwise replace by defaults
with `400` and the warning list instead of accepting them silently. The curve editor
uses it, so an operator's curve is never swapped for the built-in default behind their
back. The bounds the UI checks against come from `GET /api/version` as `limits`:
`curve_points_max`, `critical_min`, `critical_max`, `min_hdd_override` (the lowest
fixed `stop` and the manual-override floor for HDD-like channels), `hysteresis_max`,
`min_on_max_s`, `password_min`, `password_max` and `dashboard_sensors_max` — nothing
else; the temperature range, the duty scale and the name rules for users, presets and
channels are client constants that mirror the parser. A hand-edited file goes through
the lenient path: defaults plus a `config` alert.

Next: [Alerts](07-alerts.md) · [Dashboard: Curves](04-dashboard.md#curves) ·
[API and integrations](12-api.md) · [Backup and restore](09-updates.md#backup-and-restore)
