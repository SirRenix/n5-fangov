// wiring_v2.go holds the calls into the v0.2 WEB-builder code
// (internal/tlscert, web.Server.ServeTLSStore, web.Deps.Log/Bundle/TLS/
// TLSMgr). It was build-tagged `ignore` while the two trees were separate;
// since the merge it is a regular part of the package and the stub is gone.
// tlsmgr.go (the certificate manager) is the other cmd file that imports
// internal/tlscert.
package main

import (
	"context"
	"net"

	"github.com/SirRenix/n5-fangov/internal/tlscert"
	"github.com/SirRenix/n5-fangov/internal/web"
)

// applyV2Deps sets the v0.2 members of web.Deps. Log, Bundle and TLS map
// 1:1 (logStore has web.LogStore's method set); TLSMgr only when set (a
// typed nil would look non-nil behind the interface).
func applyV2Deps(deps *web.Deps, d webDeps) {
	deps.Log = d.Log
	deps.Bundle = d.Bundle
	deps.TLS = d.TLS
	if d.TLSMgr != nil {
		deps.TLSMgr = d.TLSMgr
	}
	deps.TLSHosts = d.TLSHosts
}

// serveTLSFunc returns a ServeTLSStore wrapper (TLS 1.2+, HSTS, sets
// Deps.TLS) fed by the manager's store, so a regenerate/upload/reset from
// the dashboard reaches the next handshake without a restart.
func serveTLSFunc(s *web.Server) func(context.Context, net.Listener, *tlsManager) error {
	return func(ctx context.Context, ln net.Listener, mgr *tlsManager) error {
		return s.ServeTLSStore(ctx, ln, mgr.Store())
	}
}

// tlsCheckKeyMode reports a private key file readable by group/others (L8).
func tlsCheckKeyMode(keyFile string) error { return tlscert.CheckKeyMode(keyFile) }
