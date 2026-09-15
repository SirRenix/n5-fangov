package alert

import (
	"context"
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
	script := "#!/bin/sh\n{ echo \"ARGS: $*\"; echo \"STDIN:\"; cat; echo \"ENV:\"; env | grep '^PVEFAND_' | sort; } > " + out + "\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLogSink(t *testing.T) {
	l := &recLogger{}
	s := &Log{Logger: l}
	s.Alert("stall", "fan1 stopped")
	if s.Name() != "log" || len(l.lines) != 1 || l.lines[0] != "ALERT[stall]: fan1 stopped" {
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
	for _, want := range []string{"ARGS: -MPVE::Notify -e ", "PVEFAND_TEMPLATE=pvefand", "PVEFAND_TITLE=sensor",
		"PVEFAND_MSG=sensor unreadable -> 255", "PVEFAND_HOST=n5host", "PVEFAND_WHEN=20"} {
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
	for _, want := range []string{"ARGS: -s [deb] pvefand: temp root", "pvefand on deb reports:", "cpu=90C", "Time: "} {
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

// L5: every delivery command carries WaitDelay so a grandchild holding the
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
