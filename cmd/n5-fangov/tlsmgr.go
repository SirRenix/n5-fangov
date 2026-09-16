package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/tlscert"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// Uploaded pair inside the tls directory (next to the automatic cert.pem
// and key.pem, which stay in place so "back to auto" is instant).
const (
	customCertFile = "custom-cert.pem"
	customKeyFile  = "custom-key.pem"
)

// modeFallback is what Info reports while the configured file pair could
// not be loaded and the automatic certificate is served instead.
const modeFallback = "auto (fallback from file)"

// tlsManager implements web.TLSMgr: it owns the certificate the listener
// serves (through a tlscert.Store), the tls directory and the [web] tls
// keys of the config file. serve builds it with create=true (the
// automatic pair is made at start); the CLI builds it with create=false
// for the offline path when the daemon is not running.
//
// mode is the configured [web].tls: "auto", "file" or "off". In "file" mode
// certFile/keyFile are the configured paths — the uploaded pair or
// something the operator pointed the config at by hand. fallback is set
// when that pair could not be loaded at start and the automatic one is
// served in its place; mode and the paths then still say "file" so the
// config keeps the operator's intent and the CLI/UI can show both.
//
// ownsConfig: the manager re-applies tls/cert_file/key_file to every
// config write that goes past it (PUT /api/config with a stale editor
// copy, settings import, preset apply — see pinConfig). serve clears it
// when the effective mode came from a --listen override rather than from
// the file; an Upload or ResetAuto, which write the file themselves, set
// it again.
type tlsManager struct {
	mu         sync.Mutex
	cfgPath    string
	dir        string
	hosts      []string
	org        string
	mode       string
	certFile   string
	keyFile    string
	fallback   bool
	ownsConfig bool
	store      *tlscert.Store
	logf       func(string, ...any)
}

// newTLSManager captures the wiring; load fills the store. cfgPath is made
// absolute: the daemon's working directory is / under systemd but
// the CLI's is wherever the operator stands, and the tls directory is
// derived from it.
func newTLSManager(cfgPath string, w webSpec, hosts []string) *tlsManager {
	if abs, err := filepath.Abs(cfgPath); err == nil {
		cfgPath = abs
	}
	return &tlsManager{
		cfgPath: cfgPath, dir: tlsDir(cfgPath), hosts: hosts, org: setupOrg,
		mode: w.TLS, certFile: w.CertFile, keyFile: w.KeyFile, ownsConfig: true,
		store: &tlscert.Store{}, logf: log.Printf,
	}
}

// load reads (create=false) or ensures (create=true) the certificate for
// the current mode into the store. Mode "off" loads nothing. The second
// result is the path of the served certificate for the start-up log line.
// A pair the listener could not serve is an error, not a swap.
func (m *tlsManager) load(create bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked(create)
}

func (m *tlsManager) loadLocked(create bool) (string, error) {
	switch m.mode {
	case "off":
		return "", nil
	case "file":
		cert, err := tlscert.LoadFiles(m.certFile, m.keyFile)
		if err != nil {
			return "", err
		}
		if err := m.serve(cert); err != nil {
			return "", fmt.Errorf("%s / %s: %w", m.certFile, m.keyFile, err)
		}
		return m.certFile, nil
	}
	certPath, keyPath := tlscert.Paths(m.dir)
	if !create {
		cert, err := tlscert.LoadFiles(certPath, keyPath)
		if err != nil {
			return "", err
		}
		return certPath, m.serve(cert)
	}
	cert, certPath, err := tlscert.EnsureAuto(m.options())
	if err != nil {
		return "", err
	}
	return certPath, m.serve(cert)
}

// loadForServe is load(true) with the fallback recovery: when tls = "file" and
// the pair cannot be loaded or served, the automatic certificate is
// ensured and served instead, the manager reports the fallback and the
// listener stays up. The second result is that load error (nil when
// nothing fell back) for the caller's log line and alert.
func (m *tlsManager) loadForServe() (certPath string, fellBack error, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	certPath, err = m.loadLocked(true)
	if !shouldFallbackToAuto(m.mode, err) {
		return certPath, nil, err
	}
	fellBack = err
	cert, autoPath, aerr := tlscert.EnsureAuto(m.options())
	if aerr != nil {
		return "", fellBack, fmt.Errorf("file pair unusable (%v) and no automatic certificate either: %w", fellBack, aerr)
	}
	if aerr := m.serve(cert); aerr != nil {
		return "", fellBack, aerr
	}
	m.fallback = true
	return autoPath, fellBack, nil
}

// shouldFallbackToAuto is the fallback decision: only a configured file pair
// that failed to load falls back; "auto" failing has nothing to fall back
// to and "off" never loads.
func shouldFallbackToAuto(mode string, loadErr error) bool {
	return mode == "file" && loadErr != nil
}

// serve is the one place a certificate reaches the store: it runs the
// in-process handshake first, so a pair crypto/tls cannot sign with
// never replaces a working one.
func (m *tlsManager) serve(cert tls.Certificate) error {
	if err := tlscert.CheckUsable(cert); err != nil {
		return err
	}
	m.store.Set(cert)
	return nil
}

func (m *tlsManager) options() tlscert.Options {
	return tlscert.Options{Dir: m.dir, Hosts: m.hosts, Org: m.org, Logf: m.logf}
}

// Mode returns the configured tls mode ("file" also during a fallback).
func (m *tlsManager) Mode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

// Fallback implements web.TLSFallback.
func (m *tlsManager) Fallback() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fallback
}

// Store is the certificate source for web.Server.ServeTLSStore.
func (m *tlsManager) Store() *tlscert.Store { return m.store }

// reportedMode is what Info and the API say: the configured mode, or
// modeFallback while the automatic certificate stands in for the file.
func (m *tlsManager) reportedMode() string {
	if m.fallback {
		return modeFallback
	}
	return m.mode
}

func (m *tlsManager) Info() (tlscert.InfoData, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode == "off" {
		return tlscert.InfoData{}, "off", nil
	}
	cur := m.store.Current()
	if len(cur.Certificate) == 0 {
		return tlscert.InfoData{}, m.reportedMode(), errors.New("no certificate loaded")
	}
	return tlscert.Info(cur), m.reportedMode(), nil
}

func (m *tlsManager) current() (tls.Certificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode == "off" {
		return tls.Certificate{}, web.ErrTLSOff
	}
	cur := m.store.Current()
	if len(cur.Certificate) == 0 {
		return tls.Certificate{}, errors.New("no certificate loaded")
	}
	return cur, nil
}

func (m *tlsManager) ExportPEM() ([]byte, error) {
	cur, err := m.current()
	if err != nil {
		return nil, err
	}
	return tlscert.PEM(cur)
}

func (m *tlsManager) ExportDER() ([]byte, error) {
	cur, err := m.current()
	if err != nil {
		return nil, err
	}
	return tlscert.DER(cur)
}

// Regenerate reissues the automatic certificate for the current hosts.
// keepKey reuses the stored private key (imported trust survives), else a
// new pair is made; kept says whether the key really was reused.
// Refused in "file" mode: the served certificate is the operator's, use
// ResetAuto first. During a fallback the automatic certificate is the one
// being served, so regenerating it is allowed.
func (m *tlsManager) Regenerate(keepKey bool) (tlscert.InfoData, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case m.mode == "off":
		return tlscert.InfoData{}, false, web.ErrTLSOff
	case m.mode == "file" && !m.fallback:
		return tlscert.InfoData{}, false, web.ErrTLSFileMode
	}
	var cert tls.Certificate
	var kept bool
	var err error
	if keepKey {
		cert, kept, err = tlscert.Reissue(m.options())
	} else {
		cert, err = tlscert.Regenerate(m.options())
	}
	if err != nil {
		return tlscert.InfoData{}, false, err
	}
	if err := m.serve(cert); err != nil {
		return tlscert.InfoData{}, false, err
	}
	return tlscert.Info(cert), kept, nil
}

// Upload validates the pair against the listen hosts (including the
// handshake check), stores it as custom-cert.pem / custom-key.pem
// (0600, atomic), points the config at it (tls = "file", comments
// preserved) and serves it from the next handshake on. The config is
// written before the swap: a daemon restart then comes up with the same
// certificate. A config write that fails puts the custom files back the
// way they were — previous content or absent — so the file system
// never disagrees with the config.
func (m *tlsManager) Upload(certPEM, keyPEM []byte) (tlscert.InfoData, []string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode == "off" {
		return tlscert.InfoData{}, nil, web.ErrTLSOff
	}
	cert, warns, err := tlscert.ValidatePair(certPEM, keyPEM, m.hosts)
	if err != nil {
		return tlscert.InfoData{}, nil, web.ValidationError{Err: err}
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return tlscert.InfoData{}, nil, fmt.Errorf("create %s: %w", m.dir, err)
	}
	certPath := filepath.Join(m.dir, customCertFile)
	keyPath := filepath.Join(m.dir, customKeyFile)
	prevCert, prevKey := readIfExists(certPath), readIfExists(keyPath)
	rollback := func() {
		restoreFile(keyPath, prevKey, m.logf)
		restoreFile(certPath, prevCert, m.logf)
	}
	if err := tlscert.WritePrivate(keyPath, keyPEM); err != nil {
		rollback()
		return tlscert.InfoData{}, nil, err
	}
	if err := tlscert.WritePrivate(certPath, certPEM); err != nil {
		rollback()
		return tlscert.InfoData{}, nil, err
	}
	if err := m.writeMode("file", certPath, keyPath); err != nil {
		rollback()
		return tlscert.InfoData{}, nil, err
	}
	m.mode, m.certFile, m.keyFile = "file", certPath, keyPath
	m.fallback, m.ownsConfig = false, true
	m.store.Set(cert) // ValidatePair already ran the handshake
	return tlscert.Info(cert), warns, nil
}

// readIfExists returns the file content, nil when it does not exist.
func readIfExists(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return b
}

// restoreFile puts path back to prev (nil → removed).
func restoreFile(path string, prev []byte, logf func(string, ...any)) {
	var err error
	if prev == nil {
		err = os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			err = nil
		}
	} else {
		err = tlscert.WritePrivate(path, prev)
	}
	if err != nil {
		logf("tls: rollback of %s: %v", path, err)
	}
}

// ResetAuto returns to the automatic certificate: EnsureAuto (reusing the
// existing pair when it still fits), config back to tls = "auto", the
// uploaded pair deleted. A cert_file/key_file the operator pointed the
// config at by hand (outside custom-*.pem) is left alone.
func (m *tlsManager) ResetAuto() (tlscert.InfoData, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode == "off" {
		return tlscert.InfoData{}, web.ErrTLSOff
	}
	cert, _, err := tlscert.EnsureAuto(m.options())
	if err != nil {
		return tlscert.InfoData{}, err
	}
	if err := tlscert.CheckUsable(cert); err != nil {
		return tlscert.InfoData{}, err
	}
	if err := m.writeMode("auto", "", ""); err != nil {
		return tlscert.InfoData{}, err
	}
	m.mode, m.certFile, m.keyFile = "auto", "", ""
	m.fallback, m.ownsConfig = false, true
	m.store.Set(cert)
	for _, f := range []string{customCertFile, customKeyFile} {
		if err := os.Remove(filepath.Join(m.dir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			m.logf("tls: remove %s: %v", f, err)
		}
	}
	return tlscert.Info(cert), nil
}

// writeMode sets [web] tls / cert_file / key_file in the config file with
// config.SetKey (everything else, comments included, stays) and saves it
// atomically. A missing file starts from the built-in defaults.
func (m *tlsManager) writeMode(mode, certFile, keyFile string) error {
	raw, err := os.ReadFile(m.cfgPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read %s: %w", m.cfgPath, err)
		}
		raw = nil
	}
	if err := saveConfig(m.cfgPath, setTLSKeys(raw, mode, certFile, keyFile)); err != nil {
		return fmt.Errorf("write %s: %w", m.cfgPath, err)
	}
	return nil
}

// setTLSKeys edits the three [web] tls keys in raw config text.
func setTLSKeys(raw []byte, mode, certFile, keyFile string) []byte {
	raw = setConfigKey(raw, "web", "tls", tomlString(mode))
	raw = setConfigKey(raw, "web", "cert_file", tomlString(certFile))
	raw = setConfigKey(raw, "web", "key_file", tomlString(keyFile))
	return raw
}

// pinConfig is applied to every config text written past the
// manager (PUT /api/config, settings import, preset apply): the tls keys
// are the manager's, so a stale copy of the file in the curve editor or
// an imported bundle cannot silently revert an upload or a reset. Not
// applied while the effective mode came from a --listen override
// (ownsConfig=false) — then the file's own values are the operator's.
func (m *tlsManager) pinConfig(raw []byte) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.ownsConfig {
		return raw
	}
	// Text that already yields the manager's values is left byte-for-byte
	// (a file that relies on the default gets no explicit keys added).
	if cfg, _, err := config.Parse(raw); err == nil {
		if cfg.Web.TLS == m.mode && cfg.Web.CertFile == m.certFile && cfg.Web.KeyFile == m.keyFile {
			return raw
		}
	}
	return setTLSKeys(raw, m.mode, m.certFile, m.keyFile)
}
