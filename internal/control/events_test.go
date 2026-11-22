// events_test.go covers the controller's side of the event log: start,
// critical and ceiling entered and left, the write-error failsafe and the
// alerts the cooldown suppressed — each recorded once, on the edge, never
// per cycle.
package control

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/events"
	"github.com/SirRenix/n5-fangov/internal/version"
)

// recEvents is the events.Recorder of the tests.
type recEvents struct {
	mu  sync.Mutex
	evs []events.Event
}

func (r *recEvents) Add(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evs = append(r.evs, e)
}

// kinds lists "kind" or "kind:ch" in order.
func (r *recEvents) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.evs {
		k := e.Kind
		if e.Ch != "" {
			k += ":" + e.Ch
		}
		out = append(out, k)
	}
	return out
}

func (r *recEvents) last() events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.evs[len(r.evs)-1]
}

func eventHarness(t *testing.T, cfg config.Config) (*harness, *recEvents) {
	t.Helper()
	rec := &recEvents{}
	h := newHarness(t, cfg, func(o *Options) { o.Events = rec })
	return h, rec
}

func wantKinds(t *testing.T, rec *recEvents, want ...string) {
	t.Helper()
	if got := strings.Join(rec.kinds(), " "); got != strings.Join(want, " ") {
		t.Fatalf("events %q, want %q", got, strings.Join(want, " "))
	}
}

// Run records one start event with the version, before the first cycle.
func TestEventStart(t *testing.T) {
	h, rec := eventHarness(t, n5cfg())
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // one cycle, then the wait ends the loop
	_ = h.c.Run(ctx)
	wantKinds(t, rec, "start")
	if e := rec.last(); e.Text != "started "+version.Version || e.By != events.ByDaemon || e.TS != 1_789_500_000 {
		t.Errorf("start event %+v", e)
	}
}

func TestEventCriticalEnterLeave(t *testing.T) {
	h, rec := eventHarness(t, n5cfg())
	cpu := h.sensors.get("k10temp")
	h.cycles(2)
	wantKinds(t, rec)
	cpu.set(95000) // above critical 88
	h.cycles(1)
	wantKinds(t, rec, "critical:cpu")
	if e := rec.last(); e.By != events.ByDaemon || !strings.Contains(e.Text, "cpu critical 95.0C") || e.TS != time.Unix(1_789_500_000, 0).Add(2*h.c.interval()).Unix() {
		t.Errorf("critical event %+v", e)
	}
	// more critical cycles: no second critical event; the temp alert raised
	// again in every one of them is suppressed and marked once
	h.cycles(4)
	wantKinds(t, rec, "critical:cpu", "alert")
	// a failed read inside the state neither ends nor repeats it
	cpu.fail(errors.New("sensor gone"))
	h.cycles(2)
	wantKinds(t, rec, "critical:cpu", "alert")
	cpu.set(60000)
	h.cycles(3)
	wantKinds(t, rec, "critical:cpu", "alert", "critical-end:cpu")
	if n := h.alerts.count("temp"); n != 1 {
		t.Errorf("temp alerts %d", n)
	}
}

func TestEventCeilingEnterLeave(t *testing.T) {
	h, rec := eventHarness(t, hddCfg())
	hdd := h.sensors.get("drivetemp:max")
	h.cycles(1)
	hdd.set(66000) // above the ceiling (65), below critical (70)
	h.cycles(3)
	wantKinds(t, rec, "ceiling:hdd")
	if e := rec.last(); !strings.Contains(e.Text, "hdd ceiling 65C") || strings.Contains(e.Text, "/") {
		t.Errorf("ceiling text %q", e.Text)
	}
	hdd.set(63000) // inside the hysteresis: still in
	h.cycles(2)
	wantKinds(t, rec, "ceiling:hdd")
	hdd.set(50000)
	h.cycles(3)
	wantKinds(t, rec, "ceiling:hdd", "ceiling-end:hdd")
}

func TestEventFailsafeEnterLeave(t *testing.T) {
	h, rec := eventHarness(t, n5cfg())
	h.cycles(1)
	h.dev.setFailWrite(1, true)
	h.sensors.get("k10temp").set(70000) // a new target: the loop writes pwm1
	h.cycles(2)
	wantKinds(t, rec, "failsafe")
	if e := rec.last(); e.Ch != "" || !strings.Contains(e.Text, "write error") {
		t.Errorf("failsafe event %+v", e)
	}
	// the failsafe runs again every cycle: no second event (the repeated
	// write alert is suppressed and marked once)
	h.cycles(3)
	wantKinds(t, rec, "failsafe", "alert")
	h.dev.setFailWrite(1, false)
	h.cycles(3)
	wantKinds(t, rec, "failsafe", "alert", "failsafe-end")
}

// One marker per cooldown window for a suppressed alert; a delivery opens
// the next window.
func TestEventSuppressedAlert(t *testing.T) {
	h, rec := eventHarness(t, n5cfg())
	h.cycles(1)
	h.c.raise("stall", "one")   // delivered: the ring marks it, not the controller
	h.c.raise("stall", "two")   // suppressed: marked
	h.c.raise("stall", "three") // suppressed again: not marked a second time
	h.c.raise("sensor", "x")    // another kind: delivered
	wantKinds(t, rec, "alert")
	if e := rec.last(); e.Text != "alert stall (suppressed by the cooldown)" || e.By != events.ByDaemon {
		t.Errorf("suppressed marker %+v", e)
	}
	h.clock.advance(31 * time.Minute)
	h.c.raise("stall", "four") // delivered after the window
	h.c.raise("stall", "five") // suppressed in the new window: marked once more
	wantKinds(t, rec, "alert", "alert")
	if h.alerts.count("stall") != 2 {
		t.Errorf("stall deliveries %d", h.alerts.count("stall"))
	}
}

// A changed sensor resets the ceiling state on reload: the span ends.
func TestEventCeilingEndOnSensorChange(t *testing.T) {
	h, rec := eventHarness(t, hddCfg())
	h.cycles(1)
	h.sensors.get("drivetemp:max").set(66000)
	h.cycles(1)
	cfg := hddCfg()
	cfg.Channels[2].Sensor = "nvme:max"
	if err := h.c.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	h.cycles(1)
	wantKinds(t, rec, "ceiling:hdd", "ceiling-end:hdd")
}

// The loop's own stop ends the critical and ceiling spans: the downtime
// after it is not drawn as a critical band.
func TestEventSpansEndAtStop(t *testing.T) {
	h, rec := eventHarness(t, hddCfg())
	// cpu critical, hdd at its ceiling
	h.sensors.get("k10temp").set(95000)
	h.sensors.get("drivetemp:max").set(66000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // one cycle, then the loop leaves
	_ = h.c.Run(ctx)
	wantKinds(t, rec, "start", "critical:cpu", "ceiling:hdd", "critical-end:cpu", "ceiling-end:hdd")
	if e := rec.last(); !strings.Contains(e.Text, "daemon stopped") {
		t.Errorf("stop end %+v", e)
	}
}
