package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/control"
)

// The CLI through the socket: start, status, stop, a refusal; without the
// socket a clear error.
func TestCLIPanic(t *testing.T) {
	d := startFakeDaemon(t)
	d.stop()
	until := time.Now().Add(10 * time.Minute).Unix()
	var actions []string
	refused := false
	d.extra = func(mux *http.ServeMux) {
		mux.HandleFunc("POST /api/panic", func(w http.ResponseWriter, r *http.Request) {
			var b map[string]string
			_ = json.NewDecoder(r.Body).Decode(&b)
			d.mu.Lock()
			defer d.mu.Unlock()
			actions = append(actions, b["action"])
			w.Header().Set("Content-Type", "application/json")
			switch {
			case refused:
				w.WriteHeader(409)
				_, _ = w.Write([]byte(`{"error":"full cooling refused: profile monitor: n5-fangov only reads the sensors here"}`))
			case b["action"] == "start":
				d.snap.Panic = &control.PanicState{Until: until, By: "cli"}
				_ = json.NewEncoder(w).Encode(map[string]any{"active": true, "until": until})
			default:
				d.snap.Panic = nil
				_ = json.NewEncoder(w).Encode(map[string]any{"active": false, "until": 0})
			}
		})
	}
	d.start(t)
	out, errOut, code := captureOutput(t, func() int { return cmdPanic(nil) })
	if code != exitOK || !strings.Contains(out, "every channel at 255 until "+time.Unix(until, 0).Format("15:04:05")) {
		t.Fatalf("start: %d %q %q", code, out, errOut)
	}
	out, _, code = captureOutput(t, func() int { return cmdPanic([]string{"status"}) })
	if code != exitOK || !strings.Contains(out, "full cooling: on until") || !strings.Contains(out, "started by cli") {
		t.Errorf("status on: %d %q", code, out)
	}
	out, _, code = captureOutput(t, func() int { return cmdPanic([]string{"stop"}) })
	if code != exitOK || !strings.Contains(out, "full cooling: off") {
		t.Errorf("stop: %d %q", code, out)
	}
	out, _, _ = captureOutput(t, func() int { return cmdPanic([]string{"status"}) })
	if !strings.Contains(out, "full cooling: off") {
		t.Errorf("status off: %q", out)
	}
	d.mu.Lock()
	refused = true
	if strings.Join(actions, ",") != "start,stop" {
		t.Errorf("actions %v", actions)
	}
	d.mu.Unlock()
	_, errOut, code = captureOutput(t, func() int { return cmdPanic(nil) })
	if code != exitFail || !strings.Contains(errOut, "profile monitor") {
		t.Errorf("refused: %d %q", code, errOut)
	}
	if _, _, code = captureOutput(t, func() int { return cmdPanic([]string{"loud"}) }); code != exitUsage {
		t.Errorf("bad subcommand: %d", code)
	}
	d.stop()
	_, errOut, code = captureOutput(t, func() int { return cmdPanic(nil) })
	if code != exitFail || !strings.Contains(errOut, "daemon is not running") {
		t.Errorf("no daemon: %d %q", code, errOut)
	}
}

func TestPanicHealthLine(t *testing.T) {
	if panicLine(nil) != "" {
		t.Error("line without a full cooling")
	}
	u := time.Date(2026, 11, 21, 14, 32, 0, 0, time.Local).Unix()
	if got := panicLine(&control.PanicState{Until: u, By: "user:admin"}); got != "until 14:32 (admin)" {
		t.Errorf("line %q", got)
	}
	v := &healthView{svc: &panicSvc{p: &control.PanicState{Until: u, By: "token:ha"}}}
	h := map[string]any{"summary": "all good"}
	if got := v.withPanic(h); got["panic"] != "full cooling until 14:32 (token ha)" || h["panic"] != nil {
		t.Errorf("health doc %v, cached %v", got, h)
	}
}

// panicSvc is a control.Service whose snapshot carries a full cooling.
type panicSvc struct {
	control.Service
	p *control.PanicState
}

func (s *panicSvc) Snapshot() control.Snapshot { return control.Snapshot{Panic: s.p} }
