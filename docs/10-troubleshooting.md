# Troubleshooting and logs

What this page covers: symptoms with their cause and the command that fixes them, what
`n5-fangov check` reports, and where the daemon logs (file, journal, rotation, export,
clear).

- [Symptoms](#symptoms)
- [What check reports](#what-check-reports)
- [Logs](#logs)

## Symptoms

| Symptom | Cause | Check / fix |
|---|---|---|
| `n5-fangov check` exits 1: `profile "…" not detected` / `setup` finds no profile | the hwmon device is not there: module not loaded, or (N5 Pro) `experimental_write=1` missing so the `pwm*` nodes are invisible. Not a cause: a different `hwmonN` than last boot — the number is not stable, the daemon finds the device by name | `n5-fangov detect`; `lsmod \| grep minisforum`; `cat /etc/modprobe.d/minisforum-n5-it5571.conf`; `modprobe minisforum_n5_it5571`; `ls /sys/class/hwmon/*/pwm1`; then `systemctl start n5-fangov` — [Kernel driver](02-kernel-driver.md#driver-options-and-autoload) |
| `check` says `dkms: module missing for <kernel>` / after a reboot the fans run the BIOS curve | DKMS did not build the module for the new kernel (headers missing, build failed) | [Rebuild the module by hand](02-kernel-driver.md#rebuild-the-module-by-hand); then `systemctl restart n5-fangov` |
| `check` exits 1: `channel <name>: … pwmN …: permission denied` / journal `permission denied` on `/sys/...` | the sandbox forbids the write: `ProtectKernelTunables` set to `yes` in a drop-in, or `ReadWritePaths` lost the hwmon path | `systemctl cat n5-fangov`; `make verify-deploy`; keep `ProtectKernelTunables=no` and `-/sys/class/hwmon -/sys/devices/platform` in `ReadWritePaths` ([Hardening](08-https-security.md#hardening)) |
| `status` shows a channel in `sensor-error` | its sensor is unresolved or unreadable (N5 Pro without SATA drives: `drivetemp:max` has no device) | expected on a box without HDDs; the channel sits at its stop duty. Otherwise `sensors`, `ls /sys/class/hwmon/*/name`, and the sensor id in the config |
| Browser shows a certificate warning | self-signed automatic certificate not yet trusted | Settings → *Certificate* → *Download .crt/.cer* → trust it per the section's *How to trust* recipe → reload; or `n5-fangov cert export` ([The certificate](08-https-security.md#the-certificate)) |
| Certificate still untrusted after importing the `.cer` on Windows | the import wizard's default *Automatically select the certificate store based on the type of certificate* put it into the wrong store; or the browser was not restarted; or it is Firefox, which keeps its own store | re-import and choose *Place all certificates in the following store* → *Trusted Root Certification Authorities* ([Windows walkthrough](08-https-security.md#windows-walkthrough)); close the browser completely (every window), reopen, reload; Firefox: Settings → Certificates → Authorities → Import ([Trust](08-https-security.md#the-certificate)) |
| Still a warning after the import, and the store already holds an entry named like the host | an **old** certificate with the same name (`CN=<host name>`) is in the store — after `uninstall.sh --purge` + reinstall, *Regenerate…* with a new key or an upload the daemon serves a different certificate under the same name; or the browser was not restarted; or Firefox (own store) | compare the fingerprint in Settings → Certificate with the store entry; delete the old entry (Windows `certlm.msc` → Trusted Root Certification Authorities → Certificates; Firefox Authorities; macOS Keychain *System*; Android Trusted credentials → User; Linux the file under `/usr/local/share/ca-certificates/` + `update-ca-certificates`), import the new file, restart the browser ([A new certificate with the same name](08-https-security.md#the-certificate)) |
| Cannot find the certificate in a store | the entry is named after the host (`n5host`), not "n5-fangov" | look for the host name under *Issued To*; the *subject* row of Settings → Certificate shows the exact name ([What the store entry is called](08-https-security.md#the-certificate)) |
| Forgot the dashboard password | no recovery link by design: root on the host is the recovery path | `n5-fangov passwd` (same `--user` / `--password-file` / `--password -` flags as `setup`), then `systemctl restart n5-fangov` ([Change user or password later](03-setup.md#change-user-or-password-later)) |
| Browser refuses the LAN name outright (no warning page) after a certificate change | HSTS: the served certificate does not cover that name any more (upload without the name, or the `tls` fallback) | open the dashboard by IP (`https://192.0.2.10:8010`), then *Back to auto* or upload a pair that covers the name; `n5-fangov cert info` |
| `421 host header not allowed` | the `Host` a reverse proxy passes is neither an IP, `localhost`, the listen host nor listed | add the public name to `[web].allowed_hosts` or let the proxy rewrite `Host` to the upstream; restart ([Behind a reverse proxy](08-https-security.md#behind-a-reverse-proxy)) |
| Journal: `auth misconfigured — web bound to loopback` | `auth = "basic"` without usable `user`/`password_hash`, or a typo in `auth` — the daemon fails closed | `n5-fangov check` prints the field; `n5-fangov passwd`; restart |
| Dashboard on the LAN answers plain HTTP or refuses to start with `tls` | non-loopback listen with `tls = "off"` is forced to `auto`; `tls = "file"` without both paths falls back | `n5-fangov check`; `n5-fangov cert info`; fix `[web]` |
| Alerts do not arrive | transport degraded (`pve` without `PVE::Notify`, `mail` without `mail(1)`), cooldown active, PVE matcher routes elsewhere, or (mail path) the sandbox blocks `postdrop` | Alerts page → *Send test alert* (reports the delivery error); `n5-fangov alerts status`; `journalctl -u n5-fangov \| grep -i alert`; [Alert delivery under the sandbox](08-https-security.md#alert-delivery-under-the-sandbox) for the `mail(1)` case |
| Toast `Session expired` / `401` while working | the cookie session ran out (12 h, 30 d with *Remember me*) or the password was changed outside the dashboard | sign in again; unsaved curve and schedule edits are restored after the sign-in |
| Notice `restart required` after Apply/Import/Preset | the channel set or the profile changed; `[web]`/`[log]` keys changed by import | `systemctl restart n5-fangov` (the daemon keeps running the old set until then) |
| Dashboard banner `Connection to the daemon lost` | daemon restarting or down; fans stay on the daemon side (failsafe on exit) | `systemctl status n5-fangov`; `journalctl -u n5-fangov -n 50` |
| `n5-fangov test` refuses: `daemon socket answers` | the daemon regulates that channel | `systemctl stop n5-fangov` first (or `--force` after stopping it yourself) |
| Unit keeps restarting, `n5-fangov-onfailure` mails `failed` | `check` fails at every start (see the first rows) or the controller cannot write | `journalctl -u n5-fangov -b`; `n5-fangov check` by hand |
| A `[[schedule]]` entry did not switch the preset | the preset name does not exist (checked at the switch, not when the file is read), the window's day or time is not what you think (host **local** time; `to < from` crosses midnight), or the switch happened and something applied another preset afterwards — the scheduler acts on transitions only | Schedules page (status card: active, next, last switch with its error, timezone; a missing preset is flagged in its row) / `GET /api/schedules`; Alerts page: kind `schedule`; `journalctl -u n5-fangov \| grep schedule:`; `timedatectl` for the host's zone ([Schedules](06-configuration.md#schedules)) |
| Script gets `401` with a token that worked | the token expired or was revoked (`web: bearer token rejected from <ip>: expired\|unknown` in the journal); or `auth` is `none`, where a Bearer header is ignored and 401 comes from somewhere else | `n5-fangov token list` (expiry column); create a new one; `GET /api/session` with the token shows `via` and `scope` ([API tokens](08-https-security.md#api-tokens)) |
| Script gets `403` | the endpoint is outside the token's scope (`required` in the answer), or it is a token on a session-only endpoint (`/api/tokens*`, `/api/account/*`), or a cookie/Basic caller without `X-N5-Fangov-Csrf: 1` | create a token with the scope the answer names; tokens and account changes go through the dashboard or Basic auth ([Scopes](12-api.md#scopes)) |
| Script gets `429` | per-token rate limit (20 req/s sustained, burst 40) or the login throttling after rejected credentials | poll less often; one `/api/state` call carries every channel |
| Webhook alerts fail (`webhook: <status or error>` on *Send test alert*) | the receiver did not answer 2xx (wrong path or token, a redirect — n5-fangov follows none), the TLS certificate is not in the system CA pool (no insecure switch), or the box has no route | use the final URL; the receiver's own log; a self-signed receiver needs its CA under `/usr/local/share/ca-certificates/` + `update-ca-certificates`, or plain `http` on the LAN; the log shows the URL without its query ([Webhook](07-alerts.md#webhook)) |
| A channel sits at 255 in mode `critical` although the reading is **below** its `critical` | the reading reached the sensor kind's built-in **ceiling** (HDD 65 / SSD 85 / CPU 100 °C, or a lower `[[channel]] ceiling`); it stays until the reading is 3 °C below | Alerts page: kind `ceiling`; `n5-fangov status` / `GET /api/state` (`ceiling`, `ceiling_hit`); `n5-fangov check` warns when `critical` is above the ceiling ([Ceilings](06-configuration.md#ceilings-and-the-emergency-action)) |
| Chart history has a gap after a restart | `history.json` is written every 10 minutes and at a clean stop — a crash, kill or watchdog restart loses up to 10 minutes; an unwritable state directory keeps the history in memory only (one journal line at start) | expected after a hard restart; otherwise `ls -l /var/lib/n5-fangov/history.json` and the journal line about the state dir |

## What check reports

`n5-fangov check` walks the same list the unit runs as `ExecStartPre` before every
start: config (invalid values that fell back to their default, the misconfigured
`auth` field), profile detection, pwm writability, sensors, the emergency hook
(`/etc/n5-fangov/emergency.sh`: `ok`, `absent` or `refused: <why>` — a warning only
with `emergency = true`), tls, log, dkms (module for the running kernel). Exit 1 means
`serve` could not run with this config; `--quiet` prints failures only;
`--after-update` is the apt hook's [kernel gate](02-kernel-driver.md#the-kernel-update-gate).
It also warns about `auth = "none"` on a non-loopback listener, a `critical` above the
channel's ceiling and a degraded alert transport (including `webhook` without a usable
URL).

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
as root and must not be pointed at `/dev/sda` or its own config. `[log]` changes take a
restart.

```
n5-fangov log -n 200                 newest lines of the current file
n5-fangov log --export today.log     whole current file (rotated files not included)
n5-fangov log --clear                truncate the current file; rotated files and the journal stay
```

The dashboard's [Log page](04-dashboard.md#log) uses the same file (`GET /api/log`,
`/api/log/export`; *Clear log* in Settings → Danger zone is `DELETE /api/log`); without
a file it falls back to the journal and cannot clear. Log lines never carry secrets (hashes, tokens, passwords are redacted at
the source); a periodic status line every `[daemon] log_every` cycles shows what the
controller is doing.

Next: [Kernel driver](02-kernel-driver.md) · [Alerts](07-alerts.md) ·
[API and integrations](12-api.md) · [HTTPS and security](08-https-security.md)
