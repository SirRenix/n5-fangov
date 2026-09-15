package sdnotify

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestUnset(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "")
	if Enabled() {
		t.Fatal("Enabled with empty NOTIFY_SOCKET")
	}
	if err := Ready(); err != nil {
		t.Errorf("Ready without socket must be a no-op: %v", err)
	}
	if err := Watchdog(); err != nil {
		t.Errorf("Watchdog without socket must be a no-op: %v", err)
	}
}

func TestSend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.sock")
	l, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Skipf("unixgram not available: %v", err)
	}
	defer l.Close()
	t.Setenv("NOTIFY_SOCKET", path)

	recv := func() string {
		t.Helper()
		_ = l.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 256)
		n, err := l.Read(buf)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		return string(buf[:n])
	}
	if err := Ready(); err != nil {
		t.Fatal(err)
	}
	if got := recv(); got != "READY=1\n" {
		t.Errorf("got %q", got)
	}
	if err := Watchdog(); err != nil {
		t.Fatal(err)
	}
	if got := recv(); got != "WATCHDOG=1\n" {
		t.Errorf("got %q", got)
	}
	if err := Status("cpu=36C\nok"); err != nil {
		t.Fatal(err)
	}
	if got := recv(); got != "STATUS=cpu=36C ok\n" {
		t.Errorf("got %q", got)
	}
}

func TestSendDeadSocket(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", filepath.Join(t.TempDir(), "missing.sock"))
	if err := Ready(); err == nil {
		t.Errorf("expected error for missing socket")
	}
}
