package main

import (
	"bytes"
	"log"
	"net"
	"os"
	"strings"
	"testing"
)

// tlsHosts: deterministic, deduplicated, wildcard dropped, ports stripped.
func TestTLSHosts(t *testing.T) {
	hosts := tlsHosts(webSpec{Listen: "192.0.2.10:8010", AllowedHosts: []string{"fans.example:8010", "*", " ", "fans.example"}})
	joined := " " + strings.Join(hosts, " ") + " "
	for _, want := range []string{"192.0.2.10", "fans.example", "localhost", "127.0.0.1"} {
		if !strings.Contains(joined, " "+want+" ") {
			t.Errorf("missing %s in %v", want, hosts)
		}
	}
	if strings.Contains(joined, " * ") || strings.Contains(joined, ":8010") {
		t.Errorf("wildcard or port leaked: %v", hosts)
	}
	seen := map[string]bool{}
	for _, h := range hosts {
		if seen[h] {
			t.Errorf("duplicate %s", h)
		}
		seen[h] = true
	}
	if again := tlsHosts(webSpec{Listen: "192.0.2.10:8010", AllowedHosts: []string{"fans.example:8010", "*", " ", "fans.example"}}); strings.Join(again, ",") != strings.Join(hosts, ",") {
		t.Errorf("not deterministic")
	}
	if hn, _ := os.Hostname(); hn != "" && !seen[hn] {
		t.Errorf("hostname %s missing", hn)
	}
}

// M5/H3: an unspecified listen takes the primary addresses (route-based),
// not every interface; when none can be found a warning is logged and the
// certificate still covers host name + loopback.
func TestTLSHostsUnspecified(t *testing.T) {
	defer func(f func() []string) { primaryIPsFn = f }(primaryIPsFn)
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	primaryIPsFn = func() []string { return []string{"192.0.2.10", "2001:db8::10"} }
	for _, listen := range []string{"0.0.0.0:8010", "[::]:8010", ":8010"} {
		hosts := tlsHosts(webSpec{Listen: listen})
		joined := " " + strings.Join(hosts, " ") + " "
		for _, want := range []string{"192.0.2.10", "2001:db8::10", "localhost", "127.0.0.1"} {
			if !strings.Contains(joined, " "+want+" ") {
				t.Errorf("%s: missing %s in %v", listen, want, hosts)
			}
		}
		if strings.Contains(joined, " 0.0.0.0 ") || strings.Contains(joined, " :: ") {
			t.Errorf("%s: wildcard leaked: %v", listen, hosts)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected log: %s", buf.String())
	}
	// a specific listen host never consults the primary addresses
	primaryIPsFn = func() []string { t.Error("primaryIPs called for a specific host"); return nil }
	tlsHosts(webSpec{Listen: "192.0.2.10:8010"})
	// nothing found: warning, host name + loopback only
	primaryIPsFn = func() []string { return nil }
	hosts := tlsHosts(webSpec{Listen: "0.0.0.0:8010"})
	if !strings.Contains(buf.String(), "no primary IPv4/IPv6 address") {
		t.Errorf("no warning logged: %q", buf.String())
	}
	joined := " " + strings.Join(hosts, " ") + " "
	if !strings.Contains(joined, " localhost ") || !strings.Contains(joined, " 127.0.0.1 ") {
		t.Errorf("loopback missing: %v", hosts)
	}
	// the real resolver: a route may or may not exist on the test host,
	// but it never returns loopback/unspecified addresses
	for _, a := range primaryIPs() {
		if ip := net.ParseIP(a); ip == nil || !ip.IsGlobalUnicast() {
			t.Errorf("primaryIPs returned %q", a)
		}
	}
	if s := routeSource("127.0.0.1:53"); s != "" {
		t.Errorf("loopback route reported as primary: %q", s)
	}
}
