package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/sensor"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// v0.3.0-beta: state dir, account store, alert manager, dashboard store,
// preset store with built-ins, reload hook, About.

const v3Config = `# my config
[daemon]
interval = "10s"   # keep

[web]
listen = "127.0.0.1:8010"
auth = "basic"
user = "admin"
password_hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

[[channel]]
name = "cpu"
pwm = 1
sensor = "k10temp"
curve = [[45,85],[80,255]]
critical = 88
`

func writeV3Config(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(v3Config), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStateDir(t *testing.T) {
	t.Setenv(stateDirEnv, "")
	t.Setenv("STATE_DIRECTORY", "")
	if stateDir() != defaultStateDir {
		t.Errorf("default: %s", stateDir())
	}
	t.Setenv("STATE_DIRECTORY", "/var/lib/a:/var/lib/b")
	if stateDir() != "/var/lib/a" {
		t.Errorf("STATE_DIRECTORY first entry: %s", stateDir())
	}
	t.Setenv(stateDirEnv, "/tmp/x")
	if stateDir() != "/tmp/x" {
		t.Errorf("env override: %s", stateDir())
	}
	// ensureStateDir creates the directory 0700; an unwritable one yields ""
	dir := filepath.Join(t.TempDir(), "state")
	if got := ensureStateDir(dir); got != dir {
		t.Errorf("ensure: %q", got)
	}
	if st, err := os.Stat(dir); err != nil || (isUnix() && st.Mode().Perm() != 0o700) {
		t.Errorf("state dir mode: %v %v", err, st)
	}
	if sessionsPath(dir) != filepath.Join(dir, "sessions.json") || alertsPath("") != "" || sessionsPath("") != "" {
		t.Errorf("paths")
	}
	if ensureStateDir("") != "" {
		t.Errorf("empty stays empty")
	}
	file := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(file, nil, 0o600)
	if got := ensureStateDir(filepath.Join(file, "sub")); got != "" {
		t.Errorf("unwritable must yield \"\": %q", got)
	}
}

func isUnix() bool { return os.PathSeparator == '/' }

func TestAccountStore(t *testing.T) {
	cfgPath := writeV3Config(t)
	cfg, _ := parseConfig([]byte(v3Config))
	pinned := 0
	pin := func(raw []byte) []byte { pinned++; return raw }
	s := newAccountStore(cfgPath, webOf(cfg), pin)
	if cur := s.Current(); cur.Mode != "basic" || cur.User != "admin" || !strings.HasPrefix(cur.PasswordHash, "0123") {
		t.Fatalf("current: %+v", cur)
	}
	// "" keeps; nothing written when nothing changes
	if got, err := s.Update("", ""); err != nil || got != s.Current() || pinned != 0 {
		t.Errorf("no-op update: %+v %v pinned=%d", got, err, pinned)
	}
	// a garbage hash is refused before anything is written
	if _, err := s.Update("", "not-a-hash"); err == nil {
		t.Errorf("invalid hash must be refused")
	}
	h := passwordHash("ops", "secret-1")
	got, err := s.Update("ops", h)
	if err != nil || got.User != "ops" || got.PasswordHash != h || got.Mode != "basic" || pinned != 1 {
		t.Fatalf("update: %+v %v pinned=%d", got, err, pinned)
	}
	raw, _ := os.ReadFile(cfgPath)
	txt := string(raw)
	if !strings.Contains(txt, "# my config") || !strings.Contains(txt, `interval = "10s"   # keep`) {
		t.Errorf("comments must survive:\n%s", txt)
	}
	if !strings.Contains(txt, `user = "ops"`) || !strings.Contains(txt, `password_hash = "`+h+`"`) || strings.Contains(txt, "0123456789abcdef") {
		t.Errorf("keys not rewritten:\n%s", txt)
	}
	back, warns := parseConfig(raw)
	if len(warns) != 0 || webOf(back).User != "ops" || webOf(back).Auth != "basic" {
		t.Errorf("re-parse: %v %+v", warns, webOf(back))
	}
	if isUnix() {
		if st, _ := os.Stat(cfgPath); st.Mode().Perm() != 0o600 {
			t.Errorf("config mode %04o", st.Mode().Perm())
		}
	}
	// user only
	if got, err := s.Update("root2", ""); err != nil || got.User != "root2" || got.PasswordHash != h {
		t.Errorf("user-only update: %+v %v", got, err)
	}
}

type fakeWatched struct{ ids []string }

func (f *fakeWatched) SetWatched(ids []string) { f.ids = ids }
func (f *fakeWatched) Watched() []string       { return f.ids }

func TestDashboardStore(t *testing.T) {
	cfgPath := writeV3Config(t)
	ctrl := &fakeWatched{}
	factory := sensorFactory(func(id string) (sensor.Source, error) {
		switch {
		case id == "k10temp" || id == "ec:system":
			return nil, nil
		case strings.HasPrefix(id, "hwmon:") && strings.Count(id, ":") == 2:
			return nil, sensor.ErrNoDevice
		}
		return nil, errors.New("sensor: unknown id")
	})
	s := newDashboardStore(cfgPath, ctrl, factory, nil)
	if got := s.Sensors(); len(got) != 0 {
		t.Errorf("initial: %v", got)
	}
	warns, err := s.SetSensors([]string{" k10temp ", "hwmon:amdgpu:temp1", "k10temp", ""})
	if err != nil || len(warns) != 1 || !strings.Contains(warns[0], "hwmon:amdgpu:temp1") {
		t.Fatalf("set: %v %v", warns, err)
	}
	if !reflect.DeepEqual(ctrl.ids, []string{"k10temp", "hwmon:amdgpu:temp1"}) {
		t.Errorf("controller list: %v", ctrl.ids)
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "[dashboard]\nsensors = [\"k10temp\", \"hwmon:amdgpu:temp1\"]") || !strings.Contains(string(raw), "# my config") {
		t.Errorf("file:\n%s", raw)
	}
	back, pw := parseConfig(raw)
	if len(pw) != 0 || !reflect.DeepEqual(back.Dashboard.Sensors, []string{"k10temp", "hwmon:amdgpu:temp1"}) {
		t.Errorf("re-parse: %v %v", pw, back.Dashboard.Sensors)
	}
	// unknown id form → error, nothing written
	if _, err := s.SetSensors([]string{"bogus"}); err == nil {
		t.Errorf("unparsable id must be refused")
	}
	if _, err := s.SetSensors([]string{"hwmon:x:temp1\"\n"}); err == nil {
		t.Errorf("quote/newline must be refused")
	}
	var many []string
	for i := 0; i < config.MaxDashboardSensors+1; i++ {
		many = append(many, "hwmon:x:temp"+string(rune('1'+i)))
	}
	if _, err := s.SetSensors(many); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("too many: %v", err)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(raw) {
		t.Errorf("refused updates must not write")
	}
	// empty list: sensors = [] and warnings is a non-nil empty slice
	warns, err = s.SetSensors(nil)
	if err != nil || warns == nil || len(warns) != 0 || len(ctrl.ids) != 0 {
		t.Errorf("clear: %v %v %v", warns, err, ctrl.ids)
	}
	raw, _ = os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "sensors = []") {
		t.Errorf("cleared file:\n%s", raw)
	}
	if tomlStringArray([]string{`a"b`, "c"}) != `["a\"b", "c"]` {
		t.Errorf("tomlStringArray: %s", tomlStringArray([]string{`a"b`, "c"}))
	}
}

func TestAlertManagerConfigure(t *testing.T) {
	cfgPath := writeV3Config(t)
	state := t.TempDir()
	m := newAlertManager(cfgPath, alertsPath(state), config.Alert{Transport: "log", MailTo: "root"}, nil)
	m.cooldown = func() time.Duration { return 30 * time.Minute }
	st := m.Status()
	if st.Transport != "log" || st.Effective != "log" || st.Cooldown != "30m0s" || len(st.Kinds) != 11 || st.Template.Path == "" {
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

// fakeService records Reload calls for the preset store and the hook.
type fakeService struct {
	control.Service
	raws [][]byte
	err  error
}

func (f *fakeService) Reload(raw []byte) error {
	f.raws = append(f.raws, raw)
	return f.err
}

func TestHookedServiceReload(t *testing.T) {
	cfgPath := writeV3Config(t)
	m := newAlertManager(cfgPath, "", config.Alert{Transport: "log", MailTo: "root"}, nil)
	inner := &fakeService{}
	svc := hookedService{Service: inner, alerts: m}
	raw := []byte(v3Config + "\n[alert]\ntransport = \"off\"\n")
	if err := svc.Reload(raw); err != nil || len(inner.raws) != 1 {
		t.Fatalf("reload: %v", err)
	}
	if m.sw.Get().Name() != "off" {
		t.Errorf("[alert] must be re-applied after a successful reload: %s", m.sw.Get().Name())
	}
	// restart required: passed through, nothing applied
	inner.err = errRestartRequired()
	if err := svc.Reload([]byte(v3Config)); !isRestartRequired(err) {
		t.Errorf("restart sentinel: %v", err)
	}
	if m.sw.Get().Name() != "off" {
		t.Errorf("202 must not apply [alert]")
	}
}

func TestPresetStoreBuiltins(t *testing.T) {
	cfgPath := writeV3Config(t)
	dir := filepath.Join(t.TempDir(), "presets")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "mine.toml"), []byte(quietPreset), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "n5pro-quiet.toml"), []byte(quietPreset), 0o644) // shadowed
	svc := &fakeService{}
	s := dirPresetStore{dir: dir, cfgPath: cfgPath, svc: svc, profile: "n5pro"}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	builtin := 0
	for _, p := range list {
		names = append(names, p.Name)
		if p.Builtin {
			builtin++
			if p.Description == "" || len(p.Channels) != 3 {
				t.Errorf("built-in %s: %+v", p.Name, p)
			}
		}
	}
	if builtin != 3 || !reflect.DeepEqual(names, []string{"n5pro-balanced", "n5pro-cool", "n5pro-quiet", "mine"}) {
		t.Errorf("list: %v (builtin %d)", names, builtin)
	}
	// other profile: no built-ins
	if l, _ := (dirPresetStore{dir: dir, cfgPath: cfgPath, svc: svc, profile: "nct67xx"}).List(); len(l) != 1 || l[0].Builtin {
		t.Errorf("nct67xx list: %+v", l)
	}
	// apply a built-in: config rewritten, daemon reloaded
	if err := s.Apply("n5pro-balanced"); err != nil || len(svc.raws) != 1 {
		t.Fatalf("apply built-in: %v", err)
	}
	cfg, _, _ := config.Load(cfgPath)
	if len(cfg.Channels) != 3 || cfg.Channel("hdd") == nil || cfg.Channel("hdd").Critical != 60 {
		t.Errorf("applied channels: %+v", cfg.Channels)
	}
	// save over / delete a built-in → ErrPresetBuiltin
	if err := s.Save("n5pro-cool"); !errors.Is(err, web.ErrPresetBuiltin) {
		t.Errorf("save built-in: %v", err)
	}
	if err := s.Delete("n5pro-cool"); !errors.Is(err, web.ErrPresetBuiltin) {
		t.Errorf("delete built-in: %v", err)
	}
	// user preset: save, delete, delete again → ErrNotExist
	if err := s.Save("custom"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("custom"); err != nil {
		t.Errorf("delete: %v", err)
	}
	if err := s.Delete("custom"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("delete missing: %v", err)
	}
	if err := s.Delete("../x"); err == nil {
		t.Errorf("invalid name must be refused")
	}
}

func TestAboutInfo(t *testing.T) {
	a := aboutInfo()
	if a.Name != "n5-fangov" || a.Version == "" || a.License != "GPL-2.0-only" || a.Go == "" || len(a.Credits) != 2 {
		t.Errorf("about: %+v", a)
	}
	if !strings.HasPrefix(a.Repo, "https://github.com/SirRenix/") || a.Author != "SirRenix" || !strings.Contains(a.LicenseURL, "gpl-2.0") {
		t.Errorf("links: %+v", a)
	}
}

func TestPresetDetailAndRename(t *testing.T) {
	dir := t.TempDir()
	chans := config.N5ProChannels()
	if err := config.SavePreset(dir, "winter", chans); err != nil {
		t.Fatal(err)
	}
	s := dirPresetStore{dir: dir, profile: "n5pro"}
	det, err := s.Detail("winter")
	if err != nil || det.Builtin || len(det.Channels) != len(chans) || det.Channels[0].Sensor != chans[0].Sensor || len(det.Channels[0].Curve) == 0 {
		t.Fatalf("detail: %+v %v", det, err)
	}
	if det, err := s.Detail("n5pro-balanced"); err != nil || !det.Builtin || det.Description == "" || len(det.Channels) != 3 {
		t.Fatalf("builtin detail: %+v %v", det, err)
	}
	if _, err := s.Detail("nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing detail: %v", err)
	}
	if err := s.Rename("winter", "n5pro-quiet"); !errors.Is(err, web.ErrPresetBuiltin) {
		t.Fatalf("rename onto builtin: %v", err)
	}
	if err := s.Rename("nope", "x"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rename missing: %v", err)
	}
	if err := config.SavePreset(dir, "summer", chans); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("winter", "summer"); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("rename onto existing: %v", err)
	}
	if err := s.Rename("winter", "cold"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(presetPath(dir, "cold")); err != nil {
		t.Fatal("renamed file missing")
	}
	if _, err := os.Stat(presetPath(dir, "winter")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("old file still there")
	}
}
