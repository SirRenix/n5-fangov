// webhook.go holds the Webhook sink (transport "webhook"): one HTTP POST
// per alert to a configured URL, in a JSON document (Gotify, Home
// Assistant) or as plain text (ntfy). Never chosen by "auto".
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SirRenix/n5-fangov/internal/version"
)

// Webhook transport and body format names.
const (
	TransportWebhook = "webhook"
	EffectiveWebhook = "webhook"

	FormatJSON = "json"
	FormatText = "text"
)

// Config is the [alert] section as the sink factory takes it
// (NewForConfig). MailTo "" means root; WebhookFormat "" means json.
type Config struct {
	Transport     string
	MailTo        string
	WebhookURL    string
	WebhookFormat string
}

// webhookHeader names the alert kind on every POST; Title is what ntfy
// shows as the notification title.
const (
	webhookKindHeader = "X-N5-Fangov-Kind"
	webhookTitle      = "Title"
)

// Webhook posts every alert to URL. The client follows no redirect (a
// receiver that answers 3xx is misconfigured, and a redirect could
// carry the body elsewhere) and is bounded by Timeout.
type Webhook struct {
	Logger   Logger
	Hostname string
	URL      string
	// Format is FormatJSON (default) or FormatText.
	Format string
	// Client overrides the HTTP client (tests); nil builds one with
	// Timeout and no redirects.
	Client *http.Client
	// now is the clock behind the JSON "ts" (tests).
	now func() time.Time
}

// Name is EffectiveWebhook.
func (w *Webhook) Name() string { return EffectiveWebhook }

// Alert is Send with the failure logged.
func (w *Webhook) Alert(kind, msg string) {
	if err := w.Send(kind, msg); err != nil {
		w.Logger.Printf("alert: %v", err)
	}
}

// Send posts the alert and returns the failure, if any.
func (w *Webhook) Send(kind, msg string) error { return w.SendCtx(context.Background(), kind, msg) }

// webhookPayload is the JSON body (format json).
type webhookPayload struct {
	Type     string `json:"type"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Hostname string `json:"hostname"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	TS       int64  `json:"ts"`
}

// SendCtx is Send bounded by ctx as well as Timeout. A 2xx answer is
// success; anything else, or a transport error, is "webhook: …" with
// the URL redacted.
func (w *Webhook) SendCtx(ctx context.Context, kind, msg string) error {
	w.Logger.Printf("ALERT[%s]: %s", kind, msg)
	client := w.Client
	if client == nil {
		client = webhookClient()
	}
	now := time.Now
	if w.now != nil {
		now = w.now
	}
	title := fmt.Sprintf("n5-fangov %s on %s", kind, w.Hostname)
	var body []byte
	ctype := "text/plain; charset=utf-8"
	if w.Format == FormatText {
		body = []byte(msg)
	} else {
		var err error
		body, err = json.Marshal(webhookPayload{Type: "n5-fangov", Kind: kind, Severity: "warning", Hostname: w.Hostname, Title: title, Message: msg, TS: now().Unix()})
		if err != nil {
			return fmt.Errorf("webhook: encode: %w", err)
		}
		ctype = "application/json"
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: %s: %w", RedactURL(w.URL), redactErr(err, w.URL))
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("User-Agent", "n5-fangov/"+version.Version)
	req.Header.Set(webhookKindHeader, kind)
	req.Header.Set(webhookTitle, title)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook: %s: %w", RedactURL(w.URL), redactErr(err, w.URL))
	}
	defer resp.Body.Close()
	// Drain a little so the connection can be reused; the answer body is
	// not interpreted.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("webhook: %s: HTTP %s", RedactURL(w.URL), resp.Status)
	}
	return nil
}

// webhookClient is the HTTP client of a Webhook built by NewForConfig:
// system CA pool, Timeout, redirects refused.
func webhookClient() *http.Client {
	return &http.Client{
		Timeout: Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirects are not followed")
		},
	}
}

// redactErr strips the full URL out of an error text (net/http and
// net/url errors repeat it, query included) so a log line never carries
// a key that sits in the query. The chain stays (errors.Is finds the
// context error behind it).
func redactErr(err error, full string) error {
	if err == nil || full == "" {
		return err
	}
	txt := err.Error()
	if !strings.Contains(txt, full) {
		return err
	}
	return &redactedErr{msg: strings.ReplaceAll(txt, full, RedactURL(full)), err: err}
}

// redactedErr is err with a rewritten text.
type redactedErr struct {
	msg string
	err error
}

func (e *redactedErr) Error() string { return e.msg }
func (e *redactedErr) Unwrap() error { return e.err }

// RedactURL renders u without query and userinfo for logs and status
// views (Gotify puts its key into the query). A string that does not
// parse is cut at its first "?" or "#" instead.
func RedactURL(u string) string {
	p, err := url.Parse(u)
	if err != nil {
		if i := strings.IndexAny(u, "?#"); i >= 0 {
			return u[:i]
		}
		return u
	}
	p.RawQuery = ""
	p.ForceQuery = false
	p.Fragment = ""
	p.RawFragment = ""
	p.User = nil
	return p.String()
}
