package web

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/tlscert"
)

// fakeTLSMgr records calls and answers from a fixed InfoData.
type fakeTLSMgr struct {
	mode      string
	info      tlscert.InfoData
	infoErr   error
	regen     []bool
	regenErr  error
	uploads   [][2]string
	uploadErr error
	warns     []string
	resets    int
	notKept   bool // Regenerate(true) could not reuse the key (L2)
	fallback  bool // M3: file pair unreadable, auto served
}

func newFakeTLSMgr(mode string) *fakeTLSMgr {
	return &fakeTLSMgr{mode: mode, info: tlscert.InfoData{
		Subject: "CN=n5.lan,O=n5-fangov", Issuer: "CN=n5.lan,O=n5-fangov", DNSNames: []string{"n5.lan", "localhost"}, IPs: []string{"192.0.2.20", "127.0.0.1", "::1"},
		NotBefore: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2036, 9, 13, 0, 0, 0, 0, time.UTC),
		FingerprintSHA256: "AA:BB", IsCA: true, KeyAlgo: "ECDSA P-256", SerialHex: "0A0B"}}
}

func (m *fakeTLSMgr) Info() (tlscert.InfoData, string, error) {
	if m.mode == "off" {
		return tlscert.InfoData{}, "off", nil
	}
	return m.info, m.mode, m.infoErr
}
func (m *fakeTLSMgr) ExportPEM() ([]byte, error) {
	return []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"), nil
}
func (m *fakeTLSMgr) ExportDER() ([]byte, error) { return []byte{0x30, 0x82, 0x01, 0x02}, nil }
func (m *fakeTLSMgr) Regenerate(keepKey bool) (tlscert.InfoData, bool, error) {
	if m.regenErr != nil {
		return tlscert.InfoData{}, false, m.regenErr
	}
	if m.mode == "file" {
		return tlscert.InfoData{}, false, ErrTLSFileMode
	}
	m.regen = append(m.regen, keepKey)
	m.info.SerialHex = "NEW"
	return m.info, keepKey && !m.notKept, nil
}
func (m *fakeTLSMgr) Fallback() bool { return m.fallback }
func (m *fakeTLSMgr) Upload(certPEM, keyPEM []byte) (tlscert.InfoData, []string, error) {
	if m.uploadErr != nil {
		return tlscert.InfoData{}, nil, m.uploadErr
	}
	m.uploads = append(m.uploads, [2]string{string(certPEM), string(keyPEM)})
	m.mode = "file"
	m.info.Subject = "CN=uploaded"
	return m.info, m.warns, nil
}
func (m *fakeTLSMgr) ResetAuto() (tlscert.InfoData, error) {
	m.resets++
	m.mode = "auto"
	m.info.Subject = "CN=auto"
	return m.info, nil
}

func tlsEnv(t *testing.T, auth AuthConfig, mgr TLSMgr) *env {
	t.Helper()
	e := newEnv(t, auth)
	e.withDeps(t, auth, func(d *Deps) { d.TLSMgr = mgr; d.TLSHosts = []string{"192.0.2.20", "n5.lan"} })
	return e
}

// TestTLSInfoAndDownloads: GET /api/tls carries mode/info/hosts; the two
// downloads are attachments with the right media types. Since v0.3 the
// whole /api/tls tree is protected: anonymous callers get 401.
func TestTLSInfoAndDownloads(t *testing.T) {
	m := newFakeTLSMgr("auto")
	creds := basicAuth("admin", "pw")
	e := tlsEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: PasswordHash("admin", "pw")}, m)
	r := e.do(t, "GET", "/api/tls", "", creds)
	wantCode(t, r, 200)
	var out struct {
		Mode  string           `json:"mode"`
		Info  tlscert.InfoData `json:"info"`
		Hosts []string         `json:"hosts"`
	}
	decode(t, r.body, &out)
	if out.Mode != "auto" || out.Info.FingerprintSHA256 != "AA:BB" || out.Info.KeyAlgo != "ECDSA P-256" || strings.Join(out.Hosts, ",") != "192.0.2.20,n5.lan" {
		t.Fatalf("GET /api/tls = %s", r.body)
	}
	if !strings.Contains(r.body, `"not_after":"2036-09-13T00:00:00Z"`) || !strings.Contains(r.body, `"dns_names":["n5.lan","localhost"]`) {
		t.Errorf("info JSON shape: %s", r.body)
	}
	if !strings.Contains(r.body, `"warnings":[]`) || !strings.Contains(r.body, `"fallback":false`) {
		t.Errorf("warnings/fallback defaults: %s", r.body)
	}
	// L4: warnings are computed server-side against TLSHosts (loopback
	// excluded); M3: fallback is exposed
	m.fallback = true
	e.withDeps(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: PasswordHash("admin", "pw")}, func(d *Deps) {
		d.TLSMgr = m
		d.TLSHosts = []string{"192.0.2.20", "n5.lan", "other.lan:8010", "localhost", "127.0.0.1", "::1"}
	})
	r = e.do(t, "GET", "/api/tls", "", creds)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"warnings":["SAN list lacks host other.lan"]`) || !strings.Contains(r.body, `"fallback":true`) {
		t.Errorf("warnings/fallback: %s", r.body)
	}
	m.fallback = false
	m.info.NotAfter = time.Now().Add(3 * 24 * time.Hour)
	r = e.do(t, "GET", "/api/tls", "", creds)
	if !strings.Contains(r.body, `"warnings":["certificate expires in 3 days (`) {
		t.Errorf("expiry warning: %s", r.body)
	}
	m.info.NotAfter = time.Date(2036, 9, 13, 0, 0, 0, 0, time.UTC)
	r = e.do(t, "GET", "/api/tls/cert.crt", "", creds)
	name := wantAttachment(t, r, "application/x-pem-file", `^n5-fangov-[A-Za-z0-9.-]+\.crt$`)
	if !strings.HasPrefix(r.body, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("crt body %q (%s)", r.body, name)
	}
	r = e.do(t, "GET", "/api/tls/cert.cer", "", creds)
	wantAttachment(t, r, "application/pkix-cert", `^n5-fangov-[A-Za-z0-9.-]+\.cer$`)
	if r.body != string([]byte{0x30, 0x82, 0x01, 0x02}) {
		t.Errorf("cer body % x", r.body)
	}
	// empty list members are [] not null
	m.info.DNSNames, m.info.IPs = nil, nil
	r = e.do(t, "GET", "/api/tls", "", creds)
	if !strings.Contains(r.body, `"dns_names":[]`) || !strings.Contains(r.body, `"ips":[]`) {
		t.Errorf("nil lists: %s", r.body)
	}
	// no manager → 501
	e.withDeps(t, AuthConfig{}, func(d *Deps) {})
	wantCode(t, e.do(t, "GET", "/api/tls", "", nil), 501)
	wantCode(t, e.do(t, "POST", "/api/tls/reset", "", csrf), 501)
}

// TestTLSPostsNeedAuthCSRF: the three state changes need the CSRF header
// and, with basic auth, a credential; each one logs "web: tls <action> by <ip>".
func TestTLSPostsNeedAuthCSRF(t *testing.T) {
	m := newFakeTLSMgr("auto")
	creds := basicAuth("admin", "pw")
	e := tlsEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: PasswordHash("admin", "pw")}, m)
	pair := `{"cert":"-----BEGIN CERTIFICATE-----\nA\n-----END CERTIFICATE-----\n","key":"-----BEGIN PRIVATE KEY-----\nB\n-----END PRIVATE KEY-----\n"}`
	for _, c := range []struct{ path, body, action string }{
		{"/api/tls/regenerate", `{"keep_key":true}`, "regenerate"},
		{"/api/tls/upload", pair, "upload"},
		{"/api/tls/reset", "", "reset"},
	} {
		wantError(t, e.do(t, "POST", c.path, c.body, nil), 403, CSRFHeader)
		wantCode(t, e.do(t, "POST", c.path, c.body, csrf), 401)
		hdr := map[string]string{"Content-Type": "application/json"}
		for k, v := range creds {
			hdr[k] = v
		}
		if c.action == "reset" {
			m.mode = "file"
		}
		r := e.do(t, "POST", c.path, c.body, hdr)
		wantCode(t, r, 200)
		e.logMu.Lock()
		joined := strings.Join(e.logged, "\n")
		e.logMu.Unlock()
		if !strings.Contains(joined, "web: tls "+c.action) || !strings.Contains(joined, "by 127.0.0.1") {
			t.Errorf("%s: no audit line in %q", c.action, joined)
		}
	}
	if len(m.regen) != 1 || len(m.uploads) != 1 || m.resets != 1 {
		t.Errorf("manager calls: regen %v uploads %d resets %d", m.regen, len(m.uploads), m.resets)
	}
}

// TestTLSOff409: with TLS off, GET /api/tls reports the mode and every
// other endpoint answers 409 {"error":"tls is off"}.
func TestTLSOff409(t *testing.T) {
	e := tlsEnv(t, AuthConfig{}, newFakeTLSMgr("off"))
	r := e.do(t, "GET", "/api/tls", "", nil)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"mode":"off"`) || !strings.Contains(r.body, `"info":null`) {
		t.Errorf("off: %s", r.body)
	}
	wantError(t, e.do(t, "GET", "/api/tls/cert.crt", "", nil), 409, "tls is off")
	wantError(t, e.do(t, "GET", "/api/tls/cert.cer", "", nil), 409, "tls is off")
	wantError(t, e.do(t, "POST", "/api/tls/regenerate", "", csrf), 409, "tls is off")
	wantError(t, e.do(t, "POST", "/api/tls/upload", `{"cert":"a","key":"b"}`, csrf), 409, "tls is off")
	wantError(t, e.do(t, "POST", "/api/tls/reset", "", csrf), 409, "tls is off")
}

// TestTLSUploadSizeLimit: 64 KiB is the ceiling for JSON and multipart
// bodies; an empty part, a broken JSON and a validation error are 400; a
// storage error is 500; warnings pass through.
func TestTLSUploadSizeLimit(t *testing.T) {
	m := newFakeTLSMgr("auto")
	e := tlsEnv(t, AuthConfig{}, m)
	big := strings.Repeat("A", maxTLSUpload)
	jsonHdr := map[string]string{CSRFHeader: "1", "Content-Type": "application/json"}
	wantError(t, e.do(t, "POST", "/api/tls/upload", `{"cert":"`+big+`","key":"k"}`, jsonHdr), 413, "exceeds")
	// multipart over the limit
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("cert", "cert.pem")
	io.WriteString(fw, big)
	fw, _ = mw.CreateFormFile("key", "key.pem")
	io.WriteString(fw, "k")
	mw.Close()
	wantCode(t, e.do(t, "POST", "/api/tls/upload", buf.String(), map[string]string{CSRFHeader: "1", "Content-Type": mw.FormDataContentType()}), 413)
	// multipart within the limit reaches the manager
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	fw, _ = mw.CreateFormFile("cert", "cert.pem")
	io.WriteString(fw, "CERT")
	mw.WriteField("key", "KEY")
	mw.Close()
	r := e.do(t, "POST", "/api/tls/upload", buf.String(), map[string]string{CSRFHeader: "1", "Content-Type": mw.FormDataContentType()})
	wantCode(t, r, 200)
	if len(m.uploads) != 1 || m.uploads[0] != [2]string{"CERT", "KEY"} {
		t.Errorf("multipart upload = %v", m.uploads)
	}
	if !strings.Contains(r.body, `"mode":"file"`) || !strings.Contains(r.body, `"warnings":[]`) || !strings.Contains(r.body, "CN=uploaded") {
		t.Errorf("upload response: %s", r.body)
	}
	// missing part
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	mw.WriteField("cert", "CERT")
	mw.Close()
	wantError(t, e.do(t, "POST", "/api/tls/upload", buf.String(), map[string]string{CSRFHeader: "1", "Content-Type": mw.FormDataContentType()}), 400, `"key" missing`)
	// JSON: bad body, empty key, validation error (400), storage error (500), warnings
	wantError(t, e.do(t, "POST", "/api/tls/upload", `{"cert":`, jsonHdr), 400, "invalid JSON")
	wantError(t, e.do(t, "POST", "/api/tls/upload", `{"cert":"a","key":" "}`, jsonHdr), 400, "both required")
	m.uploadErr = ValidationError{errors.New("certificate/key pair: private key does not match")}
	wantError(t, e.do(t, "POST", "/api/tls/upload", `{"cert":"a","key":"b"}`, jsonHdr), 400, "does not match")
	m.uploadErr = errors.New("write custom-cert.pem: disk full")
	wantError(t, e.do(t, "POST", "/api/tls/upload", `{"cert":"a","key":"b"}`, jsonHdr), 500, "disk full")
	m.uploadErr = nil
	m.warns = []string{"SAN list lacks host n5.lan"}
	r = e.do(t, "POST", "/api/tls/upload", `{"cert":"a","key":"b"}`, jsonHdr)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"warnings":["SAN list lacks host n5.lan"]`) {
		t.Errorf("warnings: %s", r.body)
	}
}

// TestTLSRegenerateKeepKey: keep_key defaults to true; false yields the
// trust warning; unknown fields are refused; file mode answers 409.
func TestTLSRegenerateKeepKey(t *testing.T) {
	m := newFakeTLSMgr("auto")
	e := tlsEnv(t, AuthConfig{}, m)
	hdr := map[string]string{CSRFHeader: "1", "Content-Type": "application/json"}
	r := e.do(t, "POST", "/api/tls/regenerate", "", csrf)
	wantCode(t, r, 200)
	if strings.Contains(r.body, "warning") || !strings.Contains(r.body, `"keep_key":true`) || !strings.Contains(r.body, `"serial_hex":"NEW"`) {
		t.Errorf("default regenerate: %s", r.body)
	}
	r = e.do(t, "POST", "/api/tls/regenerate", `{"keep_key":true}`, hdr)
	wantCode(t, r, 200)
	r = e.do(t, "POST", "/api/tls/regenerate", `{"keep_key":false}`, hdr)
	wantCode(t, r, 200)
	var out map[string]any
	decode(t, r.body, &out)
	if w, _ := out["warning"].(string); !strings.Contains(w, "new private key") || out["keep_key"] != false {
		t.Errorf("new-key regenerate: %s", r.body)
	}
	if len(m.regen) != 3 || !m.regen[0] || !m.regen[1] || m.regen[2] {
		t.Errorf("keep flags = %v", m.regen)
	}
	wantError(t, e.do(t, "POST", "/api/tls/regenerate", `{"keepkey":1}`, hdr), 400, "invalid JSON")
	// L2: keep requested but the stored key was unusable → kept=false + warning
	m.notKept = true
	r = e.do(t, "POST", "/api/tls/regenerate", `{"keep_key":true}`, hdr)
	wantCode(t, r, 200)
	decode(t, r.body, &out)
	if w, _ := out["warning"].(string); !strings.Contains(w, "could not be reused") || out["kept"] != false || out["keep_key"] != true {
		t.Errorf("not-kept regenerate: %s", r.body)
	}
	m.notKept = false
	m.mode = "file"
	wantError(t, e.do(t, "POST", "/api/tls/regenerate", "", csrf), 409, "custom certificate")
	m.mode = "auto"
	m.regenErr = errors.New("write cert.pem: read-only file system")
	wantError(t, e.do(t, "POST", "/api/tls/regenerate", "", csrf), 500, "read-only")
}

// TestServeTLSStoreHotSwap: Store.Set changes the certificate for new
// handshakes while a connection opened before the swap keeps working and
// still holds the old certificate.
func TestServeTLSStoreHotSwap(t *testing.T) {
	quiet := func(string, ...any) {}
	dir := t.TempDir()
	first, _, err := tlscert.EnsureAuto(tlscert.Options{Dir: dir, Hosts: []string{"127.0.0.1"}, Logf: quiet})
	if err != nil {
		t.Fatal(err)
	}
	store := tlscert.NewStore(first)
	e := newEnv(t, AuthConfig{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- e.srv.ServeTLSStore(ctx, ln, store) }()
	url := "https://" + ln.Addr().String() + "/api/version"
	// one keep-alive connection from before the swap
	oldTr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ForceAttemptHTTP2: false, TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{}}
	oldClient := &http.Client{Transport: oldTr}
	res, err := oldClient.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if !bytes.Equal(res.TLS.PeerCertificates[0].Raw, first.Leaf.Raw) {
		t.Fatal("first connection got a foreign certificate")
	}
	second, err := tlscert.Regenerate(tlscert.Options{Dir: dir, Hosts: []string{"127.0.0.1", "swapped.example"}, Logf: quiet})
	if err != nil {
		t.Fatal(err)
	}
	store.Set(second)
	// the old connection is reused (keep-alive) and still carries the old certificate
	res, err = oldClient.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !bytes.Equal(res.TLS.PeerCertificates[0].Raw, first.Leaf.Raw) {
		t.Error("existing connection was disturbed by the swap")
	}
	// a new connection sees the new certificate
	fresh := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DisableKeepAlives: true}}
	res, err = fresh.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Equal(res.TLS.PeerCertificates[0].Raw, second.Leaf.Raw) {
		t.Error("new connection did not get the swapped certificate")
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil || v["tls"] != true {
		t.Errorf("version over swapped TLS = %s", body)
	}
	// L1: a client with a session cache that connected before a swap must
	// not resume the old session afterwards (no tickets are issued), so it
	// too sees the new certificate.
	third, err := tlscert.Regenerate(tlscert.Options{Dir: dir, Hosts: []string{"127.0.0.1", "third.example"}, Logf: quiet})
	if err != nil {
		t.Fatal(err)
	}
	cache := tls.NewLRUClientSessionCache(4)
	resuming := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ClientSessionCache: cache}, DisableKeepAlives: true}}
	res, err = resuming.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if !bytes.Equal(res.TLS.PeerCertificates[0].Raw, second.Leaf.Raw) {
		t.Fatal("cache-priming connection got the wrong certificate")
	}
	store.Set(third)
	res, err = resuming.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.TLS.DidResume {
		t.Error("session resumed across a certificate swap (session tickets must be disabled)")
	}
	if !bytes.Equal(res.TLS.PeerCertificates[0].Raw, third.Leaf.Raw) {
		t.Error("client with a session cache did not see the swapped certificate")
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeTLSStore did not stop")
	}
}

// testLeafPEM builds a self-signed P-256 certificate PEM for the given
// SANs (the fake manager never parses it; the handler's host guard does).
func testLeafPEM(t *testing.T, hosts ...string) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "guard"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// TestTLSUploadHostGuard (M4): over TLS, an upload whose leaf does not
// cover the name the request came in by (SNI, else Host) is refused with
// 400 + force_required; force=true (JSON or multipart) or a covering leaf
// passes; over plain HTTP (unix socket, reverse proxy) there is no guard.
func TestTLSUploadHostGuard(t *testing.T) {
	m := newFakeTLSMgr("auto")
	e := newEnv(t, AuthConfig{})
	e.withDeps(t, AuthConfig{}, func(d *Deps) { d.TLSMgr = m; d.TLSHosts = []string{"127.0.0.1"}; d.AllowedHosts = []string{"n5.lan"} })
	ts := httptest.NewUnstartedServer(e.srv.Handler())
	ts.StartTLS()
	t.Cleanup(ts.Close)
	key := "-----BEGIN PRIVATE KEY-----\nB\n-----END PRIVATE KEY-----\n"
	post := func(t *testing.T, sni, ctype, body string) (int, string) {
		t.Helper()
		tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, ServerName: sni}, DisableKeepAlives: true}
		req, _ := http.NewRequest("POST", ts.URL+"/api/tls/upload", strings.NewReader(body))
		req.Header.Set(CSRFHeader, "1")
		req.Header.Set("Content-Type", ctype)
		res, err := (&http.Client{Transport: tr}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	jsonBody := func(cert string, force bool) string {
		b, _ := json.Marshal(map[string]any{"cert": cert, "key": key, "force": force})
		return string(b)
	}
	foreign := testLeafPEM(t, "fans.example")
	// reached by IP (no SNI): a leaf without that IP SAN is refused
	code, body := post(t, "", "application/json", jsonBody(foreign, false))
	if code != 400 || !strings.Contains(body, `"force_required":true`) || !strings.Contains(body, `"host":"127.0.0.1"`) || !strings.Contains(body, "HSTS") {
		t.Errorf("uncovered IP: %d %s", code, body)
	}
	if len(m.uploads) != 0 {
		t.Fatal("refused upload reached the manager")
	}
	// force=true passes it through
	if code, body = post(t, "", "application/json", jsonBody(foreign, true)); code != 200 || len(m.uploads) != 1 {
		t.Errorf("forced upload: %d %s", code, body)
	}
	// a leaf covering the IP passes without force
	if code, body = post(t, "", "application/json", jsonBody(testLeafPEM(t, "fans.example", "127.0.0.1"), false)); code != 200 || len(m.uploads) != 2 {
		t.Errorf("covering upload: %d %s", code, body)
	}
	// SNI names the host: n5.lan covered / not covered
	if code, body = post(t, "n5.lan", "application/json", jsonBody(testLeafPEM(t, "n5.lan"), false)); code != 200 {
		t.Errorf("SNI covered: %d %s", code, body)
	}
	if code, body = post(t, "n5.lan", "application/json", jsonBody(testLeafPEM(t, "other.lan"), false)); code != 400 || !strings.Contains(body, `"host":"n5.lan"`) {
		t.Errorf("SNI uncovered: %d %s", code, body)
	}
	// multipart with force field
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("cert", foreign)
	mw.WriteField("key", key)
	mw.Close()
	if code, body = post(t, "", mw.FormDataContentType(), buf.String()); code != 400 {
		t.Errorf("multipart uncovered: %d %s", code, body)
	}
	buf.Reset()
	mw = multipart.NewWriter(&buf)
	mw.WriteField("cert", foreign)
	mw.WriteField("key", key)
	mw.WriteField("force", "true")
	mw.Close()
	if code, body = post(t, "", mw.FormDataContentType(), buf.String()); code != 200 {
		t.Errorf("multipart forced: %d %s", code, body)
	}
	// an unparsable certificate is left to the manager's validation (400 from there)
	m.uploadErr = ValidationError{errors.New("certificate: no PEM CERTIFICATE block")}
	if code, body = post(t, "", "application/json", jsonBody("junk", false)); code != 400 || strings.Contains(body, "force_required") {
		t.Errorf("junk over TLS: %d %s", code, body)
	}
	m.uploadErr = nil
	// plain HTTP: no guard
	n := len(m.uploads)
	r := e.do(t, "POST", "/api/tls/upload", jsonBody(foreign, false), map[string]string{CSRFHeader: "1", "Content-Type": "application/json"})
	if r.code != 200 || len(m.uploads) != n+1 {
		t.Errorf("plain HTTP upload guarded: %d %s", r.code, r.body)
	}
	e.logMu.Lock()
	joined := strings.Join(e.logged, "\n")
	e.logMu.Unlock()
	if !strings.Contains(joined, `web: tls upload by 127.0.0.1 ("CN=uploaded"`) {
		t.Errorf("L7 subject not quoted in audit line: %q", joined)
	}
}

// ---- TLS listener ------------------------------------------------------------

// TestServeTLSRoundTrip: a generated certificate serves HTTPS, the response
// carries HSTS and "tls":true, the negotiated version is ≥ 1.2, TLS 1.1 is
// refused, and plain Serve has neither HSTS nor the flag.
func TestServeTLSRoundTrip(t *testing.T) {
	cert, _, err := tlscert.EnsureAuto(tlscert.Options{Dir: t.TempDir(), Hosts: []string{"127.0.0.1"}, Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, AuthConfig{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- e.srv.ServeTLS(ctx, ln, cert) }()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	url := "https://" + ln.Addr().String()
	res, err := client.Get(url + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	if got := res.Header.Get("Strict-Transport-Security"); got != hstsValue {
		t.Errorf("HSTS = %q, want %q", got, hstsValue)
	}
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil || v["tls"] != true {
		t.Errorf("version over TLS = %s", body)
	}
	if res.TLS == nil || res.TLS.Version < tls.VersionTLS12 {
		t.Errorf("negotiated TLS %#x", res.TLS.Version)
	}
	if !bytes.Equal(res.TLS.PeerCertificates[0].Raw, cert.Leaf.Raw) {
		t.Error("served a different certificate")
	}
	// HSTS also on guarded errors (Host check, CSRF) and on the UI
	req, _ := http.NewRequest("PUT", url+"/api/override/cpu", strings.NewReader(`{"duty":1}`))
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 || res.Header.Get("Strict-Transport-Security") == "" {
		t.Errorf("CSRF answer over TLS: %d HSTS %q", res.StatusCode, res.Header.Get("Strict-Transport-Security"))
	}
	res, err = client.Get(url + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Strict-Transport-Security") == "" {
		t.Error("index without HSTS")
	}
	// TLS 1.1 refused
	old := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11}}}
	if res, err := old.Get(url + "/api/version"); err == nil {
		res.Body.Close()
		t.Error("TLS 1.1 handshake succeeded")
	}
	// plain HTTP on the TLS port fails (no downgrade)
	if res, err := http.Get("http://" + ln.Addr().String() + "/api/version"); err == nil {
		res.Body.Close()
		if res.StatusCode == 200 {
			t.Error("plain HTTP served on the TLS listener")
		}
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("ServeTLS: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeTLS did not stop")
	}

	// plain Serve: no HSTS, tls false
	e2 := newEnv(t, AuthConfig{})
	r := e2.do(t, "GET", "/api/version", "", nil)
	wantCode(t, r, 200)
	if r.hdr.Get("Strict-Transport-Security") != "" || !strings.Contains(r.body, `"tls":false`) {
		t.Errorf("plain HTTP: HSTS %q body %s", r.hdr.Get("Strict-Transport-Security"), r.body)
	}
	// Deps.TLS is informational for a TLS reverse proxy in front of plain Serve
	e2.withDeps(t, AuthConfig{}, func(d *Deps) { d.TLS = true })
	if r := e2.do(t, "GET", "/api/version", "", nil); !strings.Contains(r.body, `"tls":true`) || r.hdr.Get("Strict-Transport-Security") != "" {
		t.Errorf("Deps.TLS: %s HSTS %q", r.body, r.hdr.Get("Strict-Transport-Security"))
	}
}

// TestServeTLSWarnsNonLoopbackWithoutAuth: TLS does not silence the H3
// warning — encryption is not authentication.
func TestServeTLSWarnsNonLoopbackWithoutAuth(t *testing.T) {
	cert, _, err := tlscert.EnsureAuto(tlscert.Options{Dir: t.TempDir(), Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, AuthConfig{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- e.srv.ServeTLS(ctx, addrListener{ln, &net.TCPAddr{IP: net.IPv4zero, Port: 8010}}, cert)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	<-errc
	e.logMu.Lock()
	defer e.logMu.Unlock()
	joined := strings.Join(e.logged, "\n")
	if !strings.Contains(joined, "non-loopback") || !strings.Contains(joined, "without auth") {
		t.Fatalf("no warning: %q", joined)
	}
}
