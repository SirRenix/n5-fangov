package main

// Regression tests for the second v0.4 review round on the cmd side: the
// start-up alert stamp written before the delivery goroutine (L5), the
// placeholder in an inline [web] table refused by the bundle import (L6)
// and the preset apply that was written but not reloaded (L7).

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// blockSink parks every delivery until released and counts them.
type blockSink struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockSink) Alert(kind, _ string) { b.entered <- struct{}{}; <-b.release }
func (b *blockSink) Name() string         { return "block" }

// TestStartAlertStampBeforeDelivery (L5): startAlert writes the cooldown
// stamp before it returns, while the delivery is still in flight — an
// early exit of serve right after the call cannot lose it, and a second
// alert of the kind is suppressed at once.
func TestStartAlertStampBeforeDelivery(t *testing.T) {
	dir := t.TempDir()
	s := &blockSink{entered: make(chan struct{}, 2), release: make(chan struct{})}
	defer close(s.release)
	startAlert(dir, s, "config", "first")
	b, err := os.ReadFile(filepath.Join(dir, "alert.config"))
	if err != nil {
		t.Fatalf("stamp not written before startAlert returned: %v", err)
	}
	if strings.TrimSpace(string(b)) == "" {
		t.Fatalf("empty stamp")
	}
	select {
	case <-s.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("delivery never started")
	}
	// the sink is still parked: the second alert of the kind is suppressed
	// by the stamp, not by anything the delivery did
	startAlert(dir, s, "config", "second")
	sendAlertCooled(dir, s, "config", "third")
	select {
	case <-s.entered:
		t.Fatal("suppressed alert delivered")
	case <-time.After(50 * time.Millisecond):
	}
	// another kind goes out
	startAlert(dir, s, "profile", "other")
	select {
	case <-s.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("other kind not delivered")
	}
}

// TestBundleImportInlinePlaceholder (L6): a bundle whose config carries
// the <unchanged> placeholder in an inline table — where restoreHash does
// not reach — is refused (400 over the API) instead of written with the
// placeholder as the stored hash.
func TestBundleImportInlinePlaceholder(t *testing.T) {
	cfgPath, presetDir := bundleDirs(t)
	before, _ := os.ReadFile(cfgPath)
	reloads := 0
	b := fileBundle{cfgPath: cfgPath, presetDir: presetDir, reload: func([]byte) error { reloads++; return nil }}
	mk := func(cfg string) []byte {
		d, _ := json.Marshal(settingsBundle{Format: bundleFormat, Config: cfg, Presets: map[string]string{}})
		return d
	}
	inline := "web = { auth = \"basic\", user = \"admin\", password_hash = \"" + redactedHash + "\" }\n"
	_, _, err := b.Import(mk(inline))
	var be *bundleError
	if !errors.As(err, &be) || len(be.Errors()) != 1 || !strings.Contains(be.Errors()[0], "password_hash "+redactedHash+" not restored") {
		t.Fatalf("inline placeholder: %v", err)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) || reloads != 0 {
		t.Fatalf("refused bundle changed the config (reloads %d)", reloads)
	}
	// over the API: 400 with the line under "errors"
	srv := web.New(web.Deps{Bundle: b, Logf: func(string, ...any) {}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/config/import", strings.NewReader(string(mk(inline))))
	req.Header.Set(web.CSRFHeader, "1")
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 400 || !strings.Contains(string(raw), "import rejected: config: password_hash") {
		t.Fatalf("API: %d %s", res.StatusCode, raw)
	}
	// the line form restores the stored hash and is imported
	if _, _, err := b.Import(mk("[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \"" + redactedHash + "\"\n")); err != nil {
		t.Fatalf("line form: %v", err)
	}
	cfg, _, err := config.Load(cfgPath)
	if err != nil || cfg.Web.PasswordHash != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" || reloads != 1 {
		t.Fatalf("line form not restored: %+v %v (reloads %d)", cfg.Web, err, reloads)
	}
}

// TestPresetApplyReloadError (L7): a preset that is written but not taken
// by the daemon comes back as web.ReloadError with the "written, reload
// failed" text — the file carries the preset; the restart sentinel passes
// through unchanged.
func TestPresetApplyReloadError(t *testing.T) {
	cfgPath := writeV3Config(t)
	dir := filepath.Join(t.TempDir(), "presets")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "mine.toml"), []byte(quietPreset), 0o644)
	svc := &fakeService{err: errors.New("channel cpu: pwm1_enable not writable")}
	s := dirPresetStore{dir: dir, cfgPath: cfgPath, svc: svc, profile: "n5pro"}
	err := s.Apply("mine")
	var re *web.ReloadError
	if !errors.As(err, &re) || err.Error() != "preset mine written, reload failed: channel cpu: pwm1_enable not writable" {
		t.Fatalf("apply: %v", err)
	}
	if isRestartRequired(err) {
		t.Fatal("reload error reads as restart required")
	}
	if cfg, _, _ := config.Load(cfgPath); cfg.Channel("cpu") == nil || cfg.Channel("cpu").Critical != 90 {
		t.Fatalf("preset not written before the reload: %+v", cfg.Channels)
	}
	svc.err = errRestartRequired()
	if err := s.Apply("mine"); !isRestartRequired(err) || errors.As(err, &re) {
		t.Fatalf("restart sentinel: %v", err)
	}
	if svc.reloads() != 2 {
		t.Fatalf("reloads = %d", svc.reloads())
	}
}
