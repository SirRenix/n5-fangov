// wiring_v2.go holds the calls into the v0.2 WEB-builder code
// (internal/tlscert, web.Server.ServeTLS, web.Deps.Log/Bundle/TLS). It was
// build-tagged `ignore` while the two trees were separate; since the merge
// it is a regular part of the package and the stub is gone.
package main

import (
	"context"
	"crypto/tls"
	"log"
	"net"

	"github.com/SirRenix/n5-fangov/internal/tlscert"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// applyV2Deps sets the v0.2 members of web.Deps. Deps.Log is `any`: web
// accepts a LogStore (our logStore has the same method set) or the legacy
// func(int) ([]string, error). Bundle and TLS map 1:1.
func applyV2Deps(deps *web.Deps, d webDeps) {
	deps.Log = d.Log
	deps.Bundle = d.Bundle
	deps.TLS = d.TLS
}

// serveTLSFunc returns web.Server.ServeTLS (TLS 1.2+, HSTS, sets Deps.TLS).
func serveTLSFunc(s *web.Server) func(context.Context, net.Listener, tls.Certificate) error {
	return s.ServeTLS
}

func tlsOptions(dir string, hosts []string, org string) tlscert.Options {
	return tlscert.Options{Dir: dir, Hosts: hosts, Org: org, Logf: log.Printf}
}

// tlsEnsureAuto loads or creates the automatic self-signed pair in dir.
func tlsEnsureAuto(dir string, hosts []string, org string) (tls.Certificate, string, error) {
	return tlscert.EnsureAuto(tlsOptions(dir, hosts, org))
}

// tlsLoadFiles loads a configured PEM pair (tls = "file").
func tlsLoadFiles(certFile, keyFile string) (tls.Certificate, error) {
	return tlscert.LoadFiles(certFile, keyFile)
}

// tlsExportPEM returns the certificate block only (never the key).
func tlsExportPEM(dir string) ([]byte, error) { return tlscert.ExportPEM(dir) }

// tlsRegenerate replaces the automatic pair.
func tlsRegenerate(dir string, hosts []string, org string) (tls.Certificate, error) {
	return tlscert.Regenerate(tlsOptions(dir, hosts, org))
}
