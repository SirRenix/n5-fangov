//go:build unix

package ringfile

import (
	"path/filepath"
	"testing"
	"time"
)

// The lock serializes two holders on one file; a holder that does not let
// go within the wait costs the serialization only (Lock returns a no-op,
// logged once).
func TestLockSerializes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ring.json")
	unlock, err := lockFile(path+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockFile(path+".lock", 50*time.Millisecond); err == nil {
		t.Fatal("second holder got the lock")
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(released)
		unlock()
	}()
	u2, err := lockFile(path+".lock", 2*time.Second)
	if err != nil {
		t.Fatalf("waiting holder: %v", err)
	}
	select {
	case <-released:
	default:
		t.Fatal("got the lock before it was released")
	}
	u2()
}
