// Package history is the tiered history store behind the dashboard charts
// (DESIGN.md "History"): one raw point per regulation cycle for 2 h, 1-min
// means for 24 h, 5-min means for 7 d, persisted as JSON in the state
// directory and exported as CSV. The controller pushes one point per
// cycle; the web layer reads a tier by span.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/SirRenix/n5-fangov/internal/fsutil"
)

// Point is one history entry: the channel series by channel name and the
// watched dashboard sensors by id. control.HistoryPoint is an alias of it.
type Point struct {
	TS   int64              `json:"ts"`
	Temp map[string]float64 `json:"temp"` // degrees C by channel name
	Duty map[string]int     `json:"duty"`
	RPM  map[string]int     `json:"rpm"`
	// Extra holds the watched dashboard sensors ([dashboard].sensors) by
	// id, degrees C; absent ids could not be read that cycle.
	Extra map[string]float64 `json:"extra,omitempty"`
}

// Tier spans and bucket widths.
const (
	RawSpan    = 2 * time.Hour
	Min1Span   = 24 * time.Hour
	Min1Bucket = time.Minute
	Min5Span   = 7 * 24 * time.Hour
	Min5Bucket = 5 * time.Minute

	// saveMode is the file mode of history.json (it names the operator's
	// sensors and hardware).
	saveMode = 0o600
	// format is the file format version.
	format = 1
)

// RawCapacity is the number of raw points kept for interval: RawSpan /
// interval, at least 1.
func RawCapacity(interval time.Duration) int {
	if interval <= 0 {
		return 1
	}
	n := int(RawSpan / interval)
	if n < 1 {
		n = 1
	}
	return n
}

// Store keeps the three tiers. All methods are safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	path     string // "" = memory only
	interval time.Duration
	now      func() time.Time
	logf     func(string, ...any)

	raw    []Point // oldest first
	rawCap int
	min1   tier
	min5   tier

	// saveFailed: the unwritable state dir was logged once; later
	// failures stay silent (the store keeps running in memory).
	saveFailed bool
}

// New creates a store for a regulation interval and loads path (Load).
// An empty path keeps the store in memory. now and logf may be nil.
func New(path string, interval time.Duration, now func() time.Time, logf func(string, ...any)) *Store {
	if now == nil {
		now = time.Now
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &Store{
		path:     path,
		interval: interval,
		now:      now,
		logf:     logf,
		rawCap:   RawCapacity(interval),
		min1:     tier{bucket: int64(Min1Bucket / time.Second), span: Min1Span, capacity: int(Min1Span / Min1Bucket)},
		min5:     tier{bucket: int64(Min5Bucket / time.Second), span: Min5Span, capacity: int(Min5Span / Min5Bucket)},
	}
	s.Load()
	return s
}

// SetInterval follows a reload that changed [daemon].interval: the raw
// capacity is recomputed and the raw tier trimmed.
func (s *Store) SetInterval(interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interval = interval
	s.rawCap = RawCapacity(interval)
	s.trimRawLocked()
}

// RawCap returns the raw tier's capacity (tests).
func (s *Store) RawCap() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rawCap
}

// Push records one point (called by the controller once per cycle). The
// point's maps are copied.
func (s *Store) Push(p Point) {
	p = clonePoint(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw = append(s.raw, p)
	s.trimRawLocked()
	s.min1.push(p)
	s.min5.push(p)
}

func (s *Store) trimRawLocked() {
	if len(s.raw) > s.rawCap {
		drop := len(s.raw) - s.rawCap
		s.raw = append(s.raw[:0], s.raw[drop:]...)
	}
}

// Range returns the points of the last span (tier by span: ≤ RawSpan raw,
// ≤ Min1Span 1-min means, else 5-min means) with ts > since, oldest
// first, as copies. The open bucket of an averaged tier is included with
// its running mean.
func (s *Store) Range(span time.Duration, since int64) []Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := s.now().Add(-span).Unix()
	var src []Point
	switch {
	case span <= RawSpan:
		src = s.raw
	case span <= Min1Span:
		src = s.min1.all()
	default:
		src = s.min5.all()
	}
	out := make([]Point, 0, len(src))
	for _, p := range src {
		if p.TS >= cut && p.TS > since {
			out = append(out, clonePoint(p))
		}
	}
	return out
}

// file is the on-disk layout of history.json.
type file struct {
	Format   int     `json:"format"`
	Interval string  `json:"interval"`
	Raw      []Point `json:"raw"`
	Min1     []Point `json:"min1"`
	Min5     []Point `json:"min5"`
}

// Save writes the store to its file (atomic, 0600). The open buckets are
// written with their running means; Load rebuilds them from the raw
// tier. A memory-only store returns nil. The first write failure is
// logged; the store keeps running in memory.
func (s *Store) Save() error {
	if s.path == "" {
		return nil
	}
	s.mu.Lock()
	f := file{Format: format, Interval: s.interval.String(), Raw: s.raw, Min1: s.min1.all(), Min5: s.min5.all()}
	if f.Raw == nil {
		f.Raw = []Point{}
	}
	data, err := json.Marshal(f)
	s.mu.Unlock()
	if err == nil {
		err = fsutil.WriteAtomic(s.path, data, saveMode)
	}
	if err != nil {
		s.mu.Lock()
		first := !s.saveFailed
		s.saveFailed = true
		s.mu.Unlock()
		if first {
			s.logf("history: cannot write %s: %v (history kept in memory only)", s.path, err)
		}
		return err
	}
	return nil
}

// Load replaces the store's content with the file's. A missing or corrupt
// file leaves the store empty with one log line; points older than their
// tier are dropped; an open bucket is rebuilt from the raw points that
// fall into it.
func (s *Store) Load() {
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.logf("history: no %s yet, starting empty", s.path)
		} else {
			s.logf("history: cannot read %s: %v, starting empty", s.path, err)
		}
		return
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil || f.Format != format {
		if err == nil {
			err = fmt.Errorf("format %d, want %d", f.Format, format)
		}
		s.logf("history: %s unusable (%v), starting empty", s.path, err)
		return
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw = keepSince(f.Raw, now.Add(-RawSpan).Unix())
	s.trimRawLocked()
	s.min1.load(f.Min1, now, s.raw)
	s.min5.load(f.Min5, now, s.raw)
	s.logf("history: loaded %s (%d raw, %d 1-min, %d 5-min points kept)", s.path, len(s.raw), len(s.min1.pts), len(s.min5.pts))
}

// keepSince returns the points with ts >= cut in their order (the file is
// written oldest first; an unordered file is tolerated).
func keepSince(pts []Point, cut int64) []Point {
	out := make([]Point, 0, len(pts))
	for _, p := range pts {
		if p.TS >= cut {
			out = append(out, normalise(p))
		}
	}
	return out
}

// normalise gives a decoded point non-nil channel maps (the encoder
// writes {} for them, a hand-edited file may not).
func normalise(p Point) Point {
	if p.Temp == nil {
		p.Temp = map[string]float64{}
	}
	if p.Duty == nil {
		p.Duty = map[string]int{}
	}
	if p.RPM == nil {
		p.RPM = map[string]int{}
	}
	return p
}

func clonePoint(p Point) Point {
	out := Point{TS: p.TS, Temp: make(map[string]float64, len(p.Temp)), Duty: make(map[string]int, len(p.Duty)), RPM: make(map[string]int, len(p.RPM))}
	for k, v := range p.Temp {
		out.Temp[k] = v
	}
	for k, v := range p.Duty {
		out.Duty[k] = v
	}
	for k, v := range p.RPM {
		out.RPM[k] = v
	}
	if len(p.Extra) > 0 {
		out.Extra = make(map[string]float64, len(p.Extra))
		for k, v := range p.Extra {
			out.Extra[k] = v
		}
	}
	return out
}

// ---- averaged tiers ---------------------------------------------------------

// tier is one averaged tier: closed buckets (oldest first, at most
// capacity) plus the open bucket that collects the current raw points.
type tier struct {
	bucket   int64 // seconds
	span     time.Duration
	capacity int
	pts      []Point
	open     *acc
}

// acc accumulates the raw points of one bucket.
type acc struct {
	start int64
	temp  map[string]sum
	duty  map[string]sum
	rpm   map[string]sum
	extra map[string]sum
}

type sum struct {
	total float64
	n     int
}

func newAcc(start int64) *acc {
	return &acc{start: start, temp: map[string]sum{}, duty: map[string]sum{}, rpm: map[string]sum{}, extra: map[string]sum{}}
}

// add folds a raw point into the bucket. A negative duty is the
// controller's "unknown after a failed write" marker (-1), not a value:
// it is skipped, so a mean is formed from the duties that were written
// (a -1 among them would pull the mean below every real duty).
func (a *acc) add(p Point) {
	for k, v := range p.Temp {
		a.temp[k] = a.temp[k].add(v)
	}
	for k, v := range p.Duty {
		if v < 0 {
			continue
		}
		a.duty[k] = a.duty[k].add(float64(v))
	}
	for k, v := range p.RPM {
		a.rpm[k] = a.rpm[k].add(float64(v))
	}
	for k, v := range p.Extra {
		a.extra[k] = a.extra[k].add(v)
	}
}

func (s sum) add(v float64) sum { return sum{total: s.total + v, n: s.n + 1} }
func (s sum) mean() float64     { return s.total / float64(s.n) }

// point is the bucket's arithmetic mean: temperatures to three decimals,
// duty and rpm rounded to int; a key absent in every raw point is absent.
func (a *acc) point() Point {
	p := Point{TS: a.start, Temp: make(map[string]float64, len(a.temp)), Duty: make(map[string]int, len(a.duty)), RPM: make(map[string]int, len(a.rpm))}
	for k, s := range a.temp {
		p.Temp[k] = math.Round(s.mean()*1000) / 1000
	}
	for k, s := range a.duty {
		p.Duty[k] = int(math.Round(s.mean()))
	}
	for k, s := range a.rpm {
		p.RPM[k] = int(math.Round(s.mean()))
	}
	if len(a.extra) > 0 {
		p.Extra = make(map[string]float64, len(a.extra))
		for k, s := range a.extra {
			p.Extra[k] = math.Round(s.mean()*1000) / 1000
		}
	}
	return p
}

func (t *tier) bucketStart(ts int64) int64 {
	return ts - ((ts%t.bucket)+t.bucket)%t.bucket
}

// push folds p into the open bucket, closing it first when p starts a
// later one. A point before the open bucket (clock step) joins the open
// bucket; a point in the bucket of the last closed point (restart within
// a bucket) reopens that point with the weight of one raw point.
func (t *tier) push(p Point) {
	start := t.bucketStart(p.TS)
	if t.open != nil {
		if start > t.open.start {
			t.closeOpen()
		} else {
			t.open.add(p)
			return
		}
	}
	if n := len(t.pts); n > 0 && start <= t.pts[n-1].TS {
		last := t.pts[n-1]
		t.pts = t.pts[:n-1]
		t.open = newAcc(last.TS)
		t.open.add(last)
	} else {
		t.open = newAcc(start)
	}
	t.open.add(p)
}

func (t *tier) closeOpen() {
	if t.open == nil {
		return
	}
	t.pts = append(t.pts, t.open.point())
	t.open = nil
	if len(t.pts) > t.capacity {
		drop := len(t.pts) - t.capacity
		t.pts = append(t.pts[:0], t.pts[drop:]...)
	}
}

// all returns the closed buckets plus the open one (running mean).
func (t *tier) all() []Point {
	if t.open == nil {
		return t.pts
	}
	out := make([]Point, 0, len(t.pts)+1)
	out = append(out, t.pts...)
	return append(out, t.open.point())
}

// load replaces the tier with the file's points (older than span dropped,
// capacity applied) and rebuilds the last bucket from the raw points that
// fall into it, so a restart inside a bucket continues its mean exactly.
func (t *tier) load(pts []Point, now time.Time, raw []Point) {
	t.open = nil
	t.pts = keepSince(pts, now.Add(-t.span).Unix())
	if len(t.pts) > t.capacity {
		drop := len(t.pts) - t.capacity
		t.pts = append(t.pts[:0], t.pts[drop:]...)
	}
	n := len(t.pts)
	if n == 0 {
		return
	}
	last := t.pts[n-1].TS
	var later []Point
	rebuild := false
	for _, p := range raw {
		if p.TS >= last {
			later = append(later, p)
			if t.bucketStart(p.TS) == last {
				rebuild = true
			}
		}
	}
	if rebuild {
		t.pts = t.pts[:n-1]
	}
	for _, p := range later {
		t.push(p)
	}
}
