//go:build !unix

package logfile

import "errors"

func mkfifo(string) error { return errors.New("no fifo") }
