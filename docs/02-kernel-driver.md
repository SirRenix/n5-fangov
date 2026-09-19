# Kernel driver (Minisforum N5 Pro)

The N5 Pro needs an out-of-tree kernel module for its fans. This page covers the
module, DKMS, install, verify, the kernel-update gate and removal. Other boards:
[Other boards](#other-boards).

Verified 2026-09-19 (the 0.4.0 release gate): driver `minisforum-n5-it5571` 0.2.0,
built by DKMS for Proxmox kernels **7.0.14-17-pve** (running) and 7.0.12-1-pve
(fallback), Proxmox VE 9.2.20 / Debian 13.7, BIOS 1.05, lm-sensors 3.6.2. The reboot
proof dates from the 0.3.1 gate of 2026-09-18; the boot chain is unchanged since.
Other kernel or driver versions are untested. The [gate](#the-kernel-update-gate)
tells you when a kernel has no module.

## Why an out-of-tree module

The N5 Pro's fans hang on an ITE IT5571 embedded controller (EC) that no in-tree
driver knows; the BIOS "silent" curve lets the drives sit at 40–42 °C. The community
driver [`ltdstudio/minisforum-n5-it5571`](https://github.com/ltdstudio/minisforum-n5-it5571)
exposes the EC's fan channels as a normal hwmon device (`pwm1..4`, `fan*_input`,
temperatures), which the `n5pro` profile regulates.

The IT5571 **does not resume automatic regulation of the HDD channel after any
write**; only a cold boot brings it back. So n5-fangov never sets that channel to
`auto` and stops it to a fixed safe duty instead ([Curve rules](06-configuration.md#curve-rules)).

Validation data, measurement scripts and the Bash predecessor `n5-fand`:
[`SirRenix/minisforum-n5pro-fan-proxmox`](https://github.com/SirRenix/minisforum-n5pro-fan-proxmox).

## What DKMS does for you

A kernel module loads only into the kernel it was built against. DKMS keeps the driver
source under `/usr/src/` and rebuilds the module for every kernel apt installs
(`AUTOINSTALL="yes"`), as long as that kernel's headers are present. The sibling
repository packages it as DKMS module `minisforum-n5-it5571/<ver>`. When DKMS cannot
build (headers missing, build error), the next boot has no `pwm*` files; the
[kernel-update gate](#the-kernel-update-gate) catches that before the reboot.

## Install

Install `build-essential git dkms proxmox-headers-$(uname -r)` (Debian:
`linux-headers-$(uname -r)`). Then, as root:

```
git clone https://github.com/SirRenix/minisforum-n5pro-fan-proxmox.git
cd minisforum-n5pro-fan-proxmox && ./scripts/02-build-tools.sh   # fetches the upstream driver source
./deploy/install.sh
```

`install.sh` registers, builds and installs the DKMS module for the running kernel and
writes the two autoload files below. It also installs the Bash regulator `n5-fand`;
n5-fangov's installer stops and disables it again, so this order is fine
([What the installer does](01-install.md#what-the-installer-does)).

## Driver options and autoload

`/etc/modprobe.d/minisforum-n5-it5571.conf`:

```
options minisforum_n5_it5571 experimental_write=1
```

Without this option the driver loads but keeps the `pwm*` nodes invisible:
`n5-fangov detect` finds no profile and `setup` refuses. The option changes nothing on
the EC. Loading the module writes nothing, `pwm*_enable` starts at `2` (EC automatic),
and only the regulator writes.

`/etc/modules-load.d/minisforum-n5-it5571.conf` holds the module name so it loads at
boot. The daemon's sandbox cannot load modules itself.

## Verify

```
dkms status minisforum-n5-it5571        # "installed" for the running kernel
lsmod | grep minisforum                 # module loaded
ls /sys/class/hwmon/*/pwm1              # pwm nodes visible (experimental_write=1)
sensors                                 # lm-sensors: the EC's fans and temperatures
n5-fangov detect                        # after the n5-fangov install: profile n5pro detected
```

The device's number under `/sys/class/hwmon/` (`hwmonN`) changes between boots. Find
the device by name, never hard-code the number:

```
grep -l minisforum_n5_it5571 /sys/class/hwmon/*/name
```

n5-fangov does the same at every start; `detect`, `setup` and `status` print the path
found this time. `n5-fangov check` reports `dkms` for the running kernel at every daemon
start (`ExecStartPre`).

## The kernel-update gate

Without the module for the running kernel there are no `pwm` files: `check` fails, the
daemon does not start, the fans stay under EC/BIOS control. The CPU and SSD fans then
run the BIOS curve; the HDD channel sits at the BIOS start value, unregulated. Two
gates catch this:

1. `/etc/apt/apt.conf.d/90n5-fangov` runs `n5-fangov check --after-update` after every
   dpkg run. It requires `updates/dkms/minisforum_n5_it5571.ko` for every kernel the box
   can **boot into**: the running one plus what `proxmox-boot-tool kernel list` selects;
   without that tool, the running one plus the newest installed. The good case is one
   line at the end of every apt run:

   ```
   n5-fangov: fan driver module present for 2 kernel(s): 7.0.12-1-pve 7.0.14-17-pve
   ```

   A missing module prints `kernel X: fan driver module missing — run: dkms install
   minisforum-n5-it5571/<ver> -k X` and raises a `kernel` alert (30 min cooldown).
   Older kernels that are merely still installed get an `info only` line and no alert.
   The check exits 0 when every bootable kernel has its module; the apt run never
   fails because of it. Non-N5-Pro boxes: no-op. To see the hook work without a
   new kernel, run `n5-fangov check --after-update` by hand, or reinstall any small
   package (`apt install --reinstall lm-sensors`) and read the last line.
2. `ExecStartPre=n5-fangov check` reports `dkms` for the running kernel at every start.
   Without the module the start fails loudly and the onfailure alert names it.

A Proxmox upgrade can affect the kernel and `dkms` itself; n5-fangov's own files:
[Updates](09-updates.md).

## Rebuild the module by hand

When the gate reports a missing module, or the fans run the BIOS curve after a reboot:

```
dkms status
apt install proxmox-headers-$(uname -r)              # if the headers are missing
dkms install minisforum-n5-it5571/<ver> -k $(uname -r)
modprobe minisforum_n5_it5571
systemctl restart n5-fangov
```

`<ver>` is the version `dkms status` prints. For a kernel that is not running yet, give
its version with `-k`.

## Remove

Stop the regulator first. The sibling repository's `deploy/uninstall.sh` removes
`n5-fand`, the autoload files and the DKMS module, and sets every channel back to
`pwm*_enable = 2`. By hand:

```
systemctl stop n5-fangov
dkms remove minisforum-n5-it5571/<ver> --all
rm /etc/modprobe.d/minisforum-n5-it5571.conf /etc/modules-load.d/minisforum-n5-it5571.conf
modprobe -r minisforum_n5_it5571
```

The HDD channel regains BIOS regulation only after a **cold boot**; until then it keeps
the last duty written. n5-fangov's own uninstall does not touch the module
([Uninstall](09-updates.md#uninstall)).

## Other boards

Nuvoton NCT67xx and ITE IT87xx chips are served by `nct6775` / `it87` from the
distribution kernel: no DKMS, no extra package, nothing on this page applies. The
`nct67xx` / `it87xx` profiles are *from documentation, untested*; the Compatibility
card on the About page says so per profile.
