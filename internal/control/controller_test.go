package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/pvefand/internal/config"
)

func TestInterpolate(t *testing.T) {
	curve := [][2]int{{45, 85}, {80, 255}}
	cases := []struct{ milli, want int }{
		{-5000, 85}, {45000, 85}, {44999, 85}, {60000, 158}, {80000, 255}, {95000, 255}, {62500, 170},
	}
	for _, c := range cases {
		if got := Interpolate(curve, c.milli); got != c.want {
			t.Errorf("Interpolate(%d) = %d, want %d", c.milli, got, c.want)
		}
	}
	multi := [][2]int{{30, 0}, {40, 0}, {60, 128}, {80, 255}}
	for _, c := range []struct{ milli, want int }{{35000, 0}, {40000, 0}, {50000, 64}, {70000, 192}, {60000, 128}} {
		if got := Interpolate(multi, c.milli); got != c.want {
			t.Errorf("multi Interpolate(%d) = %d, want %d", c.milli, got, c.want)
		}
	}
	if Interpolate(nil, 1) != 255 {
		t.Errorf("empty curve must be full speed")
	}
}

func TestSlew(t *testing.T) {
	cases := []struct{ cur, tgt, want int }{
		{100, 200, 140}, {100, 130, 130}, {100, 50, 85}, {100, 90, 90}, {100, 100, 100}, {255, 0, 240},
	}
	for _, c := range cases {
		if got := Slew(c.cur, c.tgt, 40, 15); got != c.want {
			t.Errorf("Slew(%d→%d) = %d, want %d", c.cur, c.tgt, got, c.want)
		}
	}
}

func TestFirstCycleDirectThenSlew(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.sensors.get("k10temp").set(60000)
	h.cycles(1)
	// first cycle: direct, no slew from the assumed 255
	h.expectDuty("cpu", 158)
	h.expectDuty("ssd", 74)
	h.expectDuty("hdd", 105)
	h.expectMode("cpu", ModeAuto)
	if h.dev.enable[1] != 1 || h.dev.enable[3] != 1 {
		t.Errorf("EnterManual not called before write: %v", h.dev.enable)
	}
	if h.notify != 1 {
		t.Errorf("watchdog notify per cycle: %d", h.notify)
	}
	// jump up: limited to +40 per cycle
	h.sensors.get("k10temp").set(85000)
	h.cycles(1)
	h.expectDuty("cpu", 198)
	if h.state("cpu").Target != 255 {
		t.Errorf("target must be the unslewed value")
	}
	h.cycles(1)
	h.expectDuty("cpu", 238)
	h.cycles(1)
	h.expectDuty("cpu", 255)
	// down: limited to -15
	h.sensors.get("k10temp").set(30000)
	h.cycles(1)
	h.expectDuty("cpu", 240)
	h.cycles(10)
	h.expectDuty("cpu", 90)
	h.cycles(1)
	h.expectDuty("cpu", 85)
	if s := h.c.Snapshot(); s.Status != "ok" || s.Profile != "fake" || !s.Verified || len(s.Channels) != 3 {
		t.Errorf("snapshot: %+v", s)
	}
}

func TestPeriodicRewrite(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1) // n=0: write
	h.dev.resetCalls()
	h.cycles(5) // n=1..5: unchanged, no writes
	if n := h.dev.countCalls("write:"); n != 0 {
		t.Errorf("writes during stable cycles: %d (%v)", n, h.dev.calls)
	}
	h.cycles(1) // n=6: rewrite all three
	if n := h.dev.countCalls("write:"); n != 3 {
		t.Errorf("rewrite at n%%6==0: %d writes (%v)", n, h.dev.calls)
	}
	if n := h.dev.countCalls("manual:"); n != 3 {
		t.Errorf("rewrite must re-assert manual mode: %d", n)
	}
	// external interference: someone changed pwm1_enable back to auto
	h.dev.enable[1] = 2
	h.dev.resetCalls()
	h.cycles(6)
	if h.dev.enable[1] != 1 {
		t.Errorf("manual mode not re-asserted after interference")
	}
}

func TestOverride(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(2)
	if err := h.c.SetOverride("cpu", 300); err == nil {
		t.Errorf("duty 300 accepted")
	}
	if err := h.c.SetOverride("nope", 100); err == nil {
		t.Errorf("unknown channel accepted")
	}
	if err := h.c.SetOverride("hdd", 50); err == nil {
		t.Errorf("hdd override below %d accepted", MinHDDOverride)
	}
	if err := h.c.SetOverride("hdd", 60); err != nil {
		t.Errorf("hdd override 60: %v", err)
	}
	if err := h.c.SetOverride("cpu", 200); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	// manual: direct, no slew (85 → 200 in one step)
	h.expectDuty("cpu", 200)
	h.expectMode("cpu", ModeManual)
	h.expectDuty("hdd", 60)
	if o := h.c.Overrides(); o["cpu"] != 200 || o["hdd"] != 60 {
		t.Errorf("Overrides(): %v", o)
	}
	if err := h.c.ClearOverride("cpu"); err != nil {
		t.Fatal(err)
	}
	if err := h.c.ClearOverride("nope"); err == nil {
		t.Errorf("clear unknown accepted")
	}
	h.cycles(1)
	// back to curve (85) with slew: 200-15
	h.expectDuty("cpu", 185)
	h.expectMode("cpu", ModeAuto)
}

func TestCriticalOverridesEverything(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	if err := h.c.SetOverride("cpu", 100); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectDuty("cpu", 100)
	h.sensors.get("k10temp").set(88000) // == critical
	h.cycles(1)
	h.expectDuty("cpu", 255) // immediately, despite override and slew
	h.expectMode("cpu", ModeCritical)
	if h.alerts.count("temp") != 1 {
		t.Errorf("temp alert count %d", h.alerts.count("temp"))
	}
	h.cycles(3)
	if h.alerts.count("temp") != 1 {
		t.Errorf("temp alert must respect cooldown: %d", h.alerts.count("temp"))
	}
	if !strings.Contains(h.alerts.msgs[0], "cpu=88.0C") {
		t.Errorf("alert text: %q", h.alerts.msgs[0])
	}
	// cooldown elapsed → alert again
	h.clock.advance(31 * time.Minute)
	h.cycles(1)
	if h.alerts.count("temp") != 2 {
		t.Errorf("temp alert after cooldown: %d", h.alerts.count("temp"))
	}
	// below critical: override applies again (manual, direct)
	h.sensors.get("k10temp").set(50000)
	h.cycles(1)
	h.expectDuty("cpu", 100)
	h.expectMode("cpu", ModeManual)
	if snap := h.c.Snapshot(); snap.Alerts["temp"] == 0 {
		t.Errorf("snapshot alerts: %v", snap.Alerts)
	}
}

func TestStallAndRecovery(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	h.expectDuty("ssd", 74) // ≥ stall_min_duty 60
	h.dev.setRPM(2, 0)
	h.cycles(1) // stall count 1
	h.expectDuty("ssd", 74)
	h.expectMode("ssd", ModeAuto)
	h.cycles(1) // stall count 2 → stall
	h.expectDuty("ssd", 255)
	h.expectMode("ssd", ModeStall)
	if h.alerts.count("stall") != 1 {
		t.Fatalf("stall alert count %d", h.alerts.count("stall"))
	}
	h.cycles(3)
	h.expectDuty("ssd", 255)
	if h.alerts.count("stall") != 1 {
		t.Errorf("stall alert repeated within cooldown: %d", h.alerts.count("stall"))
	}
	h.expectDuty("cpu", 85) // other channels unaffected
	// recovery: 3 cycles with RPM > 0
	h.dev.setRPM(2, 1500)
	h.cycles(2)
	h.expectMode("ssd", ModeStall)
	h.cycles(1)
	if !h.log.contains("ssd: fan spinning again") {
		t.Errorf("recovery not logged")
	}
	h.cycles(1)
	h.expectMode("ssd", ModeAuto)
	h.expectDuty("ssd", 240) // slewing down from 255
	// RPM 0 below stall_min_duty is not a stall
	cfg := n5cfg()
	cfg.Channels[2].Curve = [][2]int{{36, 40}, {46, 255}}
	h2 := newHarness(t, cfg, nil)
	h2.dev.setRPM(3, 0)
	h2.cycles(5)
	h2.expectDuty("hdd", 40)
	h2.expectMode("hdd", ModeAuto)
}

func TestSensorErrorFailsafe(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	before := h.sensors.resolves
	h.sensors.get("nvme:max").fail(errors.New("no nvme"))
	h.cycles(1)
	for _, n := range []string{"cpu", "ssd", "hdd"} {
		h.expectDuty(n, 255)
		h.expectMode(n, ModeSensor)
	}
	if s := h.c.Snapshot(); s.Status != "sensor-error" {
		t.Errorf("status %q", s.Status)
	}
	if h.alerts.count("sensor") != 1 {
		t.Errorf("sensor alert count %d", h.alerts.count("sensor"))
	}
	if h.sensors.resolves <= before {
		t.Errorf("sensors not re-resolved after error")
	}
	h.cycles(2)
	if h.alerts.count("sensor") != 1 {
		t.Errorf("sensor alert repeated: %d", h.alerts.count("sensor"))
	}
	// temperature of the broken channel is unknown in the snapshot
	if h.state("ssd").Temp != -999 {
		t.Errorf("unknown temp must be -999, got %v", h.state("ssd").Temp)
	}
	// recovery: back to ok, slewing down from 255
	h.sensors.get("nvme:max").set(44000)
	h.cycles(1)
	if s := h.c.Snapshot(); s.Status != "ok" {
		t.Errorf("status after recovery %q", s.Status)
	}
	h.expectDuty("cpu", 240)
	h.expectMode("cpu", ModeAuto)

	// implausible value is a sensor error too
	h.sensors.get("k10temp").set(130000)
	h.cycles(1)
	h.expectDuty("hdd", 255)
	h.expectMode("hdd", ModeSensor)
}

func TestStaleSensor(t *testing.T) {
	cfg := n5cfg()
	cfg.Daemon.StaleCycles = 3
	h := newHarness(t, cfg, nil)
	h.cycles(3) // identical value 3 times → sameRaw = 2
	h.expectMode("cpu", ModeAuto)
	h.cycles(1) // sameRaw = 3 ≥ 3
	h.expectMode("cpu", ModeSensor)
	h.expectDuty("cpu", 255)
	if h.alerts.count("sensor") != 1 || !h.log.contains("frozen") {
		t.Errorf("stale must alert as sensor error")
	}
	h.sensors.get("k10temp").set(36001)
	h.cycles(1)
	h.expectMode("cpu", ModeAuto)
}

func TestUnresolvedSensorAtStart(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[0].Sensor = "later"
	h := newHarness(t, cfg, nil)
	h.cycles(1)
	h.expectMode("cpu", ModeSensor)
	h.expectDuty("ssd", 255)
	h.sensors.add("later", 50000)
	h.cycles(1)
	h.expectMode("cpu", ModeAuto)
	if s := h.c.Snapshot(); s.Status != "ok" {
		t.Errorf("status %q", s.Status)
	}
}

func TestWriteErrorFailsafe(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	h.sensors.get("k10temp").set(60000)
	h.dev.setFailWrite(1, true)
	h.cycles(1) // first failure: counted, no failsafe
	if s := h.c.Snapshot(); s.Status != "write-error" {
		t.Errorf("status %q", s.Status)
	}
	h.expectDuty("ssd", 74)
	if h.alerts.count("write") != 0 {
		t.Errorf("write alert too early")
	}
	h.cycles(1) // second consecutive failure: failsafe all + alert
	if h.alerts.count("write") != 1 {
		t.Errorf("write alert count %d", h.alerts.count("write"))
	}
	h.expectDuty("ssd", 255)
	h.expectDuty("hdd", 255)
	h.expectMode("hdd", ModeFailsafe)
	// recovery resets the counter
	h.dev.setFailWrite(1, false)
	h.cycles(1)
	if s := h.c.Snapshot(); s.Status != "ok" {
		t.Errorf("status %q", s.Status)
	}
	// the failing cycles must not have advanced cur (85): slew resumes from there
	if h.state("cpu").Duty != 125 || h.dev.getDuty(1) != 125 {
		t.Errorf("cpu after recovery: %+v device=%d", h.state("cpu"), h.dev.getDuty(1))
	}
}

func TestHistoryRing(t *testing.T) {
	cfg := n5cfg()
	cfg.Daemon.Interval = 120 * time.Second // capacity 60
	cfg.Daemon.StaleCycles = 600            // constant fake sensors must not trip stale detection
	h := newHarness(t, cfg, nil)
	if h.c.histCap != 60 {
		t.Fatalf("capacity %d", h.c.histCap)
	}
	h.cycles(65)
	all := h.c.History(24 * time.Hour)
	if len(all) != 60 {
		t.Fatalf("ring length %d", len(all))
	}
	if all[0].TS >= all[59].TS || all[59].TS-all[0].TS != 59*120 {
		t.Errorf("ring order/timestamps: %d..%d", all[0].TS, all[59].TS)
	}
	if all[59].Duty["cpu"] != 85 || all[59].Temp["cpu"] != 36 || all[59].RPM["cpu"] != 2000 {
		t.Errorf("history point: %+v", all[59])
	}
	recent := h.c.History(10 * time.Minute)
	if len(recent) != 5 {
		t.Errorf("History(10m) = %d points", len(recent))
	}
	// default interval: 2h / 10s
	if got := historyCapacity(10 * time.Second); got != 720 {
		t.Errorf("capacity at 10s: %d", got)
	}
}

func TestReload(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	h.expectDuty("cpu", 85)

	// same channel set: curve change applies on the next cycle
	cfg := n5cfg()
	cfg.Channels[0].Curve = [][2]int{{30, 100}, {80, 255}}
	cfg.Daemon.StepUp = 5
	if err := h.c.Reload(config.Marshal(cfg)); err != nil {
		t.Fatalf("reload: %v", err)
	}
	h.cycles(1)
	h.expectDuty("cpu", 90) // target 100+, slewed by new step_up 5
	if h.c.Config().Daemon.StepUp != 5 {
		t.Errorf("daemon params not swapped")
	}

	// sensor change is re-resolved
	cfg.Channels[0].Sensor = "other"
	h.sensors.add("other", 80000)
	if err := h.c.Reload(config.Marshal(cfg)); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	if h.state("cpu").Sensor != "other" || h.state("cpu").Target != 255 {
		t.Errorf("sensor swap: %+v", h.state("cpu"))
	}

	// channel renamed → restart required, nothing applied
	bad := n5cfg()
	bad.Channels[0].Name = "cpu2"
	if err := h.c.Reload(config.Marshal(bad)); !errors.Is(err, ErrRestartRequired) {
		t.Errorf("rename: %v", err)
	}
	bad = n5cfg()
	bad.Channels[0].PWM = 4
	bad.Channels = bad.Channels[:1]
	if err := h.c.Reload(config.Marshal(bad)); !errors.Is(err, ErrRestartRequired) {
		t.Errorf("pwm change: %v", err)
	}
	bad = n5cfg()
	bad.Daemon.Profile = "n5pro"
	if err := h.c.Reload(config.Marshal(bad)); !errors.Is(err, ErrRestartRequired) {
		t.Errorf("profile change: %v", err)
	}
	if err := h.c.Reload([]byte("[[[")); err == nil || errors.Is(err, ErrRestartRequired) {
		t.Errorf("syntax error: %v", err)
	}
	h.cycles(1)
	if h.state("cpu").Sensor != "other" {
		t.Errorf("rejected reload must not touch state")
	}
	// warnings are logged, config still applied with defaults
	if err := h.c.Reload([]byte(strings.Replace(string(config.Marshal(cfg)), "step_up = 5", "step_up = 999", 1))); err != nil {
		t.Fatal(err)
	}
	if !h.log.contains("config warning") {
		t.Errorf("reload warnings not logged")
	}
}

func TestStopCallsSafeStop(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cycles := 0
	h.c.opts.Wait = func(ctx context.Context, d time.Duration) bool {
		cycles++
		if cycles == 3 {
			cancel()
		}
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	if err := h.c.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Run: %v", err)
	}
	if h.notify != 3 {
		t.Errorf("cycles run: %d", h.notify)
	}
	for _, want := range []string{"stop:1=auto", "stop:2=auto", "stop:3=140"} {
		if h.dev.countCalls(want) != 1 {
			t.Errorf("missing %s in %v", want, h.dev.calls)
		}
	}
	h.c.Stop() // idempotent
	if h.dev.countCalls("stop:") != 3 {
		t.Errorf("Stop repeated SafeStop")
	}
}

func TestDryRun(t *testing.T) {
	h := newHarness(t, n5cfg(), func(o *Options) { o.DryRun = true })
	h.cycles(7)
	if n := h.dev.countCalls("write:") + h.dev.countCalls("manual:"); n != 0 {
		t.Errorf("dry-run wrote hardware: %v", h.dev.calls)
	}
	if s := h.c.Snapshot(); s.Status != "dry-run" || !s.DryRun {
		t.Errorf("status %q", s.Status)
	}
	h.expectDuty("cpu", 85) // computed anyway
	h.c.Stop()
	if h.dev.countCalls("stop:") != 0 {
		t.Errorf("dry-run must not SafeStop")
	}
}

func TestRunDirFiles(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, n5cfg(), func(o *Options) { o.RunDir = dir })
	h.cycles(1)
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snap Snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Status != "ok" || len(snap.Channels) != 3 || snap.Channels[0].Duty != 85 {
		t.Errorf("state.json: %s", raw)
	}
	if err := h.c.SetOverride("cpu", 120); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "override.cpu")); strings.TrimSpace(string(b)) != "120" {
		t.Errorf("override file: %q", b)
	}
	h.sensors.get("k10temp").set(90000)
	h.cycles(1)
	if b, _ := os.ReadFile(filepath.Join(dir, "alert.temp")); strings.TrimSpace(string(b)) != "1789500010" {
		t.Errorf("alert stamp: %q", b)
	}
	h.c.Stop()
	if _, err := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(err) {
		t.Errorf("state.json must be removed on stop")
	}

	// a restarted daemon restores the override and respects the alert stamp
	h2 := newHarness(t, n5cfg(), func(o *Options) { o.RunDir = dir })
	h2.clock.t = h.clock.now()
	if o := h2.c.Overrides(); o["cpu"] != 120 {
		t.Errorf("override not restored: %v", o)
	}
	h2.sensors.get("k10temp").set(90000)
	h2.cycles(1)
	if h2.alerts.count("temp") != 0 {
		t.Errorf("alert stamp from previous run ignored")
	}
	h2.expectDuty("cpu", 255)
	if err := h2.c.ClearOverride("cpu"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "override.cpu")); !os.IsNotExist(err) {
		t.Errorf("override file must be removed")
	}
	// invalid override file is dropped
	_ = os.WriteFile(filepath.Join(dir, "override.hdd"), []byte("12"), 0o644)
	h3 := newHarness(t, n5cfg(), func(o *Options) { o.RunDir = dir })
	if len(h3.c.Overrides()) != 0 {
		t.Errorf("invalid override restored: %v", h3.c.Overrides())
	}
}

func TestChannelNotOnDevice(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels = append(cfg.Channels, config.Channel{Name: "x", PWM: 7, Sensor: "k10temp", Curve: config.DefaultCurve(), Critical: 90, Stop: "auto"})
	h := newHarness(t, cfg, nil)
	if got := h.c.Channels(); len(got) != 3 {
		t.Errorf("channels: %v", got)
	}
	if h.alerts.count("config") != 1 {
		t.Errorf("config alert: %d", h.alerts.count("config"))
	}
}

func TestExtraTemps(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "temp1_input")
	_ = os.WriteFile(p, []byte("32000\n"), 0o644)
	h := newHarness(t, n5cfg(), nil)
	h.dev.extra = map[string]string{"ec:system": p, "ec:gone": filepath.Join(dir, "missing")}
	h.c.extra = h.dev.extra
	h.cycles(1)
	s := h.c.Snapshot()
	if s.ExtraTemps["ec:system"] != 32 || len(s.ExtraTemps) != 1 {
		t.Errorf("extra temps: %v", s.ExtraTemps)
	}
}

func TestFailsafeHelper(t *testing.T) {
	dev := newFakeDev()
	cfg := n5cfg()
	cfg.Channels = append(cfg.Channels, config.Channel{Name: "x", PWM: 7, Sensor: "s", Stop: "auto"})
	if err := Failsafe(dev, cfg); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stop:1=auto", "stop:2=auto", "stop:3=140"} {
		if dev.countCalls(want) != 1 {
			t.Errorf("missing %s in %v", want, dev.calls)
		}
	}
	if dev.countCalls("stop:7") != 0 {
		t.Errorf("SafeStop on a pwm the device lacks")
	}
	if err := FullSpeed(dev, n5cfg()); err != nil {
		t.Fatal(err)
	}
	if dev.getDuty(1) != 255 || dev.getDuty(3) != 255 {
		t.Errorf("FullSpeed: %v", dev.duty)
	}
}

func TestLogEvery(t *testing.T) {
	cfg := n5cfg()
	cfg.Daemon.LogEvery = 5
	h := newHarness(t, cfg, nil)
	h.cycles(11)
	n := 0
	for _, l := range h.log.lines {
		if strings.HasPrefix(l, "cpu=36.0C->pwm1=85(auto)") {
			n++
		}
	}
	if n != 3 { // cycles 0, 5, 10
		t.Errorf("status lines: %d\n%s", n, strings.Join(h.log.lines, "\n"))
	}
}

func TestNewValidation(t *testing.T) {
	if _, err := New(n5cfg(), nil, nil, nil, Options{}); err == nil {
		t.Errorf("nil device accepted")
	}
	if _, err := New(n5cfg(), newFakeDev(), nil, nil, Options{}); err == nil {
		t.Errorf("nil factory accepted")
	}
	var _ Service = (*Controller)(nil)
}
