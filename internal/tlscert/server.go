package tlscert

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"
)

// ServerConfig is the tls.Config of the dashboard listener: TLS 1.2
// minimum, X25519/P-256/P-384, AEAD suites only, HTTP/2 offered, the
// certificate looked up per handshake through getCert (a Store.Get for the
// hot swap). Session tickets are off: a LAN dashboard gains nothing
// from resumption, and without tickets a swapped certificate is what every
// new connection sees at once instead of an old session being resumed.
//
// ValidatePair and CheckUsable run a handshake against exactly this
// config, so a pair that passes validation is one this listener can serve —
// crypto/tls has no signature scheme for ECDSA P-224 and refuses RSA keys
// below 1024 bits, and a listener that accepts such a pair fails every
// handshake after the swap.
func ServerConfig(getCert func(*tls.ClientHelloInfo) (*tls.Certificate, error)) *tls.Config {
	return &tls.Config{
		GetCertificate:   getCert,
		MinVersion:       tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384},
		CipherSuites: []uint16{ // TLS 1.2 only; 1.3 suites are fixed
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
		},
		NextProtos:             []string{"h2", "http/1.1"},
		SessionTicketsDisabled: true,
	}
}

// handshakeTimeout bounds the in-process self-handshake of CheckUsable.
const handshakeTimeout = 5 * time.Second

// CheckUsable runs one in-process TLS handshake over net.Pipe with cert
// served through ServerConfig and a client that skips verification. It
// fails for every pair crypto/tls cannot sign with — the key type is not
// covered by any supported signature scheme (ECDSA P-224), the key is too
// small (RSA < 1024 since Go 1.24), the private key does not fit the
// certificate — before such a pair reaches the listener.
func CheckUsable(cert tls.Certificate) error {
	if len(cert.Certificate) == 0 || cert.PrivateKey == nil {
		return errors.New("certificate/key cannot be used by this server: empty certificate or key")
	}
	c := cert
	srvCfg := ServerConfig(func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &c, nil })
	cliCfg := &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // self-check, no peer
	sp, cp := net.Pipe()
	deadline := time.Now().Add(handshakeTimeout)
	_ = sp.SetDeadline(deadline)
	_ = cp.SetDeadline(deadline)
	srv := tls.Server(sp, srvCfg)
	cli := tls.Client(cp, cliCfg)
	errc := make(chan error, 1)
	go func() { errc <- srv.Handshake() }()
	cliErr := cli.Handshake()
	// The raw pipe ends are closed, not the tls.Conns: a close_notify on a
	// synchronous pipe would block until the peer reads it.
	_ = cp.Close()
	srvErr := <-errc
	_ = sp.Close()
	switch {
	case srvErr != nil:
		return fmt.Errorf("certificate/key cannot be used by this server: %v", srvErr)
	case cliErr != nil:
		return fmt.Errorf("certificate/key cannot be used by this server: %v", cliErr)
	}
	return nil
}
