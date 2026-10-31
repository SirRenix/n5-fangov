//go:build !unix

package main

import (
	"os/signal"
	"syscall"
)

// ignoreRunSignals: no job control here; SIGPIPE only.
func ignoreRunSignals() { signal.Ignore(syscall.SIGPIPE) }
