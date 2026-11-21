//go:build unix

// The lock is a no-op without flock (internal/ringfile), so the
// shared-file tests only hold on unix.

package events

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// The daemon's log and a CLI run (ExecStopPost failsafe, onfailure alert)
// on one file: the second writer neither overwrites the first nor reuses
// its IDs, and the daemon sees the CLI event without writing itself (#54,
// the same mechanism as alerts.json).
func TestLogSharedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	daemon := New(path, nil)
	daemon.Add(Event{Kind: Start, Text: "started", By: ByDaemon})
	cli := New(path, nil)
	cli.Add(Event{Kind: Failsafe, Text: "safe state after daemon end (success)", By: ByCLI})
	got := daemon.Since(0, 0)
	if len(got) != 2 || got[1].Kind != Failsafe || got[1].ID != 2 {
		t.Fatalf("daemon view: %+v", got)
	}
	daemon.Add(Event{Kind: FailsafeEnd, Text: "ended (daemon started)", By: ByDaemon})
	f := readFile(t, path)
	seen := map[uint64]bool{}
	for _, e := range f {
		if seen[e.ID] {
			t.Fatalf("duplicate id %d: %+v", e.ID, f)
		}
		seen[e.ID] = true
	}
	if len(f) != 3 || f[2].ID != 3 || len(daemon.Open()) != 0 {
		t.Fatalf("file: %+v", f)
	}
	// a stale CLI log writes again: nothing of the daemon's is lost
	cli.Add(Event{Kind: Alert, Text: "alert restart", By: ByCLI})
	if f := readFile(t, path); len(f) != 4 || f[3].ID != 4 {
		t.Fatalf("after the stale writer: %+v", f)
	}
}

// Writers at the same time: every event survives with a unique ID.
func TestLogSharedFileConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	a, b := New(path, nil), New(path, nil)
	const each = 40
	var wg sync.WaitGroup
	for _, l := range []*Log{a, b} {
		wg.Add(1)
		go func(l *Log) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				l.Add(Event{Kind: Alert, Text: fmt.Sprintf("%p %d", l, i)})
			}
		}(l)
	}
	wg.Wait()
	f := readFile(t, path)
	if len(f) != 2*each {
		t.Fatalf("%d events in the file, want %d", len(f), 2*each)
	}
	seen := map[uint64]bool{}
	for _, e := range f {
		if seen[e.ID] || e.ID == 0 || e.ID > 2*each {
			t.Fatalf("id %d duplicate or out of range", e.ID)
		}
		seen[e.ID] = true
	}
	if n := len(New(path, nil).Since(0, 0)); n != 2*each {
		t.Errorf("a fresh reader sees %d", n)
	}
}
