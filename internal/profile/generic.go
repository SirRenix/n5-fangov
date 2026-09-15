package profile

import (
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
)

// generic covers Super-I/O chips driven by the standard hwmon pwm interface
// (nct6775, it87). Nothing here has been validated on real hardware; the
// auto-mode values come from the kernel documentation.
type generic struct {
	name     string
	title    string
	notes    string
	match    *regexp.Regexp
	fallback string // pwmN_enable value for "auto" when the original could not be read
}

// NCT67xx returns the profile for Nuvoton NCT67xx chips (kernel driver
// nct6775). Untested.
func NCT67xx() Profile {
	return &generic{
		name:  "nct67xx",
		title: "Nuvoton NCT67xx Super-I/O (nct6775 driver)",
		notes: "UNTESTED - from kernel documentation (Documentation/hwmon/nct6775.rst) only. " +
			"pwmN_enable: 0=full speed, 1=manual, 2=thermal cruise, 3=fan speed cruise, 4=SmartFan III, 5=SmartFan IV. " +
			"The value found at detection is restored on stop=\"auto\"; if it cannot be read, 5 (SmartFan IV) is used. " +
			"Verify each channel with `n5-fangov test` before trusting a curve.",
		match:    regexp.MustCompile(`^nct67\d\d$`),
		fallback: "5",
	}
}

// IT87xx returns the profile for ITE IT86xx/IT87xx chips (kernel driver
// it87). Untested.
func IT87xx() Profile {
	return &generic{
		name:  "it87xx",
		title: "ITE IT86xx/IT87xx Super-I/O (it87 driver)",
		notes: "UNTESTED - from kernel documentation (Documentation/hwmon/it87.rst) only. " +
			"pwmN_enable: 0=off (full speed on most boards), 1=manual, 2=automatic (chip curve). " +
			"The value found at detection is restored on stop=\"auto\"; if it cannot be read, 2 is used. " +
			"Some boards need the module parameter ignore_resource_conflict=1. " +
			"Verify each channel with `n5-fangov test` before trusting a curve.",
		match:    regexp.MustCompile(`^it8[67]\d\d$`),
		fallback: "2",
	}
}

func (g *generic) Name() string   { return g.name }
func (g *generic) Title() string  { return g.title }
func (g *generic) Verified() bool { return false }
func (g *generic) Notes() string  { return g.notes }

// Detect finds the first hwmon device matching the chip name regexp, scans
// its pwm channels and captures the original pwmN_enable values so that
// SafeStop("auto") can restore them. It never writes.
func (g *generic) Detect(fs *hwmon.FS) (Device, error) {
	devs := fs.FindByRegexp(g.match)
	if len(devs) == 0 {
		return nil, fmt.Errorf("%w: no hwmon matching %s", ErrNotFound, g.match)
	}
	hw := devs[0]
	chans, err := scanChannels(fs, hw.Path)
	if err != nil {
		return nil, fmt.Errorf("profile %s: scan %s: %w", g.name, hw.Path, err)
	}
	if len(chans) == 0 {
		return nil, fmt.Errorf("profile %s: %s (%s) exposes no pwmN/pwmN_enable pair", g.name, hw.Path, hw.Name)
	}
	original := map[int]string{}
	for _, c := range chans {
		v, err := fs.ReadString(filepath.Join(hw.Path, fmt.Sprintf("pwm%d_enable", c.Index)))
		if err != nil || v == "" {
			continue
		}
		original[c.Index] = v
	}
	return &pwmDevice{
		prof:  g,
		fs:    fs,
		path:  hw.Path,
		chans: chans,
		extra: map[string]string{},
		autoValue: func(ch int) string {
			if v, ok := original[ch]; ok && v != "1" {
				return v
			}
			// captured while already manual (e.g. daemon restart after a
			// crash): there is no original to restore, use the documented
			// automatic mode
			return g.fallback
		},
	}, nil
}
