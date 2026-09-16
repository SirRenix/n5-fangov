// account_api.go holds the account endpoints (/api/account: password,
// user, revoke) and the password/user rules they enforce.
package web

import (
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/SirRenix/n5-fangov/internal/config"
)

// Password and user rules for the account endpoints (config: one rule for
// the API, the parser and the CLI).
const (
	minPasswordLen = config.MinPasswordLen
	maxPasswordLen = config.MaxPasswordLen
)

var userName = config.UserRe

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
	if !s.limiter.acquire() {
		writeError(w, http.StatusTooManyRequests, "too many concurrent authentication attempts")
		return false
	}
	cfg := s.authCfg()
	ok := VerifyPassword(cfg.User, current, cfg.PasswordHash)
	s.limiter.release()
	if !ok {
		n, delay := s.limiter.fail(ip)
		s.logf("web: auth failure from %s (user %.64q, account change, %d recent failures, delay %s)", ip, cfg.User, n, delay)
		writeError(w, http.StatusForbidden, "current password wrong")
		return false
	}
	s.limiter.reset(ip)
	return true
}

// applyAccount stores the new credentials, swaps them in and signs every
// other session out (the caller's cookie survives; a basic caller has none).
// The store moves to the new credential epoch first, so the kept session
// is still loaded after the next restart; the kept session's User
// follows a rename.
func (s *Server) applyAccount(w http.ResponseWriter, r *http.Request, what, user, hash string) bool {
	cfg, err := s.deps.Account.Update(user, hash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "update account: "+err.Error())
		return false
	}
	s.auth.Store(&cfg)
	s.sessions.SetEpoch(CredentialEpoch(cfg.User, cfg.PasswordHash))
	s.sessions.RevokeAllRename(CallerFrom(r.Context()).token, cfg.User)
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
