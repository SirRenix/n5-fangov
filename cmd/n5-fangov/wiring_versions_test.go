package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/cfgver"
	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// versionsSetup writes the assist test config and opens a version store
// as serve does (the store is the package's; restored afterwards).
func versionsSetup(t *testing.T) (string, *cfgver.Store) {
	t.Helper()
	cfgPath := writeAssistConfig(t)
	s := cfgver.Open(filepath.Join(t.TempDir(), configVersionsDir), t.Logf)
	s.Start(cfgPath)
	cfgVersions.Store(s)
	t.Cleanup(func() { cfgVersions.Store(nil) })
	return cfgPath, s
}

func reasons(t *testing.T, s *cfgver.Store) []string {
	t.Helper()
	l, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(l) - 1; i >= 0; i-- {
		out = append(out, l[i].By+"|"+l[i].Reason)
	}
	return out
}

// TestConfigVersionWritePaths (draft "Tests"): every write path of the
// daemon stores exactly one version, named with the caller and the
// reason; the scheduler's preset switch stores none; a write that does
// not change the text before stores none either.
func TestConfigVersionWritePaths(t *testing.T) {
	cfgPath, s := versionsSetup(t)
	release := withActor("user:admin")
	defer release()
	step := func(what string, fn func() error) {
		t.Helper()
		if err := fn(); err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	raw, _ := os.ReadFile(cfgPath)
	step("editor", func() error {
		return fileConfigStore{path: cfgPath}.Save(bytes.Replace(raw, []byte("critical = 56"), []byte("critical = 57"), 1))
	})
	presetDir := filepath.Join(t.TempDir(), "presets")
	svc := &fakeService{}
	step("preset", func() error {
		return dirPresetStore{dir: presetDir, cfgPath: cfgPath, svc: svc, profile: "n5pro"}.Apply("n5pro-quiet")
	})
	step("scheduler", func() error {
		return dirPresetStore{dir: presetDir, cfgPath: cfgPath, svc: svc, profile: "n5pro", noVersion: true}.Apply("n5pro-cool")
	})
	step("assist", func() error { return newAssistStore(cfgPath, &fakeAssistCtrl{}, nil).SetEnabled("disks", true) })
	step("account", func() error {
		_, err := newAccountStore(cfgPath, webSpec{Auth: "basic", User: "admin"}, nil).Update("admin", web.PasswordHash("admin", "secret123"))
		return err
	})
	step("tls", func() error { return (&tlsManager{cfgPath: cfgPath}).writeMode("auto", "", "") })
	b := fileBundle{cfgPath: cfgPath, presetDir: presetDir}
	exp, err := b.Export()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(exp, &doc)
	doc["config"] = doc["config"].(string) + "# imported\n"
	imp, _ := json.Marshal(doc)
	step("import", func() error { _, _, err := b.Import(imp); return err })
	// the same text again: written, no version
	cur, _ := os.ReadFile(cfgPath)
	step("unchanged", func() error { return fileConfigStore{path: cfgPath}.Save(cur) })
	step("unchanged again", func() error { return fileConfigStore{path: cfgPath}.Save(cur) })
	got := reasons(t, s)
	want := []string{"user:admin|editor", "user:admin|preset n5pro-quiet", "user:admin|assist disks on", "user:admin|account", "user:admin|tls", "user:admin|import"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("versions\n got %v\nwant %v", got, want)
	}
	// the scheduler's write is known: no "external edit" at the next start
	s.Start(cfgPath)
	if l := reasons(t, s); len(l) > len(want) {
		t.Errorf("start after daemon writes stored %v", l[len(want):])
	}
	// without a store (setup, CLI) nothing is recorded and nothing fails
	cfgVersions.Store(nil)
	if err := saveConfigWhy(cfgPath, cur, "editor"); err != nil {
		t.Fatal(err)
	}
}

// TestConfigVersionRestore: a valid version is written (pinned, reloaded)
// and the text before becomes a version "restore N"; restart-only keys
// are reported; a version that does not parse is refused with nothing
// written; an unknown id is fs.ErrNotExist.
func TestConfigVersionRestore(t *testing.T) {
	cfgPath, s := versionsSetup(t)
	release := withActor("cli")
	orig, _ := os.ReadFile(cfgPath)
	changed := strings.Replace(string(orig), "interval = \"10s\"", "interval = \"10s\"\n\n[web]\nlisten = \"0.0.0.0:8010\"", 1)
	if err := (fileConfigStore{path: cfgPath}).Save([]byte(changed)); err != nil {
		t.Fatal(err)
	}
	var reloaded [][]byte
	fv := fileVersions{store: s, cfgPath: cfgPath, pin: func(b []byte) []byte { return append(b, []byte("# pinned\n")...) },
		reload: func(b []byte) error { reloaded = append(reloaded, b); return nil }}
	l, _ := s.List()
	res, err := fv.Restore(l[0].ID)
	release()
	if err != nil {
		t.Fatal(err)
	}
	now, _ := os.ReadFile(cfgPath)
	if string(now) != string(orig)+"# pinned\n" || len(reloaded) != 1 {
		t.Errorf("file after restore:\n%s", now)
	}
	if strings.Join(res.RestartKeys, ",") != "web.listen" || res.RestartRequired {
		t.Errorf("result %+v", res)
	}
	got := reasons(t, s)
	if len(got) != 2 || got[1] != "cli|restore 1" || res.Version != 2 {
		t.Errorf("versions %v, result version %d", got, res.Version)
	}
	if _, raw, _ := s.Get(2); string(raw) != changed {
		t.Errorf("the text before the restore was not kept")
	}
	// a hand edit that does not parse becomes a version; restoring it is refused
	if err := os.WriteFile(cfgPath, []byte("[daemon\nbroken"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.Start(cfgPath)
	if err := os.WriteFile(cfgPath, now, 0o600); err != nil {
		t.Fatal(err)
	}
	s.Start(cfgPath)
	l, _ = s.List()
	var broken int
	for _, v := range l {
		if _, raw, _ := s.Get(v.ID); strings.HasPrefix(string(raw), "[daemon\n") {
			broken = v.ID
		}
	}
	before, _ := os.ReadFile(cfgPath)
	n := len(l)
	if _, err := fv.Restore(broken); err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("broken version: %v", err)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Error("a refused restore wrote the file")
	}
	if l, _ := s.List(); len(l) != n {
		t.Error("a refused restore stored a version")
	}
	if _, err := fv.Restore(99); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("unknown id: %v", err)
	}
	// a reload that needs a restart is reported, not an error
	fv.reload = func([]byte) error { return errRestartRequired() }
	if res, err := fv.Restore(1); err != nil || !res.RestartRequired {
		t.Errorf("restart required: %+v %v", res, err)
	}
}

func TestRestartKeys(t *testing.T) {
	a, _, _ := config.Parse([]byte("[web]\nlisten = \"127.0.0.1:8010\"\nauth = \"none\"\ntls = \"off\"\n"))
	b, _, _ := config.Parse([]byte("[web]\nlisten = \"0.0.0.0:8010\"\nauth = \"none\"\ntls = \"auto\"\n[log]\nfile = \"\"\n[daemon]\nprofile = \"monitor\"\n"))
	got := strings.Join(restartKeys(a, b), ",")
	if !strings.Contains(got, "web.listen") || !strings.Contains(got, "log.file") || !strings.Contains(got, "daemon.profile") || strings.Contains(got, "web.tls") {
		t.Errorf("restart keys %s", got)
	}
}

// TestConfigCLIOffline: without the daemon `config versions` and `config
// diff` read the directory (hash redacted), `config restore` is refused.
func TestConfigCLIOffline(t *testing.T) {
	cfgPath, s := versionsSetup(t)
	hash := web.PasswordHash("admin", "secret123")
	raw, _ := os.ReadFile(cfgPath)
	withHash := strings.Replace(string(raw), "[daemon]", "[web]\nuser = \"admin\"\npassword_hash = \""+hash+"\"\n\n[daemon]", 1)
	if err := (fileConfigStore{path: cfgPath}).Save([]byte(withHash)); err != nil {
		t.Fatal(err)
	}
	if err := (fileConfigStore{path: cfgPath}).Save([]byte(strings.Replace(withHash, hash, web.PasswordHash("admin", "other1234"), 1))); err != nil {
		t.Fatal(err)
	}
	state := filepath.Dir(s.Dir())
	run := t.TempDir() // no socket: no daemon
	var out bytes.Buffer
	if err := configVersions(&out, run, state, cfgPath); err != nil || !strings.Contains(out.String(), "2 version(s)") || !strings.Contains(out.String(), "daemon not running") {
		t.Errorf("versions: %v\n%s", err, out.String())
	}
	out.Reset()
	if err := configDiff(&out, run, state, cfgPath, 2); err != nil || strings.Contains(out.String(), "pbkdf2") || !strings.Contains(out.String(), "<redacted, changed>") || !strings.Contains(out.String(), "resets the password") {
		t.Errorf("diff: %v\n%s", err, out.String())
	}
	if err := configRestore(&out, run, 1); err == nil || !strings.Contains(err.Error(), "restore refused") {
		t.Errorf("restore without daemon: %v", err)
	}
	if code := cmdConfig([]string{"diff"}); code != exitUsage {
		t.Errorf("usage: %d", code)
	}
}
