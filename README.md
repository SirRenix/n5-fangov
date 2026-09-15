# pvefand

Guarded fan control for Proxmox VE and Debian — one static binary: regulation daemon,
CLI and embedded web dashboard.

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
  leaves the drives unregulated. pvefand knows that and stops to a fixed safe duty instead.

## What it guards against

| Failure | Response |
|---|---|
| Controller hangs | systemd watchdog (60 s) → kill → failsafe → restart |
| Controller dies (crash, OOM, kill) | `ExecStopPost=pvefand failsafe` → profile-defined safe state |
| Kernel update without the DKMS module | `ExecStartPre=pvefand check` fails loudly → alert; fans stay in EC/BIOS mode |
| Sensor unreadable, implausible or frozen | all channels 255, alert, re-resolve |
| Fan stalls (0 RPM at duty ≥ threshold) | channel 255, alert, auto-recovery |
| Write fails or read-back differs | 255, alert |
| Somebody else writes to `/sys` | set-point re-asserted every minute, warning |
| Broken config | built-in defaults + warning + alert; daemon still starts |
| Critical temperature | 255 immediately, also in manual mode |

Alerts go to the Proxmox notification stack (`PVE::Notify`, template `pvefand`) when
running on PVE, otherwise `mail(1)`; always to the journal.

## Install

```
# Proxmox VE 9 / Debian 13, as root
./deploy/install.sh
pvefand detect          # shows the profile that will be used, never writes
pvefand check           # prerequisites
systemctl start pvefand
pvefand status
```

N5 Pro only: the kernel module must be installed first (DKMS package from the sibling
repo, `experimental_write=1`). `pvefand check` tells you if it is missing.

## Use

```
pvefand status                 temperatures, duty, RPM, mode per channel
pvefand set hdd 70%            manual override (limits and stall guard still apply)
pvefand auto hdd               back to the curve
pvefand curve                  active curves
pvefand log 50
pvefand test 3                 channel verification run (daemon must be stopped)
```

Web dashboard: `http://<host>:8010` (default binds to 127.0.0.1; set `[web].listen`
and `auth = "basic"` for LAN access). Tabs: Overview, Curves, Manual, Presets, Log,
Compatibility.

## Configure

`/etc/pvefand/config.toml` — curves as point lists, sensor source per channel:

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
container (static, `CGO_ENABLED=0`). Or plainly: `CGO_ENABLED=0 go build ./cmd/pvefand`.

## License

GPL-2.0-only. Layout of the dashboard inspired by ProxFansX; no code shared.
