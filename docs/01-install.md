# Install

What this page covers: what the box needs before n5-fangov goes on, the two install
paths (release binary or a build from a checkout), the `.deb`, what the installer does
and where the files end up.

- [Prerequisites](#prerequisites)
- [From a GitHub release](#from-a-github-release)
- [From a checkout with a build](#from-a-checkout-with-a-build)
- [The Debian package](#the-debian-package)
- [What the installer does](#what-the-installer-does)
- [File layout](#file-layout)

## Prerequisites

| | |
|---|---|
| OS | Proxmox VE 9.x (Debian 13 "trixie") or Debian 13 with systemd; the tested platform is in the README's [Tested hardware](../README.md#tested-hardware). Everything runs as root. |
| Packages | `dkms` and the kernel headers for the running kernel (`proxmox-headers-$(uname -r)` on PVE, `linux-headers-$(uname -r)` on Debian) for the N5 Pro driver; `pciutils` (`lspci`, device names in the System tab; optional); `lm-sensors` (optional, `sensors` for cross-checks). |
| Fan driver | A hwmon driver that exposes the `pwm*` files. **N5 Pro:** the out-of-tree EC module — install it first, see [Kernel driver](02-kernel-driver.md). **Other boards:** `nct6775` / `it87` from the distribution kernel, no extra package. |
| Check | `n5-fangov detect` (after the install below) lists the hwmon devices and the profile it would use. |

Both install paths end with `n5-fangov setup` ([Setup](03-setup.md)).

## From a GitHub release

No Go toolchain needed. The release carries the static `linux/amd64` binary and its
sha256; the units, scripts and templates come from the repository at the same tag:

```
# as root; VER = the tag you want without the leading v, see the releases page
VER=X.Y.Z
git clone --branch v$VER --depth 1 https://github.com/SirRenix/n5-fangov.git
cd n5-fangov && mkdir -p dist
curl -fsSL -o dist/n5-fangov        https://github.com/SirRenix/n5-fangov/releases/download/v$VER/n5-fangov-$VER-linux-amd64
curl -fsSL -o dist/n5-fangov.sha256 https://github.com/SirRenix/n5-fangov/releases/download/v$VER/n5-fangov-$VER-linux-amd64.sha256
(cd dist && sed "s/n5-fangov-$VER-linux-amd64/n5-fangov/" n5-fangov.sha256 | sha256sum -c -)
chmod 0755 dist/n5-fangov
./deploy/install.sh
n5-fangov setup
```

## From a checkout with a build

`make build` needs the Go version pinned in `go.mod`; without Go on the box,
`tools/remote-go.ps1 -Fetch` builds in Docker on another machine (details in
[Development](11-development.md#build)):

```
make build && ./deploy/install.sh
n5-fangov setup
```

## The Debian package

`make deb` builds `dist/n5-fangov_<version>_amd64.deb`; `apt install ./dist/n5-fangov_<version>_amd64.deb`
installs the same files as `install.sh` (unit under `/lib/systemd/system/`). The postinst
does what the installer does, including stopping `n5-fand`; a package update restarts a
running daemon ([Updates](09-updates.md#package-update)). Then `n5-fangov setup`.

## What the installer does

`deploy/install.sh` looks for the binary at `dist/n5-fangov` (or `./n5-fangov`), refuses
to run without one, and puts the binary, the units, the apt hook, the log directory, the
config example and (on PVE) the notification template pair in place; then it enables the
unit. It writes **no** config and starts nothing — that is `setup`. An existing
`/etc/n5-fangov/config.toml` is left untouched.

N5 Pro: the kernel module must be loaded before `setup`; `setup` refuses without a
detected profile.

`install.sh` and the deb postinst **stop and disable `n5-fand.service`** (the Bash
predecessor from the driver repository) when it is present — two regulators must never
write the same channels; the unit also carries `Conflicts=n5-fand.service`. The n5-fand
files stay installed until you remove them with that repository's `uninstall.sh`.

On PVE the template copy into `/etc/pve` is best effort: without quorum pmxcfs is
read-only, the installer prints a warning and `n5-fangov alerts template` installs the
pair later ([Alerts](07-alerts.md#the-pve-template)).

## File layout

| Path | Purpose |
|---|---|
| `/usr/bin/n5-fangov` | daemon + CLI, one static binary |
| `/etc/systemd/system/n5-fangov.service` (deb: `/lib/systemd/system/`) | `Type=notify`, watchdog 60 s, `ExecStartPre=check --quiet`, `ExecStopPost=failsafe`, sandboxed ([Hardening](08-https-security.md#hardening)) |
| `n5-fangov-onfailure.service` + `/usr/libexec/n5-fangov/n5-fangov-onfailure` | alert with the real cause when the unit fails ([Alerts](07-alerts.md#alert-kinds)) |
| `/etc/n5-fangov/config.toml` | **written by `n5-fangov setup`**, never by the installer; an existing file is left alone |
| `/etc/n5-fangov/presets/`, `/etc/n5-fangov/tls/` | user presets; the automatic TLS certificate (created at the first HTTPS start) |
| `/var/log/n5-fangov/` (0750) | rotating log file `n5-fangov.log`, `.1`..`.N`; the journal is unchanged |
| `/etc/apt/apt.conf.d/90n5-fangov` | `DPkg::Post-Invoke` → `n5-fangov check --after-update`, the [kernel-update gate](02-kernel-driver.md#the-kernel-update-gate) |
| `/var/lib/n5-fangov/` (0700) | `sessions.json` (hashed dashboard sessions), `alerts.json` (recent alerts); removed on uninstall |
| `/run/n5-fangov/` (0750) | `n5-fangov.sock` (CLI), `state.json` (fallback for `status`), override and alert stamps |
| `/usr/share/doc/n5-fangov/config.example.toml` | reference with every key explained ([Configuration](06-configuration.md)) |
| `/usr/share/n5-fangov/pve-notification/*.hbs` | copied to `/etc/pve/notification-templates/default/` when `/etc/pve` exists |

Next: [Kernel driver](02-kernel-driver.md) (N5 Pro) · [Setup](03-setup.md) ·
[Updates, rollback, uninstall](09-updates.md)
