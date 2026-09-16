package config

import (
	"strings"
	"testing"
)

func TestBuiltinPresets(t *testing.T) {
	all := BuiltinPresets("")
	names := map[string]bool{}
	for _, p := range all {
		names[p.Name] = true
	}
	for _, want := range []string{"n5pro-quiet", "n5pro-balanced", "n5pro-cool"} {
		if !names[want] {
			t.Errorf("missing built-in preset %s (have %v)", want, names)
		}
		if !IsBuiltinPreset(want) {
			t.Errorf("IsBuiltinPreset(%s) false", want)
		}
	}
	if IsBuiltinPreset("mine") || IsBuiltinPreset("") {
		t.Errorf("user names must not count as built-in")
	}
	n5 := BuiltinPresets("n5pro")
	if len(n5) != 3 || len(BuiltinPresets("nct67xx")) != 0 {
		t.Errorf("profile filter: n5pro=%d, nct67xx=%d", len(n5), len(BuiltinPresets("nct67xx")))
	}
	for _, p := range n5 {
		if p.Profile != "n5pro" || p.Description == "" || !ValidPresetName(p.Name) {
			t.Errorf("%s: header %+v", p.Name, p)
		}
		chans, warns, err := ParseChannels(p.Raw)
		if err != nil || len(warns) != 0 {
			t.Errorf("%s: parse %v %v", p.Name, warns, err)
		}
		if len(chans) != 3 {
			t.Errorf("%s: %d channels", p.Name, len(chans))
			continue
		}
		// the sanitised N5 Pro shape: cpu/ssd/hdd on pwm1..3, hdd stop 140
		byName := map[string]Channel{}
		for _, c := range chans {
			byName[c.Name] = c
		}
		if c := byName["cpu"]; c.PWM != 1 || c.Sensor != "k10temp" || c.Critical != 88 || c.Stop != "auto" {
			t.Errorf("%s cpu: %+v", p.Name, c)
		}
		if c := byName["ssd"]; c.PWM != 2 || c.Sensor != "nvme:max" || c.Critical != 72 || c.Stop != "auto" {
			t.Errorf("%s ssd: %+v", p.Name, c)
		}
		if c := byName["hdd"]; c.PWM != 3 || c.Sensor != "drivetemp:max" || c.Stop != HDDStop {
			t.Errorf("%s hdd: %+v", p.Name, c)
		}
	}
	if b, ok := BuiltinPresetByName("n5pro-balanced"); !ok || !strings.HasPrefix(b.Description, "Recommended") {
		t.Errorf("balanced must be the recommended one: %+v", b)
	}
	// the returned Raw is a copy
	all[0].Raw[0] = 'X'
	if again := BuiltinPresets(""); again[0].Raw[0] == 'X' {
		t.Errorf("Raw must be copied")
	}
}

func TestPresetHeader(t *testing.T) {
	d, p := presetHeader([]byte("\n# title\n#   description:  Some text  \n# profile: n5pro\n\n[[channel]]\n# description: not this\n"))
	if d != "Some text" || p != "n5pro" {
		t.Errorf("header: %q %q", d, p)
	}
	if d, p := presetHeader([]byte("[[channel]]\nname = \"x\"\n")); d != "" || p != "" {
		t.Errorf("no header: %q %q", d, p)
	}
}
