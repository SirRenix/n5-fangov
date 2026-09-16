package web

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/alert"
)

type fakeAlerts struct {
	status      AlertStatus
	recent      []AlertRecord
	testErr     error
	templateErr error
	configured  [][2]string
	configErr   error
}

func (a *fakeAlerts) Status() AlertStatus        { return a.status }
func (a *fakeAlerts) Recent(n int) []AlertRecord { return a.recent }
func (a *fakeAlerts) Test() (string, error)      { return "mail", a.testErr }

func (a *fakeAlerts) InstallTemplate() (string, error) {
	return "/etc/pve/notification-templates/default", a.templateErr
}

// Configure mirrors the manager's merge: a nil member keeps the value.
func (a *fakeAlerts) Configure(s AlertSettings) (AlertStatus, error) {
	if a.configErr != nil {
		return AlertStatus{}, a.configErr
	}
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&a.status.Transport, s.Transport)
	set(&a.status.MailTo, s.MailTo)
	set(&a.status.WebhookURL, s.WebhookURL)
	set(&a.status.WebhookFormat, s.WebhookFormat)
	a.configured = append(a.configured, [2]string{a.status.Transport, a.status.MailTo})
	return a.status, nil
}

// ---- alerts -----------------------------------------------------------------

func TestAlertsEndpoints(t *testing.T) {
	e, _, al, _ := storesEnv(t, adminBasic)
	ok := basicAuth("admin", "pw")
	r := e.do(t, "GET", "/api/alerts", "", ok)
	wantCode(t, r, 200)
	m := keys(t, r.body)
	for _, k := range []string{"transport", "effective", "mail_to", "webhook_url", "webhook_format", "pve_available", "mail_available", "template", "cooldown", "kinds", "last", "recent"} {
		if _, has := m[k]; !has {
			t.Errorf("alerts lacks %q: %s", k, r.body)
		}
	}
	if string(m["last"]) != `{"stall":1789490000}` || !strings.Contains(string(m["recent"]), `"msg":"hdd stalled"`) || !strings.Contains(string(m["kinds"]), `"fan stopped"`) {
		t.Errorf("alerts = %s", r.body)
	}
	al.recent = nil
	al.status.Kinds = nil
	r = e.do(t, "GET", "/api/alerts", "", ok)
	if !strings.Contains(r.body, `"recent":[]`) || !strings.Contains(r.body, `"kinds":[]`) {
		t.Errorf("nil lists: %s", r.body)
	}
	// test
	r = e.do(t, "POST", "/api/alerts/test", "", ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"transport":"mail"`) || !strings.Contains(r.body, `"ok":true`) {
		t.Errorf("test = %s", r.body)
	}
	al.testErr = errors.New("sendmail: exit 1")
	r = e.do(t, "POST", "/api/alerts/test", "", ok)
	wantError(t, r, 502, "sendmail")
	if !strings.Contains(r.body, `"transport":"mail"`) {
		t.Errorf("test failure = %s", r.body)
	}
	// template
	r = e.do(t, "POST", "/api/alerts/template", "", ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"path":"/etc/pve/notification-templates/default"`) {
		t.Errorf("template = %s", r.body)
	}
	al.templateErr = fmt.Errorf("no PVE here: %w", errors.ErrUnsupported)
	wantError(t, e.do(t, "POST", "/api/alerts/template", "", ok), 501, "no PVE")
	al.templateErr = errors.New("permission denied")
	wantError(t, e.do(t, "POST", "/api/alerts/template", "", ok), 500, "run: n5-fangov alerts template")
	// configure
	r = e.do(t, "PUT", "/api/alerts", `{"transport":"log","mail_to":"ops"}`, ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"ok":true`) || !strings.Contains(r.body, `"transport":"log"`) || !strings.Contains(r.body, `"mail_to":"ops"`) || len(al.configured) != 1 {
		t.Errorf("configure = %s %v", r.body, al.configured)
	}
	// webhook keys: omitted ones keep their value, the log line carries
	// the URL without its query
	r = e.do(t, "PUT", "/api/alerts", `{"transport":"webhook","webhook_url":"https://gotify.example.test/message?token=secret-key","webhook_format":"text"}`, ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"webhook_url":"https://gotify.example.test/message?token=secret-key"`) || !strings.Contains(r.body, `"webhook_format":"text"`) || !strings.Contains(r.body, `"mail_to":"ops"`) || al.status.Transport != "webhook" {
		t.Errorf("configure webhook = %s", r.body)
	}
	if logs := e.logLines(); strings.Contains(logs, "secret-key") || !strings.Contains(logs, "webhook https://gotify.example.test/message text") {
		t.Errorf("log lines: %s", logs)
	}
	r = e.do(t, "PUT", "/api/alerts", `{"mail_to":"root"}`, ok)
	wantCode(t, r, 200)
	if al.status.Transport != "webhook" || al.status.WebhookURL != "https://gotify.example.test/message?token=secret-key" || al.status.MailTo != "root" {
		t.Errorf("omitted keys must keep their value: %+v", al.status)
	}
	// the full URL is in the protected status document
	r = e.do(t, "GET", "/api/alerts", "", ok)
	if !strings.Contains(r.body, `"webhook_url":"https://gotify.example.test/message?token=secret-key"`) {
		t.Errorf("status = %s", r.body)
	}
	al.configErr = errors.New("unknown transport \"fax\"")
	wantError(t, e.do(t, "PUT", "/api/alerts", `{"transport":"fax","mail_to":"ops"}`, ok), 400, "fax")
	wantError(t, e.do(t, "PUT", "/api/alerts", `{"transport":`, ok), 400, "invalid JSON")
	wantError(t, e.do(t, "PUT", "/api/alerts", `{"bogus":1}`, ok), 400, "invalid JSON")
	// all protected
	wantError(t, e.do(t, "POST", "/api/alerts/test", "", csrf), 401, "authentication")
	wantError(t, e.do(t, "PUT", "/api/alerts", `{"transport":"log"}`, csrf), 401, "authentication")
}

// TestAlertsTestBusy: a test delivery still running answers 409.
func TestAlertsTestBusy(t *testing.T) {
	e, _, al, _ := storesEnv(t, adminBasic)
	al.testErr = alert.ErrTestBusy
	r := e.do(t, "POST", "/api/alerts/test", "", basicAuth("admin", "pw"))
	wantError(t, r, 409, "test in progress")
	if !strings.Contains(r.body, `"transport":"mail"`) {
		t.Errorf("busy = %s", r.body)
	}
}
