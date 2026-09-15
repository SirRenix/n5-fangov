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

// Writer is a rotating log file. Safe for concurrent use; every method
// takes the same lock, so a tail never sees a half-rotated file.
type Writer struct {
	mu       sync.Mutex
	path     string
	maxSize  int64
	maxFiles int
	f        *os.File
}

// New opens (or creates) path for appending. maxSizeMB is the rotation
// threshold, maxFiles how many rotated files to keep (.1 .. .N); values
// below 1 are raised to 1. The directory is created with DirMode.
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
	if err := os.MkdirAll(filepath.Dir(path), DirMode); err != nil {
		return nil, fmt.Errorf("logfile: %w", err)
	}
	w := &Writer{path: path, maxSize: int64(maxSizeMB) << 20, maxFiles: maxFiles}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, FileMode)
	if err != nil {
		return fmt.Errorf("logfile: %w", err)
	}
	w.f = f
	return nil
}

// Path returns the current file's path.
func (w *Writer) Path() string { return w.path }

// Write appends p, rotating first when the file would exceed the limit.
// A write larger than the limit goes into a fresh file on its own.
func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, errors.New("logfile: closed")
	}
	size := w.sizeLocked()
	if size > 0 && size+int64(len(p)) > w.maxSize {
		if err := w.rotateLocked(); err != nil {
			// Keep logging into the old file rather than losing lines.
			fmt.Fprintf(os.Stderr, "logfile: rotate %s: %v\n", w.path, err)
		}
	}
	return w.f.Write(p)
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
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// Lines returns the newest n lines of the current file (oldest first). It
// reads at most TailLimit bytes from the end; a line cut at that boundary
// is dropped. A missing file yields no lines and no error.
func (w *Writer) Lines(n int) ([]string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return tail(w.path, n)
}

// tail is Lines without the lock (shared with ReadLines).
func tail(path string, n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()
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

// Export copies the whole current file to dst. Rotated files are not
// included. A missing file exports nothing.
func (w *Writer) Export(dst io.Writer) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return export(w.path, dst)
}

func export(path string, dst io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	bw := bufio.NewWriter(dst)
	if _, err := io.Copy(bw, f); err != nil {
		return err
	}
	return bw.Flush()
}

// Clear truncates the current file. Rotated files stay.
func (w *Writer) Clear() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return errors.New("logfile: closed")
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

// Timestamped wraps w so that every Write is prefixed with the local time.
// The standard logger writes one line per call with flags 0 (journald
// stamps stdout itself), so the file gets its own stamps here.
func Timestamped(w io.Writer) io.Writer { return stampWriter{w: w} }

type stampWriter struct{ w io.Writer }

func (s stampWriter) Write(p []byte) (int, error) {
	stamp := time.Now().Format("2006-01-02 15:04:05 ")
	buf := make([]byte, 0, len(stamp)+len(p))
	buf = append(buf, stamp...)
	buf = append(buf, p...)
	n, err := s.w.Write(buf)
	if n >= len(stamp) {
		n -= len(stamp)
	}
	if n > len(p) {
		n = len(p)
	}
	return n, err
}
