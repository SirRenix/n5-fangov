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

// TestAssistHandEditedEndToEnd (BACKLOG #63) runs the real daemon (dry-run,
// N5 Pro fake sysfs) with a config file without [[assist]], appends a table
// to the file by hand and switches it through the socket: the real store
// writes the line, the real controller takes the file's tables and the
// next snapshot carries the table with its thresholds. 0.7.0-rc1 answered
// 500 "config written, not applied" here until a restart.
func TestAssistHandEditedEndToEnd(t *testing.T) {
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
	stop := func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Error(err)
		}
		select {
		case <-exit:
		case <-time.After(30 * time.Second):
			t.Errorf("serve did not stop; log:\n%s", logged())
		}
		pw.Close()
		<-copied
	}
	wait := func(what string, cond func() bool) bool {
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
			if cond() {
				return true
			}
			select {
			case code := <-exit:
				t.Fatalf("serve exited early with %d; log:\n%s", code, logged())
			case <-time.After(100 * time.Millisecond):
			}
		}
		t.Errorf("%s: timed out; log:\n%s", what, logged())
		return false
	}
	if !wait("READY", func() bool { return strings.Contains(logged(), "first cycle done, READY") }) {
		stop()
		return
	}
	api := newAPI(rdir)
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
		if err := api.get("/api/state", &st); err != nil {
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
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n[[assist]]\nchannel = \"hdd\"\nenabled = false\n\n[[assist.source]]\nsensor = \"k10temp\"\nstart = 50\nfull = 58\nboost = 45\n")
	_ = f.Close()
	var ans map[string]any
	if err := api.do(http.MethodPut, "/api/assist/hdd", map[string]any{"enabled": true}, &ans); err != nil {
		t.Errorf("switch after a hand edit: %v (0.7.0-rc1: HTTP 500)", err)
	} else if ans["ok"] != true || ans["enabled"] != true {
		t.Errorf("answer %v", ans)
	}
	wait("the table in the snapshot", func() bool { on, has, s, fl := hddAssist(); return has && on && s == 50 && fl == 58 })
	if file, _, err := config.Load(cfgPath); err != nil || file.Assist("hdd") == nil || !file.Assist("hdd").Enabled {
		t.Errorf("file: %v", err)
	}
	stop()
}
