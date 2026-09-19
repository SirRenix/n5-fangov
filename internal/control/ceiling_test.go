package control

// Tests for the hard ceilings and the emergency action (DESIGN "Ceilings
// and emergency", 0.4.1).

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/sensor"
)

// fakeExec records emergency hook runs.
type fakeExec struct {
	mu    sync.Mutex
	calls []string   // the hook paths
	envs  [][]string // the env of each call
	out   string
	err   error
}

func (f *fakeExec) run(_ context.Context, path string, env []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, path)
	f.envs = append(f.envs, env)
	return []byte(f.out), f.err
}

func (f *fakeExec) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// hookOK is the HookCheck of the tests: the file is the operator's, the
// ownership rule is HookStatus's own test (TestHookStatus).
func hookOK(string) HookState { return HookState{OK: true, State: "ok"} }

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

// A sensor that fails inside a ceiling episode does not end it and does
// not drop the channel to its safe duty: 255 stays (mode sensor-error)
// until a reading ends the episode.
func TestCeilingKeeps255ThroughSensorError(t *testing.T) {
	h := newHarness(t, hddCfg(), nil)
	hdd := h.sensors.get("drivetemp:max")
	h.cycles(1)
	hdd.set(66000)
	h.cycles(1)
	h.expectMode("hdd", ModeCritical)
	hdd.fail(errors.New("gone"))
	h.cycles(3)
	h.expectMode("hdd", ModeSensor)
	h.expectDuty("hdd", 255)
	if s := h.state("hdd"); !s.CeilingHit {
		t.Errorf("episode ended by a sensor error: %+v", s)
	}
	// a reading below the exit line ends the episode; the next error is
	// the ordinary safe duty (140)
	hdd.set(50000)
	h.cycles(1)
	h.expectMode("hdd", ModeAuto)
	hdd.fail(errors.New("gone"))
	h.cycles(3)
	h.expectMode("hdd", ModeSensor)
	h.expectDuty("hdd", 140)
}

// Composite sensors: every part is judged against its own kind's ceiling.
// The channel's reported ceiling is the lowest part ceiling.
func TestCeilingCompositePerPart(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[0].Sensor = "k10temp,drivetemp:max" // cpu fed by the hotter of both
	cfg.Channels[0].Critical = 95
	h := newHarness(t, cfg, nil)
	h.cycles(1)
	if s := h.state("cpu"); s.Ceiling != sensor.CeilingHDD {
		t.Errorf("reported composite ceiling (lowest part): %+v", s)
	}
	// the CPU part at 70 C is far below its own ceiling (100): no episode,
	// although the channel reading (70) is above the HDD part's 65
	h.sensors.get("k10temp").set(70000)
	h.cycles(1)
	h.expectMode("cpu", ModeAuto)
	if s := h.state("cpu"); s.CeilingHit || h.alerts.count("ceiling") != 0 {
		t.Fatalf("cpu part judged against the hdd ceiling: %+v %v", s, h.alerts.kinds)
	}
	// the HDD part at 66 C is above its ceiling (65): episode, the alert
	// names the part
	h.sensors.get("drivetemp:max").set(66000)
	h.cycles(1)
	h.expectMode("cpu", ModeCritical)
	h.expectDuty("cpu", 255)
	msg := lastMsg(h.alerts, "ceiling")
	if !strings.Contains(msg, "cpu: drivetemp:max of k10temp,drivetemp:max at 66.0C reached the ceiling 65C (critical 95C)") {
		t.Errorf("alert message: %q", msg)
	}
	// hdd (pwm3, drivetemp:max alone) is in its own episode too
	h.expectMode("hdd", ModeCritical)
	// exit: the HDD part must be 3 C below its ceiling; the CPU part may
	// stay at 70
	h.sensors.get("drivetemp:max").set(63000)
	h.cycles(1)
	h.expectMode("cpu", ModeCritical)
	h.sensors.get("drivetemp:max").set(61000)
	h.cycles(1)
	h.expectMode("cpu", ModeAuto)
	if !h.log.contains("cpu: drivetemp:max of k10temp,drivetemp:max below the ceiling again") {
		t.Errorf("recovery line:\n%s", strings.Join(h.log.lines, "\n"))
	}
	// the CPU part at its own ceiling (100) enters the episode (clock past
	// the alert cooldown so the second ceiling alert is delivered)
	h.clock.advance(31 * time.Minute)
	h.sensors.get("k10temp").set(100000)
	h.cycles(1)
	h.expectMode("cpu", ModeCritical)
	if msg := lastMsg(h.alerts, "ceiling"); h.alerts.count("ceiling") < 2 || !strings.Contains(msg, "cpu: k10temp of k10temp,drivetemp:max at 100.0C reached the ceiling 100C") {
		t.Errorf("cpu part alert: %q (%v)", msg, h.alerts.kinds)
	}
	// the recommended mixed pair: NVMe at 70 C (ceiling 85) next to an
	// HDD part (65) is not an episode
	cfg2 := n5cfg()
	cfg2.Channels[2].Sensor = "nvme:max,drivetemp:max"
	cfg2.Channels[2].Critical = 90
	h2 := newHarness(t, cfg2, nil)
	h2.cycles(1)
	h2.sensors.get("nvme:max").set(70000)
	h2.cycles(1)
	h2.expectMode("hdd", ModeAuto)
	h2.sensors.get("nvme:max").set(85000)
	h2.cycles(1)
	h2.expectMode("hdd", ModeCritical)
	// a configured ceiling lowers every part
	cfg3 := n5cfg()
	cfg3.Channels[2].Sensor = "nvme:max,drivetemp:max"
	cfg3.Channels[2].Critical = 70
	cfg3.Channels[2].Ceiling = 50
	h3 := newHarness(t, cfg3, nil)
	h3.cycles(1)
	if s := h3.state("hdd"); s.Ceiling != 50 {
		t.Errorf("configured composite ceiling: %+v", s)
	}
	h3.sensors.get("nvme:max").set(50000)
	h3.cycles(1)
	h3.expectMode("hdd", ModeCritical)
}

func TestCeilingConfiguredAndReload(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[1].Ceiling = 60 // lowered ssd ceiling (built-in 85)
	cfg.Channels[2].Ceiling = 90 // cannot raise the hdd ceiling (65)
	h := newHarness(t, cfg, nil)
	h.cycles(1)
	if s := h.state("ssd"); s.Ceiling != 60 {
		t.Errorf("configured ceiling: %+v", s)
	}
	if s := h.state("hdd"); s.Ceiling != sensor.CeilingHDD {
		t.Errorf("raised ceiling accepted: %+v", s)
	}
	h.sensors.get("nvme:max").set(61000)
	h.cycles(1)
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

// A disk:<dev> whose device is absent at start (unresolved, kind "") gets
// its kind — and its 65 — when the sensor resolves later, without a
// reload.
func TestCeilingRefreshedWhenDiskAppears(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[2].Sensor = "disk:sda"
	var kindMu sync.Mutex
	kind := ""
	h := newHarness(t, cfg, func(o *Options) {
		o.DiskKind = func(string) string { kindMu.Lock(); defer kindMu.Unlock(); return kind }
	})
	h.cycles(2)
	h.expectMode("hdd", ModeSensor)
	if s := h.state("hdd"); s.Ceiling != sensor.CeilingDefault {
		t.Fatalf("ceiling of an unresolved disk: %+v", s)
	}
	// the disk appears: the next cycle resolves the sensor and recomputes
	kindMu.Lock()
	kind = "hdd"
	kindMu.Unlock()
	h.sensors.add("disk:sda", 66000)
	h.cycles(1)
	if s := h.state("hdd"); s.Ceiling != sensor.CeilingHDD || !s.CeilingHit {
		t.Errorf("ceiling after the disk appeared: %+v", s)
	}
	h.expectDuty("hdd", 255)
	// the periodic re-resolve refreshes it as well: kind changes (a
	// re-enumerated device) are picked up at the 60-cycle mark
	kindMu.Lock()
	kind = "ssd"
	kindMu.Unlock()
	h.sensors.get("disk:sda").set(40000)
	h.cycles(resolveEvery + 2)
	if s := h.state("hdd"); s.Ceiling != sensor.CeilingSSD {
		t.Errorf("ceiling after re-resolve: %+v", s)
	}
}

// emergencyHarness: hdd critical 70, emergency on, emergency_cycles 2, a
// fake exec and an accepting hook check.
func emergencyHarness(t *testing.T, on bool) (*harness, *fakeExec) {
	t.Helper()
	cfg := hddCfg()
	cfg.Daemon.Emergency = on
	cfg.Daemon.EmergencyCycles = 2
	fx := &fakeExec{out: "hook ran\nsecond line\n"}
	h := newHarness(t, cfg, func(o *Options) { o.Exec = fx.run; o.HookCheck = hookOK })
	return h, fx
}

// The 0-RPM rule rests on the stall detection (stall_cycles = 2 zero
// readings at a duty the fan should turn at), never on a single sample.
func TestEmergencyAfterCyclesWithStalledFan(t *testing.T) {
	h, fx := emergencyHarness(t, true)
	hdd := h.sensors.get("drivetemp:max")
	h.cycles(1)
	hdd.set(67000)
	h.dev.setRPM(3, 0) // the fan is dead
	h.cycles(2)        // ceiling cycles 1, 2: the stall is detected in cycle 2, after the rule
	if fx.count() != 0 || h.alerts.count("emergency") != 0 {
		t.Fatalf("fired before the stall was established: %d", fx.count())
	}
	if h.alerts.count("stall") != 1 {
		t.Fatalf("stall not raised: %v", h.alerts.kinds)
	}
	h.cycles(1) // ceiling cycle 3 >= emergency_cycles with the fan stalled
	if fx.count() != 1 {
		t.Fatalf("emergency hook runs: %d, want 1", fx.count())
	}
	if h.alerts.count("emergency") != 1 {
		t.Fatalf("emergency alerts: %v", h.alerts.kinds)
	}
	msg := lastMsg(h.alerts, "emergency")
	if !strings.Contains(msg, "hdd: emergency action after 3 cycles at the ceiling (fan reports 0 RPM): hook exited 0") {
		t.Errorf("alert message: %q", msg)
	}
	env := strings.Join(fx.envs[0], " ")
	for _, want := range []string{"N5_CHANNEL=hdd", "N5_SENSOR=drivetemp:max", "N5_PART=drivetemp:max", "N5_TEMP=67.0", "N5_CEILING=65", "N5_RPM=0", "N5_CYCLES=3"} {
		if !strings.Contains(env, want) {
			t.Errorf("env %q lacks %q", env, want)
		}
	}
	if fx.calls[0] != DefaultEmergencyHook {
		t.Errorf("hook path: %q", fx.calls[0])
	}
	if !h.log.contains("running " + DefaultEmergencyHook) {
		t.Errorf("run not announced:\n%s", strings.Join(h.log.lines, "\n"))
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
	h.cycles(3)
	if fx.count() != 2 {
		t.Errorf("not re-armed after leaving the ceiling state: %d", fx.count())
	}
	// the alert is under the cooldown, the hook is not
	if h.alerts.count("emergency") != 1 {
		t.Errorf("emergency alert under cooldown: %v", h.alerts.kinds)
	}
}

// A single 0 RPM sample in the cycle the counter reaches emergency_cycles
// does not fire (the stall detection needs stall_cycles of them).
func TestEmergencyIgnoresSingleZeroSample(t *testing.T) {
	h, fx := emergencyHarness(t, true)
	hdd := h.sensors.get("drivetemp:max")
	h.cycles(1)
	hdd.set(67000)
	h.cycles(1) // ceiling cycle 1, fan spins
	h.dev.setRPM(3, 0)
	h.cycles(1) // ceiling cycle 2 = emergency_cycles, one zero sample
	h.dev.setRPM(3, 1200)
	h.cycles(2) // cycles 3, 4: spinning again
	if fx.count() != 0 || h.alerts.count("stall") != 0 {
		t.Errorf("fired on one sample: runs=%d alerts=%v", fx.count(), h.alerts.kinds)
	}
}

func TestEmergencyAfterThreeTimesCyclesRegardlessOfRPM(t *testing.T) {
	h, fx := emergencyHarness(t, true)
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
	cfg.Daemon.Emergency = true
	cfg.Daemon.EmergencyCycles = 1
	cfg.Channels = append(cfg.Channels, config.Channel{Name: "pcie", PWM: 4, Sensor: "nvme:max", Curve: [][2]int{{45, 85}, {80, 255}}, Critical: 90, Stop: "auto"})
	fx2 := &fakeExec{}
	h2 := newHarness(t, cfg, func(o *Options) { o.Exec = fx2.run; o.HookCheck = hookOK })
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

func TestEmergencyOffDoesNothing(t *testing.T) {
	h, fx := emergencyHarness(t, false)
	hdd := h.sensors.get("drivetemp:max")
	h.cycles(1)
	hdd.set(67000)
	h.dev.setRPM(3, 0)
	h.cycles(12)
	if fx.count() != 0 || h.alerts.count("emergency") != 0 {
		t.Errorf("hook ran with emergency = false: %d alerts=%v", fx.count(), h.alerts.kinds)
	}
	if h.alerts.count("ceiling") != 1 {
		t.Errorf("ceiling alert: %v", h.alerts.kinds)
	}
	if h.log.contains("running ") || h.log.contains("emergency action") {
		t.Errorf("log announces a run with emergency = false")
	}
}

// A hook the check refuses (absent, wrong owner or mode) is logged once
// per episode and never executed; no emergency alert, the ceiling alert
// covers the episode.
func TestEmergencyRefusedHook(t *testing.T) {
	cfg := hddCfg()
	cfg.Daemon.Emergency = true
	cfg.Daemon.EmergencyCycles = 1
	fx := &fakeExec{}
	h := newHarness(t, cfg, func(o *Options) {
		o.Exec = fx.run
		o.EmergencyHook = "/etc/n5-fangov/emergency.sh"
		o.HookCheck = func(string) HookState { return HookState{State: "refused: world-writable (mode 0777)"} }
	})
	h.cycles(1)
	h.sensors.get("drivetemp:max").set(67000)
	h.cycles(6)
	if fx.count() != 0 || h.alerts.count("emergency") != 0 {
		t.Errorf("refused hook ran: %d alerts=%v", fx.count(), h.alerts.kinds)
	}
	if h.log.count("hook /etc/n5-fangov/emergency.sh refused: world-writable (mode 0777), nothing runs") != 1 {
		t.Errorf("refusal line:\n%s", strings.Join(h.log.lines, "\n"))
	}
	// the absent state reads the same way
	h2 := newHarness(t, cfg, func(o *Options) {
		o.Exec = fx.run
		o.HookCheck = func(string) HookState { return HookState{State: "absent"} }
	})
	h2.cycles(1)
	h2.sensors.get("drivetemp:max").set(67000)
	h2.cycles(4)
	if !h2.log.contains("hook " + DefaultEmergencyHook + " absent, nothing runs") {
		t.Errorf("absent line:\n%s", strings.Join(h2.log.lines, "\n"))
	}
}

// Hook output is capped at HookOutputMax bytes in the log.
func TestEmergencyOutputCapped(t *testing.T) {
	cfg := hddCfg()
	cfg.Daemon.Emergency = true
	cfg.Daemon.EmergencyCycles = 1
	fx := &fakeExec{out: strings.Repeat("x", HookOutputMax+500) + "\n"}
	h := newHarness(t, cfg, func(o *Options) { o.Exec = fx.run; o.HookCheck = hookOK })
	h.cycles(1)
	h.sensors.get("drivetemp:max").set(67000)
	h.cycles(3)
	if fx.count() != 1 {
		t.Fatalf("runs: %d", fx.count())
	}
	if h.log.count("emergency[hdd]: "+strings.Repeat("x", HookOutputMax)) != 1 || h.log.count("emergency[hdd]: ... output truncated at 4 KiB") != 1 {
		t.Errorf("output not capped:\n%.300s", strings.Join(h.log.lines, "\n"))
	}
	for _, l := range h.log.lines {
		if len(l) > HookOutputMax+40 {
			t.Errorf("log line of %d bytes", len(l))
		}
	}
}

// The real exec: exit status and timeout end up in the alert text; the
// timeout kills the hook's whole process group.
func TestEmergencyExecStatusAndTimeout(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	writeHook := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg := hddCfg()
	cfg.Daemon.Emergency = true
	cfg.Daemon.EmergencyCycles = 1
	hook := writeHook("hook.sh", "echo \"$N5_CHANNEL at $N5_TEMP\"; exit 3\n")
	h := newHarness(t, cfg, func(o *Options) { o.EmergencyHook = hook; o.HookCheck = hookOK })
	h.cycles(1)
	h.sensors.get("drivetemp:max").set(67000)
	h.cycles(3)
	if h.alerts.count("emergency") != 1 {
		t.Fatalf("alerts: %v", h.alerts.kinds)
	}
	if msg := lastMsg(h.alerts, "emergency"); !strings.HasSuffix(msg, "hook exited 3") {
		t.Errorf("exit status: %q", msg)
	}
	if !h.log.contains("emergency[hdd]: hdd at 67.0") {
		t.Errorf("hook output not logged:\n%s", strings.Join(h.log.lines, "\n"))
	}

	// a hook that backgrounds a child holding the pipes: the group kill
	// ends both, well inside the 2 s pipe grace (WaitDelay)
	slow := writeHook("slow.sh", "sleep 5 &\nsleep 5\n")
	h2 := newHarness(t, cfg, func(o *Options) {
		o.EmergencyHook = slow
		o.HookCheck = hookOK
		o.EmergencyTimeout = 100 * time.Millisecond
	})
	h2.cycles(1)
	h2.sensors.get("drivetemp:max").set(67000)
	start := time.Now()
	h2.cycles(3)
	if el := time.Since(start); el > 1500*time.Millisecond {
		t.Errorf("timeout did not end the process group: %s", el)
	}
	if msg := lastMsg(h2.alerts, "emergency"); !strings.Contains(msg, "hook exited timeout after 100ms") {
		t.Errorf("timeout status: %q", msg)
	}

	// the real output cap: 6000 bytes on stdout, 4096 kept
	loud := writeHook("loud.sh", "i=0; while [ $i -lt 60 ]; do printf '%0100d\\n' 0; i=$((i+1)); done\n")
	out, err := hookExec(context.Background(), loud, nil)
	if err != nil || len(out) != HookOutputMax+1 {
		t.Errorf("capped output: %d bytes, err %v", len(out), err)
	}
}

// HookStatus applies the file rules of the fixed hook path.
func TestHookStatus(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "emergency.sh")
	if st := HookStatus(p); st.OK || st.State != "absent" {
		t.Errorf("absent: %+v", st)
	}
	if st := HookStatus(dir); st.OK || st.State != "refused: not a regular file" {
		t.Errorf("directory: %+v", st)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o777); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		mode os.FileMode
		want string
	}{
		{0o777, "refused: world-writable (mode 0777)"},
		{0o775, "refused: group-writable (mode 0775)"},
		{0o640, "refused: not executable (mode 0640)"},
		{0o600, "refused: not executable (mode 0600)"},
	}
	for _, c := range cases {
		if err := os.Chmod(p, c.mode); err != nil {
			t.Fatal(err)
		}
		if st := HookStatus(p); st.OK || st.State != c.want {
			t.Errorf("mode %04o: %+v, want %q", c.mode, st, c.want)
		}
	}
	for _, mode := range []os.FileMode{0o700, 0o750, 0o755} {
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		st := HookStatus(p)
		switch {
		case os.Getuid() == 0 && (!st.OK || st.State != "ok"):
			t.Errorf("root, mode %04o: %+v", mode, st)
		case os.Getuid() != 0 && (st.OK || !strings.HasPrefix(st.State, "refused: not owned by root (uid ")):
			t.Errorf("non-root, mode %04o: %+v", mode, st)
		}
	}
	link := filepath.Join(dir, "link.sh")
	if err := os.Symlink(p, link); err == nil {
		if st := HookStatus(link); st.OK || st.State != "refused: symbolic link" {
			t.Errorf("symlink: %+v", st)
		}
	}
}
