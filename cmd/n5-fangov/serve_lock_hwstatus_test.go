package main

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/hwstatus"
)

// captureLog redirects the standard logger for one test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	oldW, oldF := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(oldW); log.SetFlags(oldF) })
	return &buf
}

// syncSink records alerts from other goroutines (startAlert is async).
type syncSink struct {
	mu    sync.Mutex
	kinds []string
	msgs  []string
}

func (s *syncSink) Alert(kind, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kinds = append(s.kinds, kind)
	s.msgs = append(s.msgs, msg)
}
func (s *syncSink) Name() string { return "sync" }

func (s *syncSink) wait(t *testing.T, n int) ([]string, []string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s.mu.Lock()
		k, m := append([]string(nil), s.kinds...), append([]string(nil), s.msgs...)
		s.mu.Unlock()
		if len(k) >= n || time.Now().After(deadline) {
			return k, m
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Decision 7 (audit I1): a lock error other than "busy" lets serve start
// without the lock, with a journal warning and the alert "start"; a lock
// held by validate/test still stops it, without an alert.
func TestServeWriterLockFailOpen(t *testing.T) {
	old := serveAcquireLock
	t.Cleanup(func() { serveAcquireLock = old })
	buf := captureLog(t)

	sink := &syncSink{}
	st := &serveState{rdir: t.TempDir(), alerter: sink}
	serveAcquireLock = func(dir, who string) (func(), error) {
		return nil, errors.New("writer lock " + dir + "/writer.lock: permission denied")
	}
	release, ok := serveWriterLock(st)
	if !ok || release == nil {
		t.Fatalf("lock error stopped serve (ok %v)", ok)
	}
	release()
	if !strings.Contains(buf.String(), logWarning+"WARNING: writer lock") {
		t.Fatalf("no journal warning: %q", buf.String())
	}
	kinds, msgs := sink.wait(t, 1)
	if len(kinds) != 1 || kinds[0] != "start" || !strings.Contains(msgs[0], "without its pwm writer lock") {
		t.Fatalf("alerts %v %v", kinds, msgs)
	}

	sink2 := &syncSink{}
	st2 := &serveState{rdir: t.TempDir(), alerter: sink2}
	serveAcquireLock = func(dir, who string) (func(), error) {
		return nil, fmt.Errorf("%w: %s (validate pid 42)", errWriterBusy, dir)
	}
	if _, ok := serveWriterLock(st2); ok {
		t.Fatal("serve started next to a running validate")
	}
	time.Sleep(50 * time.Millisecond)
	if k, _ := sink2.wait(t, 0); len(k) != 0 {
		t.Fatalf("busy lock alerted: %v", k)
	}
}

// Decision 9: the hardware-status warning reaches the journal (priority
// warning) on the first start and when the status changed; an unchanged
// restart logs it at info level. Without a state dir: info level.
func TestHWStatusWarningOnlyOnChange(t *testing.T) {
	buf := captureLog(t)
	dir := t.TempDir()
	exp := hwstatus.Result{Status: hwstatus.Experimental, Key: "N5 PRO / F8NAA / BIOS 1.05 / EC 0.14"}
	start := func(state string, r hwstatus.Result) string {
		buf.Reset()
		logHWStatusChange(state, "n5pro", r)
		return buf.String()
	}
	if out := start(dir, exp); !strings.Contains(out, logWarning+"WARNING: hardware status experimental") {
		t.Fatalf("first start: %q", out)
	}
	for i := 0; i < 2; i++ {
		if out := start(dir, exp); strings.Contains(out, logWarning) || !strings.Contains(out, "unchanged since the last start") {
			t.Fatalf("unchanged restart %d: %q", i, out)
		}
	}
	bios := exp
	bios.Key = "N5 PRO / F8NAA / BIOS 1.06 / EC 0.14"
	if out := start(dir, bios); !strings.Contains(out, logWarning+"WARNING") || !strings.Contains(out, "was: n5pro experimental N5 PRO / F8NAA / BIOS 1.05") {
		t.Fatalf("changed key: %q", out)
	}
	tested := hwstatus.Result{Status: hwstatus.Tested, Key: bios.Key}
	if out := start(dir, tested); out != "" {
		t.Fatalf("verified status logged: %q", out)
	}
	if out := start(dir, bios); !strings.Contains(out, logWarning+"WARNING") {
		t.Fatalf("tested -> experimental not a warning: %q", out)
	}
	if b, err := os.ReadFile(filepath.Join(dir, hwStatusFileName)); err != nil || !strings.Contains(string(b), "BIOS 1.06") {
		t.Fatalf("record %q %v", b, err)
	}
	if out := start("", exp); strings.Contains(out, logWarning) || !strings.Contains(out, "no state dir") {
		t.Fatalf("no state dir: %q", out)
	}
}
