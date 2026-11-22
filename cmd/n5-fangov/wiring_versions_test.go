package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	res, err := fv.Restore(l[0].ID, "")
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
	if _, err := fv.Restore(broken, ""); err == nil || !strings.Contains(err.Error(), "does not parse") {
		t.Errorf("broken version: %v", err)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Error("a refused restore wrote the file")
	}
	if l, _ := s.List(); len(l) != n {
		t.Error("a refused restore stored a version")
	}
	if _, err := fv.Restore(99, ""); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("unknown id: %v", err)
	}
	// a reload that needs a restart is reported, not an error
	fv.reload = func([]byte) error { return errRestartRequired() }
	if res, err := fv.Restore(1, ""); err != nil || !res.RestartRequired {
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
	if err := configRestore(&out, strings.NewReader("y\n"), run, 1, false); err == nil || !strings.Contains(err.Error(), "restore refused") {
		t.Errorf("restore without daemon: %v", err)
	}
	if code := cmdConfig([]string{"diff"}); code != exitUsage {
		t.Errorf("usage: %d", code)
	}
}

// TestRestoreVersionAndConflict: Version names the version the restore
// stored, 0 when it stored none (the text was identical); if_current that
// no longer matches is ErrChanged with nothing written; a version holding
// the password placeholder is refused.
func TestRestoreVersionAndConflict(t *testing.T) {
	cfgPath, s := versionsSetup(t)
	orig, _ := os.ReadFile(cfgPath)
	release := withActor("cli")
	defer release()
	_ = (fileConfigStore{path: cfgPath}).Save(append(append([]byte{}, orig...), "# one\n"...))
	fv := fileVersions{store: s, cfgPath: cfgPath}
	cur, _ := os.ReadFile(cfgPath)
	if _, err := fv.Restore(1, cfgver.Sum([]byte("something else"))); !errors.Is(err, web.ErrChanged) {
		t.Errorf("stale if_current: %v", err)
	}
	if now, _ := os.ReadFile(cfgPath); string(now) != string(cur) {
		t.Error("a refused restore wrote")
	}
	res, err := fv.Restore(1, cfgver.Sum(cur))
	if err != nil || res.Version != 2 || res.SHA != cfgver.Sum(orig) {
		t.Fatalf("restore: %+v %v", res, err)
	}
	// restoring the same text again stores nothing: Version 0, no undo offered
	if res, err := fv.Restore(1, ""); err != nil || res.Version != 0 {
		t.Errorf("identical restore: %+v %v", res, err)
	}
	// a version with the password placeholder (a text pasted from an export)
	_ = os.WriteFile(cfgPath, append(append([]byte{}, orig...), "\n[web]\npassword_hash = \"<unchanged>\"\n"...), 0o600)
	s.Start(cfgPath)
	_ = (fileConfigStore{path: cfgPath}).Save(orig)
	l, _ := s.List()
	var ph int
	for _, v := range l {
		if _, raw, _ := s.Get(v.ID); strings.Contains(string(raw), "<unchanged>") {
			ph = v.ID
		}
	}
	if _, err := fv.Restore(ph, ""); err == nil || !strings.Contains(err.Error(), "placeholder") {
		t.Errorf("placeholder version %d: %v", ph, err)
	}
}

// TestActorConcurrency: a scheduled write (outside a request) while a
// request's write turn is open names "schedule", never the request's
// caller, and does not take over the request's version id.
func TestActorConcurrency(t *testing.T) {
	cfgPath, s := versionsSetup(t)
	orig, _ := os.ReadFile(cfgPath)
	_ = os.WriteFile(cfgPath, append(append([]byte{}, orig...), "# by hand\n"...), 0o600) // a hand edit: the scheduler keeps it
	done := withActor("user:a")
	ch := make(chan error)
	go func() {
		ch <- dirPresetStore{dir: filepath.Join(t.TempDir(), "p"), cfgPath: cfgPath, svc: &fakeService{}, profile: "n5pro", noVersion: true}.Apply("n5pro-cool")
	}()
	if err := <-ch; err != nil {
		t.Fatal(err)
	}
	cur, _ := os.ReadFile(cfgPath)
	if err := (fileConfigStore{path: cfgPath}).Save(append(cur, "# request\n"...)); err != nil {
		t.Fatal(err)
	}
	id, sha := done()
	got := reasons(t, s)
	want := []string{"|external edit", "schedule|preset n5pro-cool", "user:a|editor"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("chain %v, want %v", got, want)
	}
	if l, _ := s.List(); id != l[0].ID || sha == "" {
		t.Errorf("the request's version %d %q, newest %d", id, sha, l[0].ID)
	}
}

// TestCLIConfigRestoreConfirm: `config restore` shows the summary, the
// restart keys, the warnings and the password note, asks y/N (no: nothing
// posted), --yes does not ask; the undo hint names the version the
// restore stored and is left out when it stored none.
func TestCLIConfigRestoreConfirm(t *testing.T) {
	d := startFakeDaemon(t)
	d.stop()
	var posts, stored, conflict atomic.Int32
	stored.Store(4)
	var ifCur atomic.Value
	d.extra = func(mux *http.ServeMux) {
		mux.HandleFunc("GET /api/config/versions/{id}", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"id":3,"ts":1790000000,"by":"cli","reason":"editor","removed":2,"added":1,"changes":["[web]"],"password_changed":true,"restart_keys":["web.auth"],"warnings":["web.auth basic → none: the dashboard needs no sign-in after a restart"],"config_sha256":"abc"}`))
		})
		mux.HandleFunc("POST /api/config/versions/{id}/restore", func(w http.ResponseWriter, r *http.Request) {
			posts.Add(1)
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			ifCur.Store(b["if_current"])
			if conflict.Load() == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(409)
				_, _ = w.Write([]byte(`{"error":"restore: the config changed meanwhile"}`))
				return
			}
			_, _ = fmt.Fprintf(w, `{"ok":true,"restored":3,"version":%d,"restart_keys":["web.auth"],"warnings":[]}`, stored.Load())
		})
	}
	d.start(t)
	var out bytes.Buffer
	if err := configRestore(&out, strings.NewReader("n\n"), d.dir, 3, false); err != nil || posts.Load() != 0 || !strings.Contains(out.String(), "nothing restored") {
		t.Errorf("declined: %v %d\n%s", err, posts.Load(), out.String())
	}
	for _, want := range []string{"2 line(s) go, 1 come in [web]", "applies after restart: web.auth", "needs no sign-in", "resets the web password", "[y/N]"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("prompt lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := configRestore(&out, strings.NewReader(""), d.dir, 3, true); err != nil || posts.Load() != 1 || strings.Contains(out.String(), "[y/N]") || !strings.Contains(out.String(), "config restore 4` undoes it") {
		t.Errorf("--yes: %v %d\n%s", err, posts.Load(), out.String())
	}
	if ifCur.Load() != "abc" {
		t.Errorf("if_current %v, want the summary's hash", ifCur.Load())
	}
	conflict.Store(1)
	out.Reset()
	if err := configRestore(&out, nil, d.dir, 3, true); err == nil || !strings.Contains(err.Error(), "changed after the summary") {
		t.Errorf("409: %v", err)
	}
	conflict.Store(0)
	stored.Store(0)
	out.Reset()
	if err := configRestore(&out, strings.NewReader("y\n"), d.dir, 3, false); err != nil || strings.Contains(out.String(), "undoes it") {
		t.Errorf("nothing stored: %v\n%s", err, out.String())
	}
}

// TestCLIConfigRestoreNoTTY: without --yes and with a standard input that
// is not a terminal the restore is refused (exit 1, nothing asked, nothing
// posted); --yes counts in any position; elsewhere it is a usage error.
func TestCLIConfigRestoreNoTTY(t *testing.T) {
	t.Setenv("N5FANGOV_RUN_DIR", t.TempDir())
	old := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = old })
	_, errOut, code := captureOutput(t, func() int { return cmdConfig([]string{"restore", "3"}) })
	if code != exitFail || !strings.Contains(errOut, "not a terminal") || !strings.Contains(errOut, "--yes") {
		t.Errorf("no tty: %d %q", code, errOut)
	}
	_, errOut, code = captureOutput(t, func() int { return cmdConfig([]string{"--yes", "restore", "3"}) })
	if code != exitFail || strings.Contains(errOut, "not a terminal") || !strings.Contains(errOut, "not running") {
		t.Errorf("--yes first: %d %q", code, errOut)
	}
	if _, _, code := captureOutput(t, func() int { return cmdConfig([]string{"versions", "--yes"}) }); code != exitUsage {
		t.Errorf("--yes on versions: %d", code)
	}
}

// TestTLSWriteModePinsBeforeUnlock: a writer waiting on the config file
// lock while the certificate manager writes its mode (a scheduled preset
// switch, say) pins the new mode, not the old one: writeMode commits the
// pin fields before it releases configFileMu.
func TestTLSWriteModePinsBeforeUnlock(t *testing.T) {
	cfgPath := writeAssistConfig(t)
	m := &tlsManager{cfgPath: cfgPath, mode: "auto", ownsConfig: true}
	done := make(chan struct{})
	tlsWroteHook = func() {
		go func() { // blocks on configFileMu until writeMode returns
			defer close(done)
			configFileMu.Lock()
			defer configFileMu.Unlock()
			raw, _ := os.ReadFile(cfgPath)
			_ = config.Save(cfgPath, m.pinConfig(append(raw, "# a later write\n"...)))
		}()
	}
	t.Cleanup(func() { tlsWroteHook = nil })
	if err := m.writeMode("file", "/etc/n5-fangov/tls/c.pem", "/etc/n5-fangov/tls/k.pem"); err != nil {
		t.Fatal(err)
	}
	<-done
	cfg, _, err := config.Load(cfgPath)
	if err != nil || cfg.Web.TLS != "file" || cfg.Web.CertFile != "/etc/n5-fangov/tls/c.pem" {
		t.Errorf("after the interleaved write: tls %q cert %q (%v)", cfg.Web.TLS, cfg.Web.CertFile, err)
	}
	if raw, _ := os.ReadFile(cfgPath); !strings.Contains(string(raw), "# a later write") {
		t.Error("the interleaved write did not run")
	}
}
