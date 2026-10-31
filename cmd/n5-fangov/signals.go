package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// stopSignals end a writing run (validate, test). They are caught, never
// left to the Go default: SIGQUIT (Ctrl-\) would dump the goroutines and
// exit 2 without a restore, SIGHUP (SSH session dropped without tmux) would
// kill the process the same way. Shared by validate and test.
var stopSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT}

// stopContext is the one signal set-up of the writing commands: the
// terminal stop signals and SIGPIPE are ignored (ignoreRunSignals), and the
// returned context is cancelled by the first stop signal. The caller
// restores its channels before it prints anything after the cancellation,
// and calls cancel when it is done (signal.NotifyContext). Register it
// after the confirmation prompt: before, Ctrl-C must still end the prompt.
func stopContext() (context.Context, context.CancelFunc) {
	ignoreRunSignals()
	return signal.NotifyContext(context.Background(), stopSignals...)
}
