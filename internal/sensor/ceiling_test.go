package sensor

import "testing"

func TestBuiltinCeiling(t *testing.T) {
	kind := func(dev string) string {
		return map[string]string{"sda": "hdd", "nvme0n1": "ssd", "sdz": ""}[dev]
	}
	cases := []struct {
		id   string
		want int
	}{
		{"k10temp", CeilingCPU}, {"coretemp", CeilingCPU}, {"ec:cpu", CeilingCPU},
		{"hwmon:cpu_thermal:temp1", CeilingCPU}, {"hwmon:amdgpu:temp1", CeilingDefault},
		{"nvme:max", CeilingSSD}, {"drivetemp:max", CeilingHDD},
		{"disk:sda", CeilingHDD}, {"disk:nvme0n1", CeilingSSD}, {"disk:sdz", CeilingDefault},
		{"ec:system", CeilingDefault}, {"", CeilingDefault},
		{"drivetemp:max,ec:hdd", CeilingHDD}, {"k10temp,nvme:max", CeilingSSD}, {"k10temp, disk:sda", CeilingHDD},
	}
	for _, c := range cases {
		if got := BuiltinCeiling(c.id, kind); got != c.want {
			t.Errorf("BuiltinCeiling(%q) = %d, want %d", c.id, got, c.want)
		}
	}
	// without a resolver disk ids read as the default
	if got := BuiltinCeiling("disk:sda", nil); got != CeilingDefault {
		t.Errorf("disk without resolver: %d", got)
	}
	if got := EffectiveCeiling("drivetemp:max", 50, nil); got != 50 {
		t.Errorf("lowered: %d", got)
	}
	if got := EffectiveCeiling("drivetemp:max", 90, nil); got != CeilingHDD {
		t.Errorf("cannot raise: %d", got)
	}
	if got := EffectiveCeiling("drivetemp:max", 0, nil); got != CeilingHDD {
		t.Errorf("unset: %d", got)
	}
}
