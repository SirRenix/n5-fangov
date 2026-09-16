# Configuration

What this page covers: every key of `/etc/n5-fangov/config.toml`, the curve rules, the
preset files and the three built-in N5 Pro sets, and what "strict" means for the
editor. The annotated example is `deploy/config.example.toml` (installed as
`/usr/share/doc/n5-fangov/config.example.toml`).

- [How the file is read](#how-the-file-is-read)
- [Configuration reference](#configuration-reference)
- [Curve rules](#curve-rules)
- [Presets](#presets)
- [Strict writes](#strict-writes)

## How the file is read

Invalid values never prevent a start: each one falls back to its default with a
warning in the journal and a `config` alert (`n5-fangov check` prints them). In the
table, *reload* = applied by `PUT /api/config`, the curve editor, a preset apply or an
import without a restart; *restart* = read once at start (the account and certificate
panels edit the file and apply their keys live).

The daemon writes the file itself in some cases — curve editor, preset apply, import,
account and certificate panels, Alerts tab *Save* — always in place: comments and every
other key stay. A config file that carries a `password_hash` is written `0600`; an
existing wider mode is tightened and logged. The file is forward-compatible: a newer
daemon reads an older file, an older daemon warns about unknown keys and ignores them.

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
| `[alert] transport` | `auto` \| `pve` \| `mail` \| `log` \| `off` ([Alerts](07-alerts.md#transports)) | `auto` | reload |
| `mail_to` | local user or address, no spaces or quotes, never starts with `-` | `root` | reload |
| `[dashboard] sensors` | 0..8 sensor ids charted on the Overview ([Extra sensors](04-dashboard.md#extra-sensors)) | `[]` | reload |
| `[[channel]] name` | unique, `[a-z0-9_]{1,32}` | — | restart when the set changes |
| `pwm` | 1..8, `pwmN` of the profile's hwmon device, unique | — | restart when the set changes |
| `sensor` | `k10temp` \| `coretemp` \| `nvme:max` \| `drivetemp:max` \| `hwmon:<name>:tempN` \| `ec:<label>` | — | reload |
| `curve` | 2..8 `[temp_c, duty]` points, temps −20..120 ascending, duties 0..255 not descending; else `[[45,85],[80,255]]` | — | reload |
| `critical` | last curve temperature + 1 .. 150 → 255 immediately | last + 10 | reload |
| `stop` | `"auto"` (back to the chip) or a fixed duty 60..255 (`drivetemp:max` channels default to `140`; N5 Pro pwm3 never `auto`) | by sensor | reload |

Changing `[web]` or `[log]` settings takes a restart (except user/password through the
account forms and the tls keys through the certificate panel); `PUT /api/config`
reloads curves, sensors, `[daemon]`, `[alert]` and `[dashboard]` values. A changed
channel set or profile answers *restart required* and the daemon keeps running the old
set until then.

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

## Curve rules

- 2..8 points `[temp_c, duty]`; temperatures −20..120 and strictly ascending; duties
  0..255 and never descending. Between points the duty is interpolated linearly. A curve
  that breaks a rule is replaced by `[[45,85],[80,255]]` with a warning.
- `critical` must lie above the last curve temperature (up to 150); reaching it sets 255
  immediately, also under a manual override.
- `stop` is `"auto"` or a fixed duty 60..255 — the minimum protects disks whose
  temperature reacts minutes after the fan slows. Channels on `drivetemp:max` default to
  `140`; on the N5 Pro pwm3 is forced to a fixed value because the EC does not regulate
  it after a write ([Kernel driver](02-kernel-driver.md#why-an-out-of-tree-module)).
  On stop, every channel goes to its `stop` value; `ExecStopPost=failsafe` repeats this
  after crashes and kills.
- The daemon moves towards the target by at most `step_up` / `step_down` per cycle; the
  Overview shows the target marker while the slew is still moving.
- Sensor forms: `k10temp` (Tctl), `coretemp` (max of its inputs), `nvme:max` /
  `drivetemp:max` (hottest device of that driver; a vanished device is ignored as long
  as one remains), `hwmon:<name>:tempN` (input N of the first hwmon device with that
  name), `ec:<label>` (a temperature the profile's EC driver exports, N5 Pro:
  `temp1..4`). Readings outside −20..120 °C count as implausible.

## Presets

`/etc/n5-fangov/presets/<name>.toml` holds only `[[channel]]` tables; *Save current
curves as…* writes one, *Apply* replaces the channel set of the config file with it and
reloads (a changed channel set or profile answers "restart required"), *Delete* removes
a user preset ([Presets tab](04-dashboard.md#presets)). Three N5 Pro sets are
**built in** (embedded in the binary, listed and applicable for the `n5pro` profile
only — on another profile the API answers 404 —, never saved over or deleted — the API
answers 409). A user preset file with a built-in name is shadowed by the built-in
(logged when listing).

| Preset | cpu (`k10temp`, critical 88) | ssd (`nvme:max`, critical 72) | hdd (`drivetemp:max`, stop 140) |
|---|---|---|---|
| `n5pro-quiet` — lowest noise, HDD group settles around 45 °C | `[[30,25],[61,163],[85,255]]` | `[[35,55],[65,255]]` | `[[26,63],[55,92],[56,255]]`, critical 66 |
| `n5pro-balanced` — **recommended**: HDD group held near 40 °C, audible under load only | `[[35,60],[60,150],[80,255]]` | `[[35,74],[55,160],[68,255]]` | `[[30,87],[42,140],[50,200],[55,255]]`, critical 60 |
| `n5pro-cool` — drives first, noise second | `[[30,85],[55,170],[75,255]]` | `[[30,90],[50,180],[65,255]]` | `[[28,105],[38,150],[45,210],[50,255]]`, critical 58 |

Duty → RPM on the N5 Pro (measured): CPU 85→2000, 140→3120, 255→5073; SSD 74→2130,
255→4687; HDD 87→1237, 105→1650, 140→2250, 179→2725, 255→3540. The HDD channel keeps
`stop = 140` in every set because the EC does not regulate it after a write.

## Strict writes

`PUT /api/config?strict=1` rejects values the daemon would otherwise replace by defaults
with `400` and the warning list instead of accepting them silently. The curve editor
uses it, so an operator's curve is never swapped for the built-in default behind their
back; the validation bounds the UI checks against (curve points, temperatures,
critical, stop, HDD override minimum, password length, preset and user name rules,
dashboard sensor cap) come from `GET /api/version` as `limits`. A hand-edited file goes
through the lenient path: defaults plus a `config` alert.

Next: [Alerts](07-alerts.md) · [Dashboard: Curves](04-dashboard.md#curves) ·
[Backup and restore](09-updates.md#backup-and-restore)
