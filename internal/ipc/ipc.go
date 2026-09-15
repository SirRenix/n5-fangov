// Package ipc serves the HTTP mux on a unix socket (for the CLI) and provides a
// client that dials it. The socket carries no authentication. Who may connect
// is decided by the file system: the runtime directory (/run/pvefand, created
// by systemd as root:root with RuntimeDirectoryMode=0750) and the socket
// itself (0660, created under umask 0117 so it is never world-accessible,
// not even between bind and chmod). In the shipped unit that means root only;
// there is no dedicated group.
package ipc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Listen creates the socket directory, removes a stale socket and listens on
// socketPath with mode 0660. A socket that still answers is left alone and an
// error is returned.
func Listen(socketPath string) (net.Listener, error) {
	if socketPath == "" {
		return nil, errors.New("ipc: empty socket path")
	}
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o750); err != nil {
		return nil, fmt.Errorf("ipc: create socket dir: %w", err)
	}
	if fi, err := os.Lstat(socketPath); err == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("ipc: %s exists and is not a socket", socketPath)
		}
		if c, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond); err == nil {
			c.Close()
			return nil, fmt.Errorf("ipc: %s is in use by another process", socketPath)
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, fmt.Errorf("ipc: remove stale socket: %w", err)
		}
	}
	// The socket node is created by bind(2) with mode 0777 &^ umask; a
	// process-wide umask of 0117 closes the window before Chmod (M3).
	old := setUmask(0o117)
	ln, err := net.Listen("unix", socketPath)
	setUmask(old)
	if err != nil {
		return nil, fmt.Errorf("ipc: listen: %w", err)
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		ln.Close()
		return nil, fmt.Errorf("ipc: chmod socket: %w", err)
	}
	return ln, nil
}

// Serve listens on socketPath and serves handler until ctx is done. The socket
// file is removed on return.
func Serve(ctx context.Context, socketPath string, handler http.Handler) error {
	ln, err := Listen(socketPath)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	err = srv.Serve(ln)
	close(done)
	_ = os.Remove(socketPath)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Client returns an http.Client whose transport dials the unix socket. Request
// URLs use any host, e.g. http://pvefand/api/state.
func Client(socketPath string) *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
			DisableKeepAlives: true,
		},
	}
}
