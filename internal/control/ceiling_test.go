package control

// Tests for the hard ceilings and the emergency action (DESIGN "Ceilings
// and emergency", 0.4.1).

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/sensor"
)

// fakeExec records emergency command runs.
type fakeExec struct {
	mu    sync.Mutex
	calls []string   // the commands
	envs  [][]string // the env of each call
	out   string
	err   error
}

func (f *fakeExec) run(_ context.Context, command string, env []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, command)
	f.envs = append(f.envs, env)
	return []byte(f.out), f.err
}

func (f *fakeExec) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// hddCfg is n5cfg with the hdd critical above the built-in ceiling (65):
// the ceiling must act first.
func hddCfg() config.Config {
	cfg := n5cfg()
	cfg.Channels[2].Critical = 70
	return cfg
}

// lastMsg returns the newest alert message of kind (the stall check runs
// after the ceiling rule, so a dead fan raises stall right behind emergency).
func lastMsg(a *fakeAlerter, kind string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.kinds) - 1; i >= 0; i-- {
		if a.kinds[i] == kind {
			return a.msgs[i]
		}
	}
	return ""
}

func TestCeilingForces255BelowCritical(t *testing.T) {
	h := newHarness(t, hddCfg(), nil)
	hdd := h.sensors.get("drivetemp:max")
	h.cycles(1)
	h.expectMode("hdd", ModeAuto)
	if s := h.state("hdd"); s.Ceiling != sensor.CeilingHDD || s.CeilingHit {
		t.Fatalf("ceiling fields at start: %+v", s)
	}
	hdd.set(66000) // above the ceiling (65), below critical (70)
	h.cycles(1)
	h.expectDuty("hdd", 255)
	h.expectMode("hdd", ModeCritical)
	if s := h.state("hdd"); !s.CeilingHit {
		t.Errorf("ceiling_hit must be true: %+v", s)
	}
	if h.alerts.count("ceiling") != 1 || h.alerts.count("temp") != 0 {
		t.Errorf("alerts: %v", h.alerts.kinds)
	}
	msg := lastMsg(h.alerts, "ceiling")
	for _, want := range []string{"hdd: drivetemp:max at 66.0C", "ceiling 65C", "critical 70C", "-> 255"} {
		if !strings.Contains(msg, want) {
			t.Errorf("alert message %q lacks %q", msg, want)
		}
	}
	// stays 255 every cycle (no slew, no rewrite gap)
	h.cycles(3)
	h.expectDuty("hdd", 255)
	h.expectMode("hdd", ModeCritical)
	if h.alerts.count("ceiling") != 1 {
		t.Errorf("alert repeated: %v", h.alerts.kinds)
	}
	// the other channels regulate normally
	h.expectMode("cpu", ModeAuto)
}

func TestCeilingOverridesManual(t *testing.T) {
	h := newHarness(t, hddCfg(), nil)
	h.cycles(1)
	if err := h.c.SetOverride("hdd", 100); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectDuty("hdd", 100)
	h.expectMode("hdd", ModeManual)
	h.sensors.get("drivetemp:max").set(65000) // exactly at the ceiling counts
	h.cycles(1)
	h.expectDuty("hdd", 255)
	h.expectMode("hdd", ModeCritical)
	// hysteresis and min_on cannot hold it below either
	cfg := hddCfg()
	cfg.Channels[2].Hysteresis = 5
	cfg.Channels[2].MinOn = time.Hour
	h2 := newHarness(t, cfg, nil)
	h2.cycles(1)
	h2.sensors.get("drivetemp:max").set(65000)
	h2.cycles(1)
	h2.expectDuty("hdd", 255)
	h2.expectMode("hdd", ModeCritical)
	if s := h2.state("hdd"); s.HoldUntil != 0 {
		t.Errorf("hold reported at the ceiling: %+v", s)
	}
}

func TestCeilingRecoveryHysteresis(t *testing.T) {
	h := newHarness(t, hddCfg(), nil)
	hdd := h.sensors.get("drivetemp:max")
	h.cycles(1)
	hdd.set(66000)
	h.cycles(1)
	h.expectMode("hdd", ModeCritical)
	// below the ceiling but not 3 degrees below: still in the state
	hdd.set(63000)
	h.cycles(2)
	h.expectDuty("hdd", 255)
	h.expectMode("hdd", ModeCritical)
	if s := h.state("hdd"); !s.CeilingHit {
		t.Errorf("must stay in the ceiling state at 63: %+v", s)
	}
	hdd.set(62000) // ceiling - 3: still in (the rule is strictly below)
	h.cycles(1)
	h.expectMode("hdd", ModeCritical)
	hdd.set(61900)
	h.cycles(1)
	h.expectMode("hdd", ModeAuto)
	if s := h.state("hdd"); s.CeilingHit || s.Target != 255 {
		// 61.9 C is above the last curve point (46): curve target 255, but back in mode auto
		t.Errorf("after recovery: %+v", s)
	}
	if !h.log.contains("hdd: drivetemp:max below the ceiling again") {
		t.Errorf("recovery log line missing")
	}
	hdd.set(40000)
	h.cycles(1)
	h.expectMode("hdd", ModeAuto)
	if s := h.state("hdd"); s.Target != Interpolate(hddCfg().Channels[2].Curve, 40000) {
		t.Errorf("curve target after recovery: %+v", s)
	}
}

func TestCeilingCompositeAndConfigured(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[0].Sensor = "k10temp,drivetemp:max" // cpu fed by the hotter of both: lowest ceiling wins
	cfg.Channels[0].Critical = 95
	cfg.Channels[1].Ceiling = 60 // lowered ssd ceiling (built-in 85)
	cfg.Channels[2].Ceiling = 90 // cannot raise the hdd ceiling (65)
	h := newHarness(t, cfg, nil)
	h.sensors.add("k10temp,drivetemp:max", 40000)
	h.cycles(1)
	if s := h.state("cpu"); s.Ceiling != sensor.CeilingHDD {
		t.Errorf("composite ceiling: %+v", s)
	}
	if s := h.state("ssd"); s.Ceiling != 60 {
		t.Errorf("configured ceiling: %+v", s)
	}
	if s := h.state("hdd"); s.Ceiling != sensor.CeilingHDD {
		t.Errorf("raised ceiling accepted: %+v", s)
	}
	h.sensors.get("k10temp,drivetemp:max").set(66000)
	h.sensors.get("nvme:max").set(61000)
	h.cycles(1)
	h.expectMode("cpu", ModeCritical)
	h.expectDuty("cpu", 255)
	h.expectMode("ssd", ModeCritical)
	h.expectDuty("ssd", 255)
	// a reload that lowers the hdd ceiling takes effect on the next cycle
	cfg.Channels[2].Ceiling = 40
	if err := h.c.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	if s := h.state("hdd"); s.Ceiling != 40 {
		t.Errorf("ceiling after reload: %+v", s)
	}
	h.sensors.get("drivetemp:max").set(41000)
	h.cycles(1)
	h.expectMode("hdd", ModeCritical)
	// DiskKind resolves disk:<dev> ids
	cfg2 := n5cfg()
	cfg2.Channels[2].Sensor = "disk:sda"
	h2 := newHarness(t, cfg2, func(o *Options) {
		o.DiskKind = func(dev string) string { return map[string]string{"sda": "hdd"}[dev] }
	})
	h2.sensors.add("disk:sda", 40000)
	h2.cycles(1)
	if s := h2.state("hdd"); s.Ceiling != sensor.CeilingHDD {
		t.Errorf("disk kind ceiling: %+v", s)
	}
}

// emergencyHarness: hdd critical 70, emergency_cycles 2, a fake exec.
func emergencyHarness(t *testing.T, command string) (*harness, *fakeExec) {
	t.Helper()
	cfg := hddCfg()
	cfg.Daemon.EmergencyCommand = command
	cfg.Daemon.EmergencyCycles = 2
	fx := &fakeExec{out: "hook ran\nsecond line\n"}
	h := newHarness(t, cfg, func(o *Options) { o.Exec = fx.run })
	return h, fx
}

func TestEmergencyAfterCyclesWithZeroRPM(t *testing.T) {
	h, fx := emergencyHarness(t, "/usr/local/sbin/hook")
	hdd := h.sensors.get("drivetemp:max")
	h.cycles(1)
	hdd.set(67000)
	h.dev.setRPM(3, 0) // the fan is dead
	h.cycles(1)        // ceiling cycle 1
	if fx.count() != 0 || h.alerts.count("emergency") != 0 {
		t.Fatalf("fired too early: %d", fx.count())
	}
	h.cycles(1) // ceiling cycle 2 = emergency_cycles
	if fx.count() != 1 {
		t.Fatalf("emergency command runs: %d, want 1", fx.count())
	}
	if h.alerts.count("emergency") != 1 {
		t.Fatalf("emergency alerts: %v", h.alerts.kinds)
	}
	msg := lastMsg(h.alerts, "emergency")
	if !strings.Contains(msg, "hdd: emergency action after 2 cycles at the ceiling (fan reports 0 RPM): command exited 0") {
		t.Errorf("alert message: %q", msg)
	}
	env := strings.Join(fx.envs[0], " ")
	for _, want := range []string{"N5_CHANNEL=hdd", "N5_SENSOR=drivetemp:max", "N5_TEMP=67.0", "N5_CEILING=65", "N5_RPM=0", "N5_CYCLES=2"} {
		if !strings.Contains(env, want) {
			t.Errorf("env %q lacks %q", env, want)
		}
	}
	if fx.calls[0] != "/usr/local/sbin/hook" {
		t.Errorf("command: %q", fx.calls[0])
	}
	if h.log.count("emergency[hdd]: hook ran") != 1 || h.log.count("emergency[hdd]: second line") != 1 {
		t.Errorf("output not logged:\n%s", strings.Join(h.log.lines, "\n"))
	}
	// once per episode
	h.cycles(10)
	if fx.count() != 1 {
		t.Errorf("re-run inside the episode: %d", fx.count())
	}
	// leaving the state re-arms it
	hdd.set(50000)
	h.dev.setRPM(3, 1200)
	h.cycles(4) // the stall the dead fan caused recovers after 3 cycles with RPM
	h.expectMode("hdd", ModeAuto)
	hdd.set(67000)
	h.dev.setRPM(3, 0)
	h.cycles(2)
	if fx.count() != 2 {
		t.Errorf("not re-armed after leaving the ceiling state: %d", fx.count())
	}
	// the alert is under the cooldown, the command is not
	if h.alerts.count("emergency") != 1 {
		t.Errorf("emergency alert under cooldown: %v", h.alerts.kinds)
	}
}

func TestEmergencyAfterThreeTimesCyclesRegardlessOfRPM(t *testing.T) {
	h, fx := emergencyHarness(t, "hook")
	hdd := h.sensors.get("drivetemp:max")
	h.cycles(1)
	hdd.set(67000) // the fan spins (1200 RPM), cooling just does not work
	h.cycles(5)    // ceiling cycles 1..5
	if fx.count() != 0 {
		t.Fatalf("fired before 3 x emergency_cycles: %d", fx.count())
	}
	h.cycles(1) // 6 = 3 x 2
	if fx.count() != 1 {
		t.Fatalf("emergency runs: %d", fx.count())
	}
	if msg := lastMsg(h.alerts, "emergency"); !strings.Contains(msg, "after 6 cycles at the ceiling (cooling ineffective)") {
		t.Errorf("alert message: %q", msg)
	}
	if !strings.Contains(strings.Join(fx.envs[0], " "), "N5_RPM=1200") {
		t.Errorf("env: %v", fx.envs[0])
	}
	// a channel without tach (pwm4) uses the 3 x rule only
	cfg := hddCfg()
	cfg.Daemon.EmergencyCommand = "hook"
	cfg.Daemon.EmergencyCycles = 1
	cfg.Channels = append(cfg.Channels, config.Channel{Name: "pcie", PWM: 4, Sensor: "nvme:max", Curve: [][2]int{{45, 85}, {80, 255}}, Critical: 90, Stop: "auto"})
	fx2 := &fakeExec{}
	h2 := newHarness(t, cfg, func(o *Options) { o.Exec = fx2.run })
	h2.cycles(1)
	h2.sensors.get("nvme:max").set(86000)
	h2.cycles(2)
	h2.expectMode("pcie", ModeCritical)
	if n := fx2.count(); n != 0 {
		t.Fatalf("no-tach channel fired on the RPM rule: %d", n)
	}
	h2.cycles(1)
	// ssd (pwm2, tach, 2100 RPM) and pcie (no tach) both reached 3 cycles
	if n := fx2.count(); n != 2 {
		t.Errorf("runs after 3 cycles: %d, want 2 (ssd + pcie)", n)
	}
}

func TestEmergencyEmptyCommandDoesNothing(t *testing.T) {
	h, fx := emergencyHarness(t, "")
	hdd := h.sensors.get("drivetemp:max")
	h.cycles(1)
	hdd.set(67000)
	h.dev.setRPM(3, 0)
	h.cycles(12)
	if fx.count() != 0 || h.alerts.count("emergency") != 0 {
		t.Errorf("empty command ran: %d alerts=%v", fx.count(), h.alerts.kinds)
	}
	if h.alerts.count("ceiling") != 1 {
		t.Errorf("ceiling alert: %v", h.alerts.kinds)
	}
	if h.log.contains("running emergency_command") {
		t.Errorf("log announces a run with an empty command")
	}
}

// The real shell: exit status and timeout end up in the alert text.
func TestEmergencyShellStatusAndTimeout(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	cfg := hddCfg()
	cfg.Daemon.EmergencyCommand = "echo \"$N5_CHANNEL at $N5_TEMP\"; exit 3"
	cfg.Daemon.EmergencyCycles = 1
	h := newHarness(t, cfg, nil)
	h.cycles(1)
	h.sensors.get("drivetemp:max").set(67000)
	h.dev.setRPM(3, 0)
	h.cycles(1)
	if h.alerts.count("emergency") != 1 {
		t.Fatalf("alerts: %v", h.alerts.kinds)
	}
	if msg := lastMsg(h.alerts, "emergency"); !strings.HasSuffix(msg, "command exited 3") {
		t.Errorf("exit status: %q", msg)
	}
	if !h.log.contains("emergency[hdd]: hdd at 67.0") {
		t.Errorf("shell output not logged:\n%s", strings.Join(h.log.lines, "\n"))
	}

	cfg.Daemon.EmergencyCommand = "sleep 5"
	h2 := newHarness(t, cfg, func(o *Options) { o.EmergencyTimeout = 100 * time.Millisecond })
	h2.cycles(1)
	h2.sensors.get("drivetemp:max").set(67000)
	h2.dev.setRPM(3, 0)
	start := time.Now()
	h2.cycles(1)
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("timeout did not bound the command: %s", el)
	}
	if msg := lastMsg(h2.alerts, "emergency"); !strings.Contains(msg, "command exited timeout after 100ms") {
		t.Errorf("timeout status: %q", msg)
	}
}
