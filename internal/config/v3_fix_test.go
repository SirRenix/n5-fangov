package config

import (
	"strings"
	"testing"
)

// v0.3.0-beta review fixes: mail_to never starts with "-" (M3), SetKey on
// the dotted layout (L11).

// TestValidMailToNoLeadingDash (R-M3): a recipient that mail(1) would take
// as an option is refused; the leading character is a letter, digit or _.
func TestValidMailToNoLeadingDash(t *testing.T) {
	for _, bad := range []string{"-root", "-Sexpandaddr", "-a@example.test", "--", ".hidden", "%x", "+tag"} {
		if ValidMailTo(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
	for _, ok := range []string{"root", "_svc", "a-b", "x.y-z+tag@mail.example", "9lives"} {
		if !ValidMailTo(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	// the parser falls back to the default and warns
	cfg, warns, _ := Parse([]byte("[alert]\nmail_to = \"-Sfoo\"\n"))
	hasWarn(t, warns, "alert.mail_to")
	if cfg.Alert.MailTo != DefaultMailTo {
		t.Errorf("fallback: %q", cfg.Alert.MailTo)
	}
}

// TestParseDottedTables (R-L11): sections written as top-level dotted keys
// are tables to the parser (toml records no type for an implicit table);
// a scalar under a section name is still refused.
func TestParseDottedTables(t *testing.T) {
	src := "daemon.interval = \"7s\"\nweb.auth = \"basic\"\nweb.user = \"admin\"\nweb.password_hash = \"" + strings.Repeat("0", 64) + "\"\nalert.transport = \"log\"\ndashboard.sensors = [\"k10temp\"]\nlog.max_files = 2\n"
	cfg, warns, err := Parse([]byte(src))
	if err != nil || len(warns) != 0 {
		t.Fatalf("dotted layout: %v %v", err, warns)
	}
	if cfg.Daemon.Interval.String() != "7s" || cfg.Web.Auth != "basic" || cfg.Web.User != "admin" || cfg.Web.PasswordHash != strings.Repeat("0", 64) || cfg.Alert.Transport != "log" || len(cfg.Dashboard.Sensors) != 1 || cfg.Log.MaxFiles != 2 {
		t.Errorf("values: %+v %+v %+v", cfg.Daemon, cfg.Web, cfg.Alert)
	}
	if !HasSecret([]byte(src)) {
		t.Error("HasSecret must see the dotted hash")
	}
	// unknown dotted key → warning like inside a header table
	_, warns, _ = Parse([]byte("web.bogus = 1\n"))
	hasWarn(t, warns, "web.bogus")
	// scalar / array under the section name → not a table
	for _, bad := range []string{"web = 5\n", "web = \"x\"\n", "web = [1]\n"} {
		cfg, warns, err := Parse([]byte(bad))
		if err != nil {
			t.Fatalf("%q: %v", bad, err)
		}
		hasWarn(t, warns, "web")
		if cfg.Web.Auth != Default().Web.Auth || cfg.Web.User != "" {
			t.Errorf("%q: %+v", bad, cfg.Web)
		}
	}
	// inline table parses like a header table
	cfg, warns, err = Parse([]byte("web = { auth = \"basic\", user = \"u\", password_hash = \"" + strings.Repeat("1", 64) + "\" }\n"))
	if err != nil || len(warns) != 0 || cfg.Web.User != "u" {
		t.Errorf("inline: %v %v %+v", err, warns, cfg.Web)
	}
}

// TestSetKeyDotted (R-L11): a file that writes [web] as top-level dotted
// keys is edited in place — replace the key, insert a new key after the
// last section.* line — instead of appending a second [web] table, which
// the parser rejects. Other sections are untouched; the result parses.
func TestSetKeyDotted(t *testing.T) {
	src := "# dotted layout\ndaemon.interval = \"10s\"\nweb.listen = \"127.0.0.1:8010\"\nweb.auth = \"basic\"   # keep\nweb.user = \"admin\"\n\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45,85],[80,255]]\ncritical = 88\n"
	out := string(SetKey([]byte(src), "web", "user", `"ops"`))
	want := strings.Replace(src, `web.user = "admin"`, `web.user = "ops"`, 1)
	if out != want {
		t.Errorf("replace dotted:\n%s", out)
	}
	out = string(SetKey([]byte(out), "web", "password_hash", `"`+strings.Repeat("0", 64)+`"`))
	want = strings.Replace(want, "web.user = \"ops\"\n", "web.user = \"ops\"\nweb.password_hash = \""+strings.Repeat("0", 64)+"\"\n", 1)
	if out != want {
		t.Errorf("insert after the last dotted key:\n%s", out)
	}
	if strings.Count(out, "[web]") != 0 {
		t.Errorf("a [web] header must not be appended:\n%s", out)
	}
	cfg, warns, err := Parse([]byte(out))
	if err != nil || len(warns) != 0 || cfg.Web.User != "ops" || cfg.Web.Auth != "basic" || cfg.Web.Listen != "127.0.0.1:8010" || cfg.Daemon.Interval.String() != "10s" || len(cfg.Channels) != 1 {
		t.Errorf("parse after dotted SetKey: %v %v %+v", err, warns, cfg.Web)
	}
	// a section absent in both forms is still appended as a header table
	out = string(SetKey([]byte(out), "alert", "transport", `"log"`))
	if !strings.HasSuffix(out, "\n[alert]\ntransport = \"log\"\n") {
		t.Errorf("missing section appended:\n%s", out)
	}
	if cfg, _, err := Parse([]byte(out)); err != nil || cfg.Alert.Transport != "log" || cfg.Web.User != "ops" {
		t.Errorf("parse after append: %v %+v", err, cfg.Alert)
	}
	// dotted keys of another section do not attract the insert; the
	// dotted form only counts before the first table header
	out = string(SetKey([]byte("daemon.interval = \"5s\"\n\n[daemon]\nlog_every = 3\n"), "web", "tls", `"auto"`))
	if out != "daemon.interval = \"5s\"\n\n[daemon]\nlog_every = 3\n\n[web]\ntls = \"auto\"\n" {
		t.Errorf("unrelated dotted keys:\n%s", out)
	}
	out = string(SetKey([]byte("[daemon]\nweb.user = \"x\"\n"), "web", "user", `"y"`))
	if out != "[daemon]\nweb.user = \"x\"\n\n[web]\nuser = \"y\"\n" {
		t.Errorf("dotted key inside a table is not top level:\n%s", out)
	}
	// header layout unchanged by the refactor: key with spaces around "=" and a comment
	out = string(SetKey([]byte("[web]\n  user   =\"a\" # c\nuser_x = 1\n"), "web", "user", `"b"`))
	if out != "[web]\nuser = \"b\"\nuser_x = 1\n" {
		t.Errorf("header replace:\n%s", out)
	}
	if assignedKey("# user = 1") != "" || assignedKey("user") != "" || assignedKey(" web.user = \"a=b\" ") != "web.user" {
		t.Error("assignedKey")
	}
}
