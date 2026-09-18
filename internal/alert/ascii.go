package alert

import (
	"strings"
	"unicode"
)

// asciiRunes maps the typographic characters the daemon's texts use to their
// ASCII spelling. Every other rune above 0x7F becomes "?".
var asciiRunes = map[rune]string{
	'\u2014': "-",   // em dash
	'\u2013': "-",   // en dash
	'\u2026': "...", // ellipsis
	'\u00B0': "deg", // degree sign (45 degC)
	'\u00B7': "|",   // middle dot
	'\u2192': "->",  // arrow
	'\u201C': "\"", '\u201D': "\"", '\u2018': "'", '\u2019': "'",
	'\u00A0': " ", // no-break space
	'\u00E4': "ae", '\u00F6': "oe", '\u00FC': "ue", '\u00C4': "Ae", '\u00D6': "Oe", '\u00DC': "Ue", '\u00DF': "ss",
}

// ASCII returns s with every non-ASCII rune replaced (see asciiRunes). Alert
// texts are delivered through paths that carry no charset — PVE::Notify hands
// the body to a mail client that renders an em dash as "â€”" — so every sink
// applies it to kind, title and message before delivery (DESIGN §7).
func ASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < unicode.MaxASCII:
			b.WriteRune(r)
		default:
			if rep, ok := asciiRunes[r]; ok {
				b.WriteString(rep)
			} else {
				b.WriteByte('?')
			}
		}
	}
	return b.String()
}
