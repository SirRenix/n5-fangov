// dashboard_api.go holds the endpoints behind the dashboard cards:
// /api/dashboard (watched sensors), /api/assist (the assist switch),
// /api/about and /api/system.
package web

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"runtime"
)

// maxDashboardSensors mirrors the config rule (0..8 ids).
const maxDashboardSensors = 8

func (s *Server) dashboard(w http.ResponseWriter) (DashboardStore, bool) {
	if s.deps.Dashboard == nil {
		writeError(w, http.StatusNotImplemented, "no dashboard store")
		return nil, false
	}
	return s.deps.Dashboard, true
}

func (s *Server) getDashboard(w http.ResponseWriter, r *http.Request) {
	d, ok := s.dashboard(w)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sensors": nonNil(d.Sensors())})
}

// putDashboard: {"sensors":[...]} — array required, at most 8, the store
// validates the ids (400 on refusal) and reports unresolvable ones.
func (s *Server) putDashboard(w http.ResponseWriter, r *http.Request) {
	d, ok := s.dashboard(w)
	if !ok {
		return
	}
	var b struct {
		Sensors []string `json:"sensors"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if b.Sensors == nil {
		writeError(w, http.StatusBadRequest, `"sensors" array required`)
		return
	}
	if len(b.Sensors) > maxDashboardSensors {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d sensors", maxDashboardSensors))
		return
	}
	warnings, err := d.SetSensors(b.Sensors)
	if err != nil {
		writeError(w, storeStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sensors": nonNil(d.Sensors()), "warnings": nonNil(warnings)})
}

// putAssist: {"enabled": bool} switches the channel's [[assist]] table
// (only its enabled line is written); 404 without a table, the controller
// takes it on the next cycle.
func (s *Server) putAssist(w http.ResponseWriter, r *http.Request) {
	if s.deps.Assist == nil {
		writeError(w, http.StatusNotImplemented, "no assist store")
		return
	}
	name := r.PathValue("name")
	if !channelName.MatchString(name) {
		writeError(w, http.StatusBadRequest, "invalid channel name")
		return
	}
	var b struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	if b.Enabled == nil {
		writeError(w, http.StatusBadRequest, `"enabled" (true or false) required`)
		return
	}
	if err := s.deps.Assist.SetEnabled(name, *b.Enabled); err != nil {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			writeError(w, http.StatusNotFound, err.Error())
		case isReloadError(err):
			writeError(w, http.StatusInternalServerError, err.Error())
		default:
			writeError(w, storeStatus(err), err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel": name, "enabled": *b.Enabled})
}

// ---- about ------------------------------------------------------------------

// getAbout serves Deps.About; empty name/version fall back to what the
// server knows so the card is never blank. The Go toolchain version is
// only shown to a signed-in caller: together with a public version number
// it would tell an anonymous LAN host which stdlib to look up CVEs for.
func (s *Server) getAbout(w http.ResponseWriter, r *http.Request) {
	a := s.deps.About
	if a.Name == "" {
		a.Name = "n5-fangov"
	}
	if a.Version == "" {
		a.Version = s.deps.Version
	}
	if !CallerFrom(r.Context()).Authenticated {
		a.Go = ""
	} else if a.Go == "" {
		a.Go = runtime.Version()
	}
	if a.Credits == nil {
		a.Credits = []Credit{}
	}
	writeJSON(w, http.StatusOK, a)
}

// ---- system -----------------------------------------------------------------

// getSystem serves the hardware inventory (protected like every other
// /api/ path: it names the operator's hardware). Deps.System nil → 501.
func (s *Server) getSystem(w http.ResponseWriter, r *http.Request) {
	if s.deps.System == nil {
		writeError(w, http.StatusNotImplemented, "no system inventory")
		return
	}
	writeJSON(w, http.StatusOK, s.deps.System())
}
