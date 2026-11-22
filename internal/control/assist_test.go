package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
)

// Assist (DESIGN "Assist"). The harness hdd channel: drivetemp:max, curve
// [[36,105],[46,255]] (15 duty per degree), critical 56, stop 140; the
// drive sits at 33 °C → curve target 105. Sources: the GPU 50..58 °C
// +45 and nvme:max 46..50 °C +45 (the values of the 16.11. measurement).

const gpuID = "hwmon:amdgpu:temp1"

func assistCfg(on bool, hyst int, minOn time.Duration) config.Config {
	cfg := n5cfg()
	cfg.Assists = []config.Assist{{Channel: "hdd", Enabled: on, Hysteresis: hyst, MinOn: minOn, Sources: []config.AssistSource{
		{Sensor: gpuID, Start: 50, Full: 58, Boost: 45},
		{Sensor: "nvme:max", Start: 46, Full: 50, Boost: 45},
	}}}
	return cfg
}

// newAssistHarness is newHarness with the GPU sensor registered; New does
// not know it yet, the first cycle resolves it (an unresolved source is
// retried every cycle).
func newAssistHarness(t *testing.T, cfg config.Config) *harness {
	t.Helper()
	h := newHarness(t, cfg, nil)
	h.sensors.add(gpuID, 35000)
	return h
}

func (h *harness) assist(name string) *AssistState {
	h.t.Helper()
	return h.state(name).Assist
}

func (h *harness) expectBoost(name string, want int, src string) {
	h.t.Helper()
	a := h.assist(name)
	if a == nil {
		h.t.Fatalf("%s: no assist state", name)
	}
	if a.Boost != want || a.Source != src {
		h.t.Errorf("%s: boost %d from %q, want %d from %q (target %d, mode %s)", name, a.Boost, a.Source, want, src, h.state(name).Target, h.state(name).Mode)
	}
}

func TestSourceBoostLinear(t *testing.T) {
	sc := config.AssistSource{Start: 50, Full: 58, Boost: 45}
	for _, c := range []struct{ milli, want int }{
		{20000, 0}, {50000, 0}, {50100, 1}, {52000, 11}, {54000, 23}, {56000, 34}, {57900, 44}, {58000, 45}, {90000, 45},
	} {
		if got := sourceBoost(sc, c.milli); got != c.want {
			t.Errorf("sourceBoost(%d) = %d, want %d", c.milli, got, c.want)
		}
	}
}

func TestAssistLinearAndMax(t *testing.T) {
	h := newAssistHarness(t, assistCfg(true, 0, 0))
	gpu, nvme := h.sensors.get(gpuID), h.sensors.get("nvme:max")
	nvme.set(44000)
	steps := []struct {
		gpu, nvme, boost int
		src              string
	}{
		{35000, 44000, 0, ""},
		{50000, 44000, 0, ""},          // at start: 0
		{54000, 44000, 23, gpuID},      // half way: 22.5 → 23
		{58000, 44000, 45, gpuID},      // at full
		{70000, 44000, 45, gpuID},      // above full: capped at boost
		{54000, 49000, 34, "nvme:max"}, // max over the sources: 23 vs 33.75 → 34
		{40000, 47000, 11, "nvme:max"},
	}
	for _, s := range steps {
		gpu.set(s.gpu)
		nvme.set(s.nvme)
		h.cycles(1)
		h.expectBoost("hdd", s.boost, s.src)
		h.expectTarget("hdd", 105+s.boost)
		h.expectMode("hdd", ModeAuto)
	}
	// the other channels have no table: no assist member
	for _, n := range []string{"cpu", "ssd"} {
		if a := h.state(n).Assist; a != nil {
			t.Errorf("%s: assist %+v, want none", n, a)
		}
	}
	// per-source values are reported
	a := h.assist("hdd")
	if len(a.Sources) != 2 || a.Sources[0].Sensor != gpuID || a.Sources[0].Temp != 40 || a.Sources[0].Boost != 0 || a.Sources[1].Boost != 11 {
		t.Errorf("sources %+v", a.Sources)
	}
}

func TestAssistCap255(t *testing.T) {
	h := newAssistHarness(t, assistCfg(true, 0, 0))
	h.sensors.get("drivetemp:max").set(45000) // curve 240
	h.sensors.get(gpuID).set(60000)
	h.cycles(1)
	h.expectTarget("hdd", 255)
	h.expectBoost("hdd", 45, gpuID)
}

func TestAssistHysteresisPerSource(t *testing.T) {
	h := newAssistHarness(t, assistCfg(true, 2, 0))
	gpu := h.sensors.get(gpuID)
	for _, s := range []struct{ milli, boost int }{
		{54000, 23}, // first reading
		{55000, 23}, // +1: held at 54
		{53500, 23}, // -0.5: held
		{56000, 34}, // +2: follows
		{54500, 34}, // -1.5: held at 56
		{40000, 0},  // big drop follows
	} {
		gpu.set(s.milli)
		h.cycles(1)
		h.expectBoost("hdd", s.boost, map[bool]string{true: gpuID, false: ""}[s.boost > 0])
	}
	// the channel's own hysteresis is untouched (0): held_temp not reported
	h.expectHeld("hdd", 0)
}

func TestAssistMinOnHold(t *testing.T) {
	h := newAssistHarness(t, assistCfg(true, 0, time.Minute))
	gpu := h.sensors.get(gpuID)
	gpu.set(58000)
	h.cycles(1) // rise 0 → 45: hold until +60 s
	h.expectBoost("hdd", 45, gpuID)
	gpu.set(40000)
	for i := 0; i < 5; i++ { // +10 .. +50 s: held
		h.cycles(1)
		h.expectBoost("hdd", 45, gpuID)
		h.expectTarget("hdd", 150)
	}
	h.cycles(1) // +60 s: released
	h.expectBoost("hdd", 0, "")
	h.expectTarget("hdd", 105)
	// a smaller rise inside a hold does not restart it; a larger one does
	gpu.set(54000)
	h.cycles(1)
	h.expectBoost("hdd", 23, gpuID)
	gpu.set(58000)
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID)
	gpu.set(54000)
	h.cycles(5)
	h.expectBoost("hdd", 45, gpuID)
	h.cycles(1)
	h.expectBoost("hdd", 23, gpuID)
}

func TestAssistUnreadableSourceContributesZero(t *testing.T) {
	h := newAssistHarness(t, assistCfg(true, 0, 0))
	gpu := h.sensors.get(gpuID)
	gpu.set(58000)
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID)
	gpu.fail(errors.New("gpu gone"))
	h.cycles(5)
	h.expectBoost("hdd", 0, "")
	h.expectTarget("hdd", 105)
	h.expectMode("hdd", ModeAuto)
	if a := h.assist("hdd"); a.Sources[0].Temp != -999 || a.Sources[0].Boost != 0 {
		t.Errorf("unreadable source reported as %+v", a.Sources[0])
	}
	if n := h.log.count("source " + gpuID + " unreadable"); n != 1 {
		t.Errorf("unreadable logged %d times, want once per episode", n)
	}
	if h.alerts.count("sensor") != 0 || len(h.alerts.kinds) != 0 {
		t.Errorf("alerts %v: an assist source raises none", h.alerts.kinds)
	}
	gpu.set(58000)
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID)
	if !h.log.contains("source " + gpuID + " readable again") {
		t.Error("recovery not logged")
	}
	// a source that never resolves contributes 0 and the rest still works
	cfg := assistCfg(true, 0, 0)
	cfg.Assists[0].Sources[0].Sensor = "hwmon:nothere:temp1"
	h2 := newAssistHarness(t, cfg)
	h2.sensors.get("nvme:max").set(50000)
	h2.cycles(3)
	h2.expectBoost("hdd", 45, "nvme:max")
	if n := h2.log.count(`"hwmon:nothere:temp1"`); n != 1 {
		t.Errorf("unresolvable source: resolve failure logged %d times, want 1", n)
	}
	if n := h2.log.count("hwmon:nothere:temp1 unreadable"); n != 1 {
		t.Errorf("unresolvable source: episode logged %d times, want 1", n)
	}
}

func TestAssistOffAndSwitch(t *testing.T) {
	h := newAssistHarness(t, assistCfg(false, 0, 2*time.Minute))
	h.sensors.get(gpuID).set(58000)
	h.cycles(1)
	h.expectBoost("hdd", 0, "")
	h.expectTarget("hdd", 105)
	a := h.assist("hdd")
	if a.Enabled || a.Sources[0].Boost != 45 {
		t.Errorf("off: %+v (the sources still show what they would give)", a)
	}
	if err := h.c.setAssistEnabled("hdd", true); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID)
	h.expectTarget("hdd", 150)
	h.cycles(2)
	h.expectDuty("hdd", 150)
	// off ends the boost at once although a min_on hold runs; the slew ramps down
	if err := h.c.setAssistEnabled("hdd", false); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectBoost("hdd", 0, "")
	h.expectTarget("hdd", 105)
	h.expectDuty("hdd", 150-h.cfg.Daemon.StepDown)
	if h.c.Config().Assists[0].Enabled {
		t.Error("running config not switched off")
	}
	if err := h.c.setAssistEnabled("cpu", true); !errors.Is(err, ErrNoAssist) {
		t.Errorf("cpu without table: %v", err)
	}
}

func TestAssistApplyWithoutRestart(t *testing.T) {
	h := newAssistHarness(t, n5cfg())
	h.sensors.get(gpuID).set(58000)
	h.cycles(1)
	if h.state("hdd").Assist != nil {
		t.Fatal("assist state without a table")
	}
	if err := h.c.Apply(assistCfg(true, 0, 0)); err != nil {
		t.Fatalf("apply: %v (assist must not need a restart)", err)
	}
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID)
	if err := h.c.Apply(n5cfg()); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	if h.state("hdd").Assist != nil {
		t.Error("table removed, state still present")
	}
	h.expectTarget("hdd", 105)
}

// setAssistEnabled flips the switch of a loaded table through ApplyAssists
// (the running tables with one flag changed). Without a loaded table it is
// ErrNoAssist — what the 0.7.0-rc1 switch answered (it changed the running
// config only), the HTTP 500 of BACKLOG #63.
func (c *Controller) setAssistEnabled(name string, on bool) error {
	tables := config.CloneAssists(c.Config().Assists)
	for i := range tables {
		if tables[i].Channel == name {
			tables[i].Enabled = on
			return c.ApplyAssists(tables, name)
		}
	}
	return fmt.Errorf("channel %q: %w", name, ErrNoAssist)
}

// TestApplyAssistsHandEdited (BACKLOG #63): a daemon started without
// [[assist]], the table added to the file by hand. The rc1 switch knew
// only the running config (setAssistEnabled → ErrNoAssist, HTTP 500
// "config written, not applied"); ApplyAssists takes the file's tables:
// added on the next cycle, an unchanged table keeps its hold, a removed one
// is dropped.
func TestApplyAssistsHandEdited(t *testing.T) {
	h := newAssistHarness(t, n5cfg())
	gpu := h.sensors.get(gpuID)
	gpu.set(58000)
	h.cycles(1)
	if err := h.c.setAssistEnabled("hdd", true); !errors.Is(err, ErrNoAssist) {
		t.Fatalf("the rc1 path: %v, want ErrNoAssist", err)
	}
	tables := assistCfg(true, 0, 30*time.Minute).Assists
	if err := h.c.ApplyAssists(tables, "hdd"); err != nil {
		t.Fatal(err)
	}
	if h.state("hdd").Assist != nil {
		t.Error("applied before the next cycle")
	}
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID)
	if s := h.assist("hdd").Sources[0]; s.Start != 50 || s.Full != 58 {
		t.Errorf("source thresholds %d..%d, want 50..58", s.Start, s.Full)
	}
	gpu.set(40000)
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID) // held by min_on
	if err := h.c.ApplyAssists(tables, "hdd"); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID) // unchanged table: the hold survives
	if len(h.c.Config().Channels) != len(n5cfg().Channels) {
		t.Error("channels changed")
	}
	if err := h.c.ApplyAssists(nil, ""); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	if h.state("hdd").Assist != nil {
		t.Error("table removed, state still present")
	}
	h.expectTarget("hdd", 105)
}

// TestApplyAssistsUnfitChangesNothing: the switched channel's table does
// not fit the running channels (its source is the running channel's own
// sensor — the file changed the channel's sensor by hand too). The call
// answers ErrNoAssist and applies nothing: neither that channel's running
// table and hold nor another channel's table change, also after cycles.
func TestApplyAssistsUnfitChangesNothing(t *testing.T) {
	cfg := assistCfg(true, 0, 30*time.Minute)
	cfg.Assists = append(cfg.Assists, config.Assist{Channel: "ssd", Enabled: true, Hysteresis: 2, MinOn: 2 * time.Minute,
		Sources: []config.AssistSource{{Sensor: gpuID, Start: 50, Full: 58, Boost: 20}}})
	h := newAssistHarness(t, cfg)
	gpu := h.sensors.get(gpuID)
	gpu.set(58000)
	h.cycles(1)
	gpu.set(40000)
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID) // held
	bad := config.CloneAssists(cfg.Assists[:1])
	bad[0].Sources[0].Sensor = "drivetemp:max" // the running hdd channel's own sensor
	if err := h.c.ApplyAssists(bad, "hdd"); !errors.Is(err, ErrNoAssist) {
		t.Fatalf("unfit table: %v, want ErrNoAssist", err)
	}
	h.cycles(2)
	h.expectBoost("hdd", 45, gpuID) // table and hold untouched
	if a := h.assist("hdd"); a == nil || a.Sources[0].Sensor != gpuID || len(a.Sources) != 2 {
		t.Errorf("hdd table changed: %+v", a)
	}
	if cfg := h.c.Config(); h.state("ssd").Assist == nil || cfg.Assist("ssd") == nil {
		t.Error("the ssd table was dropped by a refused call")
	}
}

// TestApplyAssistsChangedTable: a table changed by hand (boost lowered
// below the held value) ends the hold at once, like a reload
// (dropStaleHold); a source dropped against the running channels while
// the switched channel's table fits is logged.
func TestApplyAssistsChangedTable(t *testing.T) {
	cfg := assistCfg(true, 0, 30*time.Minute)
	h := newAssistHarness(t, cfg)
	gpu := h.sensors.get(gpuID)
	gpu.set(58000)
	h.cycles(1)
	gpu.set(40000)
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID) // held
	next := config.CloneAssists(cfg.Assists)
	next[0].Sources[0].Boost = 10
	other := config.Assist{Channel: "ssd", Enabled: true, Sources: []config.AssistSource{{Sensor: "nvme:max", Start: 40, Full: 50, Boost: 10}}} // the ssd's own sensor
	if err := h.c.ApplyAssists(append(next, other), "hdd"); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectBoost("hdd", 0, "")
	h.expectTarget("hdd", 105)
	if cfg := h.c.Config(); cfg.Assist("ssd") != nil {
		t.Error("an unfit ssd table was applied")
	}
	if !h.log.contains("assist: assist.ssd") {
		t.Errorf("the dropped ssd table was not logged: %v", h.log.lines)
	}
}

// TestApplyAssistsOnPendingReload: a reload accepted but not yet applied
// (pending) is the base: the assist switch in the same cycle keeps the
// reload's changes and adds its tables.
func TestApplyAssistsOnPendingReload(t *testing.T) {
	h := newAssistHarness(t, n5cfg())
	h.sensors.get(gpuID).set(58000)
	h.cycles(1)
	next := n5cfg()
	next.Channels[2].Critical = 58
	if err := h.c.Apply(next); err != nil {
		t.Fatal(err)
	}
	if err := h.c.ApplyAssists(assistCfg(true, 0, 0).Assists, "hdd"); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	if c := h.c.Config(); c.Channel("hdd").Critical != 58 || c.Assist("hdd") == nil {
		t.Errorf("pending reload lost or tables missing: critical %d, %+v", c.Channel("hdd").Critical, c.Assists)
	}
	h.expectBoost("hdd", 45, gpuID)
}

// TestAssistPrecedence: override, critical, ceiling and the channel's own
// sensor error set the target; assist adds nothing then.
func TestAssistPrecedence(t *testing.T) {
	h := newAssistHarness(t, assistCfg(true, 0, 2*time.Minute))
	h.sensors.get(gpuID).set(58000)
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID)

	if err := h.c.SetOverride("hdd", 120); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectMode("hdd", ModeManual)
	h.expectTarget("hdd", 120)
	h.expectBoost("hdd", 0, "")
	if err := h.c.ClearOverride("hdd"); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID)

	drive := h.sensors.get("drivetemp:max")
	drive.set(56000) // critical
	h.cycles(1)
	h.expectMode("hdd", ModeCritical)
	h.expectTarget("hdd", 255)
	h.expectBoost("hdd", 0, "")
	drive.set(33000)
	h.cycles(1)

	drive.fail(errors.New("drive gone")) // the channel's sensor: safe duty, no assist
	h.cycles(1)
	h.expectMode("hdd", ModeSensor)
	h.expectTarget("hdd", 140)
	h.expectBoost("hdd", 0, "")
	drive.set(33000)
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID)

	// ceiling: lowered to 40 (critical 56 above it); 41 °C → ceiling state
	cfg := assistCfg(true, 0, 0)
	cfg.Channels[2].Ceiling = 40
	hc := newAssistHarness(t, cfg)
	hc.sensors.get(gpuID).set(58000)
	hc.sensors.get("drivetemp:max").set(41000)
	hc.cycles(1)
	if st := hc.state("hdd"); !st.CeilingHit || st.Mode != ModeCritical || st.Target != 255 {
		t.Errorf("ceiling: %+v", st)
	}
	hc.expectBoost("hdd", 0, "")
}

// TestAssistPrecedenceStallFailsafe: a stalled fan and the all-channel
// failsafe set 255 themselves; the boost reported is 0.
func TestAssistPrecedenceStallFailsafe(t *testing.T) {
	h := newAssistHarness(t, assistCfg(true, 0, 0))
	h.sensors.get(gpuID).set(58000)
	h.cycles(1)
	h.expectDuty("hdd", 150)
	h.dev.setRPM(3, 0)
	h.cycles(2) // stall_cycles = 2 at a written duty ≥ stall_min_duty
	h.expectMode("hdd", ModeStall)
	h.expectTarget("hdd", 255)
	h.expectBoost("hdd", 0, "")

	hf := newAssistHarness(t, assistCfg(true, 0, 0))
	hf.sensors.get(gpuID).set(58000)
	hf.cycles(1)
	hf.sensors.get(gpuID).set(54000) // a new target, so the next cycle writes
	hf.dev.setFailWrite(3, true)
	hf.cycles(2) // two write errors in a row → failsafe
	hf.expectMode("hdd", ModeFailsafe)
	hf.expectBoost("hdd", 0, "")
	if st := hf.state("hdd"); st.Target != 255 {
		t.Errorf("failsafe target %d", st.Target)
	}
}

// TestAssistReloadDropsStaleHold: a reload that removes the held source or
// lowers its boost below the held value ends the hold at once (no +45 for
// up to min_on under a source that no longer gives it); a raised boost
// keeps it.
func TestAssistReloadDropsStaleHold(t *testing.T) {
	for _, c := range []struct {
		name string
		mod  func(*config.Assist)
		want int
		src  string
	}{
		{"boost lowered", func(a *config.Assist) { a.Sources[0].Boost = 10 }, 0, ""},
		{"source removed", func(a *config.Assist) { a.Sources = a.Sources[1:] }, 0, ""},
		{"boost raised", func(a *config.Assist) { a.Sources[0].Boost = 60 }, 45, gpuID},
		{"other source changed", func(a *config.Assist) { a.Sources[1].Boost = 5 }, 45, gpuID},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg := assistCfg(true, 0, 30*time.Minute)
			h := newAssistHarness(t, cfg)
			gpu := h.sensors.get(gpuID)
			gpu.set(58000)
			h.cycles(1)
			gpu.set(40000)
			h.cycles(1)
			h.expectBoost("hdd", 45, gpuID) // held
			next := cfg.Clone()
			c.mod(&next.Assists[0])
			if err := h.c.Apply(next); err != nil {
				t.Fatal(err)
			}
			h.cycles(1)
			h.expectBoost("hdd", c.want, c.src)
			h.expectTarget("hdd", 105+c.want)
		})
	}
}

// TestAssistSourceReresolve: sources are resolved again every
// resolveEvery cycles like the channel sensors, also while they read fine
// (a composite resolved before all its devices appeared).
func TestAssistSourceReresolve(t *testing.T) {
	h := newAssistHarness(t, assistCfg(true, 0, 0))
	h.sensors.get(gpuID).set(58000)
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID)
	h.sensors.add(gpuID, 40000) // the factory now hands out another reader
	for h.c.n < resolveEvery {
		h.cycles(1)
	}
	h.expectBoost("hdd", 45, gpuID) // still the old reader
	h.cycles(1)                     // cycle resolveEvery: re-resolved
	h.expectBoost("hdd", 0, "")
	if a := h.assist("hdd"); a.Sources[0].Temp != 40 {
		t.Errorf("source temp %v after re-resolve", a.Sources[0].Temp)
	}
}

// TestAssistWhileFirstChannelStale: the stale check on the first channel's
// k10temp puts only that channel at its safe duty; assist on hdd goes on.
func TestAssistWhileFirstChannelStale(t *testing.T) {
	cfg := assistCfg(true, 0, 0)
	cfg.Daemon.StaleCycles = 6
	h := newAssistHarness(t, cfg)
	h.sensors.get(gpuID).set(58000)
	h.cycles(10) // k10temp bit-identical throughout
	h.expectMode("cpu", ModeSensor)
	h.expectMode("hdd", ModeAuto)
	h.expectBoost("hdd", 45, gpuID)
	h.expectTarget("hdd", 150)
}

func TestAssistStatusLineAndHistory(t *testing.T) {
	cfg := assistCfg(true, 0, 0)
	cfg.Dashboard.Sensors = []string{"k10temp"}
	h := newAssistHarness(t, cfg)
	h.sensors.get(gpuID).set(58700)
	h.cycles(1)
	line := h.c.statusLine(h.c.chans)
	if !strings.Contains(line, "hdd=33.0C->pwm3=150(auto +45 amdgpu)") {
		t.Errorf("status line %q", line)
	}
	if n := strings.Count(line, "(auto +"); n != 1 {
		t.Errorf("%d channels carry a boost in %q, want only hdd", n, line)
	}
	pts := h.c.History(time.Hour)
	if len(pts) != 1 {
		t.Fatalf("%d points", len(pts))
	}
	p := pts[0]
	if p.Assist["hdd"] != 45 || len(p.Assist) != 1 {
		t.Errorf("assist %v", p.Assist)
	}
	// the gpu source is recorded; nvme:max is the ssd channel's sensor
	// (recorded as temp.ssd) and k10temp a watched id: neither twice
	if p.Extra[gpuID] != 58.7 || len(p.Extra) != 2 {
		t.Errorf("extra %v, want the gpu source and the watched k10temp", p.Extra)
	}
	if _, dup := p.Extra["nvme:max"]; dup {
		t.Error("nvme:max recorded twice")
	}
	// off: no assist member in the point
	if err := h.c.setAssistEnabled("hdd", false); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	pts = h.c.History(time.Hour)
	if last := pts[len(pts)-1]; last.Assist != nil {
		t.Errorf("assist off, point carries %v", last.Assist)
	}
	// the snapshot JSON omits assist for channels without a table
	b, _ := json.Marshal(h.c.Snapshot())
	var doc struct {
		Channels []map[string]any `json:"channels"`
	}
	_ = json.Unmarshal(b, &doc)
	for _, ch := range doc.Channels {
		_, has := ch["assist"]
		if has != (ch["name"] == "hdd") {
			t.Errorf("channel %v: assist member present = %v", ch["name"], has)
		}
	}
}

// TestAssistReplay16Nov replays the 10 s history of the measurement of
// 16.11.2026 (testdata, anonymised: GPU, hottest NVMe, hottest drive, the
// HDD duty) with the HDD values of the design. The HDD channel's curve is
// reconstructed from the recorded auto duties (40 °C → 99, 41/42 °C →
// 106); without assist it ran 99 idle and 106 under load. With assist the
// target must reach ≈ 150 under load and stay 99 at idle.
func TestAssistReplay16Nov(t *testing.T) {
	rows := loadReplay(t, "testdata/assist-replay-20261116.csv")
	cfg := config.Default()
	cfg.Channels = []config.Channel{
		{Name: "hdd", PWM: 3, Sensor: "drivetemp:max", Curve: [][2]int{{40, 99}, {41, 106}, {42, 106}, {55, 255}}, Critical: 60, Stop: "140"},
	}
	cfg.Assists = []config.Assist{{Channel: "hdd", Enabled: true, Hysteresis: 2, MinOn: 2 * time.Minute, Sources: []config.AssistSource{
		{Sensor: gpuID, Start: 50, Full: 58, Boost: 45},
		{Sensor: "nvme:max", Start: 46, Full: 50, Boost: 45},
	}}}
	h := newAssistHarness(t, cfg)
	gpu, nvme, drive := h.sensors.get(gpuID), h.sensors.get("nvme:max"), h.sensors.get("drivetemp:max")
	type win struct {
		name     string
		from, to int
		n        int
		target   float64
		boost    float64
		maxBoost int
		recorded float64
	}
	// idle: everything before the GPU warms up (t = 2779, some 80 s before
	// the logged load start; the design's idle row 2230..2860 ends inside
	// that warm-up, where assist rightly starts to raise)
	wins := []*win{{name: "idle", from: 0, to: 2770}, {name: "warmup", from: 2779, to: 2860}, {name: "idle-row", from: 2230, to: 2860},
		{name: "A0", from: 3460, to: 4061}, {name: "B", from: 4661, to: 5262}, {name: "A1", from: 5862, to: 6462}}
	for _, r := range rows {
		gpu.set(r.gpu)
		nvme.set(r.nvme)
		drive.set(r.drive)
		h.cycles(1)
		st := h.state("hdd")
		for _, w := range wins {
			if r.t >= w.from && r.t < w.to {
				w.n++
				w.target += float64(st.Target)
				w.boost += float64(st.Assist.Boost)
				w.maxBoost = max(w.maxBoost, st.Assist.Boost)
				w.recorded += float64(r.duty)
			}
		}
	}
	for _, w := range wins {
		if w.n == 0 {
			t.Fatalf("window %s empty", w.name)
		}
		w.target /= float64(w.n)
		w.boost /= float64(w.n)
		w.recorded /= float64(w.n)
		t.Logf("replay %-4s n=%3d  target %.1f  boost %.1f (max %d)  recorded duty without assist %.1f", w.name, w.n, w.target, w.boost, w.maxBoost, w.recorded)
	}
	if w := wins[0]; w.maxBoost != 0 || w.target < 98.5 || w.target > 99.5 {
		t.Errorf("idle: target %.1f, max boost %d; want 99 unchanged", w.target, w.maxBoost)
	}
	if w := wins[1]; w.maxBoost == 0 {
		t.Errorf("warm-up (GPU 50..60 °C): no boost")
	}
	for _, w := range wins[3:] {
		if w.target < 145 || w.target > 155 {
			t.Errorf("%s: target %.1f, want ≈ 150", w.name, w.target)
		}
	}
}

type replayRow struct{ t, gpu, nvme, drive, duty int }

// loadReplay reads t,igpu,nvme_max,drive_max,hdd_duty (# comments, a
// header line); temperatures become millidegrees.
func loadReplay(t *testing.T, path string) []replayRow {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []replayRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "t,") {
			continue
		}
		f := strings.Split(line, ",")
		if len(f) != 5 {
			t.Fatalf("bad row %q", line)
		}
		num := func(s string, scale float64) int {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				t.Fatalf("bad value %q in %q", s, line)
			}
			return int(v*scale + 0.5)
		}
		out = append(out, replayRow{t: num(f[0], 1), gpu: num(f[1], 1000), nvme: num(f[2], 1000), drive: num(f[3], 1000), duty: num(f[4], 1)})
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) < 600 {
		t.Fatalf("%d rows", len(out))
	}
	return out
}
