package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	groupsapp "github.com/omnira/omnira/internal/groups/application"
)

// msg inserts a wa_group_messages row directly (the ingestion path itself is covered by TestIntake*; these
// tests exercise the observer's own claim/save contract against rows that already exist).
func (e *env) msg(tenant, group uuid.UUID, providerID, body string, fromMe bool, sent time.Time) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO wa_group_messages(id,tenant_id,group_id,provider_message_id,author_name,from_me,message_type,body,sent_at)
		VALUES($1,$2,$3,$4,'Cliente',$5,'text',$6,$7)`, id, tenant, group, providerID, fromMe, body, sent)
	return id
}

// Claim only ever finds messages of groups that are BOTH reading and opted into the observer — never a
// from_me message, never an empty body, never another tenant's group, no matter how it opted in.
func TestObserverClaimOnlyFindsOptedInTextFromParticipants(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, connB := e.tenant()
	optedIn := e.group(tenantA, connA, jid1, "Homologação", true)
	e.exec(`UPDATE wa_groups SET ai_observer_enabled=true WHERE id=$1`, optedIn)
	notOptedIn := e.group(tenantA, connA, jid2, "Sem IA", true)
	// Not reading at all, even though (hypothetically) flagged on directly in the database.
	notReading := e.group(tenantA, connA, jid3, "Desligado", false)
	e.exec(`UPDATE wa_groups SET ai_observer_enabled=true WHERE id=$1`, notReading)
	// Tenant B opted in too, but must never be visible from a Tenant A-only check — Claim spans tenants by
	// design (it is a system-session worker query), so this test instead confirms B's own candidate surfaces
	// alongside A's with ITS OWN tenant id attached, never bleeding into A's.
	optedInB := e.group(tenantB, connB, jid1, "Homologação B", true)
	e.exec(`UPDATE wa_groups SET ai_observer_enabled=true WHERE id=$1`, optedInB)

	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	want := e.msg(tenantA, optedIn, "m1", "meu pedido não chegou", false, base)
	e.msg(tenantA, optedIn, "m2", "mensagem própria", true, base.Add(time.Minute))      // from_me: excluded
	e.msg(tenantA, optedIn, "m3", "   ", false, base.Add(2*time.Minute))                // empty after trim: excluded
	e.msg(tenantA, notOptedIn, "m4", "grupo sem IA", false, base.Add(3*time.Minute))    // group not opted in: excluded
	e.msg(tenantA, notReading, "m5", "grupo nem lendo", false, base.Add(4*time.Minute)) // group not reading: excluded
	wantB := e.msg(tenantB, optedInB, "m6", "pedido do cliente de B", false, base)

	repo := NewPostgresObserverRepository(e.app)
	got, err := repo.Claim(context.Background(), 20)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	byID := map[uuid.UUID]groupsapp.Candidate{}
	for _, c := range got {
		byID[c.ID] = c
	}
	if _, ok := byID[want]; !ok {
		t.Fatalf("tenant A's eligible message was not claimed: %+v", got)
	}
	if c := byID[want]; c.TenantID != tenantA || c.Body != "meu pedido não chegou" {
		t.Fatalf("tenant A candidate = %+v", c)
	}
	if _, ok := byID[wantB]; !ok {
		t.Fatalf("tenant B's eligible message was not claimed: %+v", got)
	}
	if c := byID[wantB]; c.TenantID != tenantB {
		t.Fatalf("tenant B candidate carries the wrong tenant: %+v", c)
	}
	if len(got) != 2 {
		t.Fatalf("claim must only find the two eligible messages, got %d: %+v", len(got), got)
	}
}

// Claim never finds a message twice once it has a suggestion — Save is the only thing that can produce one.
func TestObserverClaimSkipsAlreadySuggestedMessages(t *testing.T) {
	e := newEnv(t)
	tenant, conn := e.tenant()
	g := e.group(tenant, conn, jid1, "Homologação", true)
	e.exec(`UPDATE wa_groups SET ai_observer_enabled=true WHERE id=$1`, g)
	mid := e.msg(tenant, g, "m1", "dúvida sobre o horário", false, time.Now().UTC())

	repo := NewPostgresObserverRepository(e.app)
	got, err := repo.Claim(context.Background(), 5)
	if err != nil || len(got) != 1 {
		t.Fatalf("first claim: %v %+v", err, got)
	}
	saved, err := repo.Save(context.Background(), got[0], groupsapp.Suggestion{Category: "question", Summary: "Pergunta sobre horário", Confidence: 0.6}, "fake", "mini", 42)
	if err != nil || !saved {
		t.Fatalf("save: saved=%v err=%v", saved, err)
	}
	got, err = repo.Claim(context.Background(), 5)
	if err != nil || len(got) != 0 {
		t.Fatalf("second claim must find nothing left, got %v %+v", err, got)
	}
	var category, summary string
	var confidence float64
	if err := e.seed.QueryRow(e.ctx, `SELECT category, summary, confidence FROM wa_group_message_suggestions WHERE tenant_id=$1 AND group_message_id=$2`, tenant, mid).
		Scan(&category, &summary, &confidence); err != nil || category != "question" || summary != "Pergunta sobre horário" || confidence != 0.6 {
		t.Fatalf("stored suggestion = %q %q %v (%v)", category, summary, confidence, err)
	}
}

// Save is idempotent: a race between two workers classifying the same message keeps only the first result,
// never errors and never a duplicate row.
func TestObserverSaveIsIdempotent(t *testing.T) {
	e := newEnv(t)
	tenant, conn := e.tenant()
	g := e.group(tenant, conn, jid1, "Homologação", true)
	e.exec(`UPDATE wa_groups SET ai_observer_enabled=true WHERE id=$1`, g)
	mid := e.msg(tenant, g, "m1", "reclamação do atraso", false, time.Now().UTC())
	cand := groupsapp.Candidate{ID: mid, TenantID: tenant, Body: "reclamação do atraso"}

	repo := NewPostgresObserverRepository(e.app)
	first, err := repo.Save(context.Background(), cand, groupsapp.Suggestion{Category: "complaint", Summary: "Atraso", Confidence: 0.9}, "fake", "a", 10)
	if err != nil || !first {
		t.Fatalf("first save: saved=%v err=%v", first, err)
	}
	second, err := repo.Save(context.Background(), cand, groupsapp.Suggestion{Category: "other", Summary: "outro resultado", Confidence: 0.1}, "fake", "b", 20)
	if err != nil || second {
		t.Fatalf("second save must report nothing saved: saved=%v err=%v", second, err)
	}
	if n := e.count(`SELECT count(*) FROM wa_group_message_suggestions WHERE tenant_id=$1 AND group_message_id=$2`, tenant, mid); n != 1 {
		t.Fatalf("exactly one suggestion must exist, got %d", n)
	}
	var category string
	if err := e.seed.QueryRow(e.ctx, `SELECT category FROM wa_group_message_suggestions WHERE tenant_id=$1 AND group_message_id=$2`, tenant, mid).Scan(&category); err != nil || category != "complaint" {
		t.Fatalf("the FIRST result must be the one kept, got %q (%v)", category, err)
	}
}

// A suggestion can never be attached to another tenant's message: the foreign key is on (tenant_id,
// group_message_id) together, so a mismatched pair is rejected outright, never silently cross-linked.
func TestObserverSaveCannotCrossTenants(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	tenantB, _ := e.tenant()
	gA := e.group(tenantA, connA, jid1, "A", true)
	e.exec(`UPDATE wa_groups SET ai_observer_enabled=true WHERE id=$1`, gA)
	midA := e.msg(tenantA, gA, "m1", "mensagem de A", false, time.Now().UTC())

	repo := NewPostgresObserverRepository(e.app)
	// Claim to tenant B's id, same message id: must fail (no such (tenant_id, group_message_id) pair exists).
	cross := groupsapp.Candidate{ID: midA, TenantID: tenantB, Body: "mensagem de A"}
	if _, err := repo.Save(context.Background(), cross, groupsapp.Suggestion{Category: "other", Summary: "x", Confidence: 0.1}, "fake", "m", 1); err == nil {
		t.Fatal("saving under the wrong tenant must fail, not succeed")
	}
	if n := e.count(`SELECT count(*) FROM wa_group_message_suggestions WHERE group_message_id=$1`, midA); n != 0 {
		t.Fatalf("the failed cross-tenant save must have left nothing behind, got %d", n)
	}
}
