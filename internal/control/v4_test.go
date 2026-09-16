package control

// Regression tests for the v0.4 audit fixes in this package (docs/AUDIT.md
// section 3): alert cooldown with a stamp from the future, one log line
// per sensor-error episode, stamp write errors logged once, Apply's notes
// only when applied, a panicking sink, and the journald prefix on alerts.

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/logfile"
)

// TestRaiseFutureStampExpired: a cooldown stamp that lies in the future
// (wall clock stepped back after it was written) does not silence the
// kind; the alert goes out, the log line appears and the stamp is
// rewritten with the current time.
func TestRaiseFutureStampExpired(t *testing.T) {
	dir := t.TempDir()
	future := time.Unix(1_789_500_000, 0).Add(2 * time.Hour).Unix()
	if err := os.WriteFile(filepath.Join(dir, "alert.temp"), []byte(strconv.FormatInt(future, 10)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, n5cfg(), func(o *Options) { o.RunDir = dir })
	h.cycles(1)
	h.sensors.get("k10temp").set(95000) // above critical 88
	h.cycles(1)
	h.expectMode("cpu", ModeCritical)
	if h.alerts.count("temp") != 1 {
		t.Fatalf("temp alerts = %d, want 1 (stamp from the future must not suppress)", h.alerts.count("temp"))
	}
	if !h.log.contains("ALERT[temp]") {
		t.Fatal("no ALERT[temp] log line")
	}
	b, err := os.ReadFile(filepath.Join(dir, "alert.temp"))
	if err != nil {
		t.Fatal(err)
	}
	// the alert went out in the cycle before the clock advanced by one interval
	if want := h.clock.now().Add(-h.c.interval()).Unix(); want >= future {
		t.Fatalf("test clock %d is not before the stamp %d", want, future)
	} else if got, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); got != want {
		t.Errorf("stamp = %d, want %d (rewritten)", got, want)
	}
	// the normal cooldown still holds afterwards
	h.cycles(3)
	if h.alerts.count("temp") != 1 {
		t.Errorf("temp alerts after cooldown period = %d", h.alerts.count("temp"))
	}
	h.clock.advance(31 * time.Minute)
	h.cycles(1)
	if h.alerts.count("temp") != 2 {
		t.Errorf("temp alerts after the cooldown = %d, want 2", h.alerts.count("temp"))
	}
}

// TestRaiseInMemoryCooldownIsTimeDifference: the in-memory cooldown is the
// difference of the Options.Now readings (monotonic with the real clock,
// so a wall clock step in production changes nothing); a fake clock that
// steps back reads as "expired", never as "silenced until the wall clock
// catches up" — the failure mode the stamp file had.
func TestRaiseInMemoryCooldownIsTimeDifference(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	h.sensors.get("k10temp").set(95000)
	h.cycles(1)
	if h.alerts.count("temp") != 1 {
		t.Fatalf("temp alerts = %d", h.alerts.count("temp"))
	}
	h.cycles(3)
	if h.alerts.count("temp") != 1 {
		t.Errorf("alert repeated inside the cooldown: %d", h.alerts.count("temp"))
	}
	h.clock.advance(-3 * time.Hour) // a stepped-back fake clock: expired, not silenced
	h.cycles(1)
	if h.alerts.count("temp") != 2 {
		t.Errorf("alert silenced after a backwards step: %d", h.alerts.count("temp"))
	}
	h.cycles(2)
	if h.alerts.count("temp") != 2 {
		t.Errorf("alert repeated inside the new cooldown: %d", h.alerts.count("temp"))
	}
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

// TestAlertStampWriteErrorLoggedOnce: an unwritable stamp is logged once
// and the alert still goes out.
func TestAlertStampWriteErrorLoggedOnce(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, n5cfg(), func(o *Options) { o.RunDir = dir })
	h.cycles(1)
	// the stamp path is a directory now: WriteFile fails
	if err := os.Mkdir(filepath.Join(dir, "alert.temp"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.sensors.get("k10temp").set(95000)
	h.cycles(1)
	h.clock.advance(31 * time.Minute)
	h.cycles(1)
	if h.alerts.count("temp") != 2 {
		t.Fatalf("temp alerts = %d, want 2", h.alerts.count("temp"))
	}
	if n := h.log.count("alert stamp "); n != 1 {
		t.Errorf("stamp error lines = %d, want 1:\n%s", n, strings.Join(h.log.lines, "\n"))
	}
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

// panicSink panics on delivery (a sink that breaks the "never panic" rule).
type panicSink struct{ n int }

func (p *panicSink) Alert(string, string) {
	p.n++
	panic("sink exploded")
}

// TestAlertSinkPanicRecovered: a panicking sink costs the alert, not the
// loop (asynchronous delivery path).
func TestAlertSinkPanicRecovered(t *testing.T) {
	sink := &panicSink{}
	h := newHarness(t, n5cfg(), func(o *Options) { o.SyncAlerts = false })
	h.c.alert = sink
	h.cycles(1)
	h.sensors.get("k10temp").set(95000)
	h.cycles(1)
	deadline := time.Now().Add(5 * time.Second)
	for !h.log.contains("alert sink panicked") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !h.log.contains("alert sink panicked delivering temp") {
		t.Fatalf("panic not recovered/logged:\n%s", strings.Join(h.log.lines, "\n"))
	}
	h.cycles(1) // the loop is still alive
	h.expectMode("cpu", ModeCritical)
}

// TestAlertLinesCarryPriorityPrefix: ALERT lines start with the journald
// error prefix; the file writer strips it.
func TestAlertLinesCarryPriorityPrefix(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.cycles(1)
	h.sensors.get("k10temp").set(95000)
	h.cycles(1)
	found := false
	for _, l := range h.log.lines {
		if strings.HasPrefix(l, logfile.PrefixErr+"ALERT[temp]") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no prefixed ALERT line:\n%s", strings.Join(h.log.lines, "\n"))
	}
	if got := string(logfile.StripPrefix([]byte("<3>ALERT[x]: y"))); got != "ALERT[x]: y" {
		t.Errorf("StripPrefix = %q", got)
	}
	if got := string(logfile.StripPrefix([]byte("<x>not a prefix"))); got != "<x>not a prefix" {
		t.Errorf("StripPrefix altered %q", got)
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
