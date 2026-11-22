//go:build linux

package main

import (
	"bytes"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/hwmon/hwmontest"
)

// e2eDaemon is the real daemon (dry-run, N5 Pro fake sysfs, the N5 Pro
// channels in the file) for the end-to-end tests: the socket API, the
// captured log, and a wait helper that fails on an early exit.
type e2eDaemon struct {
	t       *testing.T
	cfgPath string
	state   string
	api     *api
	exit    chan int
	logged  func() string
	stopped bool
	stopFn  func()
}

func startE2E(t *testing.T) *e2eDaemon {
	t.Helper()
	root := hwmontest.Copy(t, hwmontest.N5Pro(t))
	t.Setenv("N5FANGOV_SYSFS", root)
	t.Setenv("NOTIFY_SOCKET", "")
	base := t.TempDir()
	rdir, sdir := filepath.Join(base, "run"), filepath.Join(base, "state")
	cfgPath := filepath.Join(base, "config.toml")
	cfg := config.Default()
	cfg.Daemon.Profile = "n5pro"
	cfg.Daemon.Interval = 2 * time.Second
	cfg.Channels = config.N5ProChannels()
	cfg.Web = config.Web{Listen: "127.0.0.1:0", Auth: "none", TLS: "off"}
	cfg.Alert.Transport = "log"
	cfg.Log.File = ""
	if err := os.WriteFile(cfgPath, config.Marshal(cfg), 0o600); err != nil {
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
	d := &e2eDaemon{t: t, cfgPath: cfgPath, state: sdir, api: newAPI(rdir), exit: make(chan int, 1)}
	d.logged = func() string {
		outMu.Lock()
		defer outMu.Unlock()
		return out.String()
	}
	t.Cleanup(func() {
		d.stop()
		os.Stdout = oldStdout
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})
	d.stopFn = func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Error(err)
		}
		select {
		case <-d.exit:
		case <-time.After(30 * time.Second):
			t.Errorf("serve did not stop; log:\n%s", d.logged())
		}
		pw.Close()
		<-copied
	}
	go func() {
		d.exit <- cmdServe([]string{"--config", cfgPath, "--dry-run", "--run-dir", rdir, "--state-dir", sdir})
	}()
	if !d.wait("READY", func() bool { return strings.Contains(d.logged(), "first cycle done, READY") }) {
		t.FailNow()
	}
	return d
}

func (d *e2eDaemon) stop() {
	if !d.stopped {
		d.stopped = true
		d.stopFn()
	}
}

func (d *e2eDaemon) wait(what string, cond func() bool) bool {
	d.t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		if cond() {
			return true
		}
		select {
		case code := <-d.exit:
			d.stopped = true
			d.t.Fatalf("serve exited early with %d; log:\n%s", code, d.logged())
		case <-time.After(100 * time.Millisecond):
		}
	}
	d.t.Errorf("%s: timed out; log:\n%s", what, d.logged())
	return false
}

// TestAssistHandEditedEndToEnd (BACKLOG #63) runs the real daemon with a
// config file without [[assist]], appends a table to the file by hand and
// switches it through the socket: the real store writes the line, the real
// controller takes the file's tables and the next snapshot carries the
// table with its thresholds. 0.7.0-rc1 answered 500 "config written, not
// applied" here until a restart.
func TestAssistHandEditedEndToEnd(t *testing.T) {
	d := startE2E(t)
	type state struct {
		Channels []struct {
			Name   string `json:"name"`
			Assist *struct {
				Enabled bool `json:"enabled"`
				Sources []struct {
					Sensor string `json:"sensor"`
					Start  int    `json:"start"`
					Full   int    `json:"full"`
				} `json:"sources"`
			} `json:"assist"`
		} `json:"channels"`
	}
	hddAssist := func() (enabled, has bool, start, full int) {
		var st state
		if err := d.api.get("/api/state", &st); err != nil {
			return
		}
		for _, c := range st.Channels {
			if c.Name == "hdd" && c.Assist != nil && len(c.Assist.Sources) == 1 {
				return c.Assist.Enabled, true, c.Assist.Sources[0].Start, c.Assist.Sources[0].Full
			}
		}
		return
	}
	if _, has, _, _ := hddAssist(); has {
		t.Fatal("assist state before the table exists")
	}
	// the hand edit: a new table, switched off
	f, err := os.OpenFile(d.cfgPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n[[assist]]\nchannel = \"hdd\"\nenabled = false\n\n[[assist.source]]\nsensor = \"k10temp\"\nstart = 50\nfull = 58\nboost = 45\n")
	_ = f.Close()
	var ans map[string]any
	if err := d.api.do(http.MethodPut, "/api/assist/hdd", map[string]any{"enabled": true}, &ans); err != nil {
		t.Errorf("switch after a hand edit: %v (0.7.0-rc1: HTTP 500)", err)
	} else if ans["ok"] != true || ans["enabled"] != true {
		t.Errorf("answer %v", ans)
	}
	d.wait("the table in the snapshot", func() bool { on, has, s, fl := hddAssist(); return has && on && s == 50 && fl == 58 })
	if file, _, err := config.Load(d.cfgPath); err != nil || file.Assist("hdd") == nil || !file.Assist("hdd").Enabled {
		t.Errorf("file: %v", err)
	}
	// the hand edit before the switch is a version of its own, the switch's
	// version is the same text and skipped
	var vs []versionLine
	if err := d.api.get("/api/config/versions", &vs); err != nil || len(vs) != 1 || vs[0].Reason != "external edit" {
		t.Errorf("versions %+v %v", vs, err)
	}
}

// TestConfigVersionsEndToEnd: an editor save through the socket stores the
// text before as a version (by cli, reason editor); during full cooling a
// restore is accepted, writes the old text back, stores the text before it
// ("restore 1") and records the restore event.
func TestConfigVersionsEndToEnd(t *testing.T) {
	d := startE2E(t)
	orig, _ := os.ReadFile(d.cfgPath)
	edited := strings.Replace(string(orig), "critical = 56", "critical = 57", 1)
	if edited == string(orig) {
		edited = string(orig) + "# edited\n"
	}
	req, _ := http.NewRequest(http.MethodPut, d.api.base+"/api/config", strings.NewReader(edited))
	res, err := d.api.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("PUT /api/config: %d", res.StatusCode)
	}
	var vs []versionLine
	if err := d.api.get("/api/config/versions", &vs); err != nil || len(vs) != 1 || vs[0].By != "cli" || vs[0].Reason != "editor" {
		t.Fatalf("versions %+v %v", vs, err)
	}
	var pn map[string]any
	if err := d.api.do(http.MethodPost, "/api/panic", map[string]any{"action": "start"}, &pn); err != nil || pn["active"] != true {
		t.Fatalf("panic: %v %v", pn, err)
	}
	var rs map[string]any
	if err := d.api.do(http.MethodPost, "/api/config/versions/1/restore", nil, &rs); err != nil || rs["ok"] != true {
		t.Fatalf("restore during full cooling: %v %v", rs, err)
	}
	if now, _ := os.ReadFile(d.cfgPath); string(now) != string(orig) {
		t.Errorf("file after restore:\n%s", now)
	}
	if err := d.api.get("/api/config/versions", &vs); err != nil || len(vs) != 2 || vs[0].Reason != "restore 1" || vs[0].By != "cli" {
		t.Errorf("versions after restore %+v %v", vs, err)
	}
	var evs []struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if err := d.api.get("/api/events?minutes=10", &evs); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range evs {
		found = found || e.Kind == "restore" && strings.Contains(e.Text, "version 1")
	}
	if !found {
		t.Errorf("no restore event: %+v", evs)
	}
	_ = d.api.do(http.MethodPost, "/api/panic", map[string]any{"action": "stop"}, nil)
}
