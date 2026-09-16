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
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Cookie sessions (DESIGN.md "Sessions"): the browser holds a random token
// in an HttpOnly cookie; the store keeps sha256(token) → Session so a leaked
// mirror file does not hand out usable cookies.
const (
	sessionCookie      = "n5fangov_session"
	sessionTTL         = 12 * time.Hour
	sessionRememberTTL = 30 * 24 * time.Hour
	sessionMax         = 50          // oldest dropped beyond this
	sessionTouch       = time.Minute // LastSeen is refreshed at most this often
	sessionTokenLen    = 32
)

// sessionFile is the JSON mirror layout; key is the full hex sha256(token).
// Epoch is the credential epoch the sessions were issued under (R-M1).
type sessionFile struct {
	Format   int            `json:"format"`
	Epoch    string         `json:"epoch,omitempty"`
	Sessions []sessionEntry `json:"sessions"`
}

type sessionEntry struct {
	Key string `json:"key"`
	Session
}

type sessionStore struct {
	mu          sync.Mutex
	path        string
	epoch       string
	logf        func(string, ...any)
	now         func() time.Time
	byKey       map[string]Session
	writeFailed bool // the write error was logged; stay quiet until it succeeds again
}

// CredentialEpoch identifies a credential set: hex sha256 of user, newline,
// stored hash (R-M1). Sessions are bound to the epoch they were issued
// under; a mirror file written under another epoch — the password was
// rotated with `n5-fangov passwd` or by editing the file while the daemon
// was down — is not loaded.
func CredentialEpoch(user, hash string) string {
	sum := sha256.Sum256([]byte(user + "\n" + hash))
	return hex.EncodeToString(sum[:])
}

// NewSessionStore returns the store behind the cookie sessions. path != ""
// mirrors every change to that JSON file (0600, temp file + rename) and
// loads it at start, so a daemon restart keeps "remember me" sessions. A
// file that cannot be written is logged once; the store continues in memory.
// The epoch is "" (every mirror file is accepted); see NewSessionStoreEpoch.
func NewSessionStore(path string, logf func(string, ...any)) SessionStore {
	return NewSessionStoreEpoch(path, "", logf)
}

// NewSessionStoreEpoch is NewSessionStore bound to a credential epoch
// (CredentialEpoch): a mirror file whose epoch differs is dropped whole,
// with one log line, so a password rotation outside the daemon revokes
// every persisted session (R-M1). epoch "" accepts any file.
func NewSessionStoreEpoch(path, epoch string, logf func(string, ...any)) SessionStore {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &sessionStore{path: path, epoch: epoch, logf: logf, now: time.Now, byKey: map[string]Session{}}
	s.load()
	return s
}

// SetEpoch records a new credential epoch (after a change through the
// account endpoints) and rewrites the mirror, so the sessions kept across
// the change survive the next restart (R-M1).
func (s *sessionStore) SetEpoch(epoch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.epoch == epoch {
		return
	}
	s.epoch = epoch
	s.saveLocked()
}

// sessionKey is the map key and file key of a token; the ID shown to the
// operator is its first 8 hex characters.
func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *sessionStore) Create(user string, remember bool, ip string) (string, Session, error) {
	raw := make([]byte, sessionTokenLen)
	if _, err := rand.Read(raw); err != nil {
		return "", Session{}, fmt.Errorf("session token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	key := sessionKey(token)
	now := s.now()
	ttl := sessionTTL
	if remember {
		ttl = sessionRememberTTL
	}
	sess := Session{ID: key[:8], User: user, IP: ip, Created: now, Expires: now.Add(ttl), LastSeen: now, Remember: remember}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	for len(s.byKey) >= sessionMax {
		s.dropOldestLocked()
	}
	s.byKey[key] = sess
	s.saveLocked()
	return token, sess, nil
}

func (s *sessionStore) Lookup(token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	key := sessionKey(token)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byKey[key]
	if !ok {
		return Session{}, false
	}
	if !sess.Expires.After(now) {
		delete(s.byKey, key)
		s.saveLocked()
		return Session{}, false
	}
	if now.Sub(sess.LastSeen) >= sessionTouch {
		sess.LastSeen = now
		s.byKey[key] = sess
		s.saveLocked()
	}
	return sess, true
}

func (s *sessionStore) Revoke(token string) {
	if token == "" {
		return
	}
	key := sessionKey(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byKey[key]; ok {
		delete(s.byKey, key)
		s.saveLocked()
	}
}

func (s *sessionStore) RevokeAll(keepToken string) { s.RevokeAllRename(keepToken, "") }

// RevokeAllRename is RevokeAll with the kept session's User rewritten to
// newUser ("" keeps it): after a rename the surviving cookie must report
// the new name, not the one it was issued for (R-L10).
func (s *sessionStore) RevokeAllRename(keepToken, newUser string) {
	keep := ""
	if keepToken != "" {
		keep = sessionKey(keepToken)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for k, sess := range s.byKey {
		switch {
		case k != keep:
			delete(s.byKey, k)
			changed = true
		case newUser != "" && sess.User != newUser:
			sess.User = newUser
			s.byKey[k] = sess
			changed = true
		}
	}
	if changed {
		s.saveLocked()
	}
}

// List returns the live sessions, newest first.
func (s *sessionStore) List() []Session {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pruneLocked(now) {
		s.saveLocked()
	}
	out := make([]Session, 0, len(s.byKey))
	for _, sess := range s.byKey {
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.After(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// pruneLocked drops expired sessions; reports whether anything changed.
func (s *sessionStore) pruneLocked(now time.Time) bool {
	changed := false
	for k, sess := range s.byKey {
		if !sess.Expires.After(now) {
			delete(s.byKey, k)
			changed = true
		}
	}
	return changed
}

func (s *sessionStore) dropOldestLocked() {
	oldest := ""
	var t time.Time
	for k, sess := range s.byKey {
		if oldest == "" || sess.Created.Before(t) {
			oldest, t = k, sess.Created
		}
	}
	if oldest != "" {
		delete(s.byKey, oldest)
	}
}

// load reads the mirror file; a missing file is the normal first start.
func (s *sessionStore) load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			s.logf("web: sessions: read %s: %v (starting empty)", s.path, err)
		}
		return
	}
	var f sessionFile
	if err := json.Unmarshal(data, &f); err != nil {
		s.logf("web: sessions: %s: %v (starting empty)", s.path, err)
		return
	}
	if s.epoch != "" && f.Epoch != s.epoch {
		// R-M1: the credentials changed while these sessions were on disk.
		// The file is rewritten under the current epoch at the next change.
		if len(f.Sessions) > 0 {
			s.logf("web: sessions: credentials changed since %s was written, %d session(s) dropped", s.path, len(f.Sessions))
		}
		return
	}
	now := s.now()
	for _, e := range f.Sessions {
		if len(e.Key) != sha256.Size*2 || !e.Expires.After(now) {
			continue
		}
		e.Session.ID = e.Key[:8]
		s.byKey[e.Key] = e.Session
	}
	for len(s.byKey) > sessionMax {
		s.dropOldestLocked()
	}
}

// saveLocked mirrors the map to the file: temp file next to it, 0600,
// rename. The first failure is logged; the store keeps working in memory.
func (s *sessionStore) saveLocked() {
	if s.path == "" {
		return
	}
	f := sessionFile{Format: 1, Epoch: s.epoch, Sessions: make([]sessionEntry, 0, len(s.byKey))}
	for k, sess := range s.byKey {
		f.Sessions = append(f.Sessions, sessionEntry{Key: k, Session: sess})
	}
	sort.Slice(f.Sessions, func(i, j int) bool { return f.Sessions[i].Key < f.Sessions[j].Key })
	data, err := json.MarshalIndent(f, "", "  ")
	if err == nil {
		err = writeFileAtomic(s.path, append(data, '\n'), 0o600)
	}
	if err != nil {
		if !s.writeFailed {
			s.logf("web: sessions: write %s: %v (continuing in memory)", s.path, err)
			s.writeFailed = true
		}
		return
	}
	s.writeFailed = false
}

// writeFileAtomic writes data to a temp file in path's directory and renames
// it over path, so a crash mid-write never leaves a half file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}
