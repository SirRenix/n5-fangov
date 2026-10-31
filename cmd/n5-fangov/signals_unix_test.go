//go:build unix

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Regression tests for the shared stop-signal handling of validate and
// test (audit 2026-10-16 M1, V-1), modelled on the guardian tests of
// n5-ectest (ectest/cmd/n5-ectest/guardian_test.go): every stop signal
// restores the channel, the job-control stops do not stop the run.

// sendAndWait sends sig to this process and waits until it cancelled ctx.
func sendAndWait(t *testing.T, ctx context.Context, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Error(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Errorf("%v did not cancel the run", sig)
	}
}

// validateWithSignal runs validate pwm3 T1-T3 on the reference tree and
// sends sig in the 255 step of T1 (sleep 80: gate window 60, T1 rest 12).
func validateWithSignal(t *testing.T, sig syscall.Signal) (stdout string, rc int, root, out string) {
	t.Helper()
	root = referenceN5(t)
	fakeProc(t)
	stubValidate(t, localConsole)
	(&fakeClock{now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}).install(t, func(n int, ctx context.Context) bool {
		if n != 80 {
			return true
		}
		sendAndWait(t, ctx, sig)
		return false
	})
	validateStdin = strings.NewReader(validateConfirm + "\n")
	out = t.TempDir()
	stdout, _, rc = captureOutput(t, func() int {
		return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--tests", "T1,T2,T3", "--boot", "cold", "--out", out, "pwm3"})
	})
	return stdout, rc, root, out
}

// SIGTERM, SIGHUP (SSH dropped) and SIGQUIT (Ctrl-\) end validate like
// SIGINT: pwm3 back at its stop duty, a bundle with the restore, exit 1.
// Before rc2 SIGQUIT killed the process with a goroutine dump and no
// restore, SIGHUP was caught but SIGQUIT not.
func TestValidateStopSignalsRestore(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT} {
		t.Run(sig.String(), func(t *testing.T) {
			stdout, rc, root, out := validateWithSignal(t, sig)
			if rc != exitFail || !strings.Contains(stdout, "ABORTED: cancelled (signal)") || strings.Contains(stdout, "goroutine ") {
				t.Fatalf("rc %d\n%s", rc, stdout)
			}
			if b, _ := os.ReadFile(filepath.Join(root, "class", "hwmon", "hwmon14", "pwm3")); strings.TrimSpace(string(b)) != "140" {
				t.Fatalf("pwm3 = %q after %v, want 140", b, sig)
			}
			dirs, _ := os.ReadDir(out)
			if len(dirs) != 1 {
				t.Fatalf("bundle %v", dirs)
			}
			ev, _ := os.ReadFile(filepath.Join(out, dirs[0].Name(), "events.log"))
			if !strings.Contains(string(ev), "restore pwm3 ok") {
				t.Fatalf("events: %s", ev)
			}
		})
	}
}

// stubTest replaces the input and the sample wait of `test` for one test.
func stubTest(t *testing.T, onSleep func(n int, ctx context.Context) bool) {
	t.Helper()
	oldIn, oldSleep := testStdin, testSleep
	t.Cleanup(func() { testStdin, testSleep = oldIn, oldSleep })
	testStdin = strings.NewReader("TEST START\n")
	n := 0
	testSleep = func(ctx context.Context, d time.Duration) bool {
		n++
		if ctx.Err() != nil {
			return false
		}
		if onSleep != nil && !onSleep(n, ctx) {
			return false
		}
		return sleepCtx(ctx, d)
	}
	t.Setenv("N5FANGOV_RUN_DIR", t.TempDir())
}

// test pwm3 with short steps; the fixture's pwm3 file afterwards.
func runTestCmd(t *testing.T, root string) (string, int, string) {
	t.Helper()
	stdout, _, rc := captureOutput(t, func() int {
		return cmdTest([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--hold", "100ms", "--sample", "20ms", "pwm3"})
	})
	b, _ := os.ReadFile(filepath.Join(root, "class", "hwmon", "hwmon14", "pwm3"))
	return stdout, rc, strings.TrimSpace(string(b))
}

// V-1: `test` caught only SIGINT/SIGTERM. Every stop signal now restores
// the stop duty (pwm3: 140), and nothing is printed about the abort before
// the restore wrote it.
func TestTestStopSignalsRestore(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT} {
		t.Run(sig.String(), func(t *testing.T) {
			root := fakeN5(t, false)
			stubTest(t, func(n int, ctx context.Context) bool {
				if n != 2 { // 1: after manual mode, 2: first sample of the 255 step
					return true
				}
				sendAndWait(t, ctx, sig)
				return false
			})
			stdout, rc, pwm3 := runTestCmd(t, root)
			if rc != exitFail || pwm3 != "140" || strings.Contains(stdout, "goroutine ") {
				t.Fatalf("rc %d, pwm3 %q\n%s", rc, pwm3, stdout)
			}
			ab, rs := strings.Index(stdout, "aborted by signal"), strings.Index(stdout, "--- restore:")
			if ab < 0 || rs < 0 || strings.Contains(stdout, "pwm3 = 217") {
				t.Fatalf("abort/restore lines missing or the run went on:\n%s", stdout)
			}
		})
	}
}

// ---- Ctrl-Z: a child process in its own process group --------------------

// childEnv selects the scenario in the re-executed test binary.
const childEnv = "N5FANGOV_TEST_SIGCHILD"

// TestSignalChild is the child side: it runs validate or test with a
// SIGTSTP to itself in the middle of the writing phase and writes rc and
// pwm3 to the result file. Not a test of its own without childEnv.
func TestSignalChild(t *testing.T) {
	mode := os.Getenv(childEnv)
	if mode == "" {
		t.Skip("child of TestCtrlZDoesNotStop*")
	}
	resFile := os.Getenv(childEnv + "_RESULT")
	var rc int
	var pwm3 string
	switch mode {
	case "validate":
		root := referenceN5(t)
		fakeProc(t)
		stubValidate(t, localConsole)
		(&fakeClock{now: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}).install(t, func(n int, ctx context.Context) bool {
			if n == 80 {
				_ = syscall.Kill(os.Getpid(), syscall.SIGTSTP)
				time.Sleep(200 * time.Millisecond) // a stopped process would stop here
			}
			return true
		})
		validateStdin = strings.NewReader(validateConfirm + "\n")
		_, _, rc = captureOutput(t, func() int {
			return cmdValidate([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--tests", "T1", "--boot", "cold", "--out", t.TempDir(), "pwm3"})
		})
		b, _ := os.ReadFile(filepath.Join(root, "class", "hwmon", "hwmon14", "pwm3"))
		pwm3 = strings.TrimSpace(string(b))
	case "test":
		root := fakeN5(t, false)
		stubTest(t, func(n int, ctx context.Context) bool {
			if n == 2 {
				_ = syscall.Kill(os.Getpid(), syscall.SIGTSTP)
				time.Sleep(200 * time.Millisecond)
			}
			return true
		})
		_, rc, pwm3 = runTestCmd(t, root)
	default:
		t.Fatalf("unknown child mode %q", mode)
	}
	if err := os.WriteFile(resFile, []byte(fmt.Sprintf("rc=%d pwm3=%s\n", rc, pwm3)), 0o600); err != nil {
		t.Fatal(err)
	}
}

// runChild starts TestSignalChild in its own process group (so the
// group is not orphaned and SIGTSTP would really stop it) and waits at
// most a minute: a stopped child is killed and the test fails.
func runChild(t *testing.T, mode string) string {
	t.Helper()
	res := filepath.Join(t.TempDir(), "result")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSignalChild$")
	cmd.Env = append(os.Environ(), childEnv+"="+mode, childEnv+"_RESULT="+res)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if err != nil && !errors.As(err, &ee) {
			t.Fatal(err)
		}
		if err != nil {
			t.Fatalf("child %s failed: %v\n%s", mode, err, out.String())
		}
	case <-time.After(time.Minute):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		t.Fatalf("child %s did not end within a minute (stopped by SIGTSTP?)\n%s", mode, out.String())
	}
	b, err := os.ReadFile(res)
	if err != nil {
		t.Fatalf("no result: %v\n%s", err, out.String())
	}
	return strings.TrimSpace(string(b))
}

// M1: Ctrl-Z (SIGTSTP) does not stop validate; the run goes on with its
// limit checks and ends restored with exit 0.
func TestCtrlZDoesNotStopValidate(t *testing.T) {
	if got := runChild(t, "validate"); got != "rc=0 pwm3=140" {
		t.Fatalf("child result %q", got)
	}
}

// V-1: the same for test.
func TestCtrlZDoesNotStopTest(t *testing.T) {
	if got := runChild(t, "test"); got != "rc=0 pwm3=140" {
		t.Fatalf("child result %q", got)
	}
}
