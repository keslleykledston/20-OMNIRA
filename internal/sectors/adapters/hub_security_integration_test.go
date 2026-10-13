package adapters

import (
	"context"
	"testing"
)

// ADR-0044 / ADR-0040: configuring sectors, calendars, shifts, on-call, overflow and SLA is ADMINISTRATION of the company. A Hub operator
// serving the company is never given it, whatever the grant or the contract says, because none of its keys is delegable.
func TestRoutingAdministrationKeysAreNeverDelegableToAHub(t *testing.T) {
	e := newEnv(t)
	for _, k := range []string{"agent.read", "agent.manage", "routing.override"} {
		var n int
		if err := e.seed.QueryRow(context.Background(), `SELECT count(*) FROM permission_domains WHERE permission_key=$1`, k).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%s is delegable: a Hub operator could administer the company's routing", k)
		}
	}
}
