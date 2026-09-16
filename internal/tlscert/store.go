package tlscert

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// Store holds the certificate the TLS listener serves and lets the daemon
// swap it without a restart: tls.Config.GetCertificate reads the pointer
// per handshake, so a Set affects new connections only — established
// sessions, the HTTP server and the controller never notice.
type Store struct {
	cur atomic.Pointer[tls.Certificate]
}

// NewStore returns a Store serving cert.
func NewStore(cert tls.Certificate) *Store {
	s := &Store{}
	s.Set(cert)
	return s
}

// Get is the tls.Config.GetCertificate callback.
func (s *Store) Get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if c := s.cur.Load(); c != nil {
		return c, nil
	}
	return nil, errors.New("tlscert: store is empty")
}

// Set replaces the served certificate. A copy is stored, so the caller
// may keep mutating its value.
func (s *Store) Set(cert tls.Certificate) {
	c := cert
	if c.Leaf == nil && len(c.Certificate) > 0 {
		c.Leaf, _ = x509.ParseCertificate(c.Certificate[0])
	}
	s.cur.Store(&c)
}

// Current returns the served certificate (zero value while empty).
func (s *Store) Current() tls.Certificate {
	if c := s.cur.Load(); c != nil {
		return *c
	}
	return tls.Certificate{}
}

// InfoData describes a certificate for the dashboard and the CLI.
type InfoData struct {
	Subject           string    `json:"subject"`
	Issuer            string    `json:"issuer"`
	DNSNames          []string  `json:"dns_names"`
	IPs               []string  `json:"ips"`
	NotBefore         time.Time `json:"not_before"`
	NotAfter          time.Time `json:"not_after"`
	FingerprintSHA256 string    `json:"fingerprint_sha256"` // colon-separated upper-case hex
	IsCA              bool      `json:"is_ca"`
	KeyAlgo           string    `json:"key_algo"` // "ECDSA P-256", "RSA 2048", "Ed25519"
	SerialHex         string    `json:"serial_hex"`
}

// Info extracts InfoData from the leaf of cert. A certificate whose leaf
// does not parse yields the zero value.
func Info(cert tls.Certificate) InfoData {
	leaf := cert.Leaf
	if leaf == nil && len(cert.Certificate) > 0 {
		leaf, _ = x509.ParseCertificate(cert.Certificate[0])
	}
	if leaf == nil {
		return InfoData{}
	}
	sum := sha256.Sum256(leaf.Raw)
	fp := make([]string, len(sum))
	for i, b := range sum {
		fp[i] = fmt.Sprintf("%02X", b)
	}
	ips := make([]string, 0, len(leaf.IPAddresses))
	for _, ip := range leaf.IPAddresses {
		ips = append(ips, ip.String())
	}
	dns := append([]string{}, leaf.DNSNames...)
	return InfoData{
		Subject:           leaf.Subject.String(),
		Issuer:            leaf.Issuer.String(),
		DNSNames:          dns,
		IPs:               ips,
		NotBefore:         leaf.NotBefore.UTC(),
		NotAfter:          leaf.NotAfter.UTC(),
		FingerprintSHA256: strings.Join(fp, ":"),
		IsCA:              leaf.IsCA,
		KeyAlgo:           keyAlgo(leaf.PublicKey),
		SerialHex:         strings.ToUpper(hex.EncodeToString(leaf.SerialNumber.Bytes())),
	}
}

func keyAlgo(pub any) string {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		return "ECDSA " + k.Curve.Params().Name
	case *rsa.PublicKey:
		return fmt.Sprintf("RSA %d", k.N.BitLen())
	case ed25519.PublicKey:
		return "Ed25519"
	}
	return fmt.Sprintf("%T", pub)
}

// ExpiresSoon is the window in which Info consumers flag the expiry and
// ValidatePair warns.
const ExpiresSoon = 30 * 24 * time.Hour

// ErrEncryptedKey is returned by ValidatePair when the key PEM is
// password-protected (PKCS#8 ENCRYPTED PRIVATE KEY or a legacy
// "Proc-Type: 4,ENCRYPTED" block). The daemon has no way to ask for the
// passphrase.
var ErrEncryptedKey = errors.New("encrypted private keys are not supported — decrypt with openssl first (openssl pkey -in key.pem -out key-plain.pem)")

// ValidatePair checks an uploaded certificate/key pair before it is
// stored: the certificate PEM must carry at least one CERTIFICATE block
// (the leaf first, intermediates after it; other block types are
// skipped), the key PEM one unencrypted PRIVATE KEY block (PKCS#8, PKCS#1
// "RSA PRIVATE KEY" or SEC 1 "EC PRIVATE KEY"; "EC PARAMETERS" and the
// like are skipped, an encrypted key is ErrEncryptedKey). The key must
// belong to the leaf, the leaf must be within its validity period, and
// the pair must survive a handshake against ServerConfig: ECDSA on a
// curve other than P-256/P-384/P-521 and RSA below 1024 bits are refused
// before that with a clear message, since crypto/tls cannot sign with
// them. Anything a browser would still accept but the operator should
// know about comes back as warnings: a SAN list that lacks one of hosts
// (the addresses the dashboard is reached by), an expiry within
// ExpiresSoon, a weak key (RSA < 2048), a weak signature, a certificate
// without SANs. The returned certificate carries the whole chain.
func ValidatePair(certPEM, keyPEM []byte, hosts []string) (tls.Certificate, []string, error) {
	certs := pemBlocks(certPEM, func(t string) bool { return t == "CERTIFICATE" })
	if len(certs) == 0 {
		return tls.Certificate{}, nil, errors.New("certificate: no PEM CERTIFICATE block")
	}
	// the first PRIVATE KEY block counts; an encrypted one is refused
	keys := pemBlocks(keyPEM, func(t string) bool { return strings.HasSuffix(t, "PRIVATE KEY") })
	if len(keys) == 0 {
		return tls.Certificate{}, nil, errors.New("key: no PEM PRIVATE KEY block")
	}
	keyBlock := keys[0]
	if keyBlock.Type == "ENCRYPTED PRIVATE KEY" || strings.Contains(keyBlock.Headers["Proc-Type"], "ENCRYPTED") {
		return tls.Certificate{}, nil, fmt.Errorf("key: %w", ErrEncryptedKey)
	}
	leaf, err := x509.ParseCertificate(certs[0].Bytes)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("certificate: %w", err)
	}
	if err := usableKey(leaf.PublicKey); err != nil {
		return tls.Certificate{}, nil, err
	}
	// Re-encoded: only the CERTIFICATE blocks and the one key block reach
	// crypto/tls, whatever else the files carried.
	var chain []byte
	for _, b := range certs {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b.Bytes})...)
	}
	cert, err := tls.X509KeyPair(chain, pem.EncodeToMemory(&pem.Block{Type: keyBlock.Type, Bytes: keyBlock.Bytes}))
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("certificate/key pair: %w", err)
	}
	cert.Leaf = leaf
	now := time.Now()
	if now.After(leaf.NotAfter) {
		return tls.Certificate{}, nil, fmt.Errorf("certificate expired %s", leaf.NotAfter.UTC().Format("2006-01-02"))
	}
	if now.Before(leaf.NotBefore) {
		return tls.Certificate{}, nil, fmt.Errorf("certificate not valid before %s", leaf.NotBefore.UTC().Format("2006-01-02"))
	}
	if err := CheckUsable(cert); err != nil {
		return tls.Certificate{}, nil, err
	}
	var warns []string
	warns = append(warns, expiryWarnings(leaf.NotAfter, now)...)
	if len(leaf.DNSNames) == 0 && len(leaf.IPAddresses) == 0 {
		warns = append(warns, "certificate has no subject alternative names; browsers ignore the CN and will not match any host")
	}
	for _, h := range hosts {
		h = normalizeHost(h)
		if h == "" {
			continue
		}
		if leaf.VerifyHostname(h) != nil {
			warns = append(warns, "SAN list lacks host "+h)
		}
	}
	if k, ok := leaf.PublicKey.(*rsa.PublicKey); ok && k.N.BitLen() < 2048 {
		warns = append(warns, fmt.Sprintf("weak key: RSA %d bits (2048 or more expected)", k.N.BitLen()))
	}
	if leaf.SignatureAlgorithm == x509.SHA1WithRSA || leaf.SignatureAlgorithm == x509.ECDSAWithSHA1 || leaf.SignatureAlgorithm == x509.MD5WithRSA {
		warns = append(warns, "weak signature: "+leaf.SignatureAlgorithm.String()+" (browsers refuse it)")
	}
	return cert, warns, nil
}

// pemBlocks returns every PEM block of raw whose type satisfies want, in
// order; undecodable trailing bytes end the scan.
func pemBlocks(raw []byte, want func(string) bool) []*pem.Block {
	var out []*pem.Block
	for rest := raw; len(rest) > 0; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		if want(b.Type) {
			out = append(out, b)
		}
	}
	return out
}

// usableKey refuses public keys crypto/tls has no signature scheme
// for: ECDSA outside P-256/P-384/P-521, RSA below 1024 bits (Go 1.24
// refuses to sign with those). Everything else (including unknown types)
// is left to CheckUsable.
func usableKey(pub any) error {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		switch k.Curve {
		case elliptic.P256(), elliptic.P384(), elliptic.P521():
			return nil
		}
		return fmt.Errorf("certificate/key cannot be used by this server: %s (TLS supports ECDSA on P-256, P-384 and P-521 only)", keyAlgo(k))
	case *rsa.PublicKey:
		if k.N.BitLen() < 1024 {
			return fmt.Errorf("certificate/key cannot be used by this server: RSA %d bits (1024 bits minimum, 2048 recommended)", k.N.BitLen())
		}
	}
	return nil
}

// DaysLeft is the number of days until notAfter, rounded up: a
// certificate that expires tomorrow afternoon has "1 day" left, one that
// expired an hour ago "0 days" (negative beyond a full day).
func DaysLeft(notAfter, now time.Time) int {
	return int(math.Ceil(notAfter.Sub(now).Hours() / 24))
}

// expiryWarnings: expired, or expires within ExpiresSoon.
func expiryWarnings(notAfter, now time.Time) []string {
	date := notAfter.UTC().Format("2006-01-02")
	switch left := notAfter.Sub(now); {
	case left < 0:
		return []string{"certificate expired " + date}
	case left < ExpiresSoon:
		return []string{fmt.Sprintf("certificate expires in %d days (%s)", DaysLeft(notAfter, now), date)}
	}
	return nil
}

// Warnings computes the operator warnings for a served certificate the
// way ValidatePair does, from its InfoData (GET /api/tls): expiry
// within ExpiresSoon or past, no SANs, and every host of hosts the SAN
// list does not cover. localhost, 127.0.0.1 and ::1 are not checked — the
// operator's own certificate is for the LAN name, and the loopback names
// only matter to the automatic one, which always carries them.
func Warnings(info InfoData, hosts []string, now time.Time) []string {
	if info.FingerprintSHA256 == "" {
		return nil
	}
	var warns []string
	warns = append(warns, expiryWarnings(info.NotAfter, now)...)
	if len(info.DNSNames) == 0 && len(info.IPs) == 0 {
		warns = append(warns, "certificate has no subject alternative names; browsers ignore the CN and will not match any host")
	}
	for _, h := range MissingHosts(info, hosts) {
		warns = append(warns, "SAN list lacks host "+h)
	}
	return warns
}

// MissingHosts returns the hosts (normalised, loopback and wildcards
// skipped) that the SANs of info do not cover, with x509's own matching
// rules (wildcards, IP literals).
func MissingHosts(info InfoData, hosts []string) []string {
	probe := &x509.Certificate{DNSNames: info.DNSNames}
	for _, s := range info.IPs {
		if ip := net.ParseIP(s); ip != nil {
			probe.IPAddresses = append(probe.IPAddresses, ip)
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, h := range hosts {
		h = normalizeHost(h)
		if h == "" || seen[h] || isLoopbackName(h) {
			continue
		}
		seen[h] = true
		if probe.VerifyHostname(h) != nil {
			out = append(out, h)
		}
	}
	return out
}

func isLoopbackName(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// normalizeHost strips a port and IPv6 brackets and lowercases; "" for
// wildcards and unspecified addresses.
func normalizeHost(h string) string {
	h = strings.TrimSpace(h)
	if hp, _, err := net.SplitHostPort(h); err == nil && hp != "" {
		h = hp
	}
	h = strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
	if h == "*" {
		return ""
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsUnspecified() {
		return ""
	}
	return h
}

// DER returns the leaf certificate in DER form (the .cer download for
// Windows and Android).
func DER(cert tls.Certificate) ([]byte, error) {
	if len(cert.Certificate) == 0 {
		return nil, errors.New("tlscert: empty certificate")
	}
	return cert.Certificate[0], nil
}

// PEM returns exactly one CERTIFICATE block for the leaf of cert.
func PEM(cert tls.Certificate) ([]byte, error) {
	der, err := DER(cert)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

// Reissue creates a new self-signed certificate for o with the private key
// of the existing pair in o.Dir (the trust an operator imported stays
// valid). Without a loadable key it behaves like Regenerate and reports
// kept=false, so the caller can tell the operator that the imported
// trust is gone.
func Reissue(o Options) (cert tls.Certificate, kept bool, err error) {
	_, keyPath := Paths(o.Dir)
	if key := loadECDSAKey(keyPath); key != nil {
		o.logf("tlscert: reissuing certificate in %s, key unchanged", o.Dir)
		cert, err = generate(o, key)
		return cert, err == nil, err
	}
	o.logf("tlscert: no reusable key in %s, generating a new pair", o.Dir)
	cert, err = generate(o, nil)
	return cert, false, err
}

// loadECDSAKey returns the P-256 key stored at path, nil when absent or
// of another type.
func loadECDSAKey(path string) *ecdsa.PrivateKey {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	for rest := raw; len(rest) > 0; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if !strings.Contains(block.Type, "PRIVATE KEY") {
			continue
		}
		if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
			if ek, ok := k.(*ecdsa.PrivateKey); ok {
				return ek
			}
		}
		if ek, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
			return ek
		}
	}
	return nil
}

// WritePrivate writes data to path with mode 0600 atomically (temp file in
// the same directory + rename). Exported for the custom-certificate
// upload, which stores the operator's pair next to the automatic one.
func WritePrivate(path string, data []byte) error { return writePrivate(path, data) }
