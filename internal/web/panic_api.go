// panic_api.go holds POST /api/panic: the full cooling (DESIGN "Full
// cooling"), every channel at 255 for 10 minutes, started and stopped by
// hand. The controller does the work (control/panic.go).
package web

import (
	"errors"
	"net/http"
	"time"

	"github.com/SirRenix/n5-fangov/internal/control"
)

// PanicCtl is the full cooling of the controller (*control.Controller).
type PanicCtl interface {
	// Panic starts it, or moves its end to 10 min from now; by is the
	// caller. control.ErrPanicRefused (wrapped, with the reason) when the
	// daemon cannot drive the fans.
	Panic(by string) (time.Time, error)
	// PanicStop ends it early; false when none ran.
	PanicStop(by string) bool
	// PanicState is the running one, ok false when none.
	PanicState() (until time.Time, by string, ok bool)
}

// panicAnswer is the POST /api/panic answer: whether a full cooling runs
// after the call and its end (unix seconds, 0 when none).
type panicAnswer struct {
	Active bool  `json:"active"`
	Until  int64 `json:"until"`
}

// postPanic: {"action":"start"} starts or extends, {"action":"stop"} ends;
// 409 with the reason when the daemon cannot drive the fans (profile
// monitor, read-only driver).
func (s *Server) postPanic(w http.ResponseWriter, r *http.Request) {
	if s.deps.Panic == nil {
		writeError(w, http.StatusNotImplemented, "no full cooling")
		return
	}
	var b struct {
		Action string `json:"action"`
	}
	if !decodeJSON(w, r, &b) {
		return
	}
	by := ackSource(CallerFrom(r.Context()))
	switch b.Action {
	case "start":
		until, err := s.deps.Panic.Panic(by)
		if errors.Is(err, control.ErrPanicRefused) {
			writeError(w, http.StatusConflict, err.Error())
			return
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, panicAnswer{Active: true, Until: until.Unix()})
	case "stop":
		s.deps.Panic.PanicStop(by)
		writeJSON(w, http.StatusOK, panicAnswer{})
	default:
		writeError(w, http.StatusBadRequest, `"action" must be "start" or "stop"`)
	}
}
