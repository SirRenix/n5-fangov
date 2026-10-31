package control

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/logfile"
)

// Gate (g): driver read errors (ETIMEDOUT/EAGAIN/ENODATA/ENODEV) reach the
// journal as warnings, one per source per ReadErrInterval, with a count of
// the suppressed ones; other errors are not logged by this path.
func TestDriverReadErrorsAreWarnings(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	wrapped := fmt.Errorf("sensor ec:cpu: %w", &fs.PathError{Op: "read", Path: "/sys/class/hwmon/hwmon7/temp1_input", Err: syscall.ETIMEDOUT})
	h.c.noteReadErr("sensor ec:cpu", wrapped)
	if n := h.log.count(logfile.PrefixWarning + "driver read error: sensor ec:cpu: ETIMEDOUT"); n != 1 {
		t.Fatalf("first error: %d warning lines, log %v", n, h.log.lines)
	}
	for i := 0; i < 3; i++ {
		h.clock.advance(time.Minute)
		h.c.noteReadErr("sensor ec:cpu", wrapped)
	}
	if n := h.log.count("driver read error"); n != 1 {
		t.Fatalf("within the interval: %d lines", n)
	}
	h.c.noteReadErr("fan1_input (channel cpu)", syscall.EAGAIN) // other source: own budget
	h.clock.advance(ReadErrInterval)
	h.c.noteReadErr("sensor ec:cpu", wrapped)
	if n := h.log.count("ETIMEDOUT (3 more since"); n != 1 {
		t.Fatalf("suppressed count missing: %v", h.log.lines)
	}
	if n := h.log.count("fan1_input (channel cpu): EAGAIN"); n != 1 {
		t.Fatalf("second source: %v", h.log.lines)
	}
	before := h.log.count("driver read error")
	h.clock.advance(ReadErrInterval)
	h.c.noteReadErr("sensor x", errors.New("not an errno"))
	h.c.noteReadErr("sensor x", nil)
	if h.log.count("driver read error") != before {
		t.Fatal("non-driver errors logged")
	}
	for _, e := range []syscall.Errno{syscall.ENODATA, syscall.ENODEV} {
		if _, ok := driverErrno(fmt.Errorf("x: %w", e)); !ok {
			t.Errorf("%v not classified", e)
		}
	}
}

// Suppressed errors are not lost when the errors stop: the next good read
// after the interval logs their count once.
func TestDriverReadRecoveredCount(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.c.noteReadOK("sensor ec:cpu") // no state: nothing
	h.c.noteReadErr("sensor ec:cpu", syscall.ETIMEDOUT)
	h.clock.advance(time.Minute)
	h.c.noteReadErr("sensor ec:cpu", syscall.ETIMEDOUT)
	h.c.noteReadErr("sensor ec:cpu", syscall.ETIMEDOUT)
	h.c.noteReadOK("sensor ec:cpu") // within the interval: still pending
	if n := h.log.count("driver read recovered"); n != 0 {
		t.Fatalf("recovery line within the interval: %v", h.log.lines)
	}
	h.clock.advance(ReadErrInterval)
	h.c.noteReadOK("sensor ec:cpu")
	h.c.noteReadOK("sensor ec:cpu")
	if n := h.log.count("driver read recovered: sensor ec:cpu: 2 more error(s)"); n != 1 {
		t.Fatalf("recovery line: %v", h.log.lines)
	}
}

// The loop path: an unreadable tach with ETIMEDOUT logs the warning.
func TestReadRPMTimeoutLogged(t *testing.T) {
	h := newHarness(t, n5cfg(), nil)
	h.dev.mu.Lock()
	h.dev.rpmErr = syscall.ETIMEDOUT
	h.dev.mu.Unlock()
	h.cycles(1)
	if h.log.count(logfile.PrefixWarning+"driver read error: fan") == 0 {
		t.Fatalf("no warning: %v", h.log.lines)
	}
}
