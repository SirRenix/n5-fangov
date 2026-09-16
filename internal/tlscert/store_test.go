package tlscert

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// selfSigned builds a server-auth PEM pair from tmpl (testCertPairTmpl in
// helpers_test.go). key nil → fresh P-256.
func selfSigned(t *testing.T, key any, tmpl *x509.Certificate) (c, k []byte) {
	t.Helper()
	tmpl.KeyUsage |= x509.KeyUsageDigitalSignature
	tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	return testCertPairTmpl(t, key, tmpl)
}

// TestStoreSwap: Get serves what Set stored, Current returns a copy, an
// empty store errors instead of serving nothing, and a swap is visible to
// the next handshake while the old value is unaffected.
func TestStoreSwap(t *testing.T) {
	var empty Store
	if _, err := empty.Get(nil); err == nil {
		t.Fatal("empty store served a certificate")
	}
	if c := empty.Current(); len(c.Certificate) != 0 {
		t.Fatal("empty Current not zero")
	}
	dir := t.TempDir()
	a, _, err := EnsureAuto(Options{Dir: dir, Hosts: []string{"a.example"}, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	s := NewStore(a)
	got, err := s.Get(&tls.ClientHelloInfo{ServerName: "whatever"})
	if err != nil || got.Leaf.SerialNumber.Cmp(a.Leaf.SerialNumber) != 0 {
		t.Fatalf("Get = %v, %v", got, err)
	}
	b, err := Regenerate(Options{Dir: dir, Hosts: []string{"b.example"}, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	// Leaf stripped: Set parses it
	b.Leaf = nil
	s.Set(b)
	got2, _ := s.Get(nil)
	if got2.Leaf == nil || !hasDNS(got2.Leaf, "b.example") {
		t.Fatalf("after swap Get leaf = %v", got2.Leaf)
	}
	if got.Leaf.SerialNumber.Cmp(a.Leaf.SerialNumber) != 0 {
		t.Fatal("the pointer handed out before the swap changed")
	}
	cur := s.Current()
	cur.Certificate = nil
	if c, _ := s.Get(nil); len(c.Certificate) == 0 {
		t.Fatal("Current did not return a copy")
	}
}

// TestInfo: the fields of InfoData for the automatic certificate.
func TestInfo(t *testing.T) {
	cert, _, err := EnsureAuto(Options{Dir: t.TempDir(), Hosts: []string{"192.0.2.20", "n5.lan"}, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	i := Info(cert)
	if !strings.Contains(i.Subject, "CN=n5.lan") || !strings.Contains(i.Subject, "O=n5-fangov") || i.Issuer != i.Subject {
		t.Errorf("subject %q issuer %q", i.Subject, i.Issuer)
	}
	if strings.Join(i.DNSNames, ",") != "n5.lan,localhost" {
		t.Errorf("DNSNames = %v", i.DNSNames)
	}
	if strings.Join(i.IPs, ",") != "192.0.2.20,127.0.0.1,::1" {
		t.Errorf("IPs = %v", i.IPs)
	}
	if !i.IsCA || i.KeyAlgo != "ECDSA P-256" {
		t.Errorf("IsCA %v KeyAlgo %q", i.IsCA, i.KeyAlgo)
	}
	if ok, _ := regexp.MatchString(`^([0-9A-F]{2}:){31}[0-9A-F]{2}$`, i.FingerprintSHA256); !ok {
		t.Errorf("fingerprint %q", i.FingerprintSHA256)
	}
	if i.SerialHex == "" || i.SerialHex != strings.ToUpper(i.SerialHex) {
		t.Errorf("serial %q", i.SerialHex)
	}
	if !i.NotBefore.Before(time.Now()) || i.NotAfter.Sub(i.NotBefore) < Validity {
		t.Errorf("validity %v..%v", i.NotBefore, i.NotAfter)
	}
	if i.NotAfter.Location() != time.UTC {
		t.Error("times not UTC")
	}
	if got := Info(tls.Certificate{}); got.FingerprintSHA256 != "" {
		t.Error("empty certificate must yield the zero value")
	}
	// Leaf missing: parsed from the DER
	noLeaf := cert
	noLeaf.Leaf = nil
	if Info(noLeaf).FingerprintSHA256 != i.FingerprintSHA256 {
		t.Error("Info without Leaf differs")
	}
	// PEM / DER helpers
	p, err := PEM(cert)
	if err != nil {
		t.Fatal(err)
	}
	blk, rest := pem.Decode(p)
	if blk == nil || blk.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatalf("PEM = %q", p)
	}
	d, _ := DER(cert)
	if string(d) != string(blk.Bytes) || string(d) != string(cert.Leaf.Raw) {
		t.Error("DER differs from the leaf")
	}
	if _, err := DER(tls.Certificate{}); err == nil {
		t.Error("DER of empty certificate must fail")
	}
}

// TestValidatePair: bad PEM, mismatched key, expired and not-yet-valid
// certificates are errors; a good pair passes and reports the SANs it
// lacks for the listen hosts, an imminent expiry and a weak key.
func TestValidatePair(t *testing.T) {
	good, goodKey := selfSigned(t, nil, &x509.Certificate{Subject: pkix.Name{CommonName: "fans.example"}, DNSNames: []string{"fans.example"}, IPAddresses: []net.IP{net.ParseIP("192.0.2.10")}})
	cert, warns, err := ValidatePair(good, goodKey, []string{"fans.example:8010", "192.0.2.10", "0.0.0.0", "*", "FANS.EXAMPLE."})
	if err != nil || len(warns) != 0 {
		t.Fatalf("good pair: %v %v", err, warns)
	}
	if cert.Leaf == nil || cert.Leaf.Subject.CommonName != "fans.example" {
		t.Fatalf("leaf = %v", cert.Leaf)
	}
	// missing SAN for a listen host → warning, not error
	_, warns, err = ValidatePair(good, goodKey, []string{"192.0.2.20:8010", "n5host"})
	if err != nil || len(warns) != 2 || !strings.Contains(warns[0], "lacks host 192.0.2.20") || !strings.Contains(warns[1], "lacks host n5host") {
		t.Errorf("missing SAN: %v %v", err, warns)
	}
	// bad PEM
	if _, _, err := ValidatePair([]byte("nope"), goodKey, nil); err == nil || !strings.Contains(err.Error(), "CERTIFICATE block") {
		t.Errorf("bad cert PEM: %v", err)
	}
	if _, _, err := ValidatePair(good, []byte("nope"), nil); err == nil || !strings.Contains(err.Error(), "PRIVATE KEY block") {
		t.Errorf("bad key PEM: %v", err)
	}
	if _, _, err := ValidatePair(goodKey, good, nil); err == nil {
		t.Error("swapped cert/key accepted")
	}
	// mismatched key
	_, otherKey := selfSigned(t, nil, &x509.Certificate{Subject: pkix.Name{CommonName: "other"}})
	if _, _, err := ValidatePair(good, otherKey, nil); err == nil || !strings.Contains(err.Error(), "pair") {
		t.Errorf("mismatched key: %v", err)
	}
	// expired
	exp, expKey := selfSigned(t, nil, &x509.Certificate{Subject: pkix.Name{CommonName: "old"}, DNSNames: []string{"old"}, NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-time.Hour)})
	if _, _, err := ValidatePair(exp, expKey, nil); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired: %v", err)
	}
	// not yet valid
	fut, futKey := selfSigned(t, nil, &x509.Certificate{Subject: pkix.Name{CommonName: "future"}, DNSNames: []string{"future"}, NotBefore: time.Now().Add(24 * time.Hour), NotAfter: time.Now().Add(48 * time.Hour)})
	if _, _, err := ValidatePair(fut, futKey, nil); err == nil || !strings.Contains(err.Error(), "not valid before") {
		t.Errorf("future: %v", err)
	}
	// expires soon + no SANs
	soon, soonKey := selfSigned(t, nil, &x509.Certificate{Subject: pkix.Name{CommonName: "soon"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(5 * 24 * time.Hour)})
	_, warns, err = ValidatePair(soon, soonKey, nil)
	if err != nil || len(warns) != 2 || !strings.Contains(warns[0], "expires in 5 days") || !strings.Contains(warns[1], "no subject alternative names") {
		t.Errorf("soon: %v %v", err, warns)
	}
	// weak RSA key
	rk, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	weak, weakKey := selfSigned(t, rk, &x509.Certificate{Subject: pkix.Name{CommonName: "weak"}, DNSNames: []string{"weak"}})
	_, warns, err = ValidatePair(weak, weakKey, []string{"weak"})
	if err != nil || len(warns) != 1 || !strings.Contains(warns[0], "weak key: RSA 1024") {
		t.Errorf("weak: %v %v", err, warns)
	}
	if Info(cert).KeyAlgo != "ECDSA P-256" {
		t.Errorf("KeyAlgo = %q", Info(cert).KeyAlgo)
	}
}

// TestReissueKeepsKey: Reissue makes a new certificate with the stored
// key; without a key it falls back to a new pair; WritePrivate is 0600.
func TestReissueKeepsKey(t *testing.T) {
	dir := t.TempDir()
	quiet := func(string, ...any) {}
	first, _, err := EnsureAuto(Options{Dir: dir, Hosts: []string{"192.0.2.20"}, Logf: quiet})
	if err != nil {
		t.Fatal(err)
	}
	pub := func(c tls.Certificate) string { b, _ := x509.MarshalPKIXPublicKey(c.Leaf.PublicKey); return string(b) }
	re, kept, err := Reissue(Options{Dir: dir, Hosts: []string{"192.0.2.20", "n5.lan"}, Logf: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if !kept {
		t.Error("Reissue with a key file reported kept=false")
	}
	if re.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) == 0 || !hasDNS(re.Leaf, "n5.lan") {
		t.Fatal("Reissue did not produce a new certificate")
	}
	if pub(re) != pub(first) {
		t.Error("Reissue changed the key")
	}
	if err := os.Remove(filepath.Join(dir, KeyFile)); err != nil {
		t.Fatal(err)
	}
	fresh, kept, err := Reissue(Options{Dir: dir, Hosts: []string{"192.0.2.20"}, Logf: quiet})
	if err != nil {
		t.Fatal(err)
	}
	if kept {
		t.Error("Reissue without a key file reported kept=true")
	}
	if pub(fresh) == pub(first) {
		t.Error("Reissue without a key file reused one")
	}
	p := filepath.Join(dir, "custom.pem")
	if err := WritePrivate(p, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Mode().Perm()&0o077 != 0 && !isWindows() {
		t.Errorf("mode %o", st.Mode().Perm())
	}
}

func isWindows() bool { return runtime.GOOS == "windows" }
