package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

// prompter reads interactive answers from the controlling terminal
// (/dev/tty) so that piped stdin/stdout do not get in the way; without a
// terminal it falls back to stdin/stdout. Passwords are read with the
// echo switched off through stty(1) — no terminal library, no dependency
// (DESIGN rule 9). When stty is unavailable the input is visible and says so.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
	tty *os.File // nil when stdin/stdout are used
}

// openPrompter returns a prompter on /dev/tty, or on stdin/stdout when no
// terminal is available.
func openPrompter() *prompter {
	if f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0); err == nil {
		return &prompter{in: bufio.NewReader(f), out: f, tty: f}
	}
	return &prompter{in: bufio.NewReader(os.Stdin), out: os.Stdout}
}

func (p *prompter) close() {
	if p.tty != nil {
		_ = p.tty.Close()
	}
}

// errEOF is returned when the terminal closes mid-dialogue.
var errEOF = errors.New("input closed")

// readLine reads one line without the trailing newline.
func (p *prompter) readLine() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && s == "" {
		return "", errEOF
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// ask prints label (with the default in brackets) and returns the answer,
// or def when the answer is empty.
func (p *prompter) ask(label, def string) (string, error) {
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	s, err := p.readLine()
	if err != nil {
		return "", err
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	return s, nil
}

// askChoice asks until the answer is one of choices (case-insensitive).
func (p *prompter) askChoice(label string, choices []string, def string) (string, error) {
	for {
		s, err := p.ask(label+" ("+strings.Join(choices, "|")+")", def)
		if err != nil {
			return "", err
		}
		for _, c := range choices {
			if strings.EqualFold(s, c) {
				return c, nil
			}
		}
		fmt.Fprintf(p.out, "please answer one of: %s\n", strings.Join(choices, ", "))
	}
}

// askYesNo asks a y/n question.
func (p *prompter) askYesNo(label string, def bool) (bool, error) {
	d := "y/N"
	if def {
		d = "Y/n"
	}
	for {
		fmt.Fprintf(p.out, "%s [%s]: ", label, d)
		s, err := p.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// askPassword reads a line with echo off. Returns the password and whether
// the echo could be switched off. Ctrl-C during the read restores the echo
// before the process ends (L4); otherwise the shell is left with a silent
// terminal.
func (p *prompter) askPassword(label string) (string, bool, error) {
	fmt.Fprintf(p.out, "%s: ", label)
	hidden := p.stty("-echo") == nil
	if !hidden {
		fmt.Fprint(p.out, "(input visible, stty unavailable) ")
	} else {
		defer p.echoOnInterrupt(os.Exit)()
	}
	s, err := p.readLine()
	if hidden {
		_ = p.stty("echo")
	}
	fmt.Fprintln(p.out)
	if err != nil {
		return "", hidden, err
	}
	return s, hidden, nil
}

// echoOnInterrupt restores the terminal echo and exits with 130 (128 +
// SIGINT) when SIGINT or SIGTERM arrives while a password is being read.
// The returned func stops the handler; call it when the read is done.
func (p *prompter) echoOnInterrupt(exit func(int)) (stop func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-sig:
			_ = p.stty("echo")
			fmt.Fprintln(p.out)
			exit(130)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(sig)
		close(done)
	}
}

// askPasswordTwice asks for a password and its confirmation, up to three
// attempts; empty passwords are refused.
func (p *prompter) askPasswordTwice() (string, error) {
	for i := 0; i < 3; i++ {
		a, _, err := p.askPassword("password")
		if err != nil {
			return "", err
		}
		if a == "" {
			fmt.Fprintln(p.out, "empty password not accepted")
			continue
		}
		b, _, err := p.askPassword("repeat password")
		if err != nil {
			return "", err
		}
		if a == b {
			return a, nil
		}
		fmt.Fprintln(p.out, "passwords differ, try again")
	}
	return "", errors.New("no matching password after 3 attempts")
}

// stty runs stty with the terminal as stdin; only meaningful on the tty.
func (p *prompter) stty(arg string) error {
	if p.tty == nil {
		return errors.New("no tty")
	}
	cmd := exec.Command("stty", arg)
	cmd.Stdin = p.tty
	cmd.Stdout = p.tty
	cmd.Stderr = io.Discard
	return cmd.Run()
}
