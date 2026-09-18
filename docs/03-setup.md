# Setup

What this page covers: `n5-fangov setup` — profile, dashboard scope, password rules,
the non-interactive form — the first check and start, and how to change the user or
password later.

- [What setup writes](#what-setup-writes)
- [Non-interactive](#non-interactive)
- [First check and start](#first-check-and-start)
- [Change user or password later](#change-user-or-password-later)

## What setup writes

`n5-fangov setup` writes `/etc/n5-fangov/config.toml` for this machine:

1. **Profile**: detected on the box (`n5pro` → the verified three-channel set;
   `nct67xx`/`it87xx` → one conservative channel per PWM output, sensor `k10temp` /
   `coretemp` / first hwmon temperature; `monitor` → no channels). No detected profile
   → `setup` refuses; on the N5 Pro that means the [kernel driver](02-kernel-driver.md)
   is not loaded or lacks `experimental_write=1`.
2. **Scope** of the web UI:
   - `local` — `127.0.0.1:8010`, no auth, plain HTTP. Reach it with
     `ssh -L 8010:127.0.0.1:8010 n5host` or put a reverse proxy in front
     ([behind a reverse proxy](08-https-security.md#behind-a-reverse-proxy)).
   - `lan` — the primary LAN address, **basic auth** (user + password asked twice
     without echo) and **HTTPS** with an automatically created self-signed certificate.
   - `HOST:PORT` — explicit address; non-loopback implies auth + HTTPS like `lan`. The
     dialog asks only `scope (local|lan)`; an explicit address exists as the flag
     `--listen HOST:PORT` ([Non-interactive](#non-interactive)).
3. An existing config is backed up (`config.toml.bak-<timestamp>`) before it is replaced.

Passwords are 8..128 characters, user names `[A-Za-z0-9_.-]{1,32}`. The hash that lands
in the file is described in [Password hashes](08-https-security.md#password-hashes).

The dialog on an N5 Pro, scope `lan`:

```
profile: n5pro (Minisforum N5 Pro (IT5571 EC)) at /sys/class/hwmon/hwmon10  [verified on hardware]
  channel cpu    pwm1  sensor k10temp          curve [[45 85] [80 255]]  critical 88  stop auto
  channel ssd    pwm2  sensor nvme:max         curve [[48 74] [65 255]]  critical 72  stop auto
  channel hdd    pwm3  sensor drivetemp:max    curve [[36 105] [46 255]]  critical 56  stop 140

Web UI scope:
  local  127.0.0.1:8010, no auth, plain HTTP (use ssh -L or a reverse proxy)
  lan    this machine's LAN address, basic auth, HTTPS with a self-signed certificate
scope (local|lan) [local]: lan
web user [admin]: admin
password:
repeat password:
written: /etc/n5-fangov/config.toml
```

The channel lines are the profile's default curves, and that is what `setup` writes.
The three built-in sets (`n5pro-quiet`, `n5pro-balanced`, `n5pro-cool`) are applied
afterwards from the [Presets tab](04-dashboard.md#presets); their values are in
[Presets](06-configuration.md#presets). The hwmon number (`hwmon10` here) is not stable
across boots ([Verify](02-kernel-driver.md#verify)).

## Non-interactive

```
n5-fangov setup --yes --listen lan --user admin --password-file /root/pw
```

`--yes` asks nothing and needs every flag. The password comes from `--password-file F`
(first line, mode 0600 recommended) or `--password -` (one line on stdin); there is no
literal form, so it never lands in `ps` or the shell history. `--profile` overrides the
detection (`auto|n5pro|nct67xx|it87xx|monitor`); all flags in the [CLI reference](05-cli.md).

## First check and start

```
n5-fangov check                  # what serve will do with this config
systemctl enable --now n5-fangov
n5-fangov status
```

`check` walks config, profile, pwm writability, sensors, tls, log and dkms and prints
`[ok]`/failure per item; it is the same check the unit runs before every start. The
tail of a good run before the first start (`socket` is skipped while the unit is not
active — that is not a failure):

```
[ok  ] web                    listen 192.0.2.20:8010, auth basic, tls auto
[ok  ] log                    /var/log/n5-fangov/n5-fangov.log
[ok  ] alerts                 transport auto (pve-notify)
[ok  ] socket                 unit not active, socket check skipped
[ok  ] dkms                   minisforum-n5-it5571/0.2.0, 7.0.14-17-pve, x86_64: installed
check: all good
```

`status` right after the start:

```
n5-fangov: ok  profile n5pro (verified)  uptime 0m00s  unit active
hwmon: /sys/class/hwmon/hwmon10

  channel         sensor    temp  duty    %   rpm  mode
      cpu        k10temp  39.2 C    85  33%  2000  auto
      ssd       nvme:max  44.9 C    74  29%  2124  auto
      hdd  drivetemp:max  42.0 C   195  76%  2253  auto
```

Then open the [dashboard](04-dashboard.md) — `http://127.0.0.1:8010` (`local`) or
`https://n5host:8010` (`lan`); the browser warning for the self-signed certificate is
handled in [HTTPS](08-https-security.md#the-certificate).

## Change user or password later

- In the dashboard: settings gear → *Account…* → *Change password…* / *Change user…*
  ([Account](04-dashboard.md#account)); takes effect at once.
- From the shell: `n5-fangov passwd` (same `--user` / `--password-file` / `--password -`
  flags as `setup`) edits the file in place; `systemctl restart n5-fangov` applies it.
  This is also the way back in for a forgotten password. A change made outside the
  dashboard drops every persisted browser session at the next start.

The reference with every key explained is `/usr/share/doc/n5-fangov/config.example.toml`;
the table is in [Configuration](06-configuration.md). Scripts and Home Assistant do
not get the password: create an API token with the scope they need
([API and integrations](12-api.md#authentication)).

Next: [Dashboard](04-dashboard.md) · [Configuration](06-configuration.md) ·
[HTTPS and security](08-https-security.md)
