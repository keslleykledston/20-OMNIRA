package testhelpers

import "testing"

// These cover only the pure, no-network parsing logic. Full guard proofs
// (marker/run-id/denylist/loopback/attestation FAIL-closed cases, and the
// PASS case against a real disposable server) are proven as direct,
// separate-process `go test` invocations — see TEST.HYGIENE.3 human gate for
// the exact commands and outcomes. A subtest that calls t.Fatal/t.Skip marks
// every ancestor failed/skipped with no way to un-mark it, so those cases
// cannot be asserted in-process without misreporting this package's own test
// result (same reasoning as internal/testhelpers/integration_guard_test.go).

func TestNatsHostPort_DefaultsAndExplicitPort(t *testing.T) {
	cases := map[string]string{
		"nats://127.0.0.1:4222":    "127.0.0.1:4222",
		"nats://127.0.0.1":         "127.0.0.1:4222",
		"nats://localhost:55432":   "localhost:55432",
		"tls://nats-host.internal": "nats-host.internal:4222",
		"nats://nats:4222":         "nats:4222",
	}
	for in, want := range cases {
		got, err := natsHostPort(in)
		if err != nil {
			t.Fatalf("natsHostPort(%q): unexpected error: %v", in, err)
		}
		if got != want {
			t.Fatalf("natsHostPort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsLoopbackHostPort(t *testing.T) {
	loopback := []string{"127.0.0.1:4222", "localhost:4222", "[::1]:4222"}
	for _, hp := range loopback {
		if !isLoopbackHostPort(hp) {
			t.Fatalf("isLoopbackHostPort(%q) = false, want true", hp)
		}
	}
	notLoopback := []string{"nats:4222", "10.0.0.9:4222", "example.com:4222"}
	for _, hp := range notLoopback {
		if isLoopbackHostPort(hp) {
			t.Fatalf("isLoopbackHostPort(%q) = true, want false", hp)
		}
	}
}

func TestKnownPilotNATSHosts_ContainsRepositoryAddresses(t *testing.T) {
	for _, want := range []string{"127.0.0.1:4222", "localhost:4222", "nats:4222"} {
		if !knownPilotNATSHosts[want] {
			t.Fatalf("knownPilotNATSHosts missing %q", want)
		}
	}
}
