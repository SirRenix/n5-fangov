package web

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SirRenix/n5-fangov/internal/control"
)

type fakePanic struct {
	mu      sync.Mutex
	until   time.Time
	by      []string
	refused string
}

func (f *fakePanic) Panic(by string) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refused != "" {
		return time.Time{}, fmt.Errorf("%w: %s", control.ErrPanicRefused, f.refused)
	}
	f.by = append(f.by, by)
	f.until = time.Unix(1_789_500_600, 0)
	return f.until, nil
}

func (f *fakePanic) PanicStop(by string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.by = append(f.by, "stop "+by)
	ok := !f.until.IsZero()
	f.until = time.Time{}
	return ok
}

func (f *fakePanic) PanicState() (time.Time, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.until, "", !f.until.IsZero()
}

func TestPanicAPI(t *testing.T) {
	e, _ := eventsEnv(t)
	p := &fakePanic{}
	d := e.srv.deps
	d.Panic = p
	e.withDeps(t, adminBasic, func(x *Deps) { *x = d })
	admin := basicAuth("admin", "pw")
	r := e.do(t, "POST", "/api/panic", `{"action":"start"}`, admin)
	wantCode(t, r, http.StatusOK)
	if !strings.Contains(r.body, `"active":true`) || !strings.Contains(r.body, `"until":1789500600`) {
		t.Errorf("start: %s", r.body)
	}
	r = e.do(t, "POST", "/api/panic", `{"action":"stop"}`, admin)
	if wantCode(t, r, 200); !strings.Contains(r.body, `"active":false`) || !strings.Contains(r.body, `"until":0`) {
		t.Errorf("stop: %s", r.body)
	}
	wantError(t, e.do(t, "POST", "/api/panic", `{"action":"boost"}`, admin), 400, "start")
	wantError(t, e.do(t, "POST", "/api/panic", `{"action":"start"}`, map[string]string{CSRFHeader: "1"}), 401, "")
	// scope control: a read token is refused, a control token named in by
	read, _ := mint(t, e, "reader", "read", 0)
	wantCode(t, e.do(t, "POST", "/api/panic", `{"action":"start"}`, bearer(read)), 403)
	ctl, _ := mint(t, e, "ha", "control", 0)
	wantCode(t, e.do(t, "POST", "/api/panic", `{"action":"start"}`, bearer(ctl)), 200)
	p.mu.Lock()
	if strings.Join(p.by, ",") != "user:admin,stop user:admin,token:ha" {
		t.Errorf("callers %v", p.by)
	}
	p.refused = "profile monitor: n5-fangov only reads the sensors here"
	p.mu.Unlock()
	wantError(t, e.do(t, "POST", "/api/panic", `{"action":"start"}`, admin), 409, "profile monitor")
}

func TestPanicNotWired(t *testing.T) {
	e := newEnv(t, AuthConfig{})
	wantError(t, e.do(t, "POST", "/api/panic", `{"action":"start"}`, map[string]string{CSRFHeader: "1"}), 501, "no full cooling")
}

// /healthz stays ok while channels run in mode panic.
func TestHealthzPanicIsOK(t *testing.T) {
	snap := control.Snapshot{TS: 1, Status: control.StatusOK, Channels: []control.ChannelState{{Name: "cpu", Mode: control.ModePanic}, {Name: "hdd", Mode: control.ModePanic}}}
	if code, st := healthzState(snap, time.Hour, time.Second, time.Minute); code != 200 || st != "ok" {
		t.Errorf("healthz during full cooling: %d %s", code, st)
	}
}
