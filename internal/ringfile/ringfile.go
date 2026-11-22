// Package ringfile is the file side of a small record ring that several
// processes share: the daemon and CLI runs without the daemon socket write
// the same JSON file (alerts.json, events.json).
//
// The protocol is the one of the #54 fix: every write takes the flock on
// <path>.lock, reads the file, merges it into memory and then writes (temp
// file + rename, 0600), so no record is lost and an ID is handed out once;
// a read notices a changed file by its mtime and size and merges it first.
// File holds the mechanism; the owner of the ring keeps its records, its
// mutex and its merge rule.
package ringfile

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/SirRenix/n5-fangov/internal/fsutil"
)

// Logger is satisfied by *log.Logger.
type Logger interface {
	Printf(format string, args ...any)
}

type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

// LockWait bounds the wait for the file lock. A holder that hangs longer
// only costs the serialization: the write goes ahead, logged once.
const LockWait = 2 * time.Second

// File is the shared JSON file of a ring of T. It is not safe for
// concurrent use: the owner calls it under its own mutex. A File with an
// empty path is memory only: every method is a no-op.
type File[T any] struct {
	path   string
	label  string // log prefix, e.g. "alert history"
	noun   string // what stays in memory when the file cannot be written
	logger Logger

	warnedIO   bool
	warnedLock bool
	stampMod   time.Time // mtime and size of the file as last read or written
	stampSize  int64
}

// New returns the file at path. label prefixes the log lines ("alert
// history"), noun names what is kept in memory after a failed write
// ("history"). logger nil = silent.
func New[T any](path, label, noun string, logger Logger) *File[T] {
	if logger == nil {
		logger = nopLogger{}
	}
	return &File[T]{path: path, label: label, noun: noun, logger: logger}
}

// Path is the file path ("" = memory only).
func (f *File[T]) Path() string { return f.path }

// Lock takes the file lock (<path>.lock); the returned func releases it.
// A memory-only file and a lock that cannot be had (logged once) return a
// no-op.
func (f *File[T]) Lock() func() {
	if f.path == "" {
		return func() {}
	}
	unlock, err := lockFile(f.path+".lock", LockWait)
	if err != nil {
		if !f.warnedLock {
			f.warnedLock = true
			f.logger.Printf("%s %s: lock: %v (writing without it)", f.label, f.path, err)
		}
		return func() {}
	}
	f.warnedLock = false
	return unlock
}

// Read reads the file and remembers its mtime and size. ok is false for a
// missing or unreadable file (nothing logged) and for one that does not
// parse (logged, ignored).
func (f *File[T]) Read() (recs []T, ok bool) {
	recs, found, err := f.ReadErr()
	return recs, found && err == nil
}

// ReadErr is Read for a caller that must tell the cases apart: found is
// false for a missing file (err nil); err is an open or read failure
// (permission denied: a CLI run without root), or the parse error, which
// is logged as by Read.
func (f *File[T]) ReadErr() (recs []T, found bool, err error) {
	if f.path == "" {
		return nil, false, nil
	}
	// stat and read on one handle: a rename by another ring in between
	// must not pair the new stamp with the old content
	fh, err := os.Open(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	st, err := fh.Stat()
	if err != nil {
		_ = fh.Close()
		return nil, false, err
	}
	b, err := io.ReadAll(fh)
	_ = fh.Close()
	if err != nil {
		return nil, false, err
	}
	f.stampMod, f.stampSize = st.ModTime(), st.Size()
	if err := json.Unmarshal(b, &recs); err != nil {
		f.logger.Printf("%s %s: %v (ignored)", f.label, f.path, err)
		return nil, true, err
	}
	return recs, true, nil
}

// Changed reports whether another ring changed the file since this one
// last read or wrote it (one stat; the file is replaced atomically, so a
// read without the lock sees a whole file). A missing file is no change.
func (f *File[T]) Changed() bool {
	if f.path == "" {
		return false
	}
	st, err := os.Stat(f.path)
	return err == nil && (!st.ModTime().Equal(f.stampMod) || st.Size() != f.stampSize)
}

// Write replaces the file with recs (0600, temp file + rename). A failure
// is logged once until a write works again; the caller keeps its records
// in memory.
func (f *File[T]) Write(recs []T) {
	if f.path == "" {
		return
	}
	b, err := json.Marshal(recs)
	if err != nil {
		return
	}
	if err := fsutil.WriteAtomic(f.path, append(b, '\n'), 0o600); err != nil {
		if !f.warnedIO {
			f.warnedIO = true
			f.logger.Printf("%s %s: %v (%s kept in memory only)", f.label, f.path, err, f.noun)
		}
		return
	}
	f.warnedIO = false
	if st, err := os.Stat(f.path); err == nil {
		f.stampMod, f.stampSize = st.ModTime(), st.Size()
	}
}
