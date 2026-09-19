package alert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type recLogger struct{ lines []string }

func (r *recLogger) Printf(f string, a ...any) { r.lines = append(r.lines, fmt.Sprintf(f, a...)) }

// fakeExe writes a shell script that dumps args, stdin and env to out.
func fakeExe(t *testing.T, name, out string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	p := filepath.Join(t.TempDir(), name)
	script := "#!/bin/sh\n{ echo \"ARGS: $*\"; echo \"STDIN:\"; cat; echo \"ENV:\"; env | grep '^N5FANGOV_' | sort; } > " + out + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLogSink(t *testing.T) {
	l := &recLogger{}
	s := &Log{Logger: l}
	s.Alert("stall", "fan1 stopped")
	if s.Name() != "log" || len(l.lines) != 1 || l.lines[0] != "ALERT[stall] sent via log: fan1 stopped" {
		t.Errorf("log sink: %v", l.lines)
	}
}

func TestPVESink(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	l := &recLogger{}
	s := &PVE{Logger: l, Hostname: "n5host", Perl: fakeExe(t, "perl", out)}
	s.Alert("sensor", "sensor unreadable -> 255")
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	txt := string(got)
	for _, want := range []string{"ARGS: -MPVE::Notify -e ", "N5FANGOV_TEMPLATE=n5-fangov", "N5FANGOV_TITLE=sensor",
		"N5FANGOV_MSG=sensor unreadable -> 255", "N5FANGOV_HOST=n5host", "N5FANGOV_WHEN=20"} {
		if !strings.Contains(txt, want) {
			t.Errorf("missing %q in:\n%s", want, txt)
		}
	}
	if len(l.lines) != 1 || !strings.HasPrefix(l.lines[0], "ALERT[sensor]") {
		t.Errorf("log lines: %v", l.lines)
	}
	if !strings.Contains(perlProgram, "when =>") || strings.Contains(perlProgram, "timestamp") {
		t.Errorf("template must use 'when', not 'timestamp'")
	}
}

func TestPVESinkFailureLogged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	l := &recLogger{}
	s := &PVE{Logger: l, Hostname: "n5host", Perl: "/nonexistent/perl"}
	s.Alert("write", "x")
	if len(l.lines) != 2 || !strings.Contains(l.lines[1], "PVE::Notify failed") {
		t.Errorf("failure must be logged, not fatal: %v", l.lines)
	}
}

func TestMailSink(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	l := &recLogger{}
	s := &Mail{Logger: l, Hostname: "deb", Bin: fakeExe(t, "mail", out)}
	s.Alert("temp", "cpu=90C")
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	txt := string(got)
	for _, want := range []string{"ARGS: -s [deb] n5-fangov: temp -- root", "n5-fangov on deb reports:", "cpu=90C", "Time: "} {
		if !strings.Contains(txt, want) {
			t.Errorf("missing %q in:\n%s", want, txt)
		}
	}
}

func TestMultiAndNew(t *testing.T) {
	l := &recLogger{}
	m := Multi{&Log{Logger: l}, &Log{Logger: l}}
	m.Alert("config", "x")
	if len(l.lines) != 2 || m.Name() != "log+log" {
		t.Errorf("multi: %v %s", l.lines, m.Name())
	}
	// New never returns nil; on a plain box without PVE/mail it is the log sink.
	if s := New(nil); s == nil {
		t.Fatal("New returned nil")
	}
	if _, err := os.Stat(pveNotifyPM); err != nil {
		if s := New(l); s.Name() == "pve-notify" {
			t.Errorf("PVE sink chosen without %s", pveNotifyPM)
		}
	}
}

// Every delivery command carries WaitDelay so a grandchild holding the
// pipes cannot block the alert goroutine after the timeout.
func TestCommandWaitDelay(t *testing.T) {
	cmd := command(context.Background(), "perl", "-e", "1")
	if cmd.WaitDelay != 5*time.Second || WaitDelay != 5*time.Second {
		t.Errorf("WaitDelay = %s", cmd.WaitDelay)
	}
	if len(cmd.Args) != 3 || cmd.Args[1] != "-e" {
		t.Errorf("args: %v", cmd.Args)
	}
}

// Send on the concrete sinks, Off, Swappable and NewFor.

type failSink struct{ Log }

func (f *failSink) Send(kind, msg string) error { return errors.New("delivery broken") }
func (f *failSink) Name() string                { return "fail" }

func TestSendOnSinks(t *testing.T) {
	l := &recLogger{}
	if err := (&Log{Logger: l}).Send("test", "x"); err != nil || len(l.lines) != 1 {
		t.Errorf("log send: %v %v", err, l.lines)
	}
	l = &recLogger{}
	off := &Off{Logger: l}
	off.Alert("stall", "y")
	if err := off.Send("stall", "y"); err != nil || off.Name() != EffectiveOff {
		t.Errorf("off send: %v", err)
	}
	if len(l.lines) != 2 || !strings.Contains(l.lines[0], "suppressed (transport off)") {
		t.Errorf("off must log the suppression: %v", l.lines)
	}
	if runtime.GOOS != "windows" {
		p := &PVE{Logger: &recLogger{}, Hostname: "h", Perl: "/nonexistent/perl"}
		if err := p.Send("write", "x"); err == nil || !strings.Contains(err.Error(), "PVE::Notify failed") {
			t.Errorf("pve send must return the failure: %v", err)
		}
		m := &Mail{Logger: &recLogger{}, Hostname: "h", Bin: "/nonexistent/mail"}
		if err := m.Send("write", "x"); err == nil || !strings.Contains(err.Error(), "mail failed") {
			t.Errorf("mail send must return the failure: %v", err)
		}
	}
}

func TestSwappable(t *testing.T) {
	l1, l2 := &recLogger{}, &recLogger{}
	sw := NewSwappable(&Log{Logger: l1})
	sw.Alert("a", "1")
	if sw.Name() != EffectiveLog || len(l1.lines) != 1 {
		t.Errorf("initial target: %s %v", sw.Name(), l1.lines)
	}
	sw.Set(&Off{Logger: l2})
	sw.Alert("b", "2")
	if err := sw.Send("c", "3"); err != nil {
		t.Errorf("send via off: %v", err)
	}
	if len(l1.lines) != 1 || len(l2.lines) != 2 || sw.Name() != EffectiveOff {
		t.Errorf("after swap: %v %v %s", l1.lines, l2.lines, sw.Name())
	}
	sw.Set(&failSink{})
	if err := sw.Send("d", "4"); err == nil || err.Error() != "delivery broken" {
		t.Errorf("send error must pass through: %v", err)
	}
	var empty Swappable
	empty.Alert("x", "y") // no panic
	if err := empty.Send("x", "y"); err == nil || empty.Name() != "none" {
		t.Errorf("empty swappable: %v %s", err, empty.Name())
	}
	if sw.Get().Name() != "fail" {
		t.Errorf("Get: %s", sw.Get().Name())
	}
}

func TestNewFor(t *testing.T) {
	l := &recLogger{}
	pve, mail := Available()
	for _, tc := range []struct{ transport, wantIfNone string }{
		{TransportOff, EffectiveOff}, {TransportLog, EffectiveLog}, {TransportAuto, EffectiveLog},
		{TransportPVE, EffectiveLog}, {TransportMail, EffectiveLog}, {"garbage", EffectiveLog},
	} {
		s, eff := NewFor(tc.transport, "", l)
		if s == nil || s.Name() != eff {
			t.Errorf("%s: sink %v effective %s", tc.transport, s, eff)
			continue
		}
		if _, ok := s.(Sender); !ok {
			t.Errorf("%s: sink must be a Sender", tc.transport)
		}
		if !pve && !mail && eff != tc.wantIfNone {
			t.Errorf("%s without tools: effective %s, want %s", tc.transport, eff, tc.wantIfNone)
		}
		switch tc.transport {
		case TransportOff:
			if eff != EffectiveOff {
				t.Errorf("off must stay off: %s", eff)
			}
		case TransportLog:
			if eff != EffectiveLog {
				t.Errorf("log must stay log: %s", eff)
			}
		}
	}
	if mail {
		s, eff := NewFor(TransportMail, "ops", l)
		if m, ok := s.(*Mail); !ok || eff != EffectiveMail || m.To != "ops" {
			t.Errorf("mail_to not applied: %v %s", s, eff)
		}
	}
	// New is NewFor(auto)
	if s := New(l); s == nil {
		t.Fatal("New returned nil")
	}
}

// TestMailRecipientAfterDoubleDash: the recipient follows "--", so
// a value starting with "-" reaches mail(1) as an address, not an option.
func TestMailRecipientAfterDoubleDash(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out")
	bin := fakeExe(t, "mail", out)
	for _, c := range []struct{ to, want string }{
		{"", "ARGS: -s [deb] n5-fangov: temp -- root"},
		{"ops@example.test", "ARGS: -s [deb] n5-fangov: temp -- ops@example.test"},
		{"-Sexpandaddr", "ARGS: -s [deb] n5-fangov: temp -- -Sexpandaddr"},
	} {
		s := &Mail{Logger: &recLogger{}, Hostname: "deb", To: c.to, Bin: bin}
		if err := s.Send("temp", "cpu=90C"); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(out)
		if !strings.Contains(string(got), c.want+"\n") {
			t.Errorf("To=%q: missing %q in:\n%s", c.to, c.want, got)
		}
	}
}

// slowExe is a script that sleeps longer than the test's context (exec,
// so the kill reaches the sleeper itself and no grandchild keeps the
// pipes open for WaitDelay).
func slowExe(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	p := filepath.Join(t.TempDir(), "slow")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexec sleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestSendCtxBounded: a caller's context cuts a hanging delivery
// short on both concrete sinks, through the Ring and the Swappable. The
// child is killed at the deadline; a grandchild holding the pipes could
// add at most WaitDelay (5 s), which is why the daemon's test bound (20 s)
// stays under the 30 s write timeout.
func TestSendCtxBounded(t *testing.T) {
	slow := slowExe(t)
	l := &recLogger{}
	for name, s := range map[string]ContextSender{
		"pve":       &PVE{Logger: l, Hostname: "h", Perl: slow},
		"mail":      &Mail{Logger: l, Hostname: "h", Bin: slow},
		"ring":      NewRing(&Mail{Logger: l, Hostname: "h", Bin: slow}, "", l),
		"swappable": NewSwappable(&PVE{Logger: l, Hostname: "h", Perl: slow}),
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		start := time.Now()
		err := s.SendCtx(ctx, "test", "x")
		cancel()
		if err == nil || time.Since(start) > 3*time.Second {
			t.Errorf("%s: err=%v after %s (want a prompt failure)", name, err, time.Since(start))
		}
	}
}

// ctxSink records whether a deadline arrived; a plain Sender does not see one.
type ctxSink struct {
	Log
	deadline bool
}

func (c *ctxSink) SendCtx(ctx context.Context, kind, msg string) error {
	_, c.deadline = ctx.Deadline()
	return c.Send(kind, msg)
}

// TestSendCtxPassThrough: the wrappers hand the context to a ContextSender
// and fall back to Send / Alert for the other sink flavours.
func TestSendCtxPassThrough(t *testing.T) {
	l := &recLogger{}
	cs := &ctxSink{Log: Log{Logger: l}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := NewRing(NewSwappable(cs), "", nil).SendCtx(ctx, "k", "m"); err != nil || !cs.deadline {
		t.Errorf("ring→swappable→ctxSink: %v deadline=%v", err, cs.deadline)
	}
	if err := NewSwappable(&failSink{}).SendCtx(ctx, "k", "m"); err == nil || err.Error() != "delivery broken" {
		t.Errorf("plain Sender through Swappable: %v", err)
	}
	if err := NewRing(Multi{&Log{Logger: l}}, "", nil).SendCtx(ctx, "k", "m"); err != nil {
		t.Errorf("plain Sink through Ring: %v", err)
	}
	if err := NewSwappable(nil).SendCtx(ctx, "k", "m"); err == nil {
		t.Error("empty Swappable must fail")
	}
	if !errors.Is(ErrTestBusy, ErrTestBusy) || ErrTestBusy.Error() == "" {
		t.Error("ErrTestBusy")
	}
}
