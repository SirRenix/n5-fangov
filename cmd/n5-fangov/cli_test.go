package main

import (
	"strings"
	"testing"
)

// TestSetRefusesBadChannelName: a channel name that is not a plain
// identifier never reaches the URL path (the mux would redirect a ".."
// path to another endpoint).
func TestSetRefusesBadChannelName(t *testing.T) {
	for _, name := range []string{"../config", "cpu?x", "", "CPU", strings.Repeat("a", 33)} {
		out := captureStderr(t, func() {
			if rc := cmdSet([]string{name, "30"}); rc != exitUsage {
				t.Errorf("set %q: rc=%d", name, rc)
			}
		})
		if !strings.Contains(out, "must match") {
			t.Errorf("set %q: %q", name, out)
		}
	}
	out := captureStderr(t, func() {
		if rc := cmdAuto([]string{"../config"}); rc != exitUsage {
			t.Errorf("auto: rc=%d", rc)
		}
	})
	if !strings.Contains(out, "must match") {
		t.Errorf("auto: %q", out)
	}
	out = captureStderr(t, func() {
		if rc := cmdLog([]string{"-n", "0"}); rc != exitUsage {
			t.Errorf("log -n 0: rc=%d", rc)
		}
	})
	if !strings.Contains(out, "1..5000") {
		t.Errorf("log -n 0: %q", out)
	}
	if rc := cmdLog([]string{"-n", "6000"}); rc != exitUsage {
		t.Errorf("log -n 6000: rc=%d", rc)
	}
	if rc := cmdTest([]string{"--sample", "0s", "cpu"}); rc != exitUsage {
		t.Errorf("test --sample 0: rc=%d", rc)
	}
	if rc := cmdTest([]string{"--sample", "10s", "--hold", "5s", "cpu"}); rc != exitUsage {
		t.Errorf("test sample > hold: rc=%d", rc)
	}
}
