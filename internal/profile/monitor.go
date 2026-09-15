package profile

import (
	"fmt"

	"github.com/SirRenix/ventula/internal/hwmon"
)

// monitor is the fallback profile: sensors only, no fan control at all.
type monitor struct{}

// Monitor returns the monitoring-only profile.
func Monitor() Profile { return monitor{} }

func (monitor) Name() string   { return "monitor" }
func (monitor) Title() string  { return "Monitoring only (no fan control)" }
func (monitor) Verified() bool { return false }
func (monitor) Notes() string {
	return "No supported fan controller detected. Temperatures are read and shown, " +
		"every write request is refused. Fans stay under BIOS/EC control."
}

// Detect succeeds as soon as at least one hwmon device exists.
func (p monitor) Detect(fs *hwmon.FS) (Device, error) {
	devs, err := fs.List()
	if err != nil {
		return nil, err
	}
	if len(devs) == 0 {
		return nil, fmt.Errorf("%w: no hwmon devices under %s", ErrNotFound, fs.ClassDir())
	}
	return &monitorDevice{prof: p, path: fs.ClassDir()}, nil
}

type monitorDevice struct {
	prof Profile
	path string
}

func (d *monitorDevice) Profile() Profile              { return d.prof }
func (d *monitorDevice) HwmonPath() string             { return d.path }
func (d *monitorDevice) Channels() []Channel           { return nil }
func (d *monitorDevice) ReadRPM(int) (int, error)      { return 0, ErrMonitorOnly }
func (d *monitorDevice) ReadDuty(int) (int, error)     { return 0, ErrMonitorOnly }
func (d *monitorDevice) EnterManual(int) error         { return ErrMonitorOnly }
func (d *monitorDevice) WriteDuty(int, int) error      { return ErrMonitorOnly }
func (d *monitorDevice) SafeStop(int, string) error    { return ErrMonitorOnly }
func (d *monitorDevice) ExtraTemps() map[string]string { return map[string]string{} }
