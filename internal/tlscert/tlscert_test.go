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
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func hasDNS(c *x509.Certificate, n string) bool {
	for _, d := range c.DNSNames {
		if d == n {
			return true
		}
	}
	return false
}

func hasIP(c *x509.Certificate, s string) bool {
	ip := net.ParseIP(s)
	for _, have := range c.IPAddresses {
		if have.Equal(ip) {
			return true
		}
	}
	return false
}

// TestEnsureAutoGenerateLoadVerify: a fresh dir gets an ECDSA P-256
// self-signed pair, files are 0600, the SANs cover the hosts plus loopback,
// CN is the first DNS host, validity is ten years, and LoadFiles reads the
// same certificate back.
func TestEnsureAutoGenerateLoadVerify(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tls")
	var logged []string
	o := Options{Dir: dir, Hosts: []string{"192.0.2.20:8010", "N5.lan.", "[fd00::20]", "0.0.0.0", "*", "localhost"}, Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}
	cert, path, err := EnsureAuto(o)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, CertFile) {
		t.Errorf("cert path = %s", path)
	}
	leaf := cert.Leaf
	if leaf == nil {
		t.Fatal("Leaf not populated")
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		t.Fatalf("public key %T, want ECDSA P-256", leaf.PublicKey)
	}
	if leaf.Subject.CommonName != "n5.lan" || leaf.Subject.Organization[0] != DefaultOrg {
		t.Errorf("subject = %v", leaf.Subject)
	}
	for _, d := range []string{"n5.lan", "localhost"} {
		if !hasDNS(leaf, d) {
			t.Errorf("DNS SAN %s missing: %v", d, leaf.DNSNames)
		}
	}
	if len(leaf.DNSNames) != 2 {
		t.Errorf("DNSNames = %v (duplicates or junk)", leaf.DNSNames)
	}
	for _, ip := range []string{"192.0.2.20", "fd00::20", "127.0.0.1", "::1"} {
		if !hasIP(leaf, ip) {
			t.Errorf("IP SAN %s missing: %v", ip, leaf.IPAddresses)
		}
	}
	if len(leaf.IPAddresses) != 4 {
		t.Errorf("IPAddresses = %v (0.0.0.0 must be dropped)", leaf.IPAddresses)
	}
	if got := leaf.NotAfter.Sub(leaf.NotBefore); got < Validity || got > Validity+2*time.Hour {
		t.Errorf("validity = %v", got)
	}
	if !leaf.IsCA || !leaf.BasicConstraintsValid || leaf.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Errorf("self-signed trust anchor flags missing: IsCA=%v KeyUsage=%v", leaf.IsCA, leaf.KeyUsage)
	}
	// self-signed: verifies against itself for a SAN host
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "n5.lan"}); err != nil {
		t.Errorf("verify n5.lan: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "192.0.2.20"}); err != nil {
		t.Errorf("verify 192.0.2.20: %v", err)
	}
	if runtime.GOOS != "windows" {
		for _, f := range []string{CertFile, KeyFile} {
			st, err := os.Stat(filepath.Join(dir, f))
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm() != 0o600 {
				t.Errorf("%s mode = %o, want 600", f, st.Mode().Perm())
			}
		}
		if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %o, want 700", st.Mode().Perm())
		}
	}
	if len(logged) != 2 || !strings.Contains(logged[0], "generating") || !strings.Contains(logged[1], "wrote") {
		t.Errorf("log = %v", logged)
	}
	loaded, err := LoadFiles(filepath.Join(dir, CertFile), filepath.Join(dir, KeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Leaf == nil || loaded.Leaf.SerialNumber.Cmp(leaf.SerialNumber) != 0 {
		t.Fatal("LoadFiles returned a different certificate")
	}
	if _, err := LoadFiles(filepath.Join(dir, "nope.pem"), filepath.Join(dir, KeyFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file error = %v, want wrapped os.ErrNotExist", err)
	}
}

// TestEnsureAutoReuseAndRegenerate: a second call with the same (or a
// subset of the) hosts reuses the pair; a new host regenerates it.
func TestEnsureAutoReuseAndRegenerate(t *testing.T) {
	dir := t.TempDir()
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	first, _, err := EnsureAuto(Options{Dir: dir, Hosts: []string{"192.0.2.20", "n5.lan"}, Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	certBytes, _ := os.ReadFile(filepath.Join(dir, CertFile))
	logged = nil
	same, _, err := EnsureAuto(Options{Dir: dir, Hosts: []string{"n5.lan", "192.0.2.20"}, Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	if same.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) != 0 {
		t.Fatal("same hosts regenerated the certificate")
	}
	sub, _, err := EnsureAuto(Options{Dir: dir, Hosts: []string{"192.0.2.20"}, Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	if sub.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) != 0 {
		t.Fatal("subset of hosts regenerated the certificate")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, CertFile)); string(b) != string(certBytes) {
		t.Fatal("cert.pem rewritten on reuse")
	}
	if len(logged) != 0 {
		t.Errorf("reuse logged: %v", logged)
	}
	// SAN change → new certificate, logged
	regen, _, err := EnsureAuto(Options{Dir: dir, Hosts: []string{"192.0.2.20", "n5.lan", "fans.example"}, Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	if regen.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) == 0 {
		t.Fatal("new SAN did not regenerate")
	}
	if !hasDNS(regen.Leaf, "fans.example") || !hasDNS(regen.Leaf, "n5.lan") {
		t.Errorf("SANs after regen = %v", regen.Leaf.DNSNames)
	}
	if len(logged) != 2 || !strings.Contains(logged[0], "SAN list lacks name fans.example") {
		t.Errorf("regen log = %v", logged)
	}
	// new IP as well
	logged = nil
	regen2, _, err := EnsureAuto(Options{Dir: dir, Hosts: []string{"192.0.2.20", "n5.lan", "fans.example", "198.51.100.5"}, Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	if regen2.Leaf.SerialNumber.Cmp(regen.Leaf.SerialNumber) == 0 || !hasIP(regen2.Leaf, "198.51.100.5") {
		t.Fatal("new IP SAN did not regenerate")
	}
	if len(logged) == 0 || !strings.Contains(logged[0], "lacks IP 198.51.100.5") {
		t.Errorf("regen log = %v", logged)
	}
	// corrupt key → regenerated with a log line
	if err := os.WriteFile(filepath.Join(dir, KeyFile), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	logged = nil
	fixed, _, err := EnsureAuto(Options{Dir: dir, Hosts: []string{"192.0.2.20"}, Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	if fixed.Leaf.SerialNumber.Cmp(regen2.Leaf.SerialNumber) == 0 || len(logged) == 0 || !strings.Contains(logged[0], "regenerating") {
		t.Errorf("corrupt key not regenerated: %v", logged)
	}
	// explicit Regenerate always makes a new one
	again, err := Regenerate(Options{Dir: dir, Hosts: []string{"192.0.2.20"}, Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	if again.Leaf.SerialNumber.Cmp(fixed.Leaf.SerialNumber) == 0 {
		t.Fatal("Regenerate reused the certificate")
	}
	if again.Leaf.Subject.CommonName != DefaultOrg {
		t.Errorf("CN without DNS host = %q, want %q", again.Leaf.Subject.CommonName, DefaultOrg)
	}
	// custom Org
	custom, err := Regenerate(Options{Dir: dir, Org: "Homelab", Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	if custom.Leaf.Subject.Organization[0] != "Homelab" || custom.Leaf.Subject.CommonName != "Homelab" {
		t.Errorf("custom org subject = %v", custom.Leaf.Subject)
	}
}

// TestExportPEM returns exactly the certificate block, never the key, and
// tolerates a cert.pem that carries extra blocks.
func TestExportPEM(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := EnsureAuto(Options{Dir: dir, Logf: func(string, ...any) {}}); err != nil {
		t.Fatal(err)
	}
	out, err := ExportPEM(dir)
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(out)
	if block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Fatalf("export = %q", out)
	}
	if strings.Contains(string(out), "PRIVATE KEY") {
		t.Fatal("export leaks the key")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		t.Fatal(err)
	}
	// key block prepended to cert.pem (someone concatenated the files): still only the cert
	certPEM, _ := os.ReadFile(filepath.Join(dir, CertFile))
	keyPEM, _ := os.ReadFile(filepath.Join(dir, KeyFile))
	if err := os.WriteFile(filepath.Join(dir, CertFile), append(append([]byte{}, keyPEM...), certPEM...), 0o600); err != nil {
		t.Fatal(err)
	}
	out2, err := ExportPEM(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(out2) != string(out) {
		t.Fatal("export differs with a concatenated file")
	}
	if _, err := ExportPEM(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing dir must fail")
	}
	if err := os.WriteFile(filepath.Join(dir, CertFile), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExportPEM(dir); err == nil || !strings.Contains(err.Error(), "no CERTIFICATE") {
		t.Fatalf("key-only file: %v", err)
	}
}

// M1: the automatic certificate is a CA in the browser's store, so it
// carries name constraints that pin it to exactly its own SANs (critical)
// and MaxPathLen 0. A leaf for any other name signed with its key fails
// verification against it.
func TestNameConstraints(t *testing.T) {
	dir := t.TempDir()
	cert, _, err := EnsureAuto(Options{Dir: dir, Hosts: []string{"192.0.2.20", "n5.lan", "fd00::20"}, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	leaf := cert.Leaf
	if !leaf.PermittedDNSDomainsCritical {
		t.Error("name constraints not critical")
	}
	if !leaf.MaxPathLenZero || leaf.MaxPathLen != 0 {
		t.Errorf("MaxPathLen = %d (zero=%v), want 0", leaf.MaxPathLen, leaf.MaxPathLenZero)
	}
	if fmt.Sprint(leaf.PermittedDNSDomains) != fmt.Sprint(leaf.DNSNames) {
		t.Errorf("PermittedDNSDomains %v != DNSNames %v", leaf.PermittedDNSDomains, leaf.DNSNames)
	}
	if len(leaf.PermittedIPRanges) != len(leaf.IPAddresses) || len(leaf.ExcludedDNSDomains) != 0 || len(leaf.ExcludedIPRanges) != 0 {
		t.Fatalf("IP constraints %v for SANs %v", leaf.PermittedIPRanges, leaf.IPAddresses)
	}
	for i, ipn := range leaf.PermittedIPRanges {
		ones, bits := ipn.Mask.Size()
		if !ipn.IP.Equal(leaf.IPAddresses[i]) || ones != bits || (bits != 32 && bits != 128) {
			t.Errorf("constraint %v for SAN %v", ipn, leaf.IPAddresses[i])
		}
	}
	// a leaf for a foreign name signed by this CA: refused by the constraint
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	mint := func(dns string, ip net.IP) *x509.Certificate {
		t.Helper()
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: dns},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		if dns != "" {
			tmpl.DNSNames = []string{dns}
		}
		if ip != nil {
			tmpl.IPAddresses = []net.IP{ip}
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, leaf, &k.PublicKey, cert.PrivateKey)
		if err != nil {
			t.Fatal(err)
		}
		c, _ := x509.ParseCertificate(der)
		return c
	}
	if _, err := mint("evil.example", nil).Verify(x509.VerifyOptions{Roots: pool, DNSName: "evil.example"}); err == nil {
		t.Error("leaf for a foreign DNS name verified against the constrained CA")
	}
	if _, err := mint("", net.ParseIP("192.0.2.21")).Verify(x509.VerifyOptions{Roots: pool, DNSName: "192.0.2.21"}); err == nil {
		t.Error("leaf for a foreign IP verified against the constrained CA")
	}
	if _, err := mint("n5.lan", nil).Verify(x509.VerifyOptions{Roots: pool, DNSName: "n5.lan"}); err != nil {
		t.Errorf("leaf for an own SAN refused: %v", err)
	}
	// the certificate itself still verifies for its names
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "192.0.2.20"}); err != nil {
		t.Errorf("self verify: %v", err)
	}
	// L3: no temp files left next to the pair
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("dir holds %d entries, want cert.pem and key.pem only", len(entries))
	}
}

// M5: a regeneration caused by a SAN change keeps the private key (the
// trust imported into a browser stays valid); an expired certificate gets
// a fresh key; Regenerate always does.
func TestRegenerateKeepsKeyOnSANChange(t *testing.T) {
	dir := t.TempDir()
	var logged []string
	logf := func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	first, _, err := EnsureAuto(Options{Dir: dir, Hosts: []string{"192.0.2.20"}, Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	pub := func(c tls.Certificate) []byte { b, _ := x509.MarshalPKIXPublicKey(c.Leaf.PublicKey); return b }
	keyPEM, _ := os.ReadFile(filepath.Join(dir, KeyFile))
	logged = nil
	second, _, err := EnsureAuto(Options{Dir: dir, Hosts: []string{"192.0.2.20", "n5.lan"}, Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	if second.Leaf.SerialNumber.Cmp(first.Leaf.SerialNumber) == 0 || !hasDNS(second.Leaf, "n5.lan") {
		t.Fatal("SAN change did not regenerate")
	}
	if string(pub(second)) != string(pub(first)) {
		t.Error("SAN change replaced the private key")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, KeyFile)); string(b) != string(keyPEM) {
		t.Error("key.pem rewritten with different content")
	}
	if len(logged) == 0 || !strings.Contains(logged[0], "certificate regenerated (SANs changed") || !strings.Contains(logged[0], "key unchanged") {
		t.Errorf("log = %v", logged)
	}
	// expired: write an expired certificate for the same key, then a fresh key is made
	key := second.PrivateKey.(*ecdsa.PrivateKey)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "old"},
		NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
		DNSNames: []string{"localhost", "n5.lan"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1"), net.ParseIP("192.0.2.20")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, CertFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	logged = nil
	third, _, err := EnsureAuto(Options{Dir: dir, Hosts: []string{"192.0.2.20", "n5.lan"}, Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	if string(pub(third)) == string(pub(second)) {
		t.Error("expired certificate kept its key")
	}
	if len(logged) == 0 || !strings.Contains(logged[0], "expired") || strings.Contains(logged[0], "key unchanged") {
		t.Errorf("expiry log = %v", logged)
	}
	fresh, err := Regenerate(Options{Dir: dir, Hosts: []string{"192.0.2.20", "n5.lan"}, Logf: logf})
	if err != nil {
		t.Fatal(err)
	}
	if string(pub(fresh)) == string(pub(third)) {
		t.Error("Regenerate kept the key")
	}
}

// L8: a key file readable by group or others is reported.
func TestCheckKeyMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(key, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckKeyMode(key); err != nil {
		t.Errorf("0600: %v", err)
	}
	for _, m := range []os.FileMode{0o640, 0o604, 0o644, 0o660} {
		if err := os.Chmod(key, m); err != nil {
			t.Fatal(err)
		}
		if err := CheckKeyMode(key); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%04o", m)) {
			t.Errorf("%04o: %v", m, err)
		}
	}
	if err := CheckKeyMode(filepath.Join(dir, "missing")); err != nil {
		t.Errorf("missing file: %v", err)
	}
}

func TestSanSetShape(t *testing.T) {
	s := sanSet([]string{" Fans.Example:8010 ", "fans.example", "[::1]:8010", "127.0.0.1", "::", "", "192.0.2.20"})
	if got := s.String(); got != "fans.example, localhost, ::1, 127.0.0.1, 192.0.2.20" {
		t.Errorf("sanSet = %q", got)
	}
	if got := sanSet(nil).String(); got != "localhost, 127.0.0.1, ::1" {
		t.Errorf("empty sanSet = %q", got)
	}
}
