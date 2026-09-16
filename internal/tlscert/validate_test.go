package tlscert

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"
)

// RSA-512 pair (openssl req -x509 -newkey rsa:512, valid to 2126): Go 1.24
// refuses to generate or sign with such a key, so the fixture is static.
const (
	rsa512Cert = `-----BEGIN CERTIFICATE-----
MIIBjjCCATigAwIBAgIURQpZnIMxmtGzskCz4Z7recsZCHMwDQYJKoZIhvcNAQEL
BQAwETEPMA0GA1UEAwwGcnNhNTEyMCAXDTI2MDkxNTIzMTgwMloYDzIxMjYwODIy
MjMxODAyWjARMQ8wDQYDVQQDDAZyc2E1MTIwXDANBgkqhkiG9w0BAQEFAANLADBI
AkEA0bWdacevMA3IOE3wMuF+QQanvhMlseegLFLvysaFmBBDFSc1JiiflmCONGvq
oDxhAxp3B+bXHOBzDwXoCW9NiQIDAQABo2YwZDAdBgNVHQ4EFgQUC6a74Oykpt6m
rUebVaQI88Ag5m4wHwYDVR0jBBgwFoAUC6a74Oykpt6mrUebVaQI88Ag5m4wDwYD
VR0TAQH/BAUwAwEB/zARBgNVHREECjAIggZyc2E1MTIwDQYJKoZIhvcNAQELBQAD
QQDNlfct+q/U2Y4PXlQyes7l2yhremiyucmK1AgD0rHsmmPNZYvGsUjB+/cLGRrx
65bKKmNvI8iAVAujSpu3P4rI
-----END CERTIFICATE-----
`
	rsa512Key = `-----BEGIN PRIVATE KEY-----
MIIBVgIBADANBgkqhkiG9w0BAQEFAASCAUAwggE8AgEAAkEA0bWdacevMA3IOE3w
MuF+QQanvhMlseegLFLvysaFmBBDFSc1JiiflmCONGvqoDxhAxp3B+bXHOBzDwXo
CW9NiQIDAQABAkEAkpk5X5ceGqOn0eR6A7eqwN5cKP3Nnh5j1FhuFPzOq0t+1dcu
tlOrw/or6G1/lyUZ6gED2dKW+hzwhAgx3KM/OQIhAO/JJbyj5ZX735iXrHCCMtr4
c3QVEMht6ojQgjhUvziXAiEA3+PTlJ5G6jluwUYEa1nKFbhtmmg48iulwor/E7Y5
Tt8CIHQqmNOo+2MMISkF4g6npQecciJ8yiKvzX32tf+gXvuFAiEAh+T+UM/9VUAF
BNUd65b1fVeTV0x5fCyYEUxS5UEO6dsCIQCYfkOIvPr+C6VsVPCc5I9pni/ssfK4
Jf/VChRvYh09WQ==
-----END PRIVATE KEY-----
`
)

// newKey makes a private key of the given kind: "p224", "p256", "p384",
// "rsa2048", "ed25519".
func newKey(t *testing.T, kind string) crypto.Signer {
	t.Helper()
	var k crypto.Signer
	var err error
	switch kind {
	case "p224":
		k, err = ecdsa.GenerateKey(elliptic.P224(), rand.Reader)
	case "p256":
		k, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	case "p384":
		k, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	case "rsa2048":
		k, err = rsa.GenerateKey(rand.Reader, 2048)
	case "ed25519":
		_, k, err = ed25519.GenerateKey(rand.Reader)
	default:
		t.Fatalf("unknown key kind %q", kind)
	}
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// issue signs tmpl for pub with parent/parentKey (self-signed when parent
// is nil) and returns the DER (signCert in helpers_test.go; certPEM and
// pkcs8PEM live there as well).
func issue(t *testing.T, tmpl *x509.Certificate, pub any, parent *x509.Certificate, parentKey crypto.Signer) []byte {
	t.Helper()
	return signCert(t, tmpl, pub, parent, parentKey)
}

// leafTmpl is a server-auth template whose CN is also its only DNS SAN.
func leafTmpl(cn string) *x509.Certificate { return sanTemplate(cn, cn) }

// serveOnce runs one real TLS connection against ServerConfig with cert
// and returns the peer chain the client saw.
func serveOnce(t *testing.T, cert tls.Certificate) []*x509.Certificate {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", ServerConfig(func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &cert, nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		if tc, ok := c.(*tls.Conn); ok {
			_ = tc.Handshake()
		}
		c.Close()
	}()
	c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("handshake against ServerConfig: %v", err)
	}
	defer c.Close()
	return c.ConnectionState().PeerCertificates
}

// TestValidatePairKeyTypes (M1): key types crypto/tls cannot sign with
// are refused with a clear message (ECDSA P-224, RSA 512), the usable
// ones pass and survive a real handshake against ServerConfig; a chain
// with an intermediate is kept whole.
func TestValidatePairKeyTypes(t *testing.T) {
	p224 := newKey(t, "p224")
	c224 := certPEM(issue(t, leafTmpl("p224.example"), p224.Public(), nil, p224))
	_, _, err := ValidatePair(c224, pkcs8PEM(t, p224), nil)
	if err == nil || !strings.Contains(err.Error(), "cannot be used by this server") || !strings.Contains(err.Error(), "P-224") {
		t.Errorf("P-224 pair: %v", err)
	}
	_, _, err = ValidatePair([]byte(rsa512Cert), []byte(rsa512Key), nil)
	if err == nil || !strings.Contains(err.Error(), "cannot be used by this server") || !strings.Contains(err.Error(), "RSA 512") {
		t.Errorf("RSA-512 pair: %v", err)
	}
	// CheckUsable alone also fails on P-224 (the handshake has no scheme for it)
	if pair, err := tls.X509KeyPair(c224, pkcs8PEM(t, p224)); err == nil {
		if err := CheckUsable(pair); err == nil || !strings.Contains(err.Error(), "cannot be used by this server") {
			t.Errorf("CheckUsable(P-224) = %v", err)
		}
	}
	for _, kind := range []string{"rsa2048", "ed25519", "p256", "p384"} {
		k := newKey(t, kind)
		c := certPEM(issue(t, leafTmpl(kind+".example"), k.Public(), nil, k))
		cert, warns, err := ValidatePair(c, pkcs8PEM(t, k), []string{kind + ".example"})
		if err != nil || len(warns) != 0 {
			t.Errorf("%s: %v %v", kind, err, warns)
			continue
		}
		if err := CheckUsable(cert); err != nil {
			t.Errorf("%s: CheckUsable: %v", kind, err)
		}
		if peers := serveOnce(t, cert); len(peers) != 1 || peers[0].Subject.CommonName != kind+".example" {
			t.Errorf("%s: served chain %d", kind, len(peers))
		}
	}
	// chain: root → intermediate → leaf; the upload carries leaf + intermediate
	rootKey, interKey, leafKey := newKey(t, "p256"), newKey(t, "p256"), newKey(t, "p256")
	rootTmpl := &x509.Certificate{Subject: pkix.Name{CommonName: "root"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER := issue(t, rootTmpl, rootKey.Public(), nil, rootKey)
	root, _ := x509.ParseCertificate(rootDER)
	interTmpl := &x509.Certificate{Subject: pkix.Name{CommonName: "intermediate"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	interDER := issue(t, interTmpl, interKey.Public(), root, rootKey)
	inter, _ := x509.ParseCertificate(interDER)
	leafDER := issue(t, leafTmpl("chain.example"), leafKey.Public(), inter, interKey)
	cert, warns, err := ValidatePair(certPEM(leafDER, interDER), pkcs8PEM(t, leafKey), []string{"chain.example"})
	if err != nil || len(warns) != 0 {
		t.Fatalf("chain: %v %v", err, warns)
	}
	if len(cert.Certificate) != 2 || cert.Leaf == nil || cert.Leaf.Subject.CommonName != "chain.example" {
		t.Fatalf("chain kept %d certificates, leaf %v", len(cert.Certificate), cert.Leaf)
	}
	if peers := serveOnce(t, cert); len(peers) != 2 || peers[1].Subject.CommonName != "intermediate" {
		t.Errorf("served chain = %d", len(peers))
	}
	// the key must belong to the leaf, not the intermediate
	if _, _, err := ValidatePair(certPEM(leafDER, interDER), pkcs8PEM(t, interKey), nil); err == nil {
		t.Error("intermediate's key accepted for the leaf")
	}
}

// TestValidatePairPEMForms (L3): EC PARAMETERS before an EC PRIVATE KEY,
// a PKCS#1 RSA PRIVATE KEY and an Ed25519 PKCS#8 key all load; an
// encrypted key (PKCS#8 or legacy Proc-Type) is refused with the openssl
// hint; a key file without a key block and a certificate file with only
// foreign blocks are named as such.
func TestValidatePairPEMForms(t *testing.T) {
	ec := newKey(t, "p256").(*ecdsa.PrivateKey)
	ecCert := certPEM(issue(t, leafTmpl("ec.example"), ec.Public(), nil, ec))
	sec1, _ := x509.MarshalECPrivateKey(ec)
	params := pem.EncodeToMemory(&pem.Block{Type: "EC PARAMETERS", Bytes: []byte{0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07}})
	ecKey := append(params, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1})...)
	if cert, _, err := ValidatePair(ecCert, ecKey, nil); err != nil || cert.Leaf.Subject.CommonName != "ec.example" {
		t.Errorf("EC PARAMETERS + EC PRIVATE KEY: %v", err)
	}
	rk := newKey(t, "rsa2048").(*rsa.PrivateKey)
	rsaCert := certPEM(issue(t, leafTmpl("rsa.example"), rk.Public(), nil, rk))
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rk)})
	if _, _, err := ValidatePair(rsaCert, pkcs1, nil); err != nil {
		t.Errorf("PKCS#1 RSA PRIVATE KEY: %v", err)
	}
	ed := newKey(t, "ed25519")
	edCert := certPEM(issue(t, leafTmpl("ed.example"), ed.Public(), nil, ed))
	if cert, _, err := ValidatePair(edCert, pkcs8PEM(t, ed), nil); err != nil || Info(cert).KeyAlgo != "Ed25519" {
		t.Errorf("Ed25519 PKCS#8: %v", err)
	}
	// encrypted: PKCS#8 form
	enc8 := pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{0x30, 0x03, 0x02, 0x01, 0x00}})
	_, _, err := ValidatePair(ecCert, enc8, nil)
	if !errors.Is(err, ErrEncryptedKey) || !strings.Contains(err.Error(), "decrypt with openssl") {
		t.Errorf("ENCRYPTED PRIVATE KEY: %v", err)
	}
	// encrypted: legacy Proc-Type header
	legacy := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-256-CBC,00112233445566778899AABBCCDDEEFF"}, Bytes: sec1})
	if _, _, err := ValidatePair(ecCert, append(params, legacy...), nil); !errors.Is(err, ErrEncryptedKey) {
		t.Errorf("Proc-Type encrypted: %v", err)
	}
	// only parameters, no key
	if _, _, err := ValidatePair(ecCert, params, nil); err == nil || !strings.Contains(err.Error(), "no PEM PRIVATE KEY block") {
		t.Errorf("parameters only: %v", err)
	}
	// certificate file with a foreign block only
	if _, _, err := ValidatePair(params, ecKey, nil); err == nil || !strings.Contains(err.Error(), "no PEM CERTIFICATE block") {
		t.Errorf("no certificate block: %v", err)
	}
	// certificate file with a foreign block before the certificate
	if _, _, err := ValidatePair(append(params, ecCert...), ecKey, nil); err != nil {
		t.Errorf("foreign block before CERTIFICATE: %v", err)
	}
}

// TestServerConfig (L1): the listener config disables session tickets so
// a swapped certificate is what every new connection sees; the rest is
// TLS 1.2+, HTTP/2 offered, the certificate from the callback.
func TestServerConfig(t *testing.T) {
	called := 0
	cfg := ServerConfig(func(*tls.ClientHelloInfo) (*tls.Certificate, error) { called++; return nil, errors.New("x") })
	if !cfg.SessionTicketsDisabled {
		t.Error("SessionTicketsDisabled must be true")
	}
	if cfg.MinVersion != tls.VersionTLS12 || len(cfg.NextProtos) != 2 || cfg.NextProtos[0] != "h2" {
		t.Errorf("config = %+v", cfg)
	}
	if _, err := cfg.GetCertificate(nil); err == nil || called != 1 {
		t.Error("GetCertificate is not the callback")
	}
	if err := CheckUsable(tls.Certificate{}); err == nil {
		t.Error("CheckUsable of an empty certificate must fail")
	}
}

// TestDaysLeftAndWarnings (L4/L5): DaysLeft rounds up; Warnings from
// InfoData reports expiry and the uncovered hosts, skipping loopback and
// wildcards and honouring wildcard SANs and IP literals.
func TestDaysLeftAndWarnings(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in   time.Duration
		want int
	}{{25 * time.Hour, 2}, {24 * time.Hour, 1}, {time.Hour, 1}, {0, 0}, {-time.Hour, 0}, {-25 * time.Hour, -1}} {
		if got := DaysLeft(now.Add(c.in), now); got != c.want {
			t.Errorf("DaysLeft(%v) = %d, want %d", c.in, got, c.want)
		}
	}
	info := InfoData{FingerprintSHA256: "AA", DNSNames: []string{"*.lan", "n5"}, IPs: []string{"192.0.2.20"}, NotAfter: now.Add(400 * 24 * time.Hour)}
	hosts := []string{"n5.lan", "n5", "N5", "192.0.2.20", "192.0.2.21:8010", "other.example", "localhost", "127.0.0.1", "[::1]:8010", "0.0.0.0", "*", "deep.n5.lan"}
	got := Warnings(info, hosts, now)
	want := []string{"SAN list lacks host 192.0.2.21", "SAN list lacks host other.example", "SAN list lacks host deep.n5.lan"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("Warnings = %v, want %v", got, want)
	}
	info.NotAfter = now.Add(3 * 24 * time.Hour)
	if got := Warnings(info, nil, now); len(got) != 1 || got[0] != "certificate expires in 3 days ("+info.NotAfter.Format("2006-01-02")+")" {
		t.Errorf("soon = %v", got)
	}
	info.NotAfter = now.Add(-time.Hour)
	if got := Warnings(info, nil, now); len(got) != 1 || !strings.HasPrefix(got[0], "certificate expired ") {
		t.Errorf("expired = %v", got)
	}
	info.NotAfter = now.Add(400 * 24 * time.Hour)
	info.DNSNames, info.IPs = nil, nil
	if got := Warnings(info, []string{"localhost"}, now); len(got) != 1 || !strings.Contains(got[0], "no subject alternative names") {
		t.Errorf("no SANs = %v", got)
	}
	if got := Warnings(InfoData{}, hosts, now); got != nil {
		t.Errorf("empty info warned: %v", got)
	}
	if m := MissingHosts(InfoData{IPs: []string{"::1", "fd00::1"}}, []string{"[fd00::1]:8010", "fd00::2"}); len(m) != 1 || m[0] != "fd00::2" {
		t.Errorf("IPv6 missing = %v", m)
	}
}
