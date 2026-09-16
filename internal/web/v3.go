package web

// v0.3.0-beta handlers (DESIGN.md "v0.3.0-beta contract"): sessions,
// account, alerts panel, dashboard sensors, about, preset delete.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"regexp"
	"runtime"
	"time"
	"unicode/utf8"
)

// maxJSONBody bounds the small JSON bodies of the v0.3 endpoints.
const maxJSONBody = maxOverrideBody

// Password and user rules for the account endpoints.
const (
	minPasswordLen = 8
	maxPasswordLen = 128
)

var userName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,32}$`)

// decodeJSON reads a small JSON object body (413 over maxJSONBody, 400 on
// malformed JSON or unknown members); false means the answer was written.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if isTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("body exceeds %d bytes", maxJSONBody))
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

// ---- sessions ---------------------------------------------------------------

// setSessionCookie sends the session cookie; maxAge <= 0 clears it. Secure
// only over TLS, so a plain-HTTP loopback setup still works; SameSite=Strict
// plus the CSRF header keep a cross-site page from riding the cookie.
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	ck := &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: maxAge}
	if maxAge <= 0 {
		ck.Value = ""
		ck.MaxAge = -1
	}
	http.SetCookie(w, ck)
}

// login: {"user","password","remember"} → session cookie. Throttled like a
// basic credential (same limiter, per IP): busy → 429, a wrong pair counts,
// sleeps and is logged; a right pair resets the counter.
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.basicMode() {
		writeError(w, http.StatusConflict, "auth is none")
		return
	}
	ip := remoteIP(r)
	if s.limiter.busy(ip) {
		writeError(w, http.StatusTooManyRequests, "too many concurrent authentication attempts")
		return
	}
	var b struct {
		User     string `json:"user"`
		Password string `json:"password"`
		Remember bool   `json:"remember"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if !s.credentialsOK(b.User, b.Password) {
		n, delay := s.limiter.fail(ip)
		s.logf("web: login failure from %s (user %q, %d recent failures, delay %s)", ip, b.User, n, delay)
		writeError(w, http.StatusUnauthorized, "invalid user or password")
		return
	}
	s.limiter.reset(ip)
	token, sess, err := s.sessions.Create(b.User, b.Remember, ip)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create session: "+err.Error())
		return
	}
	setSessionCookie(w, r, token, int(time.Until(sess.Expires).Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": sess.User, "expires": sess.Expires.Unix(), "remember": sess.Remember})
}

// logout revokes the cookie session (if any) and clears the cookie; 204.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(sessionCookie); err == nil && ck.Value != "" {
		s.sessions.Revoke(ck.Value)
	}
	setSessionCookie(w, r, "", 0)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// getSession reports who the caller is; public so the UI can decide what
// to render before any login.
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	c := CallerFrom(r.Context())
	out := map[string]any{"authenticated": c.Authenticated, "mode": s.authMode(), "user": c.User, "via": c.Via}
	if c.Via == "cookie" {
		out["expires"] = c.session.Expires.Unix()
		out["remember"] = c.session.Remember
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- account ----------------------------------------------------------------

// account answers 501 without a store and 409 with auth = none (for the
// state changes); returns false when the answer was written.
func (s *Server) account(w http.ResponseWriter, change bool) bool {
	if s.deps.Account == nil {
		writeError(w, http.StatusNotImplemented, "no account store")
		return false
	}
	if change && !s.basicMode() {
		writeError(w, http.StatusConflict, "auth is none")
		return false
	}
	return true
}

func (s *Server) getAccount(w http.ResponseWriter, r *http.Request) {
	if !s.account(w, false) {
		return
	}
	c := CallerFrom(r.Context())
	type sessionJSON struct {
		ID       string    `json:"id"`
		Created  time.Time `json:"created"`
		Expires  time.Time `json:"expires"`
		LastSeen time.Time `json:"last_seen"`
		Remember bool      `json:"remember"`
		IP       string    `json:"ip"`
		Current  bool      `json:"current"`
	}
	list := s.sessions.List()
	out := make([]sessionJSON, 0, len(list))
	for _, sess := range list {
		out = append(out, sessionJSON{ID: sess.ID, Created: sess.Created, Expires: sess.Expires, LastSeen: sess.LastSeen, Remember: sess.Remember, IP: sess.IP, Current: c.Via == "cookie" && sess.ID == c.session.ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": s.authCfg().User, "mode": s.authMode(), "sessions": out})
}

// verifyCurrent checks the current password for an account change: busy →
// 429, wrong → 403 (counted and logged like a failed login); false means
// the answer was written.
func (s *Server) verifyCurrent(w http.ResponseWriter, r *http.Request, current string) bool {
	ip := remoteIP(r)
	if s.limiter.busy(ip) {
		writeError(w, http.StatusTooManyRequests, "too many concurrent authentication attempts")
		return false
	}
	cfg := s.authCfg()
	if !VerifyPassword(cfg.User, current, cfg.PasswordHash) {
		n, delay := s.limiter.fail(ip)
		s.logf("web: auth failure from %s (user %q, account change, %d recent failures, delay %s)", ip, cfg.User, n, delay)
		writeError(w, http.StatusForbidden, "current password wrong")
		return false
	}
	s.limiter.reset(ip)
	return true
}

// applyAccount stores the new credentials, swaps them in and signs every
// other session out (the caller's cookie survives; a basic caller has none).
func (s *Server) applyAccount(w http.ResponseWriter, r *http.Request, what, user, hash string) bool {
	cfg, err := s.deps.Account.Update(user, hash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update account: "+err.Error())
		return false
	}
	s.auth.Store(&cfg)
	s.sessions.RevokeAll(CallerFrom(r.Context()).token)
	s.logf("web: account %s changed by %s", what, remoteIP(r))
	return true
}

// accountPassword: {"current_password","new_password"}; the new hash is
// PBKDF2 for the current user.
func (s *Server) accountPassword(w http.ResponseWriter, r *http.Request) {
	if !s.account(w, true) {
		return
	}
	var b struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if n := utf8.RuneCountInString(b.New); n < minPasswordLen || n > maxPasswordLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("new password must be %d..%d characters", minPasswordLen, maxPasswordLen))
		return
	}
	if !s.verifyCurrent(w, r, b.Current) {
		return
	}
	if !s.applyAccount(w, r, "password", "", PasswordHash(s.authCfg().User, b.New)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// accountUser: {"current_password","user"}. The hash is always recomputed
// for the new name: the legacy form binds the name into the digest, and
// re-hashing unconditionally is simpler than telling the forms apart.
func (s *Server) accountUser(w http.ResponseWriter, r *http.Request) {
	if !s.account(w, true) {
		return
	}
	var b struct {
		Current string `json:"current_password"`
		User    string `json:"user"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if !userName.MatchString(b.User) {
		writeError(w, http.StatusBadRequest, "user must match "+userName.String())
		return
	}
	if !s.verifyCurrent(w, r, b.Current) {
		return
	}
	if !s.applyAccount(w, r, "user", b.User, PasswordHash(b.User, b.Current)) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": s.authCfg().User})
}

// accountRevoke: {"others":true} signs every other session out.
func (s *Server) accountRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.account(w, false) {
		return
	}
	var b struct {
		Others bool `json:"others"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if !b.Others {
		writeError(w, http.StatusBadRequest, `"others" must be true`)
		return
	}
	before := len(s.sessions.List())
	s.sessions.RevokeAll(CallerFrom(r.Context()).token)
	revoked := before - len(s.sessions.List())
	s.logf("web: %d session(s) revoked by %s", revoked, remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": revoked})
}

// ---- alerts -----------------------------------------------------------------

func (s *Server) alerts(w http.ResponseWriter) (AlertMgr, bool) {
	if s.deps.Alerts == nil {
		writeError(w, http.StatusNotImplemented, "no alert manager")
		return nil, false
	}
	return s.deps.Alerts, true
}

// alertStatusJSON flattens an AlertStatus into a map so the panel document
// can carry "last" and "recent" next to its members.
func alertStatusJSON(st AlertStatus) map[string]any {
	if st.Kinds == nil {
		st.Kinds = []AlertKind{}
	}
	b, _ := json.Marshal(st)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

// getAlerts: AlertStatus + last-sent times (from the snapshot) + the ring.
func (s *Server) getAlerts(w http.ResponseWriter, r *http.Request) {
	m, ok := s.alerts(w)
	if !ok {
		return
	}
	out := alertStatusJSON(m.Status())
	// last-sent times: the controller's stamps, overlaid by the ring (which
	// also sees serve's own alerts: config, kernel, tls, test).
	last := map[string]int64{}
	if s.deps.Service != nil {
		for k, v := range s.deps.Service.Snapshot().Alerts {
			last[k] = v
		}
	}
	if l, ok := m.(interface{ Last() map[string]int64 }); ok {
		for k, v := range l.Last() {
			if v > last[k] {
				last[k] = v
			}
		}
	}
	out["last"] = last
	recent := m.Recent(50)
	if recent == nil {
		recent = []AlertRecord{}
	}
	out["recent"] = recent
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) alertsTest(w http.ResponseWriter, r *http.Request) {
	m, ok := s.alerts(w)
	if !ok {
		return
	}
	transport, err := m.Test()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "transport": transport})
		return
	}
	s.logf("web: test alert sent via %s by %s", transport, remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "transport": transport})
}

// alertsTemplate installs the PVE notification templates: 501 without
// PVE, 500 with the CLI fallback in the message.
func (s *Server) alertsTemplate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.alerts(w)
	if !ok {
		return
	}
	path, err := m.InstallTemplate()
	if err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			writeError(w, http.StatusNotImplemented, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error()+"; run: n5-fangov alerts template")
		return
	}
	s.logf("web: alert template installed at %s by %s", path, remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path})
}

// putAlerts: {"transport","mail_to"} → the manager validates and applies.
func (s *Server) putAlerts(w http.ResponseWriter, r *http.Request) {
	m, ok := s.alerts(w)
	if !ok {
		return
	}
	var b struct {
		Transport string `json:"transport"`
		MailTo    string `json:"mail_to"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	st, err := m.Configure(b.Transport, b.MailTo)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.logf("web: alert transport set to %q (mail_to %q) by %s", b.Transport, b.MailTo, remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": alertStatusJSON(st)})
}

// ---- dashboard --------------------------------------------------------------

// maxDashboardSensors mirrors the config rule (0..8 ids).
const maxDashboardSensors = 8

func (s *Server) dashboard(w http.ResponseWriter) (DashboardStore, bool) {
	if s.deps.Dashboard == nil {
		writeError(w, http.StatusNotImplemented, "no dashboard store")
		return nil, false
	}
	return s.deps.Dashboard, true
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	d, ok := s.dashboard(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sensors": nonNil(d.Sensors())})
}

// putDashboard: {"sensors":[...]} — array required, at most 8, the store
// validates the ids (400 on refusal) and reports unresolvable ones.
func (s *Server) putDashboard(w http.ResponseWriter, r *http.Request) {
	d, ok := s.dashboard(w)
	if !ok {
		return
	}
	var b struct {
		Sensors []string `json:"sensors"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if b.Sensors == nil {
		writeError(w, http.StatusBadRequest, `"sensors" array required`)
		return
	}
	if len(b.Sensors) > maxDashboardSensors {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d sensors", maxDashboardSensors))
		return
	}
	warnings, err := d.SetSensors(b.Sensors)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sensors": nonNil(d.Sensors()), "warnings": nonNil(warnings)})
}

// ---- about ------------------------------------------------------------------

// getAbout serves Deps.About; empty name/version/go fall back to what the
// server knows so the card is never blank.
func (s *Server) getAbout(w http.ResponseWriter, r *http.Request) {
	a := s.deps.About
	if a.Name == "" {
		a.Name = "n5-fangov"
	}
	if a.Version == "" {
		a.Version = s.deps.Version
	}
	if a.Go == "" {
		a.Go = runtime.Version()
	}
	if a.Credits == nil {
		a.Credits = []Credit{}
	}
	writeJSON(w, http.StatusOK, a)
}

// ---- presets ----------------------------------------------------------------

// deletePreset removes a user preset: ErrPresetBuiltin → 409, missing →
// 404; a store without Delete answers 501.
func (s *Server) deletePreset(w http.ResponseWriter, r *http.Request) {
	if s.deps.Presets == nil {
		writeError(w, http.StatusNotImplemented, "no preset store")
		return
	}
	d, ok := s.deps.Presets.(PresetDeleter)
	if !ok {
		writeError(w, http.StatusNotImplemented, "preset store cannot delete")
		return
	}
	name := r.PathValue("name")
	if !presetName.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid preset name")
		return
	}
	if err := d.Delete(name); err != nil {
		switch {
		case errors.Is(err, ErrPresetBuiltin):
			writeError(w, http.StatusConflict, "delete preset: "+err.Error())
		case errors.Is(err, fs.ErrNotExist):
			writeError(w, http.StatusNotFound, "unknown preset "+name)
		default:
			writeError(w, http.StatusInternalServerError, "delete preset: "+err.Error())
		}
		return
	}
	s.logf("web: preset %q deleted by %s", name, remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": name})
}
