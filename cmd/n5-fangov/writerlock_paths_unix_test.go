//go:build unix

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Writer-lock paths outside serve (audit 2026-10-16 G7, rc2 review R5) and
// SIGPIPE for `test` (rc2 review R2).

// setManual puts pwm3 of the N5 Pro fixture at a value no safe state uses,
// so any write by the command under test changes the tree.
func setManual(t *testing.T, root string) {
	t.Helper()
	ec := filepath.Join(root, "class", "hwmon", "hwmon14")
	for f, v := range map[string]string{"pwm3_enable": "1", "pwm3": "200"} {
		if err := os.WriteFile(filepath.Join(ec, f), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// failsafe (ExecStopPost) next to a running validate/test writes nothing
// and exits 0; with the lock free it sets the safe state (control: the
// fixture is one failsafe acts on).
func TestFailsafeWritesNothingWhileLockBusy(t *testing.T) {
	root := fakeN5(t, false)
	setManual(t, root)
	dir := t.TempDir()
	t.Setenv("N5FANGOV_RUN_DIR", dir)
	cfg := filepath.Join(t.TempDir(), "none.toml")

	release, err := acquireWriterLock(dir, "validate")
	if err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, root)
	stdout, _, rc := captureOutput(t, func() int { return cmdFailsafe([]string{"--config", cfg}) })
	if rc != exitOK || !strings.Contains(stdout, "nothing written here") || !strings.Contains(stdout, "validate pid") {
		t.Fatalf("busy lock: rc %d\n%s", rc, stdout)
	}
	if after := snapshotTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("failsafe wrote to sysfs while another writer held the lock")
	}
	release()

	stdout, _, rc = captureOutput(t, func() int { return cmdFailsafe([]string{"--config", cfg}) })
	if rc != exitOK || !strings.Contains(stdout, "safe state set on n5pro") {
		t.Fatalf("free lock: rc %d\n%s", rc, stdout)
	}
	if after := snapshotTree(t, root); reflect.DeepEqual(before, after) {
		t.Fatal("control: failsafe with a free lock changed nothing; the busy case proves nothing")
	}
}

// `test --force` skips the daemon check only; the writer lock still
// refuses the run and nothing is written.
func TestTestForceDoesNotSkipWriterLock(t *testing.T) {
	root := fakeN5(t, false)
	setManual(t, root)
	stubTest(t, nil)
	release, err := acquireWriterLock(os.Getenv("N5FANGOV_RUN_DIR"), "validate")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	before := snapshotTree(t, root)
	_, stderr, rc := captureOutput(t, func() int {
		return cmdTest([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--hold", "100ms", "--sample", "20ms", "--force", "pwm3"})
	})
	if rc != exitFail || !strings.Contains(stderr, errWriterBusy.Error()) {
		t.Fatalf("rc %d\n%s", rc, stderr)
	}
	if after := snapshotTree(t, root); !reflect.DeepEqual(before, after) {
		t.Fatal("test --force wrote to sysfs while another writer held the lock")
	}
}

// stdout is a closed pipe (`n5-fangov test pwm3 | tee`, tee gone): the run
// must not die of SIGPIPE; it completes and restores pwm3 to 140.
func TestTestIgnoresSIGPIPE(t *testing.T) {
	if res := os.Getenv("N5FANGOV_TEST_SIGPIPE_TEST"); res != "" {
		root := fakeN5(t, false)
		stubTest(t, nil)
		rc := cmdTest([]string{"--config", filepath.Join(t.TempDir(), "none.toml"), "--hold", "100ms", "--sample", "20ms", "pwm3"})
		b, _ := os.ReadFile(filepath.Join(root, "class", "hwmon", "hwmon14", "pwm3"))
		if err := os.WriteFile(res, []byte(fmt.Sprintf("rc=%d pwm3=%s\n", rc, strings.TrimSpace(string(b)))), 0o600); err != nil {
			os.Exit(3)
		}
		os.Exit(0) // the test framework's own output would go to the closed pipe
	}
	res := filepath.Join(t.TempDir(), "result")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r.Close() // nobody reads: every write to stdout is EPIPE
	cmd := exec.Command(os.Args[0], "-test.run=^TestTestIgnoresSIGPIPE$")
	cmd.Env = append(os.Environ(), "N5FANGOV_TEST_SIGPIPE_TEST="+res)
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Run()
	w.Close()
	if err != nil {
		t.Fatalf("child: %v (stderr %s)", err, stderr.String())
	}
	b, err := os.ReadFile(res)
	if err != nil {
		t.Fatalf("no result: %v (stderr %s)", err, stderr.String())
	}
	if got := strings.TrimSpace(string(b)); got != "rc=0 pwm3=140" {
		t.Fatalf("child result %q (stderr %s)", got, stderr.String())
	}
}
