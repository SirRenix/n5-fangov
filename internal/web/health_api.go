// health_api.go holds the liveness probe (GET /healthz, public), the
// loaded fan driver (GET /api/driver) and the health page document
// (GET /api/health).

package web

import (
	"net/http"
	"time"

	"github.com/SirRenix/n5-fangov/internal/control"
)

// DriverInfo is the fan driver the daemon writes through, for the header
// chip and the health page. Module is the kernel module (n5pro) or the
// hwmon chip name; Version and SrcVersion come from /sys/module (empty
// for drivers without them). Expected is the MODULE_VERSION the installed
// driver package builds ("" when unknown); Pending is set when a loaded
// module differs from it, i.e. the module still has to be reloaded.
type DriverInfo struct {
	Module     string `json:"module"`
	Loaded     bool   `json:"loaded"`
	Version    string `json:"version"`
	SrcVersion string `json:"srcversion"`
	Mode       string `json:"mode"` // write-enabled, write-disabled, not-loaded, no-hwmon
	Expected   string `json:"expected"`
	Pending    bool   `json:"pending"`
}

// HealthItem is one line of the health page: the same checks as
// `n5-fangov check`. Level is ok, warn, fail or skipped.
type HealthItem struct {
	Level  string `json:"level"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// healthzBody is the whole answer of GET /healthz: a state word, nothing
// that names the hardware or carries a reading.
type healthzBody struct {
	Status string `json:"status"`
}

// healthzState maps the snapshot to the probe's answer. age is the time
// since the daemon started, sinceCycle the age of the last finished
// cycle; grace is how long either may grow before the loop counts as
// stuck (cmd: three cycle intervals).
func healthzState(snap control.Snapshot, age, sinceCycle, grace time.Duration) (int, string) {
	for _, c := range snap.Channels {
		if c.Mode == control.ModeFailsafe {
			return http.StatusServiceUnavailable, "failsafe"
		}
	}
	switch snap.Status {
	case control.StatusStarting:
		if age > grace {
			return http.StatusServiceUnavailable, "stuck"
		}
		return http.StatusOK, "starting"
	}
	if grace > 0 && sinceCycle > grace {
		return http.StatusServiceUnavailable, "stuck"
	}
	switch snap.Status {
	case control.StatusWriteError, control.StatusSensorError:
		return http.StatusServiceUnavailable, "degraded"
	}
	for _, c := range snap.Channels {
		if c.Mode == control.ModeSensor || c.Mode == control.ModeStall {
			return http.StatusServiceUnavailable, "degraded"
		}
	}
	return http.StatusOK, "ok"
}

// healthz answers 200 or 503 with {"status": ...} for monitors (Pulse,
// Uptime Kuma); public, no readings, no versions. HEAD is served by the
// GET pattern.
func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.deps.Service == nil {
		writeJSON(w, http.StatusServiceUnavailable, healthzBody{"unavailable"})
		return
	}
	snap := s.deps.Service.Snapshot()
	age := time.Duration(snap.Uptime) * time.Second
	sinceCycle := time.Since(time.Unix(snap.TS, 0))
	if snap.TS == 0 {
		sinceCycle = 0
	}
	code, st := healthzState(snap, age, sinceCycle, s.deps.HealthGrace)
	writeJSON(w, code, healthzBody{st})
}

// getDriver serves the loaded fan driver (nil Deps.Driver → 501).
func (s *Server) getDriver(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Driver == nil {
		writeError(w, http.StatusNotImplemented, "no driver information")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.deps.Driver())
}

// getHealth serves the health page document (nil Deps.Health → 501).
func (s *Server) getHealth(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Health == nil {
		writeError(w, http.StatusNotImplemented, "no health information")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.deps.Health())
}
