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
| Packages | `dkms` and the kernel headers for the running kernel (`proxmox-headers-$(uname -r)` on PVE, `linux-headers-$(uname -r)` on Debian) for the N5 Pro driver; `pciutils` (`lspci`, device names on the System page; optional); `lm-sensors` (optional, `sensors` for cross-checks). |
| Fan driver | A hwmon driver that exposes the `pwm*` files. **N5 Pro:** the out-of-tree EC module — install it first, see [Kernel driver](02-kernel-driver.md). **Other boards:** `nct6775` / `it87` from the distribution kernel, no extra package. |
| Check | `n5-fangov detect` (after the install below) lists the hwmon devices and the profile it would use. |

Both install paths end with `n5-fangov setup` ([Setup](03-setup.md)).

## From a GitHub release

No Go toolchain needed. The release carries the static `linux/amd64` binary and its
sha256; the units, scripts and templates come from the repository at the same tag.

> **While the repository is private (until 0.4.0)** the public URLs in the block below
> answer 404, and the host has no signed-in GitHub CLI — a hypervisor does not get one.
> Fetch everything on any machine with a signed-in `gh` and copy it to the host:
>
> ```
> # on the signed-in client; VER without the leading v
> VER=X.Y.Z
> gh release download v$VER -R SirRenix/n5-fangov -D dist --clobber          # binary, .sha256, .deb
> gh release download v$VER -R SirRenix/n5-fangov -A tar.gz -O src.tar.gz --clobber   # source of the tag
> scp -r src.tar.gz dist root@192.0.2.20:/root/
>
> # on the host, as root (a new shell: set VER again)
> VER=X.Y.Z
> mkdir n5-fangov && tar xzf src.tar.gz -C n5-fangov --strip-components=1
> mv dist n5-fangov/ && cd n5-fangov
> mv dist/n5-fangov-$VER-linux-amd64 dist/n5-fangov && mv dist/n5-fangov-$VER-linux-amd64.sha256 dist/n5-fangov.sha256
> ```
>
> A client that can reach the private repository over git may use
> `git clone --branch v$VER --depth 1` instead of the tarball. Then continue at the
> `sha256sum` line of the block below (the `.deb` is for the [package path](#the-debian-package)).

The public path:

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

The `.deb` is a **release asset** — `n5-fangov_<debver>_amd64.deb`, where `<debver>`
is the dpkg form of the tag (`0.3.1` → `n5-fangov_0.3.1_amd64.deb`; a pre-release
`0.3.2-rc1` → `n5-fangov_0.3.2.rc1_amd64.deb`, dpkg version `0.3.2~rc1` — GitHub
replaces the `~` in asset names). Download it like the binary (public URL, or the
private-phase copy above); `make deb` only matters when you build your own from a
checkout, it lands in `dist/` too.

```
apt install ./dist/n5-fangov_0.3.1_amd64.deb
```

It installs the same files as `install.sh` (units under `/lib/systemd/system/`). The
postinst does what the installer does, including stopping `n5-fand`, and ends with one
line — on a fresh box:

```
n5-fangov: installed and enabled. Next: n5-fangov setup
```

and when `/etc/n5-fangov/config.toml` already exists:

```
n5-fangov: installed and enabled. Next: n5-fangov check && systemctl start n5-fangov
```

and when the daemon is already running — the package installed over an `install.sh`
deployment, or an upgrade — the postinst restarts it, because a running process still
executes the binary dpkg just replaced:

```
n5-fangov: daemon is running (installer deployment), restarting it so the new binary takes over
```

(on an upgrade the bracket carries the previous package version instead). Each of the
three lines is followed by the apt hook's line, which runs after every dpkg run from now on
([kernel-update gate](02-kernel-driver.md#the-kernel-update-gate)):

```
n5-fangov: fan driver module present for 1 kernel(s): 7.0.14-17-pve
```

When the file lies under `/root`, apt prints `Notice: Download is performed unsandboxed
as root as file '/root/…' couldn't be accessed by user '_apt'` first — harmless, apt
cannot read `/root` as `_apt` and reads it as root instead. A package update restarts a
running daemon ([Updates](09-updates.md#package-update)); `dpkg -V n5-fangov` checks the
installed files against the package's md5sums (carried since 0.3.1). Then
`n5-fangov setup`.

**Switching from the installer to the package.** The installer's units live in
`/etc/systemd/system/`, the package's in `/lib/systemd/system/` — and systemd prefers
the `/etc` copy, so a leftover installer unit shadows every package update. From
0.3.1 the package's postinst removes the installer's copies when they are identical
to the packaged units, warns when they differ, and restarts a running daemon so the
packaged binary takes over; from then on `install.sh` and `uninstall.sh` refuse to run
on a package-installed host (apt maintains it). On older versions remove the copies by
hand:

```
rm /etc/systemd/system/n5-fangov.service /etc/systemd/system/n5-fangov-onfailure.service && systemctl daemon-reload
```

## What the installer does

`deploy/install.sh` looks for the binary at `dist/n5-fangov` (or `./n5-fangov`), refuses
to run without one, and puts the binary, the units, the apt hook, the log directory, the
config example (`/usr/share/doc/n5-fangov/config.example.toml`) and (on PVE) the
notification template pair in place; then it enables the unit. It writes **no** config
and starts nothing — that is `setup`. An existing `/etc/n5-fangov/config.toml` is left
untouched. What it prints on a fresh box (the first line is the sha256 check of the
binary):

```
n5-fangov: OK
--- predecessor n5-fand ---
  n5-fand files stay installed; remove with n5pro-ec/deploy/uninstall.sh when n5-fangov is proven.
--- files ---
  /etc/n5-fangov/config.toml absent: n5-fangov setup writes it (reference: /usr/share/doc/n5-fangov/config.example.toml)
  PVE notification template installed (alerts -> Proxmox notifications, template 'n5-fangov')
--- apt hook (kernel gate) ---
  /etc/apt/apt.conf.d/90n5-fangov: 'n5-fangov check --after-update' runs after every dpkg run
--- systemd ---
  enabled (not started)

Installed n5-fangov 0.3.1. Log file: /var/log/n5-fangov/n5-fangov.log (rotating; journal unchanged).
run: n5-fangov setup
```

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
| `/var/lib/n5-fangov/` (0700) | `sessions.json` (hashed dashboard sessions), `tokens.json` (hashed [API tokens](08-https-security.md#api-tokens)), `alerts.json` (recent alerts), `history.json` (chart history 2 h / 24 h / 7 d, saved every 10 min); all 0600, removed on uninstall — the settings bundle never contains them |
| `/run/n5-fangov/` (0750) | `n5-fangov.sock` (CLI), `state.json` (fallback for `status`), override and alert stamps |
| `/usr/share/doc/n5-fangov/config.example.toml` | reference with every key explained ([Configuration](06-configuration.md)) |
| `/usr/share/n5-fangov/pve-notification/*.hbs` | copied to `/etc/pve/notification-templates/default/` when `/etc/pve` exists |

Next: [Kernel driver](02-kernel-driver.md) (N5 Pro) · [Setup](03-setup.md) ·
[Updates, rollback, uninstall](09-updates.md)
