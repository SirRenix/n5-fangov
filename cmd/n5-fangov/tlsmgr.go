package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/SirRenix/n5-fangov/internal/tlscert"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// Uploaded pair inside the tls directory (next to the automatic cert.pem
// and key.pem, which stay in place so "back to auto" is instant).
const (
	customCertFile = "custom-cert.pem"
	customKeyFile  = "custom-key.pem"
)

// tlsManager implements web.TLSMgr: it owns the certificate the listener
// serves (through a tlscert.Store), the tls directory and the [web] tls
// keys of the config file. serve builds it with create=true (the
// automatic pair is made at start); the CLI builds it with create=false
// for the offline path when the daemon is not running.
//
// mode is the effective [web].tls: "auto", "file" or "off". In "file" mode
// certFile/keyFile are the configured paths — the uploaded pair or
// something the operator pointed the config at by hand.
type tlsManager struct {
	mu       sync.Mutex
	cfgPath  string
	dir      string
	hosts    []string
	org      string
	mode     string
	certFile string
	keyFile  string
	store    *tlscert.Store
	logf     func(string, ...any)
}

// newTLSManager captures the wiring; load fills the store.
func newTLSManager(cfgPath string, w webSpec, hosts []string) *tlsManager {
	return &tlsManager{
		cfgPath: cfgPath, dir: tlsDir(cfgPath), hosts: hosts, org: setupOrg,
		mode: w.TLS, certFile: w.CertFile, keyFile: w.KeyFile, store: &tlscert.Store{}, logf: log.Printf,
	}
}

// load reads (create=false) or ensures (create=true) the certificate for
// the current mode into the store. Mode "off" loads nothing. The second
// result is the path of the served certificate for the start-up log line.
func (m *tlsManager) load(create bool) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.mode {
	case "off":
		return "", nil
	case "file":
		cert, err := tlscert.LoadFiles(m.certFile, m.keyFile)
		if err != nil {
			return "", err
		}
		m.store.Set(cert)
		return m.certFile, nil
	}
	certPath, keyPath := tlscert.Paths(m.dir)
	if !create {
		cert, err := tlscert.LoadFiles(certPath, keyPath)
		if err != nil {
			return "", err
		}
		m.store.Set(cert)
		return certPath, nil
	}
	cert, certPath, err := tlscert.EnsureAuto(m.options())
	if err != nil {
		return "", err
	}
	m.store.Set(cert)
	return certPath, nil
}

func (m *tlsManager) options() tlscert.Options {
	return tlscert.Options{Dir: m.dir, Hosts: m.hosts, Org: m.org, Logf: m.logf}
}

// Mode returns the effective tls mode.
func (m *tlsManager) Mode() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

// Store is the certificate source for web.Server.ServeTLSStore.
func (m *tlsManager) Store() *tlscert.Store { return m.store }

func (m *tlsManager) Info() (tlscert.InfoData, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.mode == "off" {
		return tlscert.InfoData{}, "off", nil
	}
	cur := m.store.Current()
	if len(cur.Certificate) == 0 {
		return tlscert.InfoData{}, m.mode, errors.New("no certificate loaded")
	}
	return tlscert.Info(cur), m.mode, nil
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
// new pair is made. Refused in "file" mode: the served certificate is the
// operator's, use ResetAuto first.
func (m *tlsManager) Regenerate(keepKey bool) (tlscert.InfoData, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch m.mode {
	case "off":
		return tlscert.InfoData{}, web.ErrTLSOff
	case "file":
		return tlscert.InfoData{}, web.ErrTLSFileMode
	}
	var cert tls.Certificate
	var err error
	if keepKey {
		cert, err = tlscert.Reissue(m.options())
	} else {
		cert, err = tlscert.Regenerate(m.options())
	}
	if err != nil {
		return tlscert.InfoData{}, err
	}
	m.store.Set(cert)
	return tlscert.Info(cert), nil
}

// Upload validates the pair against the listen hosts, stores it as
// custom-cert.pem / custom-key.pem (0600, atomic), points the config at it
// (tls = "file", comments preserved) and serves it from the next
// handshake on. The config is written before the swap: a daemon restart
// then comes up with the same certificate.
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
	if err := tlscert.WritePrivate(keyPath, keyPEM); err != nil {
		return tlscert.InfoData{}, nil, err
	}
	if err := tlscert.WritePrivate(certPath, certPEM); err != nil {
		return tlscert.InfoData{}, nil, err
	}
	if err := m.writeMode("file", certPath, keyPath); err != nil {
		return tlscert.InfoData{}, nil, err
	}
	m.mode, m.certFile, m.keyFile = "file", certPath, keyPath
	m.store.Set(cert)
	return tlscert.Info(cert), warns, nil
}

// ResetAuto returns to the automatic certificate: EnsureAuto (reusing the
// existing pair when it still fits), config back to tls = "auto", the
// uploaded pair deleted.
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
	if err := m.writeMode("auto", "", ""); err != nil {
		return tlscert.InfoData{}, err
	}
	m.mode, m.certFile, m.keyFile = "auto", "", ""
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
	raw = setConfigKey(raw, "web", "tls", tomlString(mode))
	raw = setConfigKey(raw, "web", "cert_file", tomlString(certFile))
	raw = setConfigKey(raw, "web", "key_file", tomlString(keyFile))
	if err := saveConfig(m.cfgPath, raw); err != nil {
		return fmt.Errorf("write %s: %w", m.cfgPath, err)
	}
	return nil
}
