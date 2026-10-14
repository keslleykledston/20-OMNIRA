// Package netpolicy is the one place that decides which destinations the platform may call on behalf of a tenant (ADR-0046 H4): the HTTPS client that cannot be turned
// against the platform's own network, and the validation of a webhook address.
package netpolicy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrBlockedAddress: the destination resolves to an address the platform never calls (private, loopback, link-local, cloud metadata, multicast...).
var ErrBlockedAddress = errors.New("delivery: destination address is not allowed")

// ErrBadURL: the address of a webhook is not an acceptable one.
var ErrBadURL = errors.New("delivery: the webhook URL is not acceptable")

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // "this" network
		"10.0.0.0/8",      // private
		"100.64.0.0/10",   // carrier-grade NAT (also where some clouds put their metadata)
		"127.0.0.0/8",     // loopback
		"169.254.0.0/16",  // link-local, AWS/GCP/Azure metadata
		"172.16.0.0/12",   // private
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // documentation
		"192.168.0.0/16",  // private
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"224.0.0.0/4",     // multicast
		"240.0.0.0/4",     // reserved, incl. broadcast
		"::/128",          // unspecified
		"::1/128",         // loopback
		"64:ff9b::/96",    // NAT64 (could reach an IPv4 address behind the translator)
		"100::/64",        // discard-only
		"2001:db8::/32",   // documentation
		"fc00::/7",        // unique local
		"fe80::/10",       // link-local
		"ff00::/8",        // multicast
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// BlockedIP says whether the platform must never connect to ip. IPv4-mapped IPv6 addresses are judged as the IPv4 address they carry (::ffff:10.0.0.1 is private).
func BlockedIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsUnspecified() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// AllowedPorts are the only ports a webhook may use.
var AllowedPorts = map[string]bool{"443": true, "8443": true}

// ValidateURL is the check made when a subscription is saved: https, a host name or a public IP literal, no credentials, no fragment, an allowed port.
// It is NOT the protection against rebinding (the address is checked again at every connection).
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || len(raw) > 2000 {
		return ErrBadURL
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, " \t\r\n") {
		return ErrBadURL
	}
	if port := u.Port(); port != "" && !AllowedPorts[port] {
		return fmt.Errorf("%w: port %s is not allowed", ErrBadURL, port)
	}
	if ip := net.ParseIP(host); ip != nil && BlockedIP(ip) {
		return ErrBlockedAddress
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".internal") || strings.HasSuffix(lower, ".local") {
		return ErrBlockedAddress
	}
	return nil
}

// Options of the safe client. The zero value is the production policy.
type Options struct {
	// Resolve maps a host name to addresses. Default: the system resolver.
	Resolve func(ctx context.Context, host string) ([]net.IP, error)
	// Allow decides whether an address may be dialed. Default: !BlockedIP. Tests that talk to a local fake receiver replace it; production never does.
	Allow func(net.IP) bool
	// DialTimeout / Timeout bound the connection and the whole call.
	DialTimeout, Timeout time.Duration
	// TLSConfig, for tests (a private CA). Default: the system roots.
	TLSConfig *tls.Config
	// Ports overrides AllowedPorts (tests use an ephemeral port).
	Ports map[string]bool
}

// NewClient returns the HTTP client every webhook call goes through. Its protections are in the connection itself, not in a check made before it:
//   - the host is resolved HERE and the connection is made to the address that was checked (no second resolution, so DNS rebinding finds no gap);
//   - every resolved address must be allowed, otherwise the call is refused (a name with one public and one private record is refused);
//   - redirects are never followed (a 3xx is a failure the caller records);
//   - https only, an allowed port, bounded time, and the response body is never trusted nor kept.
func NewClient(o Options) *http.Client {
	if o.Resolve == nil {
		o.Resolve = func(ctx context.Context, host string) ([]net.IP, error) {
			addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			out := make([]net.IP, 0, len(addrs))
			for _, a := range addrs {
				out = append(out, a.IP)
			}
			return out, nil
		}
	}
	if o.Allow == nil {
		o.Allow = func(ip net.IP) bool { return !BlockedIP(ip) }
	}
	if o.DialTimeout == 0 {
		o.DialTimeout = 5 * time.Second
	}
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Ports == nil {
		o.Ports = AllowedPorts
	}
	dialer := &net.Dialer{Timeout: o.DialTimeout}
	tr := &http.Transport{
		Proxy:                  nil, // never through a proxy configured in the environment
		DisableKeepAlives:      true,
		TLSClientConfig:        o.TLSConfig,
		TLSHandshakeTimeout:    o.DialTimeout,
		ResponseHeaderTimeout:  o.Timeout,
		MaxResponseHeaderBytes: 16 << 10,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, ErrBadURL
			}
			if !o.Ports[port] {
				return nil, fmt.Errorf("%w: port %s", ErrBadURL, port)
			}
			var ips []net.IP
			if ip := net.ParseIP(host); ip != nil {
				ips = []net.IP{ip}
			} else if ips, err = o.Resolve(ctx, host); err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, errors.New("delivery: the host does not resolve")
			}
			for _, ip := range ips {
				if !o.Allow(ip) {
					return nil, ErrBlockedAddress
				}
			}
			var last error
			for _, ip := range ips { // connect to the ADDRESS that was checked, never to the name again
				c, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       o.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// PortOf is a small helper for tests.
func PortOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if p := u.Port(); p != "" {
		return p
	}
	return strconv.Itoa(443)
}
