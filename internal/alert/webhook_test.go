package alert

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/version"
)

// hookServer records every POST it receives and answers with status.
type hookServer struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	reqs   []hookReq
}

type hookReq struct {
	method string
	path   string
	query  string
	hdr    http.Header
	body   string
}

func newHookServer(t *testing.T) *hookServer {
	t.Helper()
	h := &hookServer{status: http.StatusOK}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.reqs = append(h.reqs, hookReq{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery, hdr: r.Header.Clone(), body: string(b)})
		status := h.status
		h.mu.Unlock()
		if status/100 == 3 {
			w.Header().Set("Location", "/elsewhere")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte("answer"))
	}))
	t.Cleanup(h.Close)
	return h
}

func (h *hookServer) last(t *testing.T) hookReq {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.reqs) == 0 {
		t.Fatal("no request received")
	}
	return h.reqs[len(h.reqs)-1]
}

func (h *hookServer) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.reqs)
}

func TestWebhookJSON(t *testing.T) {
	h := newHookServer(t)
	l := &recLogger{}
	s, eff := NewForConfig(Config{Transport: TransportWebhook, WebhookURL: h.URL + "/n5?token=secret-key", WebhookFormat: FormatJSON}, l)
	if eff != EffectiveWebhook || s.Name() != EffectiveWebhook {
		t.Fatalf("effective %s name %s", eff, s.Name())
	}
	w, ok := s.(*Webhook)
	if !ok {
		t.Fatalf("sink is %T", s)
	}
	w.now = func() time.Time { return time.Unix(1789500000, 0) }
	w.Hostname = "n5host"
	if err := w.Send("stall", "hdd stalled at duty 140"); err != nil {
		t.Fatal(err)
	}
	r := h.last(t)
	if r.method != "POST" || r.path != "/n5" || r.query != "token=secret-key" {
		t.Errorf("request: %s %s?%s", r.method, r.path, r.query)
	}
	if ct := r.hdr.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type %q", ct)
	}
	if ua := r.hdr.Get("User-Agent"); ua != "n5-fangov/"+version.Version {
		t.Errorf("user-agent %q", ua)
	}
	if r.hdr.Get("X-N5-Fangov-Kind") != "stall" || r.hdr.Get("Title") != "n5-fangov stall on n5host" {
		t.Errorf("headers: %v", r.hdr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(r.body), &doc); err != nil {
		t.Fatalf("body %q: %v", r.body, err)
	}
	want := map[string]any{"type": "n5-fangov", "kind": "stall", "severity": "warning", "hostname": "n5host", "title": "n5-fangov stall on n5host", "message": "hdd stalled at duty 140", "ts": float64(1789500000)}
	for k, v := range want {
		if doc[k] != v {
			t.Errorf("%s = %v, want %v", k, doc[k], v)
		}
	}
	if len(doc) != len(want) {
		t.Errorf("extra members: %v", doc)
	}
	// Alert is fire-and-forget: it logs the ALERT line and nothing else on success
	w.Alert("temp", "cpu critical")
	if h.count() != 2 {
		t.Errorf("Alert did not post: %d requests", h.count())
	}
	for _, line := range l.lines {
		if strings.Contains(line, "secret-key") {
			t.Errorf("query leaked into the log: %q", line)
		}
	}
}

func TestWebhookText(t *testing.T) {
	h := newHookServer(t)
	w := &Webhook{Logger: &recLogger{}, Hostname: "n5host", URL: h.URL + "/n5", Format: FormatText}
	if err := w.Send("test", "line one\nline two"); err != nil {
		t.Fatal(err)
	}
	r := h.last(t)
	if ct := r.hdr.Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("content-type %q", ct)
	}
	if r.body != "line one\nline two" {
		t.Errorf("body %q", r.body)
	}
	if r.hdr.Get("Title") != "n5-fangov test on n5host" || r.hdr.Get("X-N5-Fangov-Kind") != "test" {
		t.Errorf("headers: %v", r.hdr)
	}
	// an unknown format is json
	s, _ := NewForConfig(Config{Transport: TransportWebhook, WebhookURL: h.URL, WebhookFormat: "xml"}, nil)
	if s.(*Webhook).Format != FormatJSON {
		t.Errorf("format %q", s.(*Webhook).Format)
	}
}

func TestWebhookFailures(t *testing.T) {
	h := newHookServer(t)
	l := &recLogger{}
	w := &Webhook{Logger: l, Hostname: "n5host", URL: h.URL + "/n5?token=secret-key"}
	// non-2xx is a delivery error naming the status, not the query
	h.status = http.StatusForbidden
	err := w.Send("stall", "x")
	if err == nil || !strings.HasPrefix(err.Error(), "webhook: ") || !strings.Contains(err.Error(), "403") {
		t.Errorf("403: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "secret-key") {
		t.Errorf("query in the error: %v", err)
	}
	// redirects are not followed: the 302 is the answer
	h.status = http.StatusFound
	before := h.count()
	err = w.Send("stall", "x")
	if err == nil || !strings.Contains(err.Error(), "webhook: ") {
		t.Errorf("302: %v", err)
	}
	if h.count() != before+1 {
		t.Errorf("redirect followed: %d requests", h.count()-before)
	}
	// Alert logs the failure
	h.status = http.StatusInternalServerError
	w.Alert("stall", "x")
	found := false
	for _, line := range l.lines {
		if strings.HasPrefix(line, "alert: webhook: ") && strings.Contains(line, "500") {
			found = true
		}
		if strings.Contains(line, "secret-key") {
			t.Errorf("query leaked: %q", line)
		}
	}
	if !found {
		t.Errorf("failure not logged: %v", l.lines)
	}
	// unreachable: a closed port answers at once with a transport error
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	w2 := &Webhook{Logger: l, URL: "http://" + addr + "/hook?k=v"}
	err = w2.Send("stall", "x")
	if err == nil || !strings.HasPrefix(err.Error(), "webhook: http://"+addr+"/hook: ") || strings.Contains(err.Error(), "k=v") {
		t.Errorf("unreachable: %v", err)
	}
	// timeout: a server that never answers is cut by the context
	stall := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-stall }))
	defer slow.Close()
	defer close(stall)
	w3 := &Webhook{Logger: l, URL: slow.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = w3.SendCtx(ctx, "stall", "x")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Errorf("timeout: %v after %s", err, time.Since(start))
	}
	if !strings.HasPrefix(err.Error(), "webhook: ") {
		t.Errorf("timeout error text: %v", err)
	}
}

func TestWebhookClientNoRedirect(t *testing.T) {
	c := webhookClient()
	if c.Timeout != Timeout || c.CheckRedirect == nil {
		t.Fatalf("client: %+v", c)
	}
	if err := c.CheckRedirect(nil, nil); err == nil {
		t.Error("redirects must be refused")
	}
}

// TestRedactURL: scheme, host and the first path segment survive; the
// userinfo, the query, the fragment and every deeper path segment (Home
// Assistant's "/api/webhook/<id>") do not — also when the URL does not
// parse and the text is cut by hand.
func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://ntfy.example.test/n5":                        "https://ntfy.example.test/n5",
		"https://gotify.example.test/message?token=abc":       "https://gotify.example.test/message",
		"https://user:pw@h.example.test/hook?x=1#frag":        "https://h.example.test/hook",
		"http://192.0.2.10:8080/api/webhook/n5?key=1&other=2": "http://192.0.2.10:8080/api/…",
		"https://ha.example.test/api/webhook/abcdef0123":      "https://ha.example.test/api/…",
		"https://h.example.test/a/":                           "https://h.example.test/a/…",
		"http://h.example.test/?":                             "http://h.example.test/",
		"http://h.example.test":                               "http://h.example.test",
		"":                                                    "",
		"http://h.example.test/bad\x7f?k=v":                   "http://h.example.test/bad\x7f",
		"http://user:pw@h.example.test/bad\x7f/id?k=v":        "http://h.example.test/bad\x7f/…",
		"http://user:p@w@h.example.test/bad\x7f":              "http://h.example.test/bad\x7f",
		"user:pw@h.example.test/bad\x7f/x":                    "h.example.test/bad\x7f/…",
	} {
		got := RedactURL(in)
		if got != want {
			t.Errorf("RedactURL(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(got, "pw") || strings.Contains(got, "user") {
			t.Errorf("RedactURL(%q) = %q keeps the userinfo", in, got)
		}
	}
}

// TestNewForConfigWebhook: the webhook is never chosen by auto, and an
// empty URL degrades to the log.
func TestNewForConfigWebhook(t *testing.T) {
	if _, eff := NewForConfig(Config{Transport: TransportAuto, WebhookURL: "https://h.example.test/"}, nil); eff == EffectiveWebhook {
		t.Error("auto must not pick the webhook")
	}
	if s, eff := NewForConfig(Config{Transport: TransportWebhook}, nil); eff != EffectiveLog || s.Name() != EffectiveLog {
		t.Errorf("webhook without url: %s %s", eff, s.Name())
	}
	if _, eff := NewFor(TransportWebhook, "", nil); eff != EffectiveLog {
		t.Errorf("NewFor(webhook): %s", eff)
	}
	s, eff := NewForConfig(Config{Transport: TransportWebhook, WebhookURL: "https://h.example.test/", WebhookFormat: FormatText}, nil)
	w, ok := s.(*Webhook)
	if !ok || eff != EffectiveWebhook || w.Format != FormatText || w.Client != nil {
		t.Errorf("webhook: %T %s", s, eff)
	}
	if _, ok := s.(ContextSender); !ok {
		t.Error("Webhook must be a ContextSender")
	}
}
