//go:build unix

package main

import (
	"bufio"
	"bytes"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A SIGINT while the echo is off runs the restore path and exits 130;
// once the read is done the handler is gone and a later signal is not
// swallowed by it.
func TestEchoOnInterrupt(t *testing.T) {
	var out bytes.Buffer
	p := &prompter{in: bufio.NewReader(strings.NewReader("")), out: &out}
	exited := make(chan int, 1)
	stop := p.echoOnInterrupt(func(code int) { exited <- code })
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-exited:
		if code != 130 {
			t.Errorf("exit code %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SIGINT not handled")
	}
	stop()
	// a second stop-less handler: stop() before any signal must not block or exit
	stop2 := p.echoOnInterrupt(func(int) { t.Error("exit called without a signal") })
	stop2()
	time.Sleep(50 * time.Millisecond)
	if !strings.HasSuffix(out.String(), "\n") {
		t.Errorf("newline after the interrupted prompt missing: %q", out.String())
	}
}
