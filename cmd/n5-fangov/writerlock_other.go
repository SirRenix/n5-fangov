//go:build !unix

package main

import "errors"

// errWriterBusy: another process holds the writer lock (unix only).
var errWriterBusy = errors.New("another pwm writer holds the lock")

// acquireWriterLock is a no-op where flock does not exist; n5-fangov
// writes pwm only on Linux.
func acquireWriterLock(dir, who string) (release func(), err error) {
	return func() {}, nil
}

// acquireWriterLockChecked is acquireWriterLock with a check that is
// always "" here.
func acquireWriterLockChecked(dir, who string) (release func(), check func() string, err error) {
	return func() {}, func() string { return "" }, nil
}
