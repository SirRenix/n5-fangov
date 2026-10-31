package control

import (
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/logfile"
)

// G4 (audit 2026-10-16): with debug_regs=1 a register that differs from the
// written duty is no write error: no failsafe, no "write" alert, status ok,
// and one warning per channel per DCRWarnInterval with the count of the
// suppressed ones. The register is compared one settle time after the
// write, in the next write phase (maintenance window 1).
func TestDCRDeviationIsAWarningNotAWriteError(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.dev.mu.Lock()
	h.dev.dcrDiff = map[int]bool{1: true, 2: true, 3: true}
	h.dev.regSettle = 5 * time.Second
	h.dev.mu.Unlock()
	h.sensors.get("k10temp").set(60000)
	// 13 cycles of 10 s: writes at cycle 0, 6, 12 (rewriteEvery), compared
	// at cycle 1, 7, 13
	for i := 0; i < 14; i++ {
		h.c.cycle()
		h.clock.advance(10 * time.Second)
	}
	if s := h.c.Snapshot(); s.Status != "ok" {
		t.Fatalf("status %q", s.Status)
	}
	if h.alerts.count("write") != 0 || h.alerts.count("device") != 0 {
		t.Fatalf("alerts %v", h.alerts)
	}
	h.expectMode("hdd", ModeAuto)
	if h.state("cpu").Duty == 255 || h.state("cpu").Duty < 0 {
		t.Fatalf("cpu duty %d after DCR deviations (failsafe or unknown)", h.state("cpu").Duty)
	}
	for _, name := range []string{"cpu", "ssd", "hdd"} {
		if n := h.log.count(logfile.PrefixWarning + "EC register deviation: " + name + ":"); n != 1 {
			t.Fatalf("%s: %d warning lines within the interval, want 1\n%v", name, n, h.log.lines)
		}
	}
	h.clock.advance(DCRWarnInterval)
	for i := 0; i < 7; i++ { // up to the next rewrite and its comparison
		h.c.cycle()
		h.clock.advance(10 * time.Second)
	}
	if h.log.count("more since") == 0 {
		t.Fatalf("suppressed count not reported after the interval: %v", h.log.lines)
	}
	// Stop: the stop write with a deviating register is not "safe stop failed"
	h.c.Stop()
	if h.log.contains("safe stop") && h.log.contains("failed") {
		t.Fatalf("stop logged a failure: %v", h.log.lines)
	}
}

// Maintenance window 1 (C6, Z1, Z2): the EC shows a written duty only after
// a delay (HDD ramp about 4.6 s). A register that reaches the value within
// its settle time is no deviation: no warning in the first cycle or later,
// and the register is never read before the settle time has passed.
func TestDCRComparedAfterSettleNoFalseAlarm(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.dev.mu.Lock()
	h.dev.regSettle = 5 * time.Second
	h.dev.dcrLag = 4600 * time.Millisecond
	h.dev.regNow = h.clock.now
	h.dev.mu.Unlock()
	h.sensors.get("k10temp").set(60000)

	// short steps: the cycle 1 s after the first write must not compare yet
	h.c.cycle()
	if v := h.dev.verifies(); len(v) != 0 {
		t.Fatalf("register read in the write cycle itself: %v", v)
	}
	h.clock.advance(time.Second)
	h.c.cycle()
	if v := h.dev.verifies(); len(v) != 0 {
		t.Fatalf("register read 1 s after the write (settle 5 s): %v", v)
	}
	for i := 0; i < 12; i++ {
		h.clock.advance(10 * time.Second)
		h.c.cycle()
	}
	if len(h.dev.verifies()) == 0 {
		t.Fatal("register never compared")
	}
	if h.log.contains("EC register deviation") {
		t.Fatalf("false alarm for a register within its settle time:\n%s", strings.Join(h.log.lines, "\n"))
	}
	// a register that stays off is still reported, once per channel
	h.dev.mu.Lock()
	h.dev.dcrDiff = map[int]bool{3: true}
	h.dev.mu.Unlock()
	for i := 0; i < 7; i++ {
		h.clock.advance(10 * time.Second)
		h.c.cycle()
	}
	if n := h.log.count(logfile.PrefixWarning + "EC register deviation: hdd:"); n != 1 {
		t.Fatalf("hdd deviation warnings %d, want 1\n%s", n, strings.Join(h.log.lines, "\n"))
	}
}

// The failsafe command (ExecStopPost) writes every channel first and
// compares the fixed stop duties afterwards, each with its settle time:
// a ramping HDD register (E3: "DCR3 reads 106 after writing 140") is no
// deviation; one that never arrives is logged, not a failure.
func TestFailsafeComparesRegistersAfterSettle(t *testing.T) {
	now := time.Date(2026, 10, 17, 13, 46, 0, 0, time.UTC)
	oldNow, oldSleep := registerNow, registerSleep
	t.Cleanup(func() { registerNow, registerSleep = oldNow, oldSleep })
	registerNow = func() time.Time { return now }
	registerSleep = func(d time.Duration) { now = now.Add(d) }

	dev := newN5FakeDev()
	dev.regSettle = 5 * time.Second
	dev.dcrLag = 4600 * time.Millisecond
	dev.regNow = func() time.Time { return now }
	cfg := n5cfg()
	log := &testLogger{}
	if err := Failsafe(dev, cfg, log); err != nil {
		t.Fatal(err)
	}
	if log.contains("EC register differs") {
		t.Fatalf("ramping register reported:\n%s", strings.Join(log.lines, "\n"))
	}
	// writes before any register read
	calls := dev.countCalls("stop:")
	if calls == 0 || len(dev.verifies()) == 0 {
		t.Fatalf("stops %d, verifies %v", calls, dev.verifies())
	}
	var hdd config.Channel
	for _, c := range cfg.Channels {
		if c.PWM == 3 {
			hdd = c
		}
	}
	if _, fixed := hdd.StopDuty(); !fixed {
		t.Fatalf("n5cfg hdd stop %q is no fixed duty", hdd.Stop)
	}
	dev.dcrDiff = map[int]bool{3: true}
	log = &testLogger{}
	start := now
	if err := Failsafe(dev, cfg, log); err != nil {
		t.Fatalf("a deviating register is no failsafe failure: %v", err)
	}
	if !log.contains("EC register differs after 5s") || now.Sub(start) < 5*time.Second {
		t.Fatalf("stuck register: waited %s\n%s", now.Sub(start), strings.Join(log.lines, "\n"))
	}
}
