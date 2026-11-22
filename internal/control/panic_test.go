// panic_test.go covers the full cooling (panic.go): 255 at once and
// without the ramp, the immediate cycle, expiry and early stop, the overlay
// (what was set before or during it applies afterwards), precedence,
// restart continuation, refusal and the events.
package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func panicHarness(t *testing.T, mod func(*Options)) (*harness, *recEvents) {
	t.Helper()
	rec := &recEvents{}
	h := newHarness(t, n5cfg(), func(o *Options) {
		o.Events = rec
		if mod != nil {
			mod(o)
		}
	})
	return h, rec
}

func (h *harness) expectAll(duty int, mode Mode) {
	h.t.Helper()
	for _, n := range []string{"cpu", "ssd", "hdd"} {
		h.expectDuty(n, duty)
		h.expectMode(n, mode)
	}
}

// 255 in the first cycle after the start, without the step_up ramp; the
// curve takes over again with step_down after 10 minutes.
func TestPanicImmediateAndExpiry(t *testing.T) {
	h, rec := panicHarness(t, nil)
	h.cycles(2)
	h.expectDuty("cpu", 85)
	until, err := h.c.Panic("user:admin")
	if err != nil || until != h.clock.now().Add(PanicDuration) {
		t.Fatalf("Panic: %v %v", until, err)
	}
	if s := h.c.Snapshot(); s.Panic == nil || s.Panic.By != "user:admin" || s.Panic.Until != until.Unix() {
		t.Fatalf("snapshot before the next cycle: %+v", s.Panic)
	}
	h.cycles(1)
	h.expectAll(255, ModePanic)
	if !strings.Contains(h.c.statusLine(h.c.chans), "pwm1=255(panic)") {
		t.Errorf("status line %q", h.c.statusLine(h.c.chans))
	}
	h.cycles(5)
	h.expectAll(255, ModePanic)
	h.clock.advance(PanicDuration)
	h.cycles(1)
	h.expectMode("cpu", ModeAuto)
	if d := h.c.Config().Daemon.StepDown; h.state("cpu").Duty != 255-d {
		t.Errorf("cpu after the end: %d, want 255-%d (step_down)", h.state("cpu").Duty, d)
	}
	if h.c.Snapshot().Panic != nil {
		t.Error("panic still in the snapshot")
	}
	wantKinds(t, rec, "panic", "panic-end")
	if e := rec.last(); !strings.Contains(e.Text, "10 min passed") || e.By != "daemon" {
		t.Errorf("end event %+v", e)
	}
	if !h.log.contains("full cooling ended") {
		t.Error("no end log line")
	}
}

// The start wakes the loop: 255 is written before the next regular cycle
// (the interval here is an hour).
func TestPanicImmediateCycle(t *testing.T) {
	h := newHarness(t, n5cfg(), func(o *Options) {
		o.Now = time.Now
		o.Wait = func(ctx context.Context, d time.Duration) bool {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(time.Hour):
				return true
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = h.c.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(3 * time.Second)
	for h.c.LastCycle().IsZero() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.dev.getDuty(1) == 255 {
		t.Fatal("cpu at 255 before the start")
	}
	if _, err := h.c.Panic("cli"); err != nil {
		t.Fatal(err)
	}
	for h.dev.getDuty(1) != 255 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if h.dev.getDuty(1) != 255 || h.dev.getDuty(3) != 255 {
		t.Fatalf("no immediate cycle: pwm1=%d pwm3=%d", h.dev.getDuty(1), h.dev.getDuty(3))
	}
}

// A start during the full cooling sets 10 min from then (no adding up);
// starts within 5 s are one (one event, one log line); stop ends early.
func TestPanicExtendDedupeStop(t *testing.T) {
	h, rec := panicHarness(t, nil)
	h.cycles(1)
	t0 := h.clock.now()
	for i := 0; i < 5; i++ {
		_, _ = h.c.Panic("user:admin")
		h.clock.advance(time.Second)
	}
	wantKinds(t, rec, "panic")
	if n := h.log.count("full cooling: every channel at 255"); n != 1 {
		t.Errorf("%d start lines", n)
	}
	h.clock.advance(6 * time.Minute)
	until, _ := h.c.Panic("token:ha")
	if want := t0.Add(5*time.Second + 6*time.Minute + PanicDuration); until != want {
		t.Errorf("until %v, want %v", until, want)
	}
	wantKinds(t, rec, "panic", "panic-extend")
	h.cycles(1)
	h.expectAll(255, ModePanic)
	if !h.c.PanicStop("user:admin") || h.c.PanicStop("user:admin") {
		t.Fatal("PanicStop: first true, second false")
	}
	h.cycles(1)
	h.expectMode("cpu", ModeAuto)
	wantKinds(t, rec, "panic", "panic-extend", "panic-end")
	if e := rec.last(); e.By != "user:admin" || !strings.Contains(e.Text, "stopped (admin)") {
		t.Errorf("stop event %+v", e)
	}
	// a later expiry adds no second end
	h.clock.advance(PanicDuration)
	h.cycles(1)
	wantKinds(t, rec, "panic", "panic-extend", "panic-end")
}

// An override set before the full cooling is back afterwards; an
// override, a cleared one and a new config set during it apply after it.
func TestPanicOverlayKeepsAndDefers(t *testing.T) {
	h, _ := panicHarness(t, nil)
	h.cycles(1)
	if err := h.c.SetOverride("cpu", 150); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	h.expectDuty("cpu", 150)
	_, _ = h.c.Panic("user:admin")
	h.cycles(1)
	h.expectAll(255, ModePanic)
	// during: ssd override, a new hdd curve (a preset / schedule switch)
	if err := h.c.SetOverride("ssd", 120); err != nil {
		t.Fatal(err)
	}
	cfg := n5cfg()
	cfg.Channels[2].Curve = [][2]int{{20, 200}, {46, 255}}
	if err := h.c.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	h.cycles(2)
	h.expectAll(255, ModePanic)
	h.c.PanicStop("user:admin")
	h.cycles(1)
	h.expectDuty("cpu", 150)
	h.expectMode("cpu", ModeManual)
	h.expectDuty("ssd", 120)
	h.expectMode("ssd", ModeManual)
	if s := h.state("hdd"); s.Mode != ModeAuto || s.Target != Interpolate(cfg.Channels[2].Curve, 33000) {
		t.Errorf("hdd after the end: %+v", s)
	}
	// an override cleared during it: the curve afterwards
	_, _ = h.c.Panic("user:admin")
	_ = h.c.ClearOverride("cpu")
	h.cycles(1)
	h.expectDuty("cpu", 255)
	h.c.PanicStop("user:admin")
	h.cycles(1)
	h.expectMode("cpu", ModeAuto)
}

// An assist switch during the full cooling applies after it.
func TestPanicDefersAssist(t *testing.T) {
	h := newAssistHarness(t, assistCfg(false, 0, 0))
	h.sensors.get(gpuID).set(60000) // full boost once switched on
	h.cycles(1)
	_, _ = h.c.Panic("user:admin")
	if err := h.c.SetAssistEnabled("hdd", true); err != nil {
		t.Fatal(err)
	}
	h.cycles(2)
	h.expectDuty("hdd", 255)
	h.expectMode("hdd", ModePanic)
	h.c.PanicStop("user:admin")
	h.cycles(1)
	h.expectBoost("hdd", 45, gpuID)
	if s := h.state("hdd"); s.Target != 105+45 {
		t.Errorf("hdd target after the end: %+v", s)
	}
}

// Sensor error, critical and the ceiling stay at 255 with their mode;
// the write-error failsafe goes first.
func TestPanicPrecedence(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[1].Critical = 90 // ssd: the ceiling (85) acts before critical
	h := newHarness(t, cfg, nil)
	h.cycles(1)
	_, _ = h.c.Panic("user:admin")
	// cpu critical, ssd at its ceiling, hdd sensor error (safe duty 140)
	h.sensors.get("k10temp").set(95000)
	h.sensors.get("nvme:max").set(86000)
	h.sensors.get("drivetemp:max").fail(errors.New("gone"))
	h.cycles(1)
	h.expectDuty("cpu", 255)
	h.expectMode("cpu", ModeCritical)
	h.expectDuty("ssd", 255)
	h.expectMode("ssd", ModeCritical)
	h.expectDuty("hdd", 255) // not the safe duty 140
	h.expectMode("hdd", ModeSensor)
	// write errors: the failsafe goes first (255 is rewritten every 6 cycles)
	h.dev.setFailWrite(2, true)
	h.cycles(8)
	h.expectMode("cpu", ModeFailsafe)
}

// panic.until carries a running full cooling over a daemon restart; an
// expired or broken file means none, with a log line; a dry run computes
// it but writes no file.
func TestPanicRestart(t *testing.T) {
	dir := t.TempDir()
	h, _ := panicHarness(t, func(o *Options) { o.RunDir = dir })
	h.cycles(1)
	until, _ := h.c.Panic("user:admin")
	b, err := os.ReadFile(filepath.Join(dir, panicFile))
	if err != nil || string(b) != strconv.FormatInt(until.Unix(), 10)+"\nuser:admin\n" {
		t.Fatalf("panic.until: %q %v", b, err)
	}
	h2 := newHarness(t, n5cfg(), func(o *Options) { o.RunDir = dir; o.Now = h.clock.now }) // restarted at the same moment
	if u, by, ok := h2.c.PanicState(); !ok || u.Unix() != until.Unix() || by != "user:admin" {
		t.Fatalf("not continued: %v %q %v", u, by, ok)
	}
	h2.cycles(1)
	h2.expectAll(255, ModePanic)
	// expired
	h3 := newHarness(t, n5cfg(), func(o *Options) {
		o.RunDir = dir
		now := time.Unix(1_789_500_000, 0).Add(PanicDuration + time.Minute)
		o.Now = func() time.Time { return now }
	})
	if _, _, ok := h3.c.PanicState(); ok || !h3.log.contains("while the daemon was down") {
		t.Errorf("expired file: %v %v", ok, h3.log.lines)
	}
	if _, err := os.Stat(filepath.Join(dir, panicFile)); err == nil {
		t.Error("expired file kept")
	}
	// broken
	if err := os.WriteFile(filepath.Join(dir, panicFile), []byte("soon\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h4 := newHarness(t, n5cfg(), func(o *Options) { o.RunDir = dir })
	if _, _, ok := h4.c.PanicState(); ok || !h4.log.contains("panic.until invalid") {
		t.Errorf("broken file: %v %v", ok, h4.log.lines)
	}
	// dry run: computed, no file
	dry := t.TempDir()
	h5 := newHarness(t, n5cfg(), func(o *Options) { o.RunDir = dry; o.DryRun = true })
	if _, err := h5.c.Panic("cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dry, panicFile)); err == nil {
		t.Error("dry run wrote panic.until")
	}
	h5.cycles(1)
	h5.expectMode("cpu", ModePanic)
}

// Refused where nothing can be driven: profile monitor, a read-only driver.
func TestPanicRefused(t *testing.T) {
	dev := newFakeDev()
	dev.prof = fakeProfile{name: "monitor"}
	h := newHarnessDev(t, n5cfg(), dev, nil)
	if _, err := h.c.Panic("cli"); !errors.Is(err, ErrPanicRefused) || !strings.Contains(err.Error(), "profile monitor") {
		t.Errorf("monitor: %v", err)
	}
	if h.c.Snapshot().PanicRefused == "" {
		t.Error("snapshot lacks the refusal")
	}
	h2 := newHarness(t, n5cfg(), func(o *Options) { o.PanicRefusal = func() string { return "fan driver write-disabled" } })
	if _, err := h2.c.Panic("cli"); !errors.Is(err, ErrPanicRefused) || !strings.Contains(err.Error(), "write-disabled") {
		t.Errorf("read-only: %v", err)
	}
	if _, _, ok := h2.c.PanicState(); ok {
		t.Error("refused start runs")
	}
}

func drainKick(c *Controller) int {
	n := 0
	for {
		select {
		case <-c.kick:
			n++
		default:
			return n
		}
	}
}

// Only a real start wakes the loop, at most once per interval; a repeat
// within 5 s, an extension and a stop do not.
func TestPanicKicksOnlyARealStart(t *testing.T) {
	h, _ := panicHarness(t, nil)
	h.cycles(2)
	drainKick(h.c)
	_, _ = h.c.Panic("user:admin")
	if n := drainKick(h.c); n != 1 {
		t.Fatalf("start: %d kicks", n)
	}
	_, _ = h.c.Panic("user:admin") // within 5 s
	h.clock.advance(6 * time.Minute)
	_, _ = h.c.Panic("user:admin") // extension
	h.c.PanicStop("user:admin")
	if n := drainKick(h.c); n != 0 {
		t.Fatalf("repeat, extension, stop: %d kicks", n)
	}
	_, _ = h.c.Panic("user:admin") // a new start, 6 min after the last kick
	if n := drainKick(h.c); n != 1 {
		t.Fatalf("second start: %d kicks", n)
	}
	h.c.PanicStop("user:admin")
	_, _ = h.c.Panic("user:admin") // stop and start again within one interval: no second kick
	if n := drainKick(h.c); n != 0 {
		t.Fatalf("start within an interval of the last kick: %d kicks", n)
	}
}

// Cycles that come early (woken by a start) regulate, but do not advance
// the stall, stale or emergency counters: repeated starts cannot raise a
// false stall alert on a fan still spinning up, nor hurry the emergency
// hook.
func TestPanicEarlyCyclesDoNotCount(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[2].Critical = 70 // hdd: the ceiling (65) acts first
	h := newHarness(t, cfg, nil)
	h.cycles(2)
	h.dev.setRPM(1, 0) // cpu still spinning up
	h.sensors.get("drivetemp:max").set(66000)
	_, _ = h.c.Panic("user:admin")
	h.c.cycle() // one interval after the last: counts
	cpu, hdd := h.c.chans[0], h.c.chans[2]
	stall, ceil, same := cpu.stallCnt, hdd.ceilCycles, h.c.sameRaw
	for i := 0; i < 5; i++ { // woken again and again without time passing
		h.c.cycle()
	}
	if cpu.stallCnt != stall || hdd.ceilCycles != ceil || h.c.sameRaw != same {
		t.Fatalf("early cycles counted: stall %d→%d, ceiling %d→%d, stale %d→%d", stall, cpu.stallCnt, ceil, hdd.ceilCycles, same, h.c.sameRaw)
	}
	if h.alerts.count("stall") != 0 {
		t.Fatal("stall alert from early cycles")
	}
	h.expectDuty("cpu", 255) // they still regulate and write
	h.clock.advance(h.c.interval())
	h.c.cycle()
	if cpu.stallCnt != stall+1 || hdd.ceilCycles != ceil+1 {
		t.Errorf("a regular cycle: stall %d, ceiling %d", cpu.stallCnt, hdd.ceilCycles)
	}
	if h.alerts.count("stall") != 1 {
		t.Errorf("stall after two regular cycles: %d", h.alerts.count("stall"))
	}
}

// The end in panic.until is wall time; restored, it is measured from now
// and never more than 10 min.
func TestPanicRestoreRebasedAndBounded(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(1_789_500_000, 0)
	if err := os.WriteFile(filepath.Join(dir, panicFile), []byte(strconv.FormatInt(now.Add(10*time.Minute+30*time.Second).Unix(), 10)+"\ncli\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, n5cfg(), func(o *Options) { o.RunDir = dir })
	if u, _, ok := h.c.PanicState(); !ok || u != now.Add(PanicDuration) {
		t.Fatalf("restored until %v %v, want %v", u, ok, now.Add(PanicDuration))
	}
}

// State and panic.until change together: whatever the interleaving of
// starts, stops and an expiry, the file exists exactly while a full
// cooling runs.
func TestPanicFileFollowsState(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, n5cfg(), func(o *Options) { o.RunDir = dir })
	h.cycles(1)
	file := filepath.Join(dir, panicFile)
	check := func(what string) {
		t.Helper()
		_, _, on := h.c.PanicState()
		_, err := os.Stat(file)
		if on != (err == nil) {
			t.Fatalf("%s: running %v, file %v", what, on, err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = h.c.Panic("user:a") }()
		go func() { defer wg.Done(); h.c.PanicStop("user:b") }()
	}
	wg.Wait()
	check("after concurrent starts and stops")
	// expiry against a new start: the start wins, its file stays
	_, _ = h.c.Panic("user:a")
	h.clock.advance(PanicDuration)
	_, _ = h.c.Panic("user:b") // expired, so a new start
	h.c.cycle()
	check("expiry checked after the new start")
	h.clock.advance(PanicDuration)
	h.c.cycle()
	check("after the expiry")
}

// No start between Stop and the end of the process: nothing would cool,
// and panic.until would carry it into the next start.
func TestPanicRefusedAfterStop(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, n5cfg(), func(o *Options) { o.RunDir = dir })
	h.cycles(1)
	h.c.Stop()
	if _, err := h.c.Panic("cli"); !errors.Is(err, ErrPanicRefused) {
		t.Fatalf("start after Stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, panicFile)); err == nil {
		t.Error("panic.until written after Stop")
	}
}

// The snapshot names a set override also while full cooling runs.
func TestPanicSnapshotKeepsOverride(t *testing.T) {
	h, _ := panicHarness(t, nil)
	h.cycles(1)
	_ = h.c.SetOverride("cpu", 150)
	_, _ = h.c.Panic("user:admin")
	h.cycles(1)
	if s := h.state("cpu"); s.Mode != ModePanic || s.Override == nil || *s.Override != 150 {
		t.Errorf("cpu %+v", s)
	}
	if s := h.state("ssd"); s.Override != nil {
		t.Errorf("ssd override %v", *s.Override)
	}
}
