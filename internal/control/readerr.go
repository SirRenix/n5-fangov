package control

import (
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/SirRenix/n5-fangov/internal/logfile"
)

// ReadErrInterval is the minimum time between two journal warnings about
// driver read errors of the same source. Occurrences in between are counted
// and reported with the next warning.
const ReadErrInterval = 5 * time.Minute

// driverErrnos are the errno values a hwmon driver returns when the chip
// behind it does not answer or the device went away: the N5 Pro EC driver
// (0.2.1+sr1) returns -ETIMEDOUT/-EAGAIN after its wait budget, -ENODATA
// for EC sentinel values and an unknown mode, -ENODEV after unbind or
// shutdown. They are worth a warning even when the next cycle reads fine,
// so a slow or flaky EC shows up in the journal (driver review B11, F8b K3).
var driverErrnos = []struct {
	errno syscall.Errno
	name  string
}{
	{syscall.ETIMEDOUT, "ETIMEDOUT"},
	{syscall.EAGAIN, "EAGAIN"},
	{syscall.ENODATA, "ENODATA"},
	{syscall.ENODEV, "ENODEV"},
}

// driverErrno returns the errno name when err carries one of driverErrnos.
func driverErrno(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	for _, e := range driverErrnos {
		if errors.Is(err, e.errno) {
			return e.name, true
		}
	}
	return "", false
}

// readErrState is the rate-limit state of one source.
type readErrState struct {
	last       time.Time
	suppressed int
}

// noteReadErr logs a driver read error of source (e.g. "sensor ec:cpu
// (channel cpu)") as a journal warning, at most once per ReadErrInterval per
// source; other errors are left to the caller's own logging.
func (c *Controller) noteReadErr(source string, err error) {
	name, ok := driverErrno(err)
	if !ok {
		return
	}
	now := c.opts.Now()
	c.logMu.Lock()
	if c.readErrs == nil {
		c.readErrs = map[string]*readErrState{}
	}
	st := c.readErrs[source]
	if st == nil {
		st = &readErrState{}
		c.readErrs[source] = st
	}
	if !st.last.IsZero() && now.Sub(st.last) < ReadErrInterval {
		st.suppressed++
		c.logMu.Unlock()
		return
	}
	more := ""
	if st.suppressed > 0 {
		more = fmt.Sprintf(" (%d more since %s)", st.suppressed, st.last.Format("15:04:05"))
	}
	st.last, st.suppressed = now, 0
	c.logMu.Unlock()
	c.log.Printf("%sdriver read error: %s: %s%s: %v", logfile.PrefixWarning, source, name, more, err)
}

// noteReadOK is called after a successful read of source. When errors of
// that source were suppressed since the last warning, their count is
// logged once the interval has passed, so it is not lost when the errors
// stop; the rate limit of noteReadErr applies to these lines too.
func (c *Controller) noteReadOK(source string) {
	now := c.opts.Now()
	c.logMu.Lock()
	st := c.readErrs[source]
	if st == nil || st.suppressed == 0 || now.Sub(st.last) < ReadErrInterval {
		c.logMu.Unlock()
		return
	}
	n, since := st.suppressed, st.last
	st.last, st.suppressed = now, 0
	c.logMu.Unlock()
	c.log.Printf("%sdriver read recovered: %s: %d more error(s) since %s were not logged", logfile.PrefixWarning, source, n, since.Format("15:04:05"))
}
