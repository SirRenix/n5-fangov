//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/hwmon/hwmontest"
)

// TestServeSmoke (AUDIT hoch 4) starts the real daemon end to end — config
// file, N5 Pro fake sysfs, dry-run controller, unix socket, plain-HTTP
// loopback listener, alert manager, session store — reads the state over
// TCP and over the socket, sets an override through the socket, stops it
// with SIGTERM and expects a clean exit. It is the only test that runs
// cmdServe; everything else in this package tests the pieces.
func TestServeSmoke(t *testing.T) {
	root := hwmontest.Copy(t, hwmontest.N5Pro(t))
	t.Setenv("N5FANGOV_SYSFS", root)
	t.Setenv("NOTIFY_SOCKET", "")
	base := t.TempDir()
	rdir, sdir := filepath.Join(base, "run"), filepath.Join(base, "state")
	cfgPath := filepath.Join(base, "config.toml")
	cfg := `[daemon]
interval = "2s"
profile = "n5pro"

[web]
listen = "127.0.0.1:0"
auth = "none"
tls = "off"

[alert]
transport = "log"

[log]
file = ""
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	// cmdServe logs to os.Stdout; capture it to find the TCP port and the
	// lifecycle lines. The log package keeps the pipe as its output until
	// the test restores it.
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
		exit <- cmdServe([]string{"--config", cfgPath, "--dry-run", "--run-dir", rdir, "--state-dir", sdir})
	}()

	// Wait for the listener line and the first cycle.
	var port string
	deadline := time.Now().Add(20 * time.Second)
	portRe := regexp.MustCompile(`web: listening on http://127\.0\.0\.1:(\d+)`)
	for time.Now().Before(deadline) {
		s := logged()
		if m := portRe.FindStringSubmatch(s); m != nil && strings.Contains(s, "first cycle done, READY") {
			port = m[1]
			break
		}
		select {
		case code := <-exit:
			t.Fatalf("serve exited early with %d; log:\n%s", code, logged())
		case <-time.After(100 * time.Millisecond):
		}
	}
	if port == "" {
		t.Fatalf("no listener/READY within 20s; log:\n%s", logged())
	}

	// TCP: full state (auth = none → every caller is signed in).
	res, err := http.Get("http://127.0.0.1:" + port + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var st struct {
		Status   string `json:"status"`
		Profile  string `json:"profile"`
		Hwmon    string `json:"hwmon_path"`
		Channels []struct {
			Name string `json:"name"`
			Mode string `json:"mode"`
		} `json:"channels"`
	}
	if res.StatusCode != 200 || json.Unmarshal(body, &st) != nil {
		t.Fatalf("GET /api/state over TCP: %d %s", res.StatusCode, body)
	}
	if st.Status != "dry-run" || st.Profile != "n5pro" || st.Hwmon == "" || len(st.Channels) != 3 {
		t.Fatalf("state over TCP: %+v", st)
	}
	// TCP: a state-changing request without the CSRF header is refused.
	req, _ := http.NewRequest(http.MethodPut, "http://127.0.0.1:"+port+"/api/override/cpu", strings.NewReader(`{"duty":100}`))
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT without CSRF header over TCP: %d, want 403", res.StatusCode)
	}

	// Socket: same document, no guards; override through the CLI client.
	api := newAPI(rdir)
	var st2 map[string]any
	if err := api.get("/api/state", &st2); err != nil {
		t.Fatalf("GET /api/state over the socket: %v", err)
	}
	if st2["profile"] != "n5pro" {
		t.Fatalf("state over socket: %v", st2["profile"])
	}
	var ov map[string]any
	if err := api.do(http.MethodPut, "/api/override/cpu", map[string]any{"duty": 100}, &ov); err != nil {
		t.Fatalf("override over the socket: %v", err)
	}
	if ov["ok"] != true || ov["duty"] != float64(100) {
		t.Fatalf("override answer: %v", ov)
	}
	if _, err := os.Stat(filepath.Join(rdir, "override.cpu")); err != nil {
		t.Fatalf("override file: %v", err)
	}
	// /api/about is public and carries the version; /api/system needs the
	// inventory collector wired.
	var about map[string]any
	if err := api.get("/api/about", &about); err != nil || about["name"] != "n5-fangov" {
		t.Fatalf("about: %v %v", about, err)
	}
	var sys map[string]any
	if err := api.get("/api/system", &sys); err != nil || sys["fan_controller"] == nil {
		t.Fatalf("system: %v %v", sys, err)
	}

	// Clean stop on SIGTERM.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-exit:
		if code != 0 {
			t.Fatalf("exit %d; log:\n%s", code, logged())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("serve did not stop within 30s; log:\n%s", logged())
	}
	pw.Close()
	<-copied
	s := logged()
	for _, want := range []string{"signal received, stopping", "stopped (exit 0)"} {
		if !strings.Contains(s, want) {
			t.Errorf("log lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "fatal:") {
		t.Errorf("fatal line in log:\n%s", s)
	}
	// Dry-run never writes: the fake tree's pwm files are untouched.
	if b, _ := os.ReadFile(filepath.Join(root, "class", "hwmon", "hwmon14", "pwm1")); strings.TrimSpace(string(b)) == "100" {
		t.Error("dry-run wrote pwm1")
	}
}
