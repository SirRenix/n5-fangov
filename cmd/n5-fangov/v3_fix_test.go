package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/alert"
	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/sensor"
)

// v0.3.0-beta review fixes: template probe cache (M2), serialised config
// writers (L4), account store reads the file (L5), built-in presets per
// profile (L8), bounded single test delivery (L9), dotted / inline [web]
// layouts (L11).

// TestAlertTemplateProbeCache (R-M2): Status reuses the probe for ten
// minutes; InstallTemplate and Configure drop the cache.
func TestAlertTemplateProbeCache(t *testing.T) {
	cfgPath := writeV3Config(t)
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

// TestAlertTestBusyAndBounded (R-L9): a second test while one runs answers
// ErrTestBusy; a delivery that hangs is cut at testLimit.
func TestAlertTestBusyAndBounded(t *testing.T) {
	cfgPath := writeV3Config(t)
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

// TestAccountStoreReadsFile (R-L5): a [web] change that bypassed the store
// (PUT /api/config, import) is the base of the next Update; s.cur follows
// what was written.
func TestAccountStoreReadsFile(t *testing.T) {
	cfgPath := writeV3Config(t)
	cfg, _ := parseConfig([]byte(v3Config))
	s := newAccountStore(cfgPath, webOf(cfg), nil)
	// the editor renamed the user meanwhile
	raw, _ := os.ReadFile(cfgPath)
	if err := saveConfig(cfgPath, setConfigKey(raw, "web", "user", tomlString("editor"))); err != nil {
		t.Fatal(err)
	}
	h := passwordHash("editor", "secret-1")
	got, err := s.Update("", h)
	if err != nil || got.User != "editor" || got.PasswordHash != h {
		t.Fatalf("update after external rename: %+v %v", got, err)
	}
	if cur := s.Current(); cur != got {
		t.Errorf("Current = %+v, want %+v", cur, got)
	}
	back, _, _ := config.Load(cfgPath)
	if back.Web.User != "editor" || back.Web.PasswordHash != h {
		t.Errorf("file: %+v", back.Web)
	}
	// and the other way round: the editor replaced the hash, a rename keeps that one
	raw, _ = os.ReadFile(cfgPath)
	h2 := passwordHash("editor", "secret-2")
	_ = saveConfig(cfgPath, setConfigKey(raw, "web", "password_hash", tomlString(h2)))
	if got, err := s.Update("ops", ""); err != nil || got.User != "ops" || got.PasswordHash != h2 {
		t.Errorf("rename after external hash change: %+v %v", got, err)
	}
}

const dottedConfig = `# dotted layout
daemon.interval = "10s"
web.listen = "127.0.0.1:8010"
web.auth = "basic"
web.user = "admin"
web.password_hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
alert.transport = "log"

[[channel]]
name = "cpu"
pwm = 1
sensor = "k10temp"
curve = [[45,85],[80,255]]
critical = 88
`

const inlineConfig = `web = { listen = "127.0.0.1:8010", auth = "basic", user = "admin", password_hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" }
alert = { transport = "log" }
dashboard = { sensors = [] }

[[channel]]
name = "cpu"
pwm = 1
sensor = "k10temp"
curve = [[45,85],[80,255]]
critical = 88
`

func writeConfigText(t *testing.T, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestStoresDottedLayout (R-L11): the three single-key stores edit a
// dotted top-level layout in place; the result parses.
func TestStoresDottedLayout(t *testing.T) {
	cfgPath := writeConfigText(t, dottedConfig)
	cfg, warns := parseConfig([]byte(dottedConfig))
	if len(warns) != 0 {
		t.Fatalf("fixture: %v", warns)
	}
	acc := newAccountStore(cfgPath, webOf(cfg), nil)
	h := passwordHash("ops", "secret-1")
	if got, err := acc.Update("ops", h); err != nil || got.User != "ops" {
		t.Fatalf("account on dotted: %+v %v", got, err)
	}
	m := newAlertManager(cfgPath, "", cfgAlert(cfg), nil)
	if st, err := m.Configure("mail", "ops"); err != nil || st.Transport != "mail" || st.MailTo != "ops" {
		t.Fatalf("alert on dotted: %+v %v", st, err)
	}
	d := newDashboardStore(cfgPath, &fakeWatched{}, func(string) (sensor.Source, error) { return nil, nil }, nil)
	if _, err := d.SetSensors([]string{"k10temp"}); err != nil {
		t.Fatalf("dashboard on dotted: %v", err)
	}
	raw, _ := os.ReadFile(cfgPath)
	txt := string(raw)
	if strings.Contains(txt, "[web]") || strings.Contains(txt, "[alert]") || !strings.Contains(txt, "# dotted layout") || !strings.Contains(txt, "web.user = \"ops\"") || !strings.Contains(txt, "alert.mail_to = \"ops\"") || !strings.Contains(txt, "[dashboard]\nsensors = [\"k10temp\"]") {
		t.Errorf("file:\n%s", txt)
	}
	back, pw, err := config.Parse(raw)
	if err != nil || len(pw) != 0 || back.Web.User != "ops" || back.Web.PasswordHash != h || back.Alert.Transport != "mail" || back.Alert.MailTo != "ops" || len(back.Dashboard.Sensors) != 1 || len(back.Channels) != 1 {
		t.Errorf("re-parse: %v %v %+v %+v", err, pw, back.Web, back.Alert)
	}
}

// TestStoresInlineTableRefused (R-L11): an inline table cannot be edited
// in place; the stores say so and leave the file alone.
func TestStoresInlineTableRefused(t *testing.T) {
	cfgPath := writeConfigText(t, inlineConfig)
	cfg, _, err := config.Parse([]byte(inlineConfig))
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	before, _ := os.ReadFile(cfgPath)
	acc := newAccountStore(cfgPath, webOf(cfg), nil)
	if _, err := acc.Update("ops", passwordHash("ops", "secret-1")); err == nil || !strings.Contains(err.Error(), "inline [web] table") || !strings.Contains(err.Error(), "edit the file by hand") {
		t.Errorf("account on inline: %v", err)
	}
	if acc.Current().User != "admin" {
		t.Errorf("cur changed on refusal: %+v", acc.Current())
	}
	m := newAlertManager(cfgPath, "", cfgAlert(cfg), nil)
	if _, err := m.Configure("off", ""); err == nil || !strings.Contains(err.Error(), "inline [alert] table") {
		t.Errorf("alert on inline: %v", err)
	}
	if m.sw.Get().Name() != "log" {
		t.Errorf("sink swapped on refusal: %s", m.sw.Get().Name())
	}
	d := newDashboardStore(cfgPath, &fakeWatched{}, func(string) (sensor.Source, error) { return nil, nil }, nil)
	if _, err := d.SetSensors([]string{"k10temp"}); err == nil || !strings.Contains(err.Error(), "inline [dashboard] table") {
		t.Errorf("dashboard on inline: %v", err)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Errorf("file changed on refusal:\n%s", after)
	}
}

// TestPresetBuiltinOtherProfile (R-L8): a built-in of another profile is
// not applied (fs.ErrNotExist → 404), on the active profile it is.
func TestPresetBuiltinOtherProfile(t *testing.T) {
	cfgPath := writeV3Config(t)
	dir := filepath.Join(t.TempDir(), "presets")
	svc := &fakeService{}
	for _, prof := range []string{"nct67xx", "it87xx", "monitor", ""} {
		s := dirPresetStore{dir: dir, cfgPath: cfgPath, svc: svc, profile: prof}
		if err := s.Apply("n5pro-quiet"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("profile %q: %v", prof, err)
		}
	}
	if len(svc.raws) != 0 {
		t.Fatal("a refused apply must not reload")
	}
	// a user file with a built-in name is shadowed (not applied) on every profile
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "n5pro-quiet.toml"), []byte(quietPreset), 0o644)
	if err := (dirPresetStore{dir: dir, cfgPath: cfgPath, svc: svc, profile: "nct67xx"}).Apply("n5pro-quiet"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("shadowed file on other profile: %v", err)
	}
	if err := (dirPresetStore{dir: dir, cfgPath: cfgPath, svc: svc, profile: "n5pro"}).Apply("n5pro-quiet"); err != nil || len(svc.raws) != 1 {
		t.Errorf("active profile: %v", err)
	}
	if cfg, _, _ := config.Load(cfgPath); cfg.Channel("hdd") == nil || cfg.Channel("hdd").Critical != 66 {
		t.Errorf("the built-in, not the shadowed file, was applied: %+v", cfg.Channels)
	}
}

// TestConfigWritersSerialised (R-L4): the account store, the alert
// manager, the dashboard store and a preset apply rewrite the file
// concurrently; every one of their changes is in the final file.
func TestConfigWritersSerialised(t *testing.T) {
	cfgPath := writeV3Config(t)
	cfg, _ := parseConfig([]byte(v3Config))
	acc := newAccountStore(cfgPath, webOf(cfg), nil)
	m := newAlertManager(cfgPath, "", cfgAlert(cfg), nil)
	d := newDashboardStore(cfgPath, &fakeWatched{}, func(string) (sensor.Source, error) { return nil, nil }, nil)
	p := dirPresetStore{dir: filepath.Join(t.TempDir(), "presets"), cfgPath: cfgPath, svc: &fakeService{}, profile: "n5pro"}
	const rounds = 15
	var wg sync.WaitGroup
	errc := make(chan error, 4*rounds)
	for i := 0; i < rounds; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			if _, err := acc.Update(fmt.Sprintf("user%d", i), ""); err != nil {
				errc <- err
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := m.Configure("log", fmt.Sprintf("ops%d", i)); err != nil {
				errc <- err
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := d.SetSensors([]string{"k10temp"}); err != nil {
				errc <- err
			}
		}()
		go func() {
			defer wg.Done()
			if err := p.Apply("n5pro-quiet"); err != nil {
				errc <- err
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Errorf("writer: %v", err)
	}
	final, warns, err := config.Load(cfgPath)
	if err != nil || len(warns) != 0 {
		t.Fatalf("final file: %v %v", err, warns)
	}
	if !strings.HasPrefix(final.Web.User, "user") || final.Alert.Transport != "log" || !strings.HasPrefix(final.Alert.MailTo, "ops") || len(final.Dashboard.Sensors) != 1 || len(final.Channels) != 3 {
		t.Errorf("lost update: web=%+v alert=%+v dashboard=%v channels=%d", final.Web, final.Alert, final.Dashboard.Sensors, len(final.Channels))
	}
	if acc.Current().User != final.Web.User {
		t.Errorf("account store %q, file %q", acc.Current().User, final.Web.User)
	}
}
