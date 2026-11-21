package web

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/events"
)

// eventsEnv is a basic-auth env with a memory event log, the assist fake
// (hdd has a table) and a token file.
func eventsEnv(t *testing.T) (*env, *events.Log) {
	t.Helper()
	e, _, _, _ := storesEnv(t, adminBasic)
	log := events.New("", nil)
	e.withDeps(t, adminBasic, func(d *Deps) {
		d.Events = log
		d.Assist = &fakeAssist{state: map[string]bool{"hdd": false}}
		d.TokenFile = filepath.Join(t.TempDir(), "tokens.json")
		d.Account = &fakeAccount{cfg: adminBasic}
		d.Alerts = &fakeAlerts{status: AlertStatus{Transport: "log", Effective: "log"}}
		d.Dashboard = &fakeDashboard{sensors: []string{}}
		d.SessionFile = filepath.Join(t.TempDir(), "sessions.json")
	})
	return e, log
}

// lines renders the log as "kind ch|text|by" for the assertions.
func eventLines(l *events.Log) []string {
	var out []string
	for _, e := range l.Since(0, 0) {
		out = append(out, e.Kind+" "+e.Ch+"|"+e.Text+"|"+e.By)
	}
	return out
}

func wantEvents(t *testing.T, l *events.Log, want ...string) {
	t.Helper()
	if got := strings.Join(eventLines(l), "\n"); got != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

// Every operator action through the API is recorded once, with the caller.
func TestEventsRecordedByAPI(t *testing.T) {
	e, log := eventsEnv(t)
	admin := basicAuth("admin", "pw")
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":150}`, admin), 200)
	wantCode(t, e.do(t, "DELETE", "/api/override/cpu", "", admin), 200)
	// a clear on a channel without an override records nothing
	wantCode(t, e.do(t, "DELETE", "/api/override/cpu", "", admin), 200)
	// a refused override records nothing
	wantCode(t, e.do(t, "PUT", "/api/override/hdd", `{"duty":20}`, admin), 400)
	wantCode(t, e.do(t, "POST", "/api/presets/quiet/apply", "", admin), 200)
	wantCode(t, e.do(t, "PUT", "/api/assist/hdd", `{"enabled":true}`, admin), 200)
	wantCode(t, e.do(t, "PUT", "/api/assist/hdd", `{"enabled":false}`, admin), 200)
	wantCode(t, e.do(t, "PUT", "/api/config", sampleTOML, admin), 200)
	wantEvents(t, log,
		"override cpu|cpu manual 150 (admin)|user:admin",
		"override-end cpu|cpu back to the curve (admin)|user:admin",
		"preset |preset quiet (admin)|user:admin",
		"assist hdd|assist hdd on (admin)|user:admin",
		"assist hdd|assist hdd off (admin)|user:admin",
		"reload |config reloaded (admin)|user:admin",
	)
	if o := log.Open(); len(o) != 0 {
		t.Errorf("open spans: %+v", o)
	}
}

// The socket (CLI) is "cli", a token its name.
func TestEventsRecordedCallers(t *testing.T) {
	e, log := eventsEnv(t)
	rec := httptest.NewRecorder()
	e.srv.SocketHandler().ServeHTTP(rec, httptest.NewRequest("PUT", "/api/override/cpu", strings.NewReader(`{"duty":200}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("socket override: %d %s", rec.Code, rec.Body)
	}
	secret, _ := mint(t, e, "ha", "control", 0)
	wantCode(t, e.do(t, "DELETE", "/api/override/cpu", "", bearer(secret)), 200)
	wantEvents(t, log,
		"override cpu|cpu manual 200 (cli)|cli",
		"override-end cpu|cpu back to the curve (token ha)|token:ha",
	)
}

func TestEventsEndpoint(t *testing.T) {
	e, log := eventsEnv(t)
	now := log.Since(0, 0) // empty
	if len(now) != 0 {
		t.Fatal("fresh log not empty")
	}
	log.Add(events.Event{Kind: events.Override, Ch: "hdd", Text: "hdd manual 150 (admin)", By: "user:admin"})
	log.Add(events.Event{Kind: events.OverrideEnd, Ch: "hdd", Text: "hdd back to the curve (admin)", By: "user:admin"})
	all := log.Since(0, 0)

	// signed in: everything, oldest first
	r := e.do(t, "GET", "/api/events", "", basicAuth("admin", "pw"))
	wantCode(t, r, 200)
	var full []events.Event
	decode(t, r.body, &full)
	if len(full) != 2 || full[0].Text != "hdd manual 150 (admin)" || full[1].By != "user:admin" || !full[1].End || full[0].ID != 1 {
		t.Fatalf("signed in: %s", r.body)
	}
	// anonymous: ts, kind, ch, end only
	r = e.do(t, "GET", "/api/events", "", nil)
	wantCode(t, r, 200)
	for _, bad := range []string{`"text"`, `"by"`, `"id"`, "admin"} {
		if strings.Contains(r.body, bad) {
			t.Errorf("anonymous answer carries %s: %s", bad, r.body)
		}
	}
	var pub []map[string]any
	decode(t, r.body, &pub)
	if len(pub) != 2 || pub[0]["kind"] != "override" || pub[0]["ch"] != "hdd" || pub[1]["end"] != true {
		t.Errorf("anonymous: %s", r.body)
	}
	// since and minutes as for /api/history
	r = e.do(t, "GET", "/api/events?since="+itoa(all[0].TS-1), "", basicAuth("admin", "pw"))
	decode(t, r.body, &full)
	if len(full) != 2 {
		t.Errorf("since before both: %s", r.body)
	}
	r = e.do(t, "GET", "/api/events?minutes=1&since="+itoa(all[1].TS), "", basicAuth("admin", "pw"))
	if wantCode(t, r, 200); strings.TrimSpace(r.body) != "[]" {
		t.Errorf("since the last: %s", r.body)
	}
	wantError(t, e.do(t, "GET", "/api/events?minutes=0", "", nil), 400, "minutes must be 1..10080")
	wantError(t, e.do(t, "GET", "/api/events?minutes=10081", "", nil), 400, "minutes")
	wantError(t, e.do(t, "GET", "/api/events?since=-1", "", nil), 400, "since")
	wantError(t, e.do(t, "GET", "/api/events?since=x", "", nil), 400, "since")
	// scope read is enough, a token sees the full events
	secret, _ := mint(t, e, "reader", "read", 0)
	r = e.do(t, "GET", "/api/events", "", bearer(secret))
	if wantCode(t, r, 200); !strings.Contains(r.body, `"by":"user:admin"`) {
		t.Errorf("read token: %s", r.body)
	}
	// a read token cannot act, so it records nothing
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":100}`, bearer(secret)), 403)
	if n := len(log.Since(0, 0)); n != 2 {
		t.Errorf("events after a refused call: %d", n)
	}
}

func TestEventsNoLog(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	wantError(t, e.do(t, "GET", "/api/events", "", nil), 501, "no event log")
	// actions still work without a log
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", `{"duty":100}`, map[string]string{CSRFHeader: "1"}), 200)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
