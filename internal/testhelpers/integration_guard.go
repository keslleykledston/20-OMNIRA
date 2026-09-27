package testhelpers

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

const (
	integrationTestDatabasePrefix = "omnira_test_"
	productionDatabaseName        = "omnira_dev"
	defaultPostgresPort           = "5432"
)

// RequireIntegrationDatabase is the single entry point every real-Postgres
// integration test must go through to obtain owner/app connection URLs.
//
// TEST.HYGIENE.2: t.Cleanup-based row deletion is not a crash-recovery
// boundary — a killed or timed-out test process never reaches it. This guard
// makes the omnira_dev boundary structural instead of convention-based: it
// never hands back a URL unless it points at a disposable, clearly-labeled
// omnira_test_* database with OMNIRA_INTEGRATION_TEST=1 explicitly set, so a
// developer who happens to inherit OMNIRA_DATABASE_URL from their shell (or
// .env) can never silently drive an integration test against omnira_dev.
func RequireIntegrationDatabase(t *testing.T) (ownerURL, appURL string) {
	t.Helper()

	ownerURL = os.Getenv("OMNIRA_DATABASE_URL")
	appURL = os.Getenv("OMNIRA_APP_DATABASE_URL")

	// CASE A: today's normal unit-test behavior — no integration env at all.
	if ownerURL == "" && appURL == "" {
		t.Skip("OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL not set")
	}

	// CASE B: partial configuration is never safe to guess at.
	if ownerURL == "" || appURL == "" {
		t.Fatal("integration guard: OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL must both be set, or both be empty")
	}

	// CASE C: the explicit marker is required so a shell that happens to
	// export both URLs (e.g. from a developer's .env) cannot silently drive
	// an integration test without an intentional, separate opt-in.
	if os.Getenv("OMNIRA_INTEGRATION_TEST") != "1" {
		t.Fatal("integration guard: OMNIRA_INTEGRATION_TEST=1 is required alongside OMNIRA_DATABASE_URL/OMNIRA_APP_DATABASE_URL")
	}

	ownerDest, err := parseDestination(ownerURL)
	if err != nil {
		t.Fatalf("integration guard: OMNIRA_DATABASE_URL: %v", err)
	}
	appDest, err := parseDestination(appURL)
	if err != nil {
		t.Fatalf("integration guard: OMNIRA_APP_DATABASE_URL: %v", err)
	}
	ownerDB, appDB := ownerDest.database, appDest.database

	// CASE D: the known production/dev database name is always rejected —
	// exact match on the parsed database name, never a substring check, so
	// neither "omnira_dev" is missed nor an unrelated "omnira_dev_backup"-like
	// name is wrongly caught.
	if ownerDB == productionDatabaseName || appDB == productionDatabaseName {
		t.Fatalf("integration guard: refusing to run against database %q", productionDatabaseName)
	}

	// CASE E: only disposable, clearly-labeled test databases are allowed.
	if !strings.HasPrefix(ownerDB, integrationTestDatabasePrefix) {
		t.Fatalf("integration guard: OMNIRA_DATABASE_URL database %q must start with %q", ownerDB, integrationTestDatabasePrefix)
	}
	if !strings.HasPrefix(appDB, integrationTestDatabasePrefix) {
		t.Fatalf("integration guard: OMNIRA_APP_DATABASE_URL database %q must start with %q", appDB, integrationTestDatabasePrefix)
	}

	// CASE F: owner and app must be two roles into the SAME cluster and
	// database — never two different databases (or two different servers),
	// which would silently split fixture writes from the RLS-enforced reads
	// that are supposed to see them. Different usernames/passwords are
	// expected and never compared; only destination identity matters.
	if ownerDest.hostname != appDest.hostname || ownerDest.port != appDest.port || ownerDest.database != appDest.database {
		t.Fatalf("integration guard: OMNIRA_DATABASE_URL and OMNIRA_APP_DATABASE_URL must target the same host, port and database (got %s:%s/%s vs %s:%s/%s)",
			ownerDest.hostname, ownerDest.port, ownerDest.database, appDest.hostname, appDest.port, appDest.database)
	}

	// CASE G: valid, isolated test configuration.
	return ownerURL, appURL
}

// destination is a PostgreSQL connection URL's structurally-parsed identity —
// the (hostname, effective port, database) triple that determines which
// cluster/database a connection actually reaches. Username, password and
// query parameters are deliberately excluded: two roles into the same
// database are expected to differ in exactly those fields.
type destination struct {
	hostname string
	port     string
	database string
}

func parseDestination(rawURL string) (destination, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return destination{}, err
	}
	port := u.Port()
	if port == "" {
		// PostgreSQL connection URLs default to 5432 when the port is
		// omitted; without this, "host/db" and "host:5432/db" would
		// (wrongly) compare as different destinations.
		port = defaultPostgresPort
	}
	return destination{
		hostname: u.Hostname(),
		port:     port,
		database: strings.TrimPrefix(u.Path, "/"),
	}, nil
}
