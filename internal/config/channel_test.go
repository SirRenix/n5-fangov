package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestChannelSensorForms: sensor accepts a string, a comma-joined string
// or an array of 1..MaxSensorParts ids; the stored id is the canonical
// comma-joined form, Sensors splits it again.
func TestChannelSensorForms(t *testing.T) {
	src := `
[[channel]]
name = "one"
pwm = 1
sensor = "k10temp"
critical = 90

[[channel]]
name = "arr"
pwm = 2
sensor = [" drivetemp:max ", "ec:hdd"]
critical = 60

[[channel]]
name = "str"
pwm = 3
sensor = "drivetemp:max, ec:hdd ,ec:board"
critical = 60

[[channel]]
name = "single_arr"
pwm = 4
sensor = ["nvme:max"]
critical = 72
`
	cfg, warns, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range warns {
		if strings.HasSuffix(w.Field, ".sensor") {
			t.Errorf("unexpected warning %v", w)
		}
	}
	want := map[string]string{"one": "k10temp", "arr": "drivetemp:max,ec:hdd", "str": "drivetemp:max,ec:hdd,ec:board", "single_arr": "nvme:max"}
	for name, id := range want {
		ch := cfg.Channel(name)
		if ch == nil {
			t.Fatalf("channel %s dropped: %v", name, warns)
		}
		if ch.Sensor != id {
			t.Errorf("%s: sensor %q, want %q", name, ch.Sensor, id)
		}
		if got := ch.Sensors(); !reflect.DeepEqual(got, strings.Split(id, ",")) {
			t.Errorf("%s: Sensors() = %v", name, got)
		}
	}
	// composites containing drivetemp:max default to the HDD stop duty
	if cfg.Channel("arr").Stop != HDDStop || cfg.Channel("str").Stop != HDDStop || cfg.Channel("one").Stop != "auto" {
		t.Errorf("stop defaults: arr=%s str=%s one=%s", cfg.Channel("arr").Stop, cfg.Channel("str").Stop, cfg.Channel("one").Stop)
	}
	if DefaultStop("ec:hdd,drivetemp:max") != HDDStop || DefaultStop("nvme:max,ec:hdd") != "auto" {
		t.Errorf("DefaultStop on composites")
	}
	if (Channel{}).Sensors() != nil {
		t.Errorf("Sensors of an empty id must be nil")
	}
}

// TestChannelSensorInvalid: an empty part, a comma inside an array element,
// a duplicate, more than MaxSensorParts ids or a non-string array drop the
// channel with one warning.
func TestChannelSensorInvalid(t *testing.T) {
	cases := map[string]string{
		"empty_str":   `sensor = ""`,
		"empty_part":  `sensor = "k10temp,,nvme:max"`,
		"empty_elem":  `sensor = ["k10temp", ""]`,
		"comma_elem":  `sensor = ["k10temp,nvme:max"]`,
		"dup":         `sensor = ["k10temp", "k10temp"]`,
		"dup_str":     `sensor = "k10temp, k10temp"`,
		"too_many":    `sensor = ["a", "b", "c", "d", "e"]`,
		"empty_array": `sensor = []`,
		"not_strings": `sensor = [1, 2]`,
		"integer":     `sensor = 7`,
	}
	for name, line := range cases {
		src := "[[channel]]\nname = \"" + name + "\"\npwm = 1\n" + line + "\ncritical = 90\n"
		cfg, warns, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(cfg.Channels) != 0 {
			t.Errorf("%s: channel kept with sensor %q", name, cfg.Channels[0].Sensor)
		}
		n := 0
		for _, w := range warns {
			if w.Field == "channel."+name+".sensor" {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s: %d sensor warnings, want 1: %v", name, n, warns)
		}
	}
	// four parts are the maximum and fine
	cfg, warns, _ := Parse([]byte("[[channel]]\nname = \"four\"\npwm = 1\nsensor = [\"a\", \"b\", \"c\", \"d\"]\ncritical = 90\n"))
	if len(cfg.Channels) != 1 || cfg.Channels[0].Sensor != "a,b,c,d" {
		t.Errorf("four parts: %+v %v", cfg.Channels, warns)
	}
}

// TestChannelPostProcessingKeys: hysteresis (0..HysteresisMax) and min_on
// (0..MinOnMax) default to 0, invalid values fall back with a warning.
func TestChannelPostProcessingKeys(t *testing.T) {
	src := `
[[channel]]
name = "plain"
pwm = 1
sensor = "k10temp"
curve = [[30, 60], [50, 255]]
critical = 90

[[channel]]
name = "set"
pwm = 2
sensor = "nvme:max"
curve = [[30, 60], [50, 255]]
critical = 72
hysteresis = 2
min_on = "90s"

[[channel]]
name = "secs"
pwm = 3
sensor = "drivetemp:max"
curve = [[30, 60], [50, 255]]
critical = 60
hysteresis = 10
min_on = 3600

[[channel]]
name = "bad"
pwm = 4
sensor = "k10temp"
curve = [[30, 60], [50, 255]]
critical = 90
hysteresis = 11
min_on = "2h"

[[channel]]
name = "neg"
pwm = 5
sensor = "k10temp"
curve = [[30, 60], [50, 255]]
critical = 90
hysteresis = -1
min_on = "-5s"

[[channel]]
name = "types"
pwm = 6
sensor = "k10temp"
curve = [[30, 60], [50, 255]]
critical = 90
hysteresis = "two"
min_on = "soon"
`
	cfg, warns, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Channels) != 6 {
		t.Fatalf("channels: %d (%v)", len(cfg.Channels), warns)
	}
	want := map[string]struct {
		h int
		d time.Duration
	}{
		"plain": {0, 0}, "set": {2, 90 * time.Second}, "secs": {10, time.Hour},
		"bad": {0, 0}, "neg": {0, 0}, "types": {0, 0},
	}
	for name, w := range want {
		ch := cfg.Channel(name)
		if ch.Hysteresis != w.h || ch.MinOn != w.d {
			t.Errorf("%s: hysteresis %d min_on %s, want %d %s", name, ch.Hysteresis, ch.MinOn, w.h, w.d)
		}
	}
	for _, f := range []string{"channel.bad.hysteresis", "channel.bad.min_on", "channel.neg.hysteresis", "channel.neg.min_on", "channel.types.hysteresis", "channel.types.min_on"} {
		hasWarn(t, warns, f)
	}
	for _, w := range warns {
		if strings.HasPrefix(w.Field, "channel.plain.") || strings.HasPrefix(w.Field, "channel.set.") || strings.HasPrefix(w.Field, "channel.secs.") {
			t.Errorf("unexpected warning %v", w)
		}
	}
	// no warning for a channel that sets neither key (the defaults are silent)
	if n := countWarnPrefix(warns, "channel.plain."); n != 0 {
		t.Errorf("plain: %d warnings", n)
	}
}

func countWarnPrefix(warns []Warning, prefix string) int {
	n := 0
	for _, w := range warns {
		if strings.HasPrefix(w.Field, prefix) {
			n++
		}
	}
	return n
}

// TestMarshalPostProcessingKeys: Marshal and MarshalChannels write min_on
// as a duration string and hysteresis as an integer, omit both at their
// defaults, and the text parses back to the same channels.
func TestMarshalPostProcessingKeys(t *testing.T) {
	cfg := Default()
	cfg.Channels = N5ProChannels()
	cfg.Channels[2].Hysteresis = 2
	cfg.Channels[2].MinOn = 90 * time.Second
	cfg.Channels[2].PostSet = true // the written keys read back as "set"
	cfg.Channels[2].Sensor = "drivetemp:max,ec:hdd"
	raw := string(Marshal(cfg))
	if !strings.Contains(raw, "min_on = \"1m30s\"") || !strings.Contains(raw, "hysteresis = 2") {
		t.Errorf("marshal:\n%s", raw)
	}
	if strings.Count(raw, "min_on") != 1 || strings.Count(raw, "hysteresis") != 1 {
		t.Errorf("defaults must be omitted:\n%s", raw)
	}
	if strings.Contains(raw, "90000000000") {
		t.Errorf("min_on written as nanoseconds:\n%s", raw)
	}
	back, warns, err := Parse([]byte(raw))
	if err != nil || len(warns) != 0 || !reflect.DeepEqual(back.Channels, cfg.Channels) {
		t.Errorf("round trip: %v %v\n%+v", err, warns, back.Channels)
	}
	pre := string(MarshalChannels(cfg.Channels))
	if !strings.Contains(pre, "min_on = \"1m30s\"") || strings.Contains(pre, "[daemon]") {
		t.Errorf("preset:\n%s", pre)
	}
	chans, warns, err := ParseChannels([]byte(pre))
	if err != nil || len(warns) != 0 || !reflect.DeepEqual(chans, cfg.Channels) {
		t.Errorf("preset round trip: %v %v\n%+v", err, warns, chans)
	}
}

// TestMarshalStopNumeric: a fixed stop duty is written as a TOML integer
// (stop = 140, the form of the built-in presets and the dashboard), "auto"
// stays a quoted string; both forms and a quoted number parse back to the
// same channels (0.4.0 release gate, G10).
func TestMarshalStopNumeric(t *testing.T) {
	withChannels := func(chans []Channel) Config {
		cfg := Default()
		cfg.Channels = chans
		return cfg
	}
	chans := []Channel{
		{Name: "cpu", PWM: 1, Sensor: "k10temp", Curve: [][2]int{{45, 85}, {80, 255}}, Critical: 88, Stop: "auto"},
		{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{36, 105}, {46, 255}}, Critical: 56, Stop: "140"},
	}
	for _, raw := range []string{string(MarshalChannels(chans)), string(Marshal(withChannels(chans)))} {
		if !strings.Contains(raw, "stop = 140\n") || strings.Contains(raw, "\"140\"") {
			t.Errorf("fixed stop must be an integer:\n%s", raw)
		}
		if !strings.Contains(raw, "stop = \"auto\"\n") {
			t.Errorf("auto must stay quoted:\n%s", raw)
		}
		back, warns, err := ParseChannels([]byte(raw))
		if err != nil || len(warns) != 0 || len(back) != 2 || back[1].Stop != "140" || back[0].Stop != "auto" {
			t.Errorf("round trip: %v %v\n%+v", err, warns, back)
		}
	}
	// the quoted form of older preset files keeps parsing without a warning
	quoted := strings.Replace(string(MarshalChannels(chans)), "stop = 140", "stop = \"140\"", 1)
	back, warns, err := ParseChannels([]byte(quoted))
	if err != nil || len(warns) != 0 || len(back) != 2 || back[1].Stop != "140" {
		t.Errorf("quoted number: %v %v\n%+v", err, warns, back)
	}
}

// TestChannelCeilingKey: ceiling may only lower the sensor's built-in
// ceiling (MinCeiling..built-in); above it, or not an integer, the
// built-in value stays (0 = built-in) with a warning. [daemon]
// emergency_command / emergency_cycles parse with their bounds.
func TestChannelCeilingKey(t *testing.T) {
	src := `
[daemon]
emergency_command = "logger -t n5 test"
emergency_cycles = 3

[[channel]]
name = "hdd"
pwm = 3
sensor = "drivetemp:max"
curve = [[30, 60], [50, 255]]
critical = 70
ceiling = 55

[[channel]]
name = "raised"
pwm = 4
sensor = "drivetemp:max"
curve = [[30, 60], [50, 255]]
critical = 60
ceiling = 70

[[channel]]
name = "low"
pwm = 5
sensor = "k10temp"
curve = [[30, 60], [50, 255]]
critical = 90
ceiling = 29

[[channel]]
name = "text"
pwm = 6
sensor = "nvme:max"
curve = [[30, 60], [50, 255]]
critical = 80
ceiling = "hot"

[[channel]]
name = "composite"
pwm = 7
sensor = ["k10temp", "drivetemp:max"]
curve = [[30, 60], [50, 255]]
critical = 90
ceiling = 65

[[channel]]
name = "disk"
pwm = 8
sensor = "disk:sda"
curve = [[30, 60], [50, 255]]
critical = 90
ceiling = 90
`
	cfg, warns, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Channels) != 6 {
		t.Fatalf("channels: %d (%v)", len(cfg.Channels), warns)
	}
	if cfg.Daemon.EmergencyCommand != "logger -t n5 test" || cfg.Daemon.EmergencyCycles != 3 {
		t.Errorf("daemon: %+v", cfg.Daemon)
	}
	for name, want := range map[string]int{"hdd": 55, "raised": 0, "low": 0, "text": 0, "composite": 65, "disk": 90} {
		if got := cfg.Channel(name).Ceiling; got != want {
			t.Errorf("%s: ceiling %d, want %d", name, got, want)
		}
	}
	for _, f := range []string{"channel.raised.ceiling", "channel.low.ceiling", "channel.text.ceiling"} {
		hasWarn(t, warns, f)
	}
	for _, w := range warns {
		if w.Field == "channel.raised.ceiling" && !strings.Contains(w.Msg, "70 outside 30..65") {
			t.Errorf("raised warning: %v", w)
		}
		if strings.HasPrefix(w.Field, "channel.hdd.") || strings.HasPrefix(w.Field, "channel.composite.") || strings.HasPrefix(w.Field, "channel.disk.") {
			t.Errorf("unexpected warning %v", w)
		}
	}
	// defaults: no command, 6 cycles; bounds 1..60
	d := Default().Daemon
	if d.EmergencyCommand != "" || d.EmergencyCycles != DefaultEmergencyCycles {
		t.Errorf("defaults: %+v", d)
	}
	cfg, warns, _ = Parse([]byte("[daemon]\nemergency_cycles = 61\nemergency_command = 3\n"))
	if cfg.Daemon.EmergencyCycles != DefaultEmergencyCycles || cfg.Daemon.EmergencyCommand != "" {
		t.Errorf("out of range: %+v", cfg.Daemon)
	}
	hasWarn(t, warns, "daemon.emergency_cycles")
	hasWarn(t, warns, "daemon.emergency_command")
	// Marshal writes the key only when set and it reads back
	c2 := Default()
	c2.Channels = N5ProChannels()
	c2.Channels[2].Ceiling = 55
	raw := string(Marshal(c2))
	if strings.Count(raw, "ceiling") != 1 || !strings.Contains(raw, "ceiling = 55") {
		t.Errorf("marshal:\n%s", raw)
	}
	back, _, err := Parse([]byte(raw))
	if err != nil || back.Channel("hdd").Ceiling != 55 || back.Daemon.EmergencyCycles != DefaultEmergencyCycles {
		t.Errorf("round trip: %v %+v", err, back.Channel("hdd"))
	}
}
