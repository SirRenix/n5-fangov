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

// PartCeilings: one entry per part with the part's own kind, lowered by
// the configured value; the controller checks each part against its own.
func TestPartCeilings(t *testing.T) {
	kind := func(dev string) string { return map[string]string{"sda": "hdd", "nvme0n1": "ssd"}[dev] }
	got := PartCeilings("nvme:max, disk:sda,k10temp", 0, kind)
	want := []PartCeiling{{"nvme:max", CeilingSSD}, {"disk:sda", CeilingHDD}, {"k10temp", CeilingCPU}}
	if len(got) != len(want) {
		t.Fatalf("parts: %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("part %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	// configured 70 lowers the SSD and CPU parts, not the HDD part (65 < 70)
	got = PartCeilings("nvme:max,disk:sda,k10temp", 70, kind)
	if got[0].Ceiling != 70 || got[1].Ceiling != CeilingHDD || got[2].Ceiling != 70 {
		t.Errorf("lowered parts: %+v", got)
	}
	// a single id is one part
	if got = PartCeilings("drivetemp:max", 50, nil); len(got) != 1 || got[0] != (PartCeiling{"drivetemp:max", 50}) {
		t.Errorf("single: %+v", got)
	}
}
