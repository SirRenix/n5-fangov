# n5-fangov

Guarded fan control for Proxmox VE and Debian: daemon, CLI and web dashboard in one
static binary. Written for the Minisforum N5 Pro, whose fans hang on an EC that no
in-tree driver knows.

![Dashboard overview: sidebar navigation, channel tiles with temperature, sparkline, duty and RPM, and the history charts](docs/screenshots/03-overview-signed-in.png)

- Regulates the PWM channels of a hwmon driver from curves over one or more sensors:
  the hottest of four drives for the HDD fan, the hottest of three NVMe for the SSD fan.
- Guards what it can see: critical temperature, a hard ceiling per sensor kind, fan
  stall, sensor loss, write errors, its own crash, a kernel update without the driver.
  The chain: [What the daemon guards against](docs/07-alerts.md#what-the-daemon-guards-against).
- Alerts through Proxmox notifications, `mail(1)`, a webhook (ntfy, Gotify, Home
  Assistant) or the journal.
- Dashboard with curve editor, manual override, presets, schedules by time of day,
  history over 2 h / 24 h / 7 d and CSV export. HTTPS and login on the LAN, plain and
  open on loopback.
- API tokens with scopes for scripts and Home Assistant; OpenAPI at `/api/openapi.json`.
- Runs sandboxed under systemd with watchdog and failsafe on exit. One dependency, no
  frontend framework.

## Install

N5 Pro: install the [kernel driver](docs/02-kernel-driver.md) first. Then, as root,
check out the release tag and download its binary into `dist/` — the complete block
with the sha256 check is in [Install](docs/01-install.md#from-a-github-release). It
ends with:

```
./deploy/install.sh
n5-fangov setup
systemctl enable --now n5-fangov
```

The `.deb` is the other way: [The Debian package](docs/01-install.md#the-debian-package).

## Documentation

| | |
|---|---|
| [Install](docs/01-install.md) · [Kernel driver](docs/02-kernel-driver.md) · [Setup](docs/03-setup.md) | getting it on the box |
| [Dashboard](docs/04-dashboard.md) · [CLI reference](docs/05-cli.md) · [Configuration](docs/06-configuration.md) | using it |
| [Alerts and guards](docs/07-alerts.md) · [HTTPS and security](docs/08-https-security.md) | failure handling, access |
| [API and integrations](docs/12-api.md) | tokens, OpenAPI, the Home Assistant recipe |
| [Updates](docs/09-updates.md) · [Troubleshooting](docs/10-troubleshooting.md) | keeping it running |
| [Development](docs/11-development.md) · [DESIGN.md](DESIGN.md) | building, testing, the contract |

All pages: [docs/README.md](docs/README.md). Changes per version: [CHANGELOG.md](CHANGELOG.md).

## Tested hardware

| Platform | Status |
|---|---|
| Minisforum N5 Pro, BIOS 1.05 — Proxmox VE 9.2.20 on Debian 13.7 (trixie), kernel **7.0.14-17-pve** (running) and 7.0.12-1-pve (fallback), DKMS package `minisforum-n5-it5571-dkms` 0.2.1-1 (upstream v0.2.1), lm-sensors 3.6.2; binary built with Go 1.26 (static, `golang:1.26-alpine`) | **verified** 2026-09-19 with the [release gate](docs/RELEASE-GATE.md) of 0.4.0: channel mapping, stop behaviour, multi-hour runs; the reboot proof dates from the 0.3.1 gate of 2026-09-18, the boot chain is unchanged since |
| Debian/Ubuntu with NCT67xx (`nct6775`) or IT87xx (`it87`) | from documentation, untested — please report |
| Any Linux with hwmon, no PWM | monitoring only |
| Unraid, TrueNAS, non-systemd | binary runs; the guard chain relies on systemd |

## No warranty

I run this on one machine, and the tested column is what I measured there on the date
given. Other boards, kernels and driver versions may behave differently, and mistakes
in the code or these pages are possible. Read the guard chain before you trust the
daemon with hardware you care about, keep the fans' BIOS defaults reachable (failsafe,
`uninstall.sh`), and report what you find as an issue. Security findings go through the
repository's private vulnerability reporting ([SECURITY.md](SECURITY.md)). The licence's
warranty disclaimer applies ([LICENSE](LICENSE), GPL-2.0-only §11–12).

## Credits

This started on top of two community projects. The EC driver
[`ltdstudio/minisforum-n5-it5571`](https://github.com/ltdstudio/minisforum-n5-it5571) is used
as is — its experimental N5 Pro profile is validated on my box, the measurements and the
Bash predecessor `n5-fand` are in
[`minisforum-n5pro-fan-proxmox`](https://github.com/SirRenix/minisforum-n5pro-fan-proxmox).
The dashboard idea comes from
[`Sl0thC0der/proxfansx`](https://github.com/Sl0thC0der/proxfansx); the generic
`nct67xx`/`it87xx` profiles follow its chip handling, written from the kernel
documentation, no code shared. The control part — daemon, CLI, dashboard — is a
rewrite from scratch in Go.

[Contributing](CONTRIBUTING.md) · [License: GPL-2.0-only](LICENSE)
