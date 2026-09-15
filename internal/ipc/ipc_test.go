package ipc

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"strings"
)

// handler is a minimal stand-in for the web mux (ipc must not import web;
// the full round trip against web.Server lives in internal/web).
func handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "9.9.9-ipc"})
	})
	mux.HandleFunc("PUT /api/echo", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write([]byte(strings.ToUpper(string(b))))
	})
	return mux
}

func tempSock(t *testing.T) string {
	t.Helper()
	// Unix socket paths are limited (~108 bytes); keep it short.
	dir, err := os.MkdirTemp("", "pvefand-ipc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "sub", "pvefand.sock")
}

func waitFor(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("unix", path); err == nil {
			c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never came up", path)
}

func TestServeClientRoundTrip(t *testing.T) {
	sock := tempSock(t)
	h := handler()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, sock, h) }()
	waitFor(t, sock)

	if runtime.GOOS != "windows" {
		fi, err := os.Stat(sock)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSocket == 0 {
			t.Errorf("not a socket: %v", fi.Mode())
		}
		if perm := fi.Mode().Perm(); perm != 0o660 {
			t.Errorf("socket mode = %o, want 660", perm)
		}
	}

	c := Client(sock)
	res, err := c.Get("http://pvefand/api/version")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	var v map[string]string
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatal(err)
	}
	if v["version"] != "9.9.9-ipc" {
		t.Fatalf("version = %v", v)
	}

	req, _ := http.NewRequest("PUT", "http://pvefand/api/echo", strings.NewReader("hello"))
	res, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(body) != "HELLO" {
		t.Fatalf("PUT over socket = %d %q", res.StatusCode, body)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after cancel")
	}
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Errorf("socket file not removed after shutdown: %v", err)
	}
}

// TestListenRestoresUmask (M3): Listen narrows the umask only around bind
// and puts the caller's value back, also on the error path.
func TestListenRestoresUmask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no umask on windows")
	}
	orig := setUmask(0o027)
	defer setUmask(orig)
	sock := tempSock(t)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	if got := setUmask(0o027); got != 0o027 {
		t.Fatalf("umask after Listen = %04o, want 0027", got)
	}
	plain := filepath.Join(filepath.Dir(sock), "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(plain); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
	if got := setUmask(0o027); got != 0o027 {
		t.Fatalf("umask after failed Listen = %04o, want 0027", got)
	}
}

func TestListenRemovesStaleSocket(t *testing.T) {
	sock := tempSock(t)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crashed daemon: close the listener without unlinking.
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	ln.Close()
	if _, err := os.Lstat(sock); err != nil {
		t.Skipf("platform unlinked socket on close: %v", err)
	}
	ln2, err := Listen(sock)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	ln2.Close()
}

func TestListenRefusesLiveSocketAndNonSocket(t *testing.T) {
	sock := tempSock(t)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if _, err := Listen(sock); err == nil {
		t.Fatal("second Listen on a live socket succeeded")
	}

	plain := filepath.Join(filepath.Dir(sock), "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(plain); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
}
