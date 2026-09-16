package hwmon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Block devices: the disk temperature of <Root>/block/<dev> lives in the
// hwmon of its device (drivetemp for SATA/SAS, the controller's hwmon for
// NVMe). internal/sensor ("disk:<dev>") and internal/sysinfo (the System
// tab) read the same file through DiskHwmon, so both show the same value.

// blockDevRe is the name rule of a block device the daemon addresses.
var blockDevRe = regexp.MustCompile(`^[a-z0-9]{1,32}$`)

// ValidBlockDev reports whether dev is a usable block device name.
func ValidBlockDev(dev string) bool { return blockDevRe.MatchString(dev) }

// virtualBlock lists /sys/block name prefixes that are not physical disks.
var virtualBlock = []string{"zd", "loop", "dm-", "ram", "md", "nbd", "drbd", "rbd", "zram", "sr", "fd"}

// VirtualBlock reports whether a /sys/block entry is a virtual device
// (zvol, loop, device-mapper, md, ramdisk, network block, optical, floppy).
func VirtualBlock(name string) bool {
	for _, p := range virtualBlock {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// ErrNoDiskHwmon is wrapped by DiskHwmon when the block device exists but
// exposes no hwmon with a temp1_input.
var ErrNoDiskHwmon = errors.New("hwmon: block device has no temperature sensor")

// BlockDevices lists the physical block devices under <Root>/block (sorted
// by name, virtual devices skipped). A missing directory reads as no
// devices.
func (fs *FS) BlockDevices() []string {
	entries, err := os.ReadDir(filepath.Join(fs.Root, "block"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if VirtualBlock(name) || !ValidBlockDev(name) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// DiskHwmon returns the hwmon directory that carries the temperature of the
// block device dev: block/<dev>/device/hwmon/hwmon*/ (SATA/SAS, drivetemp)
// or block/<dev>/device/hwmon*/ (NVMe controller), the first one with a
// temp1_input. An invalid or missing device is an error wrapping
// os.ErrNotExist; a device without a sensor wraps ErrNoDiskHwmon.
func (fs *FS) DiskHwmon(dev string) (string, error) {
	if !ValidBlockDev(dev) {
		return "", fmt.Errorf("hwmon: block device %q: invalid name: %w", dev, os.ErrNotExist)
	}
	base := filepath.Join(fs.Root, "block", dev)
	if st, err := os.Stat(base); err != nil || !st.IsDir() {
		return "", fmt.Errorf("hwmon: block device %s: %w", dev, os.ErrNotExist)
	}
	for _, pat := range []string{
		filepath.Join(base, "device", "hwmon", "hwmon*"),
		filepath.Join(base, "device", "hwmon*"),
	} {
		matches, _ := filepath.Glob(pat)
		type numbered struct {
			n int
			p string
		}
		var found []numbered
		for _, p := range matches {
			n, ok := hwmonIndex(filepath.Base(p))
			if !ok {
				continue
			}
			found = append(found, numbered{n, p})
		}
		sort.Slice(found, func(i, j int) bool { return found[i].n < found[j].n })
		for _, f := range found {
			if fs.Exists(filepath.Join(f.p, "temp1_input")) {
				return f.p, nil
			}
		}
	}
	return "", fmt.Errorf("hwmon: block device %s: %w", dev, ErrNoDiskHwmon)
}

// DiskTemp reads the temperature of block device dev in millidegrees.
func (fs *FS) DiskTemp(dev string) (int, error) {
	p, err := fs.DiskHwmon(dev)
	if err != nil {
		return 0, err
	}
	return fs.ReadInt(filepath.Join(p, "temp1_input"))
}
