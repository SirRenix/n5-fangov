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
