// alerts_api.go holds the alerts panel endpoints (/api/alerts: status and
// history, test delivery, template install, transport change).
package web

import (
	"encoding/json"
	"errors"
	"fmt"
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
// can carry "last" and "recent" next to its members. For a token caller
// the webhook URL is redacted (alert.RedactURL): a token of any scope is
// meant for scripts and agents, the URL carries the receiver's key in its
// query or path — a leaked read token must not hand it out. Cookie, Basic
// and socket callers (the operator) see the full URL.
func alertStatusJSON(st AlertStatus, c Caller) map[string]any {
	if st.Kinds == nil {
		st.Kinds = []AlertKind{}
	}
	if c.Via == "bearer" {
		st.WebhookURL = alert.RedactURL(st.WebhookURL)
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
	out := alertStatusJSON(m.Status(), CallerFrom(r.Context()))
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
	if a, ok := m.(AlertAcker); ok {
		out["open"] = a.Open()
	}
	writeJSON(w, http.StatusOK, out)
}

// alertsAckBody is the POST /api/alerts/ack body: either the IDs to
// acknowledge, or all with up_to, the newest ID the caller has seen.
type alertsAckBody struct {
	IDs  []uint64 `json:"ids"`
	All  bool     `json:"all"`
	UpTo uint64   `json:"up_to"`
}

// maxAckIDs bounds the ID list (the ring holds alert.RingSize records).
const maxAckIDs = alert.RingSize

// ackSource names who acknowledged, for the record: the signed-in user,
// the token's name, or "cli" for the socket.
func ackSource(c Caller) string {
	switch c.Via {
	case "socket":
		return "cli"
	case "bearer":
		return "token:" + c.User
	}
	if c.User == "" {
		return "user"
	}
	return "user:" + c.User
}

// alertsAck acknowledges alerts (display only: delivery, cooldown and the
// failsafe are untouched).
func (s *Server) alertsAck(w http.ResponseWriter, r *http.Request) {
	m, ok := s.alerts(w)
	if !ok {
		return
	}
	a, ok := m.(AlertAcker)
	if !ok {
		writeError(w, http.StatusNotImplemented, "alert acknowledgement not available")
		return
	}
	var b alertsAckBody
	if !decodeJSON(w, r, &b) {
		return
	}
	by := ackSource(CallerFrom(r.Context()))
	switch {
	case b.All && len(b.IDs) > 0:
		writeError(w, http.StatusBadRequest, "give either ids or all, not both")
		return
	case b.All:
		if b.UpTo == 0 {
			writeError(w, http.StatusBadRequest, "all needs up_to: the newest alert id the client has seen")
			return
		}
		n := a.AckUpTo(b.UpTo, by)
		s.logf("web: %d alert(s) up to #%d acknowledged by %s from %s", n, b.UpTo, by, remoteIP(r))
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "acked": n, "open": a.Open()})
		return
	case len(b.IDs) == 0:
		writeError(w, http.StatusBadRequest, "ids is empty")
		return
	case len(b.IDs) > maxAckIDs:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d ids", maxAckIDs))
		return
	}
	n, unknown := a.Ack(b.IDs, by)
	if len(unknown) > 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown alert id", "unknown": unknown})
		return
	}
	s.logf("web: %d alert(s) acknowledged by %s from %s", n, by, remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "acked": n, "open": a.Open()})
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

// alertsUpdate is the PUT /api/alerts body; an omitted key keeps the
// value in effect.
type alertsUpdate struct {
	Transport     *string `json:"transport"`
	MailTo        *string `json:"mail_to"`
	WebhookURL    *string `json:"webhook_url"`
	WebhookFormat *string `json:"webhook_format"`
}

// putAlerts: {"transport","mail_to","webhook_url","webhook_format"} →
// the manager validates, writes and applies. The log line names the
// webhook URL without its query (a Gotify key lives there).
func (s *Server) putAlerts(w http.ResponseWriter, r *http.Request) {
	m, ok := s.alerts(w)
	if !ok {
		return
	}
	var b alertsUpdate
	if !decodeJSON(w, r, &b) {
		return
	}
	st, err := m.Configure(AlertSettings(b))
	if err != nil {
		writeError(w, storeStatus(err), err.Error())
		return
	}
	s.logf("web: alert transport set to %q (mail_to %q, webhook %s %s) by %s", st.Transport, st.MailTo, alert.RedactURL(st.WebhookURL), st.WebhookFormat, remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": alertStatusJSON(st, CallerFrom(r.Context()))})
}
