//go:build unix

// The lock is a no-op without flock (filelock_other.go), so the shared-file
// tests only hold on unix.

package alert

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Two rings on one file stand for the daemon and a CLI run without the
// daemon socket (apt hook, onfailure `alert`). Issue #54: the second writer
// overwrote the first and both handed out the same ID (measured on the
// reference host 2026-11-21 with 0.6.0).
func TestRingSharedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	daemon := NewRing(&Log{Logger: &recLogger{}}, path, nil)
	daemon.Alert("restart", "daemon one")

	cli := NewRing(&Log{Logger: &recLogger{}}, path, nil)
	cli.Alert("gate54", "from the CLI")

	// the daemon sees the CLI record without writing itself
	if got := daemon.Recent(0); len(got) != 2 || got[0].Kind != "gate54" {
		t.Fatalf("daemon view after the CLI alert: %+v", got)
	}
	if n := daemon.Open(); n != 2 {
		t.Fatalf("daemon Open = %d, want 2", n)
	}

	daemon.Alert("test", "daemon two")
	recs := readFile(t, path)
	kinds := map[string]uint64{}
	seen := map[uint64]bool{}
	for _, r := range recs {
		if seen[r.ID] {
			t.Fatalf("duplicate id %d in %+v", r.ID, recs)
		}
		seen[r.ID] = true
		kinds[r.Kind] = r.ID
	}
	if len(recs) != 3 || kinds["restart"] != 1 || kinds["gate54"] != 2 || kinds["test"] != 3 {
		t.Fatalf("file after the daemon's second alert: %+v", recs)
	}

	// the dashboard acknowledges the CLI record through the daemon's ring
	if acked, unknown := daemon.Ack([]uint64{2}, "user:admin"); acked != 1 || unknown != nil {
		t.Fatalf("ack of the CLI record: %d %v", acked, unknown)
	}
	// a later CLI write keeps that acknowledgement
	cli2 := NewRing(&Log{Logger: &recLogger{}}, path, nil)
	cli.Alert("kernel", "stale CLI ring writes again")
	for _, r := range readFile(t, path) {
		if r.ID == 2 && r.AckBy != "user:admin" {
			t.Fatalf("acknowledgement lost: %+v", r)
		}
	}
	if got := cli2.Recent(0); len(got) != 4 || got[0].Kind != "kernel" || got[0].ID != 4 {
		t.Fatalf("fresh CLI ring view: %+v", got)
	}
}

// Records from two rings at the same time and acknowledgements from a third
// all survive (the read-modify-write is serialized by the file lock).
func TestRingSharedFileConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	a := NewRing(&Log{Logger: &recLogger{}}, path, nil)
	b := NewRing(&Log{Logger: &recLogger{}}, path, nil)
	acker := NewRing(&Log{Logger: &recLogger{}}, path, nil)
	const each = 20 // 2*each <= RingSize: nothing is evicted
	var wg sync.WaitGroup
	for _, r := range []*Ring{a, b} {
		wg.Add(1)
		go func(r *Ring) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				r.Alert("stall", fmt.Sprintf("%p %d", r, i))
			}
		}(r)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < each; i++ {
			acker.AckUpTo(2*each, "user:admin")
		}
	}()
	wg.Wait()
	final := acker.AckUpTo(2*each, "user:admin") // whatever arrived after its last pass
	recs := readFile(t, path)
	if len(recs) != 2*each {
		t.Fatalf("%d records in the file, want %d", len(recs), 2*each)
	}
	seen := map[uint64]bool{}
	for _, r := range recs {
		if seen[r.ID] || r.ID == 0 || r.ID > 2*each {
			t.Fatalf("id %d duplicate or out of range", r.ID)
		}
		seen[r.ID] = true
		if r.AckBy != "user:admin" {
			t.Fatalf("acknowledgement lost on %d (final pass acked %d): %+v", r.ID, final, r)
		}
	}
	if n := a.Open() + b.Open(); n != 0 {
		t.Fatalf("open after the acks: %d", n)
	}
}

// Eviction across two rings: the newest RingSize stay, IDs keep counting,
// nothing evicted comes back.
func TestRingSharedFileEviction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	a := NewRing(&Log{Logger: &recLogger{}}, path, nil)
	b := NewRing(&Log{Logger: &recLogger{}}, path, nil)
	for i := 0; i < RingSize+10; i++ {
		r := a
		if i%2 == 1 {
			r = b
		}
		r.Alert("temp", fmt.Sprint(i))
	}
	recs := readFile(t, path)
	if len(recs) != RingSize || recs[0].ID != 11 || recs[len(recs)-1].ID != RingSize+10 {
		t.Fatalf("after eviction: %d records, ids %d..%d", len(recs), recs[0].ID, recs[len(recs)-1].ID)
	}
	if got := a.Recent(0); len(got) != RingSize || got[len(got)-1].ID != 11 {
		t.Fatalf("ring a after eviction: %d, oldest %d", len(got), got[len(got)-1].ID)
	}
}

// A conflicting or id-less file record older than everything a full ring
// keeps is dropped, not renumbered as the newest (it would come back on
// every merge while the file cannot be rewritten).
func TestMergeRecordsDropsOldRenumbered(t *testing.T) {
	var mem []Record
	for i := 0; i < RingSize; i++ {
		mem = append(mem, Record{ID: uint64(i + 100), TS: int64(1000 + i), Kind: "temp"})
	}
	file := []Record{{TS: 1, Kind: "restart", Msg: "legacy"}, {ID: 100, TS: 2, Kind: "gate54"}}
	out, last := mergeRecords(mem, file, 0)
	if len(out) != RingSize || last != 149 {
		t.Fatalf("old records were renumbered: %d records, last %d", len(out), last)
	}
	// a newer one is kept with a new id
	out, last = mergeRecords(mem, []Record{{ID: 100, TS: 5000, Kind: "gate54"}}, 0)
	if len(out) != RingSize+1 || last != 150 || out[len(out)-1].Kind != "gate54" {
		t.Fatalf("newer conflicting record: %d records, last %d", len(out), last)
	}
}

// A memory record and a file record with the same ID but other content
// (left behind by the #54 overwrite): the memory one keeps the ID, the file
// one is renumbered after the highest, nothing is lost.
func TestRingSharedFileConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	r := NewRing(&Log{Logger: &recLogger{}}, path, nil)
	r.Alert("test", "memory")
	other := []Record{{ID: 1, TS: 1, Kind: "gate54", Msg: "file"}}
	b, _ := json.Marshal(other)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	r.Alert("restart", "next")
	got := map[string]uint64{}
	for _, rec := range readFile(t, path) {
		got[rec.Kind] = rec.ID
	}
	if len(got) != 3 || got["test"] != 1 || got["gate54"] == 1 || got["gate54"] == 0 || got["restart"] == got["gate54"] {
		t.Fatalf("after the conflict: %v", got)
	}
}

func readFile(t *testing.T, path string) []Record {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var recs []Record
	if err := json.Unmarshal(b, &recs); err != nil {
		t.Fatal(err)
	}
	return recs
}
