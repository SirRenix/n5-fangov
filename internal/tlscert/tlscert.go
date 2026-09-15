// Package tlscert creates, stores and loads the daemon's TLS certificate
// (DESIGN.md "v0.2 contract", internal/tlscert). The automatic certificate is
// a self-signed ECDSA P-256 leaf valid for ten years whose subject alternative
// names cover the configured hosts plus localhost, 127.0.0.1 and ::1. It is
// meant to be exported once (`n5-fangov cert export`) and trusted in the
// browser or OS store; the daemon never talks to a CA.
package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File names inside Options.Dir.
const (
	CertFile = "cert.pem"
	KeyFile  = "key.pem"
)

// Validity of an automatic certificate.
const Validity = 10 * 365 * 24 * time.Hour

// DefaultOrg is the certificate organisation and the fallback common name.
const DefaultOrg = "n5-fangov"

// Options describes the automatic certificate.
type Options struct {
	// Dir holds cert.pem and key.pem (created 0700 when missing).
	Dir string
	// Hosts become subject alternative names: IP literals go to IPAddresses,
	// everything else to DNSNames. A ":port" suffix is stripped, wildcard
	// addresses (0.0.0.0, ::) and "*" are ignored. localhost, 127.0.0.1 and
	// ::1 are always added.
	Hosts []string
	// Org is the certificate organisation; "" → DefaultOrg.
	Org string
	// Logf receives one line when a certificate is (re)generated; nil → log.Printf.
	Logf func(format string, args ...any)
}

func (o Options) logf(format string, args ...any) {
	if o.Logf != nil {
		o.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Paths returns the certificate and key file paths inside dir.
func Paths(dir string) (certPath, keyPath string) {
	return filepath.Join(dir, CertFile), filepath.Join(dir, KeyFile)
}

// EnsureAuto returns the automatic certificate from o.Dir, creating it when
// missing. An existing pair is reused when it loads, is within its validity
// period and its SANs cover every requested host; otherwise it is
// regenerated and one log line says why. When only the SANs changed, the
// existing private key is kept (M5): the certificate is a trust anchor in
// browsers and OS stores, and a key that stays the same is what lets an
// imported trust survive a renamed host or a new address. The second
// result is the certificate path (for the "trust this file" hint).
func EnsureAuto(o Options) (tls.Certificate, string, error) {
	certPath, keyPath := Paths(o.Dir)
	want := sanSet(o.Hosts)
	cert, err := LoadFiles(certPath, keyPath)
	var keep *ecdsa.PrivateKey
	switch {
	case err == nil:
		reason, sanOnly := reuseProblem(cert.Leaf, want)
		if reason == "" {
			return cert, certPath, nil
		}
		if k, ok := cert.PrivateKey.(*ecdsa.PrivateKey); ok && sanOnly {
			keep = k
			o.logf("tlscert: certificate regenerated (SANs changed: %s), key unchanged", reason)
		} else {
			o.logf("tlscert: regenerating %s: %s", certPath, reason)
		}
	case errors.Is(err, os.ErrNotExist):
		o.logf("tlscert: no certificate in %s, generating a self-signed one (SANs: %s)", o.Dir, want)
	default:
		o.logf("tlscert: regenerating %s: %v", certPath, err)
	}
	c, err := generate(o, keep)
	return c, certPath, err
}

// LoadFiles loads a PEM certificate/key pair (tls = "file" or the automatic
// pair) with Leaf parsed. A missing file wraps os.ErrNotExist.
func LoadFiles(certFile, keyFile string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlscert: load %s / %s: %w", certFile, keyFile, err)
	}
	if cert.Leaf == nil {
		leaf, err := x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("tlscert: parse %s: %w", certFile, err)
		}
		cert.Leaf = leaf
	}
	return cert, nil
}

// ExportPEM returns the certificate block of dir/cert.pem (never the key),
// re-encoded so the output is exactly one CERTIFICATE block.
func ExportPEM(dir string) ([]byte, error) {
	certPath, _ := Paths(dir)
	raw, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("tlscert: read %s: %w", certPath, err)
	}
	for rest := raw; len(rest) > 0; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes}), nil
		}
	}
	return nil, fmt.Errorf("tlscert: %s contains no CERTIFICATE block", certPath)
}

// Regenerate creates a new self-signed ECDSA P-256 certificate with a new
// key for o and writes cert.pem and key.pem (0600) into o.Dir, replacing
// any existing pair.
func Regenerate(o Options) (tls.Certificate, error) { return generate(o, nil) }

// generate builds the certificate; key nil → a fresh P-256 key.
func generate(o Options, key *ecdsa.PrivateKey) (tls.Certificate, error) {
	want := sanSet(o.Hosts)
	org := o.Org
	if org == "" {
		org = DefaultOrg
	}
	// CN: the first configured DNS name, else the organisation.
	cn := org
	for _, d := range want.dns {
		if d != "localhost" {
			cn = d
			break
		}
	}
	if key == nil {
		var err error
		key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return tls.Certificate{}, fmt.Errorf("tlscert: generate key: %w", err)
		}
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlscert: serial: %w", err)
	}
	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn, Organization: []string{org}},
		NotBefore:    now.Add(-time.Hour), // clock skew between generator and browser
		NotAfter:     now.Add(Validity),
		// CertSign + IsCA: browsers and OS stores accept a self-signed
		// certificate as a trust anchor only when it is marked as a CA.
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              want.dns,
		IPAddresses:           want.ips,
		// M1: a trust anchor in a browser store can sign for any name.
		// Name constraints pin this one to exactly its own SANs, and
		// MaxPathLen 0 forbids intermediates: even with the key in hand
		// nobody can mint a certificate for another host that the
		// store would accept.
		MaxPathLen:                  0,
		MaxPathLenZero:              true,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         want.dns,
		PermittedIPRanges:           hostRanges(want.ips),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlscert: create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlscert: marshal key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return tls.Certificate{}, fmt.Errorf("tlscert: create %s: %w", o.Dir, err)
	}
	certPath, keyPath := Paths(o.Dir)
	if err := writePrivate(keyPath, keyPEM); err != nil {
		return tls.Certificate{}, err
	}
	if err := writePrivate(certPath, certPEM); err != nil {
		return tls.Certificate{}, err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlscert: pair: %w", err)
	}
	if cert.Leaf == nil {
		cert.Leaf, _ = x509.ParseCertificate(der)
	}
	o.logf("tlscert: wrote %s (CN %q, SANs: %s, valid until %s)", certPath, cn, want, tmpl.NotAfter.Format("2006-01-02"))
	return cert, nil
}

// hostRanges turns single addresses into /32 (IPv4) or /128 (IPv6)
// networks for the name constraints.
func hostRanges(ips []net.IP) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(ips))
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			out = append(out, &net.IPNet{IP: v4, Mask: net.CIDRMask(32, 32)})
		} else {
			out = append(out, &net.IPNet{IP: ip.To16(), Mask: net.CIDRMask(128, 128)})
		}
	}
	return out
}

// CheckKeyMode reports an error when the private key file at path is
// readable by group or others (L8). A missing file is not reported here;
// LoadFiles does that.
func CheckKeyMode(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if m := st.Mode().Perm(); m&0o077 != 0 {
		return fmt.Errorf("tlscert: %s is mode %04o, readable by group/others; run: chmod 0600 %s", path, m, path)
	}
	return nil
}

// writePrivate writes data to path with mode 0600 via an unpredictable
// temp file in the same directory (os.CreateTemp creates it 0600) and a
// rename (L3).
func writePrivate(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("tlscert: write %s: %w", path, err)
	}
	tmp := f.Name()
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("tlscert: write %s: %w", path, err)
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("tlscert: write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("tlscert: write %s: %w", path, err)
	}
	return nil
}

// sans is the normalised SAN set requested by Options.Hosts.
type sans struct {
	dns []string
	ips []net.IP
}

func (s sans) String() string {
	var parts []string
	parts = append(parts, s.dns...)
	for _, ip := range s.ips {
		parts = append(parts, ip.String())
	}
	return strings.Join(parts, ", ")
}

// sanSet parses hosts into DNS names and IPs, always adding the loopback
// names, without duplicates and in a stable order.
func sanSet(hosts []string) sans {
	var out sans
	seenDNS := map[string]bool{}
	add := func(h string) {
		h = strings.TrimSpace(h)
		if hp, _, err := net.SplitHostPort(h); err == nil && hp != "" {
			h = hp
		}
		h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
		h = strings.ToLower(strings.TrimSuffix(h, "."))
		if h == "" || h == "*" {
			return
		}
		if ip := net.ParseIP(h); ip != nil {
			if ip.IsUnspecified() {
				return
			}
			for _, have := range out.ips {
				if have.Equal(ip) {
					return
				}
			}
			out.ips = append(out.ips, ip)
			return
		}
		if seenDNS[h] {
			return
		}
		seenDNS[h] = true
		out.dns = append(out.dns, h)
	}
	for _, h := range hosts {
		add(h)
	}
	add("localhost")
	add("127.0.0.1")
	add("::1")
	return out
}

// reuseProblem reports why leaf cannot serve the requested SANs ("" =
// fine). sanOnly is true when the only problem is a missing SAN: then the
// key may be kept (see EnsureAuto).
func reuseProblem(leaf *x509.Certificate, want sans) (reason string, sanOnly bool) {
	if leaf == nil {
		return "certificate not parsed", false
	}
	now := time.Now()
	if now.After(leaf.NotAfter) {
		return "certificate expired " + leaf.NotAfter.Format("2006-01-02"), false
	}
	if now.Before(leaf.NotBefore) {
		return "certificate not valid before " + leaf.NotBefore.Format("2006-01-02"), false
	}
	for _, d := range want.dns {
		found := false
		for _, have := range leaf.DNSNames {
			if strings.EqualFold(have, d) {
				found = true
				break
			}
		}
		if !found {
			return "SAN list lacks name " + d, true
		}
	}
	for _, ip := range want.ips {
		found := false
		for _, have := range leaf.IPAddresses {
			if have.Equal(ip) {
				found = true
				break
			}
		}
		if !found {
			return "SAN list lacks IP " + ip.String(), true
		}
	}
	return "", false
}
