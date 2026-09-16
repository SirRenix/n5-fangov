// token_api.go holds the token endpoints (/api/tokens: list, create,
// revoke). They are session-only: the guard answers 403 to a bearer
// caller before any of them runs (a leaked admin token cannot mint
// tokens); cookie, Basic and socket callers manage them.
package web

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// tokenJSON is one token as GET /api/tokens lists it: the zero times are
// null (expires: never; last_used: not yet).
type tokenJSON struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Scope    string     `json:"scope"`
	Created  time.Time  `json:"created"`
	Expires  *time.Time `json:"expires"`
	LastUsed *time.Time `json:"last_used"`
	LastIP   string     `json:"last_ip"`
	Expired  bool       `json:"expired"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func tokenView(t Token, now time.Time) tokenJSON {
	return tokenJSON{ID: t.ID, Name: t.Name, Scope: t.Scope, Created: t.Created, Expires: timePtr(t.Expires), LastUsed: timePtr(t.LastUsed), LastIP: t.LastIP, Expired: t.Expired(now)}
}

func (s *Server) getTokens(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	list := s.tokens.List()
	out := make([]tokenJSON, 0, len(list))
	for _, t := range list {
		out = append(out, tokenView(t, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

// createToken: {"name","scope","ttl_days"} → 201 with the secret. scope
// defaults to read, ttl_days to TokenTTLDefault; 0 never expires and
// comes back with a warning. With auth = none a token would be minted by
// anyone who reaches the listener and ignored by the guard — 409 like the
// account changes; list and revoke stay (an operator switching to basic
// may want to clean up first).
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	if !s.basicMode() {
		writeError(w, http.StatusConflict, "auth is none")
		return
	}
	var b struct {
		Name    string `json:"name"`
		Scope   string `json:"scope"`
		TTLDays *int   `json:"ttl_days"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	ttl := TokenTTLDefault
	if b.TTLDays != nil {
		ttl = *b.TTLDays
	}
	if ttl < 0 || ttl > TokenTTLMax {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("ttl_days must be 0..%d", TokenTTLMax))
		return
	}
	var expires time.Time
	if ttl > 0 {
		expires = time.Now().Add(time.Duration(ttl) * 24 * time.Hour)
	}
	secret, t, err := s.tokens.Create(b.Name, b.Scope, expires)
	if err != nil {
		switch {
		case errors.Is(err, ErrTokenTaken), errors.Is(err, ErrTokenLimit):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, ErrTokenName), errors.Is(err, ErrTokenScope):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "create token: "+err.Error())
		}
		return
	}
	c := CallerFrom(r.Context())
	s.logf("web: api token %q (%s, scope %s, expires %s) created by %s via %s", t.Name, t.ID, t.Scope, expiryText(t.Expires), remoteIP(r), c.Via)
	out := map[string]any{"ok": true, "token": secret, "id": t.ID, "name": t.Name, "scope": t.Scope, "expires": timePtr(t.Expires)}
	if t.Expires.IsZero() {
		out["warning"] = "token never expires"
	}
	writeJSON(w, http.StatusCreated, out)
}

func expiryText(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !ValidTokenID(id) {
		writeError(w, http.StatusBadRequest, "token id must be 8 hex characters")
		return
	}
	if !s.tokens.Revoke(id) {
		writeError(w, http.StatusNotFound, "unknown token "+id)
		return
	}
	s.logf("web: api token %s revoked by %s via %s", id, remoteIP(r), CallerFrom(r.Context()).Via)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revoked": id})
}
