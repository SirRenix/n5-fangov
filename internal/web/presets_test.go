package web

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

// fakePresetDeleter adds Delete to fakePresets; the plain fakePresets is the
// 501 case.
type fakePresetDeleter struct {
	fakePresets
	deleted []string
}

func (p *fakePresetDeleter) Delete(name string) error {
	for _, e := range p.list {
		if e.Name == name {
			if e.Builtin {
				return ErrPresetBuiltin
			}
			p.deleted = append(p.deleted, name)
			return nil
		}
	}
	return fs.ErrNotExist
}

func TestPresetDelete(t *testing.T) {
	e, _, _, _ := storesEnv(t, adminBasic)
	ok := basicAuth("admin", "pw")
	wantError(t, e.do(t, "DELETE", "/api/presets/quiet", "", csrf), 401, "authentication")
	wantError(t, e.do(t, "DELETE", "/api/presets/nope", "", ok), 404, "unknown preset")
	wantError(t, e.do(t, "DELETE", "/api/presets/n5pro-balanced", "", ok), 409, "built-in")
	wantError(t, e.do(t, "DELETE", "/api/presets/Bad!", "", ok), 400, "invalid preset name")
	r := e.do(t, "DELETE", "/api/presets/quiet", "", ok)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"deleted":"quiet"`) || !strings.Contains(e.logLines(), `web: preset "quiet" deleted by 127.0.0.1`) {
		t.Errorf("delete = %s / %q", r.body, e.logLines())
	}
	if d := e.srv.deps.Presets.(*fakePresetDeleter); len(d.deleted) != 1 || d.deleted[0] != "quiet" {
		t.Errorf("deleted = %v", d.deleted)
	}
}

func (p *fakePresetDeleter) Detail(name string) (PresetDetail, error) {
	for _, e := range p.list {
		if e.Name == name {
			return PresetDetail{Name: name, Builtin: e.Builtin, Channels: []PresetChannel{{Name: "cpu", PWM: 1, Sensor: "k10temp", Curve: [][2]int{{40, 80}, {80, 255}}, Critical: 88, Stop: "auto"}}}, nil
		}
	}
	return PresetDetail{}, fs.ErrNotExist
}

func (p *fakePresetDeleter) Rename(oldName, newName string) error {
	for _, e := range p.list {
		if e.Name == newName {
			if e.Builtin {
				return ErrPresetBuiltin
			}
			return fs.ErrExist
		}
	}
	for i, e := range p.list {
		if e.Name == oldName {
			if e.Builtin {
				return ErrPresetBuiltin
			}
			p.list[i].Name = newName
			return nil
		}
	}
	return fs.ErrNotExist
}

// TestPresetDetailRename: GET /api/presets/{name} shows the tables, rename
// refuses built-ins and taken names, both are protected.
func TestPresetDetailRename(t *testing.T) {
	e, _, _, _ := storesEnv(t, adminBasic)
	wantCode(t, e.do(t, "GET", "/api/presets/quiet", "", nil), 401)
	r := e.do(t, "GET", "/api/presets/quiet", "", basicAuth("admin", "pw"))
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"curve":[[40,80],[80,255]]`) || !strings.Contains(r.body, `"sensor":"k10temp"`) {
		t.Errorf("detail = %s", r.body)
	}
	wantError(t, e.do(t, "GET", "/api/presets/none", "", basicAuth("admin", "pw")), 404, "unknown preset")
	wantError(t, e.do(t, "GET", "/api/presets/Bad%20Name", "", basicAuth("admin", "pw")), 400, "invalid")
	auth := basicAuth("admin", "pw")
	auth[CSRFHeader] = "1"
	wantError(t, e.do(t, "POST", "/api/presets/quiet/rename", `{"name":"n5pro-balanced"}`, auth), 409, "built-in")
	wantError(t, e.do(t, "POST", "/api/presets/n5pro-balanced/rename", `{"name":"x"}`, auth), 409, "built-in")
	wantError(t, e.do(t, "POST", "/api/presets/none/rename", `{"name":"x"}`, auth), 404, "unknown preset")
	wantError(t, e.do(t, "POST", "/api/presets/quiet/rename", `{"name":"Bad Name"}`, auth), 400, "invalid")
	r = e.do(t, "POST", "/api/presets/quiet/rename", `{"name":"silent"}`, auth)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"name":"silent"`) {
		t.Errorf("rename = %s", r.body)
	}
	wantCode(t, e.do(t, "GET", "/api/presets/silent", "", basicAuth("admin", "pw")), 200)
}

// TestPresetStatusCodes: save over a built-in → 409, apply a
// preset the store reports as missing → 404.
func TestPresetStatusCodes(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.presets.saveErr = fmt.Errorf("preset %q: %w", "n5pro-quiet", ErrPresetBuiltin)
	wantError(t, e.do(t, "PUT", "/api/presets/n5pro-quiet", "", csrf), 409, "built-in")
	e.presets.saveErr = fmt.Errorf("disk full")
	wantError(t, e.do(t, "PUT", "/api/presets/mine", "", csrf), 400, "disk full")
	e.presets.applyErr = fmt.Errorf("preset %q: built-in for profile n5pro, not nct67xx: %w", "n5pro-quiet", fs.ErrNotExist)
	wantError(t, e.do(t, "POST", "/api/presets/n5pro-quiet/apply", "", csrf), 404, "unknown preset n5pro-quiet")
	e.presets.applyErr = fmt.Errorf("no usable channel")
	wantError(t, e.do(t, "POST", "/api/presets/mine/apply", "", csrf), 400, "no usable channel")
}

// TestApplyPresetReloadFailed500: a preset that was written but not
// taken by the daemon answers 500 with the store's text; a refusal stays
// 400 and a write failure 500 as before.
func TestApplyPresetReloadFailed500(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.presets.applyErr = fmt.Errorf("preset quiet written, %w", &ReloadError{Err: errors.New("channel hdd: pwm3 not writable")})
	r := e.do(t, "POST", "/api/presets/quiet/apply", "", csrf)
	wantError(t, r, 500, "apply preset: preset quiet written, reload failed: channel hdd: pwm3 not writable")
	e.presets.applyErr = errors.New(`preset "quiet" contains no usable [[channel]] table`)
	wantCode(t, e.do(t, "POST", "/api/presets/quiet/apply", "", csrf), 400)
	e.presets.applyErr = fmt.Errorf("current config: %w", ErrStore)
	wantCode(t, e.do(t, "POST", "/api/presets/quiet/apply", "", csrf), 500)
	if !isReloadError(fmt.Errorf("x: %w", &ReloadError{Err: errors.New("y")})) || isReloadError(errors.New("reload failed: y")) {
		t.Error("isReloadError classification")
	}
}

// TestPresetSaveBody: PUT /api/presets/{name} with a JSON body stores the
// composed channels (the preset editor) — validated with the config's
// channel rules (400 {error, errors[]}), the channel set must be the
// running config's, a built-in name is 409, an empty body keeps saving the
// running tables.
func TestPresetSaveBody(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.presets.list = append(e.presets.list, Preset{Name: "n5pro-balanced", Builtin: true, Channels: []string{"cpu"}})
	good := `{"channels":[{"name":"cpu","pwm":1,"sensor":"k10temp","curve":[[40,80],[75,255]],"critical":85,"stop":"auto","hysteresis":2,"min_on":"1m0s"}]}`
	r := e.do(t, "PUT", "/api/presets/summer", good, csrf)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"saved":"summer"`) {
		t.Errorf("save = %s", r.body)
	}
	got := e.presets.chans["summer"]
	if len(got) != 1 || got[0].Name != "cpu" || got[0].Critical != 85 || got[0].Hysteresis != 2 || got[0].MinOn.String() != "1m0s" || len(got[0].Curve) != 2 || got[0].Curve[1][1] != 255 {
		t.Errorf("saved channels = %+v", got)
	}
	if len(e.presets.saved) != 0 {
		t.Errorf("Save (running tables) called with a body: %v", e.presets.saved)
	}
	// a bad curve: duty falls → 400 with the parser's message in errors[]
	r = e.do(t, "PUT", "/api/presets/summer", `{"channels":[{"name":"cpu","pwm":1,"sensor":"k10temp","curve":[[40,200],[75,100]],"critical":85,"stop":"auto"}]}`, csrf)
	wantCode(t, r, 400)
	var m struct {
		Error  string   `json:"error"`
		Errors []string `json:"errors"`
	}
	decode(t, r.body, &m)
	if m.Error != "preset rejected" || len(m.Errors) != 1 || !strings.Contains(m.Errors[0], "channel.cpu.curve") {
		t.Errorf("bad curve = %s", r.body)
	}
	// critical below the last point, stop below 60, hysteresis above 10: every warning is an error
	r = e.do(t, "PUT", "/api/presets/summer", `{"channels":[{"name":"cpu","pwm":1,"sensor":"k10temp","curve":[[40,80],[75,255]],"critical":70,"stop":"20","hysteresis":11}]}`, csrf)
	wantCode(t, r, 400)
	decode(t, r.body, &m)
	if len(m.Errors) != 3 {
		t.Errorf("three rule errors expected, got %v", m.Errors)
	}
	// min_on that is not a duration, and a channel set other than the running config's
	for _, tc := range []struct{ body, want string }{
		{`{"channels":[{"name":"cpu","pwm":1,"sensor":"k10temp","curve":[[40,80],[75,255]],"critical":85,"min_on":"soon"}]}`, "not a duration"},
		{`{"channels":[{"name":"hdd","pwm":3,"sensor":"drivetemp:max","curve":[[36,105],[46,255]],"critical":56}]}`, "do not match the running config"},
		// the running config's name on another pwm: Apply merges by pwm, so this would swap curves
		{`{"channels":[{"name":"cpu","pwm":2,"sensor":"k10temp","curve":[[40,80],[75,255]],"critical":85}]}`, "do not match the running config"},
		// a point that is not [temp, duty] is an error, not [40, 0] or a truncated triple
		{`{"channels":[{"name":"cpu","pwm":1,"sensor":"k10temp","curve":[[40],[75,255]],"critical":85}]}`, "need [temp, duty]"},
		{`{"channels":[{"name":"cpu","pwm":1,"sensor":"k10temp","curve":[[40,80,1],[75,255]],"critical":85}]}`, "need [temp, duty]"},
		{`{"channels":[]}`, "none given"},
	} {
		r = e.do(t, "PUT", "/api/presets/summer", tc.body, csrf)
		wantCode(t, r, 400)
		decode(t, r.body, &m)
		if len(m.Errors) != 1 || !strings.Contains(m.Errors[0], tc.want) {
			t.Errorf("%s: errors = %v, want %q", tc.body, m.Errors, tc.want)
		}
	}
	wantError(t, e.do(t, "PUT", "/api/presets/summer", `{"channels":[{"name":"cpu"}],"description":"x"}`, csrf), 400, "invalid JSON body")
	// trailing data after the object (a second document, a stray bracket) is not ignored
	wantError(t, e.do(t, "PUT", "/api/presets/summer", good+" {}", csrf), 400, "trailing data")
	wantError(t, e.do(t, "PUT", "/api/presets/summer", good+"]", csrf), 400, "trailing data")
	if len(e.presets.chans) != 1 {
		t.Errorf("a rejected body was stored: %v", e.presets.chans)
	}
	wantError(t, e.do(t, "PUT", "/api/presets/n5pro-balanced", good, csrf), 409, "built-in")
	// the empty body path is unchanged
	wantCode(t, e.do(t, "PUT", "/api/presets/night", "", csrf), 200)
	if len(e.presets.saved) != 1 || e.presets.saved[0] != "night" {
		t.Errorf("saved = %v", e.presets.saved)
	}
	if !strings.Contains(e.logLines(), `web: preset "summer" saved by`) {
		t.Errorf("log = %q", e.logLines())
	}
}
