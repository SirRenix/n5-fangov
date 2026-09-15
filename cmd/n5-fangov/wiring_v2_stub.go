// wiring_v2_stub.go keeps the default build green while internal/tlscert
// and the v0.2 members of web.Deps live in the WEB builder's tree. Every
// TLS entry point reports errTLSUnavailable; serve then disables the TCP
// listener instead of falling back to plain HTTP on a LAN address.
//
// INTEGRATE-V2: after the merge delete this file and the `//go:build ignore`
// line of wiring_v2.go (the real adapters with the same signatures).
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"net"

	"github.com/SirRenix/n5-fangov/internal/web"
)

var errTLSUnavailable = errors.New("TLS support not compiled in (build tag v2integrate)")

// INTEGRATE-V2: only the pre-v0.2 Log func exists here.
func applyV2Deps(deps *web.Deps, d webDeps) {
	deps.Log = d.Log.Lines
}

// INTEGRATE-V2: no ServeTLS in this tree.
func serveTLSFunc(*web.Server) func(context.Context, net.Listener, tls.Certificate) error {
	return func(context.Context, net.Listener, tls.Certificate) error { return errTLSUnavailable }
}

func tlsEnsureAuto(string, []string, string) (tls.Certificate, string, error) {
	return tls.Certificate{}, "", errTLSUnavailable
}

func tlsLoadFiles(string, string) (tls.Certificate, error) {
	return tls.Certificate{}, errTLSUnavailable
}

func tlsExportPEM(string) ([]byte, error) { return nil, errTLSUnavailable }

func tlsRegenerate(string, []string, string) (tls.Certificate, error) {
	return tls.Certificate{}, errTLSUnavailable
}
