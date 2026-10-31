//go:build unix

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// SIGINT during T1 (channel in manual mode at a test duty): the run
// restores pwm3 to its stop duty and exits 1.
func TestValidateSignalRestores(t *testing.T) {
	root := referenceN5(t)
	fakeProc(t)
	stubValidate(t, localConsole)
	// gate window 60 sleeps, T1 rest 12, then the 255 step: signal in it
	(&fakeClock{now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}).install(t, func(n int, ctx context.Context) bool {
		if n != 80 {
			return true
		}
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Error(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Error("SIGINT did not cancel the run")
		}
		return false
	})
	validateStdin = strings.NewReader(validateConfirm + "\n")
	out := t.TempDir()
	stdout, _, rc := captureOutput(t, func() int {
		return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--tests", "T1,T2,T3", "--boot", "cold", "--out", out, "pwm3"})
	})
	if rc != exitFail || !strings.Contains(stdout, "ABORTED: cancelled (signal)") {
		t.Fatalf("rc %d\n%s", rc, stdout)
	}
	ec := filepath.Join(root, "class", "hwmon", "hwmon14")
	if b, _ := os.ReadFile(filepath.Join(ec, "pwm3")); strings.TrimSpace(string(b)) != "140" {
		t.Fatalf("pwm3 = %q after the signal, want 140", b)
	}
	dirs, _ := os.ReadDir(out)
	if len(dirs) != 1 {
		t.Fatalf("bundle %v", dirs)
	}
	ev, _ := os.ReadFile(filepath.Join(out, dirs[0].Name(), "events.log"))
	if !strings.Contains(string(ev), "write pwm3=255") || !strings.Contains(string(ev), "restore pwm3 ok") {
		t.Fatalf("events: %s", ev)
	}
}

// stdout is a closed pipe (`validate | tee`, tee killed by the same
// Ctrl-C): the process must not die of SIGPIPE before it restores; the
// dry run in a child process completes and writes its bundle.
func TestValidateIgnoresSIGPIPE(t *testing.T) {
	if out := os.Getenv("N5FANGOV_TEST_SIGPIPE_OUT"); out != "" {
		referenceN5(t)
		fakeProc(t)
		os.Exit(cmdValidate([]string{"--dry-run", "pwm3", "--out", out, "--boot", "cold", "--hold", "10s", "--observe", "60s", "--gate-window", "10s"}))
	}
	out := t.TempDir()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close() // nobody reads: every write to stdout is EPIPE
	cmd := exec.Command(os.Args[0], "-test.run=^TestValidateIgnoresSIGPIPE$")
	cmd.Env = append(os.Environ(), "N5FANGOV_TEST_SIGPIPE_OUT="+out)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	w.Close()
	if err != nil {
		t.Fatalf("child: %v (stderr %s)", err, stderr.String())
	}
	if dirs, _ := os.ReadDir(out); len(dirs) != 1 {
		t.Fatalf("no bundle: %v", dirs)
	}
}
