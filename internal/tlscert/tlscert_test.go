package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
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

func TestSanSetShape(t *testing.T) {
	s := sanSet([]string{" Fans.Example:8010 ", "fans.example", "[::1]:8010", "127.0.0.1", "::", "", "192.0.2.20"})
	if got := s.String(); got != "fans.example, localhost, ::1, 127.0.0.1, 192.0.2.20" {
		t.Errorf("sanSet = %q", got)
	}
	if got := sanSet(nil).String(); got != "localhost, 127.0.0.1, ::1" {
		t.Errorf("empty sanSet = %q", got)
	}
}
