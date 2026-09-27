package testhelpers

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// IntegrationNATSConfig is what a real-NATS integration test receives once
// RequireIntegrationNATS has structurally verified the destination and
// attested the connected server's identity. MonitorURL is empty when the
// test's env did not set OMNIRA_NATS_MONITOR_URL — callers that do not need
// it must not be forced to use it.
type IntegrationNATSConfig struct {
	URL        string
	MonitorURL string
	RunID      string
}

// knownPilotNATSHosts are the exact host:port pairs this repository actually
// uses to address the shared dev/pilot NATS server — via the loopback port
// published in docker-compose.yml (127.0.0.1:4222 / localhost:4222) or via
// the in-network compose service name (nats:4222). Rejected unconditionally,
// before any connection is attempted.
var knownPilotNATSHosts = map[string]bool{
	"127.0.0.1:4222": true,
	"localhost:4222": true,
	"nats:4222":      true,
}

// RequireIntegrationNATS is the single entry point every real-NATS
// integration test must go through.
//
// TEST.HYGIENE.3: unique resource names alone stop tests from colliding with
// each other, but they do nothing to stop a test from silently mutating the
// shared dev/pilot NATS server (creating/leaking a stream there) when a
// developer's shell already exports OMNIRA_NATS_URL. This guard rejects the
// two known dev/pilot addresses structurally, requires the same
// OMNIRA_INTEGRATION_TEST=1 opt-in used by the PostgreSQL guard, requires an
// explicit run identity, and — because a denylist of known addresses is
// never a complete list — attests that the server on the other end actually
// identifies itself as the disposable server the runner started for this
// exact run, via a read-only connection made BEFORE returning control to the
// test. No stream/consumer/publish call happens before this function
// returns.
func RequireIntegrationNATS(t *testing.T) IntegrationNATSConfig {
	t.Helper()

	natsURL := os.Getenv("OMNIRA_NATS_URL")
	// CASE A: today's normal unit-test behavior — no integration NATS env at all.
	if natsURL == "" {
		t.Skip("OMNIRA_NATS_URL not set")
	}

	// CASE B: the explicit marker is required, same as the PostgreSQL guard —
	// one consistent opt-in for the whole integration suite.
	if os.Getenv("OMNIRA_INTEGRATION_TEST") != "1" {
		t.Fatal("integration guard: OMNIRA_INTEGRATION_TEST=1 is required alongside OMNIRA_NATS_URL")
	}

	// CASE C: an explicit run identity is required — it is both what makes
	// resource names traceable back to a run and, combined with the expected
	// server name below, what the identity attestation is checked against.
	runID := os.Getenv("OMNIRA_INTEGRATION_RUN_ID")
	if runID == "" {
		t.Fatal("integration guard: OMNIRA_INTEGRATION_RUN_ID is required alongside OMNIRA_NATS_URL")
	}
	expectedServerName := os.Getenv("OMNIRA_NATS_SERVER_NAME")
	if expectedServerName == "" {
		t.Fatal("integration guard: OMNIRA_NATS_SERVER_NAME is required alongside OMNIRA_NATS_URL")
	}

	host, err := natsHostPort(natsURL)
	if err != nil {
		t.Fatalf("integration guard: OMNIRA_NATS_URL: %v", err)
	}

	// CASE D: the known dev/pilot addresses are always rejected, before any
	// connection is attempted.
	if knownPilotNATSHosts[host] {
		t.Fatalf("integration guard: refusing to run against known dev/pilot NATS endpoint %q", host)
	}

	// CASE E: the approved runner model is a Linux-host disposable server
	// bound to loopback only; anything else is not a destination this guard
	// can vouch for.
	if !isLoopbackHostPort(host) {
		t.Fatalf("integration guard: OMNIRA_NATS_URL host %q is not loopback — only the local disposable-runner model is approved", host)
	}

	// CASE F: identity attestation. A read-only connection (no Publish, no
	// stream/consumer creation) whose only purpose is to read the server's
	// own advertised name back from the CONNECT/INFO handshake.
	actualServerName, err := attestNATSServerIdentity(natsURL)
	if err != nil {
		t.Fatalf("integration guard: could not attest NATS server identity: %v", err)
	}
	if actualServerName != expectedServerName {
		t.Fatalf("integration guard: connected NATS server identifies as %q, expected disposable server %q — refusing to proceed", actualServerName, expectedServerName)
	}

	monitorURL := os.Getenv("OMNIRA_NATS_MONITOR_URL")
	if monitorURL != "" {
		if err := attestNATSMonitorIdentity(monitorURL, expectedServerName); err != nil {
			t.Fatalf("integration guard: OMNIRA_NATS_MONITOR_URL: %v", err)
		}
	}

	return IntegrationNATSConfig{URL: natsURL, MonitorURL: monitorURL, RunID: runID}
}

func natsHostPort(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("no host in %q", rawURL)
	}
	port := u.Port()
	if port == "" {
		port = "4222" // NATS client default port
	}
	return host + ":" + port, nil
}

func isLoopbackHostPort(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// attestNATSServerIdentity opens a short-lived connection purely to read the
// server's self-reported name from the handshake, then closes it — no
// stream, consumer or message is ever created or published by this call.
func attestNATSServerIdentity(natsURL string) (string, error) {
	nc, err := nats.Connect(natsURL, nats.Timeout(5*time.Second), nats.Name("omnira-integration-guard-attestation"))
	if err != nil {
		return "", err
	}
	defer nc.Close()
	return nc.ConnectedServerName(), nil
}

// attestNATSMonitorIdentity performs a single read-only GET against the
// monitoring endpoint's /varz and compares the reported server_name — it
// never issues any state-changing request.
func attestNATSMonitorIdentity(monitorURL, expectedServerName string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(strings.TrimRight(monitorURL, "/") + "/varz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var v struct {
		ServerName string `json:"server_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return fmt.Errorf("decoding /varz: %w", err)
	}
	if v.ServerName != expectedServerName {
		return fmt.Errorf("monitor endpoint reports server_name=%q, expected %q", v.ServerName, expectedServerName)
	}
	return nil
}
