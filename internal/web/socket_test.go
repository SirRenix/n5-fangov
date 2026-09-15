package web

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/ventula/internal/ipc"
)

// TestUnixSocketRoundTrip serves the Server on a temp unix socket and talks
// to it with ipc.Client: GET /api/version, and a write without CSRF/auth
// (the socket handler enforces neither).
func TestUnixSocketRoundTrip(t *testing.T) {
	dir, err := os.MkdirTemp("", "ventula-web")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "ventula.sock")

	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: PasswordHash("admin", "pw")})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- e.srv.ServeSocket(ctx, sock) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket never came up")
		}
		time.Sleep(10 * time.Millisecond)
	}

	c := ipc.Client(sock)
	res, err := c.Get("http://ventula/api/version")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var v map[string]string
	if err := json.Unmarshal(body, &v); err != nil || v["version"] != "1.2.3-test" {
		t.Fatalf("version over socket: %d %s (%v)", res.StatusCode, body, err)
	}

	req, _ := http.NewRequest("PUT", "http://ventula/api/override/cpu", strings.NewReader(`{"duty":77}`))
	res, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || e.svc.overrides["cpu"] != 77 {
		t.Fatalf("override over socket without auth/CSRF: %d %s", res.StatusCode, body)
	}

	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("ServeSocket: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeSocket did not stop")
	}
}

// TestListenAndServeTCP checks the TCP server honours ctx cancellation.
func TestListenAndServeTCP(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- e.srv.Serve(ctx, ln) }()
	res, err := http.Get("http://" + ln.Addr().String() + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status %d", res.StatusCode)
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
	if err := e.srv.ListenAndServe(ctx, "256.1.1.1:0"); err == nil {
		t.Fatal("ListenAndServe on invalid address succeeded")
	}
}
