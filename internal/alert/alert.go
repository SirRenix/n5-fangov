// Package alert delivers daemon alerts. Delivery order (port of
// n5pro-ec/deploy/n5-fand-alert): PVE::Notify via perl when the Proxmox
// notification stack is present, else mail(1) to root, else log only; a
// webhook (HTTP POST, webhook.go) is chosen only when configured. Every
// sink also logs the alert to stdout (journald). Cooldown per kind is the
// controller's job (internal/control), not this package's.
package alert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Template is the PVE notification template name; matches
// deploy/pve-notification/n5-fangov-{subject,body}.txt.hbs.
const Template = "n5-fangov"

// Timeout bounds one delivery attempt (perl/mail may hang on a broken MTA).
const Timeout = 30 * time.Second

// WaitDelay is how long Wait keeps waiting for the child's stdout/stderr
// pipes after the timeout killed it. Without it a grandchild that inherited
// the pipes (sendmail spawned by mail) can block CombinedOutput forever.
const WaitDelay = 5 * time.Second

// command builds the delivery command with the timeout and WaitDelay set.
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = WaitDelay
	return cmd
}

// pveNotifyPM is the file whose presence selects the PVE sink.
const pveNotifyPM = "/usr/share/perl5/PVE/Notify.pm"

// Logger is the minimal logging surface (satisfied by *log.Logger).
type Logger interface {
	Printf(format string, args ...any)
}

// Sink delivers alerts. Implementations never panic and never block longer
// than Timeout plus WaitDelay (the child's pipes are given up after that).
type Sink interface {
	// Alert delivers one alert of the given kind (sensor, stall, temp, write,
	// config, config-channels, restart, failed, kernel, tls, test). Fire
	// and forget: a delivery failure is logged, never returned.
	Alert(kind, msg string)
	// Name identifies the transport for the start-up log line.
	Name() string
}

// Sender is a Sink that also reports delivery failures (the alerts panel's
// test button, `n5-fangov alerts test`). Every concrete sink here is one;
// Alert is Send with the error logged.
type Sender interface {
	Sink
	Send(kind, msg string) error
}

// ContextSender is a Sender whose delivery the caller can bound with a
// context: the panel's synchronous test must not outlive the HTTP
// write timeout, while the fire-and-forget path keeps Timeout. PVE and
// Mail (the sinks that run a child) are ones, so are Ring and Swappable
// as wrappers; Send is SendCtx with context.Background().
type ContextSender interface {
	Sender
	SendCtx(ctx context.Context, kind, msg string) error
}

// ErrTestBusy is returned by a test delivery while another one is still
// running; the web layer answers 409.
var ErrTestBusy = errors.New("test alert already in progress")

// sendCtx delivers through s honouring ctx when s can, else however s can.
func sendCtx(ctx context.Context, s Sink, kind, msg string) error {
	switch t := s.(type) {
	case ContextSender:
		return t.SendCtx(ctx, kind, msg)
	case Sender:
		return t.Send(kind, msg)
	}
	s.Alert(kind, msg)
	return nil
}

// Transport names (config [alert].transport) and effective sink names.
const (
	TransportAuto = "auto"
	TransportPVE  = "pve"
	TransportMail = "mail"
	TransportLog  = "log"
	TransportOff  = "off"

	EffectivePVE  = "pve-notify"
	EffectiveMail = "mail"
	EffectiveLog  = "log"
	EffectiveOff  = "off"
)

// New picks the best available sink: PVE, mail, or log (transport "auto").
func New(logger Logger) Sink {
	s, _ := NewFor(TransportAuto, "", logger)
	return s
}

// NewFor builds the sink for a configured transport and returns it with
// the effective transport name (EffectivePVE, EffectiveMail, EffectiveLog,
// EffectiveOff). "auto" takes PVE::Notify when the Proxmox stack and perl
// are present, else mail(1), else the log. A requested transport whose
// tool is missing degrades along the same order (pve → mail → log) — the
// alert still goes somewhere; `n5-fangov check` and the panel say why the
// effective transport differs from the configured one. mailTo "" means
// root. An unknown transport counts as "auto". NewFor is the mail-only
// shorthand of NewForConfig: "webhook" without a URL degrades to the log.
func NewFor(transport, mailTo string, logger Logger) (Sink, string) {
	return NewForConfig(Config{Transport: transport, MailTo: mailTo}, logger)
}

// NewForConfig is NewFor for the whole [alert] section. "webhook" posts
// to c.WebhookURL (EffectiveWebhook) and is never chosen by "auto"; with
// an empty URL it degrades to the log (the config parser already turned
// that case into "auto" with a warning, so this is the last line).
func NewForConfig(c Config, logger Logger) (Sink, string) {
	transport, mailTo := c.Transport, c.MailTo
	if logger == nil {
		logger = nopLogger{}
	}
	if mailTo == "" {
		mailTo = "root"
	}
	host := hostname()
	pve, mail := Available()
	switch transport {
	case TransportOff:
		return &Off{Logger: logger}, EffectiveOff
	case TransportLog:
		return &Log{Logger: logger}, EffectiveLog
	case TransportWebhook:
		if c.WebhookURL == "" {
			return &Log{Logger: logger}, EffectiveLog
		}
		format := c.WebhookFormat
		if format != FormatText {
			format = FormatJSON
		}
		return &Webhook{Logger: logger, Hostname: host, URL: c.WebhookURL, Format: format}, EffectiveWebhook
	case TransportPVE:
		if pve {
			return &PVE{Logger: logger, Hostname: host}, EffectivePVE
		}
	case TransportMail:
		if mail {
			return &Mail{Logger: logger, Hostname: host, To: mailTo}, EffectiveMail
		}
		if pve {
			return &PVE{Logger: logger, Hostname: host}, EffectivePVE
		}
		return &Log{Logger: logger}, EffectiveLog
	}
	// auto, or pve without the stack
	if pve {
		return &PVE{Logger: logger, Hostname: host}, EffectivePVE
	}
	if mail {
		return &Mail{Logger: logger, Hostname: host, To: mailTo}, EffectiveMail
	}
	return &Log{Logger: logger}, EffectiveLog
}

// Available reports which delivery tools this box has: PVE::Notify with a
// perl interpreter, and a mail(1) binary.
func Available() (pve, mail bool) {
	if _, err := os.Stat(pveNotifyPM); err == nil {
		if _, err := exec.LookPath("perl"); err == nil {
			pve = true
		}
	}
	if _, err := exec.LookPath("mail"); err == nil {
		mail = true
	}
	return pve, mail
}

// Log only writes the alert to the logger.
type Log struct{ Logger Logger }

// Name is EffectiveLog.
func (l *Log) Name() string { return EffectiveLog }

// Alert writes the alert line to the logger.
func (l *Log) Alert(kind, msg string) {
	l.Logger.Printf("ALERT[%s]: %s", kind, msg)
}

// Send is Alert; the log never fails (no SendCtx: nothing to bound).
func (l *Log) Send(kind, msg string) error {
	l.Alert(kind, msg)
	return nil
}

// Off drops every alert (transport "off"); the journal line still says
// that one was suppressed, so the history is not silently empty.
type Off struct{ Logger Logger }

// Name is EffectiveOff.
func (o *Off) Name() string { return EffectiveOff }

// Alert drops the alert and logs that it did.
func (o *Off) Alert(kind, msg string) {
	o.Logger.Printf("ALERT[%s] suppressed (transport off): %s", kind, msg)
}

// Send is Alert; dropping never fails.
func (o *Off) Send(kind, msg string) error {
	o.Alert(kind, msg)
	return nil
}

// Swappable is a Sink whose target can be replaced at runtime (the alerts
// panel's Configure and a reloaded [alert] section): the controller holds
// the Swappable, cmd swaps what is behind it. The zero value delivers to
// nothing until Set; New… never hands one out like that.
type Swappable struct {
	mu   sync.RWMutex
	sink Sink
}

// NewSwappable wraps sink.
func NewSwappable(sink Sink) *Swappable { return &Swappable{sink: sink} }

// Set replaces the target; alerts already in flight finish on the old one.
func (s *Swappable) Set(sink Sink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sink = sink
}

// Get returns the current target (nil before the first Set).
func (s *Swappable) Get() Sink {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sink
}

// Name is the target's name, "none" before the first Set.
func (s *Swappable) Name() string {
	if t := s.Get(); t != nil {
		return t.Name()
	}
	return "none"
}

// Alert delivers through the current target (nothing before the first Set).
func (s *Swappable) Alert(kind, msg string) {
	if t := s.Get(); t != nil {
		t.Alert(kind, msg)
	}
}

// Send delivers through the target and returns its error; a target that
// is only a Sink delivers fire-and-forget and reports nil.
func (s *Swappable) Send(kind, msg string) error { return s.SendCtx(context.Background(), kind, msg) }

// SendCtx is Send bounded by ctx where the target supports it.
func (s *Swappable) SendCtx(ctx context.Context, kind, msg string) error {
	t := s.Get()
	if t == nil {
		return errors.New("no alert sink configured")
	}
	return sendCtx(ctx, t, kind, msg)
}

// PVE feeds the alert into the Proxmox notification stack as severity
// "warning" with template "n5-fangov" and matcher fields type=n5-fangov.
type PVE struct {
	Logger   Logger
	Hostname string
	// Perl overrides the interpreter path (tests).
	Perl string
}

// Name is EffectivePVE.
func (p *PVE) Name() string { return EffectivePVE }

// perlProgram reads its data from the environment so that no user text is
// ever interpolated into Perl source. Note: the template field is "when",
// not "timestamp" (reserved helper name in PVE templates).
const perlProgram = `PVE::Notify::warning($ENV{N5FANGOV_TEMPLATE},
  { title => $ENV{N5FANGOV_TITLE}, message => $ENV{N5FANGOV_MSG},
    hostname => $ENV{N5FANGOV_HOST}, when => $ENV{N5FANGOV_WHEN} },
  { type => "n5-fangov", hostname => $ENV{N5FANGOV_HOST}, kind => $ENV{N5FANGOV_TITLE} });`

// Alert is Send with the failure logged.
func (p *PVE) Alert(kind, msg string) {
	if err := p.Send(kind, msg); err != nil {
		p.Logger.Printf("alert: %v", err)
	}
}

// Send delivers through PVE::Notify and returns the failure, if any.
func (p *PVE) Send(kind, msg string) error { return p.SendCtx(context.Background(), kind, msg) }

// SendCtx is Send bounded by ctx as well as Timeout.
func (p *PVE) SendCtx(ctx context.Context, kind, msg string) error {
	p.Logger.Printf("ALERT[%s]: %s", kind, msg)
	perl := p.Perl
	if perl == "" {
		perl = "perl"
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := command(ctx, perl, "-MPVE::Notify", "-e", perlProgram)
	cmd.Env = append(os.Environ(),
		"N5FANGOV_TEMPLATE="+Template,
		"N5FANGOV_TITLE="+kind,
		"N5FANGOV_MSG="+msg,
		"N5FANGOV_HOST="+p.Hostname,
		"N5FANGOV_WHEN="+time.Now().Format("2006-01-02 15:04:05"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("PVE::Notify failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Mail pipes the alert into mail(1).
type Mail struct {
	Logger   Logger
	Hostname string
	To       string
	// Bin overrides the mail binary (tests).
	Bin string
}

// Name is EffectiveMail.
func (m *Mail) Name() string { return EffectiveMail }

// Alert is Send with the failure logged.
func (m *Mail) Alert(kind, msg string) {
	if err := m.Send(kind, msg); err != nil {
		m.Logger.Printf("alert: %v", err)
	}
}

// Send pipes the alert into mail(1) and returns the failure, if any.
func (m *Mail) Send(kind, msg string) error { return m.SendCtx(context.Background(), kind, msg) }

// SendCtx is Send bounded by ctx as well as Timeout.
func (m *Mail) SendCtx(ctx context.Context, kind, msg string) error {
	m.Logger.Printf("ALERT[%s]: %s", kind, msg)
	bin := m.Bin
	if bin == "" {
		bin = "mail"
	}
	to := m.To
	if to == "" {
		to = "root"
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	// "--" ends option parsing, so a recipient that starts with "-"
	// can never become a mail(1) option (config.ValidMailTo refuses such
	// values too; this is the second line).
	cmd := command(ctx, bin, "-s", fmt.Sprintf("[%s] n5-fangov: %s", m.Hostname, kind), "--", to)
	cmd.Stdin = strings.NewReader(fmt.Sprintf("n5-fangov on %s reports:\n\n%s\n\nTime: %s\n",
		m.Hostname, msg, time.Now().Format("2006-01-02 15:04:05")))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mail failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Multi fans out to several sinks (e.g. PVE plus an in-memory ring for the UI).
type Multi []Sink

// Name joins the member names with "+".
func (m Multi) Name() string {
	names := make([]string, len(m))
	for i, s := range m {
		names[i] = s.Name()
	}
	return strings.Join(names, "+")
}

// Alert delivers to every member in order.
func (m Multi) Alert(kind, msg string) {
	for _, s := range m {
		s.Alert(kind, msg)
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "host"
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}

type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}
