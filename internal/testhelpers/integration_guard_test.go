package testhelpers_test

import (
	"testing"

	"github.com/omnira/omnira/internal/testhelpers"
)

// These cover only the paths that do not call t.Fatal/t.Skip: a Go subtest
// that fails or skips marks every ancestor as failed/skipped too, with no
// public way to un-mark it, so the FAIL-closed and SKIP cases (B, C, D, E,
// F, G, H, and the destination-mismatch cases from TEST.HYGIENE.2F §3) are
// proven instead as direct, separate-process `go test` invocations against a
// real guarded package — see TEST.HYGIENE.2F human gate for the exact
// commands and their exit codes. Embedding a subprocess-spawning harness here
// just to keep those in-process would be more machinery than this guard
// warrants.

const (
	validOwner = "postgres://owner_user:ownerpw@127.0.0.1:5555/omnira_test_x?sslmode=disable"
	validApp   = "postgres://app_user:apppw@127.0.0.1:5555/omnira_test_x?sslmode=disable"
)

// CASE A: same host, same effective port, same database, different
// usernames/passwords — must PASS and return both URLs unchanged.
func TestRequireIntegrationDatabase_CaseA_SameDestinationDifferentUsers_Passes(t *testing.T) {
	t.Setenv("OMNIRA_DATABASE_URL", validOwner)
	t.Setenv("OMNIRA_APP_DATABASE_URL", validApp)
	t.Setenv("OMNIRA_INTEGRATION_TEST", "1")

	owner, app := testhelpers.RequireIntegrationDatabase(t)
	if owner != validOwner || app != validApp {
		t.Fatalf("expected URLs returned unchanged, got owner=%q app=%q", owner, app)
	}
}

// Default port omitted on one side, explicit :5432 on the other — must still
// be treated as the SAME destination (effective port normalization).
func TestRequireIntegrationDatabase_DefaultPortNormalized_Passes(t *testing.T) {
	t.Setenv("OMNIRA_DATABASE_URL", "postgres://owner:pw@127.0.0.1/omnira_test_x")
	t.Setenv("OMNIRA_APP_DATABASE_URL", "postgres://app:pw@127.0.0.1:5432/omnira_test_x")
	t.Setenv("OMNIRA_INTEGRATION_TEST", "1")

	owner, app := testhelpers.RequireIntegrationDatabase(t)
	if owner == "" || app == "" {
		t.Fatal("expected success: an omitted port and an explicit :5432 must compare as the same destination")
	}
}
