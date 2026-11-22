package web

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/cfgver"
	"github.com/SirRenix/n5-fangov/internal/events"
)

// fake hashes, assembled so no scanner takes them for real ones
var (
	vHash = "pbkdf2$" + strings.Repeat("0a", 16) + "$" + strings.Repeat("1b", 32)
	cHash = "pbkdf2$" + strings.Repeat("2c", 16) + "$" + strings.Repeat("3d", 32)
)

// fakeVersions holds two versions and the current text; Restore records
// the id and the caller seen by ConfigActor.
type fakeVersions struct {
	mu       sync.Mutex
	vers     map[int][]byte
	cur      []byte
	restored []int
	err      error
	res      RestoreResult
}

func (f *fakeVersions) List() ([]cfgver.Version, error) {
	return []cfgver.Version{{ID: 2, TS: 1_790_000_200, By: "cli", Reason: "preset quiet"}, {ID: 1, TS: 1_790_000_100, By: "user:admin", Reason: "editor"}}, nil
}
func (f *fakeVersions) Get(id int) (cfgver.Version, []byte, error) {
	raw, ok := f.vers[id]
	if !ok {
		return cfgver.Version{}, nil, fmt.Errorf("version %d: %w", id, fs.ErrNotExist)
	}
	return cfgver.Version{ID: id, TS: 1_790_000_000 + int64(id), By: "user:admin", Reason: "editor"}, raw, nil
}
func (f *fakeVersions) Next(id int) ([]byte, error) {
	if raw, ok := f.vers[id+1]; ok {
		return raw, nil
	}
	return nil, nil
}
func (f *fakeVersions) Current() ([]byte, error) { return f.cur, nil }
func (f *fakeVersions) Restore(id int) (RestoreResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restored = append(f.restored, id)
	return f.res, f.err
}

func versionsEnv(t *testing.T) (*env, *fakeVersions, *[]string, *events.Log) {
	t.Helper()
	f := &fakeVersions{
		vers: map[int][]byte{
			1: []byte("[web]\nuser = \"admin\"\npassword_hash = \"" + vHash + "\"\n\n[alert]\ntransport = \"auto\"\n"),
			2: []byte("[web]\nuser = \"admin\"\npassword_hash = \"" + cHash + "\"\n\n[alert]\ntransport = \"log\"\n"),
		},
		cur: []byte("[web]\nuser = \"admin\"\npassword_hash = \"" + cHash + "\"\n\n[alert]\ntransport = \"mail\"\n"),
	}
	var mu sync.Mutex
	var actors []string
	el := events.New("", nil)
	e := newEnv(t, AuthConfig{})
	e.withDeps(t, AuthConfig{}, func(d *Deps) {
		d.Versions = f
		d.Events = el
		d.ConfigActor = func(by string) func() {
			mu.Lock()
			actors = append(actors, by)
			mu.Unlock()
			return func() {}
		}
	})
	return e, f, &actors, el
}

// TestConfigVersionsAPI: list (sections the next state changed), detail
// (redacted text, diff current → version, password_changed), restore
// (caller named, event, 202 on restart_required, restart keys) and the
// error codes; password_hash never in any answer.
func TestConfigVersionsAPI(t *testing.T) {
	e, f, actors, el := versionsEnv(t)
	noHash := func(what, body string) {
		t.Helper()
		if strings.Contains(body, vHash) || strings.Contains(body, cHash) || strings.Contains(body, strings.Repeat("0a", 16)) {
			t.Errorf("%s carries a password hash: %s", what, body)
		}
	}
	unesc := strings.NewReplacer(`\u003c`, "<", `\u003e`, ">", `\u0026`, "&")
	r := e.do(t, "GET", "/api/config/versions", "", nil)
	wantCode(t, r, 200)
	noHash("list", r.body)
	if !strings.Contains(r.body, `"id":2`) || !strings.Contains(r.body, `"sections":["[alert]"]`) || !strings.Contains(r.body, `"sections":["[web]","[alert]"]`) {
		t.Errorf("list %s", r.body)
	}
	r = e.do(t, "GET", "/api/config/versions/1", "", nil)
	wantCode(t, r, 200)
	noHash("detail", r.body)
	for _, want := range []string{`"password_changed":true`, `password_hash = \"<redacted, changed>\"`, `{"op":"-","line":"transport = \"mail\""}`, `{"op":"+","line":"transport = \"auto\""}`, `"removed":2`, `"added":2`} {
		if !strings.Contains(unesc.Replace(r.body), want) {
			t.Errorf("detail lacks %s: %s", want, r.body)
		}
	}
	r = e.do(t, "GET", "/api/config/versions/2", "", nil)
	if b := unesc.Replace(r.body); !strings.Contains(b, `"password_changed":false`) || !strings.Contains(b, `password_hash = \"<redacted>\"`) {
		t.Errorf("same hash: %s", r.body)
	}
	wantCode(t, e.do(t, "GET", "/api/config/versions/9", "", nil), 404)
	wantCode(t, e.do(t, "GET", "/api/config/versions/x", "", nil), 400)

	f.res = RestoreResult{Version: 3, RestartKeys: []string{"web.listen"}}
	r = e.do(t, "POST", "/api/config/versions/1/restore", "", csrf)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"restart_keys":["web.listen"]`) || !strings.Contains(r.body, `"version":3`) {
		t.Errorf("restore %s", r.body)
	}
	if len(*actors) != 1 {
		t.Errorf("ConfigActor called %d times", len(*actors))
	}
	evs := el.Since(time.Hour, 0)
	if len(evs) != 1 || evs[0].Kind != events.Restore || !strings.Contains(evs[0].Text, "version 1") {
		t.Errorf("events %+v", evs)
	}
	f.res = RestoreResult{RestartRequired: true}
	wantCode(t, e.do(t, "POST", "/api/config/versions/2/restore", "", csrf), 202)
	// refused: 400 with the hash redacted out of the message; unknown 404; write 500
	f.err = errors.New("version 1 does not parse, nothing written: line 3: password_hash = \"" + vHash + "\" broken")
	r = e.do(t, "POST", "/api/config/versions/1/restore", "", csrf)
	wantError(t, r, 400, "does not parse")
	noHash("refusal", r.body)
	f.err = fmt.Errorf("version 9: %w", fs.ErrNotExist)
	wantCode(t, e.do(t, "POST", "/api/config/versions/9/restore", "", csrf), 404)
	f.err = &ReloadError{Err: errors.New("config written, not applied")}
	wantError(t, e.do(t, "POST", "/api/config/versions/1/restore", "", csrf), 500, "not applied")
	// the other config writers run inside ConfigActor too
	n := len(*actors)
	e.do(t, "PUT", "/api/config", "[daemon]\n", csrf)
	if len(*actors) != n+1 {
		t.Errorf("PUT /api/config outside ConfigActor")
	}
	// no store: 501
	e2 := newEnv(t, AuthConfig{})
	wantCode(t, e2.do(t, "GET", "/api/config/versions", "", nil), 501)
}

func TestVersionDiffRedaction(t *testing.T) {
	ver := []byte("[web]\npassword_hash = '" + vHash + "'\nx = \"" + vHash + "\"\n")
	red, d, changed := VersionDiff(ver, []byte("[web]\npassword_hash = \""+cHash+"\"\n"))
	if !changed || strings.Contains(red, vHash) || !strings.Contains(red, "'<redacted, changed>'") {
		t.Errorf("redacted %q", red)
	}
	for _, o := range d {
		if strings.Contains(o.Line, "pbkdf2") {
			t.Errorf("diff line %q", o.Line)
		}
	}
}
