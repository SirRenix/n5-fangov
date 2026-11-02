package alert

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRing(t *testing.T) {
	l := &recLogger{}
	path := filepath.Join(t.TempDir(), "alerts.json")
	inner := &recLogger{}
	r := NewRing(&Log{Logger: inner}, path, l)
	clock := time.Unix(1_789_500_000, 0)
	r.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	if r.Name() != EffectiveLog || len(r.Recent(10)) != 0 {
		t.Errorf("fresh ring: %s %v", r.Name(), r.Recent(10))
	}
	r.Alert("stall", "one")
	if err := r.Send("test", "two"); err != nil {
		t.Errorf("send: %v", err)
	}
	got := r.Recent(10)
	if len(got) != 2 || got[0].Kind != "test" || got[1].Kind != "stall" || got[0].TS <= got[1].TS {
		t.Errorf("newest first: %+v", got)
	}
	if len(inner.lines) != 2 {
		t.Errorf("delivery through the wrapped sink: %v", inner.lines)
	}
	if last := r.Last(); last["test"] != got[0].TS || last["stall"] != got[1].TS {
		t.Errorf("Last: %v", last)
	}
	if n := len(r.Recent(1)); n != 1 {
		t.Errorf("Recent(1): %d", n)
	}
	// mirror file: 0600, reloaded by a new ring
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("mirror: %v %v", err, st)
		}
	}
	r2 := NewRing(&Log{Logger: &recLogger{}}, path, l)
	if again := r2.Recent(0); len(again) != 2 || again[0].Msg != "two" {
		t.Errorf("reload: %+v", again)
	}
	// overflow keeps the newest RingSize
	for i := 0; i < RingSize+5; i++ {
		r.Alert("temp", "n")
	}
	if all := r.Recent(0); len(all) != RingSize || all[len(all)-1].Kind != "temp" {
		t.Errorf("overflow: %d oldest=%s", len(all), all[len(all)-1].Kind)
	}
	// delivery error passes through; the record is kept anyway
	r3 := NewRing(&failSink{}, "", nil)
	if err := r3.Send("test", "x"); err == nil || len(r3.Recent(0)) != 1 {
		t.Errorf("failing sink: %v %v", err, r3.Recent(0))
	}
	// unwritable mirror: logged once, memory continues
	if runtime.GOOS != "windows" {
		r4 := NewRing(&Log{Logger: &recLogger{}}, filepath.Join(t.TempDir(), "missing", "alerts.json"), l)
		r4.Alert("a", "1")
		r4.Alert("a", "2")
		n := 0
		for _, ln := range l.lines {
			if strings.Contains(ln, "history kept in memory only") {
				n++
			}
		}
		if n != 1 || len(r4.Recent(0)) != 2 {
			t.Errorf("unwritable mirror: warned %d times, %d records", n, len(r4.Recent(0)))
		}
	}
}

// failingSink is a Sender whose delivery always fails.
type failingSink struct{ n int }

func (s *failingSink) Name() string { return "failing" }

func (s *failingSink) Alert(kind, msg string) {
	_ = s.Send(kind, msg)
}

func (s *failingSink) Send(kind, msg string) error {
	s.n++
	return errors.New("mail: exit status 127")
}

// TestRingRecordsDeliveryError: a record of a failed delivery carries the
// error text; a delivered one has none; Alert logs the failure.
func TestRingRecordsDeliveryError(t *testing.T) {
	l := &recLogger{}
	sink := &failingSink{}
	r := NewRing(sink, "", l)
	r.now = func() time.Time { return time.Unix(1_789_500_000, 0) }
	r.Alert("stall", "fan stopped")
	if err := r.Send("temp", "hot"); err == nil {
		t.Fatal("Send returned nil for a failing sink")
	}
	recs := r.Recent(0)
	if len(recs) != 2 || sink.n != 2 {
		t.Fatalf("records = %d, deliveries = %d", len(recs), sink.n)
	}
	for _, rec := range recs {
		if rec.Error != "mail: exit status 127" || rec.TS != 1_789_500_000 {
			t.Errorf("record %+v lacks the delivery error", rec)
		}
	}
	if len(l.lines) != 1 || l.lines[0] != "alert: mail: exit status 127" {
		t.Errorf("Alert log = %v", l.lines)
	}
	ok := NewRing(&Log{Logger: &recLogger{}}, "", nil)
	ok.Alert("stall", "fan stopped")
	if rec := ok.Recent(1)[0]; rec.Error != "" || rec.Kind != "stall" {
		t.Errorf("delivered record = %+v", rec)
	}
	none := NewRing(nil, "", nil)
	if err := none.SendCtx(context.Background(), "test", "x"); err == nil || none.Recent(1)[0].Error == "" {
		t.Errorf("no sink: err=%v rec=%+v", err, none.Recent(1))
	}
}

func TestRingIDsAndAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	r := NewRing(&Log{Logger: &recLogger{}}, path, nil)
	clock := time.Unix(1_789_500_000, 0)
	r.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	r.Alert("stall", "a")
	r.Alert("temp", "b")
	r.Alert("stall", "c")
	got := r.Recent(0)
	if got[0].ID != 3 || got[1].ID != 2 || got[2].ID != 1 {
		t.Fatalf("ids in arrival order: %+v", got)
	}
	if r.Open() != 3 {
		t.Errorf("open: %d", r.Open())
	}
	if n, unknown := r.Ack([]uint64{1, 99}, "user:admin"); n != 0 || len(unknown) != 1 || unknown[0] != 99 {
		t.Errorf("unknown id acknowledges nothing: %d %v", n, unknown)
	}
	if r.Open() != 3 {
		t.Errorf("open after a rejected ack: %d", r.Open())
	}
	if n, unknown := r.Ack([]uint64{1}, "user:admin"); n != 1 || unknown != nil {
		t.Errorf("ack 1: %d %v", n, unknown)
	}
	first := r.Recent(0)[2]
	if first.AckTS == 0 || first.AckBy != "user:admin" {
		t.Errorf("ack fields: %+v", first)
	}
	// a second acknowledgement keeps the first one
	if n, _ := r.Ack([]uint64{1}, "cli"); n != 0 || r.Recent(0)[2].AckBy != "user:admin" {
		t.Errorf("re-ack: %d %+v", n, r.Recent(0)[2])
	}
	// a new alert of an acknowledged kind is open
	r.Alert("stall", "d")
	if r.Recent(1)[0].AckTS != 0 || r.Recent(1)[0].ID != 4 {
		t.Errorf("new alert: %+v", r.Recent(1)[0])
	}
	// all up to the id the caller saw: the newer alert stays open
	if n := r.AckUpTo(3, "token:ha"); n != 2 {
		t.Errorf("AckUpTo(3): %d", n)
	}
	if r.Open() != 1 || r.Recent(1)[0].AckTS != 0 {
		t.Errorf("after AckUpTo: open %d, newest %+v", r.Open(), r.Recent(1)[0])
	}
	// state and ids survive a restart; numbering continues
	r2 := NewRing(&Log{Logger: &recLogger{}}, path, nil)
	if r2.Open() != 1 || r2.Recent(0)[3].AckBy != "user:admin" {
		t.Errorf("reload: open %d %+v", r2.Open(), r2.Recent(0))
	}
	r2.Alert("temp", "e")
	if id := r2.Recent(1)[0].ID; id != 5 {
		t.Errorf("id after reload: %d", id)
	}
}

func TestRingLoadsFileWithoutIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alerts.json")
	old := `[{"ts":1789500001,"kind":"restart","msg":"a"},{"ts":1789500002,"kind":"driver","msg":"b","error":"x"}]`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRing(&Log{Logger: &recLogger{}}, path, nil)
	got := r.Recent(0)
	if len(got) != 2 || got[0].ID != 2 || got[0].Kind != "driver" || got[1].ID != 1 || r.Open() != 2 {
		t.Fatalf("legacy file numbered in file order, all open: %+v", got)
	}
	r.Alert("stall", "c")
	if id := r.Recent(1)[0].ID; id != 3 {
		t.Errorf("next id: %d", id)
	}
}

func TestRingAckEvictedID(t *testing.T) {
	r := NewRing(&Log{Logger: &recLogger{}}, "", nil)
	for i := 0; i < RingSize+1; i++ {
		r.Alert("temp", "n")
	}
	if _, unknown := r.Ack([]uint64{1}, "cli"); len(unknown) != 1 {
		t.Errorf("evicted id must be unknown: %v", unknown)
	}
}
