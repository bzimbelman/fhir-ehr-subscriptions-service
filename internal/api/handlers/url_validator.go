// Copyright the fhir-ehr-subscriptions-service authors.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// ErrSSRFBlocked is the sentinel returned by URLValidator.Validate when
// a URL is rejected by SSRF policy. Handlers map this to 400 with an
// OperationOutcome (B-11).
var ErrSSRFBlocked = errors.New("url-validator: blocked by SSRF policy")

// URLValidator vets subscriber-supplied endpoint URLs. It enforces:
//
//   - scheme allowlist (https only, unless AllowHTTP is set)
//   - reject loopback, link-local, RFC1918, ULA, multicast, unspecified,
//     and the cloud metadata IP ranges
//   - the policy is enforced after DNS resolution, so a public hostname
//     that resolves to 10.0.0.5 is also blocked (DNS rebinding defense)
//   - an optional AllowHosts bypass for operator-trusted internal hosts
//
// The same validator is reused at delivery time by the rest-hook
// channel; a single host that flipped DNS between create-time and
// delivery-time will be re-checked.
type URLValidator interface {
	Validate(rawURL string) error
}

// URLValidatorConfig parameterises NewURLValidator.
type URLValidatorConfig struct {
	// AllowHTTP enables the http:// scheme. Default is false (https
	// only). Operators opt in for local dev and known internal hosts.
	AllowHTTP bool

	// AllowHosts is the explicit allow-list of host names whose
	// resolved IPs may land in private CIDRs. Useful for an operator
	// who runs the channel against an internal corp service. Match is
	// case-insensitive equality on the host (no wildcards).
	AllowHosts []string

	// Resolver overrides the default DNS resolver. Tests inject a
	// staticResolver so a public-looking host can be made to resolve
	// to 10.0.0.0/8 deterministically. Production passes nil and the
	// default resolver is used.
	Resolver Resolver

	// LookupTimeout bounds the DNS lookup. Zero means 2s.
	LookupTimeout time.Duration
}

// Resolver is the narrow DNS interface URLValidator depends on. The
// stdlib net.Resolver satisfies it.
type Resolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

// NewURLValidator constructs a URLValidator from cfg.
func NewURLValidator(cfg URLValidatorConfig) URLValidator {
	r := cfg.Resolver
	if r == nil {
		r = net.DefaultResolver
	}
	timeout := cfg.LookupTimeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	allow := make(map[string]struct{}, len(cfg.AllowHosts))
	for _, h := range cfg.AllowHosts {
		allow[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}
	return &urlValidator{
		allowHTTP:     cfg.AllowHTTP,
		allowHosts:    allow,
		resolver:      r,
		lookupTimeout: timeout,
	}
}

type urlValidator struct {
	allowHTTP     bool
	allowHosts    map[string]struct{}
	resolver      Resolver
	lookupTimeout time.Duration
}

func (v *urlValidator) Validate(raw string) error {
	if raw == "" {
		return fmt.Errorf("%w: empty url", ErrSSRFBlocked)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: parse: %v", ErrSSRFBlocked, err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
		// ok
	case "http":
		if !v.allowHTTP {
			return fmt.Errorf("%w: http scheme requires opt-in", ErrSSRFBlocked)
		}
	default:
		return fmt.Errorf("%w: scheme %q not allowed", ErrSSRFBlocked, u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrSSRFBlocked)
	}

	// Reject syntactic loopback hostnames before any DNS hop. A
	// caller-controlled override of `localhost` in /etc/hosts can move
	// it to a non-loopback address; we still want to reject.
	hostLower := strings.ToLower(host)
	if hostLower == "localhost" || hostLower == "ip6-localhost" || hostLower == "ip6-loopback" {
		return fmt.Errorf("%w: loopback hostname", ErrSSRFBlocked)
	}

	// AllowHosts bypasses the network-policy check. The scheme rule is
	// still enforced above.
	if _, ok := v.allowHosts[hostLower]; ok {
		return nil
	}

	// If host is already an IP literal, validate directly without DNS.
	if ip := net.ParseIP(host); ip != nil {
		return v.validateIP(ip, host)
	}

	ctx, cancel := context.WithTimeout(context.Background(), v.lookupTimeout)
	defer cancel()
	ips, err := v.resolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("%w: dns lookup %q: %v", ErrSSRFBlocked, host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: dns lookup %q returned no addresses", ErrSSRFBlocked, host)
	}
	for _, ip := range ips {
		if err := v.validateIP(ip, host); err != nil {
			return err
		}
	}
	return nil
}

func (v *urlValidator) validateIP(ip net.IP, host string) error {
	if ip.IsLoopback() {
		return fmt.Errorf("%w: %q resolves to loopback %s", ErrSSRFBlocked, host, ip)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("%w: %q resolves to unspecified %s", ErrSSRFBlocked, host, ip)
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("%w: %q resolves to link-local %s", ErrSSRFBlocked, host, ip)
	}
	if ip.IsMulticast() {
		return fmt.Errorf("%w: %q resolves to multicast %s", ErrSSRFBlocked, host, ip)
	}
	if ip.IsPrivate() {
		return fmt.Errorf("%w: %q resolves to private %s", ErrSSRFBlocked, host, ip)
	}
	// Cloud metadata IPs (AWS / GCP / OCI) — Go's IsLinkLocalUnicast
	// already covers 169.254.0.0/16, but be explicit for the
	// well-known IPs in case future stdlib changes alter classification.
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 169 && ip4[1] == 254 {
			return fmt.Errorf("%w: %q resolves to cloud-metadata %s", ErrSSRFBlocked, host, ip)
		}
	}
	// IPv6 ULA fc00::/7
	if ip.To4() == nil && len(ip) == net.IPv6len {
		if ip[0]&0xfe == 0xfc {
			return fmt.Errorf("%w: %q resolves to IPv6 ULA %s", ErrSSRFBlocked, host, ip)
		}
	}
	return nil
}
