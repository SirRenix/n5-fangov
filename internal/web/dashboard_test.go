package web

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

type fakeDashboard struct {
	sensors []string
	err     error
}

func (d *fakeDashboard) Sensors() []string { return d.sensors }

func (d *fakeDashboard) SetSensors(ids []string) ([]string, error) {
	if d.err != nil {
		return nil, d.err
	}
	d.sensors = ids
	var warn []string
	for _, id := range ids {
		if strings.HasPrefix(id, "bogus") {
			warn = append(warn, id+" does not resolve")
		}
	}
	return warn, nil
}

// TestSystemEndpoint: protected (401 anonymous), the closure's value is
// served verbatim when signed in (cookie or basic), 501 without it.
func TestSystemEndpoint(t *testing.T) {
	e, _, _, _ := storesEnv(t, adminBasic)
	calls := 0
	e.withDeps(t, adminBasic, func(d *Deps) {
		d.System = func() any {
			calls++
			return map[string]any{"host": map[string]any{"hostname": "n5host"}, "gpus": []string{}, "errors": []string{"lspci: not found"}}
		}
	})
	wantError(t, e.do(t, "GET", "/api/system", "", nil), 401, "authentication")
	if calls != 0 {
		t.Errorf("collector called for an anonymous request")
	}
	r := e.do(t, "GET", "/api/system", "", basicAuth("admin", "pw"))
	wantCode(t, r, 200)
	var got struct {
		Host   struct{ Hostname string }
		Errors []string
	}
	decode(t, r.body, &got)
	if got.Host.Hostname != "n5host" || len(got.Errors) != 1 || calls != 1 {
		t.Errorf("system = %s (calls %d)", r.body, calls)
	}
	if cc := r.hdr.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if r := e.do(t, "POST", "/api/system", "", basicAuth("admin", "pw")); r.code != 404 && r.code != 405 {
		t.Errorf("POST /api/system = %d %s", r.code, r.body)
	}
}

// ---- dashboard --------------------------------------------------------------

func TestDashboardEndpoints(t *testing.T) {
	e, _, _, db := storesEnv(t, adminBasic)
	ok := basicAuth("admin", "pw")
	r := e.do(t, "GET", "/api/dashboard", "", ok)
	wantCode(t, r, 200)
	if r.body != "{\"sensors\":[\"hwmon:amdgpu:temp1\"]}\n" {
		t.Errorf("dashboard = %s", r.body)
	}
	r = e.do(t, "PUT", "/api/dashboard", `{"sensors":["hwmon:nic1:temp1","bogus:x"]}`, ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"sensors":["hwmon:nic1:temp1","bogus:x"]`) || !strings.Contains(r.body, `"warnings":["bogus:x does not resolve"]`) || len(db.sensors) != 2 {
		t.Errorf("put = %s", r.body)
	}
	r = e.do(t, "PUT", "/api/dashboard", `{"sensors":[]}`, ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"sensors":[]`) || !strings.Contains(r.body, `"warnings":[]`) {
		t.Errorf("empty put = %s", r.body)
	}
	wantError(t, e.do(t, "PUT", "/api/dashboard", `{}`, ok), 400, "array required")
	wantError(t, e.do(t, "PUT", "/api/dashboard", `{"sensors":["a","b","c","d","e","f","g","h","i"]}`, ok), 400, "at most 8")
	db.err = errors.New("bad id")
	wantError(t, e.do(t, "PUT", "/api/dashboard", `{"sensors":["x"]}`, ok), 400, "bad id")
	db.sensors = nil
	db.err = nil
	if r := e.do(t, "GET", "/api/dashboard", "", ok); !strings.Contains(r.body, `"sensors":[]`) {
		t.Errorf("nil list = %s", r.body)
	}
	wantError(t, e.do(t, "GET", "/api/dashboard", "", nil), 401, "authentication")
}

// ---- about / version -----------------------------------------------------------

func TestAboutAndVersion(t *testing.T) {
	e, _, _, _ := storesEnv(t, adminBasic)
	r := e.do(t, "GET", "/api/about", "", nil)
	wantCode(t, r, 200)
	var a About
	decode(t, r.body, &a)
	// anonymous: no toolchain version (see TestAboutGoOnlySignedIn)
	if a.Name != "n5-fangov" || a.Version != "0.3.0-beta.1" || a.Prerelease != "beta.1" || a.License != "GPL-2.0-only" || a.Go != "" || len(a.Credits) != 1 {
		t.Errorf("about = %+v", a)
	}
	r = e.do(t, "GET", "/api/version", "", nil)
	if !strings.Contains(r.body, `"prerelease":"beta.1"`) || !strings.Contains(r.body, `"auth":"basic"`) {
		t.Errorf("version = %s", r.body)
	}
	// defaults for an empty About
	e2 := newEnv(t, AuthConfig{})
	r = e2.do(t, "GET", "/api/about", "", nil)
	if !strings.Contains(r.body, `"name":"n5-fangov"`) || !strings.Contains(r.body, `"version":"1.2.3-test"`) || !strings.Contains(r.body, `"credits":[]`) || !strings.Contains(r.body, `"prerelease":""`) {
		t.Errorf("default about = %s", r.body)
	}
}

// TestAboutGoOnlySignedIn: the toolchain version is absent for anonymous
// callers and present for a signed-in one (auth = none counts as signed in).
func TestAboutGoOnlySignedIn(t *testing.T) {
	e, _, _, _ := storesEnv(t, adminBasic)
	var a About
	decode(t, e.do(t, "GET", "/api/about", "", nil).body, &a)
	if a.Go != "" || a.Version != "0.3.0-beta.1" {
		t.Errorf("anonymous about = %+v", a)
	}
	decode(t, e.do(t, "GET", "/api/about", "", basicAuth("admin", "pw")).body, &a)
	if a.Go != runtime.Version() {
		t.Errorf("signed-in about go = %q", a.Go)
	}
	e2 := newEnv(t, AuthConfig{})
	decode(t, e2.do(t, "GET", "/api/about", "", nil).body, &a)
	if a.Go != runtime.Version() {
		t.Errorf("auth=none about go = %q", a.Go)
	}
}
