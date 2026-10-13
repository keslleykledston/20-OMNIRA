package application_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/adapters"
	. "github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/flowstest"
	"github.com/omnira/omnira/internal/flows/ports"
)

// onlyQueues is a ResourceChecker that cannot tell about sectors (an unusual wiring): publishing must fail closed.
type onlyQueues struct{ ports.ResourceChecker }

func TestPublishChecksSectorReferencesAgainstTheTenantAndFailsClosed(t *testing.T) {
	env := flowstest.New(t)
	repo := adapters.NewPostgresFlowRepository(env.App)
	mine, foreign := uuid.New(), uuid.New()
	for sector, tenant := range map[uuid.UUID]uuid.UUID{mine: env.TenantA, foreign: env.TenantB} {
		if _, err := env.Seed.Exec(context.Background(), `INSERT INTO sectors (id, tenant_id, name) VALUES ($1,$2,$3)`, sector, tenant, "S-"+sector.String()[:8]); err != nil {
			t.Fatal(err)
		}
	}
	flow := func(sector uuid.UUID) string {
		return wf(fmt.Sprintf(`{"id":"start","type":"trigger"},{"id":"c","type":"check_schedule","config":{"sector":%q}},{"id":"a","type":"end"},{"id":"b","type":"end"}`, sector),
			strings.Join([]string{edge("1", "start", "next", "c"), edge("2", "c", "open", "a"), edge("3", "c", "closed", "b")}, ","), "")
	}
	has := func(issues []string, code string) bool {
		for _, i := range issues {
			if i == code {
				return true
			}
		}
		return false
	}
	check := func(cp *ControlPlane, sector uuid.UUID) (codes []string) {
		env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
			issues, err := cp.ValidateRaw(ctx, []byte(flow(sector)))
			if err != nil {
				t.Fatal(err)
			}
			for _, i := range issues {
				if i.Severity == "error" {
					codes = append(codes, i.Code)
				}
			}
		})
		return
	}
	cp := NewControlPlane(repo, repo, nil)
	if got := check(cp, mine); len(got) != 0 {
		t.Fatalf("the tenant's own sector must publish: %v", got)
	}
	if got := check(cp, foreign); !has(got, "resource_not_found") {
		t.Fatalf("another company's sector must be refused: %v", got)
	}
	if got := check(cp, uuid.New()); !has(got, "resource_not_found") {
		t.Fatalf("an unknown sector must be refused: %v", got)
	}
	if got := check(NewControlPlane(repo, onlyQueues{repo}, nil), mine); !has(got, "resource_unverifiable") {
		t.Fatalf("a checker that cannot verify sectors must fail closed: %v", got)
	}
}
