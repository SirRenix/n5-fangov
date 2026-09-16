package control

import (
	"errors"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
)

// Curve post-processing (hysteresis, min_on): only the curve output is
// touched; override, critical, sensor isolation and the safe duty behave
// as pinned in safety_test.go. The harness clock advances by the interval
// (10 s) after every cycle.

func (h *harness) expectTarget(name string, want int) {
	h.t.Helper()
	if got := h.state(name).Target; got != want {
		h.t.Errorf("%s: target %d, want %d (mode %s, temp %.1f, held %.1f)", name, got, want, h.state(name).Mode, h.state(name).Temp, h.state(name).HeldTemp)
	}
}

func (h *harness) expectHeld(name string, want float64) {
	h.t.Helper()
	if got := h.state(name).HeldTemp; got != want {
		h.t.Errorf("%s: held_temp %.1f, want %.1f", name, got, want)
	}
}

func (h *harness) expectHold(name string, want int64) {
	h.t.Helper()
	if got := h.state(name).HoldUntil; got != want {
		h.t.Errorf("%s: hold_until %d, want %d", name, got, want)
	}
}

func TestHysteresisOffIsIdentical(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	hdd := h.sensors.get("drivetemp:max")
	for _, milli := range []int{40000, 41000, 40000, 42000, 40500} {
		hdd.set(milli)
		h.cycles(1)
		h.expectTarget("hdd", Interpolate(h.cfg.Channels[2].Curve, milli))
		h.expectHeld("hdd", 0)
		h.expectHold("hdd", 0)
	}
}

// TestHysteresisHoldsAcrossJitter: with hysteresis 2 the curve is evaluated
// at a held temperature that follows the reading only on a move of 2 °C or
// more; the snapshot shows the held value when it differs from the raw one.
func TestHysteresisHoldsAcrossJitter(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[2].Hysteresis = 2 // hdd: [[36,105],[46,255]] = 15 duty per degree
	h := newHarness(t, cfg, nil)
	hdd := h.sensors.get("drivetemp:max")
	hdd.set(40000)
	h.cycles(1)
	h.expectTarget("hdd", 165)
	h.expectHeld("hdd", 0) // held == raw: omitted
	// Sequence: +1 held, -1 held, +1.9 held, +2 follows (42), -1.5 held at
	// 42, +1.5 held, -2 follows (40), a big move follows (20), +1 held on
	// the flat part of the curve (held still reported), 45.999 follows
	// (254.985 → 255), -1.999 held at 45.999.
	steps := []struct {
		milli, target int
		held          float64
	}{
		{41000, 165, 40},
		{39000, 165, 40},
		{41900, 165, 40},
		{42000, 195, 0},
		{40500, 195, 42},
		{43500, 195, 42},
		{40000, 165, 0},
		{20000, 105, 0},
		{21000, 105, 20},
		{45999, 255, 0},
		{44000, 255, 45.999},
	}
	for _, s := range steps {
		hdd.set(s.milli)
		h.cycles(1)
		h.expectTarget("hdd", s.target)
		h.expectHeld("hdd", s.held)
		if st := h.state("hdd"); st.Temp != float64(s.milli)/1000 || st.Mode != ModeAuto {
			t.Errorf("%d: temp %.3f mode %s (temp must stay the raw reading)", s.milli, st.Temp, st.Mode)
		}
	}
	// the other channels are untouched
	h.expectHeld("cpu", 0)
	h.expectHeld("ssd", 0)
}

// TestMinOnHoldsRise: a rise of the curve target is held for min_on; a
// further rise above the held value restarts the timer, a smaller rise does
// not; after the hold the curve takes over.
func TestMinOnHoldsRise(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[0].MinOn = 60 * time.Second // cpu: [[45,85],[80,255]]
	h := newHarness(t, cfg, nil)
	cpu := h.sensors.get("k10temp")
	t0 := h.clock.now()
	cpu.set(60000)
	h.cycles(1) // t0: first value, not a rise
	h.expectTarget("cpu", 158)
	h.expectHold("cpu", 0)
	cpu.set(70000)
	h.cycles(1) // t0+10: rise → hold 206 until t0+70
	h.expectTarget("cpu", 206)
	h.expectHold("cpu", t0.Add(70*time.Second).Unix())
	cpu.set(60000)
	for i := 0; i < 5; i++ { // t0+20 .. t0+60: held
		h.cycles(1)
		h.expectTarget("cpu", 206)
		h.expectHold("cpu", t0.Add(70*time.Second).Unix())
		h.expectMode("cpu", ModeAuto)
	}
	h.cycles(1) // t0+70: released
	h.expectTarget("cpu", 158)
	h.expectHold("cpu", 0)
	// slew still applies to the written duty: the hold changes the target only
	if d := h.state("cpu").Duty; d != 206-15 {
		t.Errorf("duty after release: %d, want one step_down below 206", d)
	}
	// a further rise replaces the held value and restarts the timer
	cpu.set(70000)
	h.cycles(1) // t0+80: rise → 206 until t0+140
	h.expectTarget("cpu", 206)
	cpu.set(75000)
	h.cycles(1) // t0+90: rise above the held value → 231 until t0+150
	h.expectTarget("cpu", 231)
	h.expectHold("cpu", t0.Add(150*time.Second).Unix())
	cpu.set(65000) // 182: a rise from 158 would be, but it stays below the held 231
	h.cycles(1)    // t0+100
	h.expectTarget("cpu", 231)
	h.expectHold("cpu", t0.Add(150*time.Second).Unix())
	cpu.set(60000)
	h.cycles(4) // t0+110 .. t0+140: held
	h.expectTarget("cpu", 231)
	h.cycles(1) // t0+150: released
	h.expectTarget("cpu", 158)
	h.expectHold("cpu", 0)
	// a rise that the curve keeps is simply the curve (max(curve, held))
	cpu.set(80000)
	h.cycles(3)
	h.expectTarget("cpu", 255)
	// channels without min_on never hold
	h.expectHold("ssd", 0)
}

// TestMinOnClearedByOverride: a manual override replaces the target and
// ends the hold; clearing the override returns to the curve without a
// leftover hold.
func TestMinOnClearedByOverride(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[0].MinOn = 5 * time.Minute
	h := newHarness(t, cfg, nil)
	cpu := h.sensors.get("k10temp")
	cpu.set(60000)
	h.cycles(1)
	cpu.set(70000)
	h.cycles(1)
	cpu.set(60000)
	h.cycles(1)
	h.expectTarget("cpu", 206)
	if h.state("cpu").HoldUntil == 0 {
		t.Fatal("hold expected")
	}
	if err := h.c.SetOverride("cpu", 100); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectTarget("cpu", 100)
	h.expectMode("cpu", ModeManual)
	h.expectHold("cpu", 0)
	if err := h.c.ClearOverride("cpu"); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectTarget("cpu", 158)
	h.expectMode("cpu", ModeAuto)
	h.expectHold("cpu", 0)
	// a rise after the override starts a fresh hold
	cpu.set(70000)
	h.cycles(1)
	h.expectTarget("cpu", 206)
	if h.state("cpu").HoldUntil == 0 {
		t.Error("rise after the override must hold again")
	}
}

// TestPostProcessingResetOnSensorError: a sensor error drops the held
// temperature and the hold; the channel sits at its safe duty meanwhile
// and the next good reading is taken as is (no hold from "rising" out of
// the error).
func TestPostProcessingResetOnSensorError(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[2].Hysteresis = 2
	cfg.Channels[2].MinOn = 5 * time.Minute
	h := newHarness(t, cfg, nil)
	hdd := h.sensors.get("drivetemp:max")
	hdd.set(38000)
	h.cycles(1)
	hdd.set(40000) // rise → hold 165
	h.cycles(1)
	hdd.set(41000) // held at 40, hold running
	h.cycles(1)
	h.expectTarget("hdd", 165)
	h.expectHeld("hdd", 40)
	if h.state("hdd").HoldUntil == 0 {
		t.Fatal("hold expected")
	}
	hdd.fail(errors.New("drive asleep"))
	h.cycles(1)
	h.expectMode("hdd", ModeSensor)
	h.expectTarget("hdd", 140) // safe duty = fixed stop
	h.expectHeld("hdd", 0)
	h.expectHold("hdd", 0)
	if h.alerts.count("sensor") != 1 {
		t.Errorf("sensor alert on the transition: %d", h.alerts.count("sensor"))
	}
	hdd.set(43000) // readable again: taken as is, no hold
	h.cycles(1)
	h.expectMode("hdd", ModeAuto)
	h.expectTarget("hdd", 210)
	h.expectHeld("hdd", 0)
	h.expectHold("hdd", 0)
	hdd.set(44000) // +1: held at 43
	h.cycles(1)
	h.expectTarget("hdd", 210)
	h.expectHeld("hdd", 43)
}

// TestCriticalImmediateWithPostProcessing: critical is judged on the raw
// reading — a held temperature below the critical value and a running hold
// do not delay the 255.
func TestCriticalImmediateWithPostProcessing(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[0].Curve = [][2]int{{40, 80}, {90, 200}}
	cfg.Channels[0].Critical = 90
	cfg.Channels[0].Hysteresis = 10
	cfg.Channels[0].MinOn = 10 * time.Minute
	h := newHarness(t, cfg, nil)
	cpu := h.sensors.get("k10temp")
	cpu.set(70000)
	h.cycles(1)
	h.expectTarget("cpu", 152)
	cpu.set(85000) // +15 ≥ 10: follows; rise → hold 188
	h.cycles(1)
	h.expectTarget("cpu", 188)
	cpu.set(90000) // held stays 85 (< 10 apart), raw is critical
	h.cycles(1)
	h.expectTarget("cpu", 255)
	h.expectMode("cpu", ModeCritical)
	h.expectDuty("cpu", 255)
	h.expectHeld("cpu", 85)
	if h.alerts.count("temp") != 1 {
		t.Errorf("temp alert: %d", h.alerts.count("temp"))
	}
	cpu.set(85000) // below critical: back to the curve at the held value, hold still running
	h.cycles(1)
	h.expectMode("cpu", ModeAuto)
	h.expectTarget("cpu", 188)
	if h.state("cpu").HoldUntil == 0 {
		t.Error("hold must survive a critical episode")
	}
	// override + critical: critical still wins (rule 5) with both active
	if err := h.c.SetOverride("cpu", 60); err != nil {
		t.Fatal(err)
	}
	cpu.set(91000)
	h.cycles(1)
	h.expectTarget("cpu", 255)
	h.expectMode("cpu", ModeCritical)
}

// TestPostProcessingReload: Apply swaps hysteresis and min_on with the
// rest of the channel config: a shorter min_on shortens a running hold,
// min_on = 0 ends it, a changed sensor id drops the held temperature, a
// reduced hysteresis takes effect on the next reading.
func TestPostProcessingReload(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[0].MinOn = 60 * time.Second
	cfg.Channels[2].Hysteresis = 5
	h := newHarness(t, cfg, nil)
	cpu, hdd := h.sensors.get("k10temp"), h.sensors.get("drivetemp:max")
	t0 := h.clock.now()
	cpu.set(60000)
	hdd.set(40000)
	h.cycles(1) // t0
	cpu.set(70000)
	h.cycles(1) // t0+10: hold until t0+70
	cpu.set(60000)
	hdd.set(43000) // +3 < 5: held at 40
	h.cycles(1)    // t0+20
	h.expectTarget("cpu", 206)
	h.expectTarget("hdd", 165)
	h.expectHeld("hdd", 40)
	// shorter min_on: holdUntil = holdStart (t0+10) + 30 s = t0+40
	c2 := cfg.Clone()
	c2.Channels[0].MinOn = 30 * time.Second
	c2.Channels[2].Hysteresis = 2
	if err := h.c.Apply(c2); err != nil {
		t.Fatal(err)
	}
	h.cycles(1) // t0+30: applied at the start of the cycle; still holding
	h.expectTarget("cpu", 206)
	h.expectHold("cpu", t0.Add(40*time.Second).Unix())
	h.expectTarget("hdd", 210) // hysteresis 2: |43-40| ≥ 2 → follows on this reading
	h.expectHeld("hdd", 0)
	h.cycles(1) // t0+40: released
	h.expectTarget("cpu", 158)
	h.expectHold("cpu", 0)
	// a shorter value that already lies in the past ends the hold with this cycle
	cpu.set(70000)
	h.cycles(1) // t0+50: hold until t0+80
	h.cycles(1) // t0+60
	c3 := c2.Clone()
	c3.Channels[0].MinOn = 5 * time.Second
	if err := h.c.Apply(c3); err != nil {
		t.Fatal(err)
	}
	cpu.set(60000)
	h.cycles(1) // t0+70: holdUntil = max(t0+55, now) = now → not holding
	h.expectTarget("cpu", 158)
	h.expectHold("cpu", 0)
	// min_on = 0 ends a running hold
	cpu.set(70000)
	h.cycles(1) // hold
	c4 := c3.Clone()
	c4.Channels[0].MinOn = 0
	if err := h.c.Apply(c4); err != nil {
		t.Fatal(err)
	}
	cpu.set(60000)
	h.cycles(1)
	h.expectTarget("cpu", 158)
	h.expectHold("cpu", 0)
	// sensor id change drops the held temperature: the new reading is taken as is
	hdd.set(44000) // held at 43 (+1 < 2)
	h.cycles(1)
	h.expectHeld("hdd", 43)
	h.sensors.add("ec:hdd", 44000)
	c5 := c4.Clone()
	c5.Channels[2].Sensor = "ec:hdd"
	if err := h.c.Apply(c5); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectHeld("hdd", 0)
	h.expectTarget("hdd", 225)
	if h.state("hdd").Sensor != "ec:hdd" {
		t.Errorf("sensor after reload: %s", h.state("hdd").Sensor)
	}
	// longer min_on on a fresh hold works as configured
	c6 := c5.Clone()
	c6.Channels[0].MinOn = 20 * time.Second
	if err := h.c.Apply(c6); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	cpu.set(70000)
	h.cycles(1) // T: hold until T+20
	cpu.set(60000)
	h.cycles(1) // T+10: held
	h.expectTarget("cpu", 206)
	h.cycles(1) // T+20: released
	h.expectTarget("cpu", 158)
}

// TestPWM4OptionalChannel: on the N5 Pro a fourth channel on pwm4 (no
// tachometer) is accepted as configured — SanitizeChannels neither adds
// nor corrects it —, reports rpm -1, never enters the stall check, and a
// reload with the merged channel set needs no restart.
func TestPWM4OptionalChannel(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels = append(cfg.Channels, config.Channel{
		Name: "pcie", PWM: 4, Sensor: "k10temp", Curve: [][2]int{{45, 85}, {80, 255}}, Critical: 88, Stop: "auto",
	})
	dev := newN5FakeDev()
	h := newHarnessDev(t, cfg, dev, nil)
	if got := h.c.Channels(); len(got) != 4 || got[3] != "pcie" {
		t.Fatalf("channels: %v", got)
	}
	if n := h.alerts.count(AlertConfigChannels); n != 0 || h.log.contains("pwm4") {
		t.Errorf("pwm4 must not be corrected: alerts %d, log %v", n, h.log.lines)
	}
	if out, notes := SanitizeChannels("n5pro", n5cfg().Channels); len(out) != 3 || len(notes) != 0 {
		t.Errorf("SanitizeChannels must not add pwm4: %d channels, %v", len(out), notes)
	}
	h.sensors.get("k10temp").set(70000)
	dev.setRPM(4, 0)
	h.cycles(12) // duty 206 ≥ stall_min_duty with 0 RPM: no stall without a tach
	st := h.state("pcie")
	if st.RPM != -1 || st.Mode != ModeAuto || st.Target != 206 || st.Duty != 206 {
		t.Errorf("pwm4 state: %+v", st)
	}
	if h.alerts.count("stall") != 0 {
		t.Errorf("stall alert on a channel without tach")
	}
	if dev.getDuty(4) != 206 || dev.enable[4] != 1 {
		t.Errorf("pwm4 not written: duty %d enable %d", dev.getDuty(4), dev.enable[4])
	}
	// a preset merge keeps the pwm4 channel: the channel set is unchanged → no restart
	merged := cfg.Clone()
	merged.Channels[0].Curve = [][2]int{{35, 60}, {60, 150}, {80, 255}}
	merged.Channels[2].Critical = 60
	if err := h.c.Apply(merged); err != nil {
		t.Fatalf("apply merged config: %v", err)
	}
	h.cycles(1)
	if h.state("cpu").Target != Interpolate(merged.Channels[0].Curve, 70000) || h.state("pcie").Target != 206 {
		t.Errorf("after merge: cpu %d pcie %d", h.state("cpu").Target, h.state("pcie").Target)
	}
	// dropping pwm4 from the config is a channel-set change
	three := n5cfg()
	if err := h.c.Apply(three); !errors.Is(err, ErrRestartRequired) {
		t.Errorf("removing pwm4: %v", err)
	}
	// stop: pwm4 "auto" is handed to the chip like pwm1/2
	h.c.Stop()
	if dev.countCalls("stop:4=auto") != 1 {
		t.Errorf("stop calls: %v", dev.calls)
	}
}
