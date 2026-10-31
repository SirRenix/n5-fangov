//go:build linux

package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/hwmon/hwmontest"
)

// TestStallAlarmChainSimulated is TESTSPEC 0.4 §5a "Stall detection" (b),
// operator decision 2026-10-17 (#26 a+b): the alarm chain with a SIMULATED
// sensor. The real daemon (not --dry-run) runs against a fake sysfs tree
// (N5FANGOV_SYSFS) whose fan2_input reads 0 rpm while the SSD channel runs
// at a duty >= stall_min_duty; after stall_cycles cycles the daemon must
// detect the stall, write 255 to the fake pwm2 and deliver the alert
// through the configured notification path (a webhook here).
//
// This is "alarm chain, simulated sensor" - NOT a hardware result. It says
// nothing about whether the EC reports 0 rpm for a stopped fan or which HDD
// fan carries fan3; the hardware part (a) is N/A "not testable without
// intervention" on a closed, running host.
func TestStallAlarmChainSimulated(t *testing.T) {
	root := hwmontest.Copy(t, hwmontest.N5Pro(t))
	ec := filepath.Join(root, "class", "hwmon", "hwmon14")
	if err := os.WriteFile(filepath.Join(ec, "fan2_input"), []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("N5FANGOV_SYSFS", root)
	t.Setenv("NOTIFY_SOCKET", "")

	var mu sync.Mutex
	var alerts []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		alerts = append(alerts, string(b))
		mu.Unlock()
	}))
	defer hook.Close()
	got := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(alerts, "\n")
	}

	base := t.TempDir()
	cfgPath := filepath.Join(base, "config.toml")
	cfg := `[daemon]
interval = "2s"
profile = "n5pro"
stall_min_duty = 60
stall_cycles = 2

[web]
listen = "127.0.0.1:0"
auth = "none"
tls = "off"

[alert]
transport = "webhook"
webhook_url = "` + hook.URL + `/stall"
webhook_format = "json"

[log]
file = ""
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = pw
	var out bytes.Buffer
	var outMu sync.Mutex
	copied := make(chan struct{})
	go func() {
		defer close(copied)
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				outMu.Lock()
				out.Write(buf[:n])
				outMu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	logged := func() string {
		outMu.Lock()
		defer outMu.Unlock()
		return out.String()
	}
	t.Cleanup(func() {
		os.Stdout = oldStdout
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})
	exit := make(chan int, 1)
	go func() {
		exit <- cmdServe([]string{"--config", cfgPath, "--run-dir", filepath.Join(base, "run"), "--state-dir", filepath.Join(base, "state")})
	}()
	pwm2 := filepath.Join(ec, "pwm2")
	deadline := time.Now().Add(40 * time.Second)
	done := false
	for time.Now().Before(deadline) && !done {
		b, _ := os.ReadFile(pwm2)
		done = strings.TrimSpace(string(b)) == "255" && strings.Contains(got(), `"kind":"stall"`)
		select {
		case code := <-exit:
			t.Fatalf("serve exited early with %d; log:\n%s", code, logged())
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exit:
	case <-time.After(30 * time.Second):
		t.Fatalf("serve did not stop; log:\n%s", logged())
	}
	pw.Close()
	<-copied
	if !done {
		b, _ := os.ReadFile(pwm2)
		t.Fatalf("simulated stall: pwm2 %q, webhook %q; log:\n%s", strings.TrimSpace(string(b)), got(), logged())
	}
	if !strings.Contains(got(), "fan stopped (0 RPM at duty") {
		t.Errorf("webhook body without the stall text: %s", got())
	}
	if !strings.Contains(logged(), "stall") {
		t.Errorf("journal line of the stall missing:\n%s", logged())
	}
}
