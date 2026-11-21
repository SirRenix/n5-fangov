//go:build unix

package ringfile

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// lockFile takes an exclusive flock on path (created 0600), waiting at most
// wait. The returned func releases it. The lock serializes the
// read-merge-write of a shared ring file between the daemon and CLI runs
// (#54); the file itself is still replaced atomically.
func lockFile(path string, wait time.Duration) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			_ = f.Close()
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}
