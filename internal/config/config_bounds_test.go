package config

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Exact boundary cases (AUDIT 7 "Tests": limits were only checked from
// above and below, never at min and max). Every expectation is derived from
// the parser: an in-range value is kept without a warning, an out-of-range
// value yields the default plus one warning on the field (rule 8); the two
// exceptions are interval (clamped above MaxInterval) and stop (raised to
// MinFixedStop).

// noWarn fails when warns carries anything for field.
func noWarn(t *testing.T, warns []Warning, field string) {
	t.Helper()
	for _, w := range warns {
		if w.Field == field {
			t.Errorf("unexpected warning for %q: %v", field, w)
		}
	}
}

// parseDaemon parses one [daemon] key = value line.
func parseDaemon(t *testing.T, key, value string) (Daemon, []Warning) {
	t.Helper()
	cfg, warns, err := Parse([]byte("[daemon]\n" + key + " = " + value + "\n"))
	if err != nil {
		t.Fatalf("%s = %s: %v", key, value, err)
	}
	return cfg.Daemon, warns
}

func TestDaemonIntBounds(t *testing.T) {
	def := Default().Daemon
	get := map[string]func(Daemon) int{
		"step_up":        func(d Daemon) int { return d.StepUp },
		"step_down":      func(d Daemon) int { return d.StepDown },
		"stall_min_duty": func(d Daemon) int { return d.StallMinDuty },
		"stall_cycles":   func(d Daemon) int { return d.StallCycles },
		"stale_cycles":   func(d Daemon) int { return d.StaleCycles },
	}
	defs := map[string]int{
		"step_up": def.StepUp, "step_down": def.StepDown, "stall_min_duty": def.StallMinDuty,
		"stall_cycles": def.StallCycles, "stale_cycles": def.StaleCycles,
	}
	cases := []struct {
		key   string
		value int
		want  int  // resulting value
		warn  bool // warning on daemon.<key>
	}{
		{"step_up", 1, 1, false},
		{"step_up", 255, 255, false},
		{"step_up", 0, def.StepUp, true},
		{"step_up", 256, def.StepUp, true},
		{"step_down", 1, 1, false},
		{"step_down", 255, 255, false},
		{"step_down", 0, def.StepDown, true},
		{"step_down", 256, def.StepDown, true},
		{"stall_min_duty", 1, 1, false},
		{"stall_min_duty", 255, 255, false},
		{"stall_min_duty", 0, def.StallMinDuty, true},
		{"stall_min_duty", 256, def.StallMinDuty, true},
		{"stall_cycles", 1, 1, false},
		{"stall_cycles", MaxStallCycle, MaxStallCycle, false},
		{"stall_cycles", 0, def.StallCycles, true},
		{"stall_cycles", MaxStallCycle + 1, def.StallCycles, true},
		{"stale_cycles", MinStaleCycle, MinStaleCycle, false},
		{"stale_cycles", MaxStaleCycle, MaxStaleCycle, false},
		{"stale_cycles", MinStaleCycle - 1, def.StaleCycles, true},
		{"stale_cycles", MaxStaleCycle + 1, def.StaleCycles, true},
	}
	if MaxStallCycle != 20 || MinStaleCycle != 6 || MaxStaleCycle != 600 {
		t.Fatalf("limits changed: stall max %d, stale %d..%d", MaxStallCycle, MinStaleCycle, MaxStaleCycle)
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s=%d", c.key, c.value), func(t *testing.T) {
			d, warns := parseDaemon(t, c.key, fmt.Sprint(c.value))
			if got := get[c.key](d); got != c.want {
				t.Errorf("value %d, want %d (default %d)", got, c.want, defs[c.key])
			}
			if c.warn {
				hasWarn(t, warns, "daemon."+c.key)
				if len(warns) != 1 {
					t.Errorf("want exactly one warning, got %v", warns)
				}
			} else if len(warns) != 0 {
				t.Errorf("in-range value warned: %v", warns)
			}
		})
	}
}

func TestDaemonDurationBounds(t *testing.T) {
	def := Default().Daemon
	if MinCooldown != time.Minute || MaxCooldown != 24*time.Hour || MinInterval != 2*time.Second || MaxInterval != 30*time.Second {
		t.Fatalf("limits changed: cooldown %s..%s interval %s..%s", MinCooldown, MaxCooldown, MinInterval, MaxInterval)
	}
	cases := []struct {
		key, value string
		want       time.Duration
		warn       bool
		msg        string // substring of the warning text
	}{
		{"alert_cooldown", `"60s"`, time.Minute, false, ""},
		{"alert_cooldown", `"24h"`, 24 * time.Hour, false, ""},
		{"alert_cooldown", `"59s"`, def.AlertCooldown, true, "outside"},
		{"alert_cooldown", `"25h"`, def.AlertCooldown, true, "outside"},
		{"alert_cooldown", `60`, time.Minute, false, ""},
		{"alert_cooldown", `86400`, 24 * time.Hour, false, ""},
		{"alert_cooldown", `86401`, def.AlertCooldown, true, "outside"},
		{"interval", `"2s"`, 2 * time.Second, false, ""},
		{"interval", `"30s"`, 30 * time.Second, false, ""},
		{"interval", `"1s"`, def.Interval, true, "below"},
		// above the maximum: clamped, not defaulted (WatchdogSec window)
		{"interval", `"31s"`, MaxInterval, true, "above"},
		{"interval", `2`, 2 * time.Second, false, ""},
		{"interval", `31`, MaxInterval, true, "above"},
		{"interval", `1`, def.Interval, true, "below"},
	}
	for _, c := range cases {
		t.Run(c.key+"="+c.value, func(t *testing.T) {
			d, warns := parseDaemon(t, c.key, c.value)
			got := d.AlertCooldown
			if c.key == "interval" {
				got = d.Interval
			}
			if got != c.want {
				t.Errorf("value %s, want %s", got, c.want)
			}
			if !c.warn {
				if len(warns) != 0 {
					t.Errorf("in-range value warned: %v", warns)
				}
				return
			}
			hasWarn(t, warns, "daemon."+c.key)
			if len(warns) != 1 || !strings.Contains(warns[0].Msg, c.msg) {
				t.Errorf("warning %v, want one containing %q", warns, c.msg)
			}
		})
	}
}

// channelTOML builds one [[channel]] table; extra lines are appended verbatim.
func channelTOML(extra ...string) string {
	return "[[channel]]\nname = \"x\"\npwm = 1\nsensor = \"k10temp\"\n" + strings.Join(extra, "\n") + "\n"
}

func TestCriticalBounds(t *testing.T) {
	if MaxCritical != 150 {
		t.Fatalf("MaxCritical = %d", MaxCritical)
	}
	const curve = "curve = [[45,85],[80,255]]" // last temp 80 → default critical 90
	cases := []struct {
		critical int
		want     int
		warn     bool
	}{
		{81, 81, false},   // last curve temp + 1
		{150, 150, false}, // MaxCritical
		{151, 90, true},   // above → default last+10
		{80, 90, true},    // equal to the last curve temp → default
		{-5, 90, true},
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.critical), func(t *testing.T) {
			cfg, warns, err := Parse([]byte(channelTOML(curve, fmt.Sprintf("critical = %d", c.critical))))
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Channels) != 1 {
				t.Fatalf("channel dropped: %v", warns)
			}
			if got := cfg.Channels[0].Critical; got != c.want {
				t.Errorf("critical %d, want %d", got, c.want)
			}
			if c.warn {
				hasWarn(t, warns, "channel.x.critical")
			} else {
				noWarn(t, warns, "channel.x.critical")
			}
		})
	}
	// the default for a curve ending at MaxCurveTemp is last+10 = 130; the
	// MaxCritical cap on the default (config.go) is unreachable as long as
	// MaxCurveTemp+10 <= MaxCritical
	if MaxCurveTemp+10 > MaxCritical {
		t.Fatalf("MaxCurveTemp %d + 10 > MaxCritical %d", MaxCurveTemp, MaxCritical)
	}
	cfg, warns, _ := Parse([]byte(channelTOML("curve = [[100,85],[120,255]]")))
	hasWarn(t, warns, "channel.x.critical") // "missing"
	if cfg.Channels[0].Critical != MaxCurveTemp+10 {
		t.Errorf("default critical for a curve ending at %d: %d, want %d", MaxCurveTemp, cfg.Channels[0].Critical, MaxCurveTemp+10)
	}
	// with the curve at 120 there is no valid critical above it except 121..150
	cfg, warns, _ = Parse([]byte(channelTOML("curve = [[100,85],[120,255]]", "critical = 121")))
	noWarn(t, warns, "channel.x.critical")
	if cfg.Channels[0].Critical != 121 {
		t.Errorf("critical 121 above curve end 120: %d", cfg.Channels[0].Critical)
	}
}

func TestStopBounds(t *testing.T) {
	if MinFixedStop != 60 {
		t.Fatalf("MinFixedStop = %d", MinFixedStop)
	}
	cases := []struct {
		name  string
		value string // TOML literal
		want  string
		warn  bool
	}{
		{"min_int", "60", "60", false},
		{"max_int", "255", "255", false},
		{"min_str", `"60"`, "60", false},
		{"max_str", `"255"`, "255", false},
		{"below_min_raised", "59", "60", true},   // raised to MinFixedStop
		{"below_min_str", `"59"`, "60", true},    // same via string
		{"above_max_int", "256", "auto", true},   // default for k10temp
		{"above_max_str", `"256"`, "auto", true}, // same via string
		{"auto", `"auto"`, "auto", false},
		{"auto_padded", `" auto "`, "auto", false}, // enum values are trimmed and lower-cased (AUDIT: normalisation)
		{"negative", "-1", "auto", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, warns, err := Parse([]byte(channelTOML("stop = " + c.value)))
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Channels[0].Stop; got != c.want {
				t.Errorf("stop %s → %q, want %q", c.value, got, c.want)
			}
			if c.warn {
				hasWarn(t, warns, "channel.x.stop")
			} else {
				noWarn(t, warns, "channel.x.stop")
			}
		})
	}
	// the drivetemp default is HDDStop, not "auto"
	cfg, warns, _ := Parse([]byte("[[channel]]\nname = \"h\"\npwm = 2\nsensor = \"drivetemp:max\"\nstop = 256\n"))
	hasWarn(t, warns, "channel.h.stop")
	if cfg.Channels[0].Stop != HDDStop {
		t.Errorf("drivetemp out-of-range stop → %q, want %q", cfg.Channels[0].Stop, HDDStop)
	}
}

func TestCurveBounds(t *testing.T) {
	if MinCurvePts != 2 || MaxCurvePts != 8 {
		t.Fatalf("curve point limits %d..%d", MinCurvePts, MaxCurvePts)
	}
	pts := func(n int) string {
		var s []string
		for i := 0; i < n; i++ {
			s = append(s, fmt.Sprintf("[%d,%d]", 30+i*10, 50+i*25))
		}
		return "[" + strings.Join(s, ",") + "]"
	}
	cases := []struct {
		name  string
		curve string
		want  [][2]int // nil → DefaultCurve
		warn  bool
	}{
		{"two_points", pts(2), [][2]int{{30, 50}, {40, 75}}, false},
		{"eight_points", pts(8), [][2]int{{30, 50}, {40, 75}, {50, 100}, {60, 125}, {70, 150}, {80, 175}, {90, 200}, {100, 225}}, false},
		{"one_point", pts(1), nil, true},
		{"nine_points", pts(9), nil, true},
		{"equal_temps", "[[45,85],[45,255]]", nil, true},
		{"falling_temps", "[[80,85],[45,255]]", nil, true},
		{"temp_min", fmt.Sprintf("[[%d,0],[0,255]]", MinCurveTemp), [][2]int{{MinCurveTemp, 0}, {0, 255}}, false},
		{"temp_max", fmt.Sprintf("[[0,0],[%d,255]]", MaxCurveTemp), [][2]int{{0, 0}, {MaxCurveTemp, 255}}, false},
		{"temp_below_min", fmt.Sprintf("[[%d,0],[0,255]]", MinCurveTemp-1), nil, true},
		{"temp_above_max", fmt.Sprintf("[[0,0],[%d,255]]", MaxCurveTemp+1), nil, true},
		{"duty_equal_allowed", "[[40,100],[60,100]]", [][2]int{{40, 100}, {60, 100}}, false},
		{"duty_falling", "[[40,200],[60,100]]", nil, true},
		{"duty_256", "[[40,0],[60,256]]", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, warns, err := Parse([]byte(channelTOML("curve = "+c.curve, "critical = 130")))
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.Channels) != 1 {
				t.Fatalf("channel dropped: %v", warns)
			}
			want := c.want
			if want == nil {
				want = DefaultCurve()
			}
			if got := cfg.Channels[0].Curve; !reflect.DeepEqual(got, want) {
				t.Errorf("curve %v, want %v", got, want)
			}
			if c.warn {
				hasWarn(t, warns, "channel.x.curve")
			} else {
				noWarn(t, warns, "channel.x.curve")
			}
		})
	}
}

func TestPWMBounds(t *testing.T) {
	if MaxPWM != 8 {
		t.Fatalf("MaxPWM = %d", MaxPWM)
	}
	for _, c := range []struct {
		pwm  int
		kept bool
	}{{1, true}, {8, true}, {0, false}, {9, false}, {-1, false}} {
		t.Run(fmt.Sprint(c.pwm), func(t *testing.T) {
			cfg, warns, err := Parse([]byte(fmt.Sprintf("[[channel]]\nname = \"x\"\npwm = %d\nsensor = \"k10temp\"\ncurve = [[45,85],[80,255]]\ncritical = 88\n", c.pwm)))
			if err != nil {
				t.Fatal(err)
			}
			if c.kept {
				if len(cfg.Channels) != 1 || cfg.Channels[0].PWM != c.pwm {
					t.Fatalf("pwm %d not kept: %+v %v", c.pwm, cfg.Channels, warns)
				}
				noWarn(t, warns, "channel.x.pwm")
				return
			}
			if len(cfg.Channels) != 0 {
				t.Fatalf("pwm %d kept: %+v", c.pwm, cfg.Channels)
			}
			hasWarn(t, warns, "channel.x.pwm")
		})
	}
}

func TestLogBounds(t *testing.T) {
	def := Default().Log
	if MinLogSizeMB != 1 || MaxLogSizeMB != 100 || MinLogFiles != 1 || MaxLogFiles != 20 {
		t.Fatalf("log limits changed: size %d..%d files %d..%d", MinLogSizeMB, MaxLogSizeMB, MinLogFiles, MaxLogFiles)
	}
	cases := []struct {
		key   string
		value int
		want  int
		warn  bool
	}{
		{"max_size_mb", 1, 1, false},
		{"max_size_mb", 100, 100, false},
		{"max_size_mb", 0, def.MaxSizeMB, true},
		{"max_size_mb", 101, def.MaxSizeMB, true},
		{"max_files", 1, 1, false},
		{"max_files", 20, 20, false},
		{"max_files", 0, def.MaxFiles, true},
		{"max_files", 21, def.MaxFiles, true},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%s=%d", c.key, c.value), func(t *testing.T) {
			cfg, warns, err := Parse([]byte(fmt.Sprintf("[log]\n%s = %d\n", c.key, c.value)))
			if err != nil {
				t.Fatal(err)
			}
			got := cfg.Log.MaxSizeMB
			if c.key == "max_files" {
				got = cfg.Log.MaxFiles
			}
			if got != c.want {
				t.Errorf("value %d, want %d", got, c.want)
			}
			if c.warn {
				hasWarn(t, warns, "log."+c.key)
				if len(warns) != 1 {
					t.Errorf("want exactly one warning, got %v", warns)
				}
			} else if len(warns) != 0 {
				t.Errorf("in-range value warned: %v", warns)
			}
		})
	}
}

func TestDashboardSensorBounds(t *testing.T) {
	if MaxDashboardSensors != 8 {
		t.Fatalf("MaxDashboardSensors = %d", MaxDashboardSensors)
	}
	ids := func(n int) string {
		var s []string
		for i := 0; i < n; i++ {
			s = append(s, fmt.Sprintf("\"hwmon:x:temp%d\"", i+1))
		}
		return "[" + strings.Join(s, ", ") + "]"
	}
	cfg, warns, err := Parse([]byte("[dashboard]\nsensors = " + ids(8) + "\n"))
	if err != nil || len(warns) != 0 || len(cfg.Dashboard.Sensors) != 8 {
		t.Errorf("exactly 8 sensors: %d kept, warnings %v, err %v", len(cfg.Dashboard.Sensors), warns, err)
	}
	cfg, warns, err = Parse([]byte("[dashboard]\nsensors = " + ids(9) + "\n"))
	if err != nil || len(cfg.Dashboard.Sensors) != 8 {
		t.Errorf("9 sensors: %d kept, err %v", len(cfg.Dashboard.Sensors), err)
	}
	hasWarn(t, warns, "dashboard.sensors")
	if len(warns) != 1 || !strings.Contains(warns[0].Msg, "hwmon:x:temp9") {
		t.Errorf("9th id must be named in the single warning: %v", warns)
	}
	// duplicates do not count towards the cap
	cfg, warns, _ = Parse([]byte("[dashboard]\nsensors = " + strings.Replace(ids(9), "temp9", "temp1", 1) + "\n"))
	if len(warns) != 0 || len(cfg.Dashboard.Sensors) != 8 {
		t.Errorf("8 unique + 1 duplicate: %d kept, warnings %v", len(cfg.Dashboard.Sensors), warns)
	}
}

func TestPresetNameBounds(t *testing.T) {
	for _, c := range []struct {
		name string
		ok   bool
	}{
		{"a", true},
		{strings.Repeat("z", 64), true},
		{strings.Repeat("z", 65), false},
		{"A", false},
		{"../x", false},
		{"", false},
		{"n5pro-quiet_2", true},
	} {
		if got := ValidPresetName(c.name); got != c.ok {
			t.Errorf("ValidPresetName(%q) = %v, want %v", c.name, got, c.ok)
		}
	}
}
