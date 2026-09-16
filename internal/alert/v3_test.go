package alert

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// v0.3: Send on the concrete sinks, Off, Swappable, NewFor, the template
// helpers and the Ring.

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

func TestTemplateFiles(t *testing.T) {
	files := TemplateFiles()
	if len(files) != 2 {
		t.Fatalf("embedded templates: %v", files)
	}
	for _, name := range []string{"n5-fangov-subject.txt.hbs", "n5-fangov-body.txt.hbs"} {
		if len(files[name]) == 0 {
			t.Errorf("%s missing or empty", name)
		}
	}
	// the deploy copies must be identical (install.sh uses them)
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join("..", "..", "deploy", "pve-notification", name))
		if err != nil {
			t.Errorf("deploy copy: %v", err)
			continue
		}
		if string(got) != string(want) {
			t.Errorf("deploy/pve-notification/%s differs from the embedded copy", name)
		}
	}
	if !strings.Contains(string(files["n5-fangov-body.txt.hbs"]), "{{when}}") {
		t.Errorf("body template must use {{when}} (see perlProgram)")
	}
}

func TestTemplateStatusAndInstall(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes / rename semantics")
	}
	root := t.TempDir()
	notify := filepath.Join(root, "Notify.pm")
	dir := filepath.Join(root, "etc", "pve", "templates")

	// no PVE: unsupported, status says so
	if _, err := installTemplateIn(dir, notify); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("without Notify.pm: %v", err)
	}
	inst, cur, wr, reason := templateStatusIn(dir, notify)
	if inst || cur || wr || !strings.Contains(reason, "PVE::Notify absent") {
		t.Errorf("status without PVE: %v %v %v %q", inst, cur, wr, reason)
	}
	_ = os.WriteFile(notify, []byte("package PVE::Notify;\n"), 0o644)

	// PVE, directory missing: not writable (the daemon cannot create it), install creates it
	_, _, wr, reason = templateStatusIn(dir, notify)
	if wr || !strings.Contains(reason, "does not exist") {
		t.Errorf("missing dir: %v %q", wr, reason)
	}
	got, err := installTemplateIn(dir, notify)
	if err != nil || got != dir {
		t.Fatalf("install: %v %s", err, got)
	}
	inst, cur, wr, reason = templateStatusIn(dir, notify)
	if !inst || !cur || !wr || reason != "" {
		t.Errorf("after install: %v %v %v %q", inst, cur, wr, reason)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("temp files left behind: %v", entries)
	}
	st, _ := os.Stat(filepath.Join(dir, "n5-fangov-body.txt.hbs"))
	if st.Mode().Perm() != 0o640 {
		t.Errorf("mode %04o, want 0640", st.Mode().Perm())
	}

	// stale copy: installed but not current
	_ = os.WriteFile(filepath.Join(dir, "n5-fangov-body.txt.hbs"), []byte("old"), 0o640)
	inst, cur, _, _ = templateStatusIn(dir, notify)
	if !inst || cur {
		t.Errorf("stale: installed=%v current=%v", inst, cur)
	}
	if _, err := installTemplateIn(dir, notify); err != nil {
		t.Fatal(err)
	}
	if _, cur, _, _ = templateStatusIn(dir, notify); !cur {
		t.Errorf("reinstall must make it current")
	}

	// read-only directory: not writable, reason names it
	if os.Getuid() != 0 {
		_ = os.Chmod(dir, 0o500)
		defer os.Chmod(dir, 0o700)
		if _, _, wr, reason := templateStatusIn(dir, notify); wr || !strings.Contains(reason, "not writable") {
			t.Errorf("read-only dir: %v %q", wr, reason)
		}
	}
}

func TestRing(t *testing.T) {
	l := &recLogger{}
	path := filepath.Join(t.TempDir(), "alerts.json")
	inner := &recLogger{}
	r := NewRing(&Log{Logger: inner}, path, l)
	clock := time.Unix(1_789_500_000, 0)
	r.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	if r.Name() != EffectiveLog || len(r.Recent(10)) != 0 {
		t.Errorf("fresh ring: %s %v", r.Name(), r.Recent(10))
	}
	r.Alert("stall", "one")
	if err := r.Send("test", "two"); err != nil {
		t.Errorf("send: %v", err)
	}
	got := r.Recent(10)
	if len(got) != 2 || got[0].Kind != "test" || got[1].Kind != "stall" || got[0].TS <= got[1].TS {
		t.Errorf("newest first: %+v", got)
	}
	if len(inner.lines) != 2 {
		t.Errorf("delivery through the wrapped sink: %v", inner.lines)
	}
	if last := r.Last(); last["test"] != got[0].TS || last["stall"] != got[1].TS {
		t.Errorf("Last: %v", last)
	}
	if n := len(r.Recent(1)); n != 1 {
		t.Errorf("Recent(1): %d", n)
	}
	// mirror file: 0600, reloaded by a new ring
	if runtime.GOOS != "windows" {
		if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("mirror: %v %v", err, st)
		}
	}
	r2 := NewRing(&Log{Logger: &recLogger{}}, path, l)
	if again := r2.Recent(0); len(again) != 2 || again[0].Msg != "two" {
		t.Errorf("reload: %+v", again)
	}
	// overflow keeps the newest RingSize
	for i := 0; i < RingSize+5; i++ {
		r.Alert("temp", "n")
	}
	if all := r.Recent(0); len(all) != RingSize || all[len(all)-1].Kind != "temp" {
		t.Errorf("overflow: %d oldest=%s", len(all), all[len(all)-1].Kind)
	}
	// delivery error passes through; the record is kept anyway
	r3 := NewRing(&failSink{}, "", nil)
	if err := r3.Send("test", "x"); err == nil || len(r3.Recent(0)) != 1 {
		t.Errorf("failing sink: %v %v", err, r3.Recent(0))
	}
	// unwritable mirror: logged once, memory continues
	if runtime.GOOS != "windows" {
		r4 := NewRing(&Log{Logger: &recLogger{}}, filepath.Join(t.TempDir(), "missing", "alerts.json"), l)
		r4.Alert("a", "1")
		r4.Alert("a", "2")
		n := 0
		for _, ln := range l.lines {
			if strings.Contains(ln, "history kept in memory only") {
				n++
			}
		}
		if n != 1 || len(r4.Recent(0)) != 2 {
			t.Errorf("unwritable mirror: warned %d times, %d records", n, len(r4.Recent(0)))
		}
	}
}
