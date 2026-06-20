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
	"time"

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

// ---------------------------------------------------------------------
// OP #183 — RED. Validate must reject the unhandled SSRF surfaces today:
// IPv4 broadcast, CG-NAT 100.64.0.0/10, IPv6 site-local fec0::/10, and
// IDN/Punycode hostnames that decode to "localhost" before the loopback
// substring check fires.
// ---------------------------------------------------------------------

func TestURLValidator_BlocksIPv4Broadcast(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	if err := v.Validate(context.Background(), "https://255.255.255.255/"); err == nil {
		t.Fatalf("expected IPv4 broadcast 255.255.255.255 to be rejected")
	}
}

func TestURLValidator_BlocksCGNAT(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	for _, raw := range []string{
		"https://100.64.0.1/",
		"https://100.127.255.254/",
	} {
		if err := v.Validate(context.Background(), raw); err == nil {
			t.Errorf("expected CG-NAT %s to be rejected", raw)
		}
	}
}

func TestURLValidator_BlocksIPv6SiteLocal(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	if err := v.Validate(context.Background(), "https://[fec0::1]/"); err == nil {
		t.Fatalf("expected IPv6 site-local fec0::1 to be rejected")
	}
}

// TestURLValidator_BlocksIDNHomoglyphLocalhost asserts the validator
// runs idna.Lookup.ToASCII (or equivalent) BEFORE the loopback
// substring check. The literal hostname "loсalhost" (with the Cyrillic
// 'с' U+0441) is NOT == "localhost" by direct strings.ToLower, so the
// existing syntactic gate misses it. After IDN decode the host
// normalises to a string an attacker-controlled DNS could point at
// loopback-equivalent infra.
func TestURLValidator_BlocksIDNHomoglyphLocalhost(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	// Cyrillic small letter 'es' (U+0441) replaces ASCII 'c'. The host
	// looks like "localhost" but is not byte-equal. Today the validator
	// does no IDN normalization, so this is accepted as a bare hostname
	// and DNS-looked-up. The AC requires explicit IDN-aware loopback
	// rejection — the test asserts a non-nil error.
	if err := v.Validate(context.Background(), "https://loсalhost/"); err == nil {
		t.Fatalf("expected IDN homoglyph host that decodes to a localhost-equivalent to be rejected")
	}
}

// TestURLValidator_BlocksPunycodeLocalhost covers the explicit punycode
// form: an attacker registers a hostname whose Punycode (ACE) form
// includes "localhost" once decoded. We use the literal IDN form
// `xn--lcalhost-w0a.example` here as a stand-in shape; the AC says the
// validator MUST decode IDN BEFORE the loopback substring check.
func TestURLValidator_BlocksPunycodeLocalhost(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	// `xn--lcalhost-w0a` is a punycode form whose Unicode decode
	// includes a Latin-with-diacritic that visually reads "lócalhost".
	// The substring "localhost" is not present byte-equal, but a host
	// like this in production should still be flagged because IDN-aware
	// normalization can fold it. We assert the validator does the
	// normalization step and rejects the hostname.
	if err := v.Validate(context.Background(), "https://xn--lclhst-rua.example/"); err == nil {
		t.Fatalf("expected punycode/IDN-decoded hostname containing a localhost homoglyph to be rejected")
	}
}

// TestURLValidator_BlocksGCPMetadataDNS — explicit AC coverage. GCP's
// metadata.google.internal canonically resolves to 169.254.169.254
// (link-local), and a malicious DNS or /etc/hosts override could try
// the same trick. We inject a resolver that returns 169.254.169.254
// for the GCP-shaped hostname and assert rejection.
func TestURLValidator_BlocksGCPMetadataDNS(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{
		Resolver: staticResolver{ips: []net.IP{net.ParseIP("169.254.169.254")}},
	})
	if err := v.Validate(context.Background(), "https://metadata.google.internal/computeMetadata/v1/"); err == nil {
		t.Fatalf("expected GCP metadata host to be rejected when DNS lands on 169.254.169.254")
	}
}

// TestURLValidator_BlocksAzureMetadataExplicit — explicit AC coverage
// that Azure's well-known metadata IP is rejected over https as well as
// http (Azure's IMDS lives at 169.254.169.254 just like AWS).
func TestURLValidator_BlocksAzureMetadataExplicit(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{})
	for _, raw := range []string{
		"http://169.254.169.254/metadata/instance",
		"https://169.254.169.254/metadata/instance?api-version=2021-02-01",
	} {
		if err := v.Validate(context.Background(), raw); err == nil {
			t.Errorf("expected Azure metadata URL %s to be rejected", raw)
		}
	}
}

// ---------------------------------------------------------------------
// OP #186 — RED. Validate MUST take a context.Context and propagate it
// into the DNS lookup so a slow / hung resolver cannot pin the API
// goroutine past the request deadline. The test below intentionally
// uses the new signature (ctx, rawURL); today Validate has signature
// (rawURL) and the call will fail to compile. That compile failure is
// the RED. Phase B's job is to update the signature and all three
// production callers.
// ---------------------------------------------------------------------

// blockingResolver simulates a slow DNS lookup: it sleeps for `delay`
// and only returns once `delay` elapses OR the context is cancelled,
// whichever fires first.
type blockingResolver struct {
	delay time.Duration
}

func (b blockingResolver) LookupIP(ctx context.Context, _, _ string) ([]net.IP, error) {
	select {
	case <-time.After(b.delay):
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestURLValidator_PropagatesContextCancellation asserts the caller's
// ctx (not context.Background) bounds the DNS hop. We pass a 10ms ctx
// to a 5s-blocking resolver and require Validate to return promptly
// (within 2s — generous slack for CI scheduling).
func TestURLValidator_PropagatesContextCancellation(t *testing.T) {
	t.Parallel()
	v := handlers.NewURLValidator(handlers.URLValidatorConfig{
		Resolver:      blockingResolver{delay: 5 * time.Second},
		LookupTimeout: 10 * time.Second, // large internal timeout so ONLY the caller ctx can fire
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := v.Validate(ctx, "https://slow.example.com/")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected error from cancelled ctx, got nil after %v", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Validate did not propagate caller ctx — waited %v (expected ~10ms-bounded)", elapsed)
	}
	// Sanity: error must be ctx-cancellation-related, not the SSRF
	// sentinel masquerading. Either the caller ctx error wraps through
	// or ErrSSRFBlocked carries it — either is acceptable as long as it
	// is not a "no addresses returned" success-shaped error.
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, handlers.ErrSSRFBlocked) {
		t.Errorf("expected DeadlineExceeded or ErrSSRFBlocked, got %v", err)
	}
}
