// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"time"
)

// TLSRestHookServer wraps an http.Handler in a TLS HTTP server bound to
// 127.0.0.1:0 with a self-signed cert. The rest-hook channel rejects
// non-HTTPS endpoints, so anything that wants to assert "the channel
// posted to my mock subscriber" needs this.
//
// The returned URL is the https://127.0.0.1:<port> base; the caller
// composes the per-subscription path on top (e.g., /hook/<id>).
type TLSRestHookServer struct {
	URL    string
	CAPool *x509.CertPool
	server *http.Server
	ln     net.Listener
}

// StartTLSRestHookServer brings up a TLS HTTP server with the given
// handler. Use the returned CAPool to construct an http.Client that
// trusts the server's cert; pass that client to the rest-hook channel
// (Options.HTTPClient).
func StartTLSRestHookServer(handler http.Handler) (*TLSRestHookServer, error) {
	cert, certPEM, err := SelfSignedCert()
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM) {
		return nil, fmt.Errorf("harness: append CA: failed")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("harness: listen: %w", err)
	}
	srv := &http.Server{
		Handler: handler,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		},
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()

	return &TLSRestHookServer{
		URL:    fmt.Sprintf("https://%s", ln.Addr().String()),
		CAPool: pool,
		server: srv,
		ln:     ln,
	}, nil
}

// Client returns an *http.Client that trusts the TLS server's cert.
// Use this to build the rest-hook channel:
//
//	ch, _ := resthook.New(resthook.Options{HTTPClient: srv.Client()})
func (s *TLSRestHookServer) Client() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    s.CAPool,
				MinVersion: tls.VersionTLS12,
			},
		},
		Timeout: 30 * time.Second,
	}
}

// Close shuts the TLS server down.
func (s *TLSRestHookServer) Close() error {
	if s == nil || s.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}
