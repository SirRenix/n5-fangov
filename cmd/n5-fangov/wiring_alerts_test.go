package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/alert"
	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// settings is the transport/mail_to pair as PUT /api/alerts sends it
// (the webhook keys omitted).
func settings(transport, mailTo string) web.AlertSettings {
	return web.AlertSettings{Transport: &transport, MailTo: &mailTo}
}

func strp(s string) *string { return &s }

func TestAlertManagerConfigure(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	state := t.TempDir()
	m := newAlertManager(cfgPath, alertsPath(state), config.Alert{Transport: "log", MailTo: "root"}, nil)
	m.cooldown = func() time.Duration { return 30 * time.Minute }
	st := m.Status()
	if st.Transport != "log" || st.Effective != "log" || st.Cooldown != "30m0s" || len(st.Kinds) != len(alertKinds) || st.Template.Path == "" {
		t.Errorf("status: %+v", st)
	}
	kinds := map[string]bool{}
	for _, k := range st.Kinds {
		kinds[k.Kind] = true
		if k.Description == "" {
			t.Errorf("kind %s without description", k.Kind)
		}
	}
	for _, k := range []string{"sensor", "stall", "temp", "write", "config", "config-channels", "restart", "failed", "kernel", "tls", "test"} {
		if !kinds[k] {
			t.Errorf("kind %s missing", k)
		}
	}
	// test alert lands in the ring and its mirror
	eff, err := m.Test()
	if err != nil || eff != "log" {
		t.Errorf("test: %s %v", eff, err)
	}
	rec := m.Recent(5)
	if len(rec) != 1 || rec[0].Kind != "test" || !strings.Contains(rec[0].Msg, "test alert") || m.Last()["test"] == 0 {
		t.Errorf("recent: %+v", rec)
	}
	if _, err := os.Stat(alertsPath(state)); err != nil {
		t.Errorf("mirror file: %v", err)
	}
	// the sink serve/controller use is the ring: an alert through it is recorded
	m.sink().Alert("stall", "fan stopped")
	if rec := m.Recent(1); len(rec) != 1 || rec[0].Kind != "stall" {
		t.Errorf("ring wraps the sink: %+v", rec)
	}
	// Configure: validation
	if _, err := m.Configure(settings("pigeon", "")); err == nil {
		t.Errorf("unknown transport must be refused")
	}
	if _, err := m.Configure(settings("mail", "two words")); err == nil {
		t.Errorf("bad mail_to must be refused")
	}
	// Configure: off is written, hot-applied, comments kept
	st, err = m.Configure(settings(" OFF ", ""))
	if err != nil || st.Transport != "off" || st.Effective != "off" || st.MailTo != "root" || st.WebhookFormat != "json" {
		t.Fatalf("configure off: %+v %v", st, err)
	}
	// only the keys the request set are written
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "[alert]\ntransport = \"off\"\nmail_to = \"root\"\n") || strings.Contains(string(raw), "webhook") || !strings.Contains(string(raw), "# my config") {
		t.Errorf("file:\n%s", raw)
	}
	if m.sw.Get().Name() != "off" {
		t.Errorf("sink not swapped: %s", m.sw.Get().Name())
	}
	// apply with the same values is a no-op, with new ones a swap
	if m.apply(config.Alert{Transport: "off", MailTo: "root"}) != "off" || m.apply(config.Alert{Transport: "log", MailTo: "root"}) != "log" {
		t.Errorf("apply")
	}
	if m.sw.Get().Name() != "log" {
		t.Errorf("apply must swap: %s", m.sw.Get().Name())
	}
	// a fresh manager on the mirror file sees the history
	m2 := newAlertManager(cfgPath, alertsPath(state), config.Alert{Transport: "log"}, nil)
	if got := m2.Recent(0); len(got) != 2 || got[0].Kind != "stall" {
		t.Errorf("history reload: %+v", got)
	}
}

// TestAlertManagerWebhook: the webhook transport through the manager —
// Configure merges omitted keys, validates the merged section, writes the
// four keys, swaps the sink; the test alert is delivered by POST; the log
// line and the CLI view carry the URL without its query.
func TestAlertManagerWebhook(t *testing.T) {
	var mu sync.Mutex
	var got []string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, r.URL.RawQuery+" "+r.Header.Get("Content-Type")+" "+string(b))
		mu.Unlock()
	}))
	defer hook.Close()
	url := hook.URL + "/hook?token=secret-key"

	cfgPath := writeStoreConfig(t)
	// the file carries the section in effect (the merge basis is the file)
	if err := os.WriteFile(cfgPath, []byte(storeConfig+"\n[alert]\ntransport = \"log\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := newAlertManager(cfgPath, "", config.Alert{Transport: "log", MailTo: "root", WebhookFormat: "json"}, nil)
	var logged strings.Builder
	m.logger = log.New(&logged, "", 0)
	// webhook without a URL, in the same request or in effect: refused
	if _, err := m.Configure(web.AlertSettings{Transport: strp("webhook")}); err == nil || !strings.Contains(err.Error(), "webhook_url") {
		t.Errorf("webhook without url: %v", err)
	}
	for _, bad := range []string{"ntfy.example.test/n5", "ftp://h.example.test/", "https://user:pw@h.example.test/"} {
		if _, err := m.Configure(web.AlertSettings{Transport: strp("webhook"), WebhookURL: strp(bad)}); err == nil || !strings.Contains(err.Error(), "webhook_url") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, err := m.Configure(web.AlertSettings{WebhookFormat: strp("xml")}); err == nil || !strings.Contains(err.Error(), "webhook_format") {
		t.Errorf("bad format: %v", err)
	}
	if m.sw.Get().Name() != "log" {
		t.Fatalf("sink swapped on refusal: %s", m.sw.Get().Name())
	}
	// valid: URL first (transport stays log), then the transport alone
	st, err := m.Configure(web.AlertSettings{WebhookURL: strp(" " + url + " "), WebhookFormat: strp(" TEXT ")})
	if err != nil || st.Transport != "log" || st.WebhookURL != url || st.WebhookFormat != "text" || st.Effective != "log" {
		t.Fatalf("url only: %+v %v", st, err)
	}
	st, err = m.Configure(web.AlertSettings{Transport: strp("webhook")})
	if err != nil || st.Transport != "webhook" || st.Effective != "webhook" || st.WebhookURL != url || st.WebhookFormat != "text" || st.MailTo != "root" {
		t.Fatalf("transport only: %+v %v", st, err)
	}
	// only the keys the requests set are written (transport in place, the
	// webhook keys appended); mail_to was never set and is not in the file
	raw, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(raw), "[alert]\ntransport = \"webhook\"\nwebhook_url = \""+url+"\"\nwebhook_format = \"text\"\n") || strings.Contains(string(raw), "mail_to") {
		t.Errorf("file:\n%s", raw)
	}
	if m.sw.Get().Name() != "webhook" {
		t.Errorf("sink: %s", m.sw.Get().Name())
	}
	// the log line names the redacted URL and the format
	if l := logged.String(); !strings.Contains(l, "alerts: transport webhook (webhook), webhook "+hook.URL+"/hook (text)") || strings.Contains(l, "secret-key") {
		t.Errorf("log: %s", l)
	}
	// the test alert goes out as a POST with the text body
	if eff, err := m.Test(); err != nil || eff != "webhook" {
		t.Fatalf("test: %s %v", eff, err)
	}
	mu.Lock()
	reqs := append([]string(nil), got...)
	mu.Unlock()
	if len(reqs) != 1 || !strings.HasPrefix(reqs[0], "token=secret-key text/plain; charset=utf-8 test alert from n5-fangov") {
		t.Errorf("requests: %q", reqs)
	}
	// a fresh manager on the file parses the same section; the apply of
	// an unchanged section is a no-op, a format switch swaps
	cfg, _, err := config.Load(cfgPath)
	if err != nil || cfg.Alert.Transport != "webhook" || cfg.Alert.WebhookURL != url || cfg.Alert.WebhookFormat != "text" {
		t.Fatalf("reload: %+v %v", cfg.Alert, err)
	}
	if m.apply(cfg.Alert) != "webhook" {
		t.Errorf("apply same")
	}
	cfg.Alert.WebhookFormat = "json"
	m.apply(cfg.Alert)
	if eff, err := m.Test(); err != nil || eff != "webhook" {
		t.Fatalf("test json: %s %v", eff, err)
	}
	mu.Lock()
	last := got[len(got)-1]
	mu.Unlock()
	if !strings.HasPrefix(last, "token=secret-key application/json {") || !strings.Contains(last, `"kind":"test"`) {
		t.Errorf("json request: %q", last)
	}
	// CLI view: the status prints the redacted URL and the format
	var r alertsResp
	s := m.Status()
	r.Transport, r.Effective, r.WebhookURL, r.WebhookFormat = s.Transport, s.Effective, s.WebhookURL, s.WebhookFormat
	out, _, _ := captureOutput(t, func() int { printAlertStatus(r, "test"); return 0 })
	if !strings.Contains(out, "webhook:      "+hook.URL+"/hook (json)") || strings.Contains(out, "secret-key") {
		t.Errorf("status output:\n%s", out)
	}
	// back to log: the URL is cleared from the file and from the status (a
	// receiver key must not linger after the switch, 0.4.1); the format stays
	if st, err := m.Configure(settings("log", "")); err != nil || st.WebhookURL != "" || st.Effective != "log" || st.WebhookFormat != "text" {
		t.Errorf("back to log: %+v %v", st, err)
	}
	raw, _ = os.ReadFile(cfgPath)
	if s := string(raw); !strings.Contains(s, "transport = \"log\"") || !strings.Contains(s, "webhook_url = \"\"") || strings.Contains(s, url) {
		t.Errorf("file after the switch back:\n%s", raw)
	}
	if m.cur.WebhookURL != "" {
		t.Errorf("URL still in effect: %q", m.cur.WebhookURL)
	}
	// a request that only changes mail_to leaves an existing URL alone
	if _, err := m.Configure(web.AlertSettings{Transport: strp("webhook"), WebhookURL: strp(url)}); err != nil {
		t.Fatal(err)
	}
	if st, err := m.Configure(web.AlertSettings{MailTo: strp("ops")}); err != nil || st.WebhookURL != url || st.Transport != "webhook" {
		t.Errorf("mail_to only: %+v %v", st, err)
	}
}

// TestAlertConfigureMergesOnFile: the merge basis of Configure is the
// [alert] section of the file, not the section in effect — a PUT
// /api/config that answered 202, or an edit by hand, put a webhook URL and
// a recipient into the file that the manager never applied; a later PUT
// /api/alerts {transport: webhook} must find the URL there and must not
// write the stale recipient back. A file that does not parse falls back to
// the section in effect.
func TestAlertConfigureMergesOnFile(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	m := newAlertManager(cfgPath, "", config.Alert{Transport: "log", MailTo: "root", WebhookFormat: "json"}, nil)
	// another writer changes the file behind the manager's back
	if err := os.WriteFile(cfgPath, []byte(storeConfig+"\n[alert]\ntransport = \"log\"\nmail_to = \"ops\"\nwebhook_url = \"https://ntfy.example.test/n5\"\nwebhook_format = \"text\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := m.Configure(web.AlertSettings{Transport: strp("webhook")})
	if err != nil {
		t.Fatalf("configure on the file's URL: %v", err)
	}
	if st.Transport != "webhook" || st.WebhookURL != "https://ntfy.example.test/n5" || st.WebhookFormat != "text" || st.MailTo != "ops" || st.Effective != "webhook" {
		t.Errorf("status merged on the stale section: %+v", st)
	}
	raw, _ := os.ReadFile(cfgPath)
	if s := string(raw); !strings.Contains(s, "transport = \"webhook\"") || !strings.Contains(s, "mail_to = \"ops\"") || !strings.Contains(s, "webhook_format = \"text\"") || strings.Count(s, "webhook_url") != 1 {
		t.Errorf("file after the merge:\n%s", s)
	}
	// the section in effect follows the file
	if m.cur.MailTo != "ops" || m.cur.WebhookURL != "https://ntfy.example.test/n5" {
		t.Errorf("cur: %+v", m.cur)
	}
	// a refused merge writes nothing: the file keeps its bytes
	before, _ := os.ReadFile(cfgPath)
	if _, err := m.Configure(web.AlertSettings{MailTo: strp("two words")}); err == nil {
		t.Fatal("bad mail_to must be refused")
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Errorf("refused request changed the file:\n%s", after)
	}
	// a request that sets nothing changes nothing and answers the file's state
	if st, err := m.Configure(web.AlertSettings{}); err != nil || st.Transport != "webhook" || st.MailTo != "ops" {
		t.Errorf("empty request: %+v %v", st, err)
	}
}

// TestAlertTemplateProbeCache: Status reuses the probe for ten
// minutes; InstallTemplate and Configure drop the cache.
func TestAlertTemplateProbeCache(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	m := newAlertManager(cfgPath, "", config.Alert{Transport: "log", MailTo: "root"}, nil)
	now := time.Unix(1789500000, 0)
	m.now = func() time.Time { return now }
	probes := 0
	m.probe = func() (bool, bool, bool, string) {
		probes++
		return true, probes%2 == 1, false, fmt.Sprintf("probe %d", probes)
	}
	for i := 0; i < 3; i++ {
		if st := m.Status(); st.Template.Reason != "probe 1" || !st.Template.Installed || !st.Template.Current || st.Template.Path != alert.TemplatePath {
			t.Errorf("call %d: %+v", i, st.Template)
		}
	}
	if probes != 1 {
		t.Fatalf("probes after three Status calls: %d", probes)
	}
	now = now.Add(templateProbeEvery - time.Second)
	m.Status()
	if probes != 1 {
		t.Errorf("re-probed before the interval: %d", probes)
	}
	now = now.Add(time.Second)
	if st := m.Status(); probes != 2 || st.Template.Reason != "probe 2" || st.Template.Current {
		t.Errorf("after the interval: probes=%d %+v", probes, st.Template)
	}
	// InstallTemplate invalidates, also when it fails (no PVE here)
	_, _ = m.InstallTemplate()
	m.Status()
	if probes != 3 {
		t.Errorf("after InstallTemplate: %d", probes)
	}
	// Configure invalidates; its own Status re-probes once
	if _, err := m.Configure(settings("log", "root")); err != nil {
		t.Fatal(err)
	}
	if probes != 4 {
		t.Errorf("after Configure: %d", probes)
	}
	m.Status()
	if probes != 4 {
		t.Errorf("cached again after Configure: %d", probes)
	}
	// a clock that went backwards re-probes rather than caching forever
	now = now.Add(-time.Hour)
	m.Status()
	if probes != 5 {
		t.Errorf("clock skew: %d", probes)
	}
}

// gate is a ContextSender that blocks until released or the context ends.
type gate struct {
	alert.Log
	started chan struct{}
	release chan struct{}
}

func (g *gate) Name() string { return "gate" }

func (g *gate) SendCtx(ctx context.Context, kind, msg string) error {
	close(g.started)
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestAlertTestBusyAndBounded: a second test while one runs answers
// ErrTestBusy; a delivery that hangs is cut at testLimit.
func TestAlertTestBusyAndBounded(t *testing.T) {
	cfgPath := writeStoreConfig(t)
	m := newAlertManager(cfgPath, "", config.Alert{Transport: "log", MailTo: "root"}, nil)
	g := &gate{Log: alert.Log{Logger: m.logger}, started: make(chan struct{}), release: make(chan struct{})}
	m.sw.Set(g)
	done := make(chan error, 1)
	go func() { _, err := m.Test(); done <- err }()
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first test never reached the sink")
	}
	if eff, err := m.Test(); !errors.Is(err, alert.ErrTestBusy) || eff != "log" {
		t.Errorf("second test: %s %v", eff, err)
	}
	close(g.release)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("first test: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first test did not finish")
	}
	// free again, and bounded: a sink that never returns is cut at testLimit
	m.testLimit = 50 * time.Millisecond
	g2 := &gate{Log: alert.Log{Logger: m.logger}, started: make(chan struct{}), release: make(chan struct{})}
	m.sw.Set(g2)
	start := time.Now()
	if _, err := m.Test(); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Errorf("bounded test: %v after %s", err, time.Since(start))
	}
	if rec := m.Recent(0); len(rec) != 2 || rec[0].Kind != "test" {
		t.Errorf("both tests must be in the history: %+v", rec)
	}
}

// TestAlertKindsComplete: every alert kind raised anywhere in the code
// (controller raise, serve's start-up alerts, the onfailure/apt-hook
// paths) has an entry in alertKinds, and alertKinds names nothing else.
func TestAlertKindsComplete(t *testing.T) {
	listed := map[string]bool{}
	for _, k := range alertKinds {
		listed[k.Kind] = true
	}
	raised := map[string]bool{control.AlertConfigChannels: true, "restart": true, "failed": true} // the onfailure unit passes these as argv
	call := regexp.MustCompile(`\b(?:raise|sendAlertCooled|startAlert|sendAlert)\((?:[^,"\n]+,\s*)*"([a-z-]+)"`)
	for _, dir := range []string{".", "../../internal/control"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			src, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range call.FindAllStringSubmatch(string(src), -1) {
				raised[m[1]] = true
			}
		}
	}
	if len(raised) < 10 {
		t.Fatalf("only %d raised kinds found, the scan is broken: %v", len(raised), raised)
	}
	for k := range raised {
		if !listed[k] {
			t.Errorf("kind %q is raised but missing from alertKinds", k)
		}
	}
	for k := range listed {
		if !raised[k] && k != "test" {
			t.Errorf("alertKinds lists %q, which nothing raises", k)
		}
	}
}

// TestAlertStatusCooldownSeconds: the panel gets the cooldown as seconds
// next to the Go duration text.
func TestAlertStatusCooldownSeconds(t *testing.T) {
	m := newAlertManager(filepath.Join(t.TempDir(), "config.toml"), "", config.Alert{Transport: "log", MailTo: "root"}, nil)
	if st := m.Status(); st.Cooldown != "" || st.CooldownS != 0 {
		t.Errorf("offline: %q %d", st.Cooldown, st.CooldownS)
	}
	m.cooldown = func() time.Duration { return 30 * time.Minute }
	if st := m.Status(); st.Cooldown != "30m0s" || st.CooldownS != 1800 {
		t.Errorf("online: %q %d", st.Cooldown, st.CooldownS)
	}
}
