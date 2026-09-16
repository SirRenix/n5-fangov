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
| `n5-fangov check` exits 1: `profile "…" not detected` / `setup` finds no profile | the hwmon device is not there: module not loaded, or (N5 Pro) `experimental_write=1` missing so the `pwm*` nodes are invisible | `n5-fangov detect`; `lsmod \| grep minisforum`; `cat /etc/modprobe.d/minisforum-n5-it5571.conf`; `modprobe minisforum_n5_it5571`; `ls /sys/class/hwmon/*/pwm1` — [Kernel driver](02-kernel-driver.md#driver-options-and-autoload) |
| `check` says `dkms: module missing for <kernel>` / after a reboot the fans run the BIOS curve | DKMS did not build the module for the new kernel (headers missing, build failed) | [Rebuild the module by hand](02-kernel-driver.md#rebuild-the-module-by-hand); then `systemctl restart n5-fangov` |
| `check` exits 1: `channel <name>: … pwmN …: permission denied` / journal `permission denied` on `/sys/...` | the sandbox forbids the write: `ProtectKernelTunables` set to `yes` in a drop-in, or `ReadWritePaths` lost the hwmon path | `systemctl cat n5-fangov`; `make verify-deploy`; keep `ProtectKernelTunables=no` and `-/sys/class/hwmon -/sys/devices/platform` in `ReadWritePaths` ([Hardening](08-https-security.md#hardening)) |
| `status` shows a channel in `sensor-error` | its sensor is unresolved or unreadable (N5 Pro without SATA drives: `drivetemp:max` has no device) | expected on a box without HDDs; the channel sits at its stop duty. Otherwise `sensors`, `ls /sys/class/hwmon/*/name`, and the sensor id in the config |
| Browser shows a certificate warning | self-signed automatic certificate not yet trusted | lock icon → *Download .crt/.cer* → trust it per the panel's recipe → reload; or `n5-fangov cert export` ([The certificate](08-https-security.md#the-certificate)) |
| Browser refuses the LAN name outright (no warning page) after a certificate change | HSTS: the served certificate does not cover that name any more (upload without the name, or the `tls` fallback) | open the dashboard by IP (`https://192.0.2.10:8010`), then *Back to auto* or upload a pair that covers the name; `n5-fangov cert info` |
| `421 host header not allowed` | the `Host` a reverse proxy passes is neither an IP, `localhost`, the listen host nor listed | add the public name to `[web].allowed_hosts` or let the proxy rewrite `Host` to the upstream; restart ([Behind a reverse proxy](08-https-security.md#behind-a-reverse-proxy)) |
| Journal: `auth misconfigured — web bound to loopback` | `auth = "basic"` without usable `user`/`password_hash`, or a typo in `auth` — the daemon fails closed | `n5-fangov check` prints the field; `n5-fangov passwd`; restart |
| Dashboard on the LAN answers plain HTTP or refuses to start with `tls` | non-loopback listen with `tls = "off"` is forced to `auto`; `tls = "file"` without both paths falls back | `n5-fangov check`; `n5-fangov cert info`; fix `[web]` |
| Alerts do not arrive | transport degraded (`pve` without `PVE::Notify`, `mail` without `mail(1)`), cooldown active, PVE matcher routes elsewhere, or (mail path) the sandbox blocks `postdrop` | Alerts tab → *Send test alert* (reports the delivery error); `n5-fangov alerts status`; `journalctl -u n5-fangov \| grep -i alert`; [Alert delivery under the sandbox](08-https-security.md#alert-delivery-under-the-sandbox) for the `mail(1)` case |
| Toast `Session expired` / `401` while working | the cookie session ran out (12 h, 30 d with *Remember me*) or the password was changed outside the dashboard | sign in again; the editor keeps unsaved curve edits |
| Notice `restart required` after Apply/Import/Preset | the channel set or the profile changed; `[web]`/`[log]` keys changed by import | `systemctl restart n5-fangov` (the daemon keeps running the old set until then) |
| Dashboard banner `Connection to the daemon lost` | daemon restarting or down; fans stay on the daemon side (failsafe on exit) | `systemctl status n5-fangov`; `journalctl -u n5-fangov -n 50` |
| `n5-fangov test` refuses: `daemon socket answers` | the daemon regulates that channel | `systemctl stop n5-fangov` first (or `--force` after stopping it yourself) |
| Unit keeps restarting, `n5-fangov-onfailure` mails `failed` | `check` fails at every start (see the first rows) or the controller cannot write | `journalctl -u n5-fangov -b`; `n5-fangov check` by hand |

## What check reports

`n5-fangov check` walks the same list the unit runs as `ExecStartPre` before every
start: config (invalid values that fell back to their default, the misconfigured
`auth` field), profile detection, pwm writability, sensors, tls, log, dkms (module for
the running kernel). Exit 1 means `serve` could not run with this config; `--quiet`
prints failures only; `--after-update` is the apt hook's
[kernel gate](02-kernel-driver.md#the-kernel-update-gate). It also warns about
`auth = "none"` on a non-loopback listener and a degraded alert transport.

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

The dashboard's [Log tab](04-dashboard.md#log) uses the same file (`GET /api/log`,
`/api/log/export`, `DELETE /api/log`); without a file it falls back to the journal and
cannot clear. Log lines never carry secrets (hashes, tokens, passwords are redacted at
the source); a periodic status line every `[daemon] log_every` cycles shows what the
controller is doing.

Next: [Kernel driver](02-kernel-driver.md) · [Alerts](07-alerts.md) ·
[HTTPS and security](08-https-security.md)
