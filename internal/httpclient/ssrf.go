// Package httpclient is a hardened outbound HTTP client: pooled transport, an
// SSRF guard enforced at dial time, per-call auth, error mapping onto the errs
// hierarchy, and every call wrapped in the resilience registry (breaker+retry).
package httpclient

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"syscall"

	"github.com/prajwalmahajan101/busyapi/internal/errs"
)

// Validation messages for outbound URL guarding. Named so tests assert on the
// same string the guard emits.
const (
	msgInvalidURL  = "invalid url"
	msgBadScheme   = "url scheme must be http or https"
	msgNoHost      = "url has no host"
	msgBlockedAddr = "url resolves to a blocked address"
)

// AssertPublicURL is a cheap early reject before any network work: it validates
// scheme and host and, when the host is an IP literal, rejects private/loopback/
// link-local addresses. It does NOT resolve DNS — the authoritative check runs at
// dial time (dialControl) to defeat DNS-rebinding/TOCTOU. allowPrivate mirrors
// Config.HTTPAllowPrivateIPs.
func AssertPublicURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errs.NewValidation(msgInvalidURL, map[string]any{"error": err.Error()})
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errs.NewValidation(msgBadScheme, map[string]any{"scheme": u.Scheme})
	}
	host := u.Hostname()
	if host == "" {
		return errs.NewValidation(msgNoHost, nil)
	}
	if allowPrivate {
		return nil
	}
	// Only reject here when the host is a literal IP; hostnames are checked at dial.
	if addr, perr := netip.ParseAddr(host); perr == nil && blockedIP(addr) {
		return errs.NewValidation(msgBlockedAddr, map[string]any{"host": host})
	}
	return nil
}

// SafeHost returns just the host of raw for logging, dropping scheme, path and
// any embedded credentials. Returns "invalid" when raw cannot be parsed.
func SafeHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "invalid"
	}
	return u.Hostname()
}

// dialControl returns a net.Dialer.Control hook. It runs after DNS resolution and
// before connect, on the actual ip:port being dialed — the TOCTOU-safe point to
// block SSRF. allowPrivate short-circuits the check for local development.
func dialControl(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("ssrf: bad dial address %q: %w", address, err)
		}
		addr, err := netip.ParseAddr(host)
		if err != nil {
			return fmt.Errorf("ssrf: dial address not an ip %q: %w", host, err)
		}
		if blockedIP(addr) {
			return fmt.Errorf("ssrf: blocked dial to %s", addr)
		}
		return nil
	}
}

// blockedIP reports whether ip is one an outbound call must never reach:
// loopback, private (RFC1918 + IPv6 ULA fc00::/7 via IsPrivate), link-local
// (covers the 169.254.169.254 cloud-metadata endpoint), unspecified, or multicast.
func blockedIP(ip netip.Addr) bool {
	ip = ip.Unmap() // treat ::ffff:10.0.0.1 as the IPv4 it wraps
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}
