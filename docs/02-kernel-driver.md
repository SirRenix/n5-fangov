# Kernel driver (Minisforum N5 Pro)

What this page covers: why the N5 Pro needs an out-of-tree kernel module, what DKMS does
with it, how to install and verify it, the kernel-update gate, and how to remove it.
Other boards skip to [Other boards](#other-boards).

- [Why an out-of-tree module](#why-an-out-of-tree-module)
- [What DKMS does for you](#what-dkms-does-for-you)
- [Install](#install)
- [Driver options and autoload](#driver-options-and-autoload)
- [Verify](#verify)
- [The kernel-update gate](#the-kernel-update-gate)
- [Rebuild the module by hand](#rebuild-the-module-by-hand)
- [Remove](#remove)
- [Other boards](#other-boards)

## Why an out-of-tree module

The N5 Pro exposes no fan control to Linux at all: its fans hang on an ITE IT5571
embedded controller (EC) that no in-tree driver knows, and the BIOS "silent" curve lets
the drives sit at 40–42 °C while offering only fixed PWM. The community driver
[`ltdstudio/minisforum-n5-it5571`](https://github.com/ltdstudio/minisforum-n5-it5571)
exposes the EC's fan channels as a normal hwmon device (`pwm1..4`, `fan*_input`,
temperatures), which is what n5-fangov's `n5pro` profile regulates.

`fancontrol` would not do: it regulates one channel from one sensor, while the HDD group
needs the hottest of four drives and the SSD fan the hottest of three NVMe.

One EC property shapes everything else: the IT5571 **does not resume automatic
regulation of the HDD channel after any write** (measured 2026-09-14 on BIOS 1.05; only
a cold boot brings it back). A controller that hands control back to the EC on exit
leaves the drives unregulated, so n5-fangov never sets that channel to `auto` — it stops
to a fixed safe duty instead ([stop](06-configuration.md#curve-rules)).

Validation data, the measurement scripts and the Bash predecessor `n5-fand` live in
the sibling repository
[`SirRenix/minisforum-n5pro-fan-proxmox`](https://github.com/SirRenix/minisforum-n5pro-fan-proxmox).

## What DKMS does for you

The module is not in the distribution kernel, so it cannot come as a normal binary
package: a kernel module only loads into the exact kernel it was built against, and
Proxmox ships new kernels regularly. DKMS keeps the driver **source** under `/usr/src/`
and rebuilds and installs the module automatically for every new kernel apt installs
(`AUTOINSTALL="yes"`), as long as the headers for that kernel are present. The sibling
repository packages it as DKMS module `minisforum-n5-it5571/<ver>`.

When DKMS cannot build (headers missing, build error), the next boot has no `pwm*`
files. That is what the [kernel-update gate](#the-kernel-update-gate) is for.

## Install

Prerequisites on the box: `build-essential git dkms proxmox-headers-$(uname -r)`
(Debian: `linux-headers-$(uname -r)`). Then, as root:

```
git clone https://github.com/SirRenix/minisforum-n5pro-fan-proxmox.git
cd minisforum-n5pro-fan-proxmox && ./scripts/02-build-tools.sh   # fetches the upstream driver source
./deploy/install.sh
```

`install.sh` registers, builds and installs the DKMS module for the running kernel and
writes the two autoload files below. It also installs the older Bash regulator
`n5-fand` — n5-fangov's installer stops and disables it again, so running the two
installers in this order is fine ([Install](01-install.md#what-the-installer-does)).
A DKMS `.deb` that replaces this step is planned in the sibling repository.

## Driver options and autoload

`/etc/modprobe.d/minisforum-n5-it5571.conf`:

```
options minisforum_n5_it5571 experimental_write=1
```

The N5 Pro profile is marked *experimental* upstream. Without this option the driver
loads but keeps the `pwm*` nodes invisible, `n5-fangov detect` finds no profile and
`setup` refuses. The option changes nothing on the EC: loading the module writes nothing,
`pwm*_enable` starts at `2` (EC automatic) and only the regulator writes.

`/etc/modules-load.d/minisforum-n5-it5571.conf` holds the module name so it is loaded
at boot — the daemon's sandbox cannot load modules itself.

## Verify

```
dkms status minisforum-n5-it5571        # "installed" for the running kernel
lsmod | grep minisforum                 # module loaded
ls /sys/class/hwmon/*/pwm1              # pwm nodes visible (experimental_write=1)
sensors                                 # lm-sensors: the EC's fans and temperatures
n5-fangov detect                        # after the n5-fangov install: profile n5pro detected
```

The device's number under `/sys/class/hwmon/` (`hwmonN`) is **not stable across
boots** — `hwmon14` on one boot, `hwmon10` on the next, depending on the order the
drivers register. Find the device by name and never hard-code the number:

```
grep -l minisforum_n5_it5571 /sys/class/hwmon/*/name
```

n5-fangov does the same at every start (`detect`, `setup`, `status` print the path it
found this time).

`n5-fangov check` reports `dkms` for the running kernel at every daemon start
(`ExecStartPre`).

## The kernel-update gate

If DKMS did not build the module for a new kernel, the next boot has no `pwm` files:
`check` fails, the daemon does not start and the fans stay under EC/BIOS control. That is
safe for the hardware — the EC runs its BIOS curve for the CPU and SSD fans — but the HDD
channel sits at the BIOS start value and nothing regulates it by drive temperature.
Two gates catch this before it happens:

1. `/etc/apt/apt.conf.d/90n5-fangov` runs `n5-fangov check --after-update` after every
   dpkg run. It requires `updates/dkms/minisforum_n5_it5571.ko` for every kernel the
   box can **boot into**: the running one plus what `proxmox-boot-tool kernel list`
   selects (manually, automatically, pinned); without that tool the running one plus
   the newest installed. Missing → `kernel X: fan driver module missing — run: dkms
   install minisforum-n5-it5571/<ver> -k X` on the apt output plus a `kernel` alert
   (30 min cooldown). Older kernels that are merely still installed (apt keeps two)
   get an `info only` line and no alert. The apt run never fails because of it.
   Non-N5-Pro boxes: no-op. The good case is one line at the end of every apt run:

   ```
   n5-fangov: fan driver module present for 2 kernel(s): 7.0.12-1-pve 7.0.14-17-pve
   ```

   To see the hook work without waiting for a kernel: `n5-fangov check --after-update`
   by hand (exit 0 when every bootable kernel has its module), or reinstall any small
   package that is already installed — `apt install --reinstall lm-sensors` — and read
   the line at the end of the apt output. A kernel reinstall is not needed.
2. `ExecStartPre=n5-fangov check` reports `dkms` for the running kernel at every start;
   without the module there is no hwmon device, the start fails loudly and the
   onfailure alert names it.

What a Proxmox upgrade **can** affect for the driver: the kernel (above) and `dkms`
itself. n5-fangov's own files are covered in [Updates](09-updates.md).

## Rebuild the module by hand

When the gate reports a missing module, or after a reboot the fans run the BIOS curve:

```
dkms status
apt install proxmox-headers-$(uname -r)              # if the headers are missing
dkms install minisforum-n5-it5571/<ver> -k $(uname -r)
modprobe minisforum_n5_it5571
systemctl restart n5-fangov
```

`<ver>` is the version `dkms status` prints. For a kernel that is not running yet,
give its version with `-k`.

## Remove

Stop the regulator first; then the sibling repository's `deploy/uninstall.sh` removes
`n5-fand`, the autoload files and the DKMS module, and sets every channel back to
`pwm*_enable = 2`. By hand:

```
systemctl stop n5-fangov
dkms remove minisforum-n5-it5571/<ver> --all
rm /etc/modprobe.d/minisforum-n5-it5571.conf /etc/modules-load.d/minisforum-n5-it5571.conf
modprobe -r minisforum_n5_it5571
```

The HDD channel regains BIOS regulation only after a **cold boot** (the EC property
above); until then it keeps the last duty written. n5-fangov's own uninstall does not
touch the module ([Uninstall](09-updates.md#uninstall)).

## Other boards

Nuvoton NCT67xx and ITE IT87xx chips are served by `nct6775` / `it87` from the
distribution kernel — no DKMS, no extra package, nothing on this page applies. The
`nct67xx` / `it87xx` profiles ship *from documentation, untested*; the dashboard's
Compatibility card on the About page says so per profile.

Next: [Install](01-install.md) · [Setup](03-setup.md) ·
[Troubleshooting](10-troubleshooting.md)
