// Package debver compares version strings the way dpkg does
// (dpkg --compare-versions; deb-version(7)): [epoch:]upstream[-revision],
// where "~" sorts before everything, even the end of the string
// (0.2.1+sr1~rc1 < 0.2.1+sr1), letters sort before other characters and
// digit runs compare numerically.
package debver

import "strings"

// Compare returns -1, 0 or +1 for a < b, a == b, a > b.
func Compare(a, b string) int {
	ae, au, ar := split(a)
	be, bu, br := split(b)
	if c := cmpInt(ae, be); c != 0 {
		return c
	}
	if c := verrevcmp(au, bu); c != 0 {
		return sign(c)
	}
	return sign(verrevcmp(ar, br))
}

// Less reports a < b.
func Less(a, b string) bool { return Compare(a, b) < 0 }

// split returns epoch (0 when absent or not numeric), upstream version and
// revision ("" when there is no "-"; the revision is after the LAST "-").
func split(v string) (int, string, string) {
	v = strings.TrimSpace(v)
	epoch := 0
	if i := strings.IndexByte(v, ':'); i >= 0 {
		n, ok := atoi(v[:i])
		if ok {
			epoch = n
		}
		v = v[i+1:]
	}
	rev := ""
	if i := strings.LastIndexByte(v, '-'); i >= 0 {
		v, rev = v[:i], v[i+1:]
	}
	return epoch, v, rev
}

func atoi(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// order is dpkg's character weight; i == len(s) is the end of the string.
func order(s string, i int) int {
	if i >= len(s) {
		return 0
	}
	c := s[i]
	switch {
	case isDigit(c):
		return 0
	case isAlpha(c):
		return int(c)
	case c == '~':
		return -1
	}
	return int(c) + 256
}

// verrevcmp is dpkg's lib/dpkg/version.c verrevcmp.
func verrevcmp(a, b string) int {
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		firstDiff := 0
		for (i < len(a) && !isDigit(a[i])) || (j < len(b) && !isDigit(b[j])) {
			ac, bc := order(a, i), order(b, j)
			if ac != bc {
				return ac - bc
			}
			i++
			j++
		}
		for i < len(a) && a[i] == '0' {
			i++
		}
		for j < len(b) && b[j] == '0' {
			j++
		}
		for i < len(a) && isDigit(a[i]) && j < len(b) && isDigit(b[j]) {
			if firstDiff == 0 {
				firstDiff = int(a[i]) - int(b[j])
			}
			i++
			j++
		}
		if i < len(a) && isDigit(a[i]) {
			return 1
		}
		if j < len(b) && isDigit(b[j]) {
			return -1
		}
		if firstDiff != 0 {
			return firstDiff
		}
	}
	return 0
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

// UpstreamOf strips the Debian revision: "0.2.1+sr1~rc1-1" -> "0.2.1+sr1~rc1".
// An epoch is dropped as well.
func UpstreamOf(v string) string {
	_, u, _ := split(v)
	return u
}
