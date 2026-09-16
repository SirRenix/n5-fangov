package control

import (
	"errors"
	"testing"
)

// cyclesChecked is harness.cycles with the cycle() error checked: any
// non-nil return ends the test (AUDIT 7: the harness discards it, so an
// unexpected ErrDeviceLost stayed invisible in every controller test but
// TestDeviceLostEndsRun).
func cyclesChecked(h *harness, n int) {
	h.t.Helper()
	for i := 0; i < n; i++ {
		if err := h.c.cycle(); err != nil {
			h.t.Fatalf("cycle %d returned %v", i, err)
		}
		h.clock.advance(h.c.interval())
	}
}

// TestCycleErrorContract: cycle() returns nil through normal regulation,
// sensor errors, overrides and a single failing channel; only the
// all-channel failsafe failing deviceLostCycles times in a row yields
// ErrDeviceLost, and one successful write resets that counter.
func TestCycleErrorContract(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	cyclesChecked(h, 30)
	if s := h.c.Snapshot(); s.Status != "ok" {
		t.Fatalf("status %q after 30 healthy cycles", s.Status)
	}
	// a sensor error isolates the channel, the loop keeps running (the
	// status stays "ok" while any channel still regulates)
	h.sensors.get("nvme:max").fail(errors.New("nvme gone"))
	cyclesChecked(h, 5)
	if s := h.c.Snapshot(); s.Status != "ok" {
		t.Errorf("status %q with one failing sensor", s.Status)
	}
	h.expectMode("ssd", ModeSensor)
	h.sensors.get("nvme:max").set(44000)
	// an override and a single failing pwm are handled inside the cycle
	if err := h.c.SetOverride("cpu", 200); err != nil {
		t.Fatal(err)
	}
	h.dev.setFailWrite(2, true)
	cyclesChecked(h, 5)
	h.dev.setFailWrite(2, false)
	if err := h.c.ClearOverride("cpu"); err != nil {
		t.Fatal(err)
	}
	cyclesChecked(h, 3)
	if s := h.c.Snapshot(); s.Status != "ok" {
		t.Errorf("status %q after recovery", s.Status)
	}

	// device gone: the first failing cycle is a write error, the second
	// runs the failsafe; from then on every cycle counts towards
	// deviceLostCycles. The error appears exactly once the counter is full.
	h.dev.setFailAll(true)
	h.sensors.get("k10temp").set(70000) // forces a write every cycle
	var got error
	var n int
	for n = 1; n <= 2+deviceLostCycles; n++ {
		if got = h.c.cycle(); got != nil {
			break
		}
		h.clock.advance(h.c.interval())
	}
	if !errors.Is(got, ErrDeviceLost) {
		t.Fatalf("no ErrDeviceLost within %d failing cycles (last err %v)", 2+deviceLostCycles, got)
	}
	if n < deviceLostCycles {
		t.Errorf("ErrDeviceLost after %d cycles, want at least %d", n, deviceLostCycles)
	}
	if h.alerts.count("device") != 1 {
		t.Errorf("device alert count %d", h.alerts.count("device"))
	}
	// the device comes back: a successful failsafe write resets the counter
	h.dev.setFailAll(false)
	cyclesChecked(h, 3)
}

// TestSlewBounds: step_up/step_down at their limits (1 and 255) move by
// exactly that much per cycle and never overshoot the target.
func TestSlewBounds(t *testing.T) {
	cases := []struct{ cur, tgt, up, down, want int }{
		{100, 200, 1, 1, 101},
		{100, 50, 1, 1, 99},
		{100, 200, 255, 255, 200},
		{0, 255, 255, 1, 255},
		{255, 0, 1, 255, 0},
		{0, 255, 1, 255, 1},
		{255, 0, 255, 1, 254},
		{100, 100, 1, 1, 100},
	}
	for _, c := range cases {
		if got := Slew(c.cur, c.tgt, c.up, c.down); got != c.want {
			t.Errorf("Slew(%d→%d, up %d, down %d) = %d, want %d", c.cur, c.tgt, c.up, c.down, got, c.want)
		}
	}
	// through the controller: step_up 1 → one duty unit per cycle
	cfg := n5cfg()
	cfg.Daemon.StepUp, cfg.Daemon.StepDown = 1, 1
	h := newHarness(t, cfg, nil)
	cyclesChecked(h, 1) // first cycle is direct (85 at 36 C)
	h.expectDuty("cpu", 85)
	h.sensors.get("k10temp").set(80000)
	cyclesChecked(h, 1)
	h.expectDuty("cpu", 86)
	cyclesChecked(h, 9)
	h.expectDuty("cpu", 95)
	h.sensors.get("k10temp").set(36000)
	cyclesChecked(h, 1)
	h.expectDuty("cpu", 94)
	// step 255: reaches the target in one cycle, both directions
	cfg = n5cfg()
	cfg.Daemon.StepUp, cfg.Daemon.StepDown = 255, 255
	h = newHarness(t, cfg, nil)
	cyclesChecked(h, 1)
	h.sensors.get("k10temp").set(80000)
	cyclesChecked(h, 1)
	h.expectDuty("cpu", 255)
	h.sensors.get("k10temp").set(36000)
	cyclesChecked(h, 1)
	h.expectDuty("cpu", 85)
}

// TestOverrideBounds: 0 and 255 are accepted (0 on a chip-regulated
// channel), -1 and 256 refused; the HDD-like channel refuses below
// MinHDDOverride and accepts exactly MinHDDOverride.
func TestOverrideBounds(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	cyclesChecked(h, 1)
	for _, d := range []int{0, 255} {
		if err := h.c.SetOverride("cpu", d); err != nil {
			t.Errorf("cpu override %d refused: %v", d, err)
		}
	}
	for _, d := range []int{-1, 256} {
		if err := h.c.SetOverride("cpu", d); err == nil {
			t.Errorf("cpu override %d accepted", d)
		}
	}
	if err := h.c.SetOverride("hdd", MinHDDOverride-1); err == nil {
		t.Errorf("hdd override %d accepted", MinHDDOverride-1)
	}
	if err := h.c.SetOverride("hdd", MinHDDOverride); err != nil {
		t.Errorf("hdd override %d refused: %v", MinHDDOverride, err)
	}
	if err := h.c.SetOverride("hdd", 255); err != nil {
		t.Errorf("hdd override 255 refused: %v", err)
	}
	cyclesChecked(h, 1)
	h.expectDuty("cpu", 255)
	h.expectDuty("hdd", 255)
	if err := h.c.SetOverride("cpu", 0); err != nil {
		t.Fatal(err)
	}
	cyclesChecked(h, 1)
	h.expectDuty("cpu", 0)
	h.expectMode("cpu", ModeManual)
}
