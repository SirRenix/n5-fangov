//go:build unix

package control

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/events"
)

// A blocked event log (another process holds events.json.lock, every
// write waits the 2 s lock bound) does not slow the cycle: the controller
// records through the queue serve wires.
func TestEventsBlockedLogDoesNotSlowCycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	lf, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	q := events.NewQueue(events.New(path, nil), nil)
	h := newHarness(t, n5cfg(), func(o *Options) { o.Events = q })
	cpu := h.sensors.get("k10temp")
	h.cycles(1)
	start := time.Now()
	for i := 0; i < 6; i++ { // critical entered and left three times: six events
		if i%2 == 0 {
			cpu.set(95000)
		} else {
			cpu.set(50000)
		}
		h.cycles(1)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("six cycles took %s with the event log blocked", d)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("the writer did not wait for the held lock: the test proves nothing")
	}
	_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	q.Close(30 * time.Second)
	b, err := os.ReadFile(path) // the file itself: the fake clock lies outside Since's window
	if err != nil {
		t.Fatal(err)
	}
	var evs []events.Event
	if err := json.Unmarshal(b, &evs); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if e.Kind == events.Critical || e.Kind == events.CriticalEnd {
			n++
		}
	}
	if n != 6 {
		t.Errorf("%d critical events in the file after the lock was released, want 6", n)
	}
}
