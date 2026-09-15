// Package hwmon provides sysfs hwmon discovery and read/write helpers.
// The sysfs root is overridable via PVEFAND_SYSFS so tests can use a fake tree
// (see testdata/sysfs/n5pro).
package hwmon

import "os"

// FS is a view on a sysfs root ("/sys" by default).
type FS struct {
	Root string
}

// New returns an FS rooted at PVEFAND_SYSFS or /sys.
func New() *FS {
	if r := os.Getenv("PVEFAND_SYSFS"); r != "" {
		return &FS{Root: r}
	}
	return &FS{Root: "/sys"}
}

// Device is one hwmon device directory.
type Device struct {
	Path string // e.g. /sys/class/hwmon/hwmon14
	Name string // content of <Path>/name
}
