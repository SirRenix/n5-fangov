package sensor

import "strings"

// Built-in ceilings in degrees C (DESIGN "Ceilings and emergency"): the
// floor of the guard chain below [[channel]] critical that no configuration
// can raise. MinCeiling is the lowest value [[channel]] ceiling may set.
const (
	CeilingCPU     = 100
	CeilingSSD     = 85
	CeilingHDD     = 65
	CeilingDefault = 100
	MinCeiling     = 30
)

// BuiltinCeiling returns the built-in ceiling for a sensor id: 100 for the
// CPU ids (k10temp, coretemp, ec:cpu, hwmon:<name>:tempN with "cpu" in the
// name), 85 for SSDs (nvme:max, disk:<dev> of kind ssd), 65 for HDDs
// (drivetemp:max, disk:<dev> of kind hdd), 100 for everything else. A
// composite id reports the lowest ceiling of its parts (informational: the
// controller judges every part against its own, PartCeilings). diskKind
// resolves a disk:<dev> device name to "ssd", "hdd" or "" (DiskKind bound
// to a hwmon.FS); nil or "" yields the default — the config parser has no
// sysfs and validates disk ids against 100, the daemon applies
// min(configured, built-in) anyway, so a configured value can only lower
// the ceiling.
func BuiltinCeiling(id string, diskKind func(dev string) string) int {
	best := 0
	for _, pc := range PartCeilings(id, 0, diskKind) {
		if best == 0 || pc.Ceiling < best {
			best = pc.Ceiling
		}
	}
	if best == 0 {
		return CeilingDefault
	}
	return best
}

// PartCeiling is the effective ceiling of one part of a channel sensor.
type PartCeiling struct {
	ID      string
	Ceiling int
}

// PartCeilings splits id into its parts (a single id is one part) and
// returns each part's effective ceiling: its own kind's built-in value,
// lowered to configured when that is set (> 0) and lower. The controller
// checks every part against its own entry, so a composite of an NVMe and
// an HDD holds the NVMe to 85 and the HDD to 65 — not both to 65.
func PartCeilings(id string, configured int, diskKind func(dev string) string) []PartCeiling {
	raw := strings.Split(id, ",")
	out := make([]PartCeiling, 0, len(raw))
	for _, part := range raw {
		part = strings.TrimSpace(part)
		c := singleCeiling(part, diskKind)
		if configured > 0 && configured < c {
			c = configured
		}
		out = append(out, PartCeiling{ID: part, Ceiling: c})
	}
	return out
}

func singleCeiling(id string, diskKind func(dev string) string) int {
	switch {
	case id == "k10temp", id == "coretemp", id == "ec:cpu":
		return CeilingCPU
	case id == "nvme:max":
		return CeilingSSD
	case id == "drivetemp:max":
		return CeilingHDD
	case strings.HasPrefix(id, "disk:"):
		if diskKind != nil {
			switch diskKind(strings.TrimPrefix(id, "disk:")) {
			case "ssd":
				return CeilingSSD
			case "hdd":
				return CeilingHDD
			}
		}
		return CeilingDefault
	case strings.HasPrefix(id, "hwmon:") && strings.Contains(strings.ToLower(id), "cpu"):
		return CeilingCPU
	}
	return CeilingDefault
}

// EffectiveCeiling is the ceiling a channel runs with: the built-in one,
// lowered to configured when that is set (> 0) and lower.
func EffectiveCeiling(id string, configured int, diskKind func(dev string) string) int {
	c := BuiltinCeiling(id, diskKind)
	if configured > 0 && configured < c {
		return configured
	}
	return c
}
