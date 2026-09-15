package web

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/tlscert"
)

// TLSMgr is the certificate manager behind /api/tls (implemented in cmd
// wiring: it knows the config path, the tls directory and the SAN hosts).
// Every method that changes the served certificate hot-swaps it through
// the tlscert.Store the listener uses. mode is "auto", "file" or "off".
type TLSMgr interface {
	Info() (info tlscert.InfoData, mode string, err error)
	ExportPEM() ([]byte, error)
	ExportDER() ([]byte, error)
	// Regenerate reissues the automatic certificate; keepKey keeps the
	// private key so trust imported into browsers survives. kept reports
	// whether the key really was kept (L2): a stored key that cannot be
	// loaded yields a new pair even with keepKey, and the response says so.
	Regenerate(keepKey bool) (info tlscert.InfoData, kept bool, err error)
	// Upload validates and installs an operator-supplied PEM pair (mode
	// becomes "file"); the warnings come from tlscert.ValidatePair.
	Upload(certPEM, keyPEM []byte) (tlscert.InfoData, []string, error)
	// ResetAuto returns to the automatic certificate (mode "auto") and
	// removes the uploaded pair.
	ResetAuto() (tlscert.InfoData, error)
}

// TLSFallback is optionally implemented by a TLSMgr: true while the
// configured file pair could not be loaded and the automatic certificate
// is served in its place (M3). GET /api/tls exposes it as "fallback".
type TLSFallback interface {
	Fallback() bool
}

// maxTLSUpload bounds the upload body (two PEM files fit in a few KiB).
const maxTLSUpload = 64 << 10

// ErrTLSOff is returned by a TLSMgr whose listener runs plain HTTP; the
// endpoints answer 409.
var ErrTLSOff = errors.New("tls is off")

// ErrTLSFileMode is returned by TLSMgr.Regenerate while an uploaded
// certificate is active (409): reset to auto first.
var ErrTLSFileMode = errors.New("a custom certificate is active; reset to auto before regenerating")

// tlsMgr returns the manager or answers 501; with off=true it also answers
// 409 when TLS is off (every endpoint except GET /api/tls).
func (s *Server) tlsMgr(w http.ResponseWriter, off bool) (TLSMgr, bool) {
	m := s.deps.TLSMgr
	if m == nil {
		writeError(w, http.StatusNotImplemented, "no certificate manager")
		return nil, false
	}
	if off {
		if _, mode, err := m.Info(); err == nil && mode == "off" {
			writeError(w, http.StatusConflict, ErrTLSOff.Error())
			return nil, false
		}
	}
	return m, true
}

// tlsFail maps a manager error: ErrTLSOff → 409, anything else → 500 (or
// 400 for validation failures the caller marks).
func tlsFail(w http.ResponseWriter, err error, clientErr bool) {
	switch {
	case errors.Is(err, ErrTLSOff), errors.Is(err, ErrTLSFileMode):
		writeError(w, http.StatusConflict, err.Error())
	case clientErr:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

// getTLS: {mode, info, hosts, warnings, fallback}. Public — the
// certificate is what every client sees in the handshake anyway; the
// panel needs it before login. warnings are computed here the way
// ValidatePair does it (L4): expiry, no SANs, listen hosts the SAN list
// does not cover (loopback excluded) — the UI does no matching of its own.
func (s *Server) getTLS(w http.ResponseWriter, r *http.Request) {
	m, ok := s.tlsMgr(w, false)
	if !ok {
		return
	}
	info, mode, err := m.Info()
	if err != nil && !errors.Is(err, ErrTLSOff) {
		tlsFail(w, err, false)
		return
	}
	out := map[string]any{"mode": mode, "hosts": nonNil(s.deps.TLSHosts), "warnings": []string{}, "fallback": false}
	if fb, ok := m.(TLSFallback); ok && fb.Fallback() {
		out["fallback"] = true
	}
	if mode == "off" || info.FingerprintSHA256 == "" {
		out["info"] = nil
	} else {
		out["info"] = tlsInfoJSON(info)
		out["warnings"] = nonNil(tlscert.Warnings(info, s.deps.TLSHosts, time.Now()))
	}
	writeJSON(w, http.StatusOK, out)
}

// tlsInfoJSON keeps the list members non-null for the UI.
func tlsInfoJSON(i tlscert.InfoData) tlscert.InfoData {
	i.DNSNames = nonNil(i.DNSNames)
	i.IPs = nonNil(i.IPs)
	return i
}

func (s *Server) tlsCertPEM(w http.ResponseWriter, r *http.Request) {
	m, ok := s.tlsMgr(w, true)
	if !ok {
		return
	}
	data, err := m.ExportPEM()
	if err != nil {
		tlsFail(w, err, false)
		return
	}
	attachment(w, "application/x-pem-file", "n5-fangov-"+hostLabel()+".crt")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) tlsCertDER(w http.ResponseWriter, r *http.Request) {
	m, ok := s.tlsMgr(w, true)
	if !ok {
		return
	}
	der, err := m.ExportDER()
	if err != nil {
		tlsFail(w, err, false)
		return
	}
	attachment(w, "application/pkix-cert", "n5-fangov-"+hostLabel()+".cer")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(der)
}

// tlsRegenerate: {"keep_key": true} (default true; an empty body is
// allowed). With a new key the response carries a warning: every store
// that trusts the old certificate has to import the new one.
func (s *Server) tlsRegenerate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.tlsMgr(w, true)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxOverrideBody))
	if err != nil {
		if isTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body exceeds %d bytes", maxOverrideBody))
			return
		}
		writeError(w, http.StatusBadRequest, "unreadable body: "+err.Error())
		return
	}
	keep := true
	if len(strings.TrimSpace(string(body))) > 0 {
		var b struct {
			KeepKey *bool `json:"keep_key"`
		}
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&b); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		if b.KeepKey != nil {
			keep = *b.KeepKey
		}
	}
	info, kept, err := m.Regenerate(keep)
	if err != nil {
		tlsFail(w, err, false)
		return
	}
	s.logf("web: tls regenerate (keep_key=%v, kept=%v) by %s", keep, kept, remoteIP(r))
	out := map[string]any{"ok": true, "keep_key": keep, "kept": kept, "info": tlsInfoJSON(info)}
	switch {
	case !keep:
		out["warning"] = "new private key: the trust imported from the previous certificate no longer applies — download and trust the certificate again"
	case !kept:
		out["warning"] = "the stored private key could not be reused, so a new pair was generated: the trust imported from the previous certificate no longer applies — download and trust the certificate again"
	}
	writeJSON(w, http.StatusOK, out)
}

// tlsUpload accepts multipart/form-data (parts "cert" and "key", file or
// field, optional "force") or JSON {"cert": pem, "key": pem, "force":
// bool}, at most maxTLSUpload bytes.
//
// M4: a request that arrived over this listener's TLS names the host the
// operator is connected through (SNI, else the Host header). A leaf that
// does not cover that name is refused with 400 and "force_required":
// after the swap the browser would see a name mismatch and, under the
// HSTS this listener sends, refuse the connection outright — the operator
// would be locked out of the panel that could undo it. force=true
// overrides (the operator reaches the dashboard by another name).
func (s *Server) tlsUpload(w http.ResponseWriter, r *http.Request) {
	m, ok := s.tlsMgr(w, true)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTLSUpload)
	ctype, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var certPEM, keyPEM []byte
	force := false
	switch ctype {
	case "multipart/form-data":
		if err := r.ParseMultipartForm(maxTLSUpload); err != nil {
			if isTooLarge(err) || strings.Contains(err.Error(), "request body too large") {
				writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body exceeds %d bytes", maxTLSUpload))
				return
			}
			writeError(w, http.StatusBadRequest, "multipart: "+err.Error())
			return
		}
		var err error
		if certPEM, err = formPart(r, "cert"); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if keyPEM, err = formPart(r, "key"); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		switch strings.ToLower(strings.TrimSpace(r.FormValue("force"))) {
		case "1", "true", "on", "yes":
			force = true
		}
	default:
		body, err := io.ReadAll(r.Body)
		if err != nil {
			if isTooLarge(err) {
				writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body exceeds %d bytes", maxTLSUpload))
				return
			}
			writeError(w, http.StatusBadRequest, "unreadable body: "+err.Error())
			return
		}
		var b struct {
			Cert  string `json:"cert"`
			Key   string `json:"key"`
			Force bool   `json:"force"`
		}
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&b); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body (expected {\"cert\": pem, \"key\": pem}): "+err.Error())
			return
		}
		certPEM, keyPEM, force = []byte(b.Cert), []byte(b.Key), b.Force
	}
	if len(strings.TrimSpace(string(certPEM))) == 0 || len(strings.TrimSpace(string(keyPEM))) == 0 {
		writeError(w, http.StatusBadRequest, "cert and key are both required (PEM)")
		return
	}
	if host := connectedHost(r); host != "" && !force {
		if uncovered, ok := leafLacksHost(certPEM, host); ok && uncovered {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": fmt.Sprintf("certificate does not cover %q, the name this browser session uses: after the swap the browser would see a name mismatch and, under HSTS, refuse the connection — you would be locked out of this panel. Reach the dashboard through a name the certificate covers, or resend with force=true if you know what you are doing.", host),
				"host":  host, "force_required": true,
			})
			return
		}
	}
	info, warns, err := m.Upload(certPEM, keyPEM)
	if err != nil {
		// A pair that does not validate is the client's problem; anything
		// after validation (file, config) is ours.
		tlsFail(w, err, isValidationError(err))
		return
	}
	s.logf("web: tls upload by %s (%q, %d warning(s), force=%v)", remoteIP(r), info.Subject, len(warns), force)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "file", "info": tlsInfoJSON(info), "warnings": nonNil(warns)})
}

// connectedHost is the name the client reached this TLS listener by: the
// SNI of the handshake, else the Host header without port ("" when the
// request did not arrive over TLS — the unix socket and plain HTTP have
// no HSTS lock-out to guard against).
func connectedHost(r *http.Request) string {
	if r.TLS == nil {
		return ""
	}
	if r.TLS.ServerName != "" {
		return strings.ToLower(strings.TrimSuffix(r.TLS.ServerName, "."))
	}
	return normalizeHost(r.Host)
}

// leafLacksHost parses the first CERTIFICATE block of certPEM and reports
// whether its SANs fail to cover host (IP literals against the IP SANs,
// names against DNS SANs with x509's wildcard rules). ok is false when
// there is no parsable leaf — then ValidatePair produces the real error.
func leafLacksHost(certPEM []byte, host string) (lacks, ok bool) {
	for rest := certPEM; len(rest) > 0; {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			return false, false
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		leaf, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return false, false
		}
		return leaf.VerifyHostname(host) != nil, true
	}
	return false, false
}

// ValidationError marks an Upload error caused by the submitted pair
// (400) rather than by storing it (500).
type ValidationError struct{ Err error }

func (e ValidationError) Error() string { return e.Err.Error() }
func (e ValidationError) Unwrap() error { return e.Err }

func isValidationError(err error) bool {
	var ve ValidationError
	return errors.As(err, &ve)
}

// formPart returns the content of a multipart part, from a file or a
// plain field of that name.
func formPart(r *http.Request, name string) ([]byte, error) {
	if f, _, err := r.FormFile(name); err == nil {
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, maxTLSUpload+1))
		if err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		if len(b) > maxTLSUpload {
			return nil, fmt.Errorf("%s exceeds %d bytes", name, maxTLSUpload)
		}
		return b, nil
	}
	if v := r.FormValue(name); v != "" {
		return []byte(v), nil
	}
	return nil, fmt.Errorf("multipart part %q missing", name)
}

func (s *Server) tlsReset(w http.ResponseWriter, r *http.Request) {
	m, ok := s.tlsMgr(w, true)
	if !ok {
		return
	}
	info, err := m.ResetAuto()
	if err != nil {
		tlsFail(w, err, false)
		return
	}
	s.logf("web: tls reset to auto by %s", remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "auto", "info": tlsInfoJSON(info)})
}
