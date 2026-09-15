// Package sensor resolves sensor ids from the config ("k10temp", "nvme:max",
// "ec:system", ...) to readable temperature sources on a hwmon.FS.
package sensor

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/SirRenix/pvefand/internal/hwmon"
	"github.com/SirRenix/pvefand/internal/profile"
)

// Plausibility window in millidegrees. Readings outside are reported as
// errors so a stuck or garbage sensor triggers the failsafe path.
const (
	MinPlausible = -20000
	MaxPlausible = 120000
)

// ErrImplausible wraps readings outside MinPlausible..MaxPlausible.
var ErrImplausible = errors.New("sensor: implausible reading")

// ErrNoDevice is returned by Parse when the id is well-formed but nothing
// on this machine matches it.
var ErrNoDevice = errors.New("sensor: no matching hwmon device")

// Source is one resolved temperature input.
type Source interface {
	ID() string
	// Read returns the current temperature in millidegrees Celsius.
	Read() (int, error)
}

// Info describes a sensor id for the UI dropdown.
type Info struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// checkPlausible rejects readings outside the plausibility window.
func checkPlausible(id string, v int) (int, error) {
	if v < MinPlausible || v > MaxPlausible {
		return 0, fmt.Errorf("%w: %s = %d", ErrImplausible, id, v)
	}
	return v, nil
}

// single reads one temp*_input file.
type single struct {
	id   string
	fs   *hwmon.FS
	path string
}

func (s *single) ID() string { return s.id }

func (s *single) Read() (int, error) {
	v, err := s.fs.ReadInt(s.path)
	if err != nil {
		return 0, fmt.Errorf("sensor %s: %w", s.id, err)
	}
	return checkPlausible(s.id, v)
}

// maximum reads several temp*_input files and returns the highest value.
// Files that fail to read (device vanished, implausible) are ignored as
// long as at least one reading succeeds.
type maximum struct {
	id    string
	fs    *hwmon.FS
	paths []string
}

func (m *maximum) ID() string { return m.id }

func (m *maximum) Read() (int, error) {
	best := 0
	ok := false
	var firstErr error
	for _, p := range m.paths {
		v, err := m.fs.ReadInt(p)
		if err == nil {
			_, err = checkPlausible(m.id, v)
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !ok || v > best {
			best, ok = v, true
		}
	}
	if !ok {
		if firstErr == nil {
			firstErr = ErrNoDevice
		}
		return 0, fmt.Errorf("sensor %s: no readable input: %w", m.id, firstErr)
	}
	return best, nil
}

var (
	hwmonIDRe = regexp.MustCompile(`^hwmon:([^:]+):temp([0-9]+)$`)
	tempInRe  = regexp.MustCompile(`^temp([0-9]+)_input$`)
)

// Parse resolves id against fs (and dev for "ec:*" ids; dev may be nil for
// all other ids). Device lookup happens once here; Read only touches the
// resolved files.
func Parse(id string, fs *hwmon.FS, dev profile.Device) (Source, error) {
	id = strings.TrimSpace(id)
	if fs == nil {
		return nil, errors.New("sensor: nil hwmon.FS")
	}
	switch {
	case id == "":
		return nil, errors.New("sensor: empty id")
	case id == "k10temp":
		devs := fs.FindByName("k10temp")
		if len(devs) == 0 {
			return nil, fmt.Errorf("%w: k10temp (AMD CPU)", ErrNoDevice)
		}
		return &single{id: id, fs: fs, path: filepath.Join(devs[0].Path, "temp1_input")}, nil
	case id == "coretemp":
		paths := allTempInputs(fs, "coretemp")
		if len(paths) == 0 {
			return nil, fmt.Errorf("%w: coretemp (Intel CPU)", ErrNoDevice)
		}
		return &maximum{id: id, fs: fs, paths: paths}, nil
	case id == "nvme:max", id == "drivetemp:max":
		name := strings.TrimSuffix(id, ":max")
		var paths []string
		for _, d := range fs.FindByName(name) {
			paths = append(paths, filepath.Join(d.Path, "temp1_input"))
		}
		if len(paths) == 0 {
			return nil, fmt.Errorf("%w: %s", ErrNoDevice, name)
		}
		return &maximum{id: id, fs: fs, paths: paths}, nil
	case strings.HasPrefix(id, "hwmon:"):
		m := hwmonIDRe.FindStringSubmatch(id)
		if m == nil {
			return nil, fmt.Errorf("sensor: malformed id %q (want hwmon:<name>:tempN)", id)
		}
		devs := fs.FindByName(m[1])
		if len(devs) == 0 {
			// also accept the directory name (hwmon:hwmon14:temp2)
			for _, d := range mustList(fs) {
				if filepath.Base(d.Path) == m[1] {
					devs = append(devs, d)
				}
			}
		}
		if len(devs) == 0 {
			return nil, fmt.Errorf("%w: hwmon %q", ErrNoDevice, m[1])
		}
		p := filepath.Join(devs[0].Path, "temp"+m[2]+"_input")
		if !fs.Exists(p) {
			return nil, fmt.Errorf("%w: %s has no temp%s_input", ErrNoDevice, devs[0].Path, m[2])
		}
		return &single{id: id, fs: fs, path: p}, nil
	case strings.HasPrefix(id, "ec:"):
		if dev == nil {
			return nil, fmt.Errorf("sensor: %s needs a detected profile device", id)
		}
		p, ok := dev.ExtraTemps()[id]
		if !ok {
			return nil, fmt.Errorf("%w: profile %s exposes no %s (has: %s)",
				ErrNoDevice, dev.Profile().Name(), id, strings.Join(sortedKeys(dev.ExtraTemps()), ", "))
		}
		return &single{id: id, fs: fs, path: p}, nil
	}
	return nil, fmt.Errorf("sensor: unknown id %q", id)
}

func mustList(fs *hwmon.FS) []hwmon.Device {
	devs, _ := fs.List()
	return devs
}

// allTempInputs returns every tempN_input of every device with that name.
func allTempInputs(fs *hwmon.FS, name string) []string {
	var paths []string
	for _, d := range fs.FindByName(name) {
		paths = append(paths, tempInputs(d.Path)...)
	}
	return paths
}

// tempInputs lists tempN_input files of one device, ordered by N.
func tempInputs(devPath string) []string {
	matches, _ := filepath.Glob(filepath.Join(devPath, "temp*_input"))
	type numbered struct {
		n int
		p string
	}
	var found []numbered
	for _, p := range matches {
		m := tempInRe.FindStringSubmatch(filepath.Base(p))
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		found = append(found, numbered{n, p})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].n < found[j].n })
	out := make([]string, 0, len(found))
	for _, f := range found {
		out = append(out, f.p)
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Known lists the sensor ids usable on this machine: the concrete ids that
// resolve right now (with the current reading in the description when
// readable), the profile's extra temps, one hwmon:<name>:tempN entry per
// temperature input, and finally the generic patterns. dev may be nil.
func Known(fs *hwmon.FS, dev profile.Device) []Info {
	var out []Info
	add := func(id, desc string) {
		if src, err := Parse(id, fs, dev); err == nil {
			if v, err := src.Read(); err == nil {
				desc = fmt.Sprintf("%s (now %.1f C)", desc, float64(v)/1000)
			}
			out = append(out, Info{ID: id, Description: desc})
		}
	}
	add("k10temp", "AMD CPU temperature (Tctl)")
	add("coretemp", "Intel CPU temperature (hottest core/package)")
	add("nvme:max", "hottest NVMe SSD")
	add("drivetemp:max", "hottest SATA/SAS drive")
	if dev != nil {
		for _, id := range sortedKeys(dev.ExtraTemps()) {
			add(id, fmt.Sprintf("%s sensor %s", dev.Profile().Title(), strings.TrimPrefix(id, "ec:")))
		}
	}
	seen := map[string]bool{}
	for _, d := range mustList(fs) {
		if seen[d.Name] {
			continue // hwmon:<name> always resolves to the first device of that name
		}
		seen[d.Name] = true
		for _, p := range tempInputs(d.Path) {
			m := tempInRe.FindStringSubmatch(filepath.Base(p))
			label := ""
			if s, err := fs.ReadString(filepath.Join(d.Path, "temp"+m[1]+"_label")); err == nil && s != "" {
				label = " " + s
			}
			add(fmt.Sprintf("hwmon:%s:temp%s", d.Name, m[1]),
				fmt.Sprintf("%s temp%s%s [%s]", d.Name, m[1], label, filepath.Base(d.Path)))
		}
	}
	out = append(out,
		Info{ID: "hwmon:<name>:tempN", Description: "any hwmon device by name and temperature index"},
		Info{ID: "ec:<label>", Description: "extra temperature of the detected fan controller profile"},
	)
	return out
}
