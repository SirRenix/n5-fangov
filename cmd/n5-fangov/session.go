package main

import (
	"context"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// session describes where a writing command (validate) is typed from.
// TESTSPEC G5 wants the operator at the device; environment variables
// alone do not show that (sudo and su - clear SSH_*), so the process chain
// is read as well.
type session struct {
	// remote lists why the session counts as remote: SSH_* variables, an
	// sshd/termproxy/... process among the ancestors.
	remote []string
	// console: some process of the chain (this one up to init) has a local
	// VT, serial or system console as its controlling terminal. A login on
	// tty1 (or the KVM) passes this also through sudo with use_pty.
	console bool
	// stdin is the terminal device standard input is connected to; "" when
	// it is not a terminal (pipe, file, /dev/null).
	stdin string
}

// remoteParents are the process names (comm, 15 characters at most) of
// servers that hand out remote shells.
var remoteParents = map[string]bool{
	"sshd": true, "sshd-session": true, "dropbear": true, "termproxy": true,
	"mosh-server": true, "telnetd": true, "in.telnetd": true, "ttyd": true,
	"shellinaboxd": true, "tmate": true,
}

// detectSession walks pid and its ancestors through procRoot/<pid>/stat.
func detectSession(pid int, getenv func(string) string, stdin string) session {
	s := session{stdin: stdin}
	for _, k := range []string{"SSH_CONNECTION", "SSH_CLIENT", "SSH_TTY"} {
		if getenv(k) != "" {
			s.remote = append(s.remote, k+" set")
			break
		}
	}
	seen := map[int]bool{}
	for hops := 0; pid > 0 && hops < 128 && !seen[pid]; hops++ {
		seen[pid] = true
		comm, ppid, tty, ok := procStat(pid)
		if !ok {
			break
		}
		if remoteParents[comm] {
			s.remote = append(s.remote, "ancestor process "+comm+" (pid "+strconv.Itoa(pid)+")")
		}
		if localConsoleTTY(tty) {
			s.console = true
		}
		if pid == 1 {
			break
		}
		pid = ppid
	}
	return s
}

// procStat parses procRoot/<pid>/stat: comm, ppid and tty_nr.
func procStat(pid int) (comm string, ppid, tty int, ok bool) {
	b := readProc(strconv.Itoa(pid), "stat")
	open, end := strings.IndexByte(b, '('), strings.LastIndexByte(b, ')')
	if open < 0 || end < open {
		return "", 0, 0, false
	}
	comm = b[open+1 : end]
	f := strings.Fields(b[end+1:])
	// state ppid pgrp session tty_nr ...
	if len(f) < 5 {
		return "", 0, 0, false
	}
	ppid, err1 := strconv.Atoi(f[1])
	tty, err2 := strconv.Atoi(f[4])
	if err1 != nil || err2 != nil {
		return "", 0, 0, false
	}
	return comm, ppid, tty, true
}

// localConsoleTTY reports whether tty_nr is a VT (/dev/ttyN, major 4,
// minor < 64), a serial port (/dev/ttySN, major 4, minor 64..255), the
// system console (major 5, minor 1) or a hypervisor console (hvc, major
// 229). Pseudo terminals (major 136..143) are not.
func localConsoleTTY(ttyNr int) bool {
	if ttyNr == 0 {
		return false
	}
	major := (ttyNr >> 8) & 0xfff
	minor := (ttyNr & 0xff) | ((ttyNr >> 12) & 0xfff00)
	switch major {
	case 4:
		return minor >= 1
	case 5:
		return minor == 1
	case 229:
		return true
	}
	return false
}

// stdinTerminal returns the terminal device of standard input, or "".
func stdinTerminal() string {
	st, err := os.Stdin.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return ""
	}
	link, err := os.Readlink("/proc/self/fd/0")
	if err != nil {
		return ""
	}
	for _, p := range []string{"/dev/tty", "/dev/pts/", "/dev/console", "/dev/hvc"} {
		if strings.HasPrefix(link, p) {
			return link
		}
	}
	return ""
}

// unitRunning reports whether a unit is running or about to: every state
// of `systemctl is-active` except inactive, failed and unknown counts
// (active, activating - also the auto-restart wait of Restart=always -,
// deactivating, reloading, refreshing). Without systemctl it is false.
func unitRunning(unit string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "systemctl", "is-active", unit).Output()
	switch strings.TrimSpace(string(out)) {
	case "", "inactive", "failed", "unknown":
		return false
	}
	return true
}

func orText(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func sortedKeys(m map[int]string) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
