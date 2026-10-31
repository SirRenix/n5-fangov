package profile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/hwstatus"
	"github.com/SirRenix/n5-fangov/internal/n5driver"
)

// All returns every profile in detection order: n5pro first, the
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
// whose Detect succeeds) or a specific profile name. Under "auto" a board
// the driver opens for writes (hwstatus.WriteCapable) does not fall back to
// monitor: an N5 Pro driver without pwm nodes stops the search with
// ErrWriteDisabled, a missing driver with ErrDriverMissing. A read-only
// board of the hardware table (N5 Air, N5 PRO/F8NAB) runs as monitor in
// both cases, as in 0.4.2 (audit 2026-10-16 G3). A system outside the
// table keeps ErrWriteDisabled (force-loaded driver). Detection never
// writes to sysfs; the returned Device does not touch hardware until a
// write method is called.
func Detect(fs *hwmon.FS, want string) (Device, error) {
	want = strings.TrimSpace(want)
	if want == "" || want == "auto" {
		id := hwstatus.ReadIdentity(fs.Root)
		readOnlyBoard := n5Board(id) && !hwstatus.WriteCapable(id)
		var errs []string
		for _, p := range All() {
			if p.Name() == "monitor" {
				// last resort reached: on a board the driver writes on,
				// the missing driver is a fault, not "monitoring only"
				if n5Board(id) && hwstatus.WriteCapable(id) {
					return nil, fmt.Errorf("%w: DMI %s / %s is an N5 board with driver write support, but no hwmon named %s exists (package %s not installed, not built for this kernel, or not loaded: modinfo %s, lsmod). Set [daemon] profile = \"monitor\" to run sensors only",
						ErrDriverMissing, id.Product, id.Board, N5ProHwmonName, n5driver.PackageName, n5driver.ModuleName)
				}
			}
			dev, err := p.Detect(fs)
			if err == nil {
				return dev, nil
			}
			if errors.Is(err, ErrWriteDisabled) && !readOnlyBoard {
				// the N5 Pro driver is there but read-only on a board it
				// should write on: no silent fallback to monitor (check
				// fails, serve alerts)
				return nil, err
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

// ErrDriverMissing is returned by auto-detection when the DMI names a
// board of the hardware table (an N5 model) but the N5 Pro EC driver has
// no hwmon device: without it auto would fall back to monitor silently.
var ErrDriverMissing = errors.New("profile: N5 EC driver missing")

// n5Board reports whether product and board are a row of the hardware
// status table (any BIOS/EC).
func n5Board(id hwstatus.Identity) bool {
	if id.Product == "" || id.Board == "" {
		return false
	}
	for _, e := range hwstatus.Table() {
		if e.Product == id.Product && e.Board == id.Board {
			return true
		}
	}
	return false
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
