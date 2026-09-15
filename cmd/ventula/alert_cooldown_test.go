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
