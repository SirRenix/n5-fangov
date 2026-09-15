// Package profile defines hardware profiles: how a fan controller chip is
// detected and driven through sysfs. Only n5pro is hardware-verified.
package profile

import "github.com/SirRenix/ventula/internal/hwmon"

// Channel describes one PWM output of a device.
type Channel struct {
	Index   int    // pwmN index (1-based)
	Label   string // e.g. "CPU Fan"
	HasTach bool   // fanN_input present and meaningful
}

// Profile knows how to detect and instantiate a Device.
type Profile interface {
	Name() string   // short id: n5pro, nct67xx, it87xx, monitor
	Title() string  // human readable
	Verified() bool // true only when validated on real hardware
	Notes() string  // caveats shown in the UI
	Detect(fs *hwmon.FS) (Device, error)
}

// Device is a detected, drivable fan controller. Implementations must read
// back every write and return an error on mismatch (DESIGN rule 3).
type Device interface {
	Profile() Profile
	HwmonPath() string
	Channels() []Channel
	ReadRPM(ch int) (int, error)
	ReadDuty(ch int) (int, error)
	// EnterManual puts pwmN into manual mode (pwmN_enable=1). Idempotent.
	EnterManual(ch int) error
	// WriteDuty writes 0..255 and verifies by reading back.
	WriteDuty(ch int, duty int) error
	// SafeStop returns the channel to a safe state: stop=="auto" → profile
	// default (n5pro: pwmN_enable=2), otherwise a fixed duty "0".."255".
	SafeStop(ch int, stop string) error
	// ExtraTemps maps extra sensor ids (e.g. "ec:cpu") to temp*_input paths.
	ExtraTemps() map[string]string
}
