// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/bzimbelman/fhir-ehr-subscriptions-service/internal/api/handlers"
)

// staticResolver returns a fixed set of IPs for any host. Used to
// simulate a host whose DNS resolution lands on a private CIDR.
type staticResolver struct {
	ips []net.IP
	err error
}

func (s staticResolver) LookupIP(_ context.Context, _, _ string) ([]net.IP, error) {
	return s.ips, s.err
}

func TestURLValidator_RejectsHTTPByDefault(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	if err := v.Validate("http://example.com/hook"); err == nil {
		t.Fatalf("expected http to be rejected by default")
	}
}

func TestURLValidator_AcceptsHTTPSPublicHost(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{
		Resolver: staticResolver{ips: []net.IP{net.ParseIP("93.184.216.34")}},
	})
	if err := v.Validate("https://example.com/hook"); err != nil {
		t.Fatalf("expected https public host to pass, got %v", err)
	}
}

func TestURLValidator_BlocksAWSMetadata(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	err := v.Validate("http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Fatalf("expected metadata IP to be rejected")
	}
}

func TestURLValidator_BlocksAWSMetadataOverHTTPS(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	err := v.Validate("https://169.254.169.254/")
	if err == nil {
		t.Fatalf("expected metadata IP to be rejected even over https")
	}
}

func TestURLValidator_BlocksLoopback(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	for _, raw := range []string{
		"http://localhost:5432/",
		"http://127.0.0.1:5432/",
		"http://[::1]:5432/",
		"http://0.0.0.0:80/",
	} {
		if err := v.Validate(raw); err == nil {
			t.Errorf("expected %s to be rejected", raw)
		}
	}
}

func TestURLValidator_BlocksRFC1918(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	for _, raw := range []string{
		"https://10.0.0.1/",
		"https://10.255.255.255/",
		"https://172.16.0.1/",
		"https://192.168.1.1/",
	} {
		if err := v.Validate(raw); err == nil {
			t.Errorf("expected %s to be rejected", raw)
		}
	}
}

func TestURLValidator_BlocksLinkLocalIPv6(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	for _, raw := range []string{
		"https://[fe80::1]/",
		"https://[fc00::1]/",
	} {
		if err := v.Validate(raw); err == nil {
			t.Errorf("expected %s to be rejected", raw)
		}
	}
}

func TestURLValidator_RejectsExoticSchemes(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	for _, raw := range []string{
		"file:///etc/passwd",
		"gopher://example.com/_",
		"ftp://example.com/",
		"javascript:alert(1)",
	} {
		if err := v.Validate(raw); err == nil {
			t.Errorf("expected %s to be rejected", raw)
		}
	}
}

func TestURLValidator_HostnameResolvesToPrivate_Blocked(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{
		Resolver: staticResolver{ips: []net.IP{net.ParseIP("10.0.0.1")}},
	})
	if err := v.Validate("https://internal.example.com/"); err == nil {
		t.Fatalf("expected DNS->private to be rejected")
	}
}

func TestURLValidator_AllowHostBypassesPrivate(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{
		AllowHosts: []string{"internal.example.com"},
		Resolver:   staticResolver{ips: []net.IP{net.ParseIP("10.0.0.1")}},
	})
	if err := v.Validate("https://internal.example.com/"); err != nil {
		t.Fatalf("expected allow-listed host to bypass, got %v", err)
	}
}

func TestURLValidator_AllowHTTPOptIn(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{
		AllowHTTP: true,
		Resolver:  staticResolver{ips: []net.IP{net.ParseIP("93.184.216.34")}},
	})
	if err := v.Validate("http://example.com/"); err != nil {
		t.Fatalf("expected http to pass with AllowHTTP=true, got %v", err)
	}
}

func TestURLValidator_DNSError_Blocked(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{
		Resolver: staticResolver{err: errors.New("nxdomain")},
	})
	if err := v.Validate("https://does-not-exist.example.com/"); err == nil {
		t.Fatalf("expected DNS-failure to be rejected")
	}
}

func TestURLValidator_RejectsEmptyAndMalformed(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	for _, raw := range []string{
		"",
		":",
		"https://",
		"://example.com",
		"https://exa mple.com/",
	} {
		if err := v.Validate(raw); err == nil {
			t.Errorf("expected malformed %q to be rejected", raw)
		}
	}
}

// Validator surfaces a stable public ErrSSRFBlocked sentinel so handlers
// can map to fhirerror without sniffing strings.
func TestURLValidator_ReturnsSSRFSentinel(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	err := v.Validate("http://169.254.169.254/")
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.Is(err, handlers.ErrSSRFBlocked) {
		t.Errorf("error is not ErrSSRFBlocked: %v", err)
	}
}

// Integration: the create handler must reject SSRF endpoints with 400
// before hitting the channel registry.
func TestCreate_RejectsSSRFMetadataEndpoint(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.URLValidator = handlers.NewURLValidator(handlers.URLValidatorConfig{})
	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "http://169.254.169.254/latest/meta-data/",
		"content": "id-only",
		"channel": {"type": "rest-hook"}
	}`
	resp, err := postRaw(srv.URL+"/Subscription", body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestCreate_RejectsSSRFLocalhostEndpoint(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.URLValidator = handlers.NewURLValidator(handlers.URLValidatorConfig{})
	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "http://localhost:5432/",
		"content": "id-only",
		"channel": {"type": "rest-hook"}
	}`
	resp, err := postRaw(srv.URL+"/Subscription", body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestCreate_RejectsHTTPEndpointByDefault(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.URLValidator = handlers.NewURLValidator(handlers.URLValidatorConfig{
		Resolver: staticResolver{ips: []net.IP{net.ParseIP("93.184.216.34")}},
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "http://example.com/hook",
		"content": "id-only",
		"channel": {"type": "rest-hook"}
	}`
	resp, err := postRaw(srv.URL+"/Subscription", body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestCreate_AllowsHTTPSEndpointWithValidator(t *testing.T) {
	t.Parallel()
	deps := defaultDeps(t)
	deps.URLValidator = handlers.NewURLValidator(handlers.URLValidatorConfig{
		Resolver: staticResolver{ips: []net.IP{net.ParseIP("93.184.216.34")}},
	})
	srv := newTestServer(t, defaultPrincipal(), deps)
	body := `{
		"resourceType": "Subscription",
		"status": "requested",
		"topic": "http://example.org/topics/orders",
		"channelType": {"code": "rest-hook"},
		"endpoint": "https://example.com/hook",
		"content": "id-only",
		"channel": {"type": "rest-hook"}
	}`
	resp, err := postRaw(srv.URL+"/Subscription", body)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 201 {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
}

func postRaw(u, body string) (*http.Response, error) {
	req, _ := http.NewRequest("POST", u, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/fhir+json")
	return http.DefaultClient.Do(req)
}
