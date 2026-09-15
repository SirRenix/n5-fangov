//go:build ignore

// wiring_v2.go holds the few calls into WEB-builder code of v0.2
// (internal/tlscert, web.Server.ServeTLS, web.Deps.Log/Bundle/TLS).
//
// INTEGRATE-V2: until both trees are merged this file is excluded with
// `ignore` — the only constraint `go mod tidy` honours; a `v2integrate` tag
// would make tidy resolve internal/tlscert, which does not exist in the CMD
// tree. wiring_v2_stub.go provides the no-op versions for the default build.
// After the merge: delete the `//go:build ignore` line above and the file
// wiring_v2_stub.go, then `go vet ./cmd/...`.
package main

import (
	"context"
	"crypto/tls"
	"net"

	"github.com/SirRenix/n5-fangov/internal/tlscert"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// INTEGRATE-V2: web.Deps gained Log (web.LogStore), Bundle (web.Bundle)
// and TLS (bool). logStore and bundle have the same method sets.
func applyV2Deps(deps *web.Deps, d webDeps) {
	deps.Log = d.Log
	deps.Bundle = d.Bundle
	deps.TLS = d.TLS
}

// INTEGRATE-V2: web.Server.ServeTLS(ctx, ln, cert) error.
func serveTLSFunc(s *web.Server) func(context.Context, net.Listener, tls.Certificate) error {
	return s.ServeTLS
}

// INTEGRATE-V2: tlscert.EnsureAuto(Options{Dir, Hosts, Org}) (tls.Certificate, string, error).
func tlsEnsureAuto(dir string, hosts []string, org string) (tls.Certificate, string, error) {
	return tlscert.EnsureAuto(tlscert.Options{Dir: dir, Hosts: hosts, Org: org})
}

// INTEGRATE-V2: tlscert.LoadFiles(certFile, keyFile) (tls.Certificate, error).
func tlsLoadFiles(certFile, keyFile string) (tls.Certificate, error) {
	return tlscert.LoadFiles(certFile, keyFile)
}

// INTEGRATE-V2: tlscert.ExportPEM(dir) ([]byte, error) — certificate only.
func tlsExportPEM(dir string) ([]byte, error) { return tlscert.ExportPEM(dir) }

// INTEGRATE-V2: tlscert.Regenerate(Options) (tls.Certificate, error).
func tlsRegenerate(dir string, hosts []string, org string) (tls.Certificate, error) {
	return tlscert.Regenerate(tlscert.Options{Dir: dir, Hosts: hosts, Org: org})
}
