package adapters_test

// Ties the two authorization barriers together on a real PostgreSQL: for every state of a grant, contract,
// hub and queue scope, the Go HubAuthorizationService (real repository, running inside the actor's own
// RLS session) and PostgreSQL RLS must reach the SAME decision. A drift between them is a bug in one.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/adapters"
	"github.com/omnira/omnira/internal/hub/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func (w *world) resolve(actor, hub uuid.UUID, tenantKey string, queue *uuid.UUID) (*tenancydomain.TenantContext, error) {
	var tc *tenancydomain.TenantContext
	err := platformdb.WithTenantSession(w.ctx, w.app, actor, false, func(c context.Context) error {
		svc := application.NewHubAuthorizationService(adapters.NewPostgresHubRepository(w.app))
		var err error
		tc, err = svc.ResolveHubAccess(c, application.HubAccessRequest{ActorID: actor, HubID: hub, TenantID: w.tenant[tenantKey], QueueID: queue, CorrelationID: "it"})
		return err
	})
	return tc, err
}

func TestHubAuthorizationAgreesWithRLS(t *testing.T) {
	type tc struct {
		name   string
		setup  func(w *world, u uuid.UUID) // mutate state after a valid grant on A
		tenant string
		conv   string // which conversation RLS is asked about
		queue  string // "", "q1", "q2" -> the queue passed to the service
	}
	setScope := func(w *world, j string) {
		w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = $2::jsonb WHERE id = $1`, w.contract["A"], j)
	}
	cases := []tc{
		{"valid grant", func(w *world, u uuid.UUID) {}, "A", "A", "q1"},
		{"tenant without grant", func(w *world, u uuid.UUID) {}, "B", "B", "q1"},
		{"expired grant", func(w *world, u uuid.UUID) {
			w.exec(`UPDATE effective_access_grants SET valid_from = now() - interval '2 hours', valid_until = now() - interval '1 hour' WHERE user_id = $1`, u)
		}, "A", "A", "q1"},
		{"revoked grant", func(w *world, u uuid.UUID) {
			w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE user_id = $1`, u)
		}, "A", "A", "q1"},
		{"future grant", func(w *world, u uuid.UUID) {
			w.exec(`UPDATE effective_access_grants SET valid_from = now() + interval '1 hour' WHERE user_id = $1`, u)
		}, "A", "A", "q1"},
		{"contract revoked", func(w *world, u uuid.UUID) {
			w.exec(`UPDATE hub_tenant_service_contracts SET status = 'revoked' WHERE id = $1`, w.contract["A"])
		}, "A", "A", "q1"},
		{"contract expired", func(w *world, u uuid.UUID) {
			w.exec(`UPDATE hub_tenant_service_contracts SET valid_from = now() - interval '2 days', valid_until = now() - interval '1 day' WHERE id = $1`, w.contract["A"])
		}, "A", "A", "q1"},
		{"hub suspended", func(w *world, u uuid.UUID) {
			w.exec(`UPDATE service_hubs SET status = 'suspended' WHERE id = $1`, w.hub)
		}, "A", "A", "q1"},
		{"left the hub", func(w *world, u uuid.UUID) { w.exec(`DELETE FROM hub_memberships WHERE user_id = $1`, u) }, "A", "A", "q1"},
		{"scope allows q1: q1 conversation", func(w *world, u uuid.UUID) { setScope(w, fmt.Sprintf(`{"queue_ids":["%s"]}`, w.queue1["A"])) }, "A", "A", "q1"},
		{"scope allows q1: q2 conversation", func(w *world, u uuid.UUID) { setScope(w, fmt.Sprintf(`{"queue_ids":["%s"]}`, w.queue1["A"])) }, "A", "A2", "q2"},
		{"scope allows q1: queue-less conversation", func(w *world, u uuid.UUID) { setScope(w, fmt.Sprintf(`{"queue_ids":["%s"]}`, w.queue1["A"])) }, "A", "A0", ""},
		{"empty allowlist", func(w *world, u uuid.UUID) { setScope(w, `{"queue_ids":[]}`) }, "A", "A", "q1"},
		{"malformed scope", func(w *world, u uuid.UUID) { setScope(w, `{"queue_ids":"all"}`) }, "A", "A", "q1"},
		{"null scope value", func(w *world, u uuid.UUID) { setScope(w, `{"queue_ids":null}`) }, "A", "A", "q1"},
	}
	var allowed, denied int
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			u := w.hubAgent("agent")
			w.grant(u, "A")
			c.setup(w, u)

			var queue *uuid.UUID
			switch c.queue {
			case "q1":
				queue = &[]uuid.UUID{w.queue1["A"]}[0]
			case "q2":
				queue = &[]uuid.UUID{w.queue2["A"]}[0]
			}
			if c.tenant == "B" {
				queue = &[]uuid.UUID{w.queue1["B"]}[0]
			}
			ctxOut, svcErr := w.resolve(u, w.hub, c.tenant, queue)
			svcAllows := svcErr == nil
			if !svcAllows && !errors.Is(svcErr, application.ErrAccessDenied) {
				t.Fatalf("service failed with a non-denial error: %v", svcErr)
			}
			rlsAllows := w.n(u, `SELECT count(*) FROM conversations WHERE id = $1`, w.conv[c.conv]) == 1

			if svcAllows != rlsAllows {
				t.Fatalf("barriers disagree: service allows=%v (%s) but RLS allows=%v", svcAllows, application.DenialReason(svcErr), rlsAllows)
			}
			if svcAllows {
				allowed++
				if ctxOut.Source != tenancydomain.AccessSourceHub || ctxOut.HubID == nil || *ctxOut.HubID != w.hub ||
					ctxOut.EffectiveGrantID == nil || ctxOut.ServiceContractID == nil || ctxOut.ActorID != u || ctxOut.TenantID != w.tenant[c.tenant] {
					t.Fatalf("allowed, but the effective context is wrong: %+v", ctxOut)
				}
			} else {
				denied++
			}
		})
	}
	t.Run("the matrix is not vacuous", func(t *testing.T) {
		if allowed+denied == 0 {
			t.Skip("no case ran (integration database not configured)")
		}
		if allowed < 2 || denied < 10 {
			t.Fatalf("matrix too one-sided: %d allowed, %d denied", allowed, denied)
		}
	})
}

func TestHubAuthorization_ForgedIdentifiers(t *testing.T) {
	w := newWorld(t)
	alice := w.hubAgent("alice")
	w.grant(alice, "A")

	t.Run("a forged hub id is a plain denial", func(t *testing.T) {
		_, err := w.resolve(alice, uuid.New(), "A", nil)
		if !errors.Is(err, application.ErrAccessDenied) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a tenant without a grant is a plain denial, indistinguishable from a nonexistent tenant", func(t *testing.T) {
		_, errC := w.resolve(alice, w.hub, "C", nil)
		w.tenant["ghost"] = uuid.New()
		_, errGhost := w.resolve(alice, w.hub, "ghost", nil)
		if !errors.Is(errC, application.ErrAccessDenied) || !errors.Is(errGhost, application.ErrAccessDenied) {
			t.Fatalf("C: %v / ghost: %v", errC, errGhost)
		}
		if errC.Error() != errGhost.Error() {
			t.Fatal("the external error distinguishes a real ungranted tenant from a nonexistent one")
		}
	})
	t.Run("another agent's hub membership does not help a user outside the hub", func(t *testing.T) {
		stranger := w.user("stranger")
		if _, err := w.resolve(stranger, w.hub, "A", nil); !errors.Is(err, application.ErrAccessDenied) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("the service runs under RLS: it cannot see a hub or grant of someone else", func(t *testing.T) {
		bob := w.hubAgent("bob")
		w.grant(bob, "B")
		// bob asks for tenant A using alice's data; the repository (as bob) must not find alice's grant
		if _, err := w.resolve(bob, w.hub, "A", nil); !errors.Is(err, application.ErrAccessDenied) {
			t.Fatalf("got %v", err)
		}
	})
}
