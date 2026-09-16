// session_api.go holds the session endpoints: login, logout and the
// session view (POST /api/session, DELETE /api/session, GET /api/session).
package web

import (
	"net/http"
	"time"
)

// setSessionCookie sends the session cookie; maxAge <= 0 clears it. Secure
// when the request came over TLS, when the listener is known to be
// TLS-terminated (Deps.TLS) or when a TLS reverse proxy is declared
// ([web] behind_tls_proxy); a plain-HTTP loopback setup without either
// still works. SameSite=Strict plus the CSRF header keep a cross-site page
// from riding the cookie.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	secure := r.TLS != nil || s.deps.TLS || s.deps.BehindTLSProxy
	ck := &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure, MaxAge: maxAge}
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
	if !s.limiter.acquire() {
		writeError(w, http.StatusTooManyRequests, "too many concurrent authentication attempts")
		return
	}
	ok := s.credentialsOK(b.User, b.Password)
	s.limiter.release()
	if !ok {
		n, delay := s.limiter.fail(ip)
		s.logf("web: login failure from %s (user %.64q, %d recent failures, delay %s)", ip, b.User, n, delay)
		writeError(w, http.StatusUnauthorized, "invalid user or password")
		return
	}
	s.limiter.reset(ip)
	s.upgradeLegacyHash(b.User, b.Password)
	token, sess, err := s.sessions.Create(b.User, b.Remember, ip)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create session: "+err.Error())
		return
	}
	s.setSessionCookie(w, r, token, int(time.Until(sess.Expires).Seconds()))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": sess.User, "expires": sess.Expires.Unix(), "remember": sess.Remember})
}

// logout revokes the cookie session (if any) and clears the cookie; 204.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if ck, err := r.Cookie(sessionCookie); err == nil && ck.Value != "" {
		s.sessions.Revoke(ck.Value)
	}
	s.setSessionCookie(w, r, "", 0)
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
