package alert

// Regression tests for the v0.4 audit fixes: a delivery failure is visible
// in the ring record, and the exec error stays unwrappable.

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
