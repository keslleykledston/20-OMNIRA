package adapters_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

type noPresence struct{}

func (noPresence) OnlineMembers(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (f *fx) sector(tenant uuid.UUID, name string, active bool) uuid.UUID {
	id := uuid.New()
	f.exec(`INSERT INTO sectors(id, tenant_id, name, active) VALUES($1,$2,$3,$4)`, id, tenant, name, active)
	return id
}

func (f *fx) sectorQueue(tenant, sector uuid.UUID, name string) uuid.UUID {
	id := f.queue(tenant, name, "manual", false)
	f.exec(`UPDATE queues SET sector_id=$1 WHERE id=$2 AND tenant_id=$3`, sector, id, tenant)
	return id
}

// The routing nodes read the SHARED services under the session tenant: another company's sector or queue is simply "missing".
func TestRoutingEffectsAreTenantScopedAndReadTheSharedCalendar(t *testing.T) {
	f := newFx(t)
	env := f.env
	rx := f.effects.WithRouting(nil, noPresence{})

	fin := f.sector(env.TenantA, "Financeiro", true)
	q := f.sectorQueue(env.TenantA, fin, "Cobrança")
	off := f.sector(env.TenantA, "Inativo", false)
	f.sectorQueue(env.TenantA, off, "Fila do inativo")
	empty := f.sector(env.TenantA, "Sem fila", true)
	foreign := f.sector(env.TenantB, "Estrangeiro", true)
	foreignQ := f.sectorQueue(env.TenantB, foreign, "Fila B")

	f.sys(env.TenantA, func(ctx context.Context) {
		for name, tc := range map[string]struct {
			kind string
			id   uuid.UUID
			want string
		}{
			"open sector (no calendar anywhere = always open, as before ADR-0044)": {"sector", fin, "open"},
			"open queue":      {"queue", q, "open"},
			"inactive sector": {"sector", off, "unavailable"},
			"sector no queue": {"sector", empty, "unavailable"},
			"foreign sector":  {"sector", foreign, "unavailable"},
			"foreign queue":   {"queue", foreignQ, "unavailable"},
			"unknown":         {"sector", uuid.New(), "unavailable"},
		} {
			if got, err := rx.Schedule(ctx, tc.kind, tc.id); err != nil || got != tc.want {
				t.Fatalf("%s: schedule = %q, %v (want %q)", name, got, err, tc.want)
			}
		}
		if got, _ := rx.Availability(ctx, "sector", foreign); got != "missing" {
			t.Fatalf("a foreign sector is missing, never described: %q", got)
		}
		secs, err := rx.ActiveSectors(ctx)
		if err != nil || len(secs) != 1 || secs[0].ID != fin || secs[0].Name != "Financeiro" {
			t.Fatalf("only this company's active sectors with a queue are offered: %v %v", secs, err)
		}
	})

	// suspending the sector's calendar flips the answer (the node reads the same calendar the engine does)
	cal := uuid.New()
	f.exec(`INSERT INTO calendars(id, tenant_id, name, timezone, kind) VALUES($1,$2,'Parado','America/Manaus','24x7')`, cal, env.TenantA)
	f.exec(`INSERT INTO calendar_suspensions(tenant_id, calendar_id, starts_at, ends_at, reason) VALUES($1,$2, now()-interval '1 hour', now()+interval '1 hour', 'teste')`, env.TenantA, cal)
	f.exec(`UPDATE sectors SET calendar_id=$1 WHERE id=$2 AND tenant_id=$3`, cal, fin, env.TenantA)
	f.sys(env.TenantA, func(ctx context.Context) {
		if got, err := rx.Schedule(ctx, "sector", fin); err != nil || got != "closed" {
			t.Fatalf("a suspended calendar is closed: %q %v", got, err)
		}
		if got, _ := rx.Availability(ctx, "sector", fin); got != "closed" {
			t.Fatalf("availability of a closed sector is closed: %q", got)
		}
	})
}

func TestAssignSectorPicksAQueueOfThatSectorOnly(t *testing.T) {
	f := newFx(t)
	env := f.env
	rx := f.effects.WithRouting(nil, noPresence{})
	conv, _ := env.SeedConversation(t, env.TenantA, "bot")
	sec := f.sector(env.TenantA, "Comercial", true)
	q := f.sectorQueue(env.TenantA, sec, "Vendas")
	foreign := f.sector(env.TenantB, "B", true)
	f.sectorQueue(env.TenantB, foreign, "Fila B")
	inactive := f.sector(env.TenantA, "Parado", false)
	f.sectorQueue(env.TenantA, inactive, "Fila parada")

	f.sys(env.TenantA, func(ctx context.Context) {
		for name, id := range map[string]uuid.UUID{"foreign sector": foreign, "inactive sector": inactive, "unknown": uuid.New()} {
			if got, err := rx.AssignSector(ctx, conv, id); err != nil || got != "unavailable" {
				t.Fatalf("%s must be unavailable: %q %v", name, got, err)
			}
		}
		if got, err := rx.AssignSector(ctx, conv, sec); err != nil || got != "assigned" {
			t.Fatalf("assign: %q %v", got, err)
		}
	})
	var queue *uuid.UUID
	if err := env.Seed.QueryRow(context.Background(), `SELECT queue_id FROM conversations WHERE id=$1`, conv).Scan(&queue); err != nil || queue == nil || *queue != q {
		t.Fatalf("the conversation must be in the sector's queue: %v %v", queue, err)
	}
}

func TestApplyOverflowNeedsTheEngine(t *testing.T) {
	f := newFx(t)
	conv, _ := f.env.SeedConversation(t, f.env.TenantA, "bot")
	f.sys(f.env.TenantA, func(ctx context.Context) {
		if _, err := f.effects.WithRouting(nil, noPresence{}).ApplyOverflow(ctx, conv); err == nil {
			t.Fatal("without the routing engine the node must report an error (its error port), not pretend it routed")
		}
	})
}

// Codex 6a: a queue of an INACTIVE sector is unavailable for the flow nodes, however they reach it.
func TestQueueOfAnInactiveSectorIsUnavailableToFlows(t *testing.T) {
	f := newFx(t)
	env := f.env
	rx := f.effects.WithRouting(nil, noPresence{})
	sec := f.sector(env.TenantA, "Vai fechar", true)
	q := f.sectorQueue(env.TenantA, sec, "Fila do setor")
	loose := f.queue(env.TenantA, "Sem setor", "manual", false)
	f.sys(env.TenantA, func(ctx context.Context) {
		if got, _ := rx.Schedule(ctx, "queue", q); got != "open" {
			t.Fatalf("active sector: %q", got)
		}
		if got, _ := rx.Schedule(ctx, "queue", loose); got != "open" {
			t.Fatalf("a queue without a sector keeps working as before: %q", got)
		}
	})
	f.exec(`UPDATE sectors SET active=false WHERE id=$1`, sec)
	f.sys(env.TenantA, func(ctx context.Context) {
		if got, _ := rx.Schedule(ctx, "queue", q); got != "unavailable" {
			t.Fatalf("schedule of a queue in an inactive sector: %q", got)
		}
		if got, _ := rx.Availability(ctx, "queue", q); got != "missing" {
			t.Fatalf("availability of a queue in an inactive sector: %q", got)
		}
		if got, _ := rx.Schedule(ctx, "queue", loose); got != "open" {
			t.Fatalf("unrelated queue unaffected: %q", got)
		}
	})
}

// Codex 6a: while the overflow chain is working a conversation, a flow cannot move it (that would start a fresh episode and empty the loop guard).
func TestAFlowCannotMoveAConversationTheChainIsWorking(t *testing.T) {
	f := newFx(t)
	env := f.env
	rx := f.effects.WithRouting(nil, noPresence{})
	conv, _ := env.SeedConversation(t, env.TenantA, "bot")
	sec := f.sector(env.TenantA, "Destino", true)
	q := f.sectorQueue(env.TenantA, sec, "Fila destino")
	origin := f.queue(env.TenantA, "Origem", "manual", false)
	f.exec(`UPDATE conversations SET queue_id=$2 WHERE id=$1`, conv, origin)
	f.exec(`INSERT INTO routing_engine_state (tenant_id, conversation_id, state, queue_id, origin_queue_id, hops, visited) VALUES ($1,$2,'queued',$3,$3,2,ARRAY['queue:a','queue:b'])`, env.TenantA, conv, origin)
	f.sys(env.TenantA, func(ctx context.Context) {
		if got, err := rx.AssignSector(ctx, conv, sec); err != nil || got != "unavailable" {
			t.Fatalf("a conversation the chain moved (hops=2) must not be moved by a flow: %q %v", got, err)
		}
	})
	var queue uuid.UUID
	if err := env.Seed.QueryRow(context.Background(), `SELECT queue_id FROM conversations WHERE id=$1`, conv).Scan(&queue); err != nil || queue != origin {
		t.Fatalf("the refused move changed nothing: %v %v", queue, err)
	}
	// once it is finished (assigned) or the chain has not moved it, the flow may route
	f.exec(`UPDATE routing_engine_state SET hops=0 WHERE conversation_id=$1`, conv)
	f.sys(env.TenantA, func(ctx context.Context) {
		if got, err := rx.AssignSector(ctx, conv, sec); err != nil || got != "assigned" {
			t.Fatalf("hops=0: %q %v", got, err)
		}
	})
	_ = q
}
