// wiring_sysinfo.go adapts internal/sysinfo (DESIGN "System inventory"):
// the collector behind GET /api/system and the CLI's offline collection.
package main

import (
	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/profile"
	"github.com/SirRenix/n5-fangov/internal/sysinfo"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// systemCollector caches the static hardware inventory (10 min) and reads
// the live parts per call.
type systemCollector = sysinfo.Collector

// systemInfo is the inventory document (GET /api/system).
type systemInfo = sysinfo.Info

// newSystemCollector builds the collector for the running device: the
// profile name and hwmon path identify the fan controller, the sysfs root
// follows the hwmon FS (N5FANGOV_SYSFS). dev may be nil (no fan controller
// section then).
func newSystemCollector(fs *hwmon.FS, dev profile.Device) *systemCollector {
	var fan sysinfo.FanController
	if dev != nil {
		fan.Profile, fan.Hwmon = dev.Profile().Name(), dev.HwmonPath()
	}
	return sysinfo.NewCollector(sysinfo.Options{Sysfs: fs.Root, Fan: fan})
}

// collectSystem is a one-shot local collection (CLI without a daemon).
func collectSystem(fs *hwmon.FS, dev profile.Device) systemInfo {
	return newSystemCollector(fs, dev).Collect()
}

// applySystemDeps sets web.Deps.System; a typed nil stays a nil closure.
func applySystemDeps(deps *web.Deps, c *systemCollector) {
	if c != nil {
		deps.System = func() any { return c.Collect() }
	}
}
