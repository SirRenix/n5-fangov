package main

import (
	"crypto/elliptic"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/tlscert"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// fileModeMgr writes a config with tls = "file" pointing at certFile /
// keyFile and returns a manager built from it (as serve and the CLI do).
func fileModeMgr(t *testing.T, root, certFile, keyFile string) (*tlsManager, string) {
	t.Helper()
	cfgPath := filepath.Join(root, "config.toml")
	raw := setTLSKeys([]byte(mgrTOML), "file", certFile, keyFile)
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, warns, err := config.Parse(raw)
	if err != nil || len(warns) != 0 {
		t.Fatalf("file-mode config: %v %v", err, warns)
	}
	m := newTLSManager(cfgPath, webOf(cfg), []string{"192.0.2.10", "n5.lan"})
	m.logf = func(string, ...any) {}
	return m, cfgPath
}

func webOfFile(t *testing.T, cfgPath string) config.Web {
	t.Helper()
	cfg, _, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Web
}

// TestTLSManagerPinsConfigKeys (M2): after an upload, a config text
// written through the API paths with the pre-upload values (stale editor
// copy, imported bundle, preset apply) keeps tls = "file" and the custom
// paths; a text that already matches is not touched; with ownsConfig off
// (--listen override) nothing is pinned; after ResetAuto the pin says auto.
func TestTLSManagerPinsConfigKeys(t *testing.T) {
	m, cfgPath := newTestMgr(t)
	if _, err := m.load(true); err != nil {
		t.Fatal(err)
	}
	// nothing to pin: the stale text already says auto without paths
	if got := m.pinConfig([]byte(mgrTOML)); string(got) != mgrTOML {
		t.Errorf("auto pin changed matching text:\n%s", got)
	}
	certPEM, keyPEM := testPair(t, "fans.example", "fans.example", "192.0.2.10", "n5.lan")
	if _, _, err := m.Upload(certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(m.dir, customCertFile)
	keyPath := filepath.Join(m.dir, customKeyFile)
	// 1. PUT /api/config with the stale raw (what the curve editor holds)
	store := fileConfigStore{path: cfgPath, pin: m.pinConfig}
	stale := strings.Replace(mgrTOML, "curve = [[45,85],[80,255]]", "curve = [[40,80],[80,255]]", 1)
	if err := store.Save([]byte(stale)); err != nil {
		t.Fatal(err)
	}
	w := webOfFile(t, cfgPath)
	if w.TLS != "file" || w.CertFile != certPath || w.KeyFile != keyPath {
		t.Errorf("after stale PUT: %+v", w)
	}
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "curve = [[40,80],[80,255]]") || !strings.Contains(string(raw), "# operator notes stay") {
		t.Errorf("stale PUT lost its own change or the comments:\n%s", raw)
	}
	// 2. settings import with a bundle whose config says auto
	bundle, _ := json.Marshal(map[string]any{"format": 1, "version": "x", "exported": 1, "config": mgrTOML, "presets": map[string]string{}})
	var reloaded []byte
	fb := fileBundle{cfgPath: cfgPath, presetDir: filepath.Join(filepath.Dir(cfgPath), "presets"), reload: func(r []byte) error { reloaded = r; return nil }, pin: m.pinConfig}
	if _, err := fb.Import(bundle); err != nil {
		t.Fatal(err)
	}
	if w := webOfFile(t, cfgPath); w.TLS != "file" || w.CertFile != certPath {
		t.Errorf("after import: %+v", w)
	}
	if cfg, _, _ := config.Parse(reloaded); cfg.Web.TLS != "file" {
		t.Error("import reloaded the unpinned text")
	}
	// 3. preset apply (reads the file, marshals, pins)
	presetDir := t.TempDir()
	if err := config.SavePreset(presetDir, "quiet", []config.Channel{{Name: "cpu", PWM: 1, Sensor: "k10temp", Curve: [][2]int{{40, 60}, {80, 255}}, Critical: 88, Stop: "auto"}}); err != nil {
		t.Fatal(err)
	}
	ps := dirPresetStore{dir: presetDir, cfgPath: cfgPath, svc: fakeReloadService{}, pin: m.pinConfig}
	if err := ps.Apply("quiet"); err != nil {
		t.Fatal(err)
	}
	if w := webOfFile(t, cfgPath); w.TLS != "file" || w.KeyFile != keyPath {
		t.Errorf("after preset apply: %+v", w)
	}
	// ownsConfig off: the text passes through
	m.ownsConfig = false
	if got := m.pinConfig([]byte(mgrTOML)); string(got) != mgrTOML {
		t.Error("pin applied although the manager does not own the config")
	}
	// ResetAuto owns it again and pins auto
	if _, err := m.ResetAuto(); err != nil {
		t.Fatal(err)
	}
	fileRaw := string(setTLSKeys([]byte(mgrTOML), "file", "/x/c.pem", "/x/k.pem"))
	if err := store.Save([]byte(fileRaw)); err != nil {
		t.Fatal(err)
	}
	if w := webOfFile(t, cfgPath); w.TLS != "auto" || w.CertFile != "" {
		t.Errorf("after reset + stale file-mode PUT: %+v", w)
	}
}

// fakeReloadService is the minimal control.Service for dirPresetStore.Apply:
// only Reload is called; the embedded nil interface panics on anything else.
type fakeReloadService struct{ control.Service }

func (fakeReloadService) Reload([]byte) error { return nil }

// TestTLSManagerFallback (M3): a file pair that cannot be loaded makes
// loadForServe serve the automatic certificate, report the fallback and
// keep the config on "file"; a usable pair does not fall back; the
// decision helper is a pure function; Regenerate is allowed during the
// fallback; ResetAuto and Upload end it.
func TestTLSManagerFallback(t *testing.T) {
	root := t.TempDir()
	broken := filepath.Join(root, "broken.pem")
	if err := os.WriteFile(broken, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, cfgPath := fileModeMgr(t, root, broken, broken)
	certPath, fellBack, err := m.loadForServe()
	if err != nil || fellBack == nil {
		t.Fatalf("loadForServe = %q, fellBack %v, err %v", certPath, fellBack, err)
	}
	if certPath != filepath.Join(m.dir, tlscert.CertFile) {
		t.Errorf("served %s, want the auto pair", certPath)
	}
	info, mode, err := m.Info()
	if err != nil || mode != modeFallback || !m.Fallback() || !info.IsCA {
		t.Errorf("Info = %+v %q %v fallback=%v", info, mode, err, m.Fallback())
	}
	if m.Mode() != "file" {
		t.Errorf("configured mode changed to %q", m.Mode())
	}
	if w := webOfFile(t, cfgPath); w.TLS != "file" || w.CertFile != broken {
		t.Errorf("fallback rewrote the config: %+v", w)
	}
	// the config is pinned to what the operator configured, not to the fallback
	if got := m.pinConfig([]byte(mgrTOML)); !strings.Contains(string(got), `tls = "file"`) || !strings.Contains(string(got), tomlString(broken)) {
		t.Errorf("pin during fallback:\n%s", got)
	}
	// regenerate during fallback reissues the auto pair that is being served
	if _, kept, err := m.Regenerate(true); err != nil || !kept {
		t.Errorf("Regenerate during fallback: kept=%v %v", kept, err)
	}
	if _, mode, _ := m.Info(); mode != modeFallback {
		t.Errorf("mode after regenerate = %q", mode)
	}
	// M1 in the manager: a P-224 pair on disk is "unusable", also a fallback
	c224, k224 := testPairCurve(t, elliptic.P224(), "p224", "n5.lan")
	os.WriteFile(filepath.Join(root, "c224.pem"), c224, 0o600)
	os.WriteFile(filepath.Join(root, "k224.pem"), k224, 0o600)
	m2, _ := fileModeMgr(t, root, filepath.Join(root, "c224.pem"), filepath.Join(root, "k224.pem"))
	if _, err := m2.load(false); err == nil || !strings.Contains(err.Error(), "cannot be used by this server") {
		t.Errorf("load(P-224) = %v", err)
	}
	if len(m2.store.Current().Certificate) != 0 {
		t.Error("unusable pair reached the store")
	}
	if _, fb, err := m2.loadForServe(); err != nil || fb == nil || !m2.Fallback() {
		t.Errorf("loadForServe(P-224) = fb %v err %v", fb, err)
	}
	// a good pair: no fallback
	goodC, goodK := testPair(t, "good", "n5.lan", "192.0.2.10")
	os.WriteFile(filepath.Join(root, "good-c.pem"), goodC, 0o600)
	os.WriteFile(filepath.Join(root, "good-k.pem"), goodK, 0o600)
	m3, _ := fileModeMgr(t, root, filepath.Join(root, "good-c.pem"), filepath.Join(root, "good-k.pem"))
	if p, fb, err := m3.loadForServe(); err != nil || fb != nil || m3.Fallback() || p != filepath.Join(root, "good-c.pem") {
		t.Errorf("loadForServe(good) = %s fb %v err %v", p, fb, err)
	}
	if _, mode, _ := m3.Info(); mode != "file" {
		t.Errorf("good file mode = %q", mode)
	}
	// decision helper
	for _, c := range []struct {
		mode string
		err  error
		want bool
	}{{"file", errors.New("x"), true}, {"file", nil, false}, {"auto", errors.New("x"), false}, {"off", errors.New("x"), false}} {
		if got := shouldFallbackToAuto(c.mode, c.err); got != c.want {
			t.Errorf("shouldFallbackToAuto(%q, %v) = %v", c.mode, c.err, got)
		}
	}
	// ResetAuto ends the fallback and writes auto
	if _, err := m.ResetAuto(); err != nil {
		t.Fatal(err)
	}
	if _, mode, _ := m.Info(); mode != "auto" || m.Fallback() {
		t.Errorf("after reset: %q fallback=%v", mode, m.Fallback())
	}
	if _, err := os.Stat(broken); err != nil {
		t.Error("ResetAuto removed a cert_file outside the tls directory")
	}
	// Upload ends it too
	if _, _, err := m2.Upload(goodC, goodK); err != nil {
		t.Fatal(err)
	}
	if _, mode, _ := m2.Info(); mode != "file" || m2.Fallback() {
		t.Errorf("after upload: %q fallback=%v", mode, m2.Fallback())
	}
}

// TestCertCLIOfflineResetBrokenPair (M3a): with no daemon and a file pair
// that does not load, `cert reset` and `cert upload` still work (the
// repair), `cert info` and `cert export` report the problem.
func TestCertCLIOfflineResetBrokenPair(t *testing.T) {
	root := t.TempDir()
	broken := filepath.Join(root, "broken.pem")
	if err := os.WriteFile(broken, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cfgPath := fileModeMgr(t, root, broken, broken)
	c := certClient{cfgPath: cfgPath, dir: filepath.Join(root, "run")} // no socket → errNoDaemon
	if rc := c.info(); rc != exitFail {
		t.Errorf("cert info with a broken pair = %d", rc)
	}
	if rc := c.export(false, filepath.Join(root, "out.crt")); rc != exitFail {
		t.Errorf("cert export with a broken pair = %d", rc)
	}
	goodC, goodK := testPair(t, "good", "n5.lan")
	os.WriteFile(filepath.Join(root, "gc.pem"), goodC, 0o600)
	os.WriteFile(filepath.Join(root, "gk.pem"), goodK, 0o600)
	if rc := c.upload(filepath.Join(root, "gc.pem"), filepath.Join(root, "gk.pem")); rc != exitOK {
		t.Fatalf("cert upload over a broken pair = %d", rc)
	}
	if w := webOfFile(t, cfgPath); w.TLS != "file" || w.CertFile != filepath.Join(tlsDir(cfgPath), customCertFile) {
		t.Errorf("after upload: %+v", w)
	}
	// break it again, then reset
	if err := os.WriteFile(filepath.Join(tlsDir(cfgPath), customKeyFile), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rc := c.reset(); rc != exitOK {
		t.Fatalf("cert reset over a broken pair = %d", rc)
	}
	if w := webOfFile(t, cfgPath); w.TLS != "auto" || w.CertFile != "" {
		t.Errorf("after reset: %+v", w)
	}
	if _, err := os.Stat(filepath.Join(tlsDir(cfgPath), tlscert.CertFile)); err != nil {
		t.Error("reset did not create the auto pair")
	}
	if rc := c.info(); rc != exitOK {
		t.Errorf("cert info after reset = %d", rc)
	}
}

// TestTLSManagerUploadRollback (L9): when the config cannot be written
// after the custom files were, the files go back to their previous state
// — absent on a first upload, the previous pair on a re-upload — and
// mode, config and store are unchanged.
func TestTLSManagerUploadRollback(t *testing.T) {
	m, cfgPath := newTestMgr(t)
	if _, err := m.load(true); err != nil {
		t.Fatal(err)
	}
	autoSerial := tlscert.Info(m.store.Current()).SerialHex
	certPath := filepath.Join(m.dir, customCertFile)
	keyPath := filepath.Join(m.dir, customKeyFile)
	c1, k1 := testPair(t, "one", "n5.lan")
	// config path is a directory: the read in writeMode fails
	m.cfgPath = t.TempDir()
	if _, _, err := m.Upload(c1, k1); err == nil {
		t.Fatal("upload succeeded with an unwritable config")
	}
	for _, p := range []string{certPath, keyPath} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind after the failed upload (%v)", p, err)
		}
	}
	if m.Mode() != "auto" || tlscert.Info(m.store.Current()).SerialHex != autoSerial {
		t.Error("failed upload changed mode or store")
	}
	// a working upload, then a failing re-upload: the first pair is restored
	m.cfgPath = cfgPath
	if _, _, err := m.Upload(c1, k1); err != nil {
		t.Fatal(err)
	}
	oneSerial := tlscert.Info(m.store.Current()).SerialHex
	c2, k2 := testPair(t, "two", "n5.lan")
	m.cfgPath = t.TempDir()
	if _, _, err := m.Upload(c2, k2); err == nil {
		t.Fatal("re-upload succeeded with an unwritable config")
	}
	if b, _ := os.ReadFile(certPath); string(b) != string(c1) {
		t.Error("custom-cert.pem not restored to the previous pair")
	}
	if b, _ := os.ReadFile(keyPath); string(b) != string(k1) {
		t.Error("custom-key.pem not restored to the previous pair")
	}
	if tlscert.Info(m.store.Current()).SerialHex != oneSerial || m.Mode() != "file" {
		t.Error("failed re-upload changed the served certificate or the mode")
	}
	m.cfgPath = cfgPath
	if w := webOfFile(t, cfgPath); w.TLS != "file" || w.CertFile != certPath {
		t.Errorf("config after the failed re-upload: %+v", w)
	}
	// still consistent: a fresh manager loads the restored pair
	cfg2, _, _ := config.Load(cfgPath)
	m2 := newTLSManager(cfgPath, webOf(cfg2), m.hosts)
	m2.logf = m.logf
	if _, err := m2.load(false); err != nil {
		t.Errorf("reload after rollback: %v", err)
	}
	if tlscert.Info(m2.store.Current()).SerialHex != oneSerial {
		t.Error("reload served a different pair than the rollback left")
	}
}

// TestTLSManagerResetLeavesForeignPair: tls = "file" pointing at a pair
// outside the tls directory (configured by hand); ResetAuto switches the
// config to auto and does not delete those files. Also L8: a relative
// config path is made absolute.
func TestTLSManagerResetLeavesForeignPair(t *testing.T) {
	root := t.TempDir()
	own := filepath.Join(root, "own")
	os.MkdirAll(own, 0o700)
	c, k := testPair(t, "hand", "n5.lan")
	certFile, keyFile := filepath.Join(own, "cert.pem"), filepath.Join(own, "key.pem")
	os.WriteFile(certFile, c, 0o600)
	os.WriteFile(keyFile, k, 0o600)
	m, cfgPath := fileModeMgr(t, root, certFile, keyFile)
	if _, err := m.load(false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ResetAuto(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{certFile, keyFile} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed by ResetAuto", p)
		}
	}
	if w := webOfFile(t, cfgPath); w.TLS != "auto" || w.CertFile != "" || w.KeyFile != "" {
		t.Errorf("config after reset: %+v", w)
	}
	if _, err := os.Stat(filepath.Join(m.dir, tlscert.CertFile)); err != nil {
		t.Error("auto pair missing after reset")
	}
	// L8
	rel := newTLSManager("config.toml", webSpec{TLS: "auto"}, nil)
	if !filepath.IsAbs(rel.cfgPath) || !filepath.IsAbs(rel.dir) {
		t.Errorf("relative config path kept: %s / %s", rel.cfgPath, rel.dir)
	}
	_ = web.ErrTLSOff
}
