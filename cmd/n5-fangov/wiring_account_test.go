package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/sensor"
)

// storeConfig is the config text the store tests start from: comments and
// an inline comment that must survive every in-place edit.
const storeConfig = `# my config
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

func writeStoreConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(storeConfig), 0o600); err != nil {
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
	cfgPath := writeStoreConfig(t)
	cfg, _ := parseConfig([]byte(storeConfig))
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

// TestAccountStoreReadsFile: a [web] change that bypassed the store
// (PUT /api/config, import) is the base of the next Update; s.cur follows
// what was written.
func TestAccountStoreReadsFile(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	cfg, _ := parseConfig([]byte(storeConfig))
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

// TestStoresDottedLayout: the three single-key stores edit a
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

// TestStoresInlineTableRefused: an inline table cannot be edited
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

// TestConfigWritersSerialised: the account store, the alert
// manager, the dashboard store and a preset apply rewrite the file
// concurrently; every one of their changes is in the final file.
func TestConfigWritersSerialised(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	cfg, _ := parseConfig([]byte(storeConfig))
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
