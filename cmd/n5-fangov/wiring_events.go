// wiring_events.go holds the glue of the event log (internal/events,
// DESIGN "Events"): the alert marker hook, the close-out of the previous
// process's spans at start, the failsafe marker of the ExecStopPost run and
// the CLI's log on the state file.
package main

import (
	"log"
	"os"
	"regexp"

	"github.com/SirRenix/n5-fangov/internal/alert"
	"github.com/SirRenix/n5-fangov/internal/events"
)

// alertMarker returns the alert ring hook that marks every recorded alert
// in rec: kind only, never the message (it can carry paths and sensor
// ids); a failed delivery says so.
func alertMarker(rec events.Recorder, by string) func(alert.Record) {
	return func(r alert.Record) {
		text := "alert " + r.Kind
		if r.Error != "" {
			text += " (not delivered)"
		}
		rec.Add(events.Event{TS: r.TS, Kind: events.Alert, Text: text, By: by})
	}
}

// serveEventLog is the daemon's event log: <state dir>/events.json, or
// memory only for a dry run. A dry run holds no writer lock and may run
// next to the live daemon; its simulated states and its start-time
// cleanup must not reach the shared file.
func serveEventLog(state string, dryRun bool) *events.Log {
	if dryRun {
		log.Printf("events: dry run, event log in memory only")
		return events.New("", log.Default())
	}
	return events.New(eventsPath(state), log.Default())
}

// endStaleSpans ends the spans the previous daemon left open (it stopped,
// so its critical, ceiling and failsafe states and the stop failsafe of
// ExecStopPost are over), except an override this daemon restored from its
// run file, and a full cooling it continued from panic.until (panicOn).
// Called before the loop starts; the open spans are read from l
// (no lock), the end events go through q, so the cleanup never holds the
// start up on the file lock. The daemon's own stop ends critical and
// ceiling already (control: Run); this is the fallback after a kill.
func endStaleSpans(l *events.Log, q events.Recorder, overrides map[string]int, panicOn bool) {
	n := 0
	for _, e := range l.Open() {
		if _, restored := overrides[e.Ch]; e.Kind == events.Override && restored {
			continue
		}
		if e.Kind == events.Panic && panicOn {
			continue
		}
		q.Add(events.Event{Kind: e.Kind + "-end", Ch: e.Ch, Text: "ended (daemon started)", By: events.ByDaemon})
		n++
	}
	if n > 0 {
		log.Printf("events: %d open span(s) of the previous run ended", n)
	}
}

// cliEventLog is the event log of a CLI run without the daemon: the state
// file when the state directory exists (a box where the daemon never ran
// has none, and the CLI does not create it), else memory only.
func cliEventLog() *events.Log {
	dir := stateDir()
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return events.New("", nil)
	}
	return events.New(eventsPath(dir), log.Default())
}

// serviceResult is the form of systemd's $SERVICE_RESULT ("success",
// "exit-code", "watchdog"); anything else stays out of the text.
var serviceResult = regexp.MustCompile(`^[a-z-]{1,32}$`)

// markStopFailsafe records the safe state that `failsafe` (ExecStopPost)
// set after the daemon ended: a failsafe span that the next daemon start
// ends. A span that is already open (the daemon's own write-error
// failsafe) is not opened twice. result is $SERVICE_RESULT ("" for a run
// by hand).
func markStopFailsafe(l *events.Log, result string) {
	for _, e := range l.Open() {
		if e.Kind == events.Failsafe {
			return
		}
	}
	text := "safe state, daemon not running"
	if serviceResult.MatchString(result) {
		text = "safe state after daemon end (" + result + ")"
	}
	l.Add(events.Event{Kind: events.Failsafe, Text: text, By: events.ByCLI})
}
