// events_api.go holds GET /api/events (the chart markers) and the
// recording of the operator's actions into the event log (DESIGN
// "Events"): preset apply, override set and clear, the assist switch and
// a config reload from the editor or an import. The daemon's own events
// (start, critical, ceiling, failsafe, alerts) are recorded by the
// controller and serve.
package web

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/SirRenix/n5-fangov/internal/events"
)

// EventLog is the event log behind GET /api/events (*events.Log).
type EventLog interface {
	events.Recorder
	// Since returns the events of the last span with ts > since, oldest
	// first.
	Since(span time.Duration, since int64) []events.Event
	// Open returns the spans that have not ended.
	Open() []events.Event
}

// publicEvent is an event for an anonymous caller: when and what kind, no
// text and no cause (they name users, tokens and values).
type publicEvent struct {
	TS   int64  `json:"ts"`
	Kind string `json:"kind"`
	Ch   string `json:"ch,omitempty"`
	End  bool   `json:"end,omitempty"`
}

// getEvents answers the events of the last `minutes` (default 120,
// 1..10080, as /api/history) with ts > since, oldest first; reduced for
// anonymous callers. 501 without an event log.
func (s *Server) getEvents(w http.ResponseWriter, r *http.Request) {
	if s.deps.Events == nil {
		writeError(w, http.StatusNotImplemented, "no event log")
		return
	}
	minutes, ok := historyMinutes(w, r)
	if !ok {
		return
	}
	var since int64
	if q := r.URL.Query().Get("since"); q != "" {
		n, err := strconv.ParseInt(q, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "since must be a unix timestamp")
			return
		}
		since = n
	}
	evs := s.deps.Events.Since(time.Duration(minutes)*time.Minute, since)
	if !CallerFrom(r.Context()).Authenticated {
		out := make([]publicEvent, 0, len(evs))
		for _, e := range evs {
			out = append(out, publicEvent{TS: e.TS, Kind: e.Kind, Ch: e.Ch, End: e.End})
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	if evs == nil {
		evs = []events.Event{}
	}
	writeJSON(w, http.StatusOK, evs)
}

// event records an operator action of r's caller (user:<name>,
// token:<name>, cli for the socket). The text gets the caller's short name
// appended in parentheses. No event log: nothing.
func (s *Server) event(r *http.Request, kind, ch, text string) {
	if s.deps.Events == nil {
		return
	}
	by := ackSource(CallerFrom(r.Context()))
	s.deps.Events.Add(events.Event{Kind: kind, Ch: ch, Text: fmt.Sprintf("%s (%s)", text, events.Who(by)), By: by})
}

// overrideOpen reports whether the event log holds an open override span
// for ch: a clear without one (the channel was on its curve) records
// nothing.
func (s *Server) overrideOpen(ch string) bool {
	if s.deps.Events == nil {
		return false
	}
	for _, e := range s.deps.Events.Open() {
		if e.Kind == events.Override && e.Ch == ch {
			return true
		}
	}
	return false
}
