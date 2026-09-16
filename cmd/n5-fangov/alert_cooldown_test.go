package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type recSink struct{ kinds []string }

func (r *recSink) Alert(kind, _ string) { r.kinds = append(r.kinds, kind) }
func (r *recSink) Name() string         { return "rec" }

// Integrator note 6: start-up alerts from serve go through a stamp-file
// cooldown so a restart loop does not spam notifications.
func TestSendAlertCooled(t *testing.T) {
	dir := t.TempDir()
	s := &recSink{}
	sendAlertCooled(dir, s, "config", "first\nsecond line")
	sendAlertCooled(dir, s, "config", "again")
	sendAlertCooled(dir, s, "profile", "other kind")
	if strings.Join(s.kinds, ",") != "config,profile" {
		t.Errorf("delivered: %v", s.kinds)
	}
	b, err := os.ReadFile(filepath.Join(dir, "alert.config"))
	if err != nil {
		t.Fatal(err)
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)) > time.Minute {
		t.Errorf("stamp: %q %v", b, err)
	}
	// stamp older than the cooldown: delivered again
	old := time.Now().Add(-startAlertCooldown - time.Minute).Unix()
	_ = os.WriteFile(filepath.Join(dir, "alert.config"), []byte(strconv.FormatInt(old, 10)+"\n"), 0o644)
	sendAlertCooled(dir, s, "config", "after cooldown")
	if len(s.kinds) != 3 {
		t.Errorf("not delivered after cooldown: %v", s.kinds)
	}
	// garbage stamp: delivered
	_ = os.WriteFile(filepath.Join(dir, "alert.start"), []byte("junk"), 0o644)
	sendAlertCooled(dir, s, "start", "x")
	if len(s.kinds) != 4 {
		t.Errorf("garbage stamp suppressed the alert: %v", s.kinds)
	}
	// no run dir: always delivered
	sendAlertCooled("", s, "config", "x")
	sendAlertCooled("", s, "config", "x")
	if len(s.kinds) != 6 {
		t.Errorf("no-dir delivery: %v", s.kinds)
	}
}

// blockSink parks every delivery until released and counts them.
type blockSink struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockSink) Alert(kind, _ string) { b.entered <- struct{}{}; <-b.release }
func (b *blockSink) Name() string         { return "block" }

// TestStartAlertStampBeforeDelivery: startAlert writes the cooldown
// stamp before it returns, while the delivery is still in flight — an
// early exit of serve right after the call cannot lose it, and a second
// alert of the kind is suppressed at once.
func TestStartAlertStampBeforeDelivery(t *testing.T) {
	dir := t.TempDir()
	s := &blockSink{entered: make(chan struct{}, 2), release: make(chan struct{})}
	defer close(s.release)
	startAlert(dir, s, "config", "first")
	b, err := os.ReadFile(filepath.Join(dir, "alert.config"))
	if err != nil {
		t.Fatalf("stamp not written before startAlert returned: %v", err)
	}
	if strings.TrimSpace(string(b)) == "" {
		t.Fatalf("empty stamp")
	}
	select {
	case <-s.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("delivery never started")
	}
	// the sink is still parked: the second alert of the kind is suppressed
	// by the stamp, not by anything the delivery did
	startAlert(dir, s, "config", "second")
	sendAlertCooled(dir, s, "config", "third")
	select {
	case <-s.entered:
		t.Fatal("suppressed alert delivered")
	case <-time.After(50 * time.Millisecond):
	}
	// another kind goes out
	startAlert(dir, s, "profile", "other")
	select {
	case <-s.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("other kind not delivered")
	}
}
