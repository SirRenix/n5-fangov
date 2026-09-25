# Setup

`n5-fangov setup` writes the config for this machine and asks three things. Run it as
root after the install.

## What it asks

```
n5-fangov setup
```

1. **Profile** — detected. On the N5 Pro you get the three channels of the built-in
   preset `n5pro-balanced` (the recommended one). On `nct67xx`/`it87xx` you get one
   conservative channel per PWM output on the CPU sensor (`k10temp`, else `coretemp`,
   else the first hwmon temperature); `monitor` writes no channels. No profile found
   means the kernel driver is not loaded or `experimental_write=1` is missing — see
   [Kernel driver](02-kernel-driver.md).
2. **Scope** — `local` or `lan`.
   - `local`: `127.0.0.1:8010`, no login, plain HTTP. Reach it with `ssh -L 8010:127.0.0.1:8010 n5host`.
   - `lan`: your LAN address, login required, HTTPS with a self-signed certificate.
3. **User and password** — password twice, no echo, 8 to 128 characters.

This is what it looks like on an N5 Pro with `lan`:

```
profile: n5pro (Minisforum N5 Pro (IT5571 EC)) at /sys/class/hwmon/hwmon10  [verified on hardware]
  channel cpu    pwm1  sensor k10temp          curve [[35 60] [60 150] [80 255]]  critical 88  stop auto
  channel ssd    pwm2  sensor nvme:max         curve [[35 74] [55 160] [68 255]]  critical 72  stop auto
  channel hdd    pwm3  sensor drivetemp:max    curve [[30 87] [42 140] [50 200] [55 255]]  critical 60  stop 140

Web UI scope:
  local  127.0.0.1:8010, no auth, plain HTTP (use ssh -L or a reverse proxy)
  lan    this machine's LAN address, basic auth, HTTPS with a self-signed certificate
scope (local|lan) [local]: lan
web user [admin]: admin
password:
repeat password:
written: /etc/n5-fangov/config.toml
```

The `hwmon10` number changes between boots. That is normal, the daemon finds the
device by name.

## Without questions

```
n5-fangov setup --yes --listen lan --user admin --password-file /root/pw
```

`--yes` needs every flag. The password comes from a file (`--password-file`) or stdin
(`--password -`), never from the command line — so it never shows up in `ps` or your
shell history. All flags: [CLI](05-cli.md).

## Running setup again

An existing config is backed up as `config.toml.bak-<timestamp>` first. Setup then
writes the profile, the channel curves and the web settings new and keeps the rest:

- `[alert]`, `[dashboard]`, `[log]`, `[[schedule]]` and the other `[daemon]` keys
- `hysteresis`, `min_on` and `ceiling` of a channel with the same name and PWM
- channels on other PWM outputs, as long as the profile stays the same
- `allowed_hosts`, `behind_tls_proxy`, `tls = "file"`

Before it asks, setup prints one `kept:` line per item, and one `not carried over:` line
per value that was invalid in the old file (typos included) — the default is written
for those:

```
kept: [alert] transport, mail_to
kept: [dashboard] sensors
kept: channel hdd: hysteresis 2, min_on 1m0s
not carried over (invalid in the old file, default written): channel.hdd.hysterese
backup: /etc/n5-fangov/config.toml.bak-20260924-221500
written: /etc/n5-fangov/config.toml
```

Curves, `critical` and `stop` are reset to the profile's. Apply your preset again
afterwards (Fans → Presets). `--fresh` keeps nothing. A config with a TOML syntax error
keeps nothing either; setup says so.

To change only the password, use `n5-fangov passwd` instead
([below](#change-user-or-password-later)).

## Check and start

```
n5-fangov check
systemctl enable --now n5-fangov
n5-fangov status
```

`check` prints one line per item and ends with `check: all good`. With `[warn]` lines it
ends with `check: passed with N warning(s)` — the daemon still starts, but read them: a
syntax error in the config, for example, starts the web UI on `127.0.0.1` without login.
The tail of a good run before the first start — `socket … unit not active` at this
point is fine:

```
[ok  ] web                    listen 192.0.2.20:8010, auth basic, tls auto
[ok  ] log                    /var/log/n5-fangov/n5-fangov.log
[ok  ] alerts                 transport auto (pve-notify)
[ok  ] socket                 unit not active, socket check skipped
[ok  ] dkms                   minisforum-n5-it5571/0.2.1, 7.0.14-17-pve, x86_64: installed
check: all good
```

`status` shows every channel with temperature, duty and mode:

```
n5-fangov: ok  profile n5pro (verified)  uptime 0m00s  unit active
hwmon: /sys/class/hwmon/hwmon10

  channel         sensor    temp  duty    %   rpm  mode
      cpu        k10temp  39.2 C    85  33%  2000  auto
      ssd       nvme:max  44.9 C    74  29%  2124  auto
      hdd  drivetemp:max  42.0 C   195  76%  2253  auto
```

Now open the [dashboard](04-dashboard.md): `http://127.0.0.1:8010` for `local`,
`https://n5host:8010` for `lan`. The browser will warn about the certificate once —
[here is how to trust it](08-https-security.md#the-certificate).

## Change user or password later

- Dashboard: Settings → *Account & sessions*.
- Shell: `n5-fangov passwd`, then `systemctl restart n5-fangov`. This is also the way
  back in if you forgot the password — root on the box is the recovery.

Scripts and Home Assistant should not use the password. Give them an
[API token](12-api.md#authentication) with the scope they need.

Every config key is explained in `/usr/share/doc/n5-fangov/config.example.toml` and in
[Configuration](06-configuration.md).
