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

	"golang.org/x/net/idna"
)

// ErrSSRFBlocked is the sentinel returned by URLValidator.Validate when
// a URL is rejected by SSRF policy. Handlers map this to 400 with an
// OperationOutcome (B-11).
var ErrSSRFBlocked = errors.New("url-validator: blocked by SSRF policy")

// URLValidator vets subscriber-supplied endpoint URLs. It enforces:
//
//   - scheme allowlist (https only, unless AllowHTTP is set)
//   - reject loopback, link-local, RFC1918, ULA, multicast, unspecified,
//     CG-NAT, IPv6 site-local, broadcast, and the cloud metadata IP ranges
//   - the policy is enforced after DNS resolution, so a public hostname
//     that resolves to 10.0.0.5 is also blocked (DNS rebinding defense)
//   - IDN / Punycode hostnames are normalized to canonical ASCII form
//     (idna.Lookup.ToASCII) BEFORE the loopback substring gate, so a
//     homoglyph host like "loсalhost" (Cyrillic 'с') cannot bypass the
//     check; any host whose canonical form differs from the literal
//     input — i.e. it was IDN-encoded — is rejected as suspicious
//   - an optional AllowHosts bypass for operator-trusted internal hosts
//
// The same validator is reused at delivery time by the rest-hook
// channel; a single host that flipped DNS between create-time and
// delivery-time will be re-checked.
type URLValidator interface {
	Validate(ctx context.Context, rawURL string) error
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
		// Normalize allow-list entries through idna.Lookup.ToASCII so
		// an operator who lists an IDN hostname matches the same
		// canonical form Validate computes from the request URL.
		normalized := strings.ToLower(strings.TrimSpace(h))
		if ascii, err := idna.Lookup.ToASCII(normalized); err == nil {
			normalized = ascii
		}
		allow[normalized] = struct{}{}
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

func (v *urlValidator) Validate(ctx context.Context, raw string) error {
	if ctx == nil {
		ctx = context.Background()
	}
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

	// Run the host through IDN normalization BEFORE the loopback
	// substring check. The Lookup profile (RFC 5891 §5.4) is the
	// strict-validation profile clients use to resolve a hostname; it
	// rejects characters outside the registered IDN code-point set
	// (e.g. ZWJ, fullwidth digits). For a host that is already a bare
	// IP literal or ASCII hostname, ToASCII is a no-op.
	//
	// IPv6 literals come back from u.Hostname() with the brackets
	// stripped (e.g. "::1"); Lookup.ToASCII rejects ":" so we skip the
	// IDN step when the host parses as an IP.
	parsedIP := net.ParseIP(host)
	asciiHost := host
	if parsedIP == nil {
		canonical, ierr := idna.Lookup.ToASCII(host)
		if ierr != nil {
			return fmt.Errorf("%w: idn normalize %q: %v", ErrSSRFBlocked, host, ierr)
		}
		// If the canonical ASCII form differs from the lowercased
		// literal input, the host was IDN-encoded. Treat any such host
		// as suspicious — a homoglyph attack converging on
		// "localhost" or any other reserved name would land here. The
		// AllowHosts bypass below uses the canonical form, so an
		// operator who needs an IDN endpoint can opt in explicitly.
		//
		// We compare against strings.ToLower(host) (NOT
		// strings.EqualFold) because we want to detect any character
		// that ToASCII transformed — case folding alone is not enough,
		// since idna.Lookup.ToASCII performs Unicode-aware folding
		// that EqualFold does not match.
		if canonical != strings.ToLower(host) { //nolint:gocritic // EqualFold semantically wrong here — see comment above
			return fmt.Errorf("%w: IDN-encoded host %q (canonical %q) requires explicit allow-listing",
				ErrSSRFBlocked, host, canonical)
		}
		// A host whose canonical form contains the punycode prefix is
		// also IDN — reject for the same reason.
		if strings.Contains(canonical, "xn--") {
			return fmt.Errorf("%w: punycode host %q requires explicit allow-listing",
				ErrSSRFBlocked, canonical)
		}
		asciiHost = canonical
	}

	// Reject syntactic loopback hostnames before any DNS hop. A
	// caller-controlled override of `localhost` in /etc/hosts can move
	// it to a non-loopback address; we still want to reject. The check
	// runs against the canonical ASCII form so an IDN homoglyph cannot
	// dodge it.
	hostLower := strings.ToLower(asciiHost)
	if hostLower == "localhost" || hostLower == "ip6-localhost" || hostLower == "ip6-loopback" {
		return fmt.Errorf("%w: loopback hostname", ErrSSRFBlocked)
	}

	// AllowHosts bypasses the network-policy check. The scheme rule is
	// still enforced above. Match against the canonical ASCII form.
	if _, ok := v.allowHosts[hostLower]; ok {
		return nil
	}

	// If host is already an IP literal, validate directly without DNS.
	if parsedIP != nil {
		return v.validateIP(parsedIP, host)
	}

	lookupCtx, cancel := context.WithTimeout(ctx, v.lookupTimeout)
	defer cancel()
	ips, err := v.resolver.LookupIP(lookupCtx, "ip", asciiHost)
	if err != nil {
		return fmt.Errorf("%w: dns lookup %q: %v", ErrSSRFBlocked, asciiHost, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("%w: dns lookup %q returned no addresses", ErrSSRFBlocked, asciiHost)
	}
	for _, ip := range ips {
		if err := v.validateIP(ip, asciiHost); err != nil {
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
	// IPv4-specific blocks: broadcast, CG-NAT, cloud metadata.
	if ip4 := ip.To4(); ip4 != nil {
		// 255.255.255.255 (limited broadcast) — net.IP.IsLinkLocalUnicast
		// and friends do not flag this, so we check explicitly.
		if ip.Equal(net.IPv4bcast) {
			return fmt.Errorf("%w: %q resolves to IPv4 broadcast %s", ErrSSRFBlocked, host, ip)
		}
		// CG-NAT 100.64.0.0/10 (RFC 6598). The shared-address space is
		// reachable across carrier boundaries and must never be a
		// subscriber endpoint target — treat it as private.
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return fmt.Errorf("%w: %q resolves to CG-NAT %s (RFC 6598)", ErrSSRFBlocked, host, ip)
		}
		// Cloud metadata IPs (AWS / GCP / OCI / Azure) — Go's
		// IsLinkLocalUnicast already covers 169.254.0.0/16, but be
		// explicit for the well-known IPs in case future stdlib changes
		// alter classification.
		if ip4[0] == 169 && ip4[1] == 254 {
			return fmt.Errorf("%w: %q resolves to cloud-metadata %s", ErrSSRFBlocked, host, ip)
		}
	}
	// IPv6-specific blocks: ULA fc00::/7 and site-local fec0::/10.
	if ip.To4() == nil && len(ip) == net.IPv6len {
		if ip[0]&0xfe == 0xfc {
			return fmt.Errorf("%w: %q resolves to IPv6 ULA %s", ErrSSRFBlocked, host, ip)
		}
		// Site-local fec0::/10 (deprecated by RFC 3879 but still seen
		// on legacy networks; never a valid subscriber target).
		if ip[0] == 0xfe && (ip[1]&0xc0) == 0xc0 {
			return fmt.Errorf("%w: %q resolves to IPv6 site-local %s", ErrSSRFBlocked, host, ip)
		}
	}
	return nil
}
