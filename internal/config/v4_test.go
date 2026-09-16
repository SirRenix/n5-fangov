package config

// Regression tests for the v0.4 audit fixes in the parser: enumeration
// keys are normalised, the listen port is checked, channel names are
// bounded, a missing pwm is one warning, the user name follows the API
// rule, behind_tls_proxy parses.

import (
	"strings"
	"testing"
)

func warnFields(warns []Warning) string {
	var out []string
	for _, w := range warns {
		out = append(out, w.Field)
	}
	return strings.Join(out, ",")
}

// TestEnumKeysNormalised: blanks and letter case around profile, auth,
// tls, transport and stop do not turn a valid value into "unknown".
func TestEnumKeysNormalised(t *testing.T) {
	src := "[daemon]\nprofile = \" N5Pro \"\n[web]\nlisten = \"192.0.2.20:8010\"\nauth = \"Basic\"\nuser = \"admin\"\npassword_hash = \"" + strings.Repeat("ab", 32) + "\"\ntls = \" AUTO\"\n[alert]\ntransport = \"LOG \"\n[[channel]]\nname = \"cpu\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45, 85], [80, 255]]\ncritical = 88\nstop = \"Auto \"\n"
	cfg, warns, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	if cfg.Daemon.Profile != "n5pro" || cfg.Web.Auth != "basic" || cfg.Web.TLS != "auto" || cfg.Alert.Transport != "log" || cfg.Channels[0].Stop != "auto" {
		t.Errorf("normalised: %+v %+v %+v %+v", cfg.Daemon.Profile, cfg.Web, cfg.Alert, cfg.Channels[0].Stop)
	}
}

// TestListenPortChecked: a listen value whose port is not a number in
// 1..65535 falls back to the default with one warning.
func TestListenPortChecked(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:abc", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:"} {
		cfg, warns, err := Parse([]byte("[web]\nlisten = \"" + listen + "\"\n"))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Web.Listen != Default().Web.Listen || len(warns) != 1 || warns[0].Field != "web.listen" {
			t.Errorf("%q: listen=%q warns=%v", listen, cfg.Web.Listen, warns)
		}
	}
	cfg, warns, _ := Parse([]byte("[web]\nlisten = \"[::1]:65535\"\n"))
	if cfg.Web.Listen != "[::1]:65535" || len(warns) != 0 {
		t.Errorf("valid port: %q %v", cfg.Web.Listen, warns)
	}
}

// TestChannelNameLength: 32 characters pass, 33 drop the channel — the
// same bound the override API applies.
func TestChannelNameLength(t *testing.T) {
	mk := func(name string) string {
		return "[[channel]]\nname = \"" + name + "\"\npwm = 1\nsensor = \"k10temp\"\ncurve = [[45, 85], [80, 255]]\ncritical = 88\n"
	}
	if cfg, _, _ := Parse([]byte(mk(strings.Repeat("a", 32)))); len(cfg.Channels) != 1 {
		t.Errorf("32-character name dropped")
	}
	cfg, warns, _ := Parse([]byte(mk(strings.Repeat("a", 33))))
	if len(cfg.Channels) != 0 || len(warns) != 1 || !strings.Contains(warns[0].Msg, "{1,32}") {
		t.Errorf("33-character name: %d channels, %v", len(cfg.Channels), warns)
	}
}

// TestPwmOneWarning: a missing, non-integer or out-of-range pwm yields
// exactly one warning that says the channel is dropped.
func TestPwmOneWarning(t *testing.T) {
	for _, pwm := range []string{"", "pwm = 0\n", "pwm = 9\n", "pwm = \"one\"\n"} {
		src := "[[channel]]\nname = \"cpu\"\n" + pwm + "sensor = \"k10temp\"\ncurve = [[45, 85], [80, 255]]\ncritical = 88\n"
		cfg, warns, err := Parse([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Channels) != 0 || len(warns) != 1 || warns[0].Field != "channel.cpu.pwm" || !strings.Contains(warns[0].Msg, "channel dropped") {
			t.Errorf("%q: %d channels, warnings %s / %v", pwm, len(cfg.Channels), warnFields(warns), warns)
		}
	}
}

// TestUserNameRule: a user outside the API rule with auth = basic drops
// to auth = none (fail closed: loopback) with a warning naming the rule.
func TestUserNameRule(t *testing.T) {
	hash := strings.Repeat("ab", 32)
	for _, user := range []string{" admin", "ad min", "über", strings.Repeat("a", 33)} {
		cfg, warns, _ := Parse([]byte("[web]\nlisten = \"192.0.2.20:8010\"\nauth = \"basic\"\nuser = \"" + user + "\"\npassword_hash = \"" + hash + "\"\n"))
		if cfg.Web.Auth != "none" || cfg.Web.Listen != Default().Web.Listen || !strings.Contains(warnFields(warns), "web.user") {
			t.Errorf("%q: auth=%s listen=%s warns=%v", user, cfg.Web.Auth, cfg.Web.Listen, warns)
		}
	}
	cfg, warns, _ := Parse([]byte("[web]\nlisten = \"192.0.2.20:8010\"\nauth = \"basic\"\nuser = \"root.ops-1\"\npassword_hash = \"" + hash + "\"\n"))
	if cfg.Web.Auth != "basic" || len(warns) != 0 {
		t.Errorf("valid user: auth=%s warns=%v", cfg.Web.Auth, warns)
	}
	if !UserRe.MatchString("Admin_1.x-y") || UserRe.MatchString("") || MinPasswordLen != 8 || MaxPasswordLen != 128 {
		t.Error("shared rules")
	}
}

// TestBehindTLSProxy: the key parses, defaults to false, and a non-boolean
// value is one warning.
func TestBehindTLSProxy(t *testing.T) {
	cfg, warns, _ := Parse([]byte("[web]\nbehind_tls_proxy = true\n"))
	if !cfg.Web.BehindTLSProxy || len(warns) != 0 {
		t.Errorf("true: %v %v", cfg.Web.BehindTLSProxy, warns)
	}
	cfg, warns, _ = Parse([]byte("[web]\nlisten = \"127.0.0.1:8010\"\n"))
	if cfg.Web.BehindTLSProxy || len(warns) != 0 {
		t.Errorf("default: %v %v", cfg.Web.BehindTLSProxy, warns)
	}
	cfg, warns, _ = Parse([]byte("[web]\nbehind_tls_proxy = \"yes\"\n"))
	if cfg.Web.BehindTLSProxy || len(warns) != 1 || warns[0].Field != "web.behind_tls_proxy" {
		t.Errorf("string: %v %v", cfg.Web.BehindTLSProxy, warns)
	}
	// round trip through Marshal
	c := Default()
	c.Web.BehindTLSProxy = true
	back, warns, err := Parse(Marshal(c))
	if err != nil || len(warns) != 0 || !back.Web.BehindTLSProxy {
		t.Errorf("round trip: %v %v %v", err, warns, back.Web.BehindTLSProxy)
	}
}
