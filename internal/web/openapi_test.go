package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// openAPIDoc fetches and decodes the document of e.
func openAPIDoc(t *testing.T, e *env) map[string]any {
	t.Helper()
	r := e.do(t, "GET", "/api/openapi.json", "", nil)
	wantCode(t, r, 200)
	var doc map[string]any
	decode(t, r.body, &doc)
	return doc
}

// TestOpenAPICoversRoutes: every route of the table is an operation of
// the document and resolves on the mux to its own pattern; every
// operation of the document is a route of the table (the two catch-alls
// are not operations). The route table is the only declaration, so a
// route the mux serves without an operation, or an operation nothing
// serves, cannot exist.
func TestOpenAPICoversRoutes(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	doc := openAPIDoc(t, e)
	paths, _ := doc["paths"].(map[string]any)
	if len(paths) == 0 {
		t.Fatal("no paths")
	}
	table := e.srv.routeTable()
	seen := map[string]bool{}
	for _, rt := range table {
		pat := rt.pattern()
		if seen[pat] {
			t.Errorf("%s declared twice", pat)
		}
		seen[pat] = true
		ops, _ := paths[rt.Path].(map[string]any)
		if _, ok := ops[strings.ToLower(rt.Method)]; !ok {
			t.Errorf("%s not in the document", pat)
		}
		// the mux serves the pattern of the table entry
		req := httptest.NewRequest(rt.Method, "http://127.0.0.1"+samplePath(rt.Path), nil)
		if _, got := e.srv.mux.Handler(req); got != pat {
			t.Errorf("%s resolves on the mux to %q", pat, got)
		}
		if rt.Handler == nil || rt.Summary == "" || len(rt.Responses) == 0 {
			t.Errorf("%s: handler, summary and responses are mandatory", pat)
		}
	}
	// the other direction: every operation is a table entry that the mux serves
	n := 0
	for path, v := range paths {
		ops, _ := v.(map[string]any)
		for method := range ops {
			n++
			pat := strings.ToUpper(method) + " " + path
			if !seen[pat] {
				t.Errorf("document operation %s is not in the route table", pat)
			}
			req := httptest.NewRequest(strings.ToUpper(method), "http://127.0.0.1"+samplePath(path), nil)
			if _, got := e.srv.mux.Handler(req); got != pat {
				t.Errorf("document operation %s is served by %q", pat, got)
			}
		}
	}
	if n != len(table) {
		t.Errorf("%d operations, %d routes", n, len(table))
	}
	// the catch-alls are not operations
	for _, p := range []string{"/api/", "/"} {
		if _, ok := paths[p]; ok {
			t.Errorf("catch-all %q listed as an operation", p)
		}
	}
	// every registered pattern maps to its table entry (the guard's lookup)
	if len(e.srv.byPattern) != len(table) {
		t.Errorf("byPattern has %d entries, table %d", len(e.srv.byPattern), len(table))
	}
}

var samplePathParam = regexp.MustCompile(`\{[a-z_]+\}`)

// samplePath replaces every {param} by a concrete segment.
func samplePath(p string) string { return samplePathParam.ReplaceAllString(p, "x") }

// TestOpenAPIShape: version, servers, security schemes, every operation
// with summary, operationId, responses (error codes referencing Error),
// x-class and x-scope from the fixed sets; request bodies reference
// existing schemas; the DESIGN component schemas exist.
func TestOpenAPIShape(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	doc := openAPIDoc(t, e)
	if doc["openapi"] != "3.1.0" {
		t.Errorf("openapi = %v", doc["openapi"])
	}
	info, _ := doc["info"].(map[string]any)
	if info["title"] != "n5-fangov API" || info["version"] != "1.2.3-test" {
		t.Errorf("info = %v", info)
	}
	if servers, _ := doc["servers"].([]any); len(servers) != 1 || servers[0].(map[string]any)["url"] != "/" {
		t.Errorf("servers = %v", doc["servers"])
	}
	comps, _ := doc["components"].(map[string]any)
	schemes, _ := comps["securitySchemes"].(map[string]any)
	for name, want := range map[string]map[string]string{
		"bearer": {"type": "http", "scheme": "bearer"},
		"basic":  {"type": "http", "scheme": "basic"},
		"cookie": {"type": "apiKey", "in": "cookie", "name": sessionCookie},
	} {
		got, _ := schemes[name].(map[string]any)
		for k, v := range want {
			if got[k] != v {
				t.Errorf("securitySchemes.%s.%s = %v, want %v", name, k, got[k], v)
			}
		}
	}
	schemas, _ := comps["schemas"].(map[string]any)
	for _, name := range []string{"Error", "State", "Channel", "HistoryPoint", "Override", "Version", "Session", "Token", "TokenCreate", "TokenCreated", "Alerts", "AlertsUpdate", "Schedules", "Dashboard"} {
		if _, ok := schemas[name]; !ok {
			t.Errorf("schema %s missing", name)
		}
	}
	// Version.limits lists exactly the keys GET /api/version carries
	var limitKeys map[string]any
	lb, _ := json.Marshal(apiLimits())
	_ = json.Unmarshal(lb, &limitKeys)
	version, _ := schemas["Version"].(map[string]any)
	vprops, _ := version["properties"].(map[string]any)
	limits, _ := vprops["limits"].(map[string]any)
	lprops, _ := limits["properties"].(map[string]any)
	for k := range limitKeys {
		if _, ok := lprops[k]; !ok {
			t.Errorf("Version.limits schema lacks %q (apiLimits carries it)", k)
		}
	}
	for k := range lprops {
		if _, ok := limitKeys[k]; !ok {
			t.Errorf("Version.limits schema lists %q, apiLimits does not", k)
		}
	}
	paths, _ := doc["paths"].(map[string]any)
	scopes := map[string]bool{"read": true, "control": true, "admin": true, "session": true}
	classes := map[string]bool{"public": true, "filtered": true, "protected": true}
	ids := map[string]bool{}
	for path, v := range paths {
		ops, _ := v.(map[string]any)
		for method, o := range ops {
			op, _ := o.(map[string]any)
			what := strings.ToUpper(method) + " " + path
			if s, _ := op["summary"].(string); s == "" {
				t.Errorf("%s: no summary", what)
			}
			id, _ := op["operationId"].(string)
			if id == "" || ids[id] {
				t.Errorf("%s: operationId %q missing or duplicate", what, id)
			}
			ids[id] = true
			if !scopes[op["x-scope"].(string)] {
				t.Errorf("%s: x-scope %v", what, op["x-scope"])
			}
			if !classes[op["x-class"].(string)] {
				t.Errorf("%s: x-class %v", what, op["x-class"])
			}
			if _, ok := op["security"].([]any); !ok {
				t.Errorf("%s: no security member", what)
			}
			responses, _ := op["responses"].(map[string]any)
			if len(responses) == 0 {
				t.Errorf("%s: no responses", what)
			}
			for code, rv := range responses {
				resp, _ := rv.(map[string]any)
				if d, _ := resp["description"].(string); d == "" {
					t.Errorf("%s %s: no description", what, code)
				}
				if code >= "400" {
					if ref := schemaRef(resp); ref != "#/components/schemas/Error" {
						t.Errorf("%s %s: error response references %q", what, code, ref)
					}
				} else if ref := schemaRef(resp); ref != "" {
					if _, ok := schemas[strings.TrimPrefix(ref, "#/components/schemas/")]; !ok {
						t.Errorf("%s %s: unknown schema %s", what, code, ref)
					}
				}
			}
			if body, ok := op["requestBody"].(map[string]any); ok {
				content, _ := body["content"].(map[string]any)
				if len(content) != 1 {
					t.Errorf("%s: request body with %d content types", what, len(content))
				}
				for ct, cv := range content {
					ref := schemaRef(map[string]any{"content": map[string]any{ct: cv}})
					if ref != "" {
						if _, ok := schemas[strings.TrimPrefix(ref, "#/components/schemas/")]; !ok {
							t.Errorf("%s: body schema %s missing", what, ref)
						}
					}
				}
			}
			// path parameters are declared
			for _, m := range pathParam.FindAllStringSubmatch(path, -1) {
				found := false
				params, _ := op["parameters"].([]any)
				for _, pv := range params {
					p, _ := pv.(map[string]any)
					if p["name"] == m[1] && p["in"] == "path" && p["required"] == true {
						found = true
					}
				}
				if !found {
					t.Errorf("%s: path parameter %s not declared", what, m[1])
				}
			}
		}
	}
	// spot checks of the scope column against DESIGN
	want := map[string]string{
		"GET /api/tls": "read", "GET /api/tls/cert.crt": "admin", "GET /api/tls/cert.cer": "admin",
		"GET /api/state": "read", "PUT /api/override/{name}": "control", "PUT /api/dashboard": "control",
		"POST /api/presets/{name}/apply": "control", "PUT /api/config": "admin", "GET /api/config": "admin",
		"POST /api/tokens": "session", "GET /api/account": "session", "POST /api/login": "session",
	}
	for pat, scope := range want {
		method, path, _ := strings.Cut(pat, " ")
		op, _ := paths[path].(map[string]any)[strings.ToLower(method)].(map[string]any)
		if op == nil || op["x-scope"] != scope {
			t.Errorf("%s: x-scope %v, want %s", pat, op["x-scope"], scope)
		}
	}
	// session-only operations list no bearer scheme, public ones none at all
	for path, v := range paths {
		ops, _ := v.(map[string]any)
		for method, o := range ops {
			op, _ := o.(map[string]any)
			sec, _ := op["security"].([]any)
			hasBearer := false
			for _, sv := range sec {
				if _, ok := sv.(map[string]any)["bearer"]; ok {
					hasBearer = true
				}
			}
			switch {
			case op["x-class"] == "public" && len(sec) != 0:
				t.Errorf("%s %s: public with security %v", method, path, sec)
			case op["x-scope"] == "session" && hasBearer:
				t.Errorf("%s %s: session-only lists bearer", method, path)
			case op["x-class"] == "protected" && op["x-scope"] != "session" && !hasBearer:
				t.Errorf("%s %s: protected without bearer", method, path)
			}
		}
	}
}

// schemaRef returns the $ref of the first content schema of a response
// or request body object, "" when there is none.
func schemaRef(obj map[string]any) string {
	content, _ := obj["content"].(map[string]any)
	for _, cv := range content {
		c, _ := cv.(map[string]any)
		schema, _ := c["schema"].(map[string]any)
		if ref, ok := schema["$ref"].(string); ok {
			return ref
		}
		if items, ok := schema["items"].(map[string]any); ok {
			if ref, ok := items["$ref"].(string); ok {
				return ref
			}
		}
	}
	return ""
}

// TestOpenAPIPublic: anonymous 200 with auth = basic, no-store, a strong
// ETag that answers 304 to If-None-Match; the document is valid JSON and
// stable across requests.
func TestOpenAPIPublic(t *testing.T) {
	e := newEnv(t, adminBasic)
	r := e.do(t, "GET", "/api/openapi.json", "", nil)
	wantCode(t, r, 200)
	if !json.Valid([]byte(r.body)) {
		t.Fatalf("invalid JSON: %.200s", r.body)
	}
	if ct := r.hdr.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type %q", ct)
	}
	if r.hdr.Get("Cache-Control") != "no-store" {
		t.Errorf("cache-control %q", r.hdr.Get("Cache-Control"))
	}
	etag := r.hdr.Get("ETag")
	if !regexp.MustCompile(`^"[0-9a-f]{64}"$`).MatchString(etag) {
		t.Errorf("etag %q", etag)
	}
	if etag != etagOf([]byte(r.body)) {
		t.Errorf("etag is not the sha256 of the body")
	}
	r2 := e.do(t, "GET", "/api/openapi.json", "", map[string]string{"If-None-Match": etag})
	if r2.code != http.StatusNotModified || r2.body != "" || r2.hdr.Get("ETag") != etag {
		t.Errorf("if-none-match: %d %q %q", r2.code, r2.body, r2.hdr.Get("ETag"))
	}
	if r3 := e.do(t, "GET", "/api/openapi.json", "", nil); r3.body != r.body {
		t.Errorf("document changed between requests")
	}
	// the document names the daemon version and is also served to a token
	if !strings.Contains(r.body, `"version": "1.2.3-test"`) {
		t.Errorf("version missing")
	}
	// the page and the CSRF rule apply to nobody here: a HEAD is fine, a POST is 405
	wantCode(t, e.do(t, "HEAD", "/api/openapi.json", "", nil), 200)
	wantError(t, e.do(t, "POST", "/api/openapi.json", "", csrf), 405, "method not allowed")
}
