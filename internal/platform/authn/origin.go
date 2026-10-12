package authn

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
)

// Same-origin check for requests the browser authenticates with the session COOKIE (defense in depth against CSRF; the cookie is SameSite=Lax, which stops the
// ordinary cross-site form post but not a request from a sibling subdomain, a compromised neighbour on the same site, or a future change of the attribute).
//
// A state-changing request (anything but GET/HEAD/OPTIONS/TRACE) that carries the cookie must say where it comes from, and where it comes from must be this
// application: browsers always send `Origin` on such requests (and `Referer` is the fallback). Bearer-token callers are not subject to it: a token does not
// ride along on a request the attacker's page makes.
//
// An origin is accepted when it is
//   - one of the configured ones (OMNIRA_ALLOWED_ORIGINS, plus the origins of OMNIRA_AUTH_REDIRECT_URL and OMNIRA_WEB_BASE_URL, which name the public address), or
//   - the host this request was addressed to (Host, or X-Forwarded-Host when a proxy in front rewrites it), or
//   - a loopback address (a developer's own machine; the cookie of a remote site is never sent to it).
// It is OFF until the server turns it on (EnableOriginCheck), so unit tests of unrelated handlers keep their plain requests.

type originConfig struct{ allowed map[string]bool }

var originGuard atomic.Pointer[originConfig]

// EnableOriginCheck turns the check on for every cookie-authenticated unsafe request. extra are origins ("https://host[:port]") or URLs whose origin counts.
func EnableOriginCheck(extra ...string) {
	c := &originConfig{allowed: map[string]bool{}}
	for _, e := range extra {
		if o := originOf(e); o != "" {
			c.allowed[o] = true
		}
	}
	originGuard.Store(c)
}

// DisableOriginCheck is for tests.
func DisableOriginCheck() { originGuard.Store(nil) }

// originOf reduces a URL or an origin to "scheme://host[:port]" in lower case ("" when it is not one).
func originOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func firstForwarded(v string) string {
	if i := strings.Index(v, ","); i >= 0 {
		v = v[:i]
	}
	return strings.ToLower(strings.TrimSpace(v))
}

// sameOriginOK reports whether a request authenticated by the session cookie may proceed. Safe methods and a disabled check always pass.
func sameOriginOK(r *http.Request) bool {
	c := originGuard.Load()
	if c == nil {
		return true
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || strings.EqualFold(origin, "null") {
		// no Origin (an old client or a request that strips it): the Referer says where the page was; with neither, a cookie request is refused
		origin = originOf(r.Header.Get("Referer"))
	} else {
		origin = originOf(origin)
	}
	if origin == "" {
		return false
	}
	if c.allowed[origin] {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if isLoopbackHost(u.Host) {
		return true
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if fh := firstForwarded(r.Header.Get("X-Forwarded-Host")); fh != "" && strings.EqualFold(u.Host, fh) {
		return true
	}
	return false
}

func refuseCrossSite(w http.ResponseWriter) {
	http.Error(w, "cross-site request refused", http.StatusForbidden)
}
