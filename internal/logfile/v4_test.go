package logfile

// Regression tests for the v0.4 audit fixes: a Writer whose file could not
// be reopened after a rotation comes back on the next Write, and the
// journald priority prefix is stripped from the file copy.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReopenAfterFailedRotation: the log directory disappears between
// two writes; the rotation's reopen fails, the file log pauses, and the
// next Write recreates directory and file instead of reporting "closed"
// until the next restart.
func TestReopenAfterFailedRotation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "logs")
	path := filepath.Join(dir, "d.log")
	w, err := New(path, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	line := []byte(strings.Repeat("x", 1023) + "\n")
	if _, err := w.Write(line); err != nil {
		t.Fatal(err)
	}
	// make the next write exceed the limit, then replace the directory by
	// a file: the rotation's reopen cannot even recreate the directory
	w.maxSize = 1500
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(line); err == nil {
		t.Fatal("write with the directory blocked succeeded")
	}
	if w.f != nil || w.closed || w.reopenErr == "" {
		t.Fatalf("state after failed reopen: f=%v closed=%v reopenErr=%q", w.f != nil, w.closed, w.reopenErr)
	}
	// still blocked: fails again, no state change
	if _, err := w.Write(line); err == nil {
		t.Fatal("second write with the directory blocked succeeded")
	}
	// the obstacle is gone: the next write recreates directory and file
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("back\n")); err != nil {
		t.Fatalf("write after reopen: %v", err)
	}
	if w.reopenErr != "" {
		t.Errorf("reopenErr not cleared: %q", w.reopenErr)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not recreated: %v", err)
	}
	if string(b) != "back\n" {
		t.Errorf("content %q", b)
	}
	lines, err := w.Lines(10)
	if err != nil || len(lines) != 1 || lines[0] != "back" {
		t.Errorf("Lines = %v %v", lines, err)
	}
	// Clear works on the reopened file; a closed writer stays closed
	if err := w.Clear(); err != nil {
		t.Errorf("Clear: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("late\n")); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("write after Close = %v", err)
	}
	if err := w.Clear(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("Clear after Close = %v", err)
	}
}

// TestTimestampedStripsPrefix: the file copy carries the time stamp and no
// journald priority prefix; a line without one is unchanged apart from
// the stamp.
func TestTimestampedStripsPrefix(t *testing.T) {
	var buf bytes.Buffer
	w := Timestamped(&buf)
	for _, in := range []string{PrefixErr + "ALERT[x]: y\n", PrefixWarning + "WARNING: z\n", "plain\n", "<9>not a priority\n"} {
		n, err := w.Write([]byte(in))
		if err != nil || n != len(in) {
			t.Fatalf("Write(%q) = %d %v", in, n, err)
		}
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %v", lines)
	}
	want := []string{"ALERT[x]: y", "WARNING: z", "plain", "<9>not a priority"}
	for i, l := range lines {
		if len(l) < 20 || l[19] != ' ' || l[20:] != want[i] {
			t.Errorf("line %d = %q, want stamp + %q", i, l, want[i])
		}
	}
}
