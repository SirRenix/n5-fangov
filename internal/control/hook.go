package control

import (
	"fmt"
	"os"
)

// HookState is the result of HookStatus: OK when the daemon would run the
// hook, and a short state for `check` and the log — "ok", "absent" or
// "refused: <why>".
type HookState struct {
	OK    bool
	State string
}

// HookStatus decides whether the emergency hook at path may run (DESIGN
// "Ceilings and emergency"): the path must exist (else "absent"), be a
// regular file — not a symbolic link —, be owned by root, be neither
// group- nor world-writable and be executable by its owner. Anything else
// is "refused: <why>" and the daemon does not run it. The file is never
// created by the daemon: it is the operator's, installed by hand outside
// every API path.
func HookStatus(path string) HookState {
	st, err := os.Lstat(path)
	switch {
	case err != nil && os.IsNotExist(err):
		return HookState{State: "absent"}
	case err != nil:
		return HookState{State: "refused: " + err.Error()}
	case st.Mode()&os.ModeSymlink != 0:
		return HookState{State: "refused: symbolic link"}
	case !st.Mode().IsRegular():
		return HookState{State: "refused: not a regular file"}
	}
	if uid, ok := fileOwner(st); ok && uid != 0 {
		return HookState{State: fmt.Sprintf("refused: not owned by root (uid %d)", uid)}
	}
	perm := st.Mode().Perm()
	switch {
	case perm&0o002 != 0:
		return HookState{State: fmt.Sprintf("refused: world-writable (mode %04o)", perm)}
	case perm&0o020 != 0:
		return HookState{State: fmt.Sprintf("refused: group-writable (mode %04o)", perm)}
	case perm&0o100 == 0:
		return HookState{State: fmt.Sprintf("refused: not executable (mode %04o)", perm)}
	}
	return HookState{OK: true, State: "ok"}
}
