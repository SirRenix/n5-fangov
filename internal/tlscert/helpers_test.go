package tlscert

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"
)

// Shared certificate generation for this package's tests (AUDIT 7: five
// generators in three packages). The web and cmd packages cannot import a
// _test.go file; moving this into an exported tlscerttest package would be
// production code and is left to the backend builder.

// signCert signs tmpl for pub with parentKey (self-signed by tmpl when
// parent is nil) and returns the DER. A nil serial becomes a random one, a
// zero NotAfter a validity of -1 h .. +365 d.
func signCert(t *testing.T, tmpl *x509.Certificate, pub any, parent *x509.Certificate, parentKey any) []byte {
	t.Helper()
	if tmpl.SerialNumber == nil {
		s, err := rand.Int(rand.Reader, big.NewInt(1<<62))
		if err != nil {
			t.Fatal(err)
		}
		tmpl.SerialNumber = s
	}
	if tmpl.NotAfter.IsZero() {
		tmpl.NotBefore = time.Now().Add(-time.Hour)
		tmpl.NotAfter = time.Now().Add(365 * 24 * time.Hour)
	}
	if parent == nil {
		parent = tmpl
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, pub, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// publicKeyOf returns the public half of an ECDSA, RSA or other
// crypto.Signer key.
func publicKeyOf(t *testing.T, key any) any {
	t.Helper()
	switch k := key.(type) {
	case *ecdsa.PrivateKey:
		return &k.PublicKey
	case *rsa.PrivateKey:
		return &k.PublicKey
	case crypto.Signer:
		return k.Public()
	}
	t.Fatalf("unsupported key type %T", key)
	return nil
}

// certPEM encodes DER certificates as a PEM chain.
func certPEM(der ...[]byte) []byte {
	var out []byte
	for _, d := range der {
		out = append(out, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: d})...)
	}
	return out
}

// pkcs8PEM encodes a private key as PKCS#8 PEM.
func pkcs8PEM(t *testing.T, k any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// sanTemplate builds a server-auth leaf template for cn with hosts split
// into DNS names and IP addresses (the split every generator repeated).
func sanTemplate(cn string, hosts ...string) *x509.Certificate {
	tmpl := &x509.Certificate{Subject: pkix.Name{CommonName: cn},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else if h != "" {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	return tmpl
}

// testCertPair returns a self-signed P-256 leaf covering hosts (DNS names
// and IP literals) with its PKCS#8 key, both PEM. The CN is the first host
// or "test" when none is given.
func testCertPair(t *testing.T, hosts ...string) (cert, key []byte) {
	t.Helper()
	cn := "test"
	if len(hosts) > 0 {
		cn = hosts[0]
	}
	return testCertPairTmpl(t, nil, sanTemplate(cn, hosts...))
}

// testCertPairTmpl is testCertPair with an explicit template and key
// (nil key → fresh P-256).
func testCertPairTmpl(t *testing.T, key any, tmpl *x509.Certificate) (cert, keyOut []byte) {
	t.Helper()
	if key == nil {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key = k
	}
	der := signCert(t, tmpl, publicKeyOf(t, key), nil, key)
	return certPEM(der), pkcs8PEM(t, key)
}

// TestTestCertPair guards the helper itself: the pair loads as a TLS
// certificate, covers every host given and verifies against itself.
func TestTestCertPair(t *testing.T) {
	c, k := testCertPair(t, "n5.lan", "192.0.2.20", "::1")
	pair, err := tls.X509KeyPair(c, k)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Subject.CommonName != "n5.lan" || len(leaf.DNSNames) != 1 || len(leaf.IPAddresses) != 2 {
		t.Errorf("SANs: %v %v cn %q", leaf.DNSNames, leaf.IPAddresses, leaf.Subject.CommonName)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	for _, h := range []string{"n5.lan", "192.0.2.20", "::1"} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: h}); err != nil {
			t.Errorf("verify %s: %v", h, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "other.example"}); err == nil {
		t.Error("foreign name verified")
	}
	// two calls never share a serial or key
	c2, k2 := testCertPair(t)
	pair2, err := tls.X509KeyPair(c2, k2)
	if err != nil {
		t.Fatal(err)
	}
	leaf2, err := x509.ParseCertificate(pair2.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf2.SerialNumber.Cmp(leaf.SerialNumber) == 0 || string(k2) == string(k) || leaf2.Subject.CommonName != "test" {
		t.Error("second pair not independent")
	}
}
