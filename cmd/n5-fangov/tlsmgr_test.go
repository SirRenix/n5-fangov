package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/tlscert"
	"github.com/SirRenix/n5-fangov/internal/web"
)

const mgrTOML = "# operator notes stay\n[daemon]\ninterval = \"10s\"\n\n[web]\nlisten = \"192.0.2.10:8010\"   # LAN\nauth = \"none\"\ntls = \"auto\"\n\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45,85],[80,255]]\ncritical = 88\nstop = \"auto\"\n"

func testPair(t *testing.T, cn string, hosts ...string) (certPEM, keyPEM []byte) {
	t.Helper()
	return testPairCurve(t, elliptic.P256(), cn, hosts...)
}

// testPairCurve is testPair on an explicit curve (P-224 for the M1 tests).
func testPairCurve(t *testing.T, curve elliptic.Curve, cn string, hosts ...string) (certPEM, keyPEM []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(400 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalPKCS8PrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd})
}

func newTestMgr(t *testing.T) (*tlsManager, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(mgrTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := config.Parse([]byte(mgrTOML))
	if err != nil {
		t.Fatal(err)
	}
	m := newTLSManager(cfgPath, webOf(cfg), []string{"192.0.2.10", "n5.lan", "localhost", "127.0.0.1"})
	m.logf = func(string, ...any) {}
	return m, cfgPath
}

func pubOf(i tlscert.InfoData) string { return i.FingerprintSHA256 }

// TestTLSManagerRegenerateKeepsKey: load(true) creates the auto pair,
// Regenerate(true) changes the certificate but not the key, false makes a
// new key; the store serves the latest one each time.
func TestTLSManagerRegenerateKeepsKey(t *testing.T) {
	m, _ := newTestMgr(t)
	if _, _, err := m.Info(); err == nil {
		t.Fatal("Info before load must fail")
	}
	certPath, err := m.load(true)
	if err != nil {
		t.Fatal(err)
	}
	if certPath != filepath.Join(m.dir, tlscert.CertFile) {
		t.Errorf("cert path %s", certPath)
	}
	info, mode, err := m.Info()
	if err != nil || mode != "auto" || !strings.Contains(info.Subject, "CN=n5.lan") || info.KeyAlgo != "ECDSA P-256" {
		t.Fatalf("Info = %+v %q %v", info, mode, err)
	}
	pubKey := func() []byte {
		b, _ := x509.MarshalPKIXPublicKey(m.store.Current().Leaf.PublicKey)
		return b
	}
	k0 := pubKey()
	re, kept, err := m.Regenerate(true)
	if err != nil {
		t.Fatal(err)
	}
	if !kept {
		t.Error("Regenerate(keep) reported kept=false")
	}
	if re.SerialHex == info.SerialHex || pubOf(re) == pubOf(info) {
		t.Error("Regenerate(keep) did not issue a new certificate")
	}
	if string(pubKey()) != string(k0) {
		t.Error("Regenerate(keep) replaced the key")
	}
	if tlscert.Info(m.store.Current()).SerialHex != re.SerialHex {
		t.Error("store not swapped")
	}
	// files on disk match the store
	loaded, err := tlscert.LoadFiles(tlscert.Paths(m.dir))
	if err != nil || tlscert.Info(loaded).SerialHex != re.SerialHex {
		t.Errorf("disk pair: %v %s", err, tlscert.Info(loaded).SerialHex)
	}
	fresh, kept, err := m.Regenerate(false)
	if err != nil {
		t.Fatal(err)
	}
	if kept {
		t.Error("Regenerate(new key) reported kept=true")
	}
	if string(pubKey()) == string(k0) || fresh.SerialHex == re.SerialHex {
		t.Error("Regenerate(new key) kept the key")
	}
	// exports follow the store
	p, err := m.ExportPEM()
	if err != nil || !strings.HasPrefix(string(p), "-----BEGIN CERTIFICATE-----") || strings.Contains(string(p), "PRIVATE") {
		t.Errorf("ExportPEM: %v %.40s", err, p)
	}
	d, err := m.ExportDER()
	if err != nil || string(d) != string(m.store.Current().Leaf.Raw) {
		t.Errorf("ExportDER: %v", err)
	}
	// off mode: Info says off, everything else ErrTLSOff
	m.mode = "off"
	if _, mode, err := m.Info(); mode != "off" || err != nil {
		t.Errorf("off Info = %q %v", mode, err)
	}
	if _, err := m.ExportPEM(); !errors.Is(err, web.ErrTLSOff) {
		t.Errorf("off ExportPEM = %v", err)
	}
	if _, _, err := m.Regenerate(true); !errors.Is(err, web.ErrTLSOff) {
		t.Errorf("off Regenerate = %v", err)
	}
	if _, _, err := m.Upload(nil, nil); !errors.Is(err, web.ErrTLSOff) {
		t.Errorf("off Upload = %v", err)
	}
	if _, err := m.ResetAuto(); !errors.Is(err, web.ErrTLSOff) {
		t.Errorf("off ResetAuto = %v", err)
	}
}

// TestTLSManagerUploadWritesConfig: a valid pair lands as custom-*.pem
// (0600), the config gets tls = "file" + paths with comments preserved,
// the store serves the upload, warnings name the missing SAN; a bad pair
// is a ValidationError and changes nothing; Regenerate is refused in
// file mode.
func TestTLSManagerUploadWritesConfig(t *testing.T) {
	m, cfgPath := newTestMgr(t)
	if _, err := m.load(true); err != nil {
		t.Fatal(err)
	}
	autoSerial := tlscert.Info(m.store.Current()).SerialHex
	// bad pair: nothing changes
	if _, _, err := m.Upload([]byte("junk"), []byte("junk")); err == nil || !errors.As(err, new(web.ValidationError)) {
		t.Fatalf("junk upload err = %v", err)
	}
	if raw, _ := os.ReadFile(cfgPath); string(raw) != mgrTOML {
		t.Error("config touched by a rejected upload")
	}
	certPEM, keyPEM := testPair(t, "fans.example", "fans.example", "192.0.2.10")
	info, warns, err := m.Upload(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info.Subject, "CN=fans.example") || info.IsCA {
		t.Errorf("info = %+v", info)
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "lacks host n5.lan") || !strings.Contains(joined, "lacks host localhost") || strings.Contains(joined, "192.0.2.10") {
		t.Errorf("warnings = %v", warns)
	}
	if m.Mode() != "file" || tlscert.Info(m.store.Current()).SerialHex == autoSerial {
		t.Error("store/mode not switched to the upload")
	}
	certPath := filepath.Join(m.dir, customCertFile)
	keyPath := filepath.Join(m.dir, customKeyFile)
	for _, p := range []string{certPath, keyPath} {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %o", p, st.Mode().Perm())
		}
	}
	if b, _ := os.ReadFile(keyPath); string(b) != string(keyPEM) {
		t.Error("custom-key.pem content differs")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, warnsCfg, err := config.Parse(raw)
	if err != nil || len(warnsCfg) != 0 {
		t.Fatalf("config after upload: %v %v\n%s", err, warnsCfg, raw)
	}
	if cfg.Web.TLS != "file" || cfg.Web.CertFile != certPath || cfg.Web.KeyFile != keyPath {
		t.Errorf("config web = %+v", cfg.Web)
	}
	for _, keep := range []string{"# operator notes stay", "# LAN", "[[channel]]", "curve = [[45,85],[80,255]]", "listen = \"192.0.2.10:8010\""} {
		if !strings.Contains(string(raw), keep) {
			t.Errorf("config lost %q:\n%s", keep, raw)
		}
	}
	if strings.Count(string(raw), "tls =") != 1 || strings.Count(string(raw), "cert_file =") != 1 {
		t.Errorf("duplicate keys:\n%s", raw)
	}
	// the auto pair is still on disk (instant reset)
	if _, err := os.Stat(filepath.Join(m.dir, tlscert.CertFile)); err != nil {
		t.Error("auto cert.pem removed by upload")
	}
	if _, _, err := m.Regenerate(true); !errors.Is(err, web.ErrTLSFileMode) {
		t.Errorf("Regenerate in file mode = %v", err)
	}
	// a fresh manager from the written config loads the upload
	cfg2, _, _ := config.Load(cfgPath)
	m2 := newTLSManager(cfgPath, webOf(cfg2), m.hosts)
	if p, err := m2.load(false); err != nil || p != certPath {
		t.Errorf("reload: %v %s", err, p)
	}
	if i, mode, _ := m2.Info(); mode != "file" || i.SerialHex != info.SerialHex {
		t.Errorf("reloaded = %q %s", mode, i.SerialHex)
	}
}

// TestTLSManagerResetAuto: after an upload, ResetAuto serves the automatic
// certificate again (same one as before: EnsureAuto reuses it), writes
// tls = "auto" and deletes the custom files. A missing config file is
// created from scratch.
func TestTLSManagerResetAuto(t *testing.T) {
	m, cfgPath := newTestMgr(t)
	if _, err := m.load(true); err != nil {
		t.Fatal(err)
	}
	autoInfo := tlscert.Info(m.store.Current())
	certPEM, keyPEM := testPair(t, "own", "n5.lan")
	if _, _, err := m.Upload(certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	info, err := m.ResetAuto()
	if err != nil {
		t.Fatal(err)
	}
	if info.SerialHex != autoInfo.SerialHex || m.Mode() != "auto" || tlscert.Info(m.store.Current()).SerialHex != autoInfo.SerialHex {
		t.Errorf("reset served %s, want the auto certificate %s (mode %s)", info.SerialHex, autoInfo.SerialHex, m.Mode())
	}
	for _, f := range []string{customCertFile, customKeyFile} {
		if _, err := os.Stat(filepath.Join(m.dir, f)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still present (%v)", f, err)
		}
	}
	raw, _ := os.ReadFile(cfgPath)
	cfg, warns, err := config.Parse(raw)
	if err != nil || len(warns) != 0 || cfg.Web.TLS != "auto" || cfg.Web.CertFile != "" || cfg.Web.KeyFile != "" {
		t.Errorf("config after reset: %v %v %+v", err, warns, cfg.Web)
	}
	if !strings.Contains(string(raw), "# operator notes stay") {
		t.Error("comments lost on reset")
	}
	// reset twice is harmless
	if _, err := m.ResetAuto(); err != nil {
		t.Errorf("second reset: %v", err)
	}
	// missing config file: written from scratch with a [web] table
	os.Remove(cfgPath)
	if _, _, err := m.Upload(certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg, _, err := config.Parse(raw); err != nil || cfg.Web.TLS != "file" {
		t.Errorf("config from scratch: %v\n%s", err, raw)
	}
}
