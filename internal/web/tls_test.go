package web

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
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
func (m *fakeTLSMgr) Regenerate(keepKey bool) (tlscert.InfoData, error) {
	if m.regenErr != nil {
		return tlscert.InfoData{}, m.regenErr
	}
	if m.mode == "file" {
		return tlscert.InfoData{}, ErrTLSFileMode
	}
	m.regen = append(m.regen, keepKey)
	m.info.SerialHex = "NEW"
	return m.info, nil
}
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

// TestTLSInfoAndDownloads: GET /api/tls is public (also with basic auth
// on), carries mode/info/hosts; the two downloads are attachments with the
// right media types and never need auth.
func TestTLSInfoAndDownloads(t *testing.T) {
	m := newFakeTLSMgr("auto")
	e := tlsEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: PasswordHash("admin", "pw")}, m)
	r := e.do(t, "GET", "/api/tls", "", nil)
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
	r = e.do(t, "GET", "/api/tls/cert.crt", "", nil)
	name := wantAttachment(t, r, "application/x-pem-file", `^n5-fangov-[A-Za-z0-9.-]+\.crt$`)
	if !strings.HasPrefix(r.body, "-----BEGIN CERTIFICATE-----") {
		t.Errorf("crt body %q (%s)", r.body, name)
	}
	r = e.do(t, "GET", "/api/tls/cert.cer", "", nil)
	wantAttachment(t, r, "application/pkix-cert", `^n5-fangov-[A-Za-z0-9.-]+\.cer$`)
	if r.body != string([]byte{0x30, 0x82, 0x01, 0x02}) {
		t.Errorf("cer body % x", r.body)
	}
	// empty list members are [] not null
	m.info.DNSNames, m.info.IPs = nil, nil
	r = e.do(t, "GET", "/api/tls", "", nil)
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
