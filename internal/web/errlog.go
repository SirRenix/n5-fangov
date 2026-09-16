package web

import (
	"strings"
	"sync"
	"time"
)

// handshakeFilter is the io.Writer behind http.Server.ErrorLog. Go logs
// every client-side trust rejection ("http: TLS handshake error from
// <addr>: remote error: tls: unknown certificate", EOF, i/o timeout) —
// dozens of lines a day from browsers that have not imported the CA yet.
// Those lines are swallowed, counted per remote IP and summarised at most
// once per handshakeSummaryEvery; everything else passes through to logf.
type handshakeFilter struct {
	mu      sync.Mutex
	logf    func(string, ...any)
	now     func() time.Time
	count   int
	clients map[string]struct{}
	// overflow: clients beyond handshakeClientsMax were seen (counted, not stored).
	overflow bool
	last     time.Time // last summary
}

const handshakeSummaryEvery = 10 * time.Minute

// handshakeClientsMax caps the per-client map between two summaries; a
// flood of rejected handshakes from rotating IPv6 addresses is counted
// beyond that but no longer stored per address.
const handshakeClientsMax = 1024

const handshakeMarker = "TLS handshake error"

func newHandshakeFilter(logf func(string, ...any), now func() time.Time) *handshakeFilter {
	return &handshakeFilter{logf: logf, now: now, clients: map[string]struct{}{}}
}

// Write receives one log line per call (log.Logger writes whole lines).
func (f *handshakeFilter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\r\n")
	if !strings.Contains(line, handshakeMarker) {
		f.logf("%s", line)
		return len(p), nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.count++
	if ip := handshakeIP(line); len(f.clients) < handshakeClientsMax {
		f.clients[ip] = struct{}{}
	} else if _, known := f.clients[ip]; !known {
		f.overflow = true
	}
	if now := f.now(); now.Sub(f.last) >= handshakeSummaryEvery {
		more := ""
		if f.overflow {
			more = " or more"
		}
		f.logf("web: %d TLS handshakes rejected by %d%s client(s) since last summary (certificate not trusted by the browser yet?)", f.count, len(f.clients), more)
		f.count, f.clients, f.last, f.overflow = 0, map[string]struct{}{}, now, false
	}
	return len(p), nil
}

// handshakeIP extracts the client IP from "... error from <ip>:<port>: ...";
// "" when the line has no such part (counts as one anonymous client).
func handshakeIP(line string) string {
	_, rest, ok := strings.Cut(line, handshakeMarker+" from ")
	if !ok {
		return ""
	}
	addr, _, _ := strings.Cut(rest, ": ")
	if i := strings.LastIndex(addr, ":"); i > 0 {
		addr = addr[:i]
	}
	return strings.Trim(addr, "[]")
}
