// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package harness

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
)

// TestSelfSignedCert_RoundTrip: the generated cert validates against
// the returned CA bundle for the localhost SANs.
func TestSelfSignedCert_RoundTrip(t *testing.T) {
	cert, caPEM, err := SelfSignedCert()
	if err != nil {
		t.Fatalf("SelfSignedCert: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatalf("expected a populated tls.Certificate")
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatalf("AppendCertsFromPEM: failed")
	}

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{
		DNSName: "localhost",
		Roots:   pool,
	}); err != nil {
		t.Errorf("verify localhost: %v", err)
	}
	// IP SAN: x509.Verify can't take an IP via DNSName; check it's listed.
	foundLoopback := false
	for _, ip := range leaf.IPAddresses {
		if ip.String() == "127.0.0.1" {
			foundLoopback = true
			break
		}
	}
	if !foundLoopback {
		t.Errorf("expected 127.0.0.1 in IP SANs; got %v", leaf.IPAddresses)
	}

	// Sanity-check it can be loaded into a tls.Config server side.
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}}
	if cfg.Certificates[0].Leaf == nil && len(cfg.Certificates[0].Certificate) == 0 {
		t.Errorf("tls.Config did not retain certificate")
	}
}
