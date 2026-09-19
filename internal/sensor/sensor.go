// Package sensor resolves sensor ids from the config ("k10temp", "nvme:max",
// "ec:system", ...) to readable temperature sources on a hwmon.FS.
package sensor

import (
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/SirRenix/n5-fangov/internal/hwmon"
	"github.com/SirRenix/n5-fangov/internal/profile"
)

// Plausibility window in millidegrees. Readings outside are reported as
// errors; the controller then holds that channel at its safe duty (mode
// sensor-error) while the other channels keep regulating. This is the one
// definition; internal/control imports it.
const (
	MinPlausible = -20000
	MaxPlausible = 120000
)

// Plausible reports whether a millidegree reading lies inside the window.
func Plausible(milli int) bool { return milli >= MinPlausible && milli <= MaxPlausible }

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

// Info describes a sensor id for the UI dropdown. Kind is "ssd" or "hdd"
// for disk:<dev> ids (NVMe or non-rotational → ssd, rotational → hdd),
// empty for every other id and when the block device does not say.
type Info struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Kind        string `json:"kind,omitempty"`
}

// Composite id limits: "a,b" is the maximum of 2..MaxParts single ids.
const (
	MinParts = 2
	MaxParts = 4
)

// Part is the reading of one part of a composite source, as seen by its
// last Read: the part's configured id and its value in millidegrees.
type Part struct {
	ID    string
	Value int
}

// PartsReader is implemented by a source made of parts (the composite).
// Parts returns the readable parts of the last Read in the configured
// order — the controller judges each of them against its own kind's
// ceiling (DESIGN "Ceilings and emergency"). Nil before the first Read.
type PartsReader interface {
	Parts() []Part
}

// composite reads several sources and returns the highest value. Parts
// that could not be resolved at Parse time are absent (the controller
// re-resolves the whole id every 60 cycles, so they are picked up later);
// parts that fail to read now are ignored as long as one succeeds.
type composite struct {
	id    string
	ids   []string // configured id of every resolved part, parallel to parts
	parts []Source

	mu   sync.Mutex
	last []Part // readable parts of the last Read
}

func (m *composite) ID() string { return m.id }

func (m *composite) Read() (int, error) {
	best := 0
	ok := false
	var firstErr error
	last := make([]Part, 0, len(m.parts))
	for i, s := range m.parts {
		v, err := s.Read()
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		last = append(last, Part{ID: m.ids[i], Value: v})
		if !ok || v > best {
			best, ok = v, true
		}
	}
	m.mu.Lock()
	m.last = last
	m.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("sensor %s: no readable part: %w", m.id, firstErr)
	}
	return best, nil
}

// Parts implements PartsReader: the readable parts of the last Read.
func (m *composite) Parts() []Part {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.last)
}

// parseComposite resolves "a,b[,c[,d]]": every part is parsed on its own,
// parts that do not resolve now are skipped, at least one must resolve
// (else the first part's error). The id is the canonical form (trimmed
// parts, configured order, joined by ",").
func parseComposite(id string, fs *hwmon.FS, dev profile.Device) (Source, error) {
	raw := strings.Split(id, ",")
	if len(raw) < MinParts || len(raw) > MaxParts {
		return nil, fmt.Errorf("sensor: composite %q has %d parts, want %d..%d", id, len(raw), MinParts, MaxParts)
	}
	ids := make([]string, 0, len(raw))
	seen := map[string]bool{}
	for _, part := range raw {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("sensor: composite %q has an empty part", id)
		}
		if seen[part] {
			return nil, fmt.Errorf("sensor: composite %q lists %q twice", id, part)
		}
		seen[part] = true
		ids = append(ids, part)
	}
	c := &composite{id: strings.Join(ids, ",")}
	var firstErr error
	for _, part := range ids {
		s, err := Parse(part, fs, dev)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		c.ids = append(c.ids, part)
		c.parts = append(c.parts, s)
	}
	if len(c.parts) == 0 {
		return nil, fmt.Errorf("sensor: composite %s: no part resolves: %w", c.id, firstErr)
	}
	return c, nil
}

// checkPlausible rejects readings outside the plausibility window.
func checkPlausible(id string, v int) (int, error) {
	if !Plausible(v) {
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
	diskIDRe  = regexp.MustCompile(`^disk:([a-z0-9]{1,32})$`)
	tempInRe  = regexp.MustCompile(`^temp([0-9]+)_input$`)
)

// Parse resolves id against fs (and dev for "ec:*" ids; dev may be nil for
// all other ids). Device lookup happens once here; Read only touches the
// resolved files. A composite id "a,b" (2..MaxParts single ids) reads the
// maximum of its parts (parseComposite).
func Parse(id string, fs *hwmon.FS, dev profile.Device) (Source, error) {
	id = strings.TrimSpace(id)
	if fs == nil {
		return nil, errors.New("sensor: nil hwmon.FS")
	}
	switch {
	case id == "":
		return nil, errors.New("sensor: empty id")
	case strings.Contains(id, ","):
		return parseComposite(id, fs, dev)
	case strings.HasPrefix(id, "disk:"):
		m := diskIDRe.FindStringSubmatch(id)
		if m == nil {
			return nil, fmt.Errorf("sensor: malformed id %q (want disk:<dev>, e.g. disk:sda)", id)
		}
		p, err := fs.DiskHwmon(m[1])
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoDevice, err)
		}
		return &single{id: id, fs: fs, path: filepath.Join(p, "temp1_input")}, nil
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
			for _, d := range listOrEmpty(fs) {
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

// listOrEmpty lists the hwmon devices; a List error (no /sys/class/hwmon)
// reads as no devices, the caller then reports "no matching device".
func listOrEmpty(fs *hwmon.FS) []hwmon.Device {
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

func sortedKeys(m map[string]string) []string { return slices.Sorted(maps.Keys(m)) }

// diskModel is block/<dev>/device/model with its whitespace collapsed
// (SATA pads the field), or "disk".
func diskModel(fs *hwmon.FS, dev string) string {
	s, err := fs.ReadString(filepath.Join("block", dev, "device", "model"))
	if err != nil {
		return "disk"
	}
	if s = strings.Join(strings.Fields(s), " "); s == "" {
		return "disk"
	}
	return s
}

// hwmonName is the name file of a hwmon directory, or its base name.
func hwmonName(fs *hwmon.FS, hw string) string {
	if s, err := fs.ReadString(filepath.Join(hw, "name")); err == nil && s != "" {
		return s
	}
	return filepath.Base(hw)
}

// DiskKind classifies block device dev for the dashboard: "ssd" for an
// NVMe name or queue/rotational = 0, "hdd" for rotational = 1, "" when the
// device does not say.
func DiskKind(fs *hwmon.FS, dev string) string {
	if strings.HasPrefix(dev, "nvme") {
		return "ssd"
	}
	switch v, err := fs.ReadInt(filepath.Join("block", dev, "queue", "rotational")); {
	case err != nil:
		return ""
	case v == 1:
		return "hdd"
	case v == 0:
		return "ssd"
	}
	return ""
}

// Known lists the sensor ids usable on this machine: the concrete ids that
// resolve right now (with the current reading in the description when
// readable), the profile's extra temps, one hwmon:<name>:tempN entry per
// temperature input, and finally the generic patterns. dev may be nil.
func Known(fs *hwmon.FS, dev profile.Device) []Info {
	var out []Info
	addKind := func(id, desc, kind string) {
		if src, err := Parse(id, fs, dev); err == nil {
			if v, err := src.Read(); err == nil {
				desc = fmt.Sprintf("%s (now %.1f C)", desc, float64(v)/1000)
			}
			out = append(out, Info{ID: id, Description: desc, Kind: kind})
		}
	}
	add := func(id, desc string) { addKind(id, desc, "") }
	add("k10temp", "AMD CPU temperature (Tctl)")
	add("coretemp", "Intel CPU temperature (hottest core/package)")
	add("nvme:max", "hottest NVMe SSD")
	add("drivetemp:max", "hottest SATA/SAS drive")
	for _, d := range fs.BlockDevices() {
		hw, err := fs.DiskHwmon(d)
		if err != nil {
			continue
		}
		addKind("disk:"+d, fmt.Sprintf("%s (%s, %s)", diskModel(fs, d), d, hwmonName(fs, hw)), DiskKind(fs, d))
	}
	if dev != nil {
		for _, id := range sortedKeys(dev.ExtraTemps()) {
			add(id, fmt.Sprintf("%s sensor %s", dev.Profile().Title(), strings.TrimPrefix(id, "ec:")))
		}
	}
	seen := map[string]bool{}
	for _, d := range listOrEmpty(fs) {
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
