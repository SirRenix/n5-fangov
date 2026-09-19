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
- [Ceilings and the emergency action](#ceilings-and-the-emergency-action)
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
(the account and certificate sections of the Settings page edit the file and apply their keys live).

The daemon writes the file itself in some cases — curve editor, preset apply (by hand or
by a schedule), the schedule editor, import, the account, certificate and alert-transport sections of the Settings page — always in
place: comments and every other key stay. The curve editor and a preset apply replace
the `[[channel]]` tables as a block (comments *inside* a channel table are lost;
everything else — comments elsewhere, `[[schedule]]`, `[alert]`, `[web]` — stays
byte-identical); the schedule editor replaces the `[[schedule]]` tables the same way; the other writers change single keys. While `[[schedule]]` entries
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
| `emergency` | `true` runs the emergency hook `/etc/n5-fangov/emergency.sh` once per ceiling episode ([below](#ceilings-and-the-emergency-action)); the path is fixed, no key names a command | `false` | reload |
| `emergency_cycles` | 1..60 consecutive cycles at the ceiling before the action (× 3 when the fan still spins) | 6 | reload |
| `profile` | `auto` \| `n5pro` \| `nct67xx` \| `it87xx` \| `monitor` | `auto` | restart |
| `[web] listen` | `host:port` | `127.0.0.1:8010` | restart |
| `auth` | `none` \| `basic` (needs `user` + `password_hash`, otherwise `none` **and** loopback) | `none` | restart |
| `user` | web user (`[A-Za-z0-9_.-]{1,32}` when set through the API) | `""` | restart / live via Settings → Account |
| `password_hash` | `pbkdf2$<iter>$<salt>$<key>` from `passwd`/`setup`; legacy 64-hex sha256 of `user:password` still accepted ([hashes](08-https-security.md#password-hashes)) | `""` | restart / live via Settings → Account |
| `allowed_hosts` | extra `Host` header values (reverse-proxy names); `"*"` disables the check | `[]` | restart |
| `tls` | `auto` \| `off` \| `file`; `off` on a non-loopback listen is replaced by `auto` ([HTTPS](08-https-security.md#modes-and-actions)) | `off` loopback / `auto` else | restart / live via Settings → Certificate |
| `cert_file`, `key_file` | PEM paths for `tls = "file"` | `""` | restart / live via Settings → Certificate |
| `behind_tls_proxy` | `true` when a reverse proxy terminates TLS in front of a plain listener: the session cookie gets `Secure` | `false` | restart |
| `[log] file` | absolute clean path under `/var/log/`, `""` = journal only; not a symlink, device or directory ([Logs](10-troubleshooting.md#logs)) | `/var/log/n5-fangov/n5-fangov.log` | restart |
| `max_size_mb` | 1..100 | 5 | restart |
| `max_files` | 1..20 rotated files `.1`..`.N` | 5 | restart |
| `[alert] transport` | `auto` \| `pve` \| `mail` \| `webhook` \| `log` \| `off` ([Alerts](07-alerts.md#transports)) | `auto` | reload |
| `mail_to` | local user or address, no spaces or quotes, never starts with `-` | `root` | reload |
| `webhook_url` | absolute `http`/`https` URL with a host, no userinfo, ≤ 2048 characters; required for `transport = "webhook"` — missing or invalid → transport `auto` with a warning ([Webhook](07-alerts.md#webhook)) | `""` | reload |
| `webhook_format` | `json` \| `text` | `json` | reload |
| `[dashboard] sensors` | 0..8 distinct sensor ids charted on the Overview ([Extra sensors](04-dashboard.md#overview)) | `[]` | reload |
| `[[channel]] name` | unique, `[a-z0-9_]{1,32}`; else the channel is dropped | — | restart when the set changes |
| `pwm` | 1..8, `pwmN` of the profile's hwmon device, unique | — | restart when the set changes |
| `sensor` | one [sensor id](#sensor-ids) as a string, **or an array of 1..4 ids** (`["drivetemp:max", "ec:hdd"]` = maximum of the parts; each non-empty, distinct, without `,`) — stored and reported as the composite id `a,b` (comma-joined, no spaces); a string with commas is accepted in the same form | — | reload |
| `curve` | 2..8 `[temp_c, duty]` points, temps −20..120 ascending, duties 0..255 not descending; else `[[45,85],[80,255]]` | — | reload |
| `critical` | last curve temperature + 1 .. 150 → 255 immediately | last + 10 | reload |
| `stop` | `"auto"` (back to the chip) or a fixed duty 60..255 (`drivetemp:max` channels — and every composite that contains it — default to `140`; N5 Pro pwm3 never `auto`) | by sensor | reload |
| `hysteresis` | 0..10 °C, integer; 0 = off ([below](#hysteresis-and-minimum-on-time)) | 0 | reload |
| `min_on` | duration `0s`..`1h`; `0s` = off ([below](#hysteresis-and-minimum-on-time)) | `0s` | reload |
| `ceiling` | 30..the sensor kind's built-in ceiling — may only **lower** it ([below](#ceilings-and-the-emergency-action)); above it → warning, built-in value; missing or `0` = built-in; on a composite it lowers every part | built-in | reload |
| `[[schedule]] preset` | preset name `[a-z0-9_-]{1,64}` (existence is checked when the switch happens, not when the file is read) | — | reload |
| `from`, `to` | `HH:MM` (24 h, local time of the host), both or neither; `from == to` → entry dropped; `to < from` = the window crosses midnight | — | reload |
| `days` | subset of `mon tue wed thu fri sat sun` (lower-cased, distinct); the day of a window is the day `from` falls in; missing = every day | all | reload |

Changing `[web]` or `[log]` settings takes a restart (except user/password through the
account forms and the tls keys through the Certificate section); `PUT /api/config`
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

## Ceilings and the emergency action

Since 0.4.1 every channel has a **ceiling** below `critical` that no configuration can
raise — the floor of the guard chain. Motivation: with the admin password (or a typo)
every curve can be set to duty 0 and `critical` to 150; the CPU throttles itself, the
disks do not.

- The built-in ceiling follows the sensor kind: **CPU 100 °C** (`k10temp`, `coretemp`,
  `ec:cpu`, `hwmon:…cpu…`), **SSD 85 °C** (`nvme:max`, `disk:<dev>` of an NVMe or
  non-rotational device), **HDD 65 °C** (`drivetemp:max`, `disk:<dev>` of a rotational
  device), everything else 100 °C.
- A **composite** sensor is judged **per part**: each part against its own kind's
  ceiling. `["nvme:max", "disk:sda"]` holds the NVMe to 85 and the HDD to 65 — an NVMe at
  70 °C is not an episode, the HDD at 65 is. `GET /api/state` reports the **lowest** part
  ceiling as the channel's `ceiling` (that is what the curve editor draws); the alert
  names the part (`hdd: disk:sda of nvme:max,disk:sda at 66.0C reached the ceiling 65C`).
- `[[channel]] ceiling = N` may only **lower** it (30..built-in; on a composite every
  part). A value above the built-in ceiling is a warning and the built-in one stays —
  like every other invalid channel key, a 400 for the curve editor's strict write. `GET
  /api/state` reports the effective value per channel (`ceiling`) and whether the channel
  is in the ceiling state (`ceiling_hit`); the Fans page draws it as the second dashed
  line of the curve editor. A `disk:<dev>` whose device is absent at start (SATA missing
  after a warm reboot) runs at 100 until the device is there; the daemon retries the
  sensor every cycle and takes the kind — and the 65/85 — with it, no reload needed.
- At or above the ceiling the channel goes to **255 at once**, mode `critical`, whatever
  the curve, a manual override, hysteresis, `min_on` or `critical` say — and stays
  there until the raw reading is **3 °C below** the ceiling (every part, for a composite;
  so the fan does not flap at the line). A sensor that fails **inside** an episode does
  not end it and does not drop the channel to its safe duty: it stays at 255 (mode
  `sensor-error`) until a reading ends the episode. The alert `ceiling` is raised on the
  way in ([Alerts](07-alerts.md#alert-kinds)).
- A `critical` **above** the ceiling is accepted — it is meaningless, the ceiling acts
  first. `n5-fangov check` says so with an advisory line (`channel hdd: critical 70
  above the built-in ceiling 65 — the ceiling acts first`; `configured ceiling 50` when
  it is your own `ceiling` that sits below `critical`) and still passes; the Fans page
  repeats the warning in its notice after *Apply* and does not block.

**Emergency action.** `[daemon] emergency = true` (default `false`) makes the daemon run
the **emergency hook** when a channel has stayed at its ceiling for `emergency_cycles`
consecutive cycles (default 6, 1..60) **and** the stall detection reports its fan as
stopped (`stall_cycles` zero readings at a duty it should turn at — the fan has failed;
a single 0 RPM sample never counts), or for **3 ×** `emergency_cycles` cycles regardless
of RPM — cooling is not working. The counter runs on every cycle in the ceiling state,
the hysteresis zone included: a drive at 63 °C after one reading at 65 still counts.

The hook is **one file at a fixed path**: `/etc/n5-fangov/emergency.sh`. There is no
config key and no API field that names a command or a path — the file is root's, you
install it by hand, and nothing the dashboard or a token can do creates or changes it
([What no token can do](08-https-security.md#api-tokens)). The daemon runs it only when
it is a regular file (not a symlink) owned by root, executable, and neither group- nor
world-writable (`0700`, `0750` or `0755`); anything else is one log line per episode
(`hook /etc/n5-fangov/emergency.sh refused: world-writable (mode 0777), nothing runs`)
and no run. `n5-fangov check` prints the state — `emergency hook   /etc/n5-fangov/emergency.sh (ok)`,
`(absent)` or `(refused: …)` — and warns when `emergency = true` and it is not `ok`.

The hook runs **once** per ceiling episode and re-arms only after the channel has left
the ceiling state; the run is bounded to 60 s (the hook's whole process group is
killed at the timeout, so a child it left behind dies too), the first 4 KiB of its
output go to the daemon log (`emergency[<channel>]: …`), and the alert `emergency`
carries the reason and the exit status (`hook exited 0`, the exit code, `timeout after
1m0s`). With `emergency = false` nothing runs and no `emergency` alert is raised — the
`ceiling` alert already covers the episode. The daemon adds to the environment:
`N5_CHANNEL`, `N5_SENSOR` (the channel's sensor id), `N5_PART` (the part at its
ceiling; equal to `N5_SENSOR` unless the sensor is a composite), `N5_TEMP` (that part's
reading, °C), `N5_CEILING` (that part's ceiling, °C), `N5_RPM` (`-1` without a
tachometer), `N5_CYCLES`. A hook that powers the box off ends the daemon before the
`emergency` alert can leave — the daemon's log line `running /etc/n5-fangov/emergency.sh`
and what the hook itself wrote to the journal are the lasting evidence.

`/usr/share/doc/n5-fangov/examples/emergency.example.sh` (from `deploy/`) is the
template: it logs the values through `logger -t n5-fangov-emergency`, exits with the
status of its last command (`set -eu`, no `exit 0` at the end, so a failed `logger`
shows in the alert) and carries a commented `systemctl poweroff` line. Install it:

```sh
install -m 0750 -o root -g root /usr/share/doc/n5-fangov/examples/emergency.example.sh /etc/n5-fangov/emergency.sh
n5-fangov check          # emergency hook   /etc/n5-fangov/emergency.sh (ok)
```

```toml
[daemon]
emergency = true         # the hook above
emergency_cycles = 6     # 60 s at 10 s interval with a stalled fan, 180 s without
```

The hook runs **inside the unit's sandbox** ([Hardening](08-https-security.md#hardening)):
what is known from the unit file is that `AF_UNIX` and the `@system-service` syscall
set are allowed, so `logger` reaches the journal socket and `systemctl` reaches PID 1
over its private socket; the capability set is empty, but a poweroff is carried out by
PID 1, not by the caller, and root needs no polkit for it. `ProtectHome=yes` hides
`/root` and `/home` (a script there is invisible to the daemon), the hook can write only
under the unit's `ReadWritePaths` (`/etc/n5-fangov`, `/var/lib/n5-fangov`, …), and
`TimeoutStopSec=20` ends a running hook when the unit stops. **Neither `systemctl
poweroff` nor `logger` has been executed from inside the sandbox on the reference host
as of 0.4.1-rc1** — the release-gate re-test runs the `logger` form; the poweroff form
is *to be verified on the host*: set `emergency_cycles = 1` and a test ceiling on a
channel you can heat safely (or `ceiling = 30` on the HDD channel for a moment),
uncomment the line in your copy of the hook, and expect the box to go down cleanly.
Start with the `logger` line.

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
the defaults apply; a fixed `stop` is written as a number, `stop = 140`, and a quoted `"140"` is read the same). *New preset…* on the Fans page writes one from the preset editor
(any values, nothing applied), *Apply* merges it into the config file and reloads,
*Delete* removes a user preset ([Presets](04-dashboard.md#presets)). Three N5 Pro sets are **built in** (embedded
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
state observed on the reference host is enable 2, duty 255, 0 RPM — but **the EC does
not take pwm4 back after a write** (measured 2026-09-17: after a manual duty of 100 and
`pwm4_enable = 2` the duty stayed at 100), so `auto` leaves the last written duty in
place, exactly like pwm3. For a defined state after a stop set a fixed `stop`.

```toml
[[channel]]
name = "pcie"
pwm = 4
sensor = "k10temp"
curve = [[45,85],[80,255]]
critical = 88
stop = 140            # fixed: the EC does not take pwm4 back after a write (like pwm3)
```

A changed channel set needs a restart; after that the built-in presets leave the
channel alone (merge by pwm).

## Schedules

`[[schedule]]` tables switch presets by time of day. The scheduler ticks every 30 s
and acts on **transitions only**: when the active entry changes it applies that
entry's preset through the same path as *Apply* on the Fans page (merge by pwm,
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
- The list is edited on the dashboard's [Schedules page](04-dashboard.md#schedules) or in
  this file — both write the same `[[schedule]]` tables (the editor replaces them as a
  block with `PUT /api/config?strict=1`, everything else stays). The page and
  `GET /api/schedules` show the entries, the active one, the next switch, the last switch
  with its error and the timezone the host uses ([API](12-api.md#endpoints)).

Time-of-day is the only trigger. For anything else (presence, a noise sensor, guests)
a Home Assistant automation applies a preset through the API with a `control` token
([Home Assistant](12-api.md#home-assistant)).

## Strict writes

`PUT /api/config?strict=1` rejects values the daemon would otherwise replace by defaults
with `400` and the warning list instead of accepting them silently. The curve editor and
the schedule editor use it, so an operator's curve is never swapped for the built-in
default behind their back; the preset body of `PUT /api/presets/{name}` is checked the
same way. The bounds the UI checks against come from `GET /api/version` as `limits`:
`curve_points_max`, `critical_min`, `critical_max`, `min_hdd_override` (the lowest
fixed `stop` and the manual-override floor for HDD-like channels), `hysteresis_max`,
`min_on_max_s`, `password_min`, `password_max`, `dashboard_sensors_max` and
`ceiling_min` (30) — nothing else; the temperature range, the duty scale and the name rules for users, presets and
channels are client constants that mirror the parser. A hand-edited file goes through
the lenient path: defaults plus a `config` alert.

Next: [Alerts](07-alerts.md) · [Dashboard: Fans](04-dashboard.md#fans) ·
[API and integrations](12-api.md) · [Backup and restore](09-updates.md#backup-and-restore)
