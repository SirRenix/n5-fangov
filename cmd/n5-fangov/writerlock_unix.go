//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// writerLockName is the lock file in the run dir that every pwm writer
// holds while it writes: serve for its lifetime, test and validate for
// their run. The run dir survives a daemon stop (RuntimeDirectoryPreserve).
const writerLockName = "writer.lock"

// errWriterBusy: another process holds the writer lock.
var errWriterBusy = errors.New("another pwm writer holds the lock")

// acquireWriterLock takes the exclusive writer lock (flock, released by
// the kernel when the process ends, also on SIGKILL). who is recorded in
// the file for the message another writer gets.
func acquireWriterLock(dir, who string) (release func(), err error) {
	release, _, err = acquireWriterLockChecked(dir, who)
	return release, err
}

// acquireWriterLockChecked is acquireWriterLock with check: "" while the
// lock file on disk is still the one this process holds, otherwise why not
// (removed or replaced: another writer could take a new lock). validate
// asks it at every sample (TESTSPEC 0.4 §3 G1: writer-lock inode per sample).
func acquireWriterLockChecked(dir, who string) (release func(), check func() string, err error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, nil, err
	}
	p := filepath.Join(dir, writerLockName)
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		b := make([]byte, 256)
		n, _ := f.ReadAt(b, 0)
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			holder := strings.TrimSpace(string(b[:n]))
			if holder == "" {
				holder = "unknown process"
			}
			return nil, nil, fmt.Errorf("%w: %s (%s)", errWriterBusy, p, holder)
		}
		return nil, nil, fmt.Errorf("writer lock %s: %w", p, err)
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("%s pid %d since %s\n", who, os.Getpid(), time.Now().Format(time.RFC3339))), 0)
	check = func() string {
		held, err := f.Stat()
		if err != nil {
			return "writer lock: " + err.Error()
		}
		disk, err := os.Stat(p)
		if err != nil {
			return "writer lock file " + p + " removed"
		}
		if !os.SameFile(held, disk) {
			return "writer lock file " + p + " replaced (another inode)"
		}
		return ""
	}
	return func() {
		_ = f.Truncate(0)
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, check, nil
}
