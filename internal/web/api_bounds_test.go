package web

import (
	"fmt"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Exact boundary cases of the HTTP API (AUDIT 7 "Tests"): every limit at
// min, max, min-1 and max+1. The existing tests cover "clearly inside" and
// "clearly outside"; these pin the edge the UI and the CLI rely on.

func TestOverrideBoundsAPI(t *testing.T) {
	cases := []struct {
		name string
		body string
		code int
		duty int    // applied duty on success
		msg  string // error substring on failure
	}{
		{"duty_0", `{"duty":0}`, 200, 0, ""},
		{"duty_255", `{"duty":255}`, 200, 255, ""},
		{"duty_256", `{"duty":256}`, 400, 0, "0..255"},
		{"duty_-1", `{"duty":-1}`, 400, 0, "0..255"},
		{"percent_0", `{"percent":0}`, 200, 0, ""},
		{"percent_100", `{"percent":100}`, 200, 255, ""},
		{"percent_101", `{"percent":101}`, 400, 0, "0..100"},
		{"percent_-1", `{"percent":-1}`, 400, 0, "0..100"},
		{"percent_50_rounds", `{"percent":50}`, 200, 128, ""}, // 127.5 → 128
		{"both", `{"duty":0,"percent":0}`, 400, 0, "not both"},
		{"empty_body", ``, 400, 0, "invalid JSON"},
		{"empty_object", `{}`, 400, 0, "required"},
		{"null_duty", `{"duty":null}`, 400, 0, "required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, AuthConfig{})
			r := e.do(t, "PUT", "/api/override/cpu", c.body, csrf)
			if c.code != 200 {
				wantError(t, r, c.code, c.msg)
				if len(e.svc.overrides) != 0 {
					t.Errorf("override applied on %d: %v", c.code, e.svc.overrides)
				}
				return
			}
			wantCode(t, r, 200)
			if got := e.svc.overrides["cpu"]; got != c.duty {
				t.Errorf("applied duty %d, want %d", got, c.duty)
			}
			if !strings.Contains(r.body, fmt.Sprintf(`"duty":%d`, c.duty)) {
				t.Errorf("response %s lacks duty %d", r.body, c.duty)
			}
		})
	}
	e := newEnv(t, AuthConfig{})
	// channel name: 32 characters pass the syntax check (404: not in the
	// snapshot), 33 fail it (400) — same rule as config.nameRe plus a length
	wantError(t, e.do(t, "PUT", "/api/override/"+strings.Repeat("c", 32), `{"duty":1}`, csrf), 404, "unknown channel")
	wantError(t, e.do(t, "PUT", "/api/override/"+strings.Repeat("c", 33), `{"duty":1}`, csrf), 400, "invalid channel name")
	wantError(t, e.do(t, "DELETE", "/api/override/"+strings.Repeat("c", 33), "", csrf), 400, "invalid channel name")
}

func TestHistoryBoundsAPI(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	for _, c := range []struct {
		query string
		code  int
		since time.Duration // History() argument on success
	}{
		{"minutes=1", 200, time.Minute},
		{"minutes=1440", 200, 1440 * time.Minute},
		{"minutes=0", 400, 0},
		{"minutes=1441", 400, 0},
		{"minutes=-1", 400, 0},
		{"minutes=abc", 400, 0},
		{"minutes=", 200, 120 * time.Minute}, // empty = default
		{"since=0", 200, 120 * time.Minute},
		{"since=-1", 400, 0},
		{"since=abc", 400, 0},
		{"since=1.5", 400, 0},
		{"since=9223372036854775807", 200, 120 * time.Minute},
		{"since=9223372036854775808", 400, 0}, // int64 overflow
	} {
		t.Run(c.query, func(t *testing.T) {
			e.svc.mu.Lock()
			e.svc.histSince = -1
			e.svc.mu.Unlock()
			r := e.do(t, "GET", "/api/history?"+c.query, "", nil)
			if c.code != 200 {
				wantError(t, r, c.code, "must be")
				e.svc.mu.Lock()
				defer e.svc.mu.Unlock()
				if e.svc.histSince != -1 {
					t.Errorf("History called on a refused query")
				}
				return
			}
			wantCode(t, r, 200)
			e.svc.mu.Lock()
			defer e.svc.mu.Unlock()
			if e.svc.histSince != c.since {
				t.Errorf("History(%s), want %s", e.svc.histSince, c.since)
			}
		})
	}
	// since filters strictly: a point at exactly since is excluded
	r := e.do(t, "GET", "/api/history?since=1789499990", "", nil)
	wantCode(t, r, 200)
	if strings.Contains(r.body, `"ts":1789499990`) || !strings.Contains(r.body, `"ts":1789500000`) {
		t.Errorf("since filter: %s", r.body)
	}
}

func TestLogLinesBoundsAPI(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.logs = make([]string, 6000)
	for i := range e.logs {
		e.logs[i] = "l" + strconv.Itoa(i)
	}
	e.withDeps(t, AuthConfig{}, func(d *Deps) { d.Log = &fakeLogStore{lines: e.logs} }) // the store copies the slice at construction
	for _, c := range []struct {
		lines string
		code  int
		n     int
	}{
		{"1", 200, 1},
		{"5000", 200, 5000},
		{"0", 400, 0},
		{"5001", 400, 0},
		{"-5", 400, 0},
		{"x", 400, 0},
		{"", 200, 100},
	} {
		t.Run("lines="+c.lines, func(t *testing.T) {
			r := e.do(t, "GET", "/api/log?lines="+c.lines, "", nil)
			if c.code != 200 {
				wantError(t, r, c.code, "1..5000")
				return
			}
			wantCode(t, r, 200)
			var out struct {
				Lines []string `json:"lines"`
			}
			decode(t, r.body, &out)
			if len(out.Lines) != c.n {
				t.Errorf("%d lines, want %d", len(out.Lines), c.n)
			}
			if c.n > 0 && out.Lines[len(out.Lines)-1] != "l5999" {
				t.Errorf("last line %q, want the newest", out.Lines[len(out.Lines)-1])
			}
		})
	}
}

// TestAccountPasswordBounds: the server counts code points (8..128), not
// bytes or UTF-16 units — a 128-emoji password (512 bytes) is fine, 129
// ASCII characters are not.
func TestAccountPasswordBounds(t *testing.T) {
	if minPasswordLen != 8 || maxPasswordLen != 128 {
		t.Fatalf("password limits %d..%d", minPasswordLen, maxPasswordLen)
	}
	const emoji = "\U0001F525" // 4 bytes, 1 code point, 2 UTF-16 units
	cases := []struct {
		name string
		pw   string
		ok   bool
	}{
		{"ascii_8", strings.Repeat("a", 8), true},
		{"ascii_128", strings.Repeat("a", 128), true},
		{"ascii_7", strings.Repeat("a", 7), false},
		{"ascii_129", strings.Repeat("a", 129), false},
		{"emoji_8", strings.Repeat(emoji, 8), true},
		{"emoji_128", strings.Repeat(emoji, 128), true},
		{"emoji_7", strings.Repeat(emoji, 7), false},
		{"emoji_129", strings.Repeat(emoji, 129), false},
		{"umlaut_8", strings.Repeat("ä", 8), true}, // 2 bytes each
		{"mixed_128", strings.Repeat("a", 64) + strings.Repeat(emoji, 64), true},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// a fresh env per case: a successful change stores a production
			// (210k) hash, and the current password must stay cheap
			e, acc, _, _ := storesEnv(t, adminBasic)
			e.srv.limiter.sleep = func(time.Duration) {}
			body := fmt.Sprintf(`{"current_password":"pw","new_password":%q}`, c.pw)
			r := e.do(t, "POST", "/api/account/password", body, basicAuth("admin", "pw"))
			if !c.ok {
				wantError(t, r, 400, "8..128")
				if len(acc.updates) != 0 {
					t.Errorf("account updated on refusal")
				}
				return
			}
			wantCode(t, r, 200)
			if len(acc.updates) != 1 || !strings.HasPrefix(acc.updates[0][1], "pbkdf2$") {
				t.Fatalf("new password (%d code points, %d bytes) not stored: %v", utf8.RuneCountInString(c.pw), len(c.pw), acc.updates)
			}
			// the round trip through the verifier is the real check (one
			// production-strength PBKDF2 per case, not two)
			wantCode(t, e.do(t, "GET", "/api/config", "", basicAuth("admin", c.pw)), 200)
		})
	}
}

// TestAccountUserBounds: userName is [A-Za-z0-9_.-]{1,32}.
func TestAccountUserBounds(t *testing.T) {
	cases := []struct {
		name string
		user string
		ok   bool
	}{
		{"len_1", "a", true},
		{"len_32", strings.Repeat("u", 32), true},
		{"len_33", strings.Repeat("u", 33), false},
		{"space", "ad min", false},
		{"leading_space", " admin", false},
		{"umlaut", "ädmin", false},
		{"dots_dashes", "root.ops-1_x", true},
		{"slash", "a/b", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e, acc, _, _ := storesEnv(t, adminBasic)
			e.srv.limiter.sleep = func(time.Duration) {}
			body := fmt.Sprintf(`{"current_password":"pw","user":%q}`, c.user)
			r := e.do(t, "POST", "/api/account/user", body, basicAuth("admin", "pw"))
			if !c.ok {
				wantError(t, r, 400, "user must match")
				if len(acc.updates) != 0 {
					t.Errorf("account updated on refusal")
				}
				return
			}
			wantCode(t, r, 200)
			if len(acc.updates) != 1 || acc.updates[0][0] != c.user || !VerifyPassword(c.user, "pw", acc.updates[0][1]) {
				t.Fatalf("rename to %q not stored: %v", c.user, acc.updates)
			}
			wantCode(t, e.do(t, "GET", "/api/config", "", basicAuth(c.user, "pw")), 200)
		})
	}
}

func TestDashboardBoundsAPI(t *testing.T) {
	if maxDashboardSensors != 8 {
		t.Fatalf("maxDashboardSensors = %d", maxDashboardSensors)
	}
	e, _, _, db := storesEnv(t, adminBasic)
	ok := basicAuth("admin", "pw")
	ids := func(n int) string {
		var s []string
		for i := 0; i < n; i++ {
			s = append(s, fmt.Sprintf("\"hwmon:x:temp%d\"", i+1))
		}
		return `{"sensors":[` + strings.Join(s, ",") + `]}`
	}
	r := e.do(t, "PUT", "/api/dashboard", ids(8), ok)
	wantCode(t, r, 200)
	if len(db.sensors) != 8 || !strings.Contains(r.body, `"hwmon:x:temp8"`) || !strings.Contains(r.body, `"warnings":[]`) {
		t.Errorf("8 sensors: %s (store %d)", r.body, len(db.sensors))
	}
	wantError(t, e.do(t, "PUT", "/api/dashboard", ids(9), ok), 400, "at most 8")
	if len(db.sensors) != 8 {
		t.Errorf("store changed on refusal: %d", len(db.sensors))
	}
	wantError(t, e.do(t, "PUT", "/api/dashboard", `{"sensors":null}`, ok), 400, "array required")
	wantError(t, e.do(t, "PUT", "/api/dashboard", `{"sensors":"x"}`, ok), 400, "invalid JSON")
}

func TestTLSUploadBoundsAPI(t *testing.T) {
	m := newFakeTLSMgr("auto")
	e := tlsEnv(t, AuthConfig{}, m)
	jsonHdr := map[string]string{CSRFHeader: "1", "Content-Type": "application/json"}
	// empty and one-sided bodies never reach the manager
	for _, body := range []string{``, `{}`, `{"cert":"C"}`, `{"key":"K"}`, `{"cert":"C","key":""}`, `{"cert":"","key":"K"}`, `{"cert":"  \n","key":"K"}`} {
		r := e.do(t, "POST", "/api/tls/upload", body, jsonHdr)
		if body == "" {
			wantError(t, r, 400, "invalid JSON")
		} else {
			wantError(t, r, 400, "both required")
		}
	}
	if len(m.uploads) != 0 {
		t.Fatalf("manager called for an incomplete body: %v", m.uploads)
	}
}

// TestPresetNameBoundsAPI: presetName is [a-z0-9_-]{1,64} on every preset
// route; a traversal segment never reaches the store.
func TestPresetNameBoundsAPI(t *testing.T) {
	e, _, _, _ := storesEnv(t, adminBasic)
	store := e.srv.deps.Presets.(*fakePresetDeleter)
	auth := basicAuth("admin", "pw")
	n64, n65 := strings.Repeat("p", 64), strings.Repeat("p", 65)
	// 64 characters is syntactically fine: the store answers "unknown"
	wantError(t, e.do(t, "GET", "/api/presets/"+n64, "", auth), 404, "unknown preset")
	wantError(t, e.do(t, "DELETE", "/api/presets/"+n64, "", auth), 404, "unknown preset")
	wantError(t, e.do(t, "POST", "/api/presets/"+n64+"/rename", `{"name":"x"}`, auth), 404, "unknown preset")
	// 65 and an upper-case letter fail the syntax check
	for _, bad := range []string{n65, "A", "quiet%20night"} {
		wantError(t, e.do(t, "GET", "/api/presets/"+bad, "", auth), 400, "invalid preset name")
		wantError(t, e.do(t, "DELETE", "/api/presets/"+bad, "", auth), 400, "invalid preset name")
		wantError(t, e.do(t, "POST", "/api/presets/"+bad+"/rename", `{"name":"x"}`, auth), 400, "invalid preset name")
		wantError(t, e.do(t, "POST", "/api/presets/quiet/rename", `{"name":"`+bad+`"}`, auth), 400, "invalid preset name")
		wantError(t, e.do(t, "POST", "/api/presets/"+bad+"/apply", "", auth), 400, "invalid preset name")
		wantError(t, e.do(t, "PUT", "/api/presets/"+bad, "", auth), 400, "invalid preset name")
	}
	// ".." as a segment: the mux cleans the path (redirect to /api/, 404)
	// or the name check refuses it — either way no store call and no 2xx
	for _, seg := range []string{"..", "%2e%2e", "..%2fquiet"} {
		for _, m := range []string{"GET", "DELETE"} {
			r := e.do(t, m, "/api/presets/"+seg, "", auth)
			if r.code/100 == 2 {
				t.Errorf("%s /api/presets/%s → %d", m, seg, r.code)
			}
		}
	}
	wantError(t, e.do(t, "POST", "/api/presets/quiet/rename", `{"name":".."}`, auth), 400, "invalid preset name")
	if len(store.deleted) != 0 {
		t.Errorf("store deletes: %v", store.deleted)
	}
	// a 64-character target name is accepted by the rename
	wantCode(t, e.do(t, "POST", "/api/presets/quiet/rename", `{"name":"`+n64+`"}`, auth), 200)
	wantCode(t, e.do(t, "GET", "/api/presets/"+n64, "", auth), 200)
}

// TestBodyLimitsExact: every body-limited endpoint accepts exactly its
// limit and refuses one byte more with 413 before the payload is looked
// at. All cases share one server: after a 413 net/http parks the server
// connection for rstAvoidanceDelay (500 ms) and httptest.Server.Close
// waits for it — the measured cost behind the "slow" 413 tests — so the
// server here is closed asynchronously.
func TestBodyLimitsExact(t *testing.T) {
	b := &fakeBundle{doc: "{}"}
	m := newFakeTLSMgr("auto")
	db := &fakeDashboard{}
	e := newEnv(t, AuthConfig{})
	d := e.deps(AuthConfig{})
	d.Bundle, d.TLSMgr, d.Dashboard = b, m, db
	e.srv = New(d)
	e.ts.Close()
	e.ts = httptest.NewServer(e.srv.Handler())
	t.Cleanup(func() { go e.ts.Close() })
	jsonHdr := map[string]string{CSRFHeader: "1", "Content-Type": "application/json"}

	// override: maxOverrideBody
	exact := `{"duty":7` + strings.Repeat(" ", maxOverrideBody-len(`{"duty":7}`)) + `}`
	if len(exact) != maxOverrideBody {
		t.Fatalf("override body is %d bytes", len(exact))
	}
	wantCode(t, e.do(t, "PUT", "/api/override/cpu", exact, csrf), 200)
	if e.svc.overrides["cpu"] != 7 {
		t.Errorf("exact-size override not applied: %v", e.svc.overrides)
	}
	over := `{"duty":8` + strings.Repeat(" ", maxOverrideBody-len(`{"duty":8}`)+1) + `}`
	wantError(t, e.do(t, "PUT", "/api/override/cpu", over, csrf), 413, strconv.Itoa(maxOverrideBody))
	if e.svc.overrides["cpu"] != 7 {
		t.Errorf("oversized override applied: %v", e.svc.overrides)
	}

	// settings bundle: maxImportBody
	frame := `{"pad":""}`
	exact = `{"pad":"` + strings.Repeat("x", maxImportBody-len(frame)) + `"}`
	if len(exact) != maxImportBody {
		t.Fatalf("import body is %d bytes", len(exact))
	}
	wantCode(t, e.do(t, "POST", "/api/config/import", exact, csrf), 200)
	if len(b.imported) != 1 || len(b.imported[0]) != maxImportBody {
		t.Fatalf("exact-size import: %d calls", len(b.imported))
	}
	over = `{"pad":"` + strings.Repeat("x", maxImportBody-len(frame)+1) + `"}`
	wantError(t, e.do(t, "POST", "/api/config/import", over, csrf), 413, strconv.Itoa(maxImportBody))
	if len(b.imported) != 1 {
		t.Errorf("oversized import reached the bundle")
	}

	// TLS upload (JSON form): maxTLSUpload
	frame = `{"cert":"","key":"K"}`
	exact = `{"cert":"` + strings.Repeat("A", maxTLSUpload-len(frame)) + `","key":"K"}`
	if len(exact) != maxTLSUpload {
		t.Fatalf("upload body is %d bytes", len(exact))
	}
	wantCode(t, e.do(t, "POST", "/api/tls/upload", exact, jsonHdr), 200)
	if len(m.uploads) != 1 || len(m.uploads[0][0]) != maxTLSUpload-len(frame) {
		t.Fatalf("exact-size upload: %d calls", len(m.uploads))
	}
	over = `{"cert":"` + strings.Repeat("A", maxTLSUpload-len(frame)+1) + `","key":"K"}`
	wantError(t, e.do(t, "POST", "/api/tls/upload", over, jsonHdr), 413, strconv.Itoa(maxTLSUpload))
	if len(m.uploads) != 1 {
		t.Errorf("oversized upload reached the manager")
	}

	// config text: maxBody
	exact = sampleTOML + "# " + strings.Repeat("x", maxBody-len(sampleTOML)-3) + "\n"
	if len(exact) != maxBody {
		t.Fatalf("config body is %d bytes", len(exact))
	}
	wantCode(t, e.do(t, "PUT", "/api/config", exact, csrf), 200)
	if len(e.cfg.saved) != 1 {
		t.Fatalf("exact-size config: %d saves", len(e.cfg.saved))
	}
	wantError(t, e.do(t, "PUT", "/api/config", exact+"x", csrf), 413, strconv.Itoa(maxBody))
	if len(e.cfg.saved) != 1 {
		t.Errorf("oversized config saved")
	}

	// the small JSON endpoints share maxJSONBody (dashboard stands in)
	frame = `{"sensors":[""]}`
	exact = `{"sensors":["` + strings.Repeat("s", maxJSONBody-len(frame)) + `"]}`
	if len(exact) != maxJSONBody {
		t.Fatalf("dashboard body is %d bytes", len(exact))
	}
	wantCode(t, e.do(t, "PUT", "/api/dashboard", exact, csrf), 200)
	if len(db.sensors) != 1 || len(db.sensors[0]) != maxJSONBody-len(frame) {
		t.Errorf("exact-size dashboard body not stored: %d ids", len(db.sensors))
	}
	over = `{"sensors":["` + strings.Repeat("s", maxJSONBody-len(frame)+1) + `"]}`
	wantError(t, e.do(t, "PUT", "/api/dashboard", over, csrf), 413, strconv.Itoa(maxJSONBody))
	if len(db.sensors[0]) != maxJSONBody-len(frame) {
		t.Errorf("oversized dashboard body stored")
	}
}
