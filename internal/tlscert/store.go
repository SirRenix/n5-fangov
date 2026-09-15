package tlscert

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
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

// ValidatePair checks an uploaded certificate/key pair before it is
// stored: both must be PEM, the key must belong to the leaf, the leaf must
// be within its validity period. Anything a browser would still accept
// but the operator should know about comes back as warnings: a SAN list
// that lacks one of hosts (the addresses the dashboard is reached by), an
// expiry within ExpiresSoon, a weak key, a certificate without SANs.
func ValidatePair(certPEM, keyPEM []byte, hosts []string) (tls.Certificate, []string, error) {
	if block, _ := pem.Decode(certPEM); block == nil || block.Type != "CERTIFICATE" {
		return tls.Certificate{}, nil, errors.New("certificate: no PEM CERTIFICATE block")
	}
	if block, _ := pem.Decode(keyPEM); block == nil || !strings.Contains(block.Type, "PRIVATE KEY") {
		return tls.Certificate{}, nil, errors.New("key: no PEM PRIVATE KEY block")
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("certificate/key pair: %w", err)
	}
	if cert.Leaf == nil {
		cert.Leaf, err = x509.ParseCertificate(cert.Certificate[0])
		if err != nil {
			return tls.Certificate{}, nil, fmt.Errorf("certificate: %w", err)
		}
	}
	leaf := cert.Leaf
	now := time.Now()
	if now.After(leaf.NotAfter) {
		return tls.Certificate{}, nil, fmt.Errorf("certificate expired %s", leaf.NotAfter.UTC().Format("2006-01-02"))
	}
	if now.Before(leaf.NotBefore) {
		return tls.Certificate{}, nil, fmt.Errorf("certificate not valid before %s", leaf.NotBefore.UTC().Format("2006-01-02"))
	}
	var warns []string
	if left := leaf.NotAfter.Sub(now); left < ExpiresSoon {
		warns = append(warns, fmt.Sprintf("certificate expires in %d days (%s)", int(left.Hours()/24), leaf.NotAfter.UTC().Format("2006-01-02")))
	}
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
	switch k := leaf.PublicKey.(type) {
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			warns = append(warns, fmt.Sprintf("weak key: RSA %d bits (2048 or more expected)", k.N.BitLen()))
		}
	case *ecdsa.PublicKey:
		if k.Curve.Params().BitSize < 256 {
			warns = append(warns, "weak key: "+keyAlgo(k))
		}
	}
	if leaf.SignatureAlgorithm == x509.SHA1WithRSA || leaf.SignatureAlgorithm == x509.ECDSAWithSHA1 || leaf.SignatureAlgorithm == x509.MD5WithRSA {
		warns = append(warns, "weak signature: "+leaf.SignatureAlgorithm.String()+" (browsers refuse it)")
	}
	return cert, warns, nil
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
// valid). Without a loadable key it behaves like Regenerate.
func Reissue(o Options) (tls.Certificate, error) {
	_, keyPath := Paths(o.Dir)
	if key := loadECDSAKey(keyPath); key != nil {
		o.logf("tlscert: reissuing certificate in %s, key unchanged", o.Dir)
		return generate(o, key)
	}
	o.logf("tlscert: no reusable key in %s, generating a new pair", o.Dir)
	return generate(o, nil)
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
