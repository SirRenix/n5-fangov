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
   - `HOST:PORT` — explicit address; non-loopback implies auth + HTTPS like `lan`.
3. An existing config is backed up (`config.toml.bak-<timestamp>`) before it is replaced.

Passwords are 8..128 characters, user names `[A-Za-z0-9_.-]{1,32}`. The hash that lands
in the file is described in [Password hashes](08-https-security.md#password-hashes).

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
`[ok]`/failure per item; it is the same check the unit runs before every start. Then open
the [dashboard](04-dashboard.md) — `http://127.0.0.1:8010` (`local`) or
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
the table is in [Configuration](06-configuration.md).

Next: [Dashboard](04-dashboard.md) · [Configuration](06-configuration.md) ·
[HTTPS and security](08-https-security.md)
