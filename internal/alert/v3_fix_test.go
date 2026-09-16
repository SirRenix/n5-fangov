package alert

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// v0.3.0-beta review fixes: "--" before the mail recipient (M3), context
// bounded delivery (L9).

// TestMailRecipientAfterDoubleDash (R-M3): the recipient follows "--", so
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

// TestSendCtxBounded (R-L9): a caller's context cuts a hanging delivery
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
