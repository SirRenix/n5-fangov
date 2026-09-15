# n5-fangov — guarded fan control for Proxmox VE and Debian

One static binary: regulation daemon, CLI and embedded web dashboard.

n5-fangov — a fan governor for the Minisforum N5 / N5 Pro: it takes over the PWM
channels the kernel driver already exposes and adds curves, guards and a dashboard;
generic NCT67xx/IT87xx profiles included (untested)
(formerly pvefand/ventula during development).

**Hardware-verified on the Minisforum N5 Pro** (ITE IT5571 embedded controller via the
community driver [`ltdstudio/minisforum-n5-it5571`](https://github.com/ltdstudio/minisforum-n5-it5571)).
Generic hwmon profiles for Nuvoton NCT67xx and ITE IT87xx ship as *from documentation,
untested* — the dashboard says so, per profile.

> Status: pre-release. Validation data, the Bash predecessor `n5-fand` and the
> measurement scripts live in [`minisforum-n5pro-fan-proxmox`](https://github.com/SirRenix/minisforum-n5pro-fan-proxmox).

## Why

- The N5 Pro exposes no fan control to Linux at all; the BIOS "silent" curve lets the
  drives sit at 40–42 °C and offers only fixed PWM.
- `fancontrol` regulates one channel from one sensor. The HDD group needs the hottest
  of four drives, the SSD fan the hottest of three NVMe.
- The IT5571 EC **does not resume automatic regulation of the HDD channel after any
  write** (measured 2026-09-14). A controller that hands control back to the EC on exit
  leaves the drives unregulated. n5-fangov knows that and stops to a fixed safe duty instead.

## What it guards against

| Failure | Response |
|---|---|
| Controller hangs | systemd watchdog (60 s) → kill → failsafe → restart |
| Controller dies (crash, OOM, kill) | `ExecStopPost=n5-fangov failsafe` → profile-defined safe state |
| Kernel update without the DKMS module | apt hook `check --after-update` alerts before the reboot; `ExecStartPre=n5-fangov check` fails loudly after it; fans stay in EC/BIOS mode |
| Sensor unreadable, implausible, frozen or absent | that channel at its safe duty (fixed stop duty, else 255), alert naming it, re-resolve; the other channels keep regulating |
| Fan stalls (0 RPM at duty ≥ threshold) | channel 255, alert, auto-recovery |
| Write fails or read-back differs | 255, alert |
| Somebody else writes to `/sys` | set-point re-asserted every minute, warning |
| Broken config | built-in defaults + warning + alert; daemon still starts |
| Critical temperature | 255 immediately, also in manual mode |

Alerts go to the Proxmox notification stack (`PVE::Notify`, template `n5-fangov`) when
running on PVE, otherwise `mail(1)`; always to the journal.

## Install

```
# Proxmox VE 9 / Debian 13, as root
./deploy/install.sh           # or: apt install ./dist/n5-fangov_<version>_amd64.deb
n5-fangov setup
```

The installer puts the binary, the units, the apt hook and the log directory in
place and enables the unit. It writes **no** config and starts nothing — that is
`setup`. N5 Pro only: the kernel module must be installed first (DKMS package from
the sibling repo, `experimental_write=1`); `setup` refuses without a detected profile.

## Setup

`n5-fangov setup` writes `/etc/n5-fangov/config.toml` for this machine:

1. **Profile**: detected on the box (`n5pro` → the verified three-channel set;
   `nct67xx`/`it87xx` → one conservative channel per PWM output, sensor `k10temp` /
   `coretemp` / first hwmon temperature; `monitor` → no channels).
2. **Scope** of the web UI:
   - `local` — `127.0.0.1:8010`, no auth, plain HTTP. Reach it with `ssh -L 8010:127.0.0.1:8010 <host>`
     or put a reverse proxy in front.
   - `lan` — the primary LAN address, **basic auth** (user + password asked twice
     without echo) and **HTTPS** with an automatically created self-signed certificate.
   - `HOST:PORT` — explicit address; non-loopback implies auth + HTTPS like `lan`.
3. An existing config is backed up (`config.toml.bak-<timestamp>`) before it is replaced.

Non-interactive: `n5-fangov setup --yes --listen lan --user admin --password '...'`
(all needed flags must be present). Afterwards:

```
n5-fangov check                  # what serve will do with this config
systemctl enable --now n5-fangov
n5-fangov status
```

Change the password later with `n5-fangov passwd` (edits the file in place, restart to
apply). The reference with every key explained is `/usr/share/doc/n5-fangov/config.example.toml`.

## Use

```
n5-fangov status                 temperatures, duty, RPM, mode per channel
n5-fangov set hdd 70%            manual override (limits and stall guard still apply)
n5-fangov auto hdd               back to the curve
n5-fangov curve                  active curves
n5-fangov log -n 50              log file (journal when no file is configured)
n5-fangov test 3                 channel verification run (daemon must be stopped)
n5-fangov export settings.json   config + presets as one JSON bundle
```

Web dashboard: `http://127.0.0.1:8010` (`local`) or `https://<host>:8010` (`lan`). Tabs:
Overview, Curves, Manual, Presets, Log, Compatibility.

## HTTPS

`[web].tls` is `auto` | `off` | `file`. The default follows the listener: `off` on
loopback, `auto` everywhere else, and **a non-loopback listener never runs plain HTTP** —
`tls = "off"` there is replaced by `auto` with a warning (basic auth would otherwise
cross the LAN in clear text).

- **`auto`** — the daemon creates an ECDSA P-256 self-signed certificate (10 years) in
  `/etc/n5-fangov/tls/` at the first start and reuses it; it is regenerated when the
  names change. SANs: the listen host (all local addresses for `0.0.0.0`/`[::]`), the
  host name, `localhost`, and `[web].allowed_hosts`. Browsers warn once about the
  unknown issuer; to make that go away, trust the certificate:

  ```
  n5-fangov cert export > n5-fangov.pem      # PEM, certificate only
  # Firefox/Chrome: import as a trusted server certificate; Debian: copy to
  # /usr/local/share/ca-certificates/n5-fangov.crt && update-ca-certificates
  n5-fangov cert regen && systemctl restart n5-fangov   # new key pair, same names
  ```

- **`file`** — your own certificate: `cert_file` (PEM chain) and `key_file` (PEM key),
  both required; the paths must be readable inside the unit's sandbox (see Hardening —
  `/etc/n5-fangov/` is the simple place). A missing file disables the web listener, the
  CLI socket keeps working.

- **`off`** — plain HTTP, loopback only. A reverse proxy (Caddy, nginx, the PVE proxy)
  terminating TLS in front of `127.0.0.1:8010` is the alternative to `auto`; list its
  public name in `allowed_hosts` or let it rewrite `Host`.

TLS 1.2 minimum, modern cipher suites, HSTS header. Changes to `[web]` need a restart.

## Security

The API changes fan duties, so treat the port like a management interface.

- **Default is loopback only** (`[web].listen = "127.0.0.1:8010"`, no auth). The CLI
  uses the unix socket in `/run/n5-fangov` (root only, `RuntimeDirectoryMode=0750`).
- **LAN access means auth + TLS.** `setup --listen lan` configures both; by hand:

  ```toml
  [web]
  listen = "192.0.2.10:8010"
  auth = "basic"
  user = "admin"
  password_hash = "<sha256 hex of 'admin:password'>"   # n5-fangov passwd, or: printf 'admin:password' | sha256sum
  tls = "auto"                                         # or "file" with cert_file/key_file
  allowed_hosts = ["fans.example.internal"]            # names a proxy passes in Host
  ```

- **Fail closed.** `auth = "basic"` with a missing or unusable `password_hash`, or a
  typo in `auth`, never degrades to an open LAN listener: the daemon forces
  `listen` to `127.0.0.1:8010` and logs `auth misconfigured — web bound to loopback`.
  `auth = "none"` on a non-loopback address is allowed (still HTTPS) but logged as a
  warning at every start and by `n5-fangov check`.
- **What auth covers.** With `auth = "basic"`, every write (PUT/POST/DELETE) plus
  `GET /api/config`, `GET /api/log` and the export/import endpoints need credentials.
  State, history, presets, profiles and the dashboard itself stay readable. Failed
  logins are throttled per client IP (5 free, then 250 ms doubling to 2 s, reset after
  10 min or a success) and logged with user name and IP — only when an `Authorization`
  header was actually presented; the anonymous 401 the UI gets before login is not a
  failure.
- **The hash never leaves the daemon.** `GET /api/config` and the settings export show
  `password_hash = "<unchanged>"`; sending that text back keeps the stored hash.
- **Host header check (DNS rebinding).** Requests are only served for IP literals,
  `localhost`, the listen host and `allowed_hosts`; anything else gets 421. A reverse
  proxy must either rewrite `Host` to the upstream (nginx does by default, Caddy:
  `header_up Host {upstream_hostport}`) or its public name must be listed in
  `allowed_hosts`. `"*"` disables the check.
- **CSRF.** Every write needs the header `X-N5-Fangov-Csrf: 1`; a browser form or
  cross-site fetch cannot add it without CORS, which the API does not offer.
- Changing `[web]` or `[log]` settings takes a restart; `PUT /api/config` reloads curves,
  sensors and `[daemon]` values only.

## Configure

`/etc/n5-fangov/config.toml` — curves as point lists, sensor source per channel:

```toml
[[channel]]
name = "hdd"
pwm = 3
sensor = "drivetemp:max"
curve = [[36,105],[46,255]]
critical = 56
stop = 140            # fixed stop duty: the EC won't regulate this channel after a write
```

Sensor sources: `k10temp`, `coretemp`, `nvme:max`, `drivetemp:max`, `hwmon:<name>:tempN`,
`ec:<label>` (N5 Pro EC temperatures). Invalid values fall back to defaults with a warning.

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
runs with `UMask=0077`).

```
n5-fangov log -n 200                 newest lines of the current file
n5-fangov log --export today.log     whole current file (rotated files not included)
n5-fangov log --clear                truncate the current file; rotated files and the journal stay
```

The dashboard's Log tab uses the same file (`GET /api/log`, `/api/log/export`,
`DELETE /api/log`); without a file it falls back to the journal and cannot clear.

## Backup / Restore

```
n5-fangov export settings.json       # {"format":1, config: <toml>, presets: {name: <toml>}}
n5-fangov import settings.json       # validates everything, then writes and reloads
```

The export is the config file text (comments included) plus every preset, with the
password hash redacted to `<unchanged>`. `import` refuses the whole bundle when any
part fails to parse — nothing is written in that case. `<unchanged>` is resolved from
the password stored on the importing machine; on a fresh box run `n5-fangov passwd`
first (or put a real hash into the bundle). Presets that exist locally but not in the
bundle stay. A running daemon reloads curves and `[daemon]` values; a changed channel
set or profile, or any `[web]`/`[log]` change, needs `systemctl restart n5-fangov`
(the command says so). The API offers the same (`GET /api/config/export`,
`POST /api/config/import`, auth + CSRF).

## Updates

**Package update** (`apt upgrade` of n5-fangov): postinst restarts a running daemon;
`ExecStopPost=failsafe` puts the fans into the safe state between the old and the new
process, `check --quiet` gates the new start.

**Kernel update** (N5 Pro): the EC driver is a DKMS module. If DKMS did not build it for
the new kernel, the next boot has no `pwm` files, `check` fails and the fans stay under
BIOS/EC control (safe, but unregulated for the drives). Two gates catch this:

1. `/etc/apt/apt.conf.d/90n5-fangov` runs `n5-fangov check --after-update` after every
   dpkg run. It requires `updates/dkms/minisforum_n5_it5571.ko` for **every** kernel
   under `/lib/modules`. Missing → `kernel X: fan driver module missing — run: dkms
   install minisforum-n5-it5571/<ver> -k X` on the apt output plus a `kernel` alert
   (30 min cooldown). The apt run never fails because of it. Non-N5-Pro boxes: no-op.
2. `ExecStartPre=n5-fangov check` reports `dkms` for the running kernel at every start.

What a Proxmox upgrade **can** affect: the kernel (above), `dkms` itself, perl/
`PVE::Notify` for alerts. What it **cannot**: the config, presets, TLS certificate and
logs live under `/etc/n5-fangov` and `/var/log/n5-fangov` and are never touched by
package scripts (purge removes them).

## Hardening

The unit runs sandboxed (`deploy/n5-fangov.service`); the list is the contract, verify
on real hardware after every change — `/sys` writes are what most sandboxes forbid.

| Setting | Effect |
|---|---|
| `NoNewPrivileges=yes`, `LockPersonality=yes`, `RestrictRealtime=yes` | no privilege escalation from the daemon |
| `ProtectSystem=strict` + `ReadWritePaths=/etc/n5-fangov /run/n5-fangov /var/log/n5-fangov /sys/class/hwmon /sys/devices` | whole file system read-only except config/presets/tls, runtime dir, logs and the hwmon attributes |
| `ProtectKernelTunables=no` | **must stay `no`**: the pwm files are kernel tunables |
| `ProtectHome=yes`, `PrivateTmp=yes`, `ProtectControlGroups=yes` | no access to home, private /tmp, cgroups read-only |
| `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6`, `RestrictNamespaces=yes` | socket, TCP/HTTP(S), nothing else |
| `MemoryDenyWriteExecute=yes`, `SystemCallArchitectures=native`, `SystemCallFilter=@system-service @module` | no JIT/exec tricks, native syscalls only |
| `CapabilityBoundingSet=CAP_SYS_MODULE CAP_DAC_OVERRIDE CAP_CHOWN CAP_FOWNER` | sysfs and file ownership only |
| `UMask=0077`, `LogsDirectory=n5-fangov` (`0750`), `RuntimeDirectory=n5-fangov` (`0750`) | files private by default |

Alerts are sent by running `perl`/`mail` from inside this sandbox; they need the same
paths readable, which `ProtectSystem=strict` leaves.

## Compatibility

| Platform | Status |
|---|---|
| Proxmox VE 9 / Debian 13, Minisforum N5 Pro, BIOS 1.05 | **verified** (channel mapping, stop behaviour, load tests) |
| Debian/Ubuntu with NCT67xx (`nct6775`) or IT87xx (`it87`) | from documentation, untested — please report |
| Any Linux with hwmon, no PWM | monitoring only |
| Unraid, TrueNAS, non-systemd | binary runs; the guard chain relies on systemd |

## Build

No Go toolchain needed locally: `tools/remote-go.ps1` builds in a `golang:1.25-alpine`
container (static, `CGO_ENABLED=0`). Or plainly: `CGO_ENABLED=0 go build ./cmd/n5-fangov`.
`make deb` builds the Debian package.

## License

GPL-2.0-only. Layout of the dashboard inspired by ProxFansX; no code shared.
