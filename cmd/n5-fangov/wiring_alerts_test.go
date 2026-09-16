package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/alert"
	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
)

func TestAlertManagerConfigure(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	state := t.TempDir()
	m := newAlertManager(cfgPath, alertsPath(state), config.Alert{Transport: "log", MailTo: "root"}, nil)
	m.cooldown = func() time.Duration { return 30 * time.Minute }
	st := m.Status()
	if st.Transport != "log" || st.Effective != "log" || st.Cooldown != "30m0s" || len(st.Kinds) != len(alertKinds) || st.Template.Path == "" {
		t.Errorf("status: %+v", st)
	}
	kinds := map[string]bool{}
	for _, k := range st.Kinds {
		kinds[k.Kind] = true
		if k.Description == "" {
			t.Errorf("kind %s without description", k.Kind)
		}
	}
	for _, k := range []string{"sensor", "stall", "temp", "write", "config", "config-channels", "restart", "failed", "kernel", "tls", "test"} {
		if !kinds[k] {
			t.Errorf("kind %s missing", k)
		}
	}
	// test alert lands in the ring and its mirror
	eff, err := m.Test()
	if err != nil || eff != "log" {
		t.Errorf("test: %s %v", eff, err)
	}
	rec := m.Recent(5)
	if len(rec) != 1 || rec[0].Kind != "test" || !strings.Contains(rec[0].Msg, "test alert") || m.Last()["test"] == 0 {
		t.Errorf("recent: %+v", rec)
	}
	if _, err := os.Stat(alertsPath(state)); err != nil {
		t.Errorf("mirror file: %v", err)
	}
	// the sink serve/controller use is the ring: an alert through it is recorded
	m.sink().Alert("stall", "fan stopped")
	if rec := m.Recent(1); len(rec) != 1 || rec[0].Kind != "stall" {
		t.Errorf("ring wraps the sink: %+v", rec)
	}
	// Configure: validation
	if _, err := m.Configure("pigeon", ""); err == nil {
		t.Errorf("unknown transport must be refused")
	}
	if _, err := m.Configure("mail", "two words"); err == nil {
		t.Errorf("bad mail_to must be refused")
	}
	// Configure: off is written, hot-applied, comments kept
	st, err = m.Configure(" OFF ", "")
	if err != nil || st.Transport != "off" || st.Effective != "off" || st.MailTo != "root" {
		t.Fatalf("configure off: %+v %v", st, err)
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "[alert]\ntransport = \"off\"\nmail_to = \"root\"") || !strings.Contains(string(raw), "# my config") {
		t.Errorf("file:\n%s", raw)
	}
	if m.sw.Get().Name() != "off" {
		t.Errorf("sink not swapped: %s", m.sw.Get().Name())
	}
	// apply with the same values is a no-op, with new ones a swap
	if m.apply(config.Alert{Transport: "off", MailTo: "root"}) != "off" || m.apply(config.Alert{Transport: "log", MailTo: "root"}) != "log" {
		t.Errorf("apply")
	}
	if m.sw.Get().Name() != "log" {
		t.Errorf("apply must swap: %s", m.sw.Get().Name())
	}
	// a fresh manager on the mirror file sees the history
	m2 := newAlertManager(cfgPath, alertsPath(state), config.Alert{Transport: "log"}, nil)
	if got := m2.Recent(0); len(got) != 2 || got[0].Kind != "stall" {
		t.Errorf("history reload: %+v", got)
	}
}

// TestAlertTemplateProbeCache: Status reuses the probe for ten
// minutes; InstallTemplate and Configure drop the cache.
func TestAlertTemplateProbeCache(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	m := newAlertManager(cfgPath, "", config.Alert{Transport: "log", MailTo: "root"}, nil)
	now := time.Unix(1789500000, 0)
	m.now = func() time.Time { return now }
	probes := 0
	m.probe = func() (bool, bool, bool, string) {
		probes++
		return true, probes%2 == 1, false, fmt.Sprintf("probe %d", probes)
	}
	for i := 0; i < 3; i++ {
		if st := m.Status(); st.Template.Reason != "probe 1" || !st.Template.Installed || !st.Template.Current || st.Template.Path != alert.TemplatePath {
			t.Errorf("call %d: %+v", i, st.Template)
		}
	}
	if probes != 1 {
		t.Fatalf("probes after three Status calls: %d", probes)
	}
	now = now.Add(templateProbeEvery - time.Second)
	m.Status()
	if probes != 1 {
		t.Errorf("re-probed before the interval: %d", probes)
	}
	now = now.Add(time.Second)
	if st := m.Status(); probes != 2 || st.Template.Reason != "probe 2" || st.Template.Current {
		t.Errorf("after the interval: probes=%d %+v", probes, st.Template)
	}
	// InstallTemplate invalidates, also when it fails (no PVE here)
	_, _ = m.InstallTemplate()
	m.Status()
	if probes != 3 {
		t.Errorf("after InstallTemplate: %d", probes)
	}
	// Configure invalidates; its own Status re-probes once
	if _, err := m.Configure("log", "root"); err != nil {
		t.Fatal(err)
	}
	if probes != 4 {
		t.Errorf("after Configure: %d", probes)
	}
	m.Status()
	if probes != 4 {
		t.Errorf("cached again after Configure: %d", probes)
	}
	// a clock that went backwards re-probes rather than caching forever
	now = now.Add(-time.Hour)
	m.Status()
	if probes != 5 {
		t.Errorf("clock skew: %d", probes)
	}
}

// gate is a ContextSender that blocks until released or the context ends.
type gate struct {
	alert.Log
	started chan struct{}
	release chan struct{}
}

func (g *gate) Name() string { return "gate" }

func (g *gate) SendCtx(ctx context.Context, kind, msg string) error {
	close(g.started)
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestAlertTestBusyAndBounded: a second test while one runs answers
// ErrTestBusy; a delivery that hangs is cut at testLimit.
func TestAlertTestBusyAndBounded(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	m := newAlertManager(cfgPath, "", config.Alert{Transport: "log", MailTo: "root"}, nil)
	g := &gate{Log: alert.Log{Logger: m.logger}, started: make(chan struct{}), release: make(chan struct{})}
	m.sw.Set(g)
	done := make(chan error, 1)
	go func() { _, err := m.Test(); done <- err }()
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first test never reached the sink")
	}
	if eff, err := m.Test(); !errors.Is(err, alert.ErrTestBusy) || eff != "log" {
		t.Errorf("second test: %s %v", eff, err)
	}
	close(g.release)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("first test: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first test did not finish")
	}
	// free again, and bounded: a sink that never returns is cut at testLimit
	m.testLimit = 50 * time.Millisecond
	g2 := &gate{Log: alert.Log{Logger: m.logger}, started: make(chan struct{}), release: make(chan struct{})}
	m.sw.Set(g2)
	start := time.Now()
	if _, err := m.Test(); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Errorf("bounded test: %v after %s", err, time.Since(start))
	}
	if rec := m.Recent(0); len(rec) != 2 || rec[0].Kind != "test" {
		t.Errorf("both tests must be in the history: %+v", rec)
	}
}

// TestAlertKindsComplete: every alert kind raised anywhere in the code
// (controller raise, serve's start-up alerts, the onfailure/apt-hook
// paths) has an entry in alertKinds, and alertKinds names nothing else.
func TestAlertKindsComplete(t *testing.T) {
	listed := map[string]bool{}
	for _, k := range alertKinds {
		listed[k.Kind] = true
	}
	raised := map[string]bool{control.AlertConfigChannels: true, "restart": true, "failed": true} // the onfailure unit passes these as argv
	call := regexp.MustCompile(`\b(?:raise|sendAlertCooled|startAlert|sendAlert)\((?:[^,"\n]+,\s*)*"([a-z-]+)"`)
	for _, dir := range []string{".", "../../internal/control"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			src, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range call.FindAllStringSubmatch(string(src), -1) {
				raised[m[1]] = true
			}
		}
	}
	if len(raised) < 10 {
		t.Fatalf("only %d raised kinds found, the scan is broken: %v", len(raised), raised)
	}
	for k := range raised {
		if !listed[k] {
			t.Errorf("kind %q is raised but missing from alertKinds", k)
		}
	}
	for k := range listed {
		if !raised[k] && k != "test" {
			t.Errorf("alertKinds lists %q, which nothing raises", k)
		}
	}
}

// TestAlertStatusCooldownSeconds: the panel gets the cooldown as seconds
// next to the Go duration text.
func TestAlertStatusCooldownSeconds(t *testing.T) {
	m := newAlertManager(filepath.Join(t.TempDir(), "config.toml"), "", config.Alert{Transport: "log", MailTo: "root"}, nil)
	if st := m.Status(); st.Cooldown != "" || st.CooldownS != 0 {
		t.Errorf("offline: %q %d", st.Cooldown, st.CooldownS)
	}
	m.cooldown = func() time.Duration { return 30 * time.Minute }
	if st := m.Status(); st.Cooldown != "30m0s" || st.CooldownS != 1800 {
		t.Errorf("online: %q %d", st.Cooldown, st.CooldownS)
	}
}
