package events

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type recLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *recLogger) Printf(f string, a ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(f, a...))
}

func (l *recLogger) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// clock is a settable time source for a Log.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// base is the start of the test clocks: the real time (New trims with
// time.Now before a test installs its clock).
var base = time.Unix(time.Now().Unix(), 0)

func newClocked(t *testing.T, path string) (*Log, *clock) {
	t.Helper()
	c := &clock{t: base}
	l := New(path, nil)
	l.now = c.now
	return l, c
}

func readFile(t *testing.T, path string) []Event {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var evs []Event
	if err := json.Unmarshal(b, &evs); err != nil {
		t.Fatal(err)
	}
	return evs
}

func TestLogAddSinceAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	l, c := newClocked(t, path)
	if got := l.Since(0, 0); len(got) != 0 {
		t.Fatalf("missing file: %v", got)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("New wrote the file")
	}
	l.Add(Event{Kind: Start, Text: "started 0.7.0", By: ByDaemon})
	c.add(time.Minute)
	l.Add(Event{Kind: Override, Ch: "hdd", Text: "hdd manual 150 (admin)", By: "user:admin"})
	c.add(time.Minute)
	l.Add(Event{Kind: OverrideEnd, Ch: "hdd", Text: "hdd back to the curve (admin)", By: "user:admin"})
	got := l.Since(0, 0)
	if len(got) != 3 || got[0].ID != 1 || got[2].ID != 3 || got[0].TS != base.Unix() || !got[2].End || got[1].End {
		t.Fatalf("events %+v", got)
	}
	if s := l.Since(0, got[0].TS); len(s) != 2 || s[0].Kind != Override {
		t.Errorf("since: %+v", s)
	}
	if s := l.Since(90*time.Second, 0); len(s) != 2 {
		t.Errorf("span 90 s: %+v", s)
	}
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("file mode: %v %v", err, st)
		}
	}
	// reload from the file; numbering continues
	l2, _ := newClocked(t, path)
	l2.now = c.now
	l2.Add(Event{Kind: Reload, Text: "config reloaded (admin)"})
	if f := readFile(t, path); len(f) != 4 || f[3].ID != 4 {
		t.Errorf("file after a second log: %+v", f)
	}
	if r := l2.Recent(2); len(r) != 2 || r[1].Kind != Reload {
		t.Errorf("recent: %+v", r)
	}
}

func TestLogRetentionAndCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	l, c := newClocked(t, path)
	l.Add(Event{Kind: Start, Text: "old"})
	c.add(6 * 24 * time.Hour)
	l.Add(Event{Kind: Start, Text: "mid"})
	c.add(24*time.Hour + time.Second)
	l.Add(Event{Kind: Start, Text: "new"})
	if got := l.Since(0, 0); len(got) != 2 || got[0].Text != "mid" {
		t.Fatalf("retention: %+v", got)
	}
	if f := readFile(t, path); len(f) != 2 {
		t.Fatalf("the file keeps an expired event: %+v", f)
	}
	// the cap: another writer's full file plus this log's adds
	var full []Event
	for i := 0; i < MaxEntries+19; i++ {
		full = append(full, Event{ID: uint64(100 + i), TS: c.now().Unix(), Kind: Alert, Text: fmt.Sprint("alert ", i)})
	}
	b, _ := json.Marshal(full)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	l.Add(Event{Kind: Alert, Text: fmt.Sprint("alert ", MaxEntries+19)})
	got := l.Since(0, 0)
	if len(got) != MaxEntries || got[len(got)-1].Text != fmt.Sprint("alert ", MaxEntries+19) || got[0].Text != "alert 20" {
		t.Fatalf("cap: %d, first %q", len(got), got[0].Text)
	}
	if f := readFile(t, path); len(f) != MaxEntries {
		t.Fatalf("file holds %d", len(f))
	}
	// an expired event in a file written by another process is dropped on load
	c.add(Retention + time.Hour)
	l3 := New(path, nil)
	l3.now = c.now
	if got := l3.Since(0, 0); len(got) != 0 {
		t.Errorf("expired events visible: %d", len(got))
	}
}

// A forward clock step of more than the retention, corrected later, drops
// nothing; a step back keeps the events "from the future" as well.
func TestLogClockStep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	l, c := newClocked(t, path)
	l.Add(Event{Kind: Start, Text: "a"})
	l.Add(Event{Kind: Reload, Text: "b"})
	c.add(10 * 24 * time.Hour) // RTC or NTP step forward
	l.Add(Event{Kind: Start, Text: "jumped"})
	if f := readFile(t, path); len(f) != 3 {
		t.Fatalf("a forward step purged the log: %+v", f)
	}
	c.add(-10*24*time.Hour + time.Minute) // corrected
	l.Add(Event{Kind: Start, Text: "after"})
	if f := readFile(t, path); len(f) != 4 {
		t.Fatalf("the correction dropped events: %+v", f)
	}
	// a long stop: the old events go with the second write after it
	path2 := filepath.Join(t.TempDir(), "events.json")
	l2, c2 := newClocked(t, path2)
	l2.Add(Event{Kind: Start, Text: "a"})
	c2.add(8 * 24 * time.Hour)
	l2.Add(Event{Kind: Start, Text: "x"})
	if f := readFile(t, path2); len(f) != 2 {
		t.Fatalf("first write after a long stop: %+v", f)
	}
	l2.Add(Event{Kind: Start, Text: "y"})
	if f := readFile(t, path2); len(f) != 2 || f[0].Text != "x" {
		t.Fatalf("second write after a long stop: %+v", f)
	}
}

// An end without its start (trimmed, or a restored override whose start
// left the log) is no open span and ends nothing.
func TestOrphanEnd(t *testing.T) {
	l, _ := newClocked(t, "")
	l.Add(Event{Kind: OverrideEnd, Ch: "hdd", Text: "hdd back to the curve (admin)"})
	if o := l.Open(); len(o) != 0 {
		t.Fatalf("orphan end opened %+v", o)
	}
	if n := l.EndOpen(nil, "x", ByDaemon); n != 0 {
		t.Errorf("EndOpen on an orphan: %d", n)
	}
}

// ReadFile: a missing file is empty, an unreadable one an error (a
// directory stands for "permission denied": the tests run as root).
func TestReadFile(t *testing.T) {
	dir := t.TempDir()
	l, err := ReadFile(filepath.Join(dir, "events.json"))
	if err != nil || len(l.Since(0, 0)) != 0 {
		t.Fatalf("missing file: %v", err)
	}
	bad := filepath.Join(dir, "unreadable.json")
	if err := os.Mkdir(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(bad); err == nil {
		t.Fatal("unreadable file read as empty")
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFile(filepath.Join(dir, "broken.json")); err == nil {
		t.Error("broken file read as empty")
	}
}

func TestLogBrokenFileAndMemoryOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	lg := &recLogger{}
	l := New(path, lg)
	if len(l.Since(0, 0)) != 0 || lg.count("(ignored)") != 1 {
		t.Fatalf("broken file: %v", lg.lines)
	}
	l.Add(Event{Kind: Start, Text: "x"})
	if f := readFile(t, path); len(f) != 1 {
		t.Errorf("file rewritten: %+v", f)
	}
	m := New("", nil)
	m.Add(Event{Kind: Start, Text: "x"})
	if len(m.Since(0, 0)) != 1 {
		t.Error("memory-only log lost the event")
	}
	if runtime.GOOS != "windows" {
		lg2 := &recLogger{}
		bad := New(filepath.Join(t.TempDir(), "missing", "events.json"), lg2)
		bad.Add(Event{Kind: Start, Text: "a"})
		bad.Add(Event{Kind: Start, Text: "b"})
		if lg2.count("events kept in memory only") != 1 || len(bad.Since(0, 0)) != 2 {
			t.Errorf("unwritable file: %v", lg2.lines)
		}
	}
}

func TestOpenSpansAndEndOpen(t *testing.T) {
	l, c := newClocked(t, "")
	add := func(kind, ch string) { c.add(time.Second); l.Add(Event{Kind: kind, Ch: ch, Text: kind}) }
	add(Override, "hdd")
	add(Critical, "cpu")
	add(CriticalEnd, "cpu")
	add(Ceiling, "ssd")
	add(Override, "cpu")
	add(Failsafe, "")
	add(Preset, "")
	open := l.Open()
	var keys []string
	for _, e := range open {
		keys = append(keys, e.Kind+":"+e.Ch)
	}
	if strings.Join(keys, " ") != "override:hdd ceiling:ssd override:cpu failsafe:" {
		t.Fatalf("open spans %v", keys)
	}
	n := l.EndOpen(func(e Event) bool { return e.Kind == Override && e.Ch == "hdd" }, "ended (daemon started)", ByDaemon)
	if n != 3 {
		t.Fatalf("ended %d", n)
	}
	if o := l.Open(); len(o) != 1 || o[0].Ch != "hdd" {
		t.Fatalf("open after EndOpen: %+v", o)
	}
	last := l.Recent(1)[0]
	if !last.End || last.By != ByDaemon || last.Text != "ended (daemon started)" {
		t.Errorf("end event %+v", last)
	}
	// a second EndOpen at the next start changes nothing
	if n := l.EndOpen(func(Event) bool { return true }, "x", ByDaemon); n != 0 {
		t.Errorf("kept span ended: %d", n)
	}
}

func TestKindsAndWho(t *testing.T) {
	for _, k := range Kinds {
		if !Known(k) {
			t.Errorf("%s unknown", k)
		}
	}
	if Known("override-start") || Known("") {
		t.Error("unknown kind accepted")
	}
	if s, ok := SpanStart(CeilingEnd); !ok || s != Ceiling {
		t.Errorf("SpanStart: %s %v", s, ok)
	}
	if _, ok := SpanStart("preset-end"); ok {
		t.Error("preset is no span")
	}
	for by, want := range map[string]string{"user:admin": "admin", "token:ha": "token ha", "cli": "cli", "schedule": "schedule"} {
		if got := Who(by); got != want {
			t.Errorf("Who(%q) = %q", by, got)
		}
	}
}

func TestMergeConflict(t *testing.T) {
	mem := []Event{{ID: 1, TS: 10, Kind: Start, Text: "a"}, {ID: 2, TS: 11, Kind: Reload, Text: "b"}}
	file := []Event{{ID: 1, TS: 10, Kind: Start, Text: "a"}, {ID: 2, TS: 12, Kind: Preset, Text: "c"}, {ID: 3, TS: 13, Kind: Alert, Text: "d"}}
	out, last := merge(mem, file, 0)
	if len(out) != 4 || last != 4 {
		t.Fatalf("merge: %+v last %d", out, last)
	}
	ids := map[string]uint64{}
	for _, e := range out {
		ids[e.Text] = e.ID
	}
	if ids["a"] != 1 || ids["b"] != 2 || ids["d"] != 3 || ids["c"] != 4 {
		t.Errorf("ids %v", ids)
	}
	// merging the result again changes nothing
	if again, l2 := merge(out, append(file, Event{ID: 4, TS: 12, Kind: Preset, Text: "c"}), last); len(again) != 4 || l2 != 4 {
		t.Errorf("second merge: %+v %d", again, l2)
	}
}

func TestQueue(t *testing.T) {
	l, _ := newClocked(t, filepath.Join(t.TempDir(), "events.json"))
	q := NewQueue(l, nil)
	for i := 0; i < 50; i++ {
		q.Add(Event{Kind: Alert, Text: fmt.Sprint(i)})
	}
	q.Flush(5 * time.Second)
	got := l.Since(0, 0)
	if len(got) != 50 {
		t.Fatalf("queued %d", len(got))
	}
	for i, e := range got {
		if e.Text != fmt.Sprint(i) || e.TS == 0 {
			t.Fatalf("order/ts at %d: %+v", i, e)
		}
	}
	q.Add(Event{Kind: Start, Text: "last"})
	q.Close(5 * time.Second)
	q.Close(time.Second) // twice is fine
	q.Add(Event{Kind: Start, Text: "after close"})
	q.Flush(time.Second)
	if r := l.Recent(1); r[0].Text != "last" {
		t.Errorf("close did not drain: %+v", r)
	}
}

// A full queue drops (logged once) instead of blocking the caller.
func TestQueueFullDrops(t *testing.T) {
	lg := &recLogger{}
	q := &Queue{log: New("", nil), logger: lg, now: time.Now, ch: make(chan queued, 1), done: make(chan struct{})}
	q.Add(Event{Kind: Start, Text: "1"})
	q.Add(Event{Kind: Start, Text: "2"})
	q.Add(Event{Kind: Start, Text: "3"})
	if lg.count("queue full") != 1 {
		t.Errorf("full queue log: %v", lg.lines)
	}
}
