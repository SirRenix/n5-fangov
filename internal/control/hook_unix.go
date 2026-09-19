//go:build unix

package control

import (
	"os"
	"os/exec"
	"syscall"
)

// fileOwner returns the uid of a file's owner.
func fileOwner(st os.FileInfo) (uid int, ok bool) {
	if s, ok := st.Sys().(*syscall.Stat_t); ok {
		return int(s.Uid), true
	}
	return 0, false
}

// setProcessGroup puts the hook into its own process group and makes the
// context cancel kill the whole group, so a child the script left behind
// dies with the timeout instead of outliving the run.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
