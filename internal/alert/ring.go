package alert

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RingSize is how many delivered alerts the Ring keeps (the dashboard's
// "recent alerts" list).
const RingSize = 50

// Record is one alert that passed through the Ring.
type Record struct {
	TS   int64  `json:"ts"`
	Kind string `json:"kind"`
	Msg  string `json:"msg"`
}

// Ring wraps a Sink and records every alert that passes through it —
// the controller's (after its cooldown) and serve's start-up alerts — in
// a fixed-size ring, newest first on read. With a path the ring is
// mirrored to that JSON file (0600, temp file + rename) on every record
// and loaded from it at construction, so a restart keeps the history. A
// file that cannot be written is logged once; the ring continues in
// memory.
type Ring struct {
	inner  Sink
	path   string
	logger Logger
	now    func() time.Time

	mu       sync.Mutex
	recs     []Record // oldest first
	warnedIO bool
}

// NewRing wraps inner. path "" = memory only. logger nil = silent.
func NewRing(inner Sink, path string, logger Logger) *Ring {
	if logger == nil {
		logger = nopLogger{}
	}
	r := &Ring{inner: inner, path: path, logger: logger, now: time.Now}
	r.load()
	return r
}

// Name is the wrapped sink's name.
func (r *Ring) Name() string {
	if r.inner == nil {
		return "none"
	}
	return r.inner.Name()
}

// Alert records the alert and delivers it through the wrapped sink.
func (r *Ring) Alert(kind, msg string) {
	r.record(kind, msg)
	if r.inner != nil {
		r.inner.Alert(kind, msg)
	}
}

// Send records the alert and delivers it, returning the wrapped sink's
// delivery error (nil for a plain Sink).
func (r *Ring) Send(kind, msg string) error { return r.SendCtx(context.Background(), kind, msg) }

// SendCtx is Send bounded by ctx where the wrapped sink supports it (R-L9).
func (r *Ring) SendCtx(ctx context.Context, kind, msg string) error {
	r.record(kind, msg)
	if r.inner == nil {
		return errors.New("no alert sink configured")
	}
	return sendCtx(ctx, r.inner, kind, msg)
}

// Recent returns the newest n records, newest first (n <= 0: all).
func (r *Ring) Recent(n int) []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 || n > len(r.recs) {
		n = len(r.recs)
	}
	out := make([]Record, 0, n)
	for i := len(r.recs) - 1; i >= len(r.recs)-n; i-- {
		out = append(out, r.recs[i])
	}
	return out
}

// Last returns the newest record's timestamp per kind.
func (r *Ring) Last() map[string]int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int64{}
	for _, rec := range r.recs {
		if rec.TS > out[rec.Kind] {
			out[rec.Kind] = rec.TS
		}
	}
	return out
}

func (r *Ring) record(kind, msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recs = append(r.recs, Record{TS: r.now().Unix(), Kind: kind, Msg: msg})
	if len(r.recs) > RingSize {
		r.recs = append(r.recs[:0], r.recs[len(r.recs)-RingSize:]...)
	}
	r.saveLocked()
}

// saveLocked mirrors the ring to the JSON file. Caller holds r.mu.
func (r *Ring) saveLocked() {
	if r.path == "" {
		return
	}
	b, err := json.Marshal(r.recs)
	if err != nil {
		return
	}
	if err := writePrivate(r.path, append(b, '\n')); err != nil {
		if !r.warnedIO {
			r.warnedIO = true
			r.logger.Printf("alert history %s: %v (history kept in memory only)", r.path, err)
		}
		return
	}
	r.warnedIO = false
}

// load reads the mirror file; a missing or unreadable file starts empty.
func (r *Ring) load() {
	if r.path == "" {
		return
	}
	b, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	var recs []Record
	if err := json.Unmarshal(b, &recs); err != nil {
		r.logger.Printf("alert history %s: %v (ignored)", r.path, err)
		return
	}
	if len(recs) > RingSize {
		recs = recs[len(recs)-RingSize:]
	}
	r.mu.Lock()
	r.recs = recs
	r.mu.Unlock()
}

// writePrivate writes data to path with mode 0600 via temp file + rename.
func writePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(name, path); err != nil {
		cleanup()
		return err
	}
	return nil
}
