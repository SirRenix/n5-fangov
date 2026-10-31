package validate

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
)

// HostPlaceholder replaces the host name in every stored text.
const HostPlaceholder = "n5host"

// RedactionRules is the rule set of a result bundle (TESTSPEC 0.4 header and
// §8, #8), recorded in meta.json. Redacted values are never verdict inputs.
var RedactionRules = []string{
	"host name (full and short) -> " + HostPlaceholder,
	"DMI serial numbers and product UUID -> [serial]",
	"MAC addresses -> [mac]",
	"IPv4 addresses -> [ipv4]",
	"IPv6 addresses (with \"::\" or eight groups) -> [ipv6]",
	"mail addresses -> [mail] (systemd instance units such as getty@tty1.service stay)",
	"user names: /home/<user> -> /home/[user]; USER, SUDO_USER, LOGNAME (not root) -> [user]",
	"values of SSH_CONNECTION, SSH_CLIENT, SSH_TTY -> [ssh]",
	"\"serial: X\" style values -> [serial]",
}

var (
	macRe    = regexp.MustCompile(`\b[0-9A-Fa-f]{2}(?:[:-][0-9A-Fa-f]{2}){5}\b`)
	ipv4Re   = regexp.MustCompile(`\b(?:25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])(?:\.(?:25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])){3}\b`)
	ipv6Re   = regexp.MustCompile(`[0-9A-Fa-f]{0,4}(?::[0-9A-Fa-f]{0,4}){2,7}(?:%[0-9A-Za-z_.-]+)?`)
	serialRe = regexp.MustCompile(`(?i)\b(serial(?:[ _-]?(?:number|no\.?))?|s/n|sn)(\s*[:=]\s*)([A-Za-z0-9._-]{4,})`)
	homeRe   = regexp.MustCompile(`/home/[^/\s"']+`)
	mailRe   = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.([A-Za-z]{2,})\b`)
	// systemd instance units (getty@tty1.service) look like mail addresses
	unitSuffix = map[string]bool{"service": true, "timer": true, "socket": true, "mount": true, "target": true,
		"path": true, "slice": true, "scope": true, "device": true, "swap": true, "automount": true}
)

// Redactor removes what identifies a person, a machine or a network from
// the text of a bundle (TESTSPEC 0.4 #8). The zero value applies the
// pattern rules only.
type Redactor struct {
	hosts     []string
	protected []string
	literals  []literal // longest first
}

// NewRedactor redacts the given host names (names shorter than 3
// characters are ignored).
func NewRedactor(hosts ...string) *Redactor {
	seen := map[string]bool{}
	r := &Redactor{}
	for _, h := range hosts {
		for _, n := range []string{h, strings.SplitN(h, ".", 2)[0]} {
			n = strings.TrimSpace(n)
			if len(n) < 3 || seen[n] || n == HostPlaceholder {
				continue
			}
			seen[n] = true
			r.hosts = append(r.hosts, n)
		}
	}
	sort.Slice(r.hosts, func(i, j int) bool { return len(r.hosts[i]) > len(r.hosts[j]) })
	return r
}

// Protect keeps values that may contain the host name as a substring
// unchanged (a Proxmox host named "pve" runs kernel "7.0.14-20-pve").
func (r *Redactor) Protect(vals ...string) {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			r.protected = append(r.protected, v)
		}
	}
	sort.Slice(r.protected, func(i, j int) bool { return len(r.protected[i]) > len(r.protected[j]) })
}

type literal struct {
	value string
	re    *regexp.Regexp
	repl  string
}

// Literal replaces value by placeholder wherever it stands as a whole word
// (serials, user names, SSH_* values). Values shorter than 3 characters are
// ignored.
func (r *Redactor) Literal(value, placeholder string) {
	value = strings.TrimSpace(value)
	if len(value) < 3 {
		return
	}
	r.literals = append(r.literals, literal{value: value, re: regexp.MustCompile(`\b` + regexp.QuoteMeta(value) + `\b`), repl: placeholder})
	sort.Slice(r.literals, func(i, j int) bool { return len(r.literals[i].value) > len(r.literals[j].value) })
}

// String returns s redacted.
func (r *Redactor) String(s string) string {
	if r == nil {
		return s
	}
	for _, l := range r.literals {
		s = l.re.ReplaceAllLiteralString(s, l.repl)
	}
	if len(r.hosts) > 0 {
		// protected values are masked while the host names are replaced
		var masks []string
		for i, p := range r.protected {
			hit := false
			for _, h := range r.hosts {
				hit = hit || strings.Contains(p, h)
			}
			if !hit || !strings.Contains(s, p) {
				continue
			}
			m := fmt.Sprintf("\x00%d\x00", i)
			s = strings.ReplaceAll(s, p, m)
			masks = append(masks, m, p)
		}
		for _, h := range r.hosts {
			s = strings.ReplaceAll(s, h, HostPlaceholder)
		}
		for i := 0; i < len(masks); i += 2 {
			s = strings.ReplaceAll(s, masks[i], masks[i+1])
		}
	}
	s = macRe.ReplaceAllString(s, "[mac]")
	s = ipv6Re.ReplaceAllStringFunc(s, func(m string) string {
		host := m
		if i := strings.IndexByte(host, '%'); i >= 0 {
			host = host[:i]
		}
		// a time (12:34:56) or a hex dump row is not an address
		if net.ParseIP(host) == nil {
			return m
		}
		if !strings.Contains(host, "::") && strings.Count(host, ":") != 7 {
			return m
		}
		return "[ipv6]"
	})
	s = ipv4Re.ReplaceAllString(s, "[ipv4]")
	s = serialRe.ReplaceAllString(s, "${1}${2}[serial]")
	s = homeRe.ReplaceAllString(s, "/home/[user]")
	s = mailRe.ReplaceAllStringFunc(s, func(m string) string {
		if unitSuffix[strings.ToLower(mailRe.FindStringSubmatch(m)[1])] {
			return m
		}
		return "[mail]"
	})
	return s
}
