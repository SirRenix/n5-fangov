package main

// Regression tests for the v0.4 audit fixes on the cmd side
// (docs/AUDIT.md): password/user rules of setup and passwd, the legacy
// hash advisory of check, the alert kind list, CLI argument validation,
// journald priority prefixes and absolute path flags.

import (
	"bytes"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/config"
	"github.com/SirRenix/n5-fangov/internal/control"
)

// captureStderr runs f with os.Stderr redirected and returns what was written.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	f()
	os.Stderr = old
	w.Close()
	return <-done
}

// TestPasswdRules: passwd refuses a user outside the API rule and a
// password below the API minimum before touching the file; a valid pair
// is written as PBKDF2 with auth = basic.
func TestPasswdRules(t *testing.T) {
	dir := t.TempDir()
	cfg := writeCfg(t, dir, "[web]\nlisten = \"127.0.0.1:8010\"\nauth = \"none\"\n")
	pwFile := filepath.Join(dir, "pw")
	if err := os.WriteFile(pwFile, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStderr(t, func() {
		if rc := cmdPasswd([]string{"--config", cfg, "--user", "admin", "--password-file", pwFile}); rc != exitUsage {
			t.Errorf("short password: rc=%d", rc)
		}
	})
	if !strings.Contains(out, "8..128") {
		t.Errorf("short password message: %q", out)
	}
	if err := os.WriteFile(pwFile, []byte("longenough\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out = captureStderr(t, func() {
		if rc := cmdPasswd([]string{"--config", cfg, "--user", "ad min", "--password-file", pwFile}); rc != exitUsage {
			t.Errorf("bad user: rc=%d", rc)
		}
	})
	if !strings.Contains(out, "must match") {
		t.Errorf("bad user message: %q", out)
	}
	out = captureStderr(t, func() {
		if rc := cmdPasswd([]string{"--config", cfg, "--user", "admin", "--password", "literalpw"}); rc != exitUsage {
			t.Errorf("literal password: rc=%d", rc)
		}
	})
	if !strings.Contains(out, "--password-file") {
		t.Errorf("literal password message: %q", out)
	}
	raw, _ := os.ReadFile(cfg)
	if strings.Contains(string(raw), "password_hash") {
		t.Fatalf("file touched by refused calls:\n%s", raw)
	}
	if rc := cmdPasswd([]string{"--config", cfg, "--user", "admin", "--password-file", pwFile}); rc != exitOK {
		t.Fatalf("valid: rc=%d", rc)
	}
	raw, _ = os.ReadFile(cfg)
	c, warns := parseConfig(raw)
	w := webOf(c)
	if len(warns) != 0 || w.Auth != "basic" || w.User != "admin" || !strings.HasPrefix(w.PasswordHash, "pbkdf2$") || !verifyPassword("admin", "longenough", w.PasswordHash) {
		t.Fatalf("written: %+v %v\n%s", w, warns, raw)
	}
}

// TestSetupRejectsBadUser: --yes with a LAN listener and a user outside
// the API rule exits with usage before anything is written.
func TestSetupRejectsBadUser(t *testing.T) {
	fakeN5(t, false)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	pwFile := filepath.Join(dir, "pw")
	if err := os.WriteFile(pwFile, []byte("longenough\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := captureStderr(t, func() {
		if rc := cmdSetup([]string{"--config", cfg, "--yes", "--listen", "192.0.2.20:8010", "--user", "bad user", "--password-file", pwFile}); rc != exitUsage {
			t.Errorf("rc=%d", rc)
		}
	})
	if !strings.Contains(out, "must match") {
		t.Errorf("message: %q", out)
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Fatal("config written despite the refused user")
	}
	if err := os.WriteFile(pwFile, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rc := cmdSetup([]string{"--config", cfg, "--yes", "--listen", "192.0.2.20:8010", "--user", "admin", "--password-file", pwFile}); rc != exitUsage {
		t.Errorf("short password: rc=%d", rc)
	}
	if _, err := os.Stat(cfg); err == nil {
		t.Fatal("config written despite the short password")
	}
}

// TestCheckLegacyHashAdvisory: a legacy sha256 hash under auth = basic is
// an advisory line pointing at passwd; a PBKDF2 hash is not.
func TestCheckLegacyHashAdvisory(t *testing.T) {
	fakeN5(t, false)
	dir := t.TempDir()
	legacy := strings.Repeat("ab", 32)
	cfg := writeCfg(t, dir, "[web]\nlisten = \"127.0.0.1:8010\"\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \""+legacy+"\"\n")
	res := runChecks(cfg, dir)
	r, ok := find(res, "web auth")
	if !ok || r.ok || !r.advisory || !strings.Contains(r.detail, "n5-fangov passwd") {
		t.Fatalf("legacy advisory = %+v (found %v)", r, ok)
	}
	if f := fatals(res); len(f) != 0 {
		t.Fatalf("legacy hash must not be fatal: %v", f)
	}
	cfg = writeCfg(t, dir, "[web]\nlisten = \"127.0.0.1:8010\"\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \""+passwordHash("admin", "longenough")+"\"\n")
	if _, ok := find(runChecks(cfg, dir), "web auth"); ok {
		t.Fatal("pbkdf2 hash reported as legacy")
	}
}

// logCapture redirects the standard logger for the duration of f.
func logCapture(f func()) string {
	var buf bytes.Buffer
	old := log.Writer()
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(old); log.SetFlags(flags) }()
	f()
	return buf.String()
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

// TestSetRefusesBadChannelName: a channel name that is not a plain
// identifier never reaches the URL path (the mux would redirect a ".."
// path to another endpoint).
func TestSetRefusesBadChannelName(t *testing.T) {
	for _, name := range []string{"../config", "cpu?x", "", "CPU", strings.Repeat("a", 33)} {
		out := captureStderr(t, func() {
			if rc := cmdSet([]string{name, "30"}); rc != exitUsage {
				t.Errorf("set %q: rc=%d", name, rc)
			}
		})
		if !strings.Contains(out, "must match") {
			t.Errorf("set %q: %q", name, out)
		}
	}
	out := captureStderr(t, func() {
		if rc := cmdAuto([]string{"../config"}); rc != exitUsage {
			t.Errorf("auto: rc=%d", rc)
		}
	})
	if !strings.Contains(out, "must match") {
		t.Errorf("auto: %q", out)
	}
	out = captureStderr(t, func() {
		if rc := cmdLog([]string{"-n", "0"}); rc != exitUsage {
			t.Errorf("log -n 0: rc=%d", rc)
		}
	})
	if !strings.Contains(out, "1..5000") {
		t.Errorf("log -n 0: %q", out)
	}
	if rc := cmdLog([]string{"-n", "6000"}); rc != exitUsage {
		t.Errorf("log -n 6000: rc=%d", rc)
	}
	if rc := cmdTest([]string{"--sample", "0s", "cpu"}); rc != exitUsage {
		t.Errorf("test --sample 0: rc=%d", rc)
	}
	if rc := cmdTest([]string{"--sample", "10s", "--hold", "5s", "cpu"}); rc != exitUsage {
		t.Errorf("test sample > hold: rc=%d", rc)
	}
}

// TestTomlStringEscapes: tomlString renders control characters as TOML
// escapes, so the written file parses again.
func TestTomlStringEscapes(t *testing.T) {
	cases := map[string]string{
		"plain":         `"plain"`,
		"a\"b\\c":       `"a\"b\\c"`,
		"tab\there":     `"tab\there"`,
		"nl\nx":         `"nl\nx"`,
		"ctl\x01x":      `"ctl\u0001x"`,
		"del\x7fx":      `"del\u007Fx"`,
		"umlaut \u00e4": "\"umlaut \u00e4\"",
	}
	for in, want := range cases {
		if got := tomlString(in); got != want {
			t.Errorf("tomlString(%q) = %s, want %s", in, got, want)
		}
		raw := setConfigKey(nil, "web", "user", tomlString(in))
		cfg, _, err := config.Parse(raw)
		if err != nil || cfg.Web.User != in {
			t.Errorf("round trip of %q: %v %q", in, err, cfg.Web.User)
		}
	}
}

// TestRestoreHashOnlyAssignments: the bundle import puts the hash back into
// the password_hash assignment only, not into a comment.
func TestRestoreHashOnlyAssignments(t *testing.T) {
	raw := "# keep <unchanged> here\n[web]\npassword_hash = \"<unchanged>\"\nuser = \"<unchanged>\"\n"
	got := restoreHash(raw, "HASH")
	if strings.Count(got, "HASH") != 1 || !strings.Contains(got, "# keep <unchanged> here") || !strings.Contains(got, `user = "<unchanged>"`) {
		t.Errorf("restoreHash:\n%s", got)
	}
}
