package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/alert"
	"github.com/SirRenix/n5-fangov/internal/events"
)

// recorder collects events (events.Recorder).
type recorder struct{ evs []events.Event }

func (r *recorder) Add(e events.Event) { r.evs = append(r.evs, e) }

// A scheduled switch is one preset event by "schedule"; ticks inside the
// window and a failed apply add none.
func TestSchedulerMarksPreset(t *testing.T) {
	st := newSchedTest(t, 17, 59)
	rec := &recorder{}
	st.s.events = rec
	st.s.Set(schedEntries())
	st.s.evaluate()
	st.tick(30 * time.Second)
	st.tick(29 * time.Second)
	if len(rec.evs) != 1 {
		t.Fatalf("events %+v", rec.evs)
	}
	e := rec.evs[0]
	if e.Kind != events.Preset || e.By != events.BySchedule || e.Text != "preset office (schedule 08:00-18:00)" || e.TS != st.clock.Add(-59*time.Second).Unix() {
		t.Errorf("event %+v", e)
	}
	st.fail["balanced"] = errors.New("boom")
	st.tick(time.Second) // 18:00 → balanced fails
	if len(rec.evs) != 1 {
		t.Errorf("failed apply marked: %+v", rec.evs)
	}
}

// The ring hook marks delivered alerts with the kind only.
func TestAlertMarker(t *testing.T) {
	rec := &recorder{}
	r := alert.NewRing(&alert.Log{Logger: &discard{}}, "", nil)
	r.SetOnRecord(alertMarker(rec, events.ByDaemon))
	r.Alert("stall", "hdd: fan stopped at /sys/class/hwmon/hwmon3")
	none := alert.NewRing(nil, "", nil) // no sink: delivery error
	none.SetOnRecord(alertMarker(rec, events.ByCLI))
	none.Alert("restart", "unit restarted")
	if len(rec.evs) != 2 {
		t.Fatalf("events %+v", rec.evs)
	}
	if e := rec.evs[0]; e.Kind != events.Alert || e.Text != "alert stall" || e.By != events.ByDaemon || e.TS == 0 {
		t.Errorf("delivered %+v", e)
	}
	if e := rec.evs[1]; e.Text != "alert restart (not delivered)" || e.By != events.ByCLI {
		t.Errorf("failed %+v", e)
	}
}

type discard struct{}

func (discard) Printf(string, ...any) {}

// At start the previous run's spans end, a restored override stays open;
// the stop failsafe of the CLI opens one failsafe span, never two.
func TestEndStaleSpansAndStopFailsafe(t *testing.T) {
	l := events.New(filepath.Join(t.TempDir(), "events.json"), nil)
	l.Add(events.Event{Kind: events.Override, Ch: "hdd", Text: "hdd manual 150 (admin)"})
	l.Add(events.Event{Kind: events.Override, Ch: "cpu", Text: "cpu manual 200 (admin)"})
	l.Add(events.Event{Kind: events.Ceiling, Ch: "ssd", Text: "ssd ceiling 85C"})
	markStopFailsafe(l, "exit-code")
	markStopFailsafe(l, "success") // the span is open already
	markStopFailsafe(l, "")
	var fs []events.Event
	for _, e := range l.Since(0, 0) {
		if e.Kind == events.Failsafe {
			fs = append(fs, e)
		}
	}
	if len(fs) != 1 || fs[0].Text != "safe state after daemon end (exit-code)" || fs[0].By != events.ByCLI {
		t.Fatalf("failsafe events %+v", fs)
	}
	endStaleSpans(l, l, map[string]int{"hdd": 150})
	open := l.Open()
	if len(open) != 1 || open[0].Ch != "hdd" {
		t.Fatalf("open after start: %+v", open)
	}
	// a result that is not systemd's form stays out of the text
	l2 := events.New("", nil)
	markStopFailsafe(l2, "x; rm -rf /")
	if e := l2.Recent(1)[0]; e.Text != "safe state, daemon not running" {
		t.Errorf("text %q", e.Text)
	}
}

func TestParseSpan(t *testing.T) {
	for in, want := range map[string]time.Duration{"24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "2d": 48 * time.Hour, "90m": 90 * time.Minute, "1m": time.Minute} {
		if got, err := parseSpan(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	for _, bad := range []string{"8d", "30s", "x", "d", "-1h", ""} {
		if _, err := parseSpan(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Offline: the state file is read (and not written); --kind and --json.
func TestCLIEventsOffline(t *testing.T) {
	state := t.TempDir()
	t.Setenv(stateDirEnv, state)
	t.Setenv("N5FANGOV_RUN_DIR", t.TempDir()) // no socket there
	path := eventsPath(state)
	l := events.New(path, nil)
	l.Add(events.Event{Kind: events.Start, Text: "started 0.7.0", By: events.ByDaemon})
	l.Add(events.Event{Kind: events.Override, Ch: "hdd", Text: "hdd manual 150 (admin)", By: "user:admin"})
	l.Add(events.Event{Kind: events.OverrideEnd, Ch: "hdd", Text: "hdd back to the curve (admin)", By: "user:admin"})
	before, _ := os.Stat(path)

	out, errOut, code := captureOutput(t, func() int { return cmdEvents(nil) })
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{"start         -      started 0.7.0  [daemon]", "override      hdd    hdd manual 150 (admin)  [user:admin]", "override-end  hdd", "3 event(s), source: " + path + " (daemon not running)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	out, _, code = captureOutput(t, func() int { return cmdEvents([]string{"--kind", "override", "--json", "--since", "7d"}) })
	var evs []events.Event
	if err := json.Unmarshal([]byte(out), &evs); err != nil || code != exitOK {
		t.Fatalf("json: %v %d %s", err, code, out)
	}
	if len(evs) != 2 || evs[0].Kind != events.Override || !evs[1].End {
		t.Errorf("--kind override: %+v", evs)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		t.Error("the CLI wrote the state file")
	}
	_, errOut, code = captureOutput(t, func() int { return cmdEvents([]string{"--kind", "bogus"}) })
	if code != exitUsage || !strings.Contains(errOut, "unknown kind") {
		t.Errorf("bad kind: %d %q", code, errOut)
	}
	_, _, code = captureOutput(t, func() int { return cmdEvents([]string{"--since", "9d"}) })
	if code != exitUsage {
		t.Errorf("--since 9d: %d", code)
	}
	// an empty state dir: no events, no file created
	t.Setenv(stateDirEnv, t.TempDir())
	out, _, _ = captureOutput(t, func() int { return cmdEvents(nil) })
	if !strings.Contains(out, "no events") {
		t.Errorf("empty: %q", out)
	}
}

// With the daemon the CLI asks the socket, minutes from --since.
func TestCLIEventsSocket(t *testing.T) {
	d := startFakeDaemon(t)
	d.stop()
	var query string
	d.extra = func(mux *http.ServeMux) {
		mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) {
			d.mu.Lock()
			query = r.URL.RawQuery
			d.mu.Unlock()
			_ = json.NewEncoder(w).Encode([]events.Event{{ID: 7, TS: 1789500000, Kind: events.Preset, Text: "preset n5pro-cool (schedule fallback)", By: events.BySchedule}})
		})
	}
	d.start(t)
	out, errOut, code := captureOutput(t, func() int { return cmdEvents([]string{"--since", "90m"}) })
	if code != exitOK || !strings.Contains(out, "preset n5pro-cool (schedule fallback)  [schedule]") || !strings.Contains(out, "source: daemon") {
		t.Fatalf("socket: %d %q %q", code, out, errOut)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if query != "minutes=90" {
		t.Errorf("query %q", query)
	}
}

// Offline with a file that cannot be read: an error and exit 1, not "no
// events" (a directory stands for permission denied: the tests run as root).
func TestCLIEventsUnreadable(t *testing.T) {
	state := t.TempDir()
	t.Setenv(stateDirEnv, state)
	t.Setenv("N5FANGOV_RUN_DIR", t.TempDir())
	if err := os.Mkdir(eventsPath(state), 0o700); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := captureOutput(t, func() int { return cmdEvents(nil) })
	if code != exitFail || !strings.Contains(errOut, "not readable") || strings.Contains(out, "no events") {
		t.Fatalf("unreadable: %d %q %q", code, out, errOut)
	}
}

// A dry run next to the live daemon: its events (start, simulated states,
// the start-time cleanup) stay in memory; the shared file is untouched.
func TestDryRunWritesNoEvents(t *testing.T) {
	state := t.TempDir()
	live := events.New(eventsPath(state), nil)
	live.Add(events.Event{Kind: events.Critical, Ch: "ssd", Text: "ssd critical 76.0C (limit 75C)", By: events.ByDaemon})
	before, _ := os.ReadFile(eventsPath(state))
	dry := serveEventLog(state, true)
	q := events.NewQueue(dry, nil)
	endStaleSpans(dry, q, nil) // as serve does: the dry run's own (empty) log, so the live span stays open
	q.Add(events.Event{Kind: events.Start, Text: "started (dry-run)"})
	q.Close(5 * time.Second)
	after, _ := os.ReadFile(eventsPath(state))
	if string(before) != string(after) {
		t.Fatalf("dry run wrote the shared file:\n%s", after)
	}
	if len(dry.Since(0, 0)) != 1 || len(live.Open()) != 1 {
		t.Errorf("dry %+v live open %+v", dry.Since(0, 0), live.Open())
	}
}
