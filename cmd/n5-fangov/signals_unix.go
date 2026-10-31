//go:build unix

package main

import (
	"os/signal"
	"syscall"
)

// ignoreRunSignals ignores the job-control signals of the terminal and
// SIGPIPE for a writing run: a stopped process (Ctrl-Z, a background read
// or write on the terminal) would leave a channel manual at a test duty
// without any limit check, and a dead pipe reader (`... | tee log`, tee
// killed by the same Ctrl-C) must not kill the process before it restored.
// Same set as n5-ectest (ectest/cmd/n5-ectest/signals_unix.go).
func ignoreRunSignals() {
	signal.Ignore(syscall.SIGTSTP, syscall.SIGTTIN, syscall.SIGTTOU, syscall.SIGPIPE)
}
