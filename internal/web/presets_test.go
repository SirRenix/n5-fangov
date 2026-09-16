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

// TestPresetStatusCodes: save over a built-in → 409 (R-U8), apply a
// preset the store reports as missing → 404 (R-L8).
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

// TestApplyPresetReloadFailed500 (L7): a preset that was written but not
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
