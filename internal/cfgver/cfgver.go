// Package cfgver keeps the last versions of the config file (DESIGN
// "Config versions"): before every write by the daemon the text on disk is
// stored as a version with time, author and reason, ten are kept, the
// oldest goes. A version's author and reason name the write that replaced
// it.
//
// Hand edits: the store keeps a copy of the text the daemon last wrote
// (current.toml). A file that differs from it at start or before the next
// write was edited by hand: the daemon's text is stored with the reason
// External (it was replaced by the hand edit), and before a write the hand
// edit itself is stored with the author and reason of that write — so a
// hand edit can be undone too.
//
// Layout: <dir>/NNNN.toml (the whole text, comments included),
// <dir>/index.json and <dir>/current.toml; files 0600, the directory 0700.
// Recording never blocks the config write: a failure is logged once per
// cause and the write goes on (regulation and operation go before the
// history).
package cfgver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/SirRenix/n5-fangov/internal/fsutil"
)

// Keep is how many versions are kept.
const Keep = 10

// External is the reason of a version that a hand edit replaced.
const External = "external edit"

// Version is one stored version: the text that was on disk before a write.
// By is user:<name>, token:<name>, cli, schedule or "" (the daemon itself,
// a hand edit); Reason names the write that replaced it (editor, preset
// quiet, assist hdd on, account, alerts, dashboard, tls, import, restore
// 7) or External.
type Version struct {
	ID     int    `json:"id"`
	TS     int64  `json:"ts"`
	By     string `json:"by"`
	Reason string `json:"reason"`
	SHA    string `json:"sha256"`
}

// index is index.json: the versions oldest first and the next id.
type index struct {
	Next     int       `json:"next"`
	Versions []Version `json:"versions"`
}

// Store is the version directory. The zero value is not usable; Open.
type Store struct {
	dir    string
	logf   func(string, ...any)
	now    func() time.Time
	mu     sync.Mutex
	logged map[string]bool
	// writeIndex writes index.json (tests replace it).
	writeIndex func(path string, data []byte, perm os.FileMode) error
}

// Open returns the store for dir (created on the first write). logf may
// be nil.
func Open(dir string, logf func(string, ...any)) *Store {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Store{dir: dir, logf: logf, now: time.Now, logged: map[string]bool{}, writeIndex: fsutil.WriteAtomic}
}

// Dir is the version directory.
func (s *Store) Dir() string { return s.dir }

// SetClock replaces the clock (tests).
func (s *Store) SetClock(now func() time.Time) { s.now = now }

// Sum is the hex SHA-256 of a text (the "current" token of a restore).
func Sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (s *Store) file(id int) string { return filepath.Join(s.dir, fmt.Sprintf("%04d.toml", id)) }
func (s *Store) known() string      { return filepath.Join(s.dir, "current.toml") }

// tmpName matches the temp files of fsutil and digits: not part of a cause.
var tmpName = regexp.MustCompile(`\S*\.tmp\S*|\d+`)

// cause is the class of err for the once-per-cause log: the operation and
// the OS error without the (random) file names.
func cause(err error) string {
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &pe):
		return pe.Op + ": " + pe.Err.Error()
	case errors.As(err, &le):
		return le.Op + ": " + le.Err.Error()
	}
	return tmpName.ReplaceAllString(err.Error(), "#")
}

// fail logs err once per cause.
func (s *Store) fail(what string, err error) {
	key := what + ": " + cause(err)
	if s.logged[key] {
		return
	}
	s.logged[key] = true
	s.logf("config versions: %s: %v (the config write goes on)", what, err)
}

// maxID is the highest NNNN.toml in the directory (a fresh index after a
// corrupt one continues after it).
func (s *Store) maxID() int {
	n := 0
	ms, _ := filepath.Glob(filepath.Join(s.dir, "[0-9][0-9][0-9][0-9]*.toml"))
	for _, m := range ms {
		if v, err := strconv.Atoi(filepath.Base(m)[:len(filepath.Base(m))-len(".toml")]); err == nil && v > n {
			n = v
		}
	}
	return n
}

// load reads index.json. A file that does not parse is moved aside
// (index.json.corrupt-<unix>) and a fresh index starts; logged once.
func (s *Store) load() (index, error) {
	var ix index
	p := filepath.Join(s.dir, "index.json")
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return index{Next: s.maxID() + 1}, nil
	}
	if err != nil {
		return ix, err
	}
	if err := json.Unmarshal(b, &ix); err != nil {
		aside := fmt.Sprintf("%s.corrupt-%d", p, s.now().Unix())
		if rerr := os.Rename(p, aside); rerr != nil {
			return ix, fmt.Errorf("index.json: %v; not moved aside: %w", err, rerr)
		}
		s.logf("config versions: index.json unreadable (%v), moved to %s; a new index starts", err, aside)
		return index{Next: s.maxID() + 1}, nil
	}
	if ix.Next < 1 {
		ix.Next = s.maxID() + 1
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
	return s.writeIndex(filepath.Join(s.dir, "index.json"), append(b, '\n'), 0o600)
}

// add stores raw as a new version unless it equals the newest one (then
// that version's id is returned: restoring it undoes the write all the
// same); the ids that leave the ring are returned (their files are removed only after
// the index is saved). Called with s.mu held.
func (s *Store) add(ix *index, raw []byte, by, reason string) (int, []int, error) {
	h := Sum(raw)
	if n := len(ix.Versions); n > 0 && ix.Versions[n-1].SHA == h {
		return ix.Versions[n-1].ID, nil, nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return 0, nil, err
	}
	v := Version{ID: ix.Next, TS: s.now().Unix(), By: by, Reason: reason, SHA: h}
	if err := fsutil.WriteAtomic(s.file(v.ID), raw, 0o600); err != nil {
		return 0, nil, err
	}
	ix.Next++
	ix.Versions = append(ix.Versions, v)
	var drop []int
	for len(ix.Versions) > Keep {
		drop = append(drop, ix.Versions[0].ID)
		ix.Versions = ix.Versions[1:]
	}
	return v.ID, drop, nil
}

// commit saves the index, then removes the dropped version files.
func (s *Store) commit(ix index, drop []int) error {
	if err := s.save(ix); err != nil {
		return err
	}
	for _, id := range drop {
		_ = os.Remove(s.file(id))
	}
	return nil
}

// handEdit stores the text the daemon last wrote as External when the file
// (pre) differs from it. Called with s.mu held; reports whether it did.
func (s *Store) handEdit(ix *index, pre []byte, drop *[]int) bool {
	known, err := os.ReadFile(s.known())
	if err != nil || bytes.Equal(known, pre) {
		return false
	}
	_, d, err := s.add(ix, known, "", External)
	if err != nil {
		s.fail("store", err)
		return true
	}
	*drop = append(*drop, d...)
	return true
}

// Start compares the file at path with the text the daemon last wrote; a
// hand edit stores the replaced text as External. Then the file is the
// known text. A missing file records nothing.
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
	var drop []int
	if s.handEdit(&ix, raw, &drop) {
		if err := s.commit(ix, drop); err != nil {
			s.fail("index", err)
		}
	}
	s.remember(raw)
}

// Before stores the text at path before the daemon writes it and returns
// the id of the version holding that text (a new one, or the newest when
// it already holds it; 0: quiet, or it could not be stored). A hand edit since the last
// daemon write is stored first (see the package comment). quiet stores the
// text only after a hand edit (the scheduler's preset switch: it recurs
// daily and would fill the ring, but a hand edit must stay undoable).
func (s *Store) Before(path, by, reason string, quiet bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	pre, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.fail("read config", err)
		}
		return 0
	}
	ix, err := s.load()
	if err != nil {
		s.fail("read", err)
		return 0
	}
	var drop []int
	if s.handEdit(&ix, pre, &drop) {
		quiet = false
	}
	id := 0
	if !quiet {
		var d []int
		if id, d, err = s.add(&ix, pre, by, reason); err != nil {
			s.fail("store", err)
			id = 0
		}
		drop = append(drop, d...)
	}
	if err := s.commit(ix, drop); err != nil {
		s.fail("index", err)
		return 0
	}
	return id
}

// Wrote records raw as the text the daemon wrote (every write, the
// scheduler's included, so it is not taken for a hand edit later).
func (s *Store) Wrote(raw []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remember(raw)
}

func (s *Store) remember(raw []byte) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		s.fail("current", err)
		return
	}
	if err := fsutil.WriteAtomic(s.known(), raw, 0o600); err != nil {
		s.fail("current", err)
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
