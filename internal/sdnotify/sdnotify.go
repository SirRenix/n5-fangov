// Package sdnotify implements the sd_notify(3) protocol with a plain unix
// datagram write (no systemd library, DESIGN rule 9). Every function is a
// no-op when NOTIFY_SOCKET is unset, so the daemon behaves identically when
// started by hand.
package sdnotify

import (
	"net"
	"os"
	"strings"
)

// Ready sends READY=1 (Type=notify start-up complete).
func Ready() error { return Send("READY=1") }

// Watchdog sends WATCHDOG=1 (must arrive within WatchdogSec).
func Watchdog() error { return Send("WATCHDOG=1") }

// Stopping sends STOPPING=1.
func Stopping() error { return Send("STOPPING=1") }

// Status sends STATUS=<s> (shown by systemctl status). Newlines are replaced.
func Status(s string) error {
	return Send("STATUS=" + strings.ReplaceAll(strings.TrimSpace(s), "\n", " "))
}

// Enabled reports whether NOTIFY_SOCKET is set.
func Enabled() bool { return os.Getenv("NOTIFY_SOCKET") != "" }

// Send writes one state line to NOTIFY_SOCKET. Abstract sockets ("@name")
// are supported. Returns nil when the variable is unset.
func Send(state string) error {
	sock := os.Getenv("NOTIFY_SOCKET")
	if sock == "" {
		return nil
	}
	if sock[0] == '@' {
		sock = "\x00" + sock[1:]
	}
	addr := &net.UnixAddr{Name: sock, Net: "unixgram"}
	conn, err := net.DialUnix("unixgram", nil, addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if !strings.HasSuffix(state, "\n") {
		state += "\n"
	}
	_, err = conn.Write([]byte(state))
	return err
}
