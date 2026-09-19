package alert

import (
	"fmt"
	"testing"
)

func TestASCII(t *testing.T) {
	cases := map[string]string{
		"plain":                       "plain",
		"a — b – c … 45 °C":           "a - b - c ... 45 degC",
		"“quoted” ‘single’ x → y · z": "\"quoted\" 'single' x -> y | z",
		"Lüfter ÄÖÜ ß":                "Luefter AeOeUe ss",
		"snow ☃ man":                  "snow ? man",
	}
	for in, want := range cases {
		if got := ASCII(in); got != want {
			t.Errorf("ASCII(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAlertTextsASCII pins the contract of DESIGN §7: whatever a sink is handed,
// what it logs and delivers is ASCII.
func TestAlertTextsASCII(t *testing.T) {
	l := &captureLogger{}
	s := &Log{Logger: l}
	s.Alert("test", "dash — here")
	if got := l.last; got != "ALERT[test] sent via log: dash - here" {
		t.Fatalf("log sink delivered %q", got)
	}
}

type captureLogger struct{ last string }

func (c *captureLogger) Printf(format string, args ...any) { c.last = fmt.Sprintf(format, args...) }
