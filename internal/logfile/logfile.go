// Package logfile is a size-rotating log file with the read-side helpers
// the web UI and CLI need (tail, export, clear). It is the file half of the
// daemon's logging; stdout (journald) stays the primary sink and is never
// touched here (DESIGN.md "v0.2 contract", Log store).
//
// Rotation: when a write would push the current file past the size limit,
// <path>.N-1 .. <path>.1 shift to <path>.N .. <path>.2, the current file
// becomes <path>.1 and a fresh file is opened. The size is taken from the
// open file descriptor, so a truncate from outside (Clear from another
// process, logrotate copytruncate) is picked up instead of triggering a
// spurious rotation.
package logfile

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Permissions of the log directory and file. The daemon runs with
// UMask=0077 anyway; these are the values a root shell gets.
const (
	DirMode  = 0o750
	FileMode = 0o640
)

// TailLimit is how much of the file Lines reads from the end.
const TailLimit = 1 << 20

// journald priority prefixes (sd-daemon(3), SyslogLevelPrefix): a log line
// that starts with one is filed at that priority by journald, so
// `journalctl -p warning -u n5-fangov` finds alerts and warnings. The
// prefix is stripped from the file copy (Timestamped).
const (
	PrefixErr     = "<3>"
	PrefixWarning = "<4>"
)

// StripPrefix removes a leading journald priority prefix ("<N>").
func StripPrefix(p []byte) []byte {
	if len(p) >= 3 && p[0] == '<' && p[1] >= '0' && p[1] <= '7' && p[2] == '>' {
		return p[3:]
	}
	return p
}

// Writer is a rotating log file. Safe for concurrent use. Writes, rotation
// and Clear hold the lock; the read side (Lines, Export) holds it only to
// open the current file and take its size, then reads from that descriptor
// without the lock (H1): a slow HTTP client draining an export must never
// block the controller's log calls, which run inside the regulation cycle
// and would otherwise stall it into the systemd watchdog. A rotation that
// happens meanwhile renames the file; the open descriptor stays valid and
// the reader sees the file as it was.
type Writer struct {
	mu       sync.Mutex
	path     string
	maxSize  int64
	maxFiles int
	f        *os.File // nil after a failed reopen (rotation) until the next Write reopens it
	closed   bool     // Close was called: writes fail with "closed", no reopen
	// reopenErr is the last failed reopen, logged once to stderr; cleared
	// when a reopen succeeds.
	reopenErr string
}

// New opens (or creates) path for appending. maxSizeMB is the rotation
// threshold, maxFiles how many rotated files to keep (.1 .. .N); values
// below 1 are raised to 1. The directory is created with DirMode.
//
// An existing path must be a regular file (H2): the daemon runs as root
// and the path comes from the config, so a symlink, a device node or a
// directory there is refused instead of being appended to. The path form
// itself (under /var/log, no "..") is the config parser's job.
func New(path string, maxSizeMB, maxFiles int) (*Writer, error) {
	if path == "" {
		return nil, errors.New("logfile: empty path")
	}
	if maxSizeMB < 1 {
		maxSizeMB = 1
	}
	if maxFiles < 1 {
		maxFiles = 1
	}
	if err := checkTarget(path); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), DirMode); err != nil {
		return nil, fmt.Errorf("logfile: %w", err)
	}
	w := &Writer{path: path, maxSize: int64(maxSizeMB) << 20, maxFiles: maxFiles}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

// checkTarget refuses an existing path that is not a regular file. Lstat,
// not Stat: a symlink is refused even when it points at a regular file.
func checkTarget(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("logfile: %w", err)
	}
	switch m := st.Mode(); {
	case m&os.ModeSymlink != 0:
		return fmt.Errorf("logfile: %s is a symlink, refusing to follow it", path)
	case m.IsDir():
		return fmt.Errorf("logfile: %s is a directory", path)
	case !m.IsRegular():
		return fmt.Errorf("logfile: %s is not a regular file (%s)", path, m.Type())
	}
	return nil
}

func (w *Writer) open() error {
	if err := checkTarget(w.path); err != nil {
		return err
	}
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|openNoFollow, FileMode)
	if err != nil {
		return fmt.Errorf("logfile: %w", err)
	}
	w.f = f
	return nil
}

// Path returns the current file's path.
func (w *Writer) Path() string { return w.path }

// Write appends p, rotating first when the file would exceed the limit.
// A write larger than the limit goes into a fresh file on its own. When
// the file could not be reopened after a rotation (the log directory was
// removed meanwhile), every Write tries again — directory included — so
// the file log comes back by itself; the failure is logged to stderr once.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureOpenLocked(); err != nil {
		return 0, err
	}
	size := w.sizeLocked()
	if size > 0 && size+int64(len(p)) > w.maxSize {
		if err := w.rotateLocked(); err != nil {
			// Keep logging into the old file rather than losing lines; when
			// the reopen itself failed, try once more with the directory.
			fmt.Fprintf(os.Stderr, "logfile: rotate %s: %v\n", w.path, err)
			if w.f == nil {
				if err := w.ensureOpenLocked(); err != nil {
					return 0, err
				}
			}
		}
	}
	return w.f.Write(p)
}

// ensureOpenLocked reopens the file after a failed rotation reopen. A
// closed Writer stays closed.
func (w *Writer) ensureOpenLocked() error {
	if w.closed {
		return errors.New("logfile: closed")
	}
	if w.f != nil {
		return nil
	}
	err := os.MkdirAll(filepath.Dir(w.path), DirMode)
	if err == nil {
		err = w.open()
	}
	if err != nil {
		if msg := err.Error(); msg != w.reopenErr {
			w.reopenErr = msg
			fmt.Fprintf(os.Stderr, "logfile: reopen %s: %v (file log paused, retried on every write)\n", w.path, err)
		}
		return fmt.Errorf("logfile: reopen: %w", err)
	}
	if w.reopenErr != "" {
		fmt.Fprintf(os.Stderr, "logfile: %s reopened, file log resumed\n", w.path)
		w.reopenErr = ""
	}
	return nil
}

// sizeLocked is the current size from the descriptor (0 on error).
func (w *Writer) sizeLocked() int64 {
	st, err := w.f.Stat()
	if err != nil {
		return 0
	}
	return st.Size()
}

// rotateLocked shifts the rotated files, renames the current one to .1
// and opens a new file.
func (w *Writer) rotateLocked() error {
	_ = w.f.Close()
	w.f = nil
	for i := w.maxFiles; i >= 1; i-- {
		src := w.path + "." + strconv.Itoa(i)
		if i == w.maxFiles {
			_ = os.Remove(src)
			continue
		}
		dst := w.path + "." + strconv.Itoa(i+1)
		if err := os.Rename(src, dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = w.open()
			return err
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = w.open()
		return err
	}
	return w.open()
}

// Close closes the file; further writes fail.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// openCurrent opens the current file for reading and returns it with the
// size at that moment. The lock is held only for the open + stat: it keeps
// the open out of the rotation window (between the rename and the reopen
// the path does not exist), nothing more. A missing file yields (nil, 0, nil).
func (w *Writer) openCurrent() (*os.File, int64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return openSized(w.path)
}

// openSized opens path for reading with its current size; a missing file
// is (nil, 0, nil).
func openSized(path string) (*os.File, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, st.Size(), nil
}

// Lines returns the newest n lines of the current file (oldest first). It
// reads at most TailLimit bytes from the end; a line cut at that boundary
// is dropped. A missing file yields no lines and no error. The read runs
// without the writer lock (see Writer).
func (w *Writer) Lines(n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	f, size, err := w.openCurrent()
	if err != nil || f == nil {
		return nil, err
	}
	defer f.Close()
	return tailFrom(f, size, n)
}

// tail is Lines on a path without a Writer (ReadLines).
func tail(path string, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	f, size, err := openSized(path)
	if err != nil || f == nil {
		return nil, err
	}
	defer f.Close()
	return tailFrom(f, size, n)
}

// tailFrom reads the newest n lines from the last TailLimit bytes of f,
// whose size is size.
func tailFrom(f *os.File, size int64, n int) ([]string, error) {
	start := int64(0)
	cut := false
	if size > TailLimit {
		start = size - TailLimit
		cut = true
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if cut {
		// drop the partial first line
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		} else {
			buf = nil
		}
	}
	buf = bytes.TrimRight(buf, "\n")
	if len(buf) == 0 {
		return nil, nil
	}
	parts := bytes.Split(buf, []byte("\n"))
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = string(bytes.TrimRight(p, "\r"))
	}
	return out, nil
}

// Export copies the current file as it was when Export was called to dst
// (bytes appended meanwhile are not included). Rotated files are not
// included. A missing file exports nothing. The copy runs without the
// writer lock: dst may be a slow network client (see Writer).
func (w *Writer) Export(dst io.Writer) error {
	f, size, err := w.openCurrent()
	if err != nil || f == nil {
		return err
	}
	defer f.Close()
	return exportFrom(f, size, dst)
}

// export is Export on a path without a Writer (ExportFile).
func export(path string, dst io.Writer) error {
	f, size, err := openSized(path)
	if err != nil || f == nil {
		return err
	}
	defer f.Close()
	return exportFrom(f, size, dst)
}

// exportFrom copies the first size bytes of f to dst.
func exportFrom(f *os.File, size int64, dst io.Writer) error {
	bw := bufio.NewWriter(dst)
	if _, err := io.Copy(bw, io.LimitReader(f, size)); err != nil {
		return err
	}
	return bw.Flush()
}

// Clear truncates the current file. Rotated files stay.
func (w *Writer) Clear() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.ensureOpenLocked(); err != nil {
		return err
	}
	return w.f.Truncate(0)
}

// ---- helpers for other processes (CLI without the daemon's Writer) ------

// ReadLines returns the newest n lines of the file at path (see Lines).
func ReadLines(path string, n int) ([]string, error) { return tail(path, n) }

// ExportFile copies the file at path to dst (see Export).
func ExportFile(path string, dst io.Writer) error { return export(path, dst) }

// Truncate empties the file at path in place. Safe against a daemon that
// has it open with O_APPEND: its next write lands at the new end.
func Truncate(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(0)
}

// Timestamped wraps w so that every Write is prefixed with the local time
// and stripped of a journald priority prefix (PrefixErr, PrefixWarning).
// The standard logger writes one line per call with flags 0 (journald
// stamps stdout itself), so the file gets its own stamps here.
func Timestamped(w io.Writer) io.Writer { return stampWriter{w: w} }

type stampWriter struct{ w io.Writer }

func (s stampWriter) Write(p []byte) (int, error) {
	stamp := time.Now().Format("2006-01-02 15:04:05 ")
	body := StripPrefix(p)
	buf := make([]byte, 0, len(stamp)+len(body))
	buf = append(buf, stamp...)
	buf = append(buf, body...)
	_, err := s.w.Write(buf)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}
