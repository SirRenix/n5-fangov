package alert

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/SirRenix/n5-fangov/internal/ringfile"
)

// RingSize is how many delivered alerts the Ring keeps (the dashboard's
// "recent alerts" list).
const RingSize = 50

// Record is one alert that passed through the Ring. Error carries the
// delivery failure when the wrapped sink reported one, so the history does
// not suggest a delivery that never happened. ID numbers the records of
// one ring in arrival order (kept across restarts); AckTS and AckBy are
// set when the operator acknowledged the record (display only: delivery,
// cooldown and failsafe do not look at them).
type Record struct {
	ID    uint64 `json:"id"`
	TS    int64  `json:"ts"`
	Kind  string `json:"kind"`
	Msg   string `json:"msg"`
	Error string `json:"error,omitempty"`
	AckTS int64  `json:"ack_ts,omitempty"`
	AckBy string `json:"ack_by,omitempty"`
}

// Ring wraps a Sink and records every alert that passes through it —
// the controller's (after its cooldown) and serve's start-up alerts — in
// a fixed-size ring, newest first on read. With a path the ring is
// mirrored to that JSON file (0600, temp file + rename) on every record
// and loaded from it at construction, so a restart keeps the history. A
// file that cannot be written is logged once; the ring continues in
// memory.
//
// Several rings may share the file: the daemon's and the one of a CLI run
// without the daemon socket (apt hook, onfailure `alert`). Every write
// takes the flock on <path>.lock, reads the file, merges it into memory
// and then writes (so no record and no acknowledgement is lost and an ID
// is handed out once, #54); a read notices a changed file by its mtime and
// size and merges it first. The file mechanism is internal/ringfile, shared
// with the event log.
type Ring struct {
	inner  Sink
	file   *ringfile.File[Record]
	logger Logger
	now    func() time.Time
	// onRecord sees every new record after it is stored (SetOnRecord);
	// nil = none.
	onRecord func(Record)

	mu     sync.Mutex
	recs   []Record // ascending ID
	lastID uint64   // highest ID handed out or seen
}

// NewRing wraps inner. path "" = memory only. logger nil = silent.
func NewRing(inner Sink, path string, logger Logger) *Ring {
	if logger == nil {
		logger = nopLogger{}
	}
	r := &Ring{inner: inner, file: ringfile.New[Record](path, "alert history", "history", logger), logger: logger, now: time.Now}
	r.load()
	return r
}

// SetOnRecord installs fn, called with every new record after it is stored
// (outside the ring's lock; the event log marks alerts with it). Set it
// before the ring is used.
func (r *Ring) SetOnRecord(fn func(Record)) { r.onRecord = fn }

// Name is the wrapped sink's name.
func (r *Ring) Name() string {
	if r.inner == nil {
		return "none"
	}
	return r.inner.Name()
}

// Alert delivers the alert through the wrapped sink and records it with
// the delivery outcome; a failure is logged here (the sink's Send does not
// log it itself).
func (r *Ring) Alert(kind, msg string) {
	if err := r.SendCtx(context.Background(), kind, msg); err != nil {
		r.logger.Printf("alert: %v", err)
	}
}

// Send delivers the alert and records it, returning the wrapped sink's
// delivery error (nil for a plain Sink).
func (r *Ring) Send(kind, msg string) error { return r.SendCtx(context.Background(), kind, msg) }

// SendCtx is Send bounded by ctx where the wrapped sink supports it.
// The record carries the time the delivery started and, on failure, the
// error text.
func (r *Ring) SendCtx(ctx context.Context, kind, msg string) error {
	ts := r.now().Unix()
	var err error
	if r.inner == nil {
		err = errors.New("no alert sink configured")
	} else {
		err = sendCtx(ctx, r.inner, kind, msg)
	}
	r.record(Record{TS: ts, Kind: kind, Msg: msg, Error: errText(err)})
	return err
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Recent returns the newest n records, newest first (n <= 0: all).
func (r *Ring) Recent(n int) []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshLocked()
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
	r.refreshLocked()
	out := map[string]int64{}
	for _, rec := range r.recs {
		if rec.TS > out[rec.Kind] {
			out[rec.Kind] = rec.TS
		}
	}
	return out
}

func (r *Ring) record(rec Record) {
	rec = r.store(rec)
	if r.onRecord != nil {
		r.onRecord(rec)
	}
}

// store numbers rec and writes it under the file lock (read, merge, write).
func (r *Ring) store(rec Record) Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	unlock := r.file.Lock()
	defer unlock()
	r.mergeFileLocked()
	r.lastID++
	rec.ID = r.lastID
	r.recs = append(r.recs, rec)
	r.trimLocked()
	r.file.Write(r.recs)
	return rec
}

// trimLocked keeps the newest RingSize records. Caller holds r.mu.
func (r *Ring) trimLocked() {
	if len(r.recs) > RingSize {
		r.recs = append(r.recs[:0], r.recs[len(r.recs)-RingSize:]...)
	}
}

// refreshLocked merges the file when another ring changed it since this
// one last read or wrote it (ringfile.File.Changed). Caller holds r.mu.
func (r *Ring) refreshLocked() {
	if r.file.Changed() {
		r.mergeFileLocked()
	}
}

// load reads the mirror file; a missing or unreadable file starts empty.
func (r *Ring) load() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mergeFileLocked()
}

// mergeFileLocked reads the file and merges it into memory (mergeRecords).
// A missing file changes nothing; an unparsable one is logged and ignored.
// Caller holds r.mu.
func (r *Ring) mergeFileLocked() {
	file, ok := r.file.Read()
	if !ok {
		return
	}
	r.recs, r.lastID = mergeRecords(r.recs, file, r.lastID)
	r.trimLocked()
}

// mergeRecords joins the memory records and the file records by ID:
//   - a file record whose ID memory does not hold is taken over;
//   - the same ID with the same ts, kind and message is one record, and an
//     acknowledgement on either side is kept (it only ever gets set);
//   - the same ID with other content (left behind by the #54 overwrite)
//     keeps the memory record of the ring that merges; the file record
//     gets a new ID after the highest, unless a full ring holds only newer
//     records (it is dropped then, see below);
//   - a record without an ID (a file written before IDs existed) is the
//     memory record with the same ts, kind and message when there is one
//     (memory numbered it on load), else it is numbered after the highest,
//     in file order.
//
// The result is in ascending ID; last is the highest ID in it or the one
// handed in, whichever is higher.
func mergeRecords(mem, file []Record, last uint64) ([]Record, uint64) {
	type key struct {
		ts        int64
		kind, msg string
	}
	out := append([]Record(nil), mem...)
	idx := make(map[uint64]int, len(out)+len(file))
	byKey := make(map[key]int, len(out))
	for i, rec := range out {
		idx[rec.ID] = i
		byKey[key{rec.TS, rec.Kind, rec.Msg}] = i
		last = max(last, rec.ID)
	}
	keepAck := func(m *Record, f Record) {
		if m.AckTS == 0 && f.AckTS != 0 {
			m.AckTS, m.AckBy = f.AckTS, f.AckBy
		}
	}
	var renumber []Record
	for _, f := range file {
		if f.ID == 0 {
			if i, ok := byKey[key{f.TS, f.Kind, f.Msg}]; ok {
				keepAck(&out[i], f)
			} else {
				renumber = append(renumber, f)
			}
			continue
		}
		last = max(last, f.ID)
		i, ok := idx[f.ID]
		if !ok {
			idx[f.ID] = len(out)
			out = append(out, f)
			continue
		}
		m := &out[i]
		if m.TS == f.TS && m.Kind == f.Kind && m.Msg == f.Msg {
			keepAck(m, f)
			continue
		}
		// already renumbered by an earlier merge whose write failed
		if j, ok := byKey[key{f.TS, f.Kind, f.Msg}]; ok {
			keepAck(&out[j], f)
			continue
		}
		renumber = append(renumber, f)
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	// a renumbered record older than the oldest one a full ring keeps
	// would be evicted at once if it had kept its place; with a new ID it
	// would come back as the newest instead (on every merge while the file
	// cannot be rewritten), so it is dropped
	var oldest int64
	if len(out) >= RingSize {
		oldest = out[len(out)-RingSize].TS
	}
	for _, f := range renumber {
		if f.TS < oldest {
			continue
		}
		last++
		f.ID = last
		out = append(out, f)
	}
	return out, last
}

// Open counts the records not acknowledged yet.
func (r *Ring) Open() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refreshLocked()
	n := 0
	for _, rec := range r.recs {
		if rec.AckTS == 0 {
			n++
		}
	}
	return n
}

// Ack acknowledges the records with the given IDs on behalf of by.
// Already acknowledged records keep their first acknowledgement. unknown
// lists the IDs the ring does not hold (never seen, or evicted); nothing
// is written then.
func (r *Ring) Ack(ids []uint64, by string) (acked int, unknown []uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	unlock := r.file.Lock()
	defer unlock()
	r.mergeFileLocked()
	idx := make(map[uint64]int, len(r.recs))
	for i, rec := range r.recs {
		idx[rec.ID] = i
	}
	for _, id := range ids {
		if _, ok := idx[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return 0, unknown
	}
	ts := r.now().Unix()
	for _, id := range ids {
		if rec := &r.recs[idx[id]]; rec.AckTS == 0 {
			rec.AckTS, rec.AckBy = ts, by
			acked++
		}
	}
	if acked > 0 {
		r.file.Write(r.recs)
	}
	return acked, nil
}

// AckUpTo acknowledges every open record with an ID up to upTo: "all"
// for the IDs the caller has seen, so an alert that arrives between
// showing the list and the click stays open.
func (r *Ring) AckUpTo(upTo uint64, by string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	unlock := r.file.Lock()
	defer unlock()
	r.mergeFileLocked()
	ts := r.now().Unix()
	acked := 0
	for i := range r.recs {
		if rec := &r.recs[i]; rec.ID <= upTo && rec.AckTS == 0 {
			rec.AckTS, rec.AckBy = ts, by
			acked++
		}
	}
	if acked > 0 {
		r.file.Write(r.recs)
	}
	return acked
}
