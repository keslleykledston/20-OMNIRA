package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/flowstest"
	"github.com/omnira/omnira/internal/flows/ports"
)

func newDraft(t *testing.T, tenant uuid.UUID, slug string) *domain.Flow {
	t.Helper()
	f, err := domain.NewFlow(tenant, slug, "Flow "+slug, domain.FlowTypeInbound, nil)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFlowTenantIsolation(t *testing.T) {
	env := flowstest.New(t)
	repo := NewPostgresFlowRepository(env.App)

	var flowA *domain.Flow
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		flowA = newDraft(t, env.TenantA, "reception")
		if err := repo.CreateFlow(ctx, flowA); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.Publish(ctx, flowA.ID, 1, "first", &env.UserA); err != nil {
			t.Fatal(err)
		}
	})
	var flowB *domain.Flow
	env.AsUser(t, env.TenantB, env.UserB, func(ctx context.Context) {
		// Same slug in another tenant is fine: slugs are per tenant.
		flowB = newDraft(t, env.TenantB, "reception")
		if err := repo.CreateFlow(ctx, flowB); err != nil {
			t.Fatalf("slug is per tenant: %v", err)
		}
	})

	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		if _, err := repo.GetFlow(ctx, flowB.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("tenant A read tenant B flow: %v", err)
		}
		list, err := repo.ListFlows(ctx, ports.ListFilter{})
		if err != nil || len(list) != 1 || list[0].ID != flowA.ID {
			t.Fatalf("tenant A list leaked: %d %v", len(list), err)
		}
		if _, err := repo.SaveDraft(ctx, flowB.ID, 1, "hijack", "", domain.EmptyDefinition); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("tenant A wrote tenant B draft: %v", err)
		}
		if _, err := repo.Publish(ctx, flowB.ID, 1, "", nil); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("tenant A published tenant B flow: %v", err)
		}
		if err := repo.Archive(ctx, flowB.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("tenant A archived tenant B flow: %v", err)
		}
		// A session whose TenantContext disagrees with the row tenant cannot create it either.
		forged := newDraft(t, env.TenantB, "forged")
		if err := repo.CreateFlow(ctx, forged); err == nil {
			t.Fatal("creating a flow for another tenant must fail")
		}
	})

	// Defense in depth: even bypassing the explicit filter, RLS hides the row (no TenantContext filter in this query).
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		var n int
		if err := repo.q(ctx).QueryRow(ctx, `SELECT count(*) FROM flows WHERE id=$1`, flowB.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("RLS did not hide the other tenant's flow: n=%d err=%v", n, err)
		}
	})
}

func TestSaveDraftIsOptimistic(t *testing.T) {
	env := flowstest.New(t)
	repo := NewPostgresFlowRepository(env.App)
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		f := newDraft(t, env.TenantA, "opt")
		if err := repo.CreateFlow(ctx, f); err != nil {
			t.Fatal(err)
		}
		def := json.RawMessage(`{"schema_version":1,"nodes":[],"edges":[],"variables":[],"settings":{},"metadata":{"v":1}}`)
		saved, err := repo.SaveDraft(ctx, f.ID, 1, "Renamed", "d", def)
		if err != nil || saved.DraftRevision != 2 || saved.Name != "Renamed" {
			t.Fatalf("first save: %+v %v", saved, err)
		}
		if _, err := repo.SaveDraft(ctx, f.ID, 1, "Stale", "", def); !errors.Is(err, domain.ErrRevisionConflict) {
			t.Fatalf("a stale revision must conflict: %v", err)
		}
		if _, err := repo.SaveDraft(ctx, uuid.New(), 1, "x", "", def); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown flow: %v", err)
		}
	})
}

func TestPublishSnapshotsAreImmutableAndRollbackMovesOnlyThePointer(t *testing.T) {
	env := flowstest.New(t)
	repo := NewPostgresFlowRepository(env.App)
	defV1 := json.RawMessage(`{"schema_version":1,"nodes":[],"edges":[],"variables":[],"settings":{},"metadata":{"rev":"one"}}`)
	defV2 := json.RawMessage(`{"schema_version":1,"nodes":[],"edges":[],"variables":[],"settings":{},"metadata":{"rev":"two"}}`)
	var flow *domain.Flow
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		flow = newDraft(t, env.TenantA, "versions")
		if err := repo.CreateFlow(ctx, flow); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.SaveDraft(ctx, flow.ID, 1, flow.Name, "", defV1); err != nil {
			t.Fatal(err)
		}
		v1, err := repo.Publish(ctx, flow.ID, 2, "v1", &env.UserA)
		if err != nil || v1.Version != 1 || v1.DefinitionHash == "" {
			t.Fatalf("publish v1: %+v %v", v1, err)
		}
		// Publishing the same stale revision again is refused: it would otherwise publish unvalidated content.
		if _, err := repo.Publish(ctx, flow.ID, 1, "", nil); !errors.Is(err, domain.ErrRevisionConflict) {
			t.Fatalf("stale publish: %v", err)
		}
		if _, err := repo.SaveDraft(ctx, flow.ID, 2, flow.Name, "", defV2); err != nil {
			t.Fatal(err)
		}
		v2, err := repo.Publish(ctx, flow.ID, 3, "v2", &env.UserA)
		if err != nil || v2.Version != 2 {
			t.Fatalf("publish v2: %+v %v", v2, err)
		}
		got, _ := repo.GetFlow(ctx, flow.ID)
		if got.Status != domain.FlowStatusPublished || got.ActiveVersionID == nil || *got.ActiveVersionID != v2.ID {
			t.Fatalf("v2 must be active: %+v", got)
		}
		// Editing the draft afterwards never changes a published snapshot.
		if _, err := repo.SaveDraft(ctx, flow.ID, 3, flow.Name, "", domain.EmptyDefinition); err != nil {
			t.Fatal(err)
		}
		again, _ := repo.GetVersion(ctx, v1.ID)
		if !strings.Contains(string(again.Definition), `"one"`) || again.DefinitionHash != v1.DefinitionHash {
			t.Fatalf("v1 snapshot changed: %s", again.Definition)
		}
		// Rollback = move the pointer; both versions still exist unchanged.
		rolled, ver, err := repo.ActivateVersion(ctx, flow.ID, 1)
		if err != nil || ver.ID != v1.ID || rolled.ActiveVersionID == nil || *rolled.ActiveVersionID != v1.ID {
			t.Fatalf("rollback: %+v %v", rolled, err)
		}
		list, _ := repo.ListVersions(ctx, flow.ID, 10, 0)
		if len(list) != 2 || list[0].Version != 2 {
			t.Fatalf("history must stay intact: %d", len(list))
		}
		if _, _, err := repo.ActivateVersion(ctx, flow.ID, 99); !errors.Is(err, domain.ErrNoSuchVersion) {
			t.Fatalf("unknown version: %v", err)
		}
	})
	// The database refuses to change a snapshot even for the table owner (trigger), and omnira_app has no grant.
	if _, err := env.Seed.Exec(context.Background(), `UPDATE flow_versions SET note='tamper' WHERE flow_id=$1`, flow.ID); err == nil {
		t.Fatal("owner could update an immutable version")
	}
	if err := platformdbSession(context.Background(), env, func(sc context.Context) error {
		_, err := repo.q(sc).Exec(sc, `UPDATE flow_versions SET note='x' WHERE flow_id=$1`, flow.ID)
		return err
	}); err == nil {
		t.Fatal("omnira_app could update a version")
	}
}

func TestConcurrentPublishesNeverCollide(t *testing.T) {
	env := flowstest.New(t)
	repo := NewPostgresFlowRepository(env.App)
	var flow *domain.Flow
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		flow = newDraft(t, env.TenantA, "race")
		if err := repo.CreateFlow(ctx, flow); err != nil {
			t.Fatal(err)
		}
	})
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Every publisher read the same revision of the same draft.
			ctx, cancel := context.WithTimeout(context.Background(), 20e9)
			defer cancel()
			err := platformdbSession(ctx, env, func(sc context.Context) error {
				_, err := repo.Publish(withTenant(sc, env.TenantA, env.UserA), flow.ID, 1, "race", &env.UserA)
				return err
			})
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("every concurrent publish of the same unchanged draft must succeed idempotently: %v", err)
		}
	}
	// Serialized by the row lock; the first creates v1, the rest see an identical draft and reuse it.
	var versions, distinct int
	if err := env.Seed.QueryRow(context.Background(), `SELECT count(*), count(DISTINCT version) FROM flow_versions WHERE flow_id=$1`, flow.ID).Scan(&versions, &distinct); err != nil {
		t.Fatal(err)
	}
	if versions != 1 || distinct != 1 {
		t.Fatalf("a double publish created duplicate versions: versions=%d distinct=%d", versions, distinct)
	}
}

func TestSlugAndDefaultUniqueness(t *testing.T) {
	env := flowstest.New(t)
	repo := NewPostgresFlowRepository(env.App)
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		if err := repo.CreateFlow(ctx, newDraft(t, env.TenantA, "dup")); err != nil {
			t.Fatal(err)
		}
	})
	// A violation aborts the transaction, so each failing statement runs in its own session.
	var second *domain.Flow
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		second = newDraft(t, env.TenantA, "other")
		if err := repo.CreateFlow(ctx, second); err != nil {
			t.Fatal(err)
		}
	})
	if err := platformdbSession(context.Background(), env, func(sc context.Context) error {
		return repo.CreateFlow(withTenant(sc, env.TenantA, env.UserA), newDraft(t, env.TenantA, "dup"))
	}); !errors.Is(err, domain.ErrSlugTaken) {
		t.Fatalf("duplicate slug in a tenant: %v", err)
	}
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		if _, err := repo.UpdateSettings(ctx, second.ID, ports.Settings{Priority: 10, IsDefault: true, RestartPolicy: domain.RestartAlways}); err != nil {
			t.Fatal(err)
		}
	})
	if err := platformdbSession(context.Background(), env, func(sc context.Context) error {
		f := newDraft(t, env.TenantA, "third")
		f.IsDefault = true
		return repo.CreateFlow(withTenant(sc, env.TenantA, env.UserA), f)
	}); err == nil || !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a second default flow of the same type must be refused: %v", err)
	}
}

func TestArchiveIsFinalAndIdempotent(t *testing.T) {
	env := flowstest.New(t)
	repo := NewPostgresFlowRepository(env.App)
	env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
		f := newDraft(t, env.TenantA, "arch")
		if err := repo.CreateFlow(ctx, f); err != nil {
			t.Fatal(err)
		}
		if err := repo.Archive(ctx, f.ID); err != nil {
			t.Fatal(err)
		}
		if err := repo.Archive(ctx, f.ID); err != nil {
			t.Fatalf("archiving twice must be a no-op: %v", err)
		}
		if _, err := repo.SaveDraft(ctx, f.ID, 1, "x", "", domain.EmptyDefinition); !errors.Is(err, domain.ErrArchived) {
			t.Fatalf("archived flows are read-only: %v", err)
		}
		if _, err := repo.Publish(ctx, f.ID, 1, "", nil); !errors.Is(err, domain.ErrArchived) {
			t.Fatalf("archived flows cannot be published: %v", err)
		}
	})
}
