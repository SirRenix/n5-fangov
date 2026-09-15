// Package alert delivers daemon alerts. Delivery order (port of
// n5pro-ec/deploy/n5-fand-alert): PVE::Notify via perl when the Proxmox
// notification stack is present, else mail(1) to root, else log only. Every
// sink also logs the alert to stdout (journald). Cooldown per kind is the
// controller's job (internal/control), not this package's.
package alert

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
// than Timeout.
type Sink interface {
	// Alert delivers one alert of the given kind (sensor, stall, temp, write,
	// config, start, service, restart).
	Alert(kind, msg string)
	// Name identifies the transport for the start-up log line.
	Name() string
}

// New picks the best available sink: PVE, mail, or log.
func New(logger Logger) Sink {
	if logger == nil {
		logger = nopLogger{}
	}
	host := hostname()
	if _, err := os.Stat(pveNotifyPM); err == nil {
		if _, err := exec.LookPath("perl"); err == nil {
			return &PVE{Logger: logger, Hostname: host}
		}
	}
	if _, err := exec.LookPath("mail"); err == nil {
		return &Mail{Logger: logger, Hostname: host, To: "root"}
	}
	return &Log{Logger: logger}
}

// Log only writes the alert to the logger.
type Log struct{ Logger Logger }

func (l *Log) Name() string { return "log" }
func (l *Log) Alert(kind, msg string) {
	l.Logger.Printf("ALERT[%s]: %s", kind, msg)
}

// PVE feeds the alert into the Proxmox notification stack as severity
// "warning" with template "n5-fangov" and matcher fields type=n5-fangov.
type PVE struct {
	Logger   Logger
	Hostname string
	// Perl overrides the interpreter path (tests).
	Perl string
}

func (p *PVE) Name() string { return "pve-notify" }

// perlProgram reads its data from the environment so that no user text is
// ever interpolated into Perl source. Note: the template field is "when",
// not "timestamp" (reserved helper name in PVE templates).
const perlProgram = `PVE::Notify::warning($ENV{N5FANGOV_TEMPLATE},
  { title => $ENV{N5FANGOV_TITLE}, message => $ENV{N5FANGOV_MSG},
    hostname => $ENV{N5FANGOV_HOST}, when => $ENV{N5FANGOV_WHEN} },
  { type => "n5-fangov", hostname => $ENV{N5FANGOV_HOST}, kind => $ENV{N5FANGOV_TITLE} });`

func (p *PVE) Alert(kind, msg string) {
	p.Logger.Printf("ALERT[%s]: %s", kind, msg)
	perl := p.Perl
	if perl == "" {
		perl = "perl"
	}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
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
		p.Logger.Printf("alert: PVE::Notify failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

// Mail pipes the alert into mail(1).
type Mail struct {
	Logger   Logger
	Hostname string
	To       string
	// Bin overrides the mail binary (tests).
	Bin string
}

func (m *Mail) Name() string { return "mail" }

func (m *Mail) Alert(kind, msg string) {
	m.Logger.Printf("ALERT[%s]: %s", kind, msg)
	bin := m.Bin
	if bin == "" {
		bin = "mail"
	}
	to := m.To
	if to == "" {
		to = "root"
	}
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	cmd := command(ctx, bin, "-s", fmt.Sprintf("[%s] n5-fangov: %s", m.Hostname, kind), to)
	cmd.Stdin = strings.NewReader(fmt.Sprintf("n5-fangov on %s reports:\n\n%s\n\nTime: %s\n",
		m.Hostname, msg, time.Now().Format("2006-01-02 15:04:05")))
	if out, err := cmd.CombinedOutput(); err != nil {
		m.Logger.Printf("alert: mail failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

// Multi fans out to several sinks (e.g. PVE plus an in-memory ring for the UI).
type Multi []Sink

func (m Multi) Name() string {
	names := make([]string, len(m))
	for i, s := range m {
		names[i] = s.Name()
	}
	return strings.Join(names, "+")
}

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
