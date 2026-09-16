package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// v0.3: [alert] and [dashboard] sections, built-in presets.

func TestAlertSection(t *testing.T) {
	cfg, warns, err := Parse([]byte("[alert]\ntransport = \"mail\"\nmail_to = \"ops@example.test\"\n"))
	if err != nil || len(warns) != 0 {
		t.Fatalf("valid: %v %v", warns, err)
	}
	if cfg.Alert.Transport != "mail" || cfg.Alert.MailTo != "ops@example.test" {
		t.Errorf("alert: %+v", cfg.Alert)
	}
	// case and spacing are tolerated for the transport
	cfg, warns, _ = Parse([]byte("[alert]\ntransport = \" PVE \"\n"))
	if cfg.Alert.Transport != "pve" || len(warns) != 0 {
		t.Errorf("normalised transport: %+v %v", cfg.Alert, warns)
	}
	// invalid values → warning + default
	cfg, warns, _ = Parse([]byte("[alert]\ntransport = \"pigeon\"\nmail_to = \"two words\"\nbogus = 1\n"))
	for _, f := range []string{"alert.transport", "alert.mail_to", "alert.bogus"} {
		hasWarn(t, warns, f)
	}
	if !reflect.DeepEqual(cfg.Alert, Default().Alert) {
		t.Errorf("all-invalid alert must equal defaults: %+v", cfg.Alert)
	}
	cfg, warns, _ = Parse([]byte("[alert]\nmail_to = \"a\\\"b\"\n"))
	hasWarn(t, warns, "alert.mail_to")
	if cfg.Alert.MailTo != DefaultMailTo {
		t.Errorf("quoted mail_to must fall back: %q", cfg.Alert.MailTo)
	}
	cfg, warns, _ = Parse([]byte("alert = 5\n"))
	hasWarn(t, warns, "alert")
	if cfg.Alert.Transport != "auto" {
		t.Errorf("non-table alert: %+v", cfg.Alert)
	}
	// round trip
	cfg = Default()
	cfg.Alert = Alert{Transport: "log", MailTo: "admin"}
	back, warns, err := Parse(Marshal(cfg))
	if err != nil || len(warns) != 0 || !reflect.DeepEqual(cfg, back) {
		t.Errorf("alert round trip: %v %v\n%+v", err, warns, back)
	}
}

func TestValidMailTo(t *testing.T) {
	for _, ok := range []string{"root", "admin", "ops@example.test", "first.last+tag@mail.example", "user_1"} {
		if !ValidMailTo(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	for _, bad := range []string{"", "two words", "a\"b", "a;b", "root@", "@host", "x@y z", strings.Repeat("a", 260)} {
		if ValidMailTo(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
}

func TestDashboardSection(t *testing.T) {
	cfg, warns, err := Parse([]byte("[dashboard]\nsensors = [\"hwmon:amdgpu:temp1\", \" nvme:max \", \"\", \"hwmon:amdgpu:temp1\"]\n"))
	if err != nil || len(warns) != 0 {
		t.Fatalf("valid: %v %v", warns, err)
	}
	if !reflect.DeepEqual(cfg.Dashboard.Sensors, []string{"hwmon:amdgpu:temp1", "nvme:max"}) {
		t.Errorf("sensors (trimmed, deduplicated, empties dropped): %v", cfg.Dashboard.Sensors)
	}
	// absent / empty → non-nil empty slice (round trip with Default)
	cfg, _, _ = Parse(nil)
	if cfg.Dashboard.Sensors == nil || len(cfg.Dashboard.Sensors) != 0 {
		t.Errorf("absent sensors must be an empty non-nil slice: %#v", cfg.Dashboard.Sensors)
	}
	cfg, warns, _ = Parse([]byte("[dashboard]\nsensors = []\n"))
	if cfg.Dashboard.Sensors == nil || len(warns) != 0 {
		t.Errorf("empty sensors: %#v %v", cfg.Dashboard.Sensors, warns)
	}
	// too many → warning, truncated to MaxDashboardSensors
	var ids []string
	for i := 0; i < MaxDashboardSensors+2; i++ {
		ids = append(ids, "\"hwmon:x:temp"+string(rune('1'+i))+"\"")
	}
	cfg, warns, _ = Parse([]byte("[dashboard]\nsensors = [" + strings.Join(ids, ",") + "]\n"))
	hasWarn(t, warns, "dashboard.sensors")
	if len(cfg.Dashboard.Sensors) != MaxDashboardSensors {
		t.Errorf("truncated to %d, got %d", MaxDashboardSensors, len(cfg.Dashboard.Sensors))
	}
	// wrong type → warning, empty
	cfg, warns, _ = Parse([]byte("[dashboard]\nsensors = \"k10temp\"\nextra = 1\n"))
	hasWarn(t, warns, "dashboard.sensors")
	hasWarn(t, warns, "dashboard.extra")
	if len(cfg.Dashboard.Sensors) != 0 {
		t.Errorf("non-array sensors must be ignored: %v", cfg.Dashboard.Sensors)
	}
	// round trip and Clone independence
	cfg = Default()
	cfg.Dashboard.Sensors = []string{"k10temp", "ec:system"}
	back, warns, err := Parse(Marshal(cfg))
	if err != nil || len(warns) != 0 || !reflect.DeepEqual(cfg, back) {
		t.Errorf("dashboard round trip: %v %v\n%+v", err, warns, back)
	}
	cl := cfg.Clone()
	cl.Dashboard.Sensors[0] = "changed"
	if cfg.Dashboard.Sensors[0] != "k10temp" {
		t.Errorf("Clone must copy the sensor list")
	}
}

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

// The shipped example config must parse without a single warning: it is
// the reference for every key, and the daemon would alert on it.
func TestExampleConfigParses(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "config.example.toml"))
	if err != nil {
		t.Skip("deploy/config.example.toml not found:", err)
	}
	cfg, warns, err := Parse(raw)
	if err != nil || len(warns) != 0 {
		t.Fatalf("example config: %v %v", warns, err)
	}
	if cfg.Alert.Transport != "auto" || cfg.Alert.MailTo != "root" || len(cfg.Dashboard.Sensors) != 0 || len(cfg.Channels) != 3 {
		t.Errorf("example values: %+v %+v %d channels", cfg.Alert, cfg.Dashboard, len(cfg.Channels))
	}
}
