// token.go holds the API tokens (DESIGN.md "API tokens"): named bearer
// secrets with a scope and an optional expiry for scripts, Home Assistant
// and agents, kept like the sessions — sha256(secret) → Token in memory,
// mirrored to <state dir>/tokens.json — plus the per-token request
// limiter the guard applies to bearer callers.
package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SirRenix/n5-fangov/internal/fsutil"
)

// Token limits and formats.
const (
	tokenPrefix    = "n5t_"
	tokenSecretLen = 32          // random bytes behind the prefix (43 chars base64url)
	tokenMax       = 50          // stored tokens; creation beyond → ErrTokenLimit
	tokenTouch     = time.Minute // LastUsed/LastIP are refreshed at most this often
	tokenIDLen     = 8           // hex characters of the hash shown as id
	// TokenTTLDefault is the expiry applied when a creation names none;
	// TokenTTLMax bounds ttl_days.
	TokenTTLDefault = 90
	TokenTTLMax     = 3650
)

// Token scopes, cumulative: read ⊂ control ⊂ admin.
const (
	ScopeRead    = "read"
	ScopeControl = "control"
	ScopeAdmin   = "admin"
)

// TokenScopes lists the scopes a token may carry.
var TokenScopes = []string{ScopeRead, ScopeControl, ScopeAdmin}

// TokenNameRe is the token name rule: a letter or digit first, then up
// to 31 of letters, digits, blanks, ".", "_" and "-".
var TokenNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,31}$`)

// Token store errors (the API maps them to 400/409/404).
var (
	ErrTokenName  = errors.New("token name must match " + TokenNameRe.String())
	ErrTokenScope = errors.New("token scope must be read, control or admin")
	ErrTokenTaken = errors.New("token name already in use")
	ErrTokenLimit = fmt.Errorf("at most %d tokens", tokenMax)
)

// Token is one API token as the store and GET /api/tokens see it. ID is
// the first 8 hex characters of sha256(secret); the secret itself is
// shown once at creation and never stored.
type Token struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Scope    string    `json:"scope"`
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"` // zero = never
	LastUsed time.Time `json:"last_used"`
	LastIP   string    `json:"last_ip"`
}

// Expired reports whether t is past its expiry at now.
func (t Token) Expired(now time.Time) bool {
	return !t.Expires.IsZero() && !t.Expires.After(now)
}

// TokenStore keeps the API tokens (memory, mirrored to a JSON file when
// one is configured). Implemented by NewTokenStore.
type TokenStore interface {
	// Create mints a token; expires zero = never. The secret is returned
	// once. Errors: ErrTokenName, ErrTokenScope, ErrTokenTaken,
	// ErrTokenLimit.
	Create(name, scope string, expires time.Time) (secret string, t Token, err error)
	// Lookup resolves a presented secret; an unknown or expired token is
	// not ok (expired reports which). A hit touches LastUsed/LastIP (at
	// most once a minute).
	Lookup(secret, ip string) (t Token, ok bool, expired bool)
	// Revoke removes the token with that id; false when there is none.
	Revoke(id string) bool
	// List returns every stored token, newest first (expired ones
	// included: the operator sees and revokes them).
	List() []Token
}

// ScopeAllows reports whether a token scope covers a required one.
func ScopeAllows(have, need string) bool {
	return scopeRank(have) >= scopeRank(need) && scopeRank(need) > 0
}

func scopeRank(s string) int {
	switch s {
	case ScopeRead:
		return 1
	case ScopeControl:
		return 2
	case ScopeAdmin:
		return 3
	}
	return 0
}

// tokenFile is the JSON mirror layout; key is the full hex sha256(secret).
type tokenFile struct {
	Format int          `json:"format"`
	Tokens []tokenEntry `json:"tokens"`
}

type tokenEntry struct {
	Key string `json:"key"`
	Token
}

type tokenStore struct {
	mu          sync.Mutex
	path        string
	logf        func(string, ...any)
	now         func() time.Time
	byKey       map[string]Token
	writeFailed bool
}

// NewTokenStore returns the store behind the bearer tokens. path != ""
// mirrors every change to that JSON file (0600, atomic) and loads it at
// start. A file that cannot be written is logged once; the store
// continues in memory.
func NewTokenStore(path string, logf func(string, ...any)) TokenStore {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &tokenStore{path: path, logf: logf, now: time.Now, byKey: map[string]Token{}}
	s.load()
	return s
}

// tokenKey is the map key and file key of a secret.
func tokenKey(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ValidTokenID reports whether id has the shape of a token id (8 hex).
func ValidTokenID(id string) bool {
	if len(id) != tokenIDLen {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (s *tokenStore) Create(name, scope string, expires time.Time) (string, Token, error) {
	if !TokenNameRe.MatchString(name) {
		return "", Token{}, ErrTokenName
	}
	if scope == "" {
		scope = ScopeRead
	}
	if scopeRank(scope) == 0 {
		return "", Token{}, ErrTokenScope
	}
	raw := make([]byte, tokenSecretLen)
	if _, err := rand.Read(raw); err != nil {
		return "", Token{}, fmt.Errorf("token secret: %w", err)
	}
	secret := tokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	key := tokenKey(secret)
	now := s.now()
	t := Token{ID: key[:tokenIDLen], Name: name, Scope: scope, Created: now, Expires: expires}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cur := range s.byKey {
		if strings.EqualFold(cur.Name, name) {
			return "", Token{}, ErrTokenTaken
		}
	}
	if len(s.byKey) >= tokenMax {
		return "", Token{}, ErrTokenLimit
	}
	s.byKey[key] = t
	s.saveLocked()
	return secret, t, nil
}

func (s *tokenStore) Lookup(secret, ip string) (Token, bool, bool) {
	if !strings.HasPrefix(secret, tokenPrefix) {
		return Token{}, false, false
	}
	key := tokenKey(secret)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.byKey[key]
	if !ok {
		return Token{}, false, false
	}
	if t.Expired(now) {
		return Token{}, false, true
	}
	if now.Sub(t.LastUsed) >= tokenTouch || t.LastUsed.IsZero() || now.Before(t.LastUsed) {
		t.LastUsed, t.LastIP = now, ip
		s.byKey[key] = t
		s.saveLocked()
	}
	return t, true, false
}

func (s *tokenStore) Revoke(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.byKey {
		if t.ID == id {
			delete(s.byKey, k)
			s.saveLocked()
			return true
		}
	}
	return false
}

func (s *tokenStore) List() []Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Token, 0, len(s.byKey))
	for _, t := range s.byKey {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// load reads the mirror file; a missing file is the normal first start.
func (s *tokenStore) load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.logf("web: tokens: read %s: %v (starting empty)", s.path, err)
		}
		return
	}
	var f tokenFile
	if err := json.Unmarshal(data, &f); err != nil {
		s.logf("web: tokens: %s: %v (starting empty)", s.path, err)
		return
	}
	if f.Format != 1 {
		s.logf("web: tokens: %s: format %d not supported (starting empty)", s.path, f.Format)
		return
	}
	for _, e := range f.Tokens {
		if len(e.Key) != sha256.Size*2 || !TokenNameRe.MatchString(e.Name) || scopeRank(e.Scope) == 0 {
			continue
		}
		e.Token.ID = e.Key[:tokenIDLen]
		s.byKey[e.Key] = e.Token
		if len(s.byKey) >= tokenMax {
			break
		}
	}
}

// saveLocked mirrors the map to the file: temp file next to it, 0600,
// rename. The first failure is logged; the store keeps working in memory.
func (s *tokenStore) saveLocked() {
	if s.path == "" {
		return
	}
	f := tokenFile{Format: 1, Tokens: make([]tokenEntry, 0, len(s.byKey))}
	for k, t := range s.byKey {
		f.Tokens = append(f.Tokens, tokenEntry{Key: k, Token: t})
	}
	sort.Slice(f.Tokens, func(i, j int) bool { return f.Tokens[i].Key < f.Tokens[j].Key })
	data, err := json.MarshalIndent(f, "", "  ")
	if err == nil {
		err = fsutil.WriteAtomic(s.path, append(data, '\n'), 0o600)
	}
	if err != nil {
		if !s.writeFailed {
			s.logf("web: tokens: write %s: %v (continuing in memory)", s.path, err)
			s.writeFailed = true
		}
		return
	}
	s.writeFailed = false
}

// ---- per-token request limiter -------------------------------------------

// A bearer caller is bounded to tokenRatePerSec requests per second
// sustained with a burst of tokenRateBurst (token bucket per token id, in
// memory); above that the guard answers 429. Cookie and Basic callers are
// never limited here.
const (
	tokenRatePerSec = 20
	tokenRateBurst  = 40
	// tokenRateEntries bounds the buckets kept; idle ones are dropped.
	tokenRateEntries = 256
	tokenRateIdle    = 10 * time.Minute
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

type tokenLimiter struct {
	mu   sync.Mutex
	by   map[string]*tokenBucket
	now  func() time.Time
	rate float64
	max  float64
}

func newTokenLimiter() *tokenLimiter {
	return &tokenLimiter{by: map[string]*tokenBucket{}, now: time.Now, rate: tokenRatePerSec, max: tokenRateBurst}
}

// allow takes one request from the bucket of id; false when it is empty.
func (l *tokenLimiter) allow(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.by[id]
	if b == nil {
		if len(l.by) >= tokenRateEntries {
			l.pruneLocked(now)
		}
		b = &tokenBucket{tokens: l.max, last: now}
		l.by[id] = b
	} else {
		if el := now.Sub(b.last).Seconds(); el > 0 {
			b.tokens += el * l.rate
			if b.tokens > l.max {
				b.tokens = l.max
			}
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// pruneLocked drops buckets idle for tokenRateIdle; when none is, the
// oldest one goes (the table is bounded whatever the load).
func (l *tokenLimiter) pruneLocked(now time.Time) {
	var oldest time.Time
	oldestID := ""
	for id, b := range l.by {
		if now.Sub(b.last) > tokenRateIdle {
			delete(l.by, id)
			continue
		}
		if oldestID == "" || b.last.Before(oldest) {
			oldest, oldestID = b.last, id
		}
	}
	if len(l.by) >= tokenRateEntries && oldestID != "" {
		delete(l.by, oldestID)
	}
}
