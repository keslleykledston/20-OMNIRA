package netpolicy_test

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/omnira/omnira/internal/external/netpolicy"
)

func TestBlockedIPCoversEveryNetworkThePlatformMustNeverCall(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.255.255.254", "::1", "0.0.0.0", "::", "10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255", "192.168.0.1", "192.168.255.255",
		"169.254.169.254", "169.254.0.1", "100.64.0.1", "100.127.255.255", "224.0.0.1", "239.255.255.255", "240.0.0.1", "255.255.255.255", "198.18.0.1",
		"192.0.2.1", "198.51.100.1", "203.0.113.1", "fc00::1", "fd12:3456::1", "fe80::1", "ff02::1", "2001:db8::1", "64:ff9b::a00:1",
		"::ffff:10.0.0.1", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:192.168.1.1",
	}
	for _, s := range blocked {
		if !netpolicy.BlockedIP(net.ParseIP(s)) {
			t.Errorf("%s must be blocked", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "172.15.255.255", "172.32.0.1", "192.169.0.1", "100.63.255.255", "2606:4700:4700::1111", "::ffff:8.8.8.8"} {
		if netpolicy.BlockedIP(net.ParseIP(s)) {
			t.Errorf("%s is public and must be allowed", s)
		}
	}
	if !netpolicy.BlockedIP(nil) {
		t.Error("an unparsable address must be blocked")
	}
}

func TestValidateURL(t *testing.T) {
	good := []string{"https://crm.example.com/hook", "https://crm.example.com:8443/a/b?x=1", "https://8.8.8.8/hook", "https://[2606:4700:4700::1111]/h"}
	for _, u := range good {
		if err := netpolicy.ValidateURL(u); err != nil {
			t.Errorf("%s refused: %v", u, err)
		}
	}
	bad := []string{
		"", "http://crm.example.com/hook", "ftp://crm.example.com/", "https://", "https:///x", "//crm.example.com/x", "https://user:pw@crm.example.com/x",
		"https://crm.example.com/x#frag", "https://crm.example.com:80/x", "https://crm.example.com:22/x", "https://crm.example.com:8080/x", "https://crm.example.com:65535/x",
		"https://localhost/x", "https://api.localhost/x", "https://127.0.0.1/x", "https://[::1]/x", "https://10.0.0.5/x", "https://169.254.169.254/latest/meta-data",
		"https://metadata.internal/x", "https://printer.local/x", "https://192.168.1.1:8443/x", "https://[fd00::1]/x", "https://[::ffff:10.0.0.1]/x", "https://exa mple.com/x",
	}
	for _, u := range bad {
		if err := netpolicy.ValidateURL(u); err == nil {
			t.Errorf("%q must be refused", u)
		}
	}
}

func fakeResolver(m map[string][]string, calls *atomic.Int32) func(context.Context, string) ([]net.IP, error) {
	return func(_ context.Context, host string) ([]net.IP, error) {
		calls.Add(1)
		var out []net.IP
		for _, s := range m[host] {
			out = append(out, net.ParseIP(s))
		}
		if len(out) == 0 {
			return nil, errors.New("no such host")
		}
		return out, nil
	}
}

func tlsFor(srv *httptest.Server) *tls.Config {
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	return &tls.Config{RootCAs: pool}
}

// The protection is in the connection: names that resolve to a private address (or to both a public and a private one) are never dialed, the
// connection goes to the address that was checked (a second resolution cannot swap it: DNS rebinding), and redirects are not followed.
func TestClientRefusesPrivateDestinationsRebindingAndRedirects(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "https://169.254.169.254/latest", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port := u.Port()
	ports := map[string]bool{port: true}
	var resolves atomic.Int32
	resolver := fakeResolver(map[string][]string{
		"public.example.test":  {"127.0.0.1"}, // stands for "a public address" below, via the Allow override
		"private.example.test": {"10.1.2.3"},
		"mixed.example.test":   {"127.0.0.1", "10.1.2.3"},
		"meta.example.test":    {"169.254.169.254"},
		"v6.example.test":      {"fd00::5"},
	}, &resolves)

	// production policy: loopback (where the fake receiver listens) is private, so everything is refused and nothing reaches the server
	prod := netpolicy.NewClient(netpolicy.Options{Resolve: resolver, TLSConfig: tlsFor(srv), Ports: ports})
	for _, host := range []string{"public.example.test", "private.example.test", "mixed.example.test", "meta.example.test", "v6.example.test"} {
		resp, err := prod.Get("https://" + host + ":" + port + "/")
		if err == nil {
			resp.Body.Close()
			t.Errorf("%s was reached with the production policy", host)
		} else if !errors.Is(err, netpolicy.ErrBlockedAddress) {
			t.Errorf("%s: %v, want ErrBlockedAddress", host, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("the receiver was contacted %d times by a client that must have refused", hits.Load())
	}
	// IP literals are judged too
	if _, err := prod.Get("https://127.0.0.1:" + port + "/"); !errors.Is(err, netpolicy.ErrBlockedAddress) {
		t.Errorf("loopback literal: %v", err)
	}
	// a port outside the allow-list is refused before any connection
	if _, err := netpolicy.NewClient(netpolicy.Options{TLSConfig: tlsFor(srv)}).Get("https://127.0.0.1:" + port + "/"); err == nil {
		t.Error("a port outside 443/8443 was dialed")
	}

	// a test policy that allows only the loopback address of the fake receiver: the mixed answer is STILL refused, because one of its addresses is not allowed
	onlyLoopback := func(ip net.IP) bool { return ip.IsLoopback() }
	lax := netpolicy.NewClient(netpolicy.Options{Resolve: resolver, Allow: onlyLoopback, TLSConfig: tlsFor(srv), Ports: ports})
	resolves.Store(0)
	if _, err := lax.Get("https://mixed.example.test:" + port + "/"); !errors.Is(err, netpolicy.ErrBlockedAddress) {
		t.Errorf("a name with one allowed and one disallowed address must be refused: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the mixed answer reached the receiver")
	}
	// the allowed name works, resolves exactly once per call, and the connection is made to the checked address
	resolves.Store(0)
	req, _ := http.NewRequest(http.MethodGet, "https://public.example.test:"+port+"/", nil)
	req.Host = "public.example.test"
	cl := netpolicy.NewClient(netpolicy.Options{Resolve: resolver, Allow: onlyLoopback, Ports: ports,
		TLSConfig: &tls.Config{InsecureSkipVerify: true}}) // the fake certificate is for 127.0.0.1; verification is not what this test is about
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatalf("allowed name: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || hits.Load() != 1 || resolves.Load() != 1 {
		t.Fatalf("status=%d hits=%d resolves=%d, want 204 / 1 / 1", resp.StatusCode, hits.Load(), resolves.Load())
	}
	// redirects are never followed: the 302 is the answer, the second URL is never requested
	req2, _ := http.NewRequest(http.MethodGet, "https://public.example.test:"+port+"/redirect", nil)
	resp, err = cl.Do(req2)
	if err != nil {
		t.Fatalf("redirect: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || hits.Load() != 2 {
		t.Fatalf("redirect answered %d with %d hits: it must be returned, not followed", resp.StatusCode, hits.Load())
	}
}

// The port is judged at every connection, not only when the address is saved: a name or a redirect-free URL pointing at a port outside the list is never dialed.
func TestClientNeverDialsAPortOutsideTheList(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	client := netpolicy.NewClient(netpolicy.Options{
		Allow: func(ip net.IP) bool { return ip.IsLoopback() }, // the address would be fine; the port is the only reason to refuse
		Ports: map[string]bool{"443": true},
	})
	resp, err := client.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("a port outside the list was dialed")
	}
	if !errors.Is(err, netpolicy.ErrBadURL) {
		t.Fatalf("err = %v, want ErrBadURL", err)
	}
	if hits.Load() != 0 {
		t.Fatal("the receiver was contacted")
	}
}
