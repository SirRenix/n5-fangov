// alerts_api.go holds the alerts panel endpoints (/api/alerts: status and
// history, test delivery, template install, transport change).
package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/SirRenix/n5-fangov/internal/alert"
)

func (s *Server) alerts(w http.ResponseWriter) (AlertMgr, bool) {
	if s.deps.Alerts == nil {
		writeError(w, http.StatusNotImplemented, "no alert manager")
		return nil, false
	}
	return s.deps.Alerts, true
}

// alertStatusJSON flattens an AlertStatus into a map so the panel document
// can carry "last" and "recent" next to its members.
func alertStatusJSON(st AlertStatus) map[string]any {
	if st.Kinds == nil {
		st.Kinds = []AlertKind{}
	}
	b, _ := json.Marshal(st)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

// getAlerts: AlertStatus + last-sent times (from the snapshot) + the ring.
func (s *Server) getAlerts(w http.ResponseWriter, r *http.Request) {
	m, ok := s.alerts(w)
	if !ok {
		return
	}
	out := alertStatusJSON(m.Status())
	// last-sent times: the controller's stamps, overlaid by the ring (which
	// also sees serve's own alerts: config, kernel, tls, test).
	last := map[string]int64{}
	if s.deps.Service != nil {
		for k, v := range s.deps.Service.Snapshot().Alerts {
			last[k] = v
		}
	}
	if l, ok := m.(interface{ Last() map[string]int64 }); ok {
		for k, v := range l.Last() {
			if v > last[k] {
				last[k] = v
			}
		}
	}
	out["last"] = last
	recent := m.Recent(50)
	if recent == nil {
		recent = []AlertRecord{}
	}
	out["recent"] = recent
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) alertsTest(w http.ResponseWriter, r *http.Request) {
	m, ok := s.alerts(w)
	if !ok {
		return
	}
	transport, err := m.Test()
	if err != nil {
		if errors.Is(err, alert.ErrTestBusy) {
			// One test delivery at a time; the previous one still runs.
			writeJSON(w, http.StatusConflict, map[string]any{"error": "test in progress", "transport": transport})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error(), "transport": transport})
		return
	}
	s.logf("web: test alert sent via %s by %s", transport, remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "transport": transport})
}

// alertsTemplate installs the PVE notification templates: 501 without
// PVE, 500 with the CLI fallback in the message.
func (s *Server) alertsTemplate(w http.ResponseWriter, r *http.Request) {
	m, ok := s.alerts(w)
	if !ok {
		return
	}
	path, err := m.InstallTemplate()
	if err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			writeError(w, http.StatusNotImplemented, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error()+"; run: n5-fangov alerts template")
		return
	}
	s.logf("web: alert template installed at %s by %s", path, remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "path": path})
}

// putAlerts: {"transport","mail_to"} → the manager validates and applies.
func (s *Server) putAlerts(w http.ResponseWriter, r *http.Request) {
	m, ok := s.alerts(w)
	if !ok {
		return
	}
	var b struct {
		Transport string `json:"transport"`
		MailTo    string `json:"mail_to"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	st, err := m.Configure(b.Transport, b.MailTo)
	if err != nil {
		writeError(w, storeStatus(err), err.Error())
		return
	}
	s.logf("web: alert transport set to %q (mail_to %q) by %s", b.Transport, b.MailTo, remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": alertStatusJSON(st)})
}
