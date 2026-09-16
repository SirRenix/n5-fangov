package web

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---- error log filter --------------------------------------------------------

// TestHandshakeFilter: handshake-error lines are swallowed and counted (one
// summary per window), foreign lines pass through.
func TestHandshakeFilter(t *testing.T) {
	var logged []string
	now := time.Unix(1789500000, 0)
	f := newHandshakeFilter(func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) }, func() time.Time { return now })
	lg := func(line string) {
		if _, err := f.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	lg("http: TLS handshake error from 203.0.113.7:51234: remote error: tls: unknown certificate")
	if len(logged) != 1 || logged[0] != "web: 1 TLS handshakes rejected by 1 client(s) since last summary (certificate not trusted by the browser yet?)" {
		t.Fatalf("first summary = %v", logged)
	}
	lg("http: TLS handshake error from 203.0.113.7:51235: EOF")
	lg("http: TLS handshake error from [2001:db8::9]:4000: read tcp: i/o timeout")
	lg("http: TLS handshake error from 203.0.113.8:1: EOF")
	if len(logged) != 1 {
		t.Fatalf("summary inside the window: %v", logged)
	}
	lg("http: Accept error: accept tcp: too many open files; retrying in 1s")
	if len(logged) != 2 || logged[1] != "http: Accept error: accept tcp: too many open files; retrying in 1s" {
		t.Fatalf("foreign line: %v", logged)
	}
	now = now.Add(handshakeSummaryEvery)
	lg("http: TLS handshake error from 203.0.113.9:2: EOF")
	if len(logged) != 3 || logged[2] != "web: 4 TLS handshakes rejected by 4 client(s) since last summary (certificate not trusted by the browser yet?)" {
		t.Fatalf("second summary = %v", logged)
	}
	if ip := handshakeIP("http: TLS handshake error from [2001:db8::9]:4000: EOF"); ip != "2001:db8::9" {
		t.Errorf("handshakeIP = %q", ip)
	}
}

// TestHandshakeFilterClientCap: the per-client map stops growing at
// handshakeClientsMax; the count and the summary still reflect the flood.
func TestHandshakeFilterClientCap(t *testing.T) {
	now := time.Unix(1789500000, 0)
	var logged []string
	f := newHandshakeFilter(func(format string, a ...any) { logged = append(logged, fmt.Sprintf(format, a...)) }, func() time.Time { return now })
	f.last = now // no summary during the flood
	for i := 0; i < handshakeClientsMax+50; i++ {
		line := fmt.Sprintf("http: TLS handshake error from [2001:db8::%x]:4000: EOF\n", i+1)
		if _, err := f.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.clients) != handshakeClientsMax || f.count != handshakeClientsMax+50 || !f.overflow {
		t.Fatalf("clients=%d count=%d overflow=%v", len(f.clients), f.count, f.overflow)
	}
	now = now.Add(handshakeSummaryEvery)
	if _, err := f.Write([]byte("http: TLS handshake error from 203.0.113.1:1: EOF\n")); err != nil {
		t.Fatal(err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], fmt.Sprintf("%d TLS handshakes rejected by %d or more client(s)", handshakeClientsMax+51, handshakeClientsMax)) {
		t.Fatalf("summary = %v", logged)
	}
	if len(f.clients) != 0 || f.overflow {
		t.Fatalf("not reset after the summary: clients=%d overflow=%v", len(f.clients), f.overflow)
	}
}
