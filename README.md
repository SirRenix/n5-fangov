# ventula — guarded fan control for Proxmox VE and Debian

One static binary: regulation daemon, CLI and embedded web dashboard.

*ventula* is Dalmatian for "fan", from Latin *ventus*, wind (formerly pvefand
during development).

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
  leaves the drives unregulated. ventula knows that and stops to a fixed safe duty instead.

## What it guards against

| Failure | Response |
|---|---|
| Controller hangs | systemd watchdog (60 s) → kill → failsafe → restart |
| Controller dies (crash, OOM, kill) | `ExecStopPost=ventula failsafe` → profile-defined safe state |
| Kernel update without the DKMS module | `ExecStartPre=ventula check` fails loudly → alert; fans stay in EC/BIOS mode |
| Sensor unreadable, implausible or frozen | all channels 255, alert, re-resolve |
| Fan stalls (0 RPM at duty ≥ threshold) | channel 255, alert, auto-recovery |
| Write fails or read-back differs | 255, alert |
| Somebody else writes to `/sys` | set-point re-asserted every minute, warning |
| Broken config | built-in defaults + warning + alert; daemon still starts |
| Critical temperature | 255 immediately, also in manual mode |

Alerts go to the Proxmox notification stack (`PVE::Notify`, template `ventula`) when
running on PVE, otherwise `mail(1)`; always to the journal.

## Install

```
# Proxmox VE 9 / Debian 13, as root
./deploy/install.sh
ventula detect          # shows the profile that will be used, never writes
ventula check           # prerequisites
systemctl start ventula
ventula status
```

N5 Pro only: the kernel module must be installed first (DKMS package from the sibling
repo, `experimental_write=1`). `ventula check` tells you if it is missing.

## Use

```
ventula status                 temperatures, duty, RPM, mode per channel
ventula set hdd 70%            manual override (limits and stall guard still apply)
ventula auto hdd               back to the curve
ventula curve                  active curves
ventula log 50
ventula test 3                 channel verification run (daemon must be stopped)
```

Web dashboard: `http://<host>:8010` (default binds to 127.0.0.1). Tabs: Overview,
Curves, Manual, Presets, Log, Compatibility. LAN access: see Security.

## Security

The API changes fan duties, so treat the port like a management interface.

- **Default is loopback only** (`[web].listen = "127.0.0.1:8010"`, no auth). The CLI
  uses the unix socket in `/run/ventula` (root only, `RuntimeDirectoryMode=0750`).
- **LAN access only behind a TLS reverse proxy** (Caddy, nginx, the PVE proxy). Basic
  auth is sent in clear text on every request; without TLS anyone on the segment can
  read it. Keep `listen` on loopback and let the proxy connect to it, or bind a LAN
  address **and** set `auth = "basic"`:

  ```toml
  [web]
  listen = "127.0.0.1:8010"
  auth = "basic"
  user = "admin"
  password_hash = "<sha256 hex of 'admin:password'>"   # printf 'admin:password' | sha256sum
  allowed_hosts = ["fans.example.internal"]            # names the proxy passes in Host
  ```

- **Fail closed.** `auth = "basic"` with a missing or unusable `password_hash`, or a
  typo in `auth`, never degrades to an open LAN listener: the daemon forces
  `listen` to `127.0.0.1:8010` and logs `auth misconfigured — web bound to loopback`.
  `auth = "none"` on a non-loopback address is allowed but logged as a warning at
  every start.
- **What auth covers.** With `auth = "basic"`, every write (PUT/POST/DELETE) plus
  `GET /api/config` and `GET /api/log` need credentials. State, history, presets,
  profiles and the dashboard itself stay readable. Failed logins are throttled per
  client IP (5 free, then 250 ms doubling to 2 s, reset after 10 min or a success)
  and logged with user name and IP.
- **The hash never leaves the daemon.** `GET /api/config` shows
  `password_hash = "<unchanged>"`; sending that text back keeps the stored hash.
- **Host header check (DNS rebinding).** Requests are only served for IP literals,
  `localhost`, the listen host and `allowed_hosts`; anything else gets 421. A reverse
  proxy must either rewrite `Host` to the upstream (nginx does by default, Caddy:
  `header_up Host {upstream_hostport}`) or its public name must be listed in
  `allowed_hosts`. `"*"` disables the check.
- **CSRF.** Every write needs the header `X-Ventula-Csrf: 1`; a browser form or
  cross-site fetch cannot add it without CORS, which the API does not offer.
- Changing `[web]` settings takes a restart; `PUT /api/config` reloads curves only.

## Configure

`/etc/ventula/config.toml` — curves as point lists, sensor source per channel:

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

## Compatibility

| Platform | Status |
|---|---|
| Proxmox VE 9 / Debian 13, Minisforum N5 Pro, BIOS 1.05 | **verified** (channel mapping, stop behaviour, load tests) |
| Debian/Ubuntu with NCT67xx (`nct6775`) or IT87xx (`it87`) | from documentation, untested — please report |
| Any Linux with hwmon, no PWM | monitoring only |
| Unraid, TrueNAS, non-systemd | binary runs; the guard chain relies on systemd |

## Build

No Go toolchain needed locally: `tools/remote-go.ps1` builds in a `golang:1.25-alpine`
container (static, `CGO_ENABLED=0`). Or plainly: `CGO_ENABLED=0 go build ./cmd/ventula`.

## License

GPL-2.0-only. Layout of the dashboard inspired by ProxFansX; no code shared.
