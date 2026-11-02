# Kernel driver (Minisforum N5 Pro)

The N5 Pro needs an out-of-tree kernel module for its fans. This page covers the
module, DKMS, install, verify, the kernel-update gate and removal. Other boards:
[Other boards](#other-boards).

Tested 2026-11-02 on the reference N5 Pro (N5 PRO / F8NAA, BIOS 1.05, EC 0.14): package
`minisforum-n5-it5571-dkms` `0.2.1+sr2-1`, built by DKMS for Proxmox kernels 7.0.14-26-pve
and 7.0.12-1-pve, installed over `0.2.1+sr1-1` and reloaded with the regulator stopped.
The driver's hardware status stays `experimental`; its test results (TESTSPEC runs of
`0.2.1+sr1`) are in the driver repository's `befunde/RESULTS.md`. Other kernel or driver
versions are untested. The
[gate](#the-kernel-update-gate) tells you when a kernel has no module or an old build.

## Why an out-of-tree module

The N5 Pro's fans hang on an ITE IT5571 embedded controller (EC) that no in-tree
driver knows; the BIOS "silent" curve lets the drives sit at 40–42 °C. The community
driver [`ltdstudio/minisforum-n5-it5571`](https://github.com/ltdstudio/minisforum-n5-it5571)
exposes the EC's fan channels as a normal hwmon device (`pwm1..4`, `fan*_input`,
temperatures), which the `n5pro` profile regulates. The package is a Debian/Proxmox
fork of upstream v0.2.1 with a patch series on top.

After the hand-back to EC automatic the EC **regulates the HDD channel again**: on the
reference N5 Pro (BIOS 1.05) it steps DCR3 and DCR4 down in turn from the written value
(measured 2026-10-17: 0xb4 → 0x72/0x6a within 15 s, fan3 2736 → 2039 RPM, not yet
settled). According to EC firmware 0.14 (derived, not measured) it regulates to a target
speed ±100 RPM taken from a table on EC 0x06: 1200 RPM below 34 °C, 1600 RPM from 34 to
99 °C. Whether and how that follows the disk temperature is **open**. So n5-fangov never
sets that channel to `auto` and stops it at a fixed safe duty instead
([Curve rules](06-configuration.md#curve-rules)).
TESTSPEC T2/T3 measure this (`n5-fangov validate` or `n5-ectest handback`; [TESTSPEC](#hardware-status-and-validation)).
Driver `0.2.1+sr1` and later set HDD and PCIe to 255 (CPU/SSD to EC automatic) on reboot
and power-off, so the stop duty does not survive a reboot there. During the next boot
BIOS/EC set HDD to 100 (0x64; measured 2026-10-17 after one cold and one warm start), below
the stop duty 140, until n5-fangov takes over; PCIe keeps its last value over a warm start.

The DKMS package, validation data and measurement scripts:
[`SirRenix/minisforum-n5pro-fan-proxmox`](https://github.com/SirRenix/minisforum-n5pro-fan-proxmox).

## Hardware status and validation

```
n5-fangov identify
```

prints product, board, BIOS, EC firmware, kernel, the driver's version, parameters and
mode, and the **hardware status** of this combination: `validated` (TESTSPEC T1–T3 PASS
on at least two devices), `tested` (one TESTSPEC run, bundle published),
`experimental` (loads, no TESTSPEC run) or `unknown` (not in the table). The table
comes from `befunde/RESULTS.md` of the driver repository and is built into n5-fangov.
The reference N5 Pro (F8NAA, BIOS 1.05, EC 0.14) is `experimental` until its TESTSPEC
run. `check` warns on anything below `tested`; `serve` logs it as a journal warning only when it
changed since the last start (record in the state directory), otherwise at info level; the
dashboard shows the status next to the profile.

The test specification (version 0.3) is run with `n5-fangov validate`
([CLI](05-cli.md#validate)). `n5-ectest`, the test toolset of the driver repository
(`ectest/`), runs the same tests and records itself in `spec_deviations`. Both need the
register dump below.

## What DKMS does for you

A kernel module loads only into the kernel it was built against. DKMS keeps the driver
source under `/usr/src/` and rebuilds the module for every kernel apt installs
(`AUTOINSTALL="yes"`), as long as that kernel's headers are present — the package
pulls the header meta-package, so every new kernel brings its headers. The DKMS
module is `minisforum-n5-it5571/<ver>`. When DKMS cannot
build (headers missing, build error), the next boot has no `pwm*` files; the
[kernel-update gate](#the-kernel-update-gate) catches that before the reboot.

## Install

Download the package and its checksum from the
[latest release](https://github.com/SirRenix/minisforum-n5pro-fan-proxmox/releases/latest)
of the sibling repository, then as root:

```
sha256sum -c minisforum-n5-it5571-dkms_*_all.deb.sha256
apt install ./minisforum-n5-it5571-dkms_*_all.deb
```

The n5-fangov package lists it under `Suggests` (`>= 0.2.1+sr1~rc1`), never as a
dependency, so other boards never pull it in. The driver package is in no APT source:
install the driver `.deb` as above.

apt pulls `dkms` and, on Proxmox VE, `proxmox-default-headers` — it follows the default
kernel series the way `proxmox-default-kernel` does, so a new kernel series brings its
headers too (on Debian: `linux-headers-amd64`). The package builds the module for every
kernel that has headers, sets the driver option below and loads the module. Expected output, among apt's lines:

```
minisforum-n5-it5571: built and installed for 7.0.12-1-pve
minisforum-n5-it5571: built and installed for 7.0.14-17-pve
minisforum-n5-it5571: module loaded
```

A driver installed earlier with the sibling repository's `deploy/install.sh` is taken
over: the package unregisters the old DKMS version and removes the two `/etc` files when
they are unchanged. If the module was already loaded, the new build takes effect after
the next boot or a reload ([Rebuild the module by hand](#rebuild-the-module-by-hand)).

## Driver options and autoload

`/usr/lib/modprobe.d/minisforum-n5-it5571.conf`:

```
options minisforum_n5_it5571 experimental_write=1
```

| Parameter | Effect |
|---|---|
| `experimental_write=1` | from `0.2.1+sr1`: writes only on a board in the driver's write allowlist (N5 PRO / F8NAA) with the measured DMI vendor; elsewhere the driver stays read-only |
| `force=1` | bind on an unknown system; from `0.2.1+sr1` always read-only |
| `debug_regs=1` | read-only register dump `<debugfs>/minisforum_n5_it5571/dcr` for `validate` and `n5-ectest`; never put it into `modprobe.d` |

Without write permission the driver loads but hides the `pwm*` nodes. On a board the driver
writes on (N5 PRO/F8NAA, N5/F8NAA) n5-fangov then refuses to start, also with
`profile = "auto"`: `check` (the unit's `ExecStartPre`) fails
with the reason (`force-loaded`, `experimental_write=N`, allowlist or vendor) and sends
the `driver` alert; after 5 failed starts the unit stays `failed` and the onfailure unit
alerts `failed`. The same happens when the driver is missing on such a board (not installed,
not built for the running kernel, not loaded). On a board the driver keeps read-only (N5 Air,
N5 PRO/F8NAB, any DMI `sys_vendor` other than "Micro Computer (HK) Tech Limited") `profile = "auto"` runs as `monitor`, as in 0.4.2. For sensors only, set `[daemon] profile = "monitor"`. Loading the
module writes nothing; `pwm*_enable` starts at `2` (EC automatic), and only the
regulator writes.

The driver answers `pwmN` and `pwmN_enable` from its own cache: n5-fangov's read-back
after a write checks what the driver accepted, not the EC. With `debug_regs=1` (validation
only) n5-fangov also compares the written duty with the EC register, once the EC had
time to take it: the HDD channel ramps DCR3/DCR4 to a new value over about 4.6 s, the
other channels lag as well (measured 2026-10-17), so the register is compared 5 s (pwm3)
or 2 s (pwm1, pwm2, pwm4) after the write. The daemon compares in a later cycle, without
waiting; a difference is a journal warning (`EC register deviation`, at most one per
channel per 5 minutes), not a write error. The `failsafe` command compares after all
writes and reads the register until it matches or the settle time has passed; `validate`
does the same after a restore and records a difference in the bundle. A read of the
register dump that does not return within 5 s is abandoned.

At boot udev loads the module through its DMI alias on every board in the driver's table;
packages up to `0.2.1+sr1` also shipped `/usr/lib/modules-load.d/minisforum-n5-it5571.conf`,
`0.2.1+sr2` drops it. To keep the module from loading, put
`blacklist minisforum_n5_it5571` into `/etc/modprobe.d/`. The daemon's sandbox cannot load
modules itself. A file of the same name under `/etc/modprobe.d/` replaces the package's
option file.

## Drive temperatures

SATA drive temperatures come from the in-tree `drivetemp` module, which reads them via
SMART and exposes one hwmon device per drive. Nothing loads it by default. The N5 Pro
preset's `hdd` channel uses `drivetemp:max`; without the module the channel reports
`sensor-error` and sits at its stop duty (140).

```
modprobe drivetemp
echo drivetemp > /etc/modules-load.d/drivetemp.conf   # load at boot
```

NVMe drives need nothing extra: the `nvme` driver has its own hwmon device (`nvme:max`).

## Verify

```
dkms status minisforum-n5-it5571        # "installed" for the running kernel
lsmod | grep minisforum                 # module loaded
ls /sys/class/hwmon/*/pwm1              # pwm nodes visible (writes allowed)
sensors                                 # lm-sensors: the EC's fans and temperatures
grep -l drivetemp /sys/class/hwmon/*/name   # one line per SATA drive (hdd channel)
n5-fangov identify                      # driver version, parameters, mode, hardware status
n5-fangov detect                        # profile n5pro detected
```

The device's number under `/sys/class/hwmon/` (`hwmonN`) changes between boots. Find
the device by name, never hard-code the number:

```
grep -l minisforum_n5_it5571 /sys/class/hwmon/*/name
```

n5-fangov does the same at every start; `detect`, `setup` and `status` print the path
found this time. `n5-fangov check` reports `dkms` for the running kernel and the loaded
module's version against the installed package at every daemon start (`ExecStartPre`).

## The kernel-update gate

Without the module for the running kernel there are no `pwm` files: `check` fails, the
daemon does not start, the fans stay under EC/BIOS control. The CPU and SSD fans then
run the BIOS curve; the HDD channel runs whatever the EC sets at boot. Two gates catch
this:

1. `/etc/apt/apt.conf.d/90n5-fangov` runs `n5-fangov check --after-update` after every
   dpkg run. For every kernel the box can **boot into** (the running one plus what
   `proxmox-boot-tool kernel list` selects; without that tool, the running one plus the
   newest installed) it requires `updates/dkms/minisforum_n5_it5571.ko` and compares its
   `modinfo` version with the version the installed package builds. The good case is
   one line at the end of every apt run:

   ```
   n5-fangov: fan driver module present for 2 kernel(s), version 0.2.1+sr1~rc2: 7.0.12-1-pve 7.0.14-20-pve
   ```

   A missing module or an old build prints the kernel and
   `reinstall the driver package: apt install --reinstall minisforum-n5-it5571-dkms`,
   and raises a `kernel` alert (30 min cooldown). Older kernels that are merely still
   installed get an `info only` line and no alert. The apt run never fails because of
   it. Non-N5-Pro boxes: no-op. To see the hook work without a new kernel, run
   `n5-fangov check --after-update` by hand.
2. `ExecStartPre=n5-fangov check` reports `dkms` for the running kernel at every start.
   Without the module the start fails loudly and the onfailure alert names it.

A Proxmox upgrade can affect the kernel and `dkms` itself; n5-fangov's own files:
[Updates](09-updates.md).

## Rebuild the module by hand

When the gate reports a missing or old module, or the fans run the BIOS curve after a
reboot:

```
apt install proxmox-headers-$(uname -r)              # if the headers are missing
apt install --reinstall minisforum-n5-it5571-dkms    # builds for every kernel with headers
systemctl stop n5-fangov
modprobe -r minisforum_n5_it5571; modprobe minisforum_n5_it5571
systemctl start n5-fangov
n5-fangov identify                                   # driver version = package version
```

For a kernel that is not running yet, the reinstall is enough; skip the reload.
Stopping the daemon first puts the fans into their safe state while the module is out.

## Remove

Stop the regulator first:

```
systemctl stop n5-fangov
apt remove minisforum-n5-it5571-dkms
modprobe -r minisforum_n5_it5571
```

Unloading sends every channel the module touched back to EC automatic; the HDD channel
then leaves the stop duty and the EC regulates it to its own target; whether that target
follows the disk temperature is open (see above).
n5-fangov's own uninstall does not touch the module ([Uninstall](09-updates.md#uninstall)).

## Other boards

Nuvoton NCT67xx and ITE IT87xx chips are served by `nct6775` / `it87` from the
distribution kernel: no DKMS, no extra package, nothing on this page applies. The
`nct67xx` / `it87xx` profiles are *from documentation, untested* (hardware status
`unknown`); the Compatibility card on the About page says so per profile.
