// Package events is the event log behind the chart markers (DESIGN
// "Events"): what the daemon and the operator did, with a time — preset
// switches, overrides, the assist switch, alerts, critical and ceiling
// states, failsafe, starts and reloads.
//
// The log is a ring in <state dir>/events.json (0600), 7 days and at most
// 2000 entries, shared with CLI runs through internal/ringfile (flock,
// read, merge, write; reads refresh on a changed mtime or size). A state
// with a duration has a start event and its own "-end" event; the
// dashboard pairs them into a span. Texts name the kind, the channel,
// values and a user or token name only: no paths, no alert messages, no
// secrets.
package events

import (
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/SirRenix/n5-fangov/internal/ringfile"
)

// Retention and MaxEntries bound the log: events older than 7 days (the
// span of the chart history) are dropped, and of the rest the newest 2000
// stay.
const (
	Retention  = 7 * 24 * time.Hour
	MaxEntries = 2000
)

// The event kinds. A span kind K ends with K + "-end".
const (
	Preset      = "preset"   // a preset was applied (dashboard, API, schedule)
	Override    = "override" // manual duty on a channel (span)
	OverrideEnd = "override-end"
	Assist      = "assist" // the assist switch of a channel
	Alert       = "alert"  // an alert went out, or was suppressed by the cooldown
	Critical    = "critical"
	CriticalEnd = "critical-end"
	Ceiling     = "ceiling"
	CeilingEnd  = "ceiling-end"
	Failsafe    = "failsafe"
	FailsafeEnd = "failsafe-end"
	Start       = "start"  // the daemon started
	Reload      = "reload" // a config was applied (editor, import)
)

// Kinds lists every kind in display order.
var Kinds = []string{Preset, Override, OverrideEnd, Assist, Alert, Critical, CriticalEnd, Ceiling, CeilingEnd, Failsafe, FailsafeEnd, Start, Reload}

// spans are the kinds with a duration.
var spans = []string{Override, Critical, Ceiling, Failsafe}

// Known reports whether kind is one of Kinds.
func Known(kind string) bool { return slices.Contains(Kinds, kind) }

// SpanStart returns the start kind of an end kind ("override" for
// "override-end") and true, or kind and false for any other kind.
func SpanStart(kind string) (string, bool) {
	k, ok := strings.CutSuffix(kind, "-end")
	if ok && slices.Contains(spans, k) {
		return k, true
	}
	return kind, false
}

// IsSpan reports whether kind starts a span.
func IsSpan(kind string) bool { return slices.Contains(spans, kind) }

// The by values: who caused an event.
const (
	ByDaemon   = "daemon"
	BySchedule = "schedule"
	ByCLI      = "cli"
)

// Event is one entry. Ch is the channel for per-channel kinds; By names
// the cause (user:<name>, token:<name>, schedule, cli, daemon); End is
// true on the event that ends a span (kind "<span>-end").
type Event struct {
	ID   uint64 `json:"id"`
	TS   int64  `json:"ts"`
	Kind string `json:"kind"`
	Ch   string `json:"ch,omitempty"`
	Text string `json:"text"`
	By   string `json:"by,omitempty"`
	End  bool   `json:"end,omitempty"`
}

// Recorder takes new events. *Log and *Queue implement it; the controller,
// the scheduler and the web layer hold one.
type Recorder interface {
	Add(e Event)
}

// Logger is satisfied by *log.Logger.
type Logger = ringfile.Logger

// Log is the event ring. Safe for concurrent use.
type Log struct {
	file *ringfile.File[Event]
	now  func() time.Time

	mu     sync.Mutex
	recs   []Event // ascending ID
	lastID uint64
}

// New opens the log at path ("" = memory only) and loads the file; a
// missing file starts empty. Nothing is written until the first Add, so a
// read-only caller (the CLI without the daemon) leaves the file alone.
// logger nil = silent.
func New(path string, logger Logger) *Log {
	l := &Log{file: ringfile.New[Event](path, "event log", "events", logger), now: time.Now}
	l.mu.Lock()
	l.mergeFileLocked()
	l.mu.Unlock()
	return l
}

// ReadFile opens the log at path for reading only (the CLI without the
// daemon): like New, but a file that exists and cannot be read or parsed
// is an error (permission denied without root) instead of an empty log. A
// missing file is an empty log.
func ReadFile(path string) (*Log, error) {
	l := &Log{file: ringfile.New[Event](path, "event log", "events", nil), now: time.Now}
	recs, _, err := l.file.ReadErr()
	if err != nil {
		return nil, err
	}
	l.recs, l.lastID = merge(nil, recs, 0)
	return l, nil
}

// Add stores e: a zero TS is now, End follows the kind, the ID is the next
// one after every ID in memory and in the file. The write goes through the
// file lock (read, merge, write); a failed write is logged once and the
// event stays in memory.
func (l *Log) Add(e Event) {
	l.store(e)
}

func (l *Log) store(e Event) Event {
	if e.TS == 0 {
		e.TS = l.now().Unix()
	}
	_, e.End = SpanStart(e.Kind)
	l.mu.Lock()
	defer l.mu.Unlock()
	unlock := l.file.Lock()
	defer unlock()
	l.mergeFileLocked()
	// clock guard: a new event more than Retention after the newest one, or
	// more than a day before it, means a long stop or a clock step; the age
	// trim waits for the next write, so a step that is corrected before it
	// costs nothing (the cap still holds)
	var newest int64
	for _, r := range l.recs {
		newest = max(newest, r.TS)
	}
	byAge := len(l.recs) == 0 || (e.TS-newest <= int64(Retention.Seconds()) && newest-e.TS <= int64(clockSlack.Seconds()))
	l.lastID++
	e.ID = l.lastID
	l.recs = append(l.recs, e)
	if byAge {
		l.trimAgeLocked(e.TS)
	}
	l.trimLocked()
	l.file.Write(l.recs)
	return e
}

// clockSlack is how far the newest stored event may lie after a new one
// before the age trim of that write is skipped (clock stepped back).
const clockSlack = 24 * time.Hour

// Since returns the events with ts > since and not older than span before
// now (span <= 0: the whole retention), oldest first (by ts, then ID).
// since compares timestamps: after a clock step back, events written
// meanwhile can lie at or before a since an incremental reader already
// holds; such a reader reloads the whole span now and then (the
// dashboard does).
func (l *Log) Since(span time.Duration, since int64) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refreshLocked()
	if span <= 0 || span > Retention {
		span = Retention
	}
	cut := l.now().Add(-span).Unix()
	out := make([]Event, 0)
	for _, e := range l.recs {
		if e.TS > since && e.TS >= cut {
			out = append(out, e)
		}
	}
	sortEvents(out)
	return out
}

// Recent returns the newest n events, oldest first (n <= 0: all).
func (l *Log) Recent(n int) []Event {
	all := l.Since(0, 0)
	if n > 0 && len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

// Open returns the spans that started and have not ended, oldest first:
// per kind and channel the last start with no end after it.
func (l *Log) Open() []Event {
	return OpenSpans(l.Since(0, 0))
}

// EndOpen ends every open span that keep does not keep (nil = none kept)
// with an end event carrying text and by, and returns how many it ended.
// serve calls it at start: the spans of the previous process ended with
// it, except an override the daemon restored from its run file.
func (l *Log) EndOpen(keep func(Event) bool, text, by string) int {
	n := 0
	for _, e := range l.Open() {
		if keep != nil && keep(e) {
			continue
		}
		l.Add(Event{Kind: e.Kind + "-end", Ch: e.Ch, Text: text, By: by})
		n++
	}
	return n
}

// Who is the short form of a by value for a text: the user name of
// "user:<name>", "token <name>", else by itself.
func Who(by string) string {
	if u, ok := strings.CutPrefix(by, "user:"); ok {
		return u
	}
	if t, ok := strings.CutPrefix(by, "token:"); ok {
		return "token " + t
	}
	return by
}

// OpenSpans returns the spans of evs (oldest first) that have no end
// event after their last start, keyed by kind and channel.
func OpenSpans(evs []Event) []Event {
	type key struct{ kind, ch string }
	open := map[key]Event{}
	var order []key
	for _, e := range evs {
		if k, end := SpanStart(e.Kind); end {
			delete(open, key{k, e.Ch})
			continue
		}
		if IsSpan(e.Kind) {
			k := key{e.Kind, e.Ch}
			if _, ok := open[k]; !ok {
				order = append(order, k)
			}
			open[k] = e
		}
	}
	var out []Event
	for _, k := range order {
		if e, ok := open[k]; ok {
			out = append(out, e)
			delete(open, k) // a key that was closed and reopened appears once
		}
	}
	sortEvents(out)
	return out
}

func sortEvents(evs []Event) {
	sort.SliceStable(evs, func(a, b int) bool {
		if evs[a].TS != evs[b].TS {
			return evs[a].TS < evs[b].TS
		}
		return evs[a].ID < evs[b].ID
	})
}

// trimAgeLocked drops events more than Retention older than ref (the time
// of the event being written; store decides whether the clock is trusted).
// Reads filter by age themselves, so a merge never drops by age. Caller
// holds l.mu.
func (l *Log) trimAgeLocked(ref int64) {
	cut := ref - int64(Retention.Seconds())
	keep := l.recs[:0]
	for _, e := range l.recs {
		if e.TS >= cut {
			keep = append(keep, e)
		}
	}
	l.recs = keep
}

// trimLocked keeps the newest MaxEntries. Caller holds l.mu.
func (l *Log) trimLocked() {
	if len(l.recs) > MaxEntries {
		l.recs = append(l.recs[:0], l.recs[len(l.recs)-MaxEntries:]...)
	}
}

func (l *Log) refreshLocked() {
	if l.file.Changed() {
		l.mergeFileLocked()
	}
}

// mergeFileLocked reads the file and merges it by ID: an ID memory lacks
// is taken over; the same ID with the same ts, kind, channel and text is
// one event; the same ID with other content keeps the memory event and
// the file's gets a new ID after the highest (it would otherwise be lost),
// unless it is already held under another ID. Caller holds l.mu.
func (l *Log) mergeFileLocked() {
	file, ok := l.file.Read()
	if !ok {
		return
	}
	l.recs, l.lastID = merge(l.recs, file, l.lastID)
	l.trimLocked()
}

func merge(mem, file []Event, last uint64) ([]Event, uint64) {
	type key struct {
		ts             int64
		kind, ch, text string
	}
	out := append([]Event(nil), mem...)
	idx := make(map[uint64]int, len(out)+len(file))
	byKey := make(map[key]bool, len(out))
	for i, e := range out {
		idx[e.ID] = i
		byKey[key{e.TS, e.Kind, e.Ch, e.Text}] = true
		last = max(last, e.ID)
	}
	var renumber []Event
	for _, f := range file {
		last = max(last, f.ID)
		k := key{f.TS, f.Kind, f.Ch, f.Text}
		i, ok := idx[f.ID]
		switch {
		case f.ID != 0 && !ok:
			idx[f.ID] = len(out)
			out = append(out, f)
			byKey[k] = true
		case ok && out[i].TS == f.TS && out[i].Kind == f.Kind && out[i].Ch == f.Ch && out[i].Text == f.Text:
		case byKey[k]:
		default:
			renumber = append(renumber, f)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	for _, f := range renumber {
		k := key{f.TS, f.Kind, f.Ch, f.Text}
		if byKey[k] {
			continue
		}
		byKey[k] = true
		last++
		f.ID = last
		out = append(out, f)
	}
	return out, last
}
