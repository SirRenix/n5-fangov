package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// tokenFake adds the /api/tokens endpoints to the fake daemon: an
// in-memory list, the create body recorded, revoke by id.
type tokenFake struct {
	d       *fakeDaemon
	created []map[string]any
	tokens  []map[string]any
	revoked []string
}

func (f *tokenFake) install(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tokens", func(w http.ResponseWriter, r *http.Request) {
		f.d.mu.Lock()
		defer f.d.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": f.tokens})
	})
	mux.HandleFunc("POST /api/tokens", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			w.WriteHeader(400)
			return
		}
		f.d.mu.Lock()
		defer f.d.mu.Unlock()
		f.created = append(f.created, b)
		if b["name"] == "taken" {
			w.WriteHeader(409)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "token name already in use"})
			return
		}
		out := map[string]any{"ok": true, "token": "n5t_secret-for-" + b["name"].(string), "id": "0badcafe", "name": b["name"], "scope": b["scope"]}
		if ttl, _ := b["ttl_days"].(float64); ttl == 0 {
			out["expires"] = nil
			out["warning"] = "token never expires"
		} else {
			out["expires"] = time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
		}
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("DELETE /api/tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.d.mu.Lock()
		defer f.d.mu.Unlock()
		if id != "0badcafe" {
			w.WriteHeader(404)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "unknown token " + id})
			return
		}
		f.revoked = append(f.revoked, id)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "revoked": id})
	})
}

// startTokenFake is startFakeDaemon with the token endpoints: the fake
// is restarted with the extra routes installed.
func startTokenFake(t *testing.T) (*fakeDaemon, *tokenFake) {
	t.Helper()
	d := startFakeDaemon(t)
	d.stop()
	f := &tokenFake{d: d}
	d.extra = f.install
	d.start(t)
	return d, f
}

func TestCLITokenCreate(t *testing.T) {
	d, f := startTokenFake(t)
	// defaults: scope read, ttl 90; the secret alone on stdout
	out, errOut, code := captureOutput(t, func() int { return cmdToken([]string{"create", "Home Assistant"}) })
	if code != exitOK || out != "n5t_secret-for-Home Assistant\n" {
		t.Fatalf("create: %d stdout %q stderr %q", code, out, errOut)
	}
	if !strings.Contains(errOut, `token "Home Assistant" created (id 0badcafe, scope read, expires 2027-01-02`) || !strings.Contains(errOut, "shown only now") || strings.Contains(errOut, "warning") {
		t.Errorf("stderr %q", errOut)
	}
	d.mu.Lock()
	if len(f.created) != 1 || f.created[0]["name"] != "Home Assistant" || f.created[0]["scope"] != "read" || f.created[0]["ttl_days"] != float64(90) {
		t.Errorf("body sent: %v", f.created)
	}
	d.mu.Unlock()
	// flags before or after the name; ttl 0 → warning on stderr
	for _, args := range [][]string{{"create", "agent", "--scope", "control", "--ttl", "0"}, {"create", "--scope", "control", "--ttl", "0", "agent"}} {
		out, errOut, code := captureOutput(t, func() int { return cmdToken(args) })
		if code != exitOK || out != "n5t_secret-for-agent\n" || !strings.Contains(errOut, "warning: token never expires") || !strings.Contains(errOut, "expires never") {
			t.Errorf("%v: %d %q %q", args, code, out, errOut)
		}
	}
	d.mu.Lock()
	last := f.created[len(f.created)-1]
	d.mu.Unlock()
	if last["scope"] != "control" || last["ttl_days"] != float64(0) {
		t.Errorf("body sent: %v", last)
	}
	// server refusal → exit 1 with the server's text, nothing on stdout
	out, errOut, code = captureOutput(t, func() int { return cmdToken([]string{"create", "taken"}) })
	if code != exitFail || out != "" || !strings.Contains(errOut, "HTTP 409: token name already in use") {
		t.Errorf("taken: %d %q %q", code, out, errOut)
	}
	// usage errors: exit 2, nothing sent
	d.mu.Lock()
	sent := len(f.created)
	d.mu.Unlock()
	for _, args := range [][]string{nil, {"create"}, {"create", "a", "b"}, {"create", "x", "--scope", "root"}, {"create", "x", "--ttl", "-1"}, {"create", "x", "--bogus"}, {"list", "x"}, {"revoke"}, {"revoke", "a", "b"}, {"nope"}} {
		if out, _, code := captureOutput(t, func() int { return cmdToken(args) }); code != exitUsage || out != "" {
			t.Errorf("%v: %d %q", args, code, out)
		}
	}
	d.mu.Lock()
	if len(f.created) != sent {
		t.Errorf("usage error sent a request")
	}
	d.mu.Unlock()
	if _, errOut, _ := captureOutput(t, func() int { return cmdToken(nil) }); !strings.Contains(errOut, "usage: n5-fangov token create NAME") {
		t.Errorf("usage text: %q", errOut)
	}
	// daemon down: exit 1 with the hint, nothing on stdout
	d.stop()
	out, errOut, code = captureOutput(t, func() int { return cmdToken([]string{"create", "x"}) })
	if code != exitFail || out != "" || !strings.Contains(errOut, "daemon not reachable") || !strings.Contains(errOut, "daemon not running?") {
		t.Errorf("down: %d %q %q", code, out, errOut)
	}
}

func TestCLITokenListRevoke(t *testing.T) {
	d, f := startTokenFake(t)
	// empty list
	out, _, code := captureOutput(t, func() int { return cmdToken([]string{"list"}) })
	if code != exitOK || !strings.Contains(out, "no API tokens") {
		t.Errorf("empty list: %d %q", code, out)
	}
	created := time.Date(2026, 9, 16, 20, 0, 0, 0, time.UTC)
	used := created.Add(2 * time.Hour)
	d.mu.Lock()
	f.tokens = []map[string]any{
		{"id": "0badcafe", "name": "Home Assistant", "scope": "control", "created": created, "expires": created.AddDate(0, 0, 90), "last_used": used, "last_ip": "192.0.2.10", "expired": false},
		{"id": "deadbeef", "name": "old script", "scope": "read", "created": created, "expires": nil, "last_used": nil, "last_ip": "", "expired": false},
		{"id": "0000ffff", "name": "gone", "scope": "admin", "created": created, "expires": created.AddDate(0, 0, 1), "last_used": nil, "last_ip": "", "expired": true},
	}
	d.mu.Unlock()
	out, errOut, code := captureOutput(t, func() int { return cmdToken([]string{"list"}) })
	if code != exitOK || errOut != "" {
		t.Fatalf("list: %d %q", code, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "id        name") || !strings.Contains(lines[0], "last address") {
		t.Fatalf("list output:\n%s", out)
	}
	if !strings.HasPrefix(lines[1], "0badcafe  Home Assistant") || !strings.Contains(lines[1], "control") || !strings.Contains(lines[1], created.AddDate(0, 0, 90).Local().Format("2006-01-02 15:04")) || !strings.Contains(lines[1], used.Local().Format("2006-01-02 15:04")) || !strings.HasSuffix(lines[1], "192.0.2.10") {
		t.Errorf("row 1: %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "deadbeef  old script") || !strings.Contains(lines[2], "never") || !strings.Contains(lines[2], "  -  ") {
		t.Errorf("row 2: %q", lines[2])
	}
	if !strings.Contains(lines[3], "(expired)") {
		t.Errorf("row 3: %q", lines[3])
	}
	if strings.Contains(out, "n5t_") {
		t.Errorf("a secret in the list")
	}
	// revoke: ok, then 404 → exit 1
	out, _, code = captureOutput(t, func() int { return cmdToken([]string{"revoke", "0badcafe"}) })
	if code != exitOK || out != "token 0badcafe revoked\n" {
		t.Errorf("revoke: %d %q", code, out)
	}
	d.mu.Lock()
	if len(f.revoked) != 1 || f.revoked[0] != "0badcafe" {
		t.Errorf("revoked: %v", f.revoked)
	}
	d.mu.Unlock()
	if out, errOut, code := captureOutput(t, func() int { return cmdToken([]string{"revoke", "12345678"}) }); code != exitFail || out != "" || !strings.Contains(errOut, "HTTP 404: unknown token 12345678") {
		t.Errorf("revoke unknown: %d %q %q", code, out, errOut)
	}
	// daemon down
	d.stop()
	for _, args := range [][]string{{"list"}, {"revoke", "0badcafe"}} {
		if out, errOut, code := captureOutput(t, func() int { return cmdToken(args) }); code != exitFail || out != "" || !strings.Contains(errOut, "daemon not running?") {
			t.Errorf("%v down: %d %q %q", args, code, out, errOut)
		}
	}
}

// TestCLITokenRegistered: the subcommand is listed after alerts with a
// help line naming the three forms.
func TestCLITokenRegistered(t *testing.T) {
	if _, ok := commands["token"]; !ok {
		t.Fatal("token not registered")
	}
	pos := map[string]int{}
	for i, n := range order {
		pos[n] = i
	}
	if pos["token"] != pos["alerts"]+1 {
		t.Errorf("token must follow alerts in the usage: %v", order)
	}
	for _, want := range []string{"create NAME", "--scope", "--ttl", "list", "revoke ID"} {
		if !strings.Contains(helpText["token"], want) {
			t.Errorf("help line lacks %q: %s", want, helpText["token"])
		}
	}
}
