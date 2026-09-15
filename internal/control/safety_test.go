package control

// Regression tests for the safety review findings (H1, H2, M1..M4, L1..L4,
// integrator note 2 as far as they live in this package).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/ventula/internal/config"
)

// cpuOnly is a config that lost ssd and hdd (dropped by the parser or never
// written).
func cpuOnly() config.Config {
	cfg := config.Default()
	cfg.Channels = config.N5ProChannels()[:1]
	return cfg
}

// H1: on the N5 Pro the daemon manages pwm1..3 even when the config lacks
// them, and `ventula failsafe` (control.Failsafe) always puts pwm3 to 140.
func TestH1N5ProMissingChannelsAdded(t *testing.T) {
	h := newHarnessDev(t, cpuOnly(), newN5FakeDev(), nil)
	if got := h.c.Channels(); strings.Join(got, ",") != "cpu,ssd,hdd" {
		t.Fatalf("channels: %v", got)
	}
	if h.alerts.count("config") != 1 || !h.log.contains("pwm3 (hdd) not in config") || !h.log.contains("pwm2 (ssd) not in config") {
		t.Errorf("missing channels must warn + alert: alerts=%v\n%s", h.alerts.kinds, strings.Join(h.log.lines, "\n"))
	}
	cfgNow := h.c.Config()
	if hdd := cfgNow.Channel("hdd"); hdd == nil || hdd.Stop != "140" || hdd.Sensor != "drivetemp:max" {
		t.Errorf("added hdd: %+v", hdd)
	}
	h.cycles(1)
	h.expectDuty("hdd", 105)
	h.expectMode("hdd", ModeAuto)
	h.c.Stop()
	if h.dev.countCalls("stop:3=140") != 1 {
		t.Errorf("Stop must SafeStop pwm3 with 140: %v", h.dev.calls)
	}

	// Failsafe without a controller (ExecStopPost path)
	dev := newN5FakeDev()
	log := &testLogger{}
	if err := Failsafe(dev, cpuOnly(), log); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stop:1=auto", "stop:2=auto", "stop:3=140"} {
		if dev.countCalls(want) != 1 {
			t.Errorf("missing %s in %v", want, dev.calls)
		}
	}
	if !log.contains("pwm3 (hdd) not in config") {
		t.Errorf("Failsafe must log the correction")
	}
	// even with no channels at all
	dev = newN5FakeDev()
	if err := Failsafe(dev, config.Default(), nil); err != nil {
		t.Fatal(err)
	}
	if dev.countCalls("stop:3=140") != 1 || dev.countCalls("stop:") != 3 {
		t.Errorf("empty config: %v", dev.calls)
	}
	// name clash: config uses the name "hdd" on pwm1 -> default channel renamed
	cfg := cpuOnly()
	cfg.Channels[0].Name = "hdd"
	out, notes := SanitizeChannels("n5pro", cfg.Channels)
	if len(out) != 3 || out[2].Name != "pwm3" || out[2].PWM != 3 || len(notes) != 2 {
		t.Errorf("clash: %+v %v", out, notes)
	}
	// other profiles are untouched
	if out, notes := SanitizeChannels("nct67xx", cpuOnly().Channels); len(out) != 1 || notes != nil {
		t.Errorf("generic profile changed: %+v %v", out, notes)
	}
	// a fake profile with the same config gets no additions
	h2 := newHarness(t, cpuOnly(), nil)
	if got := h2.c.Channels(); len(got) != 1 || h2.alerts.count("config") != 0 {
		t.Errorf("non-n5pro: %v", got)
	}
}

// M1: stop="auto" on n5pro pwm3 is forced to 140 (start and reload).
func TestM1N5ProPwm3AutoForced(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[2].Stop = "auto"
	h := newHarnessDev(t, cfg, newN5FakeDev(), nil)
	if got := stopOf(h.c.Config(), "hdd"); got != "140" {
		t.Errorf("stop after New: %q", got)
	}
	if h.alerts.count("config") != 1 || !h.log.contains("pwm3) has stop=\"auto\"") {
		t.Errorf("must warn + alert: %v", h.log.lines)
	}
	h.cycles(1)
	// reload with auto again: accepted (same channel set), still 140
	if err := h.c.Reload(config.Marshal(cfg)); err != nil {
		t.Fatalf("reload: %v", err)
	}
	h.cycles(1)
	if got := stopOf(h.c.Config(), "hdd"); got != "140" {
		t.Errorf("stop after reload: %q", got)
	}
	h.c.Stop()
	if h.dev.countCalls("stop:3=140") != 1 || h.dev.countCalls("stop:3=auto") != 0 {
		t.Errorf("SafeStop calls: %v", h.dev.calls)
	}
	// on a non-n5pro device "auto" stays
	h2 := newHarness(t, cfg, nil)
	if got := stopOf(h2.c.Config(), "hdd"); got != "auto" {
		t.Errorf("fake profile stop: %q", got)
	}
}

// H2: a write that keeps failing with a stable target still reaches the
// failsafe: the failed channel's duty is unknown (-1), so it is written
// every cycle until it succeeds.
func TestH2PersistentWriteFailureStableTarget(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1) // n=0: initial write
	h.dev.setFailWrite(1, true)
	h.cycles(5) // n=1..5: stable, nothing written, no error yet
	if h.alerts.count("write") != 0 || h.c.wrErr != 0 {
		t.Fatalf("premature: wrErr=%d", h.c.wrErr)
	}
	h.cycles(1) // n=6: periodic rewrite fails
	h.expectDuty("cpu", -1)
	if s := h.c.Snapshot(); s.Status != "write-error" || h.c.wrErr != 1 {
		t.Errorf("after first failure: %q wrErr=%d", s.Status, h.c.wrErr)
	}
	h.cycles(1) // n=7: cur unknown -> written again -> fails -> failsafe
	if h.alerts.count("write") != 1 {
		t.Errorf("write alert: %d (wrErr=%d)", h.alerts.count("write"), h.c.wrErr)
	}
	h.expectDuty("ssd", 255)
	h.expectMode("ssd", ModeFailsafe)
	h.expectDuty("cpu", -1)
	// history carries the unknown marker too
	hist := h.c.History(time.Hour)
	if hist[len(hist)-1].Duty["cpu"] != -1 {
		t.Errorf("history duty: %v", hist[len(hist)-1].Duty)
	}
	// stays in failsafe while the write keeps failing, no alert spam
	h.cycles(4)
	if h.alerts.count("write") != 1 || h.c.wrErr < 6 {
		t.Errorf("alert %d wrErr %d", h.alerts.count("write"), h.c.wrErr)
	}
	// recovery: written directly, counter reset
	h.dev.setFailWrite(1, false)
	h.cycles(1)
	if h.c.wrErr != 0 || h.state("cpu").Duty != 85 || h.dev.getDuty(1) != 85 {
		t.Errorf("recovery: wrErr=%d %+v", h.c.wrErr, h.state("cpu"))
	}
}

// M2: when the failsafe itself cannot write any channel for 6 cycles the
// loop returns ErrDeviceLost (systemd restarts, ExecStopPost + re-detect).
func TestM2DeviceLostEndsRun(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	h.dev.setFailAll(true)
	h.sensors.get("k10temp").set(60000) // target moves → a write every cycle
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cycles := 0
	h.c.opts.Wait = func(ctx context.Context, d time.Duration) bool {
		cycles++
		h.clock.advance(d)
		if cycles > 40 {
			cancel()
		}
		return ctx.Err() == nil
	}
	err := h.c.Run(ctx)
	if !errors.Is(err, ErrDeviceLost) {
		t.Fatalf("Run: %v after %d cycles", err, cycles)
	}
	// n=1 failure counted, n=2 -> failsafe (all fail) = fsFail 1, n=7 -> fsFail 6
	// -> ErrDeviceLost; Wait ran after n=1..6
	if cycles != 6 {
		t.Errorf("exited after %d waits", cycles)
	}
	if !h.log.contains("device unreachable") || h.alerts.count("device") != 1 || h.alerts.count("write") != 1 {
		t.Errorf("log/alerts: %v\n%s", h.alerts.kinds, strings.Join(h.log.lines, "\n"))
	}
	// SafeStop was attempted anyway (and failed loudly)
	if h.dev.countCalls("stop:") != 3 || !h.log.contains("safe stop (140) failed") {
		t.Errorf("SafeStop: %v", h.dev.calls)
	}
	// a single channel failing is not "device lost": the loop keeps running
	h2 := newHarness(t, n5cfg(), nil)
	h2.cycles(1)
	h2.dev.setFailWrite(1, true)
	h2.cycles(20)
	if h2.c.fsFail != 0 {
		t.Errorf("one bad channel counted as device lost: %d", h2.c.fsFail)
	}
	// sensor-error failsafe path counts too
	h3 := newHarness(t, n5cfg(), nil)
	h3.cycles(1)
	h3.dev.setFailAll(true)
	h3.sensors.get("k10temp").fail(errors.New("gone"))
	for i := 0; i < 5; i++ {
		if err := h3.c.cycle(); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}
	if err := h3.c.cycle(); !errors.Is(err, ErrDeviceLost) {
		t.Errorf("6th failing failsafe: %v", err)
	}
}

// M3: the stale check only runs when the first channel's sensor is k10temp.
func TestM3StaleOnlyForK10temp(t *testing.T) {
	cfg := n5cfg()
	cfg.Daemon.StaleCycles = 6
	cfg.Channels = []config.Channel{cfg.Channels[1], cfg.Channels[0], cfg.Channels[2]} // ssd (nvme:max) first
	h := newHarness(t, cfg, nil)
	h.cycles(40) // constant whole-degree values are normal
	h.expectMode("ssd", ModeAuto)
	h.expectMode("cpu", ModeAuto)
	if h.alerts.count("sensor") != 0 || h.log.contains("frozen") {
		t.Errorf("false stale on nvme:max")
	}
	// k10temp first: still detected
	cfg2 := n5cfg()
	cfg2.Daemon.StaleCycles = 6
	h2 := newHarness(t, cfg2, nil)
	h2.cycles(7)
	h2.expectMode("cpu", ModeSensor)
	if !h2.log.contains("frozen") {
		t.Errorf("stale on k10temp not detected")
	}
	// reload that swaps the first sensor away from k10temp disables it
	cfg3 := n5cfg()
	cfg3.Daemon.StaleCycles = 6
	cfg3.Channels[0].Sensor = "nvme:max"
	if err := h2.c.Reload(config.Marshal(cfg3)); err != nil {
		t.Fatal(err)
	}
	h2.cycles(20)
	h2.expectMode("cpu", ModeAuto)
}

// M4: a panic in the loop still ends in SafeStop and Run returns an error.
func TestM4PanicInLoopStillSafeStops(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.sensors.get("k10temp").panicOnce = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := h.c.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "panic") || !strings.Contains(err.Error(), "fake sensor exploded") {
		t.Fatalf("Run: %v", err)
	}
	if !h.log.contains("PANIC in regulation loop") {
		t.Errorf("panic not logged")
	}
	for _, want := range []string{"stop:1=auto", "stop:2=auto", "stop:3=140"} {
		if h.dev.countCalls(want) != 1 {
			t.Errorf("missing %s in %v", want, h.dev.calls)
		}
	}
	// Stop must not need c.mu: hold it and call Stop
	h2 := newHarness(t, n5cfg(), nil)
	h2.c.mu.Lock()
	done := make(chan struct{})
	go func() { h2.c.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop blocked on the loop mutex")
	}
	h2.c.mu.Unlock()
}

// L1: Stop concurrent with a cycle -- after Stop the loop writes nothing.
func TestL1StopBlocksFurtherWrites(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	h.c.Stop()
	h.dev.resetCalls()
	h.sensors.get("k10temp").set(90000) // critical -> would write 255
	h.cycles(7)                         // includes a rewrite cycle
	if n := h.dev.countCalls("write:"); n != 0 {
		t.Errorf("writes after Stop: %v", h.dev.calls)
	}
	// sensor-error failsafe after Stop writes nothing either
	h.sensors.get("k10temp").fail(errors.New("x"))
	h.cycles(2)
	if n := h.dev.countCalls("write:"); n != 0 {
		t.Errorf("failsafe writes after Stop: %v", h.dev.calls)
	}
}

// L2: the initial duty comes from the device; stalls count only after our
// first successful write.
func TestL2InitialDutyAndStallGate(t *testing.T) {
	dev := newFakeDev()
	dev.setDuty(1, 200)
	dev.failRead[2] = true
	dev.setRPM(1, 0) // fan reported stopped from the start
	h := newHarnessDev(t, n5cfg(), dev, nil)
	if h.state("cpu").Duty != 200 || h.state("ssd").Duty != 255 || h.state("hdd").Duty != 105 {
		t.Errorf("initial duties: cpu=%d ssd=%d hdd=%d", h.state("cpu").Duty, h.state("ssd").Duty, h.state("hdd").Duty)
	}
	if !h.log.contains("pwm2 not readable at start") {
		t.Errorf("unreadable pwm not logged")
	}
	h.cycles(1) // n=0: stall check before our first write -> not counted
	if h.c.chans[0].stallCnt != 0 {
		t.Errorf("stall counted before first write: %d", h.c.chans[0].stallCnt)
	}
	h.cycles(1) // count 1
	h.expectMode("cpu", ModeAuto)
	h.cycles(1) // count 2 -> stall
	h.expectMode("cpu", ModeStall)
	h.expectDuty("cpu", 255)
	// dry-run never writes -> never counts stalls
	dev2 := newFakeDev()
	dev2.setRPM(1, 0)
	h2 := newHarnessDev(t, n5cfg(), dev2, func(o *Options) { o.DryRun = true })
	h2.cycles(5)
	h2.expectMode("cpu", ModeAuto)
}

// L3: a tach read error is logged once, not every cycle.
func TestL3TachErrorLoggedOnce(t *testing.T) {
	dev := newFakeDev()
	dev.failRPM[1] = true
	h := newHarnessDev(t, n5cfg(), dev, nil)
	h.cycles(5)
	if n := h.log.count("fan1_input unreadable"); n != 1 {
		t.Errorf("tach error lines: %d", n)
	}
	if h.state("cpu").RPM != -1 {
		t.Errorf("rpm: %d", h.state("cpu").RPM)
	}
	h.dev.failRPM[1] = false
	h.cycles(1)
	h.dev.failRPM[1] = true
	h.cycles(1)
	if n := h.log.count("fan1_input unreadable"); n != 2 {
		t.Errorf("tach error must be logged again after recovery: %d", n)
	}
}

// L4: the manual minimum applies to channels with a fixed stop duty or to
// n5pro pwm3, not to the sensor id.
func TestL4OverrideMinimumKey(t *testing.T) {
	cfg := n5cfg()
	cfg.Channels[0].Stop = "100"       // cpu: fixed stop -> minimum applies
	cfg.Channels[2].Stop = "auto"      // hdd on a generic profile with auto -> no minimum
	cfg.Channels[1].Sensor = "k10temp" // ssd: not a disk sensor, auto stop
	h := newHarness(t, cfg, nil)
	if err := h.c.SetOverride("cpu", 30); err == nil {
		t.Errorf("fixed-stop channel accepted low override")
	}
	if err := h.c.SetOverride("hdd", 30); err != nil {
		t.Errorf("drivetemp with auto stop on generic profile: %v", err)
	}
	if err := h.c.SetOverride("ssd", 0); err != nil {
		t.Errorf("plain channel: %v", err)
	}
	// n5pro pwm3: minimum regardless of config (stop is forced to 140 anyway)
	h2 := newHarnessDev(t, cfg, newN5FakeDev(), nil)
	if err := h2.c.SetOverride("hdd", 30); err == nil {
		t.Errorf("n5pro pwm3 accepted low override")
	}
	if err := h2.c.SetOverride("hdd", 60); err != nil {
		t.Errorf("n5pro pwm3 at minimum: %v", err)
	}
}

// Reviewer: an override on a stalled channel does not lower the duty.
func TestOverrideOnStalledChannelStays255(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	h.dev.setRPM(2, 0)
	h.cycles(2)
	h.expectMode("ssd", ModeStall)
	if err := h.c.SetOverride("ssd", 100); err != nil {
		t.Fatal(err)
	}
	h.cycles(3)
	h.expectDuty("ssd", 255)
	h.expectMode("ssd", ModeStall)
	if h.dev.getDuty(2) != 255 {
		t.Errorf("device duty %d", h.dev.getDuty(2))
	}
	// after recovery the override applies
	h.dev.setRPM(2, 1500)
	h.cycles(4)
	h.expectDuty("ssd", 100)
	h.expectMode("ssd", ModeManual)
}

func stopOf(cfg config.Config, name string) string {
	ch := cfg.Channel(name)
	if ch == nil {
		return "<missing>"
	}
	return ch.Stop
}
