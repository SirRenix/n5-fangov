package main

import (
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
)

// dialErr builds the error chain http.Client.Do returns when the unix socket
// dial fails: *url.Error -> *net.OpError -> *os.SyscallError -> syscall.Errno.
func dialErr(errno syscall.Errno) error {
	return &url.Error{
		Op:  "Get",
		URL: "http://n5-fangov/api/state",
		Err: &net.OpError{Op: "dial", Net: "unix", Err: &os.SyscallError{Syscall: "connect", Err: errno}},
	}
}

func TestClassifyDialErr(t *testing.T) {
	const sock = "/run/n5-fangov/n5-fangov.sock"
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"eacces", dialErr(syscall.EACCES), errPermission},
		{"eperm", dialErr(syscall.EPERM), errPermission},
		{"econnrefused", dialErr(syscall.ECONNREFUSED), errNoDaemon},
		{"enoent", dialErr(syscall.ENOENT), errNoDaemon},
		{"timeout", &url.Error{Op: "Get", URL: "x", Err: errors.New("context deadline exceeded")}, errNoDaemon},
		{"plain", errors.New("boom"), errNoDaemon},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDialErr(sock, tc.in)
			if !errors.Is(got, tc.want) {
				t.Fatalf("classifyDialErr(%v) = %v, want errors.Is %v", tc.in, got, tc.want)
			}
			if errors.Is(got, errPermission) == errors.Is(got, errNoDaemon) {
				t.Fatalf("classification must be exclusive, got %v", got)
			}
			msg := got.Error()
			if tc.want == errPermission {
				for _, s := range []string{"permission denied on " + sock, "run as root", "root-only by design"} {
					if !strings.Contains(msg, s) {
						t.Errorf("message %q lacks %q", msg, s)
					}
				}
				if strings.Contains(msg, "not reachable") {
					t.Errorf("permission message must not read like a stopped daemon: %q", msg)
				}
			} else if !strings.Contains(msg, "daemon not reachable") {
				t.Errorf("message %q lacks the not-reachable prefix", msg)
			}
		})
	}
}

// TestLoadSnapshotErrorPaths covers the not-running branch: without a socket
// the CLI falls back to state.json and, when that is missing too, keeps the
// "daemon not running?" hint. (The permission branch is not errNoDaemon and
// is returned unchanged; its text is checked in TestClassifyDialErr.)
func TestLoadSnapshotErrorPaths(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("N5FANGOV_RUN_DIR", dir)
	// no socket, no state.json -> not-running fallback text
	_, _, err := loadSnapshot(dir)
	if err == nil || !strings.Contains(err.Error(), "daemon not running?") {
		t.Fatalf("no socket: want the not-running hint, got %v", err)
	}
	// stale state.json -> fallback succeeds and names the source
	if werr := os.WriteFile(statePath(dir), []byte(`{"ts":1,"status":"ok","profile":"n5pro"}`), 0o644); werr != nil {
		t.Fatal(werr)
	}
	snap, source, err := loadSnapshot(dir)
	if err != nil || snap.Profile != "n5pro" || !strings.HasPrefix(source, "state.json") {
		t.Fatalf("fallback: snap=%+v source=%q err=%v", snap, source, err)
	}
}

// captureStderr runs f with os.Stderr redirected and returns what was written.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	f()
	os.Stderr = old
	w.Close()
	return <-done
}
