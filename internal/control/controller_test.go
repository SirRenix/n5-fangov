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

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/history"
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
	// The re-assert is logged once
	if n := h.log.count("pwm1_enable was \"2\""); n != 1 {
		t.Errorf("interference log lines: %d\n%s", n, strings.Join(h.log.lines, "\n"))
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

// A sensor read error isolates its channel (safe duty, mode sensor-error);
// the other channels keep regulating and the status stays "ok".
func TestSensorErrorIsolatesChannel(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	before := h.sensors.resolves
	h.sensors.get("nvme:max").fail(errors.New("no nvme"))
	h.cycles(1)
	h.expectDuty("ssd", 255) // stop "auto" -> 255
	h.expectMode("ssd", ModeSensor)
	h.expectDuty("cpu", 85) // others regulate as before
	h.expectMode("cpu", ModeAuto)
	h.expectDuty("hdd", 105)
	h.expectMode("hdd", ModeAuto)
	if s := h.c.Snapshot(); s.Status != "ok" {
		t.Errorf("status %q, want ok while other channels regulate", s.Status)
	}
	if h.alerts.count("sensor") != 1 {
		t.Fatalf("sensor alert count %d", h.alerts.count("sensor"))
	}
	if msg := h.alerts.msgs[0]; !strings.Contains(msg, `channel "ssd"`) || !strings.Contains(msg, "nvme:max") ||
		!strings.Contains(msg, "no nvme") || !strings.Contains(msg, "duty 255") || !strings.Contains(msg, "other channels keep regulating") {
		t.Errorf("alert must name the channel, sensor, cause and duty: %q", msg)
	}
	if h.sensors.resolves <= before {
		t.Errorf("sensor not re-resolved after error")
	}
	h.cycles(2)
	if h.alerts.count("sensor") != 1 {
		t.Errorf("sensor alert repeated: %d", h.alerts.count("sensor"))
	}
	// temperature of the broken channel is unknown in the snapshot
	if h.state("ssd").Temp != -999 {
		t.Errorf("unknown temp must be -999, got %v", h.state("ssd").Temp)
	}
	// the curve keeps working on the healthy channels meanwhile
	h.sensors.get("k10temp").set(60000)
	h.cycles(1)
	h.expectDuty("cpu", 125) // 85 + step_up 40 towards 158
	h.expectMode("cpu", ModeAuto)
	// recovery: ssd back to the curve, slewing down from 255; logged
	h.sensors.get("nvme:max").set(44000)
	h.cycles(1)
	h.expectDuty("ssd", 240)
	h.expectMode("ssd", ModeAuto)
	if !h.log.contains("ssd: sensor nvme:max readable again") {
		t.Errorf("recovery not logged:\n%s", strings.Join(h.log.lines, "\n"))
	}
	// a second failure after the cooldown alerts again (transition)
	h.clock.advance(31 * time.Minute)
	h.sensors.get("nvme:max").fail(errors.New("gone again"))
	h.cycles(1)
	if h.alerts.count("sensor") != 2 {
		t.Errorf("alert on re-failure after cooldown: %d", h.alerts.count("sensor"))
	}

	// implausible value is a sensor error too, and only for that channel
	h.sensors.get("nvme:max").set(44000)
	h.sensors.get("k10temp").set(130000)
	h.cycles(1)
	h.expectDuty("cpu", 255)
	h.expectMode("cpu", ModeSensor)
	h.expectMode("hdd", ModeAuto)
	h.expectDuty("hdd", 105)
	if !h.log.contains("implausible 130000") {
		t.Errorf("implausible reading not logged")
	}
}

// A channel with a fixed stop duty goes to that duty on a sensor error,
// not to 255 (the operator's "nobody regulates this fan" value); a manual
// override does not apply while the temperature is unknown.
func TestSensorErrorUsesStopDuty(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	if err := h.c.SetOverride("hdd", 200); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectDuty("hdd", 200)
	h.sensors.get("drivetemp:max").fail(errors.New("no drives"))
	h.cycles(1)
	h.expectDuty("hdd", 140) // stop = "140"
	h.expectMode("hdd", ModeSensor)
	if h.dev.getDuty(3) != 140 {
		t.Errorf("device duty %d", h.dev.getDuty(3))
	}
	if !strings.Contains(h.alerts.msgs[0], "duty 140 (configured stop duty)") {
		t.Errorf("alert: %q", h.alerts.msgs[0])
	}
	// stays there across a rewrite cycle, with the override still stored
	h.cycles(6)
	h.expectDuty("hdd", 140)
	if h.c.Overrides()["hdd"] != 200 {
		t.Errorf("override dropped")
	}
	// back: the override applies again
	h.sensors.get("drivetemp:max").set(33000)
	h.cycles(1)
	h.expectDuty("hdd", 200)
	h.expectMode("hdd", ModeManual)
}

// A sensor the factory cannot resolve at all (drivetemp absent on an N5 Pro
// without HDDs) isolates only the built-in hdd channel; cpu and ssd
// regulate. When every sensor fails the status turns "sensor-error".
func TestSensorFactoryFailsForOneChannel(t *testing.T) {
	h := newHarnessDev(t, cpuOnly(), newN5FakeDev(), nil)
	delete(h.sensors.sensors, "drivetemp:max")
	h.c.chans[2].sensor = nil // New resolved it before the delete
	if got := h.c.Channels(); strings.Join(got, ",") != "cpu,ssd,hdd" {
		t.Fatalf("channels: %v", got)
	}
	h.cycles(3)
	h.expectDuty("hdd", 140)
	h.expectMode("hdd", ModeSensor)
	h.expectDuty("cpu", 85)
	h.expectMode("cpu", ModeAuto)
	// ssd was added from the n5pro-balanced set: 44 C on [[35,74],[55,160],…] = 113
	h.expectDuty("ssd", 113)
	h.expectMode("ssd", ModeAuto)
	if s := h.c.Snapshot(); s.Status != "ok" {
		t.Errorf("status %q", s.Status)
	}
	if h.alerts.count("sensor") != 1 || !strings.Contains(h.alerts.msgs[len(h.alerts.msgs)-1], `channel "hdd": sensor drivetemp:max unresolved`) {
		t.Errorf("alerts: %v\n%v", h.alerts.kinds, h.alerts.msgs)
	}
	if n := h.log.count("sensor \"drivetemp:max\" for channel \"hdd\""); n != 1 {
		t.Errorf("resolve failure logged %d times (want once)", n)
	}
	// regulation on the healthy channels is unaffected
	h.sensors.get("k10temp").set(60000)
	h.cycles(1)
	h.expectDuty("cpu", 125)
	h.expectDuty("hdd", 140)

	// every sensor gone -> global sensor-error, each channel at its safe duty
	h.sensors.get("k10temp").fail(errors.New("cpu gone"))
	h.sensors.get("nvme:max").fail(errors.New("nvme gone"))
	h.cycles(1)
	if s := h.c.Snapshot(); s.Status != "sensor-error" {
		t.Errorf("status %q, want sensor-error when all channels fail", s.Status)
	}
	h.expectDuty("cpu", 255)
	h.expectDuty("ssd", 255)
	h.expectDuty("hdd", 140)
	h.expectMode("cpu", ModeSensor)
	if h.alerts.count("sensor") != 1 { // hdd's alert is still within the cooldown
		t.Errorf("alerts: %v", h.alerts.msgs)
	}
	// the sensor appears later (module loaded): resolved and regulated
	h.sensors.get("k10temp").set(36000)
	h.sensors.get("nvme:max").set(44000)
	h.sensors.add("drivetemp:max", 33000)
	h.cycles(1)
	if s := h.c.Snapshot(); s.Status != "ok" {
		t.Errorf("status after recovery %q", s.Status)
	}
	h.expectMode("hdd", ModeAuto)
	h.expectDuty("hdd", 125) // slew from 140 towards 105: -15
}

// All sensors failing at once: global "sensor-error", every channel at its
// own safe duty, one alert saying so.
func TestSensorErrorAllChannels(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	for _, id := range []string{"k10temp", "nvme:max", "drivetemp:max"} {
		h.sensors.get(id).fail(errors.New(id + " gone"))
	}
	h.cycles(1)
	if s := h.c.Snapshot(); s.Status != "sensor-error" {
		t.Errorf("status %q", s.Status)
	}
	h.expectDuty("cpu", 255)
	h.expectDuty("ssd", 255)
	h.expectDuty("hdd", 140)
	for _, n := range []string{"cpu", "ssd", "hdd"} {
		h.expectMode(n, ModeSensor)
	}
	if h.alerts.count("sensor") != 1 {
		t.Fatalf("alerts: %v", h.alerts.kinds)
	}
	if msg := h.alerts.msgs[0]; !strings.Contains(msg, "ALL channels affected") ||
		!strings.Contains(msg, `channel "cpu"`) || !strings.Contains(msg, `channel "hdd"`) {
		t.Errorf("alert: %q", msg)
	}
	h.cycles(3)
	if s := h.c.Snapshot(); s.Status != "sensor-error" || h.alerts.count("sensor") != 1 {
		t.Errorf("persisting: status %q alerts %d", s.Status, h.alerts.count("sensor"))
	}
	// one sensor back: status ok again, that channel regulates
	h.sensors.get("k10temp").set(36000)
	h.cycles(1)
	if s := h.c.Snapshot(); s.Status != "ok" {
		t.Errorf("status with one healthy channel %q", s.Status)
	}
	h.expectDuty("cpu", 240)
	h.expectMode("cpu", ModeAuto)
	h.expectMode("ssd", ModeSensor)
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
	h.expectMode("ssd", ModeAuto) // isolated to the frozen channel
	h.expectDuty("ssd", 74)
	if h.alerts.count("sensor") != 1 || !h.log.contains("frozen") {
		t.Errorf("stale must alert as sensor error")
	}
	if !strings.Contains(h.alerts.msgs[0], `channel "cpu": sensor k10temp unchanged for`) {
		t.Errorf("alert: %q", h.alerts.msgs[0])
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
	h.expectDuty("cpu", 255)
	h.expectMode("ssd", ModeAuto) // not affected
	h.expectDuty("ssd", 74)
	h.sensors.add("later", 50000)
	h.cycles(1)
	h.expectMode("cpu", ModeAuto)
	if s := h.c.Snapshot(); s.Status != "ok" {
		t.Errorf("status %q", s.Status)
	}
}

// LastCycle is the liveness signal for serve's watchdog pings: zero before
// the first cycle, then the clock at the end of the latest one.
func TestLastCycle(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	if !h.c.LastCycle().IsZero() {
		t.Fatalf("LastCycle before any cycle: %v", h.c.LastCycle())
	}
	h.cycles(1)
	if got := h.c.LastCycle(); !got.Equal(time.Unix(1_789_500_000, 0)) {
		t.Errorf("after first cycle: %v", got)
	}
	h.cycles(2) // clock advanced 10 s after each cycle
	if got := h.c.LastCycle(); !got.Equal(time.Unix(1_789_500_020, 0)) {
		t.Errorf("after third cycle: %v", got)
	}
	if h.c.Interval() != 10*time.Second {
		t.Errorf("Interval(): %s", h.c.Interval())
	}
	// a cycle in sensor-error still counts as alive (the loop runs)
	h.sensors.get("k10temp").fail(errors.New("x"))
	h.cycles(1)
	if got := h.c.LastCycle(); !got.Equal(time.Unix(1_789_500_030, 0)) {
		t.Errorf("after sensor-error cycle: %v", got)
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
	h.expectDuty("cpu", -1) // duty unknown after a failed write
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
	// the duty was unknown (-1) after the failures: the target is written
	// directly (no slew from an unknown value), like on the first cycle
	if h.state("cpu").Duty != 158 || h.dev.getDuty(1) != 158 {
		t.Errorf("cpu after recovery: %+v device=%d", h.state("cpu"), h.dev.getDuty(1))
	}
}

func TestHistoryRing(t *testing.T) {
	cfg := n5cfg()
	cfg.Daemon.Interval = 120 * time.Second // capacity 60
	cfg.Daemon.StaleCycles = 600            // constant fake sensors must not trip stale detection
	h := newHarness(t, cfg, nil)
	if h.c.hist.RawCap() != 60 {
		t.Fatalf("capacity %d", h.c.hist.RawCap())
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
	if got := history.RawCapacity(10 * time.Second); got != 720 {
		t.Errorf("capacity at 10s: %d", got)
	}
	// HistoryRange: the raw tier by span, since strict
	last := all[59].TS
	if got := h.c.HistoryRange(time.Hour, last-120); len(got) != 1 || got[0].TS != last {
		t.Errorf("HistoryRange(1h, since): %+v", got)
	}
}

// TestHistoryMeanSkipsUnknownDuty: a channel whose duty is unknown after
// a failed write carries the -1 marker in the raw point (the dashboard
// shows it as unknown; safety_test pins it), but the 1-minute means are
// formed from the written duties only — a -1 would pull them below every
// duty ever written.
func TestHistoryMeanSkipsUnknownDuty(t *testing.T) {
	cfg := n5cfg()
	cfg.Daemon.StaleCycles = 600
	h := newHarness(t, cfg, nil)
	h.cycles(1)
	h.expectDuty("cpu", 85)
	h.sensors.get("k10temp").set(60000) // a new target, so the next cycle writes
	h.dev.setFailWrite(1, true)
	h.cycles(1) // first failure: cpu unknown, no failsafe yet
	h.expectDuty("cpu", -1)
	raw := h.c.History(time.Hour)
	if len(raw) != 2 {
		t.Fatalf("raw points: %d", len(raw))
	}
	if raw[1].Duty["cpu"] != -1 {
		t.Errorf("raw point must carry the unknown marker: %+v", raw[1].Duty)
	}
	if raw[1].Duty["ssd"] != 74 || raw[0].Duty["cpu"] != 85 {
		t.Errorf("known duties missing: %+v %+v", raw[0].Duty, raw[1].Duty)
	}
	h.dev.setFailWrite(1, false)
	h.cycles(4) // recovery: the target (158 at 60 °C) is written directly
	h.expectDuty("cpu", 158)
	// six cycles (10 s each) sit in one 1-minute bucket: the mean of cpu is
	// (85 + 4 × 158) / 5 = 143, not (85 − 1 + 4 × 158) / 6 = 119
	means := h.c.HistoryRange(3*time.Hour, 0)
	if len(means) == 0 {
		t.Fatal("no 1-min points")
	}
	last := means[len(means)-1]
	if d, ok := last.Duty["cpu"]; !ok || d != 143 {
		t.Errorf("1-min mean of cpu = %d (%v), want 143 (the known duties only)", d, ok)
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
	h.sensors.get("k10temp").set(60000)
	h.cycles(7)
	if n := h.dev.countCalls("write:") + h.dev.countCalls("manual:"); n != 0 {
		t.Errorf("dry-run wrote hardware: %v", h.dev.calls)
	}
	if s := h.c.Snapshot(); s.Status != "dry-run" || !s.DryRun {
		t.Errorf("status %q", s.Status)
	}
	// integrator note 2: duty is what the chip does (85), target is computed
	h.expectDuty("cpu", 85)
	if h.state("cpu").Target != 158 {
		t.Errorf("dry-run target: %+v", h.state("cpu"))
	}
	h.dev.setDuty(1, 120) // someone else (EC) moved the fan
	h.cycles(1)
	h.expectDuty("cpu", 120)
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
	if h.alerts.count(AlertConfigChannels) != 1 || h.alerts.count("config") != 0 {
		t.Errorf("channel-set alert kinds: %v", h.alerts.kinds)
	}
	if !strings.Contains(h.alerts.msgs[0], `channel "x" uses pwm7`) || !h.log.contains(`config: channel "x" uses pwm7`) {
		t.Errorf("dropped channel not reported: %v\n%s", h.alerts.msgs, strings.Join(h.log.lines, "\n"))
	}
}

// The controller's channel-set corrections use their own alert kind
// ("config-channels"): serve stamps "config" for parse warnings first, and
// a shared kind would swallow the sanitizer's content through the cooldown.
// Sanitizer notes and dropped channels travel in one alert.
func TestConfigChannelsAlertKind(t *testing.T) {
	cfg := cpuOnly()
	cfg.Channels = append(cfg.Channels, config.Channel{Name: "x", PWM: 7, Sensor: "k10temp", Curve: config.DefaultCurve(), Critical: 90, Stop: "auto"})
	// serve stamped "config" a moment ago (parse warnings alert)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "alert.config"), []byte("1789500000\n"), 0o644)
	h := newHarnessDev(t, cfg, newN5FakeDev(), func(o *Options) { o.RunDir = dir })
	if AlertConfigChannels == "config" {
		t.Fatal("alert kinds must differ")
	}
	if h.alerts.count(AlertConfigChannels) != 1 || h.alerts.count("config") != 0 {
		t.Fatalf("alerts: %v (config stamp must not suppress the channel-set alert)", h.alerts.kinds)
	}
	if _, err := os.Stat(filepath.Join(dir, "alert."+AlertConfigChannels)); err != nil {
		t.Errorf("own stamp file: %v", err)
	}
	msg := h.alerts.msgs[0]
	for _, want := range []string{"pwm2 (ssd) not in config", "pwm3 (hdd) not in config", `channel "x" uses pwm7`} {
		if !strings.Contains(msg, want) {
			t.Errorf("alert lacks %q: %q", want, msg)
		}
	}
	// reload of the same file: the sanitizer completes it again, same kind,
	// after the cooldown
	h.clock.advance(31 * time.Minute)
	if err := h.c.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	if h.alerts.count(AlertConfigChannels) != 2 || !strings.Contains(h.alerts.msgs[1], "config corrected on reload") {
		t.Errorf("reload alert: %v", h.alerts.msgs)
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
	if err := Failsafe(dev, cfg, nil); err != nil {
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

// TestSensorErrorLogOnce: a sensor that stays unresolved (the N5 Pro
// without HDDs) while the other temperatures move every cycle produces one
// summary line, not one per cycle; a change of the affected set produces
// a new line.
func TestSensorErrorLogOnce(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.sensors.get("drivetemp:max").fail(errors.New("no drives"))
	for i := 0; i < 30; i++ {
		h.sensors.get("k10temp").set(36000 + i*125) // k10temp moves every cycle
		h.cycles(1)
	}
	if n := h.log.count("sensor error -> "); n != 1 {
		t.Fatalf("summary lines = %d in 30 cycles, want 1:\n%s", n, strings.Join(h.log.lines, "\n"))
	}
	if n := h.log.count("sensor drivetemp:max (hdd): read: no drives"); n != 1 {
		t.Errorf("per-channel lines = %d, want 1", n)
	}
	// the summary names the failed channel and its class, no temperatures
	for _, l := range h.log.lines {
		if strings.Contains(l, "sensor error -> ") && (strings.Contains(l, "cpu=") || strings.Contains(l, "C ") || !strings.Contains(l, "hdd=read error")) {
			t.Errorf("summary carries live values or lacks the class: %q", l)
		}
	}
	// a second channel failing changes the set: one more line
	h.sensors.get("nvme:max").fail(errors.New("gone"))
	h.cycles(5)
	if n := h.log.count("sensor error -> "); n != 2 {
		t.Errorf("summary lines after a second failure = %d, want 2", n)
	}
}

// TestStaleLogOnce: a frozen k10temp logs one stale line per episode, not
// one per cycle with a growing duration.
func TestStaleLogOnce(t *testing.T) {
	cfg := n5cfg()
	cfg.Daemon.StaleCycles = 6
	h := newHarness(t, cfg, nil)
	h.cycles(30)
	if n := h.log.count("sensor k10temp (cpu): unchanged"); n != 1 {
		t.Fatalf("stale lines = %d in 30 cycles, want 1:\n%s", n, strings.Join(h.log.lines, "\n"))
	}
	if n := h.log.count("sensor error -> "); n != 1 {
		t.Errorf("summary lines = %d, want 1", n)
	}
	h.expectMode("cpu", ModeSensor)
}

// TestApplyNotesOnlyWhenApplied: a reload that needs a restart reports no
// "config corrected on reload" line or alert; an applied one does.
func TestApplyNotesOnlyWhenApplied(t *testing.T) {
	h := newHarnessDev(t, n5cfg(), newN5FakeDev(), nil)
	h.cycles(1)
	before := h.alerts.count(AlertConfigChannels)
	// a config without the hdd channel and with a new channel name: the
	// sanitizer adds hdd back (a note), the extra channel needs a restart
	cfg := n5cfg()
	cfg.Channels = append(cfg.Channels[:2], config.Channel{Name: "extra", PWM: 4, Sensor: "k10temp", Curve: config.DefaultCurve(), Critical: 90, Stop: "auto"})
	if err := h.c.Apply(cfg); !errors.Is(err, ErrRestartRequired) {
		t.Fatalf("Apply = %v, want ErrRestartRequired", err)
	}
	if h.log.contains("config corrected on reload") || h.alerts.count(AlertConfigChannels) != before {
		t.Fatalf("notes reported although nothing was applied:\n%s", strings.Join(h.log.lines, "\n"))
	}
	// same channel set, hdd with stop "auto": applied, note reported
	cfg = n5cfg()
	cfg.Channels[2].Stop = "auto"
	h.clock.advance(31 * time.Minute)
	if err := h.c.Apply(cfg); err != nil {
		t.Fatalf("Apply = %v", err)
	}
	if !h.log.contains("config corrected on reload") {
		t.Errorf("applied correction not logged")
	}
}

// TestSnapshotStatusConstants: the typed status values are the strings
// the dashboard keys on.
func TestSnapshotStatusConstants(t *testing.T) {
	want := map[Status]string{StatusStarting: "starting", StatusOK: "ok", StatusWriteError: "write-error", StatusSensorError: "sensor-error", StatusDryRun: "dry-run"}
	for s, str := range want {
		if string(s) != str {
			t.Errorf("%q != %q", s, str)
		}
	}
	h := newHarness(t, n5cfg(), func(o *Options) { o.DryRun = true })
	if s := h.c.Snapshot().Status; s != StatusStarting {
		t.Errorf("before the first cycle: %q", s)
	}
	h.cycles(1)
	if s := h.c.Snapshot().Status; s != StatusDryRun {
		t.Errorf("dry run: %q", s)
	}
}

func TestHistoryPersisted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.json")
	cfg := n5cfg()
	cfg.Daemon.StaleCycles = 600
	h := newHarness(t, cfg, func(o *Options) { o.HistoryFile = path })
	h.cycles(3)
	h.c.saveHistory()
	if _, err := os.Stat(path); err == nil {
		t.Fatalf("history saved before %s elapsed", historySaveEvery)
	}
	h.cycles(60) // 10 min at 10 s
	h.c.saveHistory()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("history not saved after %s: %v", historySaveEvery, err)
	}
	h.cycles(2)
	h.c.Stop()
	// a new controller on the same file starts with the persisted points
	h2 := newHarness(t, cfg, func(o *Options) { o.HistoryFile = path })
	if got := h2.c.History(24 * time.Hour); len(got) != 65 || got[64].Temp["cpu"] != 36 {
		t.Errorf("history after restart: %d points, last %+v", len(got), got[len(got)-1])
	}
	if got := h2.c.HistoryRange(24*time.Hour, 0); len(got) < 10 {
		t.Errorf("1-min tier after restart: %d points", len(got))
	}
}

// TestOverrideSurvivesReload: a config reload (a preset apply, an Apply
// from the dashboard) swaps the curves but leaves a manual override in
// place — the channel stays in manual at the held duty and the new curve
// drives it only after ClearOverride. The dashboard's switch relies on
// this: "override on, then Apply a preset → override stays".
func TestOverrideSurvivesReload(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	h.expectDuty("cpu", 85)
	if err := h.c.SetOverride("cpu", 200); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectDuty("cpu", 200)
	h.expectMode("cpu", ModeManual)

	cfg := n5cfg()
	cfg.Channels[0].Curve = [][2]int{{30, 100}, {80, 255}}
	if err := h.c.Reload(config.Marshal(cfg)); err != nil {
		t.Fatalf("reload: %v", err)
	}
	h.cycles(2)
	h.expectDuty("cpu", 200)
	h.expectMode("cpu", ModeManual)
	if o := h.c.Overrides(); o["cpu"] != 200 {
		t.Errorf("override dropped by the reload: %v", o)
	}
	if c := h.c.Config().Channels[0]; c.Curve[0][1] != 100 {
		t.Errorf("curve not swapped under the override: %+v", c.Curve)
	}
	// back to the curve: the new one, not the old
	if err := h.c.ClearOverride("cpu"); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectMode("cpu", ModeAuto)
	if st := h.state("cpu"); st.Target < 100 {
		t.Errorf("target %d after the clear: the new curve should drive it (≥ 100)", st.Target)
	}
}
