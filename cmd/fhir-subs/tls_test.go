// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// genSelfSignedCert writes an ECDSA self-signed cert + key PEM pair to the
// given directory and returns the absolute paths. Used by both HTTP TLS
// tests and (separately) MLLP TLS tests when they need real cert/key bytes
// on disk.
func genSelfSignedCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "fhir-subs-test"},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
		DNSNames:              []string{"localhost"},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	certPath = filepath.Join(dir, "cert.pem")
	cf, err := os.Create(certPath) // #nosec G304 -- test path
	if err != nil {
		t.Fatalf("open cert: %v", err)
	}
	if err := pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		_ = cf.Close()
		t.Fatalf("encode cert: %v", err)
	}
	_ = cf.Close()

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	keyPath = filepath.Join(dir, "key.pem")
	kf, err := os.OpenFile(keyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- test path
	if err != nil {
		t.Fatalf("open key: %v", err)
	}
	if err := pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		_ = kf.Close()
		t.Fatalf("encode key: %v", err)
	}
	_ = kf.Close()

	return certPath, keyPath
}

// TestRun_ServesTLS_WhenCertConfigured asserts that when Insecure=false and
// valid cert/key paths are configured, run wires srv.ServeTLS so HTTPS
// works on the bound port and plain HTTP fails. This is story #111's core
// acceptance criterion.
func TestRun_ServesTLS_WhenCertConfigured(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certPath, keyPath := genSelfSignedCert(t, dir)

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server: ServerConfig{HTTP: HTTPConfig{
			Bind:      pickFreeAddr(t),
			ProbeBind: pickFreeAddr(t),
			Insecure:  false,
			TLS: TLSConfig{
				CertFile:   certPath,
				KeyFile:    keyPath,
				MinVersion: "1.3",
			},
		}},
		Lifecycle: LifecycleConfig{ShutdownGracePeriod: 5 * time.Second},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logs := &strings.Builder{}
	var addr string
	started := make(chan struct{})
	hooks := runHooks{
		onListening: func(a string) { addr = a; close(started) },
	}

	done := make(chan error, 1)
	go func() { done <- runWithHooks(ctx, cfg, logs, hooks) }()

	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatalf("server never started: logs=%s", logs.String())
	}

	// Give the goroutine a tick to call ServeTLS.
	time.Sleep(50 * time.Millisecond)

	// Successful HTTPS GET to /healthz.
	httpsClient := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, //nolint:gosec // self-signed cert in test
		},
	}
	// Probes live on a separate listener (S-118); to assert TLS is
	// active on the main listener, hit /metadata which is the
	// probe-only-mode route mounted on the main mux.
	resp, err := httpsClient.Get("https://" + addr + "/metadata")
	if err != nil {
		cancel()
		<-done
		t.Fatalf("https GET /metadata: %v (logs=%s)", err, logs.String())
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		cancel()
		<-done
		t.Fatalf("https status: %d body=%s", resp.StatusCode, body)
	}

	// Plain HTTP GET to the same address must NOT be served — Go's
	// http.Server detects the plaintext request and replies with
	// "Client sent an HTTP request to an HTTPS server" at status 400.
	// Either an error or a non-2xx response proves the server is no
	// longer accepting cleartext.
	httpClient := &http.Client{Timeout: 2 * time.Second}
	httpResp, httpErr := httpClient.Get("http://" + addr + "/metadata")
	switch {
	case httpErr != nil:
		// Connection-level error is acceptable.
	case httpResp != nil:
		_ = httpResp.Body.Close()
		if httpResp.StatusCode == http.StatusOK {
			cancel()
			<-done
			t.Fatalf("plain HTTP GET to TLS-only listener served 200 OK; expected non-2xx or error")
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned err: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return within 10s")
	}
}

// TestValidate_RejectsMissingCertFile asserts that Validate fails fast when
// the configured cert file does not exist (story #111 — fail-fast at
// startup, not first-request).
func TestValidate_RejectsMissingCertFile(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server: ServerConfig{HTTP: HTTPConfig{
			Bind:      "0.0.0.0:8443",
			ProbeBind: "0.0.0.0:8081",
			Insecure:  false,
			TLS: TLSConfig{
				CertFile: "/nonexistent/cert.pem",
				KeyFile:  "/nonexistent/key.pem",
			},
		}},
		Lifecycle: LifecycleConfig{ShutdownGracePeriod: 30 * time.Second},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for missing cert file")
	}
	if !strings.Contains(err.Error(), "cert") {
		t.Fatalf("error should mention cert file: %v", err)
	}
}

// TestValidate_RejectsNonPEMCertFile asserts that Validate refuses a cert
// file whose body is not PEM (story #111 — parse-time validation, not
// first-request).
func TestValidate_RejectsNonPEMCertFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, []byte("this is not pem"), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte("also not pem"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server: ServerConfig{HTTP: HTTPConfig{
			Bind:      "0.0.0.0:8443",
			ProbeBind: "0.0.0.0:8081",
			Insecure:  false,
			TLS: TLSConfig{
				CertFile: certPath,
				KeyFile:  keyPath,
			},
		}},
		Lifecycle: LifecycleConfig{ShutdownGracePeriod: 30 * time.Second},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for non-PEM cert file")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "pem") {
		t.Fatalf("error should mention PEM: %v", err)
	}
}

// TestValidate_DefaultMinVersionIs1_3 asserts that an unset
// server.http.tls.min_version is normalized to "1.3" by Validate.
func TestValidate_DefaultMinVersionIs1_3(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certPath, keyPath := genSelfSignedCert(t, dir)

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server: ServerConfig{HTTP: HTTPConfig{
			Bind:      "0.0.0.0:8443",
			ProbeBind: "0.0.0.0:8081",
			Insecure:  false,
			TLS: TLSConfig{
				CertFile: certPath,
				KeyFile:  keyPath,
			},
		}},
		Lifecycle: LifecycleConfig{ShutdownGracePeriod: 30 * time.Second},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.Server.HTTP.TLS.MinVersion != "1.3" {
		t.Fatalf("default min_version = %q, want %q", cfg.Server.HTTP.TLS.MinVersion, "1.3")
	}
}

// TestValidate_RejectsInvalidMinVersion asserts that
// server.http.tls.min_version values other than "", "1.2", "1.3" are
// rejected at startup.
func TestValidate_RejectsInvalidMinVersion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certPath, keyPath := genSelfSignedCert(t, dir)

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server: ServerConfig{HTTP: HTTPConfig{
			Bind:      "0.0.0.0:8443",
			ProbeBind: "0.0.0.0:8081",
			Insecure:  false,
			TLS: TLSConfig{
				CertFile:   certPath,
				KeyFile:    keyPath,
				MinVersion: "1.0",
			},
		}},
		Lifecycle: LifecycleConfig{ShutdownGracePeriod: 30 * time.Second},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for unsupported min_version")
	}
	if !strings.Contains(err.Error(), "min_version") {
		t.Fatalf("error should mention min_version: %v", err)
	}
}

// TestApplySets_TLSMinVersion asserts that --set
// server.http.tls.min_version is supported by the dotted-key applier.
func TestApplySets_TLSMinVersion(t *testing.T) {
	t.Parallel()

	cfg := defaultConfig()
	if err := applySets(cfg, []string{"server.http.tls.min_version=1.2"}); err != nil {
		t.Fatalf("applySets: %v", err)
	}
	if cfg.Server.HTTP.TLS.MinVersion != "1.2" {
		t.Fatalf("min_version = %q, want %q", cfg.Server.HTTP.TLS.MinVersion, "1.2")
	}
}

// TestLoadConfig_MLLPTLSRoundTrip asserts that mllp.listeners[].tls.* and
// require_client_cert/client_ca_file round-trip through YAML into the
// typed config (story #112).
func TestLoadConfig_MLLPTLSRoundTrip(t *testing.T) {
	t.Parallel()

	body := `
deployment:
  facility_id: hospital-a
adapter:
  id: meditech-expanse-7
server:
  http:
    bind: 0.0.0.0:8443
    insecure: true
mllp:
  listeners:
    - name: adt-feed
      bind: 0.0.0.0:2575
      tls:
        cert_file: /etc/fhir-subs/mllp-cert.pem
        key_file: /etc/fhir-subs/mllp-key.pem
        client_ca_file: /etc/fhir-subs/mllp-ca.pem
        require_client_cert: true
  read_idle_timeout: 45s
  nack_then_drop_after: 7
  inflight_cap_per_conn: 32
  on_persist_fail: drop
  frame_assembly_timeout: 25s
`
	p := writeTempYAML(t, body)
	cfg, err := loadConfig(p)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.MLLP.Listeners) != 1 {
		t.Fatalf("listeners len = %d, want 1", len(cfg.MLLP.Listeners))
	}
	ep := cfg.MLLP.Listeners[0]
	if ep.TLS == nil {
		t.Fatalf("ep.TLS is nil, expected populated TLS block")
	}
	if ep.TLS.CertFile != "/etc/fhir-subs/mllp-cert.pem" {
		t.Fatalf("cert_file = %q", ep.TLS.CertFile)
	}
	if ep.TLS.KeyFile != "/etc/fhir-subs/mllp-key.pem" {
		t.Fatalf("key_file = %q", ep.TLS.KeyFile)
	}
	if ep.TLS.ClientCAFile != "/etc/fhir-subs/mllp-ca.pem" {
		t.Fatalf("client_ca_file = %q", ep.TLS.ClientCAFile)
	}
	if !ep.TLS.RequireClientCert {
		t.Fatalf("require_client_cert = false, want true")
	}
	if cfg.MLLP.ReadIdleTimeout != 45*time.Second {
		t.Fatalf("read_idle_timeout = %v", cfg.MLLP.ReadIdleTimeout)
	}
	if cfg.MLLP.NackThenDropAfter != 7 {
		t.Fatalf("nack_then_drop_after = %d", cfg.MLLP.NackThenDropAfter)
	}
	if cfg.MLLP.InflightCapPerConn != 32 {
		t.Fatalf("inflight_cap_per_conn = %d", cfg.MLLP.InflightCapPerConn)
	}
	if cfg.MLLP.OnPersistFail != "drop" {
		t.Fatalf("on_persist_fail = %q", cfg.MLLP.OnPersistFail)
	}
	if cfg.MLLP.FrameAssemblyTimeout != 25*time.Second {
		t.Fatalf("frame_assembly_timeout = %v", cfg.MLLP.FrameAssemblyTimeout)
	}
}

// TestValidate_MLLPTLSRequiresCertKey asserts that an MLLP listener with a
// TLS block missing cert/key paths is rejected at startup.
func TestValidate_MLLPTLSRequiresCertKey(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: "0.0.0.0:8443", ProbeBind: "0.0.0.0:8081", Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 30 * time.Second},
		MLLP: MLLPConfig{
			Listeners: []MLLPListener{
				{
					Name: "adt", Bind: "0.0.0.0:2575",
					TLS: &MLLPListenerTLSConfig{
						// CertFile/KeyFile intentionally empty
					},
				},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for MLLP TLS without cert/key")
	}
	if !strings.Contains(err.Error(), "tls") {
		t.Fatalf("error should mention tls: %v", err)
	}
}

// TestValidate_MLLPMTLSRequiresClientCA asserts that
// require_client_cert=true without client_ca_file is rejected.
func TestValidate_MLLPMTLSRequiresClientCA(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certPath, keyPath := genSelfSignedCert(t, dir)

	cfg := &Config{
		Deployment: DeploymentConfig{FacilityID: "f1", Mode: DeploymentModeProbeOnly},
		Adapter:    AdapterConfig{ID: "a1"},
		Server:     ServerConfig{HTTP: HTTPConfig{Bind: "0.0.0.0:8443", ProbeBind: "0.0.0.0:8081", Insecure: true}},
		Lifecycle:  LifecycleConfig{ShutdownGracePeriod: 30 * time.Second},
		MLLP: MLLPConfig{
			Listeners: []MLLPListener{
				{
					Name: "adt", Bind: "0.0.0.0:2575",
					TLS: &MLLPListenerTLSConfig{
						CertFile:          certPath,
						KeyFile:           keyPath,
						RequireClientCert: true,
					},
				},
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for mTLS without client_ca_file")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "client_ca") {
		t.Fatalf("error should mention client_ca_file: %v", err)
	}
}
