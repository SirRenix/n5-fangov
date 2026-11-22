package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/events"
)

func init() {
	register("events", command{run: cmdEvents})
}

// kindFlags collects the repeatable --kind.
type kindFlags []string

func (k *kindFlags) String() string { return strings.Join(*k, ",") }
func (k *kindFlags) Set(v string) error {
	if !events.Known(v) {
		return fmt.Errorf("unknown kind %q (one of %s)", v, strings.Join(events.Kinds, ", "))
	}
	*k = append(*k, v)
	return nil
}

// cmdEvents prints the event log, one line per event, oldest first:
//
//	events [--since 24h|7d|DURATION] [--kind K]... [--json]
//
// With the daemon it asks GET /api/events over the socket; without it
// (socket missing or refused) it reads <state dir>/events.json, read-only.
// A span kind given to --kind includes its end event.
func cmdEvents(args []string) int {
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	since := fs.String("since", "24h", "span back from now: 24h, 7d or a duration (1m..7d)")
	var kinds kindFlags
	fs.Var(&kinds, "kind", "only this kind (repeatable; override includes override-end)")
	asJSON := fs.Bool("json", false, "print the events as a JSON array")
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return exitUsage
	}
	span, err := parseSpan(*since)
	if err != nil || len(pos) != 0 {
		if err != nil {
			fmt.Fprintln(os.Stderr, "events:", err)
		}
		fmt.Fprintln(os.Stderr, "usage: n5-fangov events [--since 24h|7d|DURATION] [--kind K]... [--json]")
		return exitUsage
	}
	evs, source, err := loadEvents(runDir(), span)
	if err != nil {
		fmt.Fprintln(os.Stderr, "events:", err)
		return exitFail
	}
	evs = filterKinds(evs, kinds)
	if *asJSON {
		b, _ := json.MarshalIndent(evs, "", "  ")
		fmt.Println(string(b))
		return exitOK
	}
	printEvents(os.Stdout, evs, source)
	return exitOK
}

// parseSpan reads --since: "7d" / "2d" (days), else a Go duration; 1 min
// to 7 days.
func parseSpan(s string) (time.Duration, error) {
	var d time.Duration
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil {
			return 0, fmt.Errorf("--since %q: not a number of days", s)
		}
		d = time.Duration(days) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, fmt.Errorf("--since %q: %v", s, err)
		}
	}
	if d < time.Minute || d > events.Retention {
		return 0, fmt.Errorf("--since %q: between 1m and 7d", s)
	}
	return d, nil
}

// loadEvents asks the daemon (minutes rounded up); without it the state
// file is read (events.New writes nothing). source names where they came
// from.
func loadEvents(dir string, span time.Duration) ([]events.Event, string, error) {
	minutes := int((span + time.Minute - 1) / time.Minute)
	var evs []events.Event
	err := newAPI(dir).get(fmt.Sprintf("/api/events?minutes=%d", minutes), &evs)
	switch {
	case err == nil:
		return evs, "daemon", nil
	case !errors.Is(err, errNoDaemon):
		return nil, "", err
	}
	path := eventsPath(stateDir())
	l, err := events.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, "", fmt.Errorf("daemon not running and %s not readable: %w (run as root)", path, err)
		}
		return nil, "", fmt.Errorf("daemon not running and %s not readable: %w", path, err)
	}
	return l.Since(span, 0), path + " (daemon not running)", nil
}

// filterKinds keeps the events of kinds (none = all); a span kind takes
// its end event along.
func filterKinds(evs []events.Event, kinds []string) []events.Event {
	if len(kinds) == 0 {
		return evs
	}
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	out := []events.Event{}
	for _, e := range evs {
		start, _ := events.SpanStart(e.Kind)
		if want[e.Kind] || want[start] {
			out = append(out, e)
		}
	}
	return out
}

// printEvents writes one line per event: local time, kind, channel, text,
// cause; then the source.
func printEvents(w io.Writer, evs []events.Event, source string) {
	if len(evs) == 0 {
		fmt.Fprintf(w, "no events (source: %s)\n", source)
		return
	}
	for _, e := range evs {
		ch := e.Ch
		if ch == "" {
			ch = "-"
		}
		by := ""
		if e.By != "" {
			by = "  [" + e.By + "]"
		}
		fmt.Fprintf(w, "%s  %-13s %-6s %s%s\n", time.Unix(e.TS, 0).Format("2006-01-02 15:04:05"), e.Kind, ch, e.Text, by)
	}
	fmt.Fprintf(w, "%d event(s), source: %s\n", len(evs), source)
}
