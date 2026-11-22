package cfgver

import (
	"regexp"
	"strings"
)

// Op is one line of a diff: " " kept, "-" only in a, "+" only in b.
type Op struct {
	Op   string `json:"op"`
	Line string `json:"line"`
}

// Diff is a line diff of a → b by the longest common subsequence (the
// files are a few KiB; no library). A trailing newline does not count as a
// line.
func Diff(a, b string) []Op {
	x, y := lines(a), lines(b)
	n, m := len(x), len(y)
	l := make([][]int, n+1)
	for i := range l {
		l[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				l[i][j] = l[i+1][j+1] + 1
			} else {
				l[i][j] = max(l[i+1][j], l[i][j+1])
			}
		}
	}
	out := make([]Op, 0, n+m)
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && x[i] == y[j]:
			out = append(out, Op{" ", x[i]})
			i++
			j++
		case i < n && (j == m || l[i+1][j] >= l[i][j+1]):
			out = append(out, Op{"-", x[i]})
			i++
		default:
			out = append(out, Op{"+", y[j]})
			j++
		}
	}
	return out
}

func lines(s string) []string {
	s = strings.TrimSuffix(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// Count returns the removed and added lines of d.
func Count(d []Op) (removed, added int) {
	for _, o := range d {
		switch o.Op {
		case "-":
			removed++
		case "+":
			added++
		}
	}
	return removed, added
}

var (
	header = regexp.MustCompile(`^\s*\[\[?\s*([A-Za-z0-9_.\-]+)\s*\]\]?\s*(#.*)?$`)
	ident  = regexp.MustCompile(`^\s*(name|channel|preset)\s*=\s*"([^"]*)"`)
)

// Sections names the sections whose lines differ between a and b, in the
// order of the diff: "[alert]", "[[channel]] hdd", "[[assist]] hdd"
// ([[assist.source]] counts to its [[assist]]), "[[schedule]] quiet";
// lines above the first header are "(top)". Blank and comment lines do
// not count.
func Sections(a, b string) []string {
	x, y := lines(a), lines(b)
	lx, ly := labels(x), labels(y)
	var out []string
	seen := map[string]bool{}
	i, j := 0, 0
	for _, o := range Diff(a, b) {
		var sec, t string
		switch o.Op {
		case " ":
			i++
			j++
			continue
		case "-":
			sec, t = lx[i], x[i]
			i++
		default:
			sec, t = ly[j], y[j]
			j++
		}
		if t = strings.TrimSpace(t); t == "" || strings.HasPrefix(t, "#") || seen[sec] {
			continue
		}
		seen[sec] = true
		out = append(out, sec)
	}
	return out
}

// labels gives every line the section it belongs to; an array table is
// named by its name / channel / preset key.
func labels(ls []string) []string {
	out := make([]string, len(ls))
	cur := "(top)"
	for i, l := range ls {
		if m := header.FindStringSubmatch(l); m != nil && m[1] != "assist.source" {
			if strings.HasPrefix(strings.TrimSpace(l), "[[") {
				cur = "[[" + m[1] + "]]"
				for _, n := range ls[i+1:] {
					if header.MatchString(n) {
						break
					}
					if id := ident.FindStringSubmatch(n); id != nil {
						cur += " " + id[2]
						break
					}
				}
			} else {
				cur = "[" + m[1] + "]"
			}
		}
		out[i] = cur
	}
	return out
}
