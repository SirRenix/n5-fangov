// openapi.go holds the route table — the only place an API route is
// declared: routes() registers from it, the guard takes visibility and
// token scope from it, and GET /api/openapi.json renders it as an
// OpenAPI 3.1 document once at start (DESIGN.md "OpenAPI").
package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// class is the visibility of a route with auth = basic (DESIGN.md
// "Visibility").
type class int

const (
	classPublic    class = iota // full for everyone
	classFiltered               // reduced for anonymous callers, full when signed in
	classProtected              // 401 anonymous
)

func (c class) String() string {
	switch c {
	case classPublic:
		return "public"
	case classFiltered:
		return "filtered"
	}
	return "protected"
}

// scope is the token scope a route needs. scopeSession marks the
// endpoints no token may use (tokens, account, login, logout).
type scope string

const (
	scopeRead    scope = ScopeRead
	scopeControl scope = ScopeControl
	scopeAdmin   scope = ScopeAdmin
	scopeSession scope = "session"
)

// bodySpec describes a request body: the content type and, for JSON,
// the component schema it follows ("" = free-form).
type bodySpec struct {
	ContentType string
	Schema      string
	Description string
}

// paramSpec is one query parameter.
type paramSpec struct {
	Name        string
	Type        string // integer | string | boolean
	Description string
}

// route is one API endpoint.
type route struct {
	Method, Path string
	Handler      http.HandlerFunc
	Class        class
	Scope        scope
	Summary      string
	Body         *bodySpec
	// Responses maps a status code to its description; codes ≥ 400
	// reference the Error schema in the document.
	Responses map[int]string
	// Response names the component schema of the 2xx JSON body ("" =
	// unspecified).
	Response string
	Params   []paramSpec
}

// pattern is the mux registration string.
func (r route) pattern() string { return r.Method + " " + r.Path }

// routeTable declares every API route. Order is the order of the
// document; the two catch-alls ("/api/" fallback, "/" static) are not
// routes and are registered by routes() itself.
func (s *Server) routeTable() []route {
	jsonBody := func(schema, desc string) *bodySpec {
		return &bodySpec{ContentType: "application/json", Schema: schema, Description: desc}
	}
	ok := func(desc string) map[int]string { return map[int]string{200: desc} }
	return []route{
		// ---- public ----------------------------------------------------
		{Method: "GET", Path: "/api/version", Handler: s.getVersion, Class: classPublic, Scope: scopeRead,
			Summary: "Daemon name, version, TLS and auth mode, validation limits", Response: "Version", Responses: ok("version card")},
		{Method: "GET", Path: "/api/about", Handler: s.getAbout, Class: classPublic, Scope: scopeRead,
			Summary: "Project card: licence, repository, author, credits", Responses: ok("about card")},
		{Method: "GET", Path: "/api/session", Handler: s.getSession, Class: classPublic, Scope: scopeRead,
			Summary: "Who the caller is (cookie, basic, bearer, socket or anonymous)", Response: "Session", Responses: ok("session view")},
		{Method: "GET", Path: "/api/openapi.json", Handler: s.getOpenAPI, Class: classPublic, Scope: scopeRead,
			Summary: "This document", Responses: map[int]string{200: "OpenAPI 3.1 document", 304: "unchanged (ETag)"}},
		{Method: "POST", Path: "/api/login", Handler: s.login, Class: classPublic, Scope: scopeSession,
			Summary: "Sign in with user and password; sets the session cookie", Body: jsonBody("Login", "credentials"),
			Responses: map[int]string{200: "signed in; Set-Cookie", 401: "invalid user or password", 403: "CSRF header missing, or the caller is a token", 409: "auth is none", 429: "too many attempts"}},
		{Method: "POST", Path: "/api/logout", Handler: s.logout, Class: classPublic, Scope: scopeSession,
			Summary: "Revoke the cookie session and clear the cookie", Responses: map[int]string{204: "signed out", 403: "CSRF header missing, or the caller is a token"}},
		// ---- filtered --------------------------------------------------
		{Method: "GET", Path: "/api/state", Handler: s.getState, Class: classFiltered, Scope: scopeRead,
			Summary: "Current snapshot: status, channels, extra temperatures, alerts (reduced for anonymous callers)", Response: "State", Responses: ok("snapshot")},
		{Method: "GET", Path: "/api/history", Handler: s.getHistory, Class: classFiltered, Scope: scopeRead,
			Summary: "History points of the last minutes (extra sensors only when signed in)", Response: "HistoryPoint",
			Params:    []paramSpec{{Name: "minutes", Type: "integer", Description: "span in minutes, 1..1440 (default 120)"}, {Name: "since", Type: "integer", Description: "only points with ts > since (unix seconds)"}},
			Responses: map[int]string{200: "array of history points", 400: "bad query"}},
		// ---- protected, read ---------------------------------------------
		{Method: "GET", Path: "/api/system", Handler: s.getSystem, Class: classProtected, Scope: scopeRead,
			Summary: "Hardware inventory (host, machine, CPU, memory, GPU/NPU, NICs, storage with live disk temperatures)", Responses: map[int]string{200: "sysinfo document", 501: "no collector"}},
		{Method: "GET", Path: "/api/sensors", Handler: s.getSensors, Class: classProtected, Scope: scopeRead,
			Summary: "Selectable sensor sources with live readings", Responses: map[int]string{200: "array of sensors", 501: "no sensor list"}},
		{Method: "GET", Path: "/api/profiles", Handler: s.getProfiles, Class: classProtected, Scope: scopeRead,
			Summary: "Hardware profiles with the active one marked", Responses: map[int]string{200: "array of profiles", 501: "no profile list"}},
		{Method: "GET", Path: "/api/presets", Handler: s.getPresets, Class: classProtected, Scope: scopeRead,
			Summary: "Curve presets (built-in and user files)", Responses: map[int]string{200: "array of presets", 501: "no preset store"}},
		{Method: "GET", Path: "/api/presets/{name}", Handler: s.getPreset, Class: classProtected, Scope: scopeRead,
			Summary: "One preset with its channel tables", Responses: map[int]string{200: "preset detail", 400: "invalid name", 404: "unknown preset", 501: "no preset store"}},
		{Method: "GET", Path: "/api/alerts", Handler: s.getAlerts, Class: classProtected, Scope: scopeRead,
			Summary: "Alert transport status, kinds, last delivery per kind and the recent alerts", Response: "Alerts", Responses: map[int]string{200: "alert status", 501: "no alert manager"}},
		{Method: "GET", Path: "/api/dashboard", Handler: s.getDashboard, Class: classProtected, Scope: scopeRead,
			Summary: "Extra sensors recorded for the dashboard chart", Response: "Dashboard", Responses: map[int]string{200: "sensor ids", 501: "no dashboard store"}},
		{Method: "GET", Path: "/api/tls", Handler: s.getTLS, Class: classProtected, Scope: scopeRead,
			Summary: "Certificate mode, info, covered hosts and warnings", Responses: map[int]string{200: "certificate view", 501: "no certificate manager"}},
		// ---- protected, control ------------------------------------------
		{Method: "PUT", Path: "/api/override/{name}", Handler: s.putOverride, Class: classProtected, Scope: scopeControl,
			Summary: "Manual duty override for one channel (critical and stall protection stay active)", Body: jsonBody("Override", "duty 0..255 or percent 0..100"),
			Responses: map[int]string{200: "override applied on the next cycle", 400: "invalid body or duty below the channel's minimum", 404: "unknown channel", 413: "body too large"}},
		{Method: "DELETE", Path: "/api/override/{name}", Handler: s.deleteOverride, Class: classProtected, Scope: scopeControl,
			Summary: "Return one channel to its curve", Responses: map[int]string{200: "override cleared", 400: "invalid name", 404: "unknown channel"}},
		{Method: "POST", Path: "/api/presets/{name}/apply", Handler: s.applyPreset, Class: classProtected, Scope: scopeControl,
			Summary:   "Apply a preset: its channels replace the config channels with the same pwm, then reload",
			Responses: map[int]string{200: "applied", 202: "written, restart required", 400: "invalid name", 404: "unknown preset", 500: "written but not reloaded", 501: "no preset store"}},
		{Method: "PUT", Path: "/api/dashboard", Handler: s.putDashboard, Class: classProtected, Scope: scopeControl,
			Summary: "Set the extra sensors recorded for the dashboard chart", Body: jsonBody("Dashboard", "sensor ids, at most 8"), Response: "Dashboard",
			Responses: map[int]string{200: "saved; unresolvable ids reported as warnings", 400: "too many or malformed ids", 500: "config not written", 501: "no dashboard store"}},
		// ---- protected, admin --------------------------------------------
		{Method: "GET", Path: "/api/config", Handler: s.getConfig, Class: classProtected, Scope: scopeAdmin,
			Summary: "Config file text and parsed values (password_hash redacted)", Responses: map[int]string{200: "raw and parsed config", 500: "config unreadable", 501: "no config store"}},
		{Method: "PUT", Path: "/api/config", Handler: s.putConfig, Class: classProtected, Scope: scopeAdmin,
			Summary:   "Replace the config file (validated first) and reload; ?strict=1 refuses channel values that would fall back to defaults",
			Body:      &bodySpec{ContentType: "application/toml", Description: "the whole config file, at most 256 KiB"},
			Params:    []paramSpec{{Name: "strict", Type: "boolean", Description: "1: refuse [[channel]] values that would be replaced by defaults"}},
			Responses: map[int]string{200: "written and reloaded", 202: "written, restart required", 400: "rejected (syntax error or strict warnings), nothing written", 413: "body too large", 500: "written but not reloaded", 501: "no config store"}},
		{Method: "GET", Path: "/api/config/export", Handler: s.exportConfig, Class: classProtected, Scope: scopeAdmin,
			Summary: "Settings bundle (config and presets, hash redacted) as a JSON attachment", Responses: map[int]string{200: "attachment n5-fangov-settings-<ts>.json", 500: "export failed", 501: "no bundle"}},
		{Method: "POST", Path: "/api/config/import", Handler: s.importConfig, Class: classProtected, Scope: scopeAdmin,
			Summary: "Restore a settings bundle (everything validated before anything is written)", Body: jsonBody("", "the bundle as exported"),
			Responses: map[int]string{200: "restored and reloaded", 202: "restored, restart required", 400: "import rejected", 413: "body too large", 500: "import failed (write)", 501: "no bundle"}},
		{Method: "PUT", Path: "/api/presets/{name}", Handler: s.savePreset, Class: classProtected, Scope: scopeAdmin,
			Summary: "Save the current channel tables as a preset", Responses: map[int]string{200: "saved", 400: "invalid name", 409: "built-in name", 500: "not written", 501: "no preset store"}},
		{Method: "POST", Path: "/api/presets/{name}/rename", Handler: s.renamePreset, Class: classProtected, Scope: scopeAdmin,
			Summary: "Rename a user preset", Body: jsonBody("PresetRename", "the new name"),
			Responses: map[int]string{200: "renamed", 400: "invalid name", 404: "unknown preset", 409: "built-in or target exists", 501: "no preset store"}},
		{Method: "DELETE", Path: "/api/presets/{name}", Handler: s.deletePreset, Class: classProtected, Scope: scopeAdmin,
			Summary: "Delete a user preset", Responses: map[int]string{200: "deleted", 400: "invalid name", 404: "unknown preset", 409: "built-in", 501: "no preset store"}},
		{Method: "GET", Path: "/api/log", Handler: s.getLog, Class: classProtected, Scope: scopeAdmin,
			Summary: "Newest log lines", Params: []paramSpec{{Name: "lines", Type: "integer", Description: "1..5000 (default 100)"}},
			Responses: map[int]string{200: "lines and their source (file or journal)", 400: "bad query", 500: "log unreadable", 501: "no log source"}},
		{Method: "GET", Path: "/api/log/export", Handler: s.exportLog, Class: classProtected, Scope: scopeAdmin,
			Summary: "The current log file as a text attachment", Responses: map[int]string{200: "attachment n5-fangov-<host>-<ts>.log", 501: "no log source"}},
		{Method: "DELETE", Path: "/api/log", Handler: s.clearLog, Class: classProtected, Scope: scopeAdmin,
			Summary: "Truncate the current log file (rotated files and the journal stay)", Responses: map[int]string{200: "cleared", 500: "clear failed", 501: "no log file"}},
		{Method: "GET", Path: "/api/tls/cert.crt", Handler: s.tlsCertPEM, Class: classProtected, Scope: scopeAdmin,
			Summary: "The served certificate as PEM attachment", Responses: map[int]string{200: "attachment cert.crt", 409: "tls is off", 500: "export failed", 501: "no certificate manager"}},
		{Method: "GET", Path: "/api/tls/cert.cer", Handler: s.tlsCertDER, Class: classProtected, Scope: scopeAdmin,
			Summary: "The served certificate as DER attachment", Responses: map[int]string{200: "attachment cert.cer", 409: "tls is off", 500: "export failed", 501: "no certificate manager"}},
		{Method: "POST", Path: "/api/tls/regenerate", Handler: s.tlsRegenerate, Class: classProtected, Scope: scopeAdmin,
			Summary: "Reissue the automatic certificate (key kept unless keep_key is false)", Body: jsonBody("TLSRegenerate", "keep_key (default true)"),
			Responses: map[int]string{200: "reissued", 400: "invalid body", 409: "tls is off or a custom certificate is active", 500: "reissue failed", 501: "no certificate manager"}},
		{Method: "POST", Path: "/api/tls/upload", Handler: s.tlsUpload, Class: classProtected, Scope: scopeAdmin,
			Summary: "Install an own certificate pair (multipart cert/key/force or JSON {cert, key, force})", Body: &bodySpec{ContentType: "multipart/form-data", Description: "cert and key PEM (also as JSON), at most 64 KiB"},
			Responses: map[int]string{200: "installed, mode file", 400: "invalid pair, or force_required when the leaf does not cover the session's host", 409: "tls is off", 413: "body too large", 500: "install failed", 501: "no certificate manager"}},
		{Method: "POST", Path: "/api/tls/reset", Handler: s.tlsReset, Class: classProtected, Scope: scopeAdmin,
			Summary: "Back to the automatic certificate; the uploaded pair is removed", Responses: map[int]string{200: "mode auto", 409: "tls is off", 500: "reset failed", 501: "no certificate manager"}},
		{Method: "PUT", Path: "/api/alerts", Handler: s.putAlerts, Class: classProtected, Scope: scopeAdmin,
			Summary: "Set the alert transport (omitted keys keep their value); written to the config and applied live", Body: jsonBody("AlertsUpdate", "transport, mail_to, webhook_url, webhook_format"), Response: "Alerts",
			Responses: map[int]string{200: "applied; the new status under \"status\"", 400: "invalid value (transport, mail_to, webhook_url, webhook_format; webhook without URL)", 500: "config not written", 501: "no alert manager"}},
		{Method: "POST", Path: "/api/alerts/test", Handler: s.alertsTest, Class: classProtected, Scope: scopeAdmin,
			Summary: "Send a test alert now through the configured transport", Responses: map[int]string{200: "delivered", 409: "a test is still in progress", 502: "delivery failed", 501: "no alert manager"}},
		{Method: "POST", Path: "/api/alerts/template", Handler: s.alertsTemplate, Class: classProtected, Scope: scopeAdmin,
			Summary: "Install or update the PVE notification template pair", Responses: map[int]string{200: "installed", 500: "write failed (use the CLI)", 501: "no PVE or no alert manager"}},
		// ---- protected, session only ------------------------------------
		{Method: "GET", Path: "/api/account", Handler: s.getAccount, Class: classProtected, Scope: scopeSession,
			Summary: "User, auth mode and the cookie sessions", Responses: map[int]string{200: "account view", 403: "token caller", 501: "no account store"}},
		{Method: "POST", Path: "/api/account/password", Handler: s.accountPassword, Class: classProtected, Scope: scopeSession,
			Summary: "Change the password (current password required); other sessions are signed out", Body: jsonBody("AccountPassword", "current_password, new_password"),
			Responses: map[int]string{200: "changed", 400: "new password outside 8..128", 403: "current password wrong, or token caller", 409: "auth is none", 429: "too many attempts", 500: "config not written", 501: "no account store"}},
		{Method: "POST", Path: "/api/account/user", Handler: s.accountUser, Class: classProtected, Scope: scopeSession,
			Summary: "Rename the user (current password required); other sessions are signed out", Body: jsonBody("AccountUser", "current_password, user"),
			Responses: map[int]string{200: "renamed", 400: "user does not match the name rule", 403: "current password wrong, or token caller", 409: "auth is none", 429: "too many attempts", 500: "config not written", 501: "no account store"}},
		{Method: "POST", Path: "/api/account/sessions/revoke", Handler: s.accountRevoke, Class: classProtected, Scope: scopeSession,
			Summary: "Sign every other session out", Body: jsonBody("SessionsRevoke", "{\"others\": true}"),
			Responses: map[int]string{200: "revoked count", 400: "others must be true", 403: "token caller", 501: "no account store"}},
		{Method: "GET", Path: "/api/tokens", Handler: s.getTokens, Class: classProtected, Scope: scopeSession,
			Summary: "The API tokens (never the secrets)", Responses: map[int]string{200: "token list", 403: "token caller"}},
		{Method: "POST", Path: "/api/tokens", Handler: s.createToken, Class: classProtected, Scope: scopeSession,
			Summary: "Create an API token; the secret is returned once", Body: jsonBody("TokenCreate", "name, scope (default read), ttl_days (default 90, 0 = never)"), Response: "TokenCreated",
			Responses: map[int]string{201: "created; \"token\" is the secret, shown only now", 400: "name, scope or ttl_days outside the rules", 403: "token caller", 409: "name in use or 50 tokens stored", 413: "body too large"}},
		{Method: "DELETE", Path: "/api/tokens/{id}", Handler: s.revokeToken, Class: classProtected, Scope: scopeSession,
			Summary: "Revoke an API token by id", Responses: map[int]string{200: "revoked", 400: "id is not 8 hex characters", 403: "token caller", 404: "unknown id"}},
	}
}

// pathParam matches a {name} segment of a route path.
var pathParam = regexp.MustCompile(`\{([a-z_]+)\}`)

// operationID renders a stable id: method and path segments joined by
// "_" ("put_override_name", "get_openapi_json").
func operationID(method, path string) string {
	p := strings.TrimPrefix(path, "/api/")
	p = pathParam.ReplaceAllString(p, "$1")
	p = strings.NewReplacer("/", "_", ".", "_", "-", "_").Replace(p)
	return strings.ToLower(method) + "_" + p
}

// securityFor renders the security requirement of a route: none for a
// public route, the three schemes for a protected one, cookie and basic
// only where no token may call, and optional (an empty requirement) for
// a filtered read.
func securityFor(r route) []map[string][]string {
	if r.Class == classPublic {
		return []map[string][]string{}
	}
	var out []map[string][]string
	if r.Scope != scopeSession {
		out = append(out, map[string][]string{"bearer": {string(r.Scope)}})
	}
	out = append(out, map[string][]string{"basic": {}}, map[string][]string{"cookie": {}})
	if r.Class != classProtected {
		out = append(out, map[string][]string{})
	}
	return out
}

// buildOpenAPI renders the document for the route table.
func buildOpenAPI(version string, table []route) ([]byte, error) {
	paths := map[string]map[string]any{}
	for _, r := range table {
		op := map[string]any{
			"operationId": operationID(r.Method, r.Path),
			"summary":     r.Summary,
			"x-class":     r.Class.String(),
			"x-scope":     string(r.Scope),
			"security":    securityFor(r),
		}
		var params []map[string]any
		for _, m := range pathParam.FindAllStringSubmatch(r.Path, -1) {
			params = append(params, map[string]any{"name": m[1], "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
		}
		for _, p := range r.Params {
			params = append(params, map[string]any{"name": p.Name, "in": "query", "required": false, "description": p.Description, "schema": map[string]any{"type": p.Type}})
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		if r.Body != nil {
			var schema map[string]any
			switch {
			case r.Body.Schema != "":
				schema = map[string]any{"$ref": "#/components/schemas/" + r.Body.Schema}
			case r.Body.ContentType == "application/json":
				schema = map[string]any{"type": "object"}
			default:
				schema = map[string]any{"type": "string"}
			}
			op["requestBody"] = map[string]any{
				"required":    true,
				"description": r.Body.Description,
				"content":     map[string]any{r.Body.ContentType: map[string]any{"schema": schema}},
			}
		}
		responses := map[string]any{}
		codes := make([]int, 0, len(r.Responses))
		for code := range r.Responses {
			codes = append(codes, code)
		}
		sort.Ints(codes)
		for _, code := range codes {
			resp := map[string]any{"description": r.Responses[code]}
			switch {
			case code >= 400:
				resp["content"] = map[string]any{"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}}}
			case code/100 == 2 && r.Response != "":
				schema := map[string]any{"$ref": "#/components/schemas/" + r.Response}
				if r.Response == "HistoryPoint" {
					schema = map[string]any{"type": "array", "items": schema}
				}
				resp["content"] = map[string]any{"application/json": map[string]any{"schema": schema}}
			}
			responses[fmt.Sprint(code)] = resp
		}
		op["responses"] = responses
		if paths[r.Path] == nil {
			paths[r.Path] = map[string]any{}
		}
		paths[r.Path][strings.ToLower(r.Method)] = op
	}
	doc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "n5-fangov API",
			"version":     version,
			"description": "Guarded fan control daemon. Protected routes take a session cookie, HTTP Basic credentials or an API token (Authorization: Bearer n5t_...). State-changing requests from a cookie or Basic caller need the header X-N5-Fangov-Csrf: 1; a bearer caller does not. Token scopes are cumulative: read < control < admin; the session-only routes (tokens, account, login, logout) answer 403 to any token. Errors are {\"error\": \"...\"}.",
		},
		"servers": []map[string]any{{"url": "/"}},
		"paths":   paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearer": map[string]any{"type": "http", "scheme": "bearer", "description": "API token n5t_… (POST /api/tokens, n5-fangov token create)"},
				"basic":  map[string]any{"type": "http", "scheme": "basic"},
				"cookie": map[string]any{"type": "apiKey", "in": "cookie", "name": sessionCookie},
			},
			"schemas": openAPISchemas(),
		},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// openAPISchemas are the component schemas, written by hand to mirror
// the endpoint table of DESIGN.md.
func openAPISchemas() map[string]any {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	number := func(desc string) map[string]any { return map[string]any{"type": "number", "description": desc} }
	boolean := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	strEnum := func(desc string, vals ...string) map[string]any {
		return map[string]any{"type": "string", "description": desc, "enum": vals}
	}
	ref := func(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }
	arrayOf := func(items map[string]any) map[string]any { return map[string]any{"type": "array", "items": items} }
	object := func(desc string, required []string, props map[string]any) map[string]any {
		o := map[string]any{"type": "object", "properties": props}
		if desc != "" {
			o["description"] = desc
		}
		if len(required) > 0 {
			o["required"] = required
		}
		return o
	}
	nullableTime := map[string]any{"type": []string{"string", "null"}, "format": "date-time"}
	tsMap := func(desc string, val map[string]any) map[string]any {
		return map[string]any{"type": "object", "description": desc, "additionalProperties": val}
	}
	return map[string]any{
		"Error": object("Error document of every non-2xx answer", []string{"error"}, map[string]any{
			"error":  str("what went wrong"),
			"errors": arrayOf(str("one line per problem, where a list exists")),
		}),
		"Channel": object("One regulated channel", []string{"name", "pwm", "sensor", "temp", "duty", "target", "rpm", "mode"}, map[string]any{
			"name":       str("channel name"),
			"pwm":        integer("pwm index 1..8"),
			"sensor":     str("sensor id"),
			"temp":       number("temperature in °C; -999 when unknown"),
			"held_temp":  number("hysteresis-held temperature (absent when equal to temp)"),
			"duty":       integer("hardware duty 0..255; -1 while unknown"),
			"target":     integer("computed duty before slew"),
			"rpm":        integer("fan speed; -1 without tachometer"),
			"mode":       strEnum("channel mode", "auto", "manual", "critical", "stall", "sensor-error", "failsafe"),
			"hold_until": integer("end of a running min_on hold (unix seconds, absent otherwise)"),
		}),
		"State": object("Snapshot (GET /api/state); anonymous callers get ts, status, profile, verified, uptime_s, dry_run and the channels without extra_temps, watched, alerts and hwmon_path", []string{"ts", "status", "profile", "verified", "dry_run", "uptime_s", "channels"}, map[string]any{
			"ts":          integer("unix seconds"),
			"status":      strEnum("daemon status", "starting", "ok", "sensor-error", "write-error", "dry-run"),
			"profile":     str("hardware profile"),
			"verified":    boolean("profile verified on real hardware"),
			"hwmon_path":  str("sysfs path of the fan controller"),
			"dry_run":     boolean("no hardware writes"),
			"uptime_s":    integer("daemon uptime in seconds"),
			"channels":    arrayOf(ref("Channel")),
			"extra_temps": tsMap("profile temperatures by id (°C)", number("")),
			"watched":     tsMap("dashboard sensors by id (°C)", number("")),
			"alerts":      tsMap("last alert per kind (unix seconds)", integer("")),
		}),
		"HistoryPoint": object("One history point; maps are keyed by channel name (extra by sensor id)", []string{"ts", "temp", "duty", "rpm"}, map[string]any{
			"ts":    integer("unix seconds"),
			"temp":  tsMap("temperature per channel (°C)", number("")),
			"duty":  tsMap("duty per channel", integer("")),
			"rpm":   tsMap("rpm per channel", integer("")),
			"extra": tsMap("extra sensors (°C); signed in only", number("")),
		}),
		"Override": object("PUT /api/override/{name} body: exactly one of duty and percent", nil, map[string]any{
			"duty":    integer("0..255"),
			"percent": number("0..100"),
		}),
		"Version": object("GET /api/version", []string{"name", "version", "prerelease", "tls", "auth", "limits"}, map[string]any{
			"name":       str("n5-fangov"),
			"version":    str("daemon version"),
			"prerelease": str("pre-release suffix, empty for a release"),
			"tls":        boolean("the listener is TLS-terminated"),
			"auth":       strEnum("auth mode", "none", "basic"),
			"limits": object("validation bounds for clients", nil, map[string]any{
				"min_hdd_override":      integer("lowest manual duty on a channel with a fixed stop"),
				"critical_min":          integer(""),
				"critical_max":          integer(""),
				"curve_points_max":      integer(""),
				"dashboard_sensors_max": integer(""),
				"password_min":          integer(""),
				"password_max":          integer(""),
			}),
		}),
		"Session": object("GET /api/session", []string{"authenticated", "mode", "user", "via"}, map[string]any{
			"authenticated": boolean("the caller is signed in (always true with auth none)"),
			"mode":          strEnum("auth mode", "none", "basic"),
			"user":          str("user name; the token name for a bearer caller"),
			"via":           strEnum("how the caller was resolved", "cookie", "basic", "bearer", "socket", "none"),
			"expires":       integer("cookie session expiry (unix seconds); cookie callers only"),
			"remember":      boolean("remember-me session; cookie callers only"),
			"scope":         strEnum("token scope; bearer callers only", "read", "control", "admin"),
			"token_id":      str("token id; bearer callers only"),
		}),
		"Token": object("One API token as GET /api/tokens lists it (never the secret)", []string{"id", "name", "scope", "created", "expires", "last_used", "last_ip", "expired"}, map[string]any{
			"id":        str("8 hex characters"),
			"name":      str("token name"),
			"scope":     strEnum("scope", "read", "control", "admin"),
			"created":   map[string]any{"type": "string", "format": "date-time"},
			"expires":   nullableTime,
			"last_used": nullableTime,
			"last_ip":   str("address of the last use, empty before"),
			"expired":   boolean("past its expiry (still listed until revoked)"),
		}),
		"TokenCreate": object("POST /api/tokens body", []string{"name"}, map[string]any{
			"name":     str("name, " + TokenNameRe.String() + ", unique"),
			"scope":    strEnum("scope (default read)", "read", "control", "admin"),
			"ttl_days": integer(fmt.Sprintf("days until expiry, 0..%d (default %d, 0 = never)", TokenTTLMax, TokenTTLDefault)),
		}),
		"TokenCreated": object("POST /api/tokens answer; the secret is shown only here", []string{"ok", "token", "id", "name", "scope", "expires"}, map[string]any{
			"ok":      boolean(""),
			"token":   str("the secret: n5t_ + 43 characters"),
			"id":      str("8 hex characters"),
			"name":    str(""),
			"scope":   strEnum("", "read", "control", "admin"),
			"expires": nullableTime,
			"warning": str("present when the token never expires"),
		}),
		"Alerts": object("GET /api/alerts and the status member of PUT /api/alerts", []string{"transport", "effective", "mail_to", "webhook_url", "webhook_format", "pve_available", "mail_available", "template", "cooldown", "cooldown_s", "kinds"}, map[string]any{
			"transport":      strEnum("configured transport", "auto", "pve", "mail", "webhook", "log", "off"),
			"effective":      strEnum("sink in effect", "pve-notify", "mail", "webhook", "log", "off"),
			"mail_to":        str("mail recipient"),
			"webhook_url":    str("full webhook URL (protected endpoint; logs show it without the query)"),
			"webhook_format": strEnum("webhook body", "json", "text"),
			"pve_available":  boolean("PVE::Notify and perl present"),
			"mail_available": boolean("mail(1) present"),
			"template": object("PVE notification template state", nil, map[string]any{
				"installed": boolean(""), "current": boolean(""), "writable": boolean(""), "path": str(""), "reason": str(""),
			}),
			"cooldown":   str("alert cooldown as duration text"),
			"cooldown_s": integer("alert cooldown in seconds (0 offline)"),
			"kinds":      arrayOf(object("", nil, map[string]any{"kind": str(""), "description": str("")})),
			"last":       tsMap("last delivery per kind (unix seconds)", integer("")),
			"recent": arrayOf(object("", nil, map[string]any{
				"ts": integer(""), "kind": str(""), "msg": str(""), "error": str("delivery failure, absent when delivered"),
			})),
		}),
		"AlertsUpdate": object("PUT /api/alerts body; an omitted key keeps its value", nil, map[string]any{
			"transport":      strEnum("", "auto", "pve", "mail", "webhook", "log", "off"),
			"mail_to":        str("local user or address"),
			"webhook_url":    str("absolute http/https URL, no userinfo, at most 2048 bytes; required for transport webhook"),
			"webhook_format": strEnum("", "json", "text"),
		}),
		"Schedules": object("GET /api/schedules", []string{"entries", "active", "next", "last", "timezone"}, map[string]any{
			"entries": arrayOf(object("", nil, map[string]any{
				"preset":   str(""),
				"from":     str("HH:MM, empty for the fallback"),
				"to":       str("HH:MM, empty for the fallback"),
				"days":     arrayOf(str("mon..sun")),
				"fallback": boolean(""),
				"active":   boolean(""),
			})),
			"active":   integer("index of the active entry, -1 for none"),
			"next":     map[string]any{"type": []string{"object", "null"}, "properties": map[string]any{"ts": integer(""), "preset": str("")}},
			"last":     map[string]any{"type": []string{"object", "null"}, "properties": map[string]any{"ts": integer(""), "preset": str(""), "ok": boolean(""), "error": str("")}},
			"timezone": str("host time zone"),
		}),
		"Dashboard": object("GET/PUT /api/dashboard", []string{"sensors"}, map[string]any{
			"sensors":  arrayOf(str("sensor id")),
			"ok":       boolean("PUT answer"),
			"warnings": arrayOf(str("PUT answer: ids that do not resolve now")),
		}),
		"Login": object("POST /api/login body", []string{"user", "password"}, map[string]any{
			"user":     str(""),
			"password": str(""),
			"remember": boolean("30-day session instead of 12 h"),
		}),
		"PresetRename":    object("POST /api/presets/{name}/rename body", []string{"name"}, map[string]any{"name": str("new name")}),
		"TLSRegenerate":   object("POST /api/tls/regenerate body", nil, map[string]any{"keep_key": boolean("keep the private key (default true)")}),
		"AccountPassword": object("POST /api/account/password body", []string{"current_password", "new_password"}, map[string]any{"current_password": str(""), "new_password": str("8..128 characters")}),
		"AccountUser":     object("POST /api/account/user body", []string{"current_password", "user"}, map[string]any{"current_password": str(""), "user": str(userName.String())}),
		"SessionsRevoke":  object("POST /api/account/sessions/revoke body", []string{"others"}, map[string]any{"others": boolean("must be true")}),
	}
}

// getOpenAPI serves the document rendered at start: public, never
// cached, with a strong ETag so a client can ask "still the same?".
func (s *Server) getOpenAPI(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("ETag", s.openapiETag)
	if inm := r.Header.Get("If-None-Match"); inm != "" && strings.Contains(inm, s.openapiETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(s.openapi)
	}
}

// etagOf is the strong ETag of a body: quoted hex sha256.
func etagOf(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}
