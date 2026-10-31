//go:build unix

package n5driver

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// L3: a dcr read that does not return (here a FIFO without a writer, which
// blocks in open like a driver read that hangs) ends after DCRTimeout;
// while it is still blocked a second read fails at once, and once it
// returns reads work again.
func TestReadDCRTimeout(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "dcr")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	old := DCRTimeout
	DCRTimeout = 100 * time.Millisecond
	t.Cleanup(func() { DCRTimeout = old })

	start := time.Now()
	if _, err := ReadDCR(fifo); !errors.Is(err, ErrDCRTimeout) {
		t.Fatalf("hanging read: %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("timeout took %s", el)
	}
	start = time.Now()
	if _, err := ReadDCR(fifo); !errors.Is(err, ErrDCRTimeout) || time.Since(start) > 50*time.Millisecond {
		t.Fatalf("second read while the first is blocked: %v after %s", err, time.Since(start))
	}
	// release the blocked reader: open the write end, write the dump, close
	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("1800: 00 ff 00 55 4a 8c 8c 8c 00 00 00 06 00 00 00 00\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	deadline := time.Now().Add(2 * time.Second)
	for dcrBlocked.Load() {
		if time.Now().After(deadline) {
			t.Fatal("blocked flag not cleared after the read returned")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ok := filepath.Join(t.TempDir(), "dcr-file")
	if err := os.WriteFile(ok, []byte("1800: 00 ff 00 55 4a 8c 8c 8c 00 00 00 06 00 00 00 00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if d, err := ReadDCR(ok); err != nil || d.Reg(1) != 0x55 {
		t.Fatalf("read after recovery: %v %v", d, err)
	}
}
