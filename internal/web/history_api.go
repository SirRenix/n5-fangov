// history_api.go holds the history CSV export and the schedules status
// (DESIGN "Web and API": GET /api/history.csv, GET /api/schedules). The
// tiered history itself lives in internal/history; GET /api/history is in
// web.go.
package web

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/SirRenix/n5-fangov/internal/history"
)

// History span bounds of GET /api/history and /api/history.csv (minutes):
// up to the 5-min tier's 7 days.
const (
	minHistoryMinutes     = 1
	maxHistoryMinutes     = 7 * 24 * 60
	defaultHistoryMinutes = 120
)

// historyMinutes parses the `minutes` query (default 120, 1..10080); a
// bad value answers 400 and returns ok = false.
func historyMinutes(w http.ResponseWriter, r *http.Request) (int, bool) {
	q := r.URL.Query().Get("minutes")
	if q == "" {
		return defaultHistoryMinutes, true
	}
	n, err := strconv.Atoi(q)
	if err != nil || n < minHistoryMinutes || n > maxHistoryMinutes {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("minutes must be %d..%d", minHistoryMinutes, maxHistoryMinutes))
		return 0, false
	}
	return n, true
}

// historyCSV (protected) streams the history tier for `minutes` as a CSV
// attachment: columns per channel (Deps.Channels, else the snapshot's
// order), then the extra sensor ids seen in the points, sorted.
func (s *Server) historyCSV(w http.ResponseWriter, r *http.Request) {
	if s.deps.Service == nil {
		writeError(w, http.StatusNotImplemented, "no service")
		return
	}
	minutes, ok := historyMinutes(w, r)
	if !ok {
		return
	}
	pts := s.deps.Service.HistoryRange(time.Duration(minutes)*time.Minute, 0)
	var channels []string
	if s.deps.Channels != nil {
		channels = s.deps.Channels()
	} else {
		for _, c := range s.deps.Service.Snapshot().Channels {
			channels = append(channels, c.Name)
		}
	}
	seen := map[string]bool{}
	var extras []string
	for _, p := range pts {
		for id := range p.Extra {
			if !seen[id] {
				seen[id] = true
				extras = append(extras, id)
			}
		}
	}
	sort.Strings(extras)
	// Local time, the same clock as the time column (DESIGN §9): a 02:08 export is
	// named 020800, not the UTC 000800.
	name := fmt.Sprintf("n5-fangov-history-%s-%s.csv", hostLabel(), time.Now().Local().Format("20060102-150405"))
	attachment(w, "text/csv; charset=utf-8", name)
	w.WriteHeader(http.StatusOK)
	if err := history.CSV(w, pts, channels, extras); err != nil {
		// Headers are out; the client sees a truncated file. Say so in the log.
		s.logf("web: history csv: %v", err)
	}
}

// getSchedules (protected) answers the scheduler's status; 501 without a
// scheduler.
func (s *Server) getSchedules(w http.ResponseWriter, r *http.Request) {
	if s.deps.Schedules == nil {
		writeError(w, http.StatusNotImplemented, "no scheduler")
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Schedules())
}
