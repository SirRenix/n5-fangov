package profile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
)

// All returns every profile in detection order: the verified one first, the
// documented-but-untested chips next, monitoring as the last resort.
func All() []Profile {
	return []Profile{N5Pro(), NCT67xx(), IT87xx(), Monitor()}
}

// ByName returns the profile with the given short name.
func ByName(name string) (Profile, bool) {
	for _, p := range All() {
		if p.Name() == name {
			return p, true
		}
	}
	return nil, false
}

// Detect resolves a Device. want is "auto" (first profile in All() order
// whose Detect succeeds) or a specific profile name. Detection never writes
// to sysfs; the returned Device does not touch hardware until a write method
// is called.
func Detect(fs *hwmon.FS, want string) (Device, error) {
	want = strings.TrimSpace(want)
	if want == "" || want == "auto" {
		var errs []string
		for _, p := range All() {
			dev, err := p.Detect(fs)
			if err == nil {
				return dev, nil
			}
			errs = append(errs, p.Name()+": "+err.Error())
		}
		return nil, fmt.Errorf("profile: auto-detection failed (%s)", strings.Join(errs, "; "))
	}
	p, ok := ByName(want)
	if !ok {
		return nil, fmt.Errorf("profile: unknown profile %q (known: %s)", want, strings.Join(Names(), ", "))
	}
	dev, err := p.Detect(fs)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, fmt.Errorf("profile %s: %w", want, err)
		}
		return nil, err
	}
	return dev, nil
}

// Names lists the short names of all profiles in detection order.
func Names() []string {
	ps := All()
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name())
	}
	return out
}
