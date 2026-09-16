package web

import (
	"strings"
	"testing"

	"github.com/SirRenix/n5-fangov/internal/config"
)

// TestPutConfigStrict: with ?strict=1 validation warnings refuse the PUT
// (400 with the list, nothing saved); without the query the file is
// written and the warnings are reported as before.
func TestPutConfigStrict(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.validate = func([]byte) ([]string, error) {
		return []string{"channel.cpu.curve: point 1 duty 100 below previous 200, default curve used"}, nil
	}
	r := e.do(t, "PUT", "/api/config?strict=1", sampleTOML, csrf)
	wantError(t, r, 400, "config rejected")
	if !strings.Contains(r.body, `"errors":["channel.cpu.curve`) || len(e.cfg.saved) != 0 || len(e.svc.reloaded) != 0 {
		t.Fatalf("strict: %s (saved %d, reloaded %d)", r.body, len(e.cfg.saved), len(e.svc.reloaded))
	}
	r = e.do(t, "PUT", "/api/config", sampleTOML, csrf)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"warnings":["channel.cpu.curve`) || len(e.cfg.saved) != 1 {
		t.Fatalf("lenient: %s (saved %d)", r.body, len(e.cfg.saved))
	}
	// strict without warnings writes
	e.validate = nil
	wantCode(t, e.do(t, "PUT", "/api/config?strict=true", sampleTOML, csrf), 200)
	if len(e.cfg.saved) != 2 {
		t.Fatalf("strict without warnings not saved")
	}
}

// realValidate is Deps.Validate as cmd wires it: the config parser's
// warnings as "<field>: <msg>".
func realValidate(raw []byte) ([]string, error) {
	_, warns, err := config.Parse(raw)
	out := make([]string, 0, len(warns))
	for _, w := range warns {
		out = append(out, w.String())
	}
	return out, err
}

// TestPutConfigStrictChannelOnly (M1): under ?strict=1 only warnings on
// the [[channel]] tables refuse the PUT. A pre-existing unknown key in
// [web] is not the editor's doing: written, 200, reported as a warning.
// A falling-duty curve is refused with 400 and the channel warning under
// "errors"; the unrelated warning rides along under "warnings".
func TestPutConfigStrictChannelOnly(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	e.validate = realValidate
	withWeb := strings.Replace(sampleTOML, "[web]\n", "[web]\nunknown = 1\n", 1)
	r := e.do(t, "PUT", "/api/config?strict=1", withWeb, csrf)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"warnings":["web.unknown: unknown key, ignored"]`) || len(e.cfg.saved) != 1 {
		t.Fatalf("strict with a [web] warning: %s (saved %d)", r.body, len(e.cfg.saved))
	}
	falling := strings.Replace(withWeb, "curve = [[45,85],[80,255]]", "curve = [[45,200],[80,100]]", 1)
	r = e.do(t, "PUT", "/api/config?strict=1", falling, csrf)
	wantError(t, r, 400, "config rejected")
	var m struct {
		Errors   []string `json:"errors"`
		Warnings []string `json:"warnings"`
	}
	decode(t, r.body, &m)
	if len(m.Errors) != 1 || !strings.HasPrefix(m.Errors[0], "channel.cpu.curve: ") || strings.Join(m.Warnings, "|") != "web.unknown: unknown key, ignored" {
		t.Fatalf("strict with a curve warning: %s", r.body)
	}
	if len(e.cfg.saved) != 1 {
		t.Fatalf("refused PUT saved (%d)", len(e.cfg.saved))
	}
	// a nameless table ("channel[1].name") and a broken array ("channel:")
	// count as channel warnings; a section merely starting with the word
	// does not
	ch, rest := splitChannelWarnings([]string{"channel[1].name: missing, channel dropped", "channel: not an array of tables", "channels: unknown section, ignored", "channel_x.y: z", "alert.mail_to: x"})
	if strings.Join(ch, "|") != "channel[1].name: missing, channel dropped|channel: not an array of tables" || strings.Join(rest, "|") != "channels: unknown section, ignored|channel_x.y: z|alert.mail_to: x" {
		t.Fatalf("split = %v / %v", ch, rest)
	}
	// without strict the falling curve is written with the warning
	r = e.do(t, "PUT", "/api/config", falling, csrf)
	wantCode(t, r, 200)
	if !strings.Contains(r.body, `"channel.cpu.curve: point 1 duty 100 below previous 200`) || strings.Contains(r.body, `"errors"`) || len(e.cfg.saved) != 2 {
		t.Fatalf("lenient: %s (saved %d)", r.body, len(e.cfg.saved))
	}
}

// TestPutConfigInlinePlaceholderRefused (L6, API side): a config text that
// carries the <unchanged> placeholder in an inline table — where
// RestoreHash does not reach — is refused instead of written with the
// placeholder as the stored hash.
func TestPutConfigInlinePlaceholderRefused(t *testing.T) {
	hash := PasswordHash("admin", "pw")
	e := newEnv(t, AuthConfig{Mode: "basic", User: "admin", PasswordHash: hash})
	e.cfg.raw = []byte("[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \"" + hash + "\"\n")
	body := "web = { auth = \"basic\", user = \"admin\", password_hash = \"" + RedactedHash + "\" }\n"
	wantError(t, e.do(t, "PUT", "/api/config", body, basicAuth("admin", "pw")), 400, "not restored")
	if len(e.cfg.saved) != 0 {
		t.Fatalf("placeholder written: %s", e.cfg.saved[0])
	}
	// the line form still restores and writes
	wantCode(t, e.do(t, "PUT", "/api/config", "[web]\nauth = \"basic\"\nuser = \"admin\"\npassword_hash = \""+RedactedHash+"\"\n", basicAuth("admin", "pw")), 200)
	if len(e.cfg.saved) != 1 || !strings.Contains(string(e.cfg.saved[0]), hash) {
		t.Fatalf("line form: saved %d", len(e.cfg.saved))
	}
}
