package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

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
	// private key so trust imported into browsers survives.
	Regenerate(keepKey bool) (tlscert.InfoData, error)
	// Upload validates and installs an operator-supplied PEM pair (mode
	// becomes "file"); the warnings come from tlscert.ValidatePair.
	Upload(certPEM, keyPEM []byte) (tlscert.InfoData, []string, error)
	// ResetAuto returns to the automatic certificate (mode "auto") and
	// removes the uploaded pair.
	ResetAuto() (tlscert.InfoData, error)
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

// getTLS: {mode, info, hosts}. Public — the certificate is what every
// client sees in the handshake anyway; the panel needs it before login.
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
	out := map[string]any{"mode": mode, "hosts": nonNil(s.deps.TLSHosts)}
	if mode == "off" || info.FingerprintSHA256 == "" {
		out["info"] = nil
	} else {
		out["info"] = tlsInfoJSON(info)
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
	pem, err := m.ExportPEM()
	if err != nil {
		tlsFail(w, err, false)
		return
	}
	attachment(w, "application/x-pem-file", "n5-fangov-"+hostLabel()+".crt")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pem)
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
	info, err := m.Regenerate(keep)
	if err != nil {
		tlsFail(w, err, false)
		return
	}
	s.logf("web: tls regenerate (keep_key=%v) by %s", keep, remoteIP(r))
	out := map[string]any{"ok": true, "keep_key": keep, "info": tlsInfoJSON(info)}
	if !keep {
		out["warning"] = "new private key: the trust imported from the previous certificate no longer applies — download and trust the certificate again"
	}
	writeJSON(w, http.StatusOK, out)
}

// tlsUpload accepts multipart/form-data (parts "cert" and "key", file or
// field) or JSON {"cert": pem, "key": pem}, at most maxTLSUpload bytes.
func (s *Server) tlsUpload(w http.ResponseWriter, r *http.Request) {
	m, ok := s.tlsMgr(w, true)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTLSUpload)
	ctype, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var certPEM, keyPEM []byte
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
			Cert string `json:"cert"`
			Key  string `json:"key"`
		}
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&b); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body (expected {\"cert\": pem, \"key\": pem}): "+err.Error())
			return
		}
		certPEM, keyPEM = []byte(b.Cert), []byte(b.Key)
	}
	if len(strings.TrimSpace(string(certPEM))) == 0 || len(strings.TrimSpace(string(keyPEM))) == 0 {
		writeError(w, http.StatusBadRequest, "cert and key are both required (PEM)")
		return
	}
	info, warns, err := m.Upload(certPEM, keyPEM)
	if err != nil {
		// A pair that does not validate is the client's problem; anything
		// after validation (file, config) is ours.
		tlsFail(w, err, isValidationError(err))
		return
	}
	s.logf("web: tls upload by %s (%s, %d warning(s))", remoteIP(r), info.Subject, len(warns))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mode": "file", "info": tlsInfoJSON(info), "warnings": nonNil(warns)})
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
