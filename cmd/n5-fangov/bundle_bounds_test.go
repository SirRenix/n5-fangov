package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/web"
)

// TestBundleFormatOverHTTP: the "format" check lives in the cmd bundle,
// not in the handler — over POST /api/config/import a bundle with format
// 2 (and 0, absent) answers 400 with the bundle's text and writes nothing;
// format 1 with the same content is imported (AUDIT 7 bounds list).
func TestBundleFormatOverHTTP(t *testing.T) {
	cfgPath, presetDir := bundleDirs(t)
	before, _ := os.ReadFile(cfgPath)
	var reloads atomic.Int32
	b := fileBundle{cfgPath: cfgPath, presetDir: presetDir, reload: func([]byte) error { reloads.Add(1); return nil }}
	srv := web.New(web.Deps{Bundle: b, Logf: func(string, ...any) {}})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	post := func(body string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/config/import", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(web.CSRFHeader, "1")
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%d %s: %v", res.StatusCode, raw, err)
		}
		return res.StatusCode, m
	}
	mk := func(format int) string {
		d, _ := json.Marshal(settingsBundle{Format: format, Config: "[daemon]\ninterval = \"5s\"\n", Presets: map[string]string{}})
		return string(d)
	}
	for _, format := range []int{2, 0, -1, bundleFormat + 1} {
		code, m := post(mk(format))
		msg, _ := m["error"].(string)
		if code != 400 || !strings.Contains(msg, "import rejected: bundle: format "+strconv.Itoa(format)+" not supported (want 1)") {
			t.Errorf("format %d: %d %v", format, code, m)
		}
		errs, _ := m["errors"].([]any)
		if len(errs) != 1 {
			t.Errorf("format %d: errors list %v", format, errs)
		}
	}
	// "format" absent decodes as 0 → same refusal
	if code, m := post(`{"config":"[daemon]\n","presets":{}}`); code != 400 || !strings.Contains(m["error"].(string), "format 0 not supported") {
		t.Errorf("absent format: %d %v", code, m)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) || reloads.Load() != 0 {
		t.Fatalf("refused bundle changed the config (reloads %d)", reloads.Load())
	}
	// format 1: imported and reloaded
	if code, m := post(mk(bundleFormat)); code != 200 || m["ok"] != true || m["restart_required"] != false {
		t.Errorf("format 1: %d %v", code, m)
	}
	if after, _ := os.ReadFile(cfgPath); !strings.Contains(string(after), "interval = \"5s\"") || reloads.Load() != 1 {
		t.Errorf("format 1 not imported: reloads %d\n%s", reloads.Load(), after)
	}
}
