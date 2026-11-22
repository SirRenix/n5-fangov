// Package cfgver keeps the last versions of the config file (DESIGN
// "Config versions"): before every write by the daemon the text on disk is
// stored as a version with time, author and reason, ten are kept, the
// oldest goes. A file that differs from what the daemon last wrote (a hand
// edit) is stored as "external edit" at start and before the next write.
//
// Layout: <dir>/NNNN.toml (the whole text, comments included) and
// <dir>/index.json; files 0600, the directory 0700. Recording never blocks
// the config write: a failure is logged once per cause and the write goes
// on (regulation and operation go before the history).
package cfgver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/SirRenix/n5-fangov/internal/fsutil"
)

// Keep is how many versions are kept.
const Keep = 10

// External is the reason of a version found on disk that the daemon did
// not write.
const External = "external edit"

// Version is one stored version: the text that was on disk before a write.
// By is user:<name>, token:<name>, cli or "" (the daemon itself, a hand
// edit); Reason names the write that replaced it (editor, preset quiet,
// assist hdd on, account, alerts, dashboard, tls, import, restore 7) or
// External.
type Version struct {
	ID     int    `json:"id"`
	TS     int64  `json:"ts"`
	By     string `json:"by"`
	Reason string `json:"reason"`
	SHA    string `json:"sha256"`
}

// index is index.json: the versions oldest first, the next id and the
// hash of the text the daemon last wrote or saw at start.
type index struct {
	Next     int       `json:"next"`
	Current  string    `json:"current"`
	Versions []Version `json:"versions"`
}

// Store is the version directory. The zero value is not usable; Open.
type Store struct {
	dir    string
	logf   func(string, ...any)
	now    func() time.Time
	mu     sync.Mutex
	logged map[string]bool
}

// Open returns the store for dir (created on the first write). logf may
// be nil.
func Open(dir string, logf func(string, ...any)) *Store {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Store{dir: dir, logf: logf, now: time.Now, logged: map[string]bool{}}
}

// Dir is the version directory.
func (s *Store) Dir() string { return s.dir }

// SetClock replaces the clock (tests).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (s *Store) file(id int) string { return filepath.Join(s.dir, fmt.Sprintf("%04d.toml", id)) }

// fail logs err once per cause (the text without the version number).
func (s *Store) fail(what string, err error) {
	key := what + ": " + err.Error()
	if s.logged[key] {
		return
	}
	s.logged[key] = true
	s.logf("config versions: %s: %v (the config write goes on)", what, err)
}

func (s *Store) load() (index, error) {
	var ix index
	b, err := os.ReadFile(filepath.Join(s.dir, "index.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return index{Next: 1}, nil
	}
	if err != nil {
		return ix, err
	}
	if err := json.Unmarshal(b, &ix); err != nil {
		return ix, fmt.Errorf("index.json: %w", err)
	}
	if ix.Next < 1 {
		ix.Next = 1
	}
	return ix, nil
}

func (s *Store) save(ix index) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(ix, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteAtomic(filepath.Join(s.dir, "index.json"), append(b, '\n'), 0o600)
}

// add stores raw as a new version unless it equals the newest one; the
// ring keeps Keep. Called with s.mu held.
func (s *Store) add(ix *index, raw []byte, by, reason string) error {
	h := sum(raw)
	if n := len(ix.Versions); n > 0 && ix.Versions[n-1].SHA == h {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	v := Version{ID: ix.Next, TS: s.now().Unix(), By: by, Reason: reason, SHA: h}
	if err := fsutil.WriteAtomic(s.file(v.ID), raw, 0o600); err != nil {
		return err
	}
	ix.Next++
	ix.Versions = append(ix.Versions, v)
	for len(ix.Versions) > Keep {
		_ = os.Remove(s.file(ix.Versions[0].ID))
		ix.Versions = ix.Versions[1:]
	}
	return nil
}

// Start compares the file at path with the text the daemon last wrote:
// different (and known) = a hand edit, stored as External. Then the file
// is the known text. A missing file or index records nothing.
func (s *Store) Start(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	ix, err := s.load()
	if err != nil {
		s.fail("read", err)
		return
	}
	if h := sum(raw); ix.Current != h {
		if ix.Current != "" {
			if err := s.add(&ix, raw, "", External); err != nil {
				s.fail("store", err)
				return
			}
		}
		ix.Current = h
		if err := s.save(ix); err != nil {
			s.fail("index", err)
		}
	}
}

// Before stores the text at path before the daemon writes it. A file that
// differs from the known text is a hand edit: stored as External (the
// write's own version would be the same text and is skipped). reason ""
// stores nothing (the scheduler's preset switch).
func (s *Store) Before(path, by, reason string) {
	if reason == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.fail("read config", err)
		}
		return
	}
	ix, err := s.load()
	if err != nil {
		s.fail("read", err)
		return
	}
	if ix.Current != "" && ix.Current != sum(raw) {
		by, reason = "", External
	}
	if err := s.add(&ix, raw, by, reason); err != nil {
		s.fail("store", err)
		return
	}
	if err := s.save(ix); err != nil {
		s.fail("index", err)
	}
}

// Wrote records raw as the text the daemon wrote (every write, the
// scheduler's included, so it is not taken for a hand edit later).
func (s *Store) Wrote(raw []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix, err := s.load()
	if err != nil {
		s.fail("read", err)
		return
	}
	if h := sum(raw); ix.Current != h {
		ix.Current = h
		if err := s.save(ix); err != nil {
			s.fail("index", err)
		}
	}
}

// List returns the versions, newest first.
func (s *Store) List() ([]Version, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix, err := s.load()
	if err != nil {
		return nil, err
	}
	out := append([]Version(nil), ix.Versions...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// Get returns version id and its text; fs.ErrNotExist for an unknown id.
func (s *Store) Get(id int) (Version, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix, err := s.load()
	if err != nil {
		return Version{}, nil, err
	}
	for _, v := range ix.Versions {
		if v.ID == id {
			raw, err := os.ReadFile(s.file(id))
			return v, raw, err
		}
	}
	return Version{}, nil, fmt.Errorf("version %d: %w", id, fs.ErrNotExist)
}

// Next returns the text that followed version id: the next newer version,
// or nil for the newest (the caller uses the current file).
func (s *Store) Next(id int) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ix, err := s.load()
	if err != nil {
		return nil, err
	}
	for i, v := range ix.Versions {
		if v.ID == id && i+1 < len(ix.Versions) {
			return os.ReadFile(s.file(ix.Versions[i+1].ID))
		}
	}
	return nil, nil
}
