package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/config"
)

// Without a log file, Clear is "unsupported" (the web layer answers
// 501), not a generic error (500).
func TestJournalLogStoreClearUnsupported(t *testing.T) {
	err := journalLogStore{}.Clear()
	if !errors.Is(err, errors.ErrUnsupported) || !strings.Contains(err.Error(), "journal is not cleared") {
		t.Errorf("Clear: %v", err)
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
