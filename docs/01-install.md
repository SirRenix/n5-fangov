# Install

Three ways onto the box: the release binary with the repository's installer, a build
from a checkout, or the `.deb`. All end with `n5-fangov setup` ([Setup](03-setup.md)).

## Prerequisites

| | |
|---|---|
| OS | Proxmox VE 9.x (Debian 13 "trixie") or Debian 13 with systemd. Everything runs as root. The tested platform: [Tested hardware](../README.md#tested-hardware). |
| Packages | Optional: `pciutils` (`lspci`, device names on the System page), `lm-sensors` (`sensors` for cross-checks). |
| Fan driver | A hwmon driver that exposes the `pwm*` files. **N5 Pro:** the out-of-tree EC module as package `minisforum-n5-it5571-dkms` (brings `dkms` and the kernel headers), install it first — [Kernel driver](02-kernel-driver.md). **Other boards:** `nct6775` / `it87` from the distribution kernel. |
| Drive temperatures | The `drivetemp` kernel module (in-tree, not loaded by default) for every channel on `drivetemp:max` or `disk:<dev>` — on the N5 Pro that is the `hdd` channel of the preset. Without it the channel sits in `sensor-error` at its stop duty. Load it and make it stick: `modprobe drivetemp && echo drivetemp > /etc/modules-load.d/drivetemp.conf` — [Drive temperatures](02-kernel-driver.md#drive-temperatures). NVMe needs nothing, the `nvme` driver has its own hwmon. |
| Check | `n5-fangov detect` (after the install) lists the hwmon devices and the profile it would use. |

## From a GitHub release

No Go toolchain needed. The release carries the static `linux/amd64` binary and its
sha256; units, scripts and templates come from the checkout at the same tag.

```
# as root; VER = the tag without the leading v, see the releases page
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

Host without GitHub access: run the `git clone` and the two `curl` lines on any
machine, copy the checkout with its `dist/` to the host with `scp`, continue at the
`sha256sum` line.

## From a checkout with a build

`make build` needs the Go version pinned in `go.mod`. Without Go on the box,
`tools/remote-go.ps1 -Fetch` builds in Docker on another machine ([Build](11-development.md#build)).

```
make build && ./deploy/install.sh
n5-fangov setup
```

## The Debian package

The `.deb` is a release asset: `n5-fangov_<debver>_amd64.deb`, where `<debver>` is the
dpkg form of the tag. A pre-release `X.Y.Z-rc1` is dpkg version `X.Y.Z~rc1` and the asset
`n5-fangov_X.Y.Z.rc1_amd64.deb` (GitHub replaces the `~`). Download it into `dist/` like
the binary; `make deb` builds your own from a checkout into the same place.

```
apt install ./dist/n5-fangov_<debver>_amd64.deb
```

It installs the same files as `install.sh`, with the units under `/lib/systemd/system/`.
The postinst does what the installer does, including stopping `n5-fand`, and ends with
one of three lines:

```
n5-fangov: installed and enabled. Next: n5-fangov setup
n5-fangov: installed and enabled. Next: n5-fangov check && systemctl start n5-fangov
n5-fangov: daemon is running (installer deployment), restarting it so the new binary takes over
```

The first on a fresh box, the second when `/etc/n5-fangov/config.toml` exists, the
third when a daemon is running (on an upgrade the bracket names the previous package
version). After it the apt hook prints its line, as after every dpkg run from now on
([kernel-update gate](02-kernel-driver.md#the-kernel-update-gate)):

```
n5-fangov: fan driver module present for 1 kernel(s): 7.0.14-17-pve
```

A file under `/root` makes apt print `Notice: Download is performed unsandboxed as
root …` first; harmless. `dpkg -V n5-fangov` checks the installed files against the
package's md5sums. Package updates: [Updates](09-updates.md#package-update).

**Installer to package.** The installer's units live in `/etc/systemd/system/`, the
package's in `/lib/systemd/system/`, and systemd prefers the `/etc` copy. The postinst
removes the installer's copies when they are identical to the packaged units, warns
when they differ, and restarts a running daemon. From then on `install.sh` and
`uninstall.sh` refuse to run on a package-installed host. If a leftover unit still
shadows the package:

```
rm /etc/systemd/system/n5-fangov.service /etc/systemd/system/n5-fangov-onfailure.service && systemctl daemon-reload
```

## What the installer does

`deploy/install.sh` needs the binary at `dist/n5-fangov` (or `./n5-fangov`) and refuses
to run without one. It puts the binary, the units, the apt hook, the log directory, the
config example and, on PVE, the notification template pair in place, then enables the
unit. It writes **no** config and starts nothing — that is `setup`. An existing
`/etc/n5-fangov/config.toml` is left untouched. On a fresh box it prints:

```
n5-fangov: OK
--- predecessor n5-fand ---
  n5-fand files stay installed; remove with minisforum-n5pro-fan-proxmox/legacy/uninstall.sh when n5-fangov is proven.
--- files ---
  /etc/n5-fangov/config.toml absent: n5-fangov setup writes it (reference: /usr/share/doc/n5-fangov/config.example.toml)
  PVE notification template installed (alerts -> Proxmox notifications, template 'n5-fangov')
--- apt hook (kernel gate) ---
  /etc/apt/apt.conf.d/90n5-fangov: 'n5-fangov check --after-update' runs after every dpkg run
--- systemd ---
  enabled (not started)

Installed n5-fangov <version>. Log file: /var/log/n5-fangov/n5-fangov.log (rotating; journal unchanged).
run: n5-fangov setup
```

The first line is the sha256 check of the binary.

`install.sh` and the postinst **stop and disable `n5-fand.service`**, the Bash
predecessor from the driver repository; two regulators must never write the same
channels, and the unit carries `Conflicts=n5-fand.service`. The n5-fand files stay
until you remove them with that repository's `uninstall.sh`.

On PVE the template copy into `/etc/pve` is best effort: without quorum pmxcfs is
read-only, the installer warns, and `n5-fangov alerts template` installs the pair later
([The PVE template](07-alerts.md#the-pve-template)).

## File layout

| Path | Purpose |
|---|---|
| `/usr/bin/n5-fangov` | daemon + CLI, one static binary |
| `/etc/systemd/system/n5-fangov.service` (deb: `/lib/systemd/system/`) | `Type=notify`, watchdog 60 s, `ExecStartPre=check --quiet`, `ExecStopPost=failsafe`, sandboxed ([Hardening](08-https-security.md#hardening)) |
| `n5-fangov-onfailure.service` + `/usr/libexec/n5-fangov/n5-fangov-onfailure` | alert with the real cause when the unit fails ([Alert kinds](07-alerts.md#alert-kinds)) |
| `/etc/n5-fangov/config.toml` | written by `n5-fangov setup`, never by the installer |
| `/etc/n5-fangov/presets/`, `/etc/n5-fangov/tls/` | user presets; the automatic TLS certificate, created at the first HTTPS start |
| `/etc/n5-fangov/emergency.sh` | the optional emergency hook, installed by hand ([Ceilings](06-configuration.md#ceilings-and-the-emergency-action)) |
| `/var/log/n5-fangov/` (0750) | rotating log file `n5-fangov.log`, `.1`..`.N`; the journal is unchanged |
| `/etc/apt/apt.conf.d/90n5-fangov` | `DPkg::Post-Invoke` → `n5-fangov check --after-update`, the [kernel-update gate](02-kernel-driver.md#the-kernel-update-gate) |
| `/var/lib/n5-fangov/` (0700) | `sessions.json` (hashed dashboard sessions), `tokens.json` (hashed [API tokens](08-https-security.md#api-tokens)), `alerts.json` (recent alerts), `history.json` (chart history, saved every 10 min); all 0600, removed on uninstall, never part of the settings bundle |
| `/run/n5-fangov/` (0750) | `n5-fangov.sock` (CLI), `state.json` (fallback for `status`), override and alert stamps |
| `/usr/share/doc/n5-fangov/config.example.toml` | reference with every key explained ([Configuration](06-configuration.md)) |
| `/usr/share/doc/n5-fangov/examples/emergency.example.sh` | template for the emergency hook |
| `/usr/share/n5-fangov/pve-notification/*.hbs` | copied to `/etc/pve/notification-templates/default/` when `/etc/pve` exists |
