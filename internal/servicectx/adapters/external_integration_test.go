package adapters_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/gatewaytest"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

// integration seeds the company's ERP connection (the integration a record belongs to).
func (s *stack) integration(tenant uuid.UUID) uuid.UUID {
	s.t.Helper()
	id := uuid.New()
	if _, err := s.env.Seed.Exec(context.Background(), `INSERT INTO channel_connections(id, tenant_id, channel, provider, provider_kind, external_number_id) VALUES($1,$2,'erp','fake_erp','official',$3)`,
		id, tenant, "erp-"+id.String()); err != nil {
		s.t.Fatal(err)
	}
	return id
}

func (s *stack) erpOrder(id, status string, class domain.StatusClass, at time.Time) domain.Snapshot {
	snap := domain.Snapshot{ExternalType: domain.ExternalTypeServiceOrder, ExternalID: id, DisplayRef: "OS " + id, ExternalStatus: status, StatusClass: class, UpdatedAt: &at}
	s.gw.Put(snap)
	return snap
}

func (s *stack) link(user uuid.UUID, in domain.LinkInput) (res application.LinkResult, err error) {
	err = s.try(user, s.env.TenantA, func(ctx context.Context) (e error) {
		res, e = s.svc.LinkExternalRecord(ctx, in)
		return
	})
	return res, err
}

func (s *stack) request(user uuid.UUID, in domain.RequestInput) (res application.RequestResult, err error) {
	err = s.try(user, s.env.TenantA, func(ctx context.Context) (e error) {
		res, e = s.svc.RequestServiceOrder(ctx, in)
		return
	})
	return res, err
}

func orderInput(integ, session uuid.UUID, key string) domain.RequestInput {
	return domain.RequestInput{IntegrationID: integ, SessionID: session, Title: "Visita técnica", Description: "Cliente sem sinal", IdempotencyKey: key}
}

// ---- linking an order the ERP already has ---------------------------------------------------------------------------------------------------

func TestLinkingAnExistingOrderKeepsOneRecordAndTellsTheTimeline(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	cs := s.mustCase(s.agent, "Instalação", &sess.ID)
	task := s.mustTask(s.agent, "Acompanhar OS", &sess.ID, &cs.ID)
	s.erpOrder("5001", "Aberta", domain.StatusOpen, time.Now().UTC().Add(-time.Hour))

	in := domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "5001", SessionID: &sess.ID}
	first, err := s.link(s.agent, in)
	if err != nil || !first.LinkCreated || !first.RecordCreated || first.Record.ExternalSystem != "fake_erp" || first.Record.DisplayRef != "OS 5001" || first.Record.SyncState != domain.SyncSynced {
		t.Fatalf("link: %+v %v", first, err)
	}
	again, err := s.link(s.agent, in)
	if err != nil || again.LinkCreated || again.Link.ID != first.Link.ID {
		t.Fatalf("linking twice changes nothing: %+v %v", again, err)
	}
	// the SAME ERP record is shared by the case and the task: still one record, three links
	for _, target := range []domain.LinkInput{
		{IntegrationID: integ, ExternalType: "service_order", ExternalID: "5001", CaseID: &cs.ID},
		{IntegrationID: integ, ExternalType: "service_order", ExternalID: "5001", TaskID: &task.ID},
	} {
		r, err := s.link(s.agent, target)
		if err != nil || !r.LinkCreated || r.RecordCreated || r.Record.ID != first.Record.ID {
			t.Fatalf("shared record: %+v %v", r, err)
		}
	}
	if n := s.count(`SELECT count(*) FROM external_records WHERE tenant_id=$1`, s.env.TenantA); n != 1 {
		t.Fatalf("no second operational copy of the order: %d records", n)
	}
	if n := s.count(`SELECT count(*) FROM service_external_links WHERE external_record_id=$1 AND unlinked_at IS NULL`, first.Record.ID); n != 3 {
		t.Fatalf("three active links: %d", n)
	}
	items, _ := s.timeline(s.agent, sess.ID, true, 50)
	if !strings.Contains(fmt.Sprint(types(items)), "external_record.linked") {
		t.Fatalf("the session timeline shows the link: %v", types(items))
	}
	if n := s.count(`SELECT count(*) FROM tickets WHERE tenant_id=$1`, s.env.TenantA); n != 0 {
		t.Fatalf("linking an ERP order creates nothing in the local ticket table: %d", n)
	}

	// validation
	if _, err := s.link(s.agent, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "5001"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a target is required: %v", err)
	}
	if _, err := s.link(s.agent, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "5001", SessionID: &sess.ID, CaseID: &cs.ID}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("exactly one target: %v", err)
	}
	if _, err := s.link(s.agent, domain.LinkInput{IntegrationID: uuid.New(), ExternalType: "service_order", ExternalID: "5001", SessionID: &sess.ID}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("an integration that is not the company's: %v", err)
	}
	if _, err := s.link(s.agent, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "9999", SessionID: &sess.ID}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("the ERP has no such order: %v", err)
	}
	s.gw.Down = true
	if _, err := s.link(s.agent, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "5001", SessionID: &sess.ID}); !errors.Is(err, domain.ErrExternalUnavailable) {
		t.Fatalf("an ERP that does not answer is not a missing record: %v", err)
	}
	s.gw.Down = false
	if _, err := s.link(s.observer, in); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("no service.link_external: %v", err)
	}
	// unlinking keeps the history
	var un *domain.ExternalLink
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) { un, e = s.svc.UnlinkExternalRecord(ctx, first.Link.ID); return }); err != nil || un.UnlinkedAt == nil {
		t.Fatalf("unlink: %+v %v", un, err)
	}
	if n := s.count(`SELECT count(*) FROM service_external_links WHERE id=$1`, first.Link.ID); n != 1 {
		t.Fatal("an unlinked link stays as history")
	}
	relinked, err := s.link(s.agent, in)
	if err != nil || !relinked.LinkCreated || relinked.Link.ID == first.Link.ID {
		t.Fatalf("a link can be made again after it ended: %+v %v", relinked, err)
	}
}

func TestWithoutAnAdapterTheERPIsNotConfiguredNeverFaked(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	bare, _, _ := adapters_BuildBare(s)
	err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		_, e := bare.LinkExternalRecord(ctx, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "1", SessionID: &sess.ID})
		return e
	})
	if !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("link without an adapter: %v", err)
	}
	err = s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		_, e := bare.RequestServiceOrder(ctx, orderInput(integ, sess.ID, "k-"+uuid.NewString()))
		return e
	})
	if !errors.Is(err, domain.ErrNotConfigured) {
		t.Fatalf("a request without an adapter: %v", err)
	}
	if n := s.count(`SELECT count(*) FROM external_record_requests WHERE tenant_id=$1`, s.env.TenantA); n != 0 {
		t.Fatalf("not configured is answered BEFORE any durable attempt exists: %d", n)
	}
}

// ---- refreshing and the ERP's own updates -------------------------------------------------------------------------------------------------

func TestRefreshUpdatesTheProjectionAndNeverGoesBackInTime(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	cs := s.mustCase(s.agent, "Caso", &sess.ID)
	t0 := time.Now().UTC().Add(-2 * time.Hour)
	s.erpOrder("6001", "Aberta", domain.StatusOpen, t0)
	rec, err := s.link(s.agent, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "6001", SessionID: &sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.link(s.agent, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "6001", CaseID: &cs.ID}); err != nil {
		t.Fatal(err)
	}
	refresh := func() (x *domain.ExternalRecord) {
		if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) { x, e = s.svc.RefreshExternalRecord(ctx, rec.Record.ID); return }); err != nil {
			t.Fatal(err)
		}
		return
	}
	s.erpOrder("6001", "Concluída", domain.StatusDone, t0.Add(time.Hour))
	done := refresh()
	if done.StatusClass != domain.StatusDone || done.ExternalStatus != "Concluída" || done.Version <= rec.Record.Version {
		t.Fatalf("refreshed: %+v", done)
	}
	// the change is told on the session AND on the case, where people look
	for name, evs := range map[string][]ports.TimelineItem{"session": func() []ports.TimelineItem { x, _ := s.timeline(s.agent, sess.ID, true, 50); return x }(), "case": func() (x []ports.TimelineItem) {
		s.as(s.agent, s.env.TenantA, func(ctx context.Context) { x, _, _ = s.svc.CaseTimeline(ctx, cs.ID, "", 50, true) })
		return
	}()} {
		found := false
		for _, e := range evs {
			if e.EventType == "external_record.status_changed" && e.Payload["to"] == "done" && e.Payload["from"] == "open" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s timeline does not show the order being concluded: %v", name, types(evs))
		}
	}
	// the ERP answers with an OLDER snapshot (a stale replica): the projection does not go back
	s.erpOrder("6001", "Aberta", domain.StatusOpen, t0)
	stale := refresh()
	if stale.StatusClass != domain.StatusDone || stale.Version != done.Version {
		t.Fatalf("an older snapshot never rolls the projection back: %+v", stale)
	}
	// and the database says the same to anybody who tries it with plain SQL
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `UPDATE external_records SET source_updated_at = source_updated_at - interval '1 day' WHERE id=$1`, rec.Record.ID)
	}); err == nil {
		t.Fatal("the database refuses to move the provider's time backwards")
	}
	// an ERP that does not answer leaves the record as it was, marked stale
	s.gw.Down = true
	down := refresh()
	if down.SyncState != domain.SyncStale || down.StatusClass != domain.StatusDone {
		t.Fatalf("stale, not wrong: %+v", down)
	}
	s.gw.Down = false
	// an ERP that no longer knows it: conflict, nothing deleted
	s.gw.Remove("service_order", "6001")
	gone := refresh()
	if gone.SyncState != domain.SyncConflict {
		t.Fatalf("conflict: %+v", gone)
	}
	if n := s.count(`SELECT count(*) FROM external_records WHERE id=$1`, rec.Record.ID); n != 1 {
		t.Fatal("nothing is deleted")
	}
}

func TestInboundUpdatesAreIdempotentOrderedAndNeverCreateRecords(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	t0 := time.Now().UTC().Add(-3 * time.Hour)
	s.erpOrder("7001", "Aberta", domain.StatusOpen, t0)
	rec, err := s.link(s.agent, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "7001", SessionID: &sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	upd := func(delivery, status string, class domain.StatusClass, at time.Time) (res application.UpdateResult, err error) {
		s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) {
			res, err = s.svc.ApplyExternalUpdate(ctx, application.ExternalUpdate{IntegrationID: integ, DeliveryID: delivery,
				Snapshot: domain.Snapshot{ExternalType: "service_order", ExternalID: "7001", ExternalStatus: status, StatusClass: class, UpdatedAt: &at}})
		})
		return
	}
	// only the integration layer (a system session) applies a push
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		_, e := s.svc.ApplyExternalUpdate(ctx, application.ExternalUpdate{IntegrationID: integ, DeliveryID: "delivery-0001", Snapshot: domain.Snapshot{ExternalType: "service_order", ExternalID: "7001"}})
		return e
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a person cannot play the ERP: %v", err)
	}
	r1, err := upd("delivery-0001", "Em andamento", domain.StatusInProgress, t0.Add(time.Hour))
	if err != nil || !r1.Applied {
		t.Fatalf("first delivery: %+v %v", r1, err)
	}
	dup, err := upd("delivery-0001", "Em andamento", domain.StatusInProgress, t0.Add(time.Hour))
	if err != nil || !dup.Duplicate || dup.Applied {
		t.Fatalf("the same delivery twice: %+v %v", dup, err)
	}
	if _, err := upd("delivery-0001", "Concluída", domain.StatusDone, t0.Add(2*time.Hour)); !errors.Is(err, domain.ErrIdempotencyMismatch) {
		t.Fatalf("a delivery id reused for other content: %v", err)
	}
	late, err := upd("delivery-0002", "Aberta", domain.StatusOpen, t0.Add(10*time.Minute))
	if err != nil || late.Applied || !late.OutOfOrder {
		t.Fatalf("an older delivery arriving late: %+v %v", late, err)
	}
	done, err := upd("delivery-0003", "Concluída", domain.StatusDone, t0.Add(2*time.Hour))
	if err != nil || !done.Applied {
		t.Fatalf("newer delivery: %+v %v", done, err)
	}
	var cur *domain.ExternalRecord
	s.as(s.agent, s.env.TenantA, func(ctx context.Context) { cur, _ = s.svc.GetExternalRecord(ctx, rec.Record.ID) })
	if cur.StatusClass != domain.StatusDone {
		t.Fatalf("the order ends concluded, whatever the arrival order: %+v", cur)
	}
	// a push about a record nobody linked creates nothing
	var unk application.UpdateResult
	s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) {
		unk, err = s.svc.ApplyExternalUpdate(ctx, application.ExternalUpdate{IntegrationID: integ, DeliveryID: "delivery-0009",
			Snapshot: domain.Snapshot{ExternalType: "service_order", ExternalID: "8888", StatusClass: domain.StatusOpen}})
	})
	if err != nil || !unk.Unknown || s.count(`SELECT count(*) FROM external_records WHERE external_id='8888' AND tenant_id=$1`, s.env.TenantA) != 0 {
		t.Fatalf("a push never creates a record: %+v %v", unk, err)
	}
	// the facts are attributed to the integration
	var kinds string
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT string_agg(DISTINCT actor_type, ',') FROM context_events WHERE event_type='external_record.status_changed' AND session_id=$1`, sess.ID).Scan(&kinds); err != nil || kinds != "integration" {
		t.Fatalf("who told us: %q %v", kinds, err)
	}
	// another company's delivery id space is its own
	otherInteg := s.integration(s.env.TenantB)
	var otherRes application.UpdateResult
	s.env.AsSystem(t, s.env.TenantB, func(ctx context.Context) {
		otherRes, err = s.svc.ApplyExternalUpdate(ctx, application.ExternalUpdate{IntegrationID: otherInteg, DeliveryID: "delivery-0001",
			Snapshot: domain.Snapshot{ExternalType: "service_order", ExternalID: "7001", StatusClass: domain.StatusDone}})
	})
	if err != nil || !otherRes.Unknown {
		t.Fatalf("company B has no such record: %+v %v", otherRes, err)
	}
}

// ---- asking the ERP to create an order ---------------------------------------------------------------------------------------------------------

func TestRequestingAServiceOrderCreatesTheReferenceAndLinksIt(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	cs := s.mustCase(s.admin, "Sem sinal", &sess.ID)
	key := "ord-" + uuid.NewString()
	in := orderInput(integ, sess.ID, key)
	in.CaseID = &cs.ID
	res, err := s.request(s.admin, in)
	if err != nil || res.Outcome != application.OutcomeCreated || res.Record == nil || res.Request.State != domain.RequestConfirmedSuccess || res.Request.ExternalRecordID == nil {
		t.Fatalf("request: %+v %v", res, err)
	}
	if s.gw.CreateCalls != 1 || s.gw.Count("service_order") != 1 {
		t.Fatalf("the ERP made exactly one order: calls=%d orders=%d", s.gw.CreateCalls, s.gw.Count("service_order"))
	}
	if res.Record.ExternalType != "service_order" || res.Record.StatusClass != domain.StatusOpen {
		t.Fatalf("record: %+v", res.Record)
	}
	if n := s.count(`SELECT count(*) FROM service_external_links WHERE external_record_id=$1 AND unlinked_at IS NULL`, res.Record.ID); n != 2 {
		t.Fatalf("linked to the session and to the case: %d", n)
	}
	// retry with the same key: the earlier answer, no second call
	again, err := s.request(s.admin, in)
	if err != nil || again.Outcome != application.OutcomeReplay || again.Record == nil || again.Record.ID != res.Record.ID || s.gw.CreateCalls != 1 {
		t.Fatalf("replay: %+v %v calls=%d", again, err, s.gw.CreateCalls)
	}
	other := in
	other.Title = "Outro pedido"
	if _, err := s.request(s.admin, other); !errors.Is(err, domain.ErrIdempotencyMismatch) {
		t.Fatalf("same key, other request: %v", err)
	}
	// a confirmed request does not block: a new key asks again
	second, err := s.request(s.admin, orderInput(integ, sess.ID, "ord2-"+uuid.NewString()))
	if err != nil || second.Outcome != application.OutcomeCreated || s.gw.CreateCalls != 2 {
		t.Fatalf("a second order for the session after the first was confirmed: %+v %v", second, err)
	}
	// the history says it all, with ids and enums only
	items, _ := s.timeline(s.agent, sess.ID, true, 100)
	got := fmt.Sprint(types(items))
	for _, want := range []string{"external_request.created", "external_request.state_changed", "external_record.linked"} {
		if !strings.Contains(got, want) {
			t.Errorf("timeline lacks %s: %s", want, got)
		}
	}
	// finalizing: the result says an ERP order came out of the attendance
	s.assign(conv, s.admin)
	if err := s.finalize(s.attendance(true), s.admin, conv, attendancedomain.ReasonResolved); err != nil {
		t.Fatal(err)
	}
	if rows := s.sessionsOf(conv); len(rows) != 1 || rows[0].canonical != domain.ResExternalServiceOrder {
		t.Fatalf("result after an order: %+v", rows)
	}
}

// THE dangerous case: the ERP processed the request and the answer was lost.
func TestATimeoutIsNotAFailureItBlocksUntilReconciled(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	s.gw.Mode = gatewaytest.LoseAnswer
	key := "lost-" + uuid.NewString()
	res, err := s.request(s.admin, orderInput(integ, sess.ID, key))
	if err != nil || res.Outcome != application.OutcomeUnknown || res.Request.State != domain.RequestOutcomeUnknown || res.Record != nil {
		t.Fatalf("a lost answer: %+v %v", res, err)
	}
	if s.gw.Count("service_order") != 1 {
		t.Fatal("(the ERP did make the order: that is the point)")
	}
	// the same key: the earlier state, NO second call
	same, err := s.request(s.admin, orderInput(integ, sess.ID, key))
	if err != nil || same.Outcome != application.OutcomeUnknown || s.gw.CreateCalls != 1 {
		t.Fatalf("same key: %+v %v calls=%d", same, err, s.gw.CreateCalls)
	}
	// another key (an impatient operator clicks again): BLOCKED, no second call — it would duplicate the order
	blocked, err := s.request(s.admin, orderInput(integ, sess.ID, "again-"+uuid.NewString()))
	if err != nil || blocked.Outcome != application.OutcomeBlocked || blocked.Request.ID != res.Request.ID || s.gw.CreateCalls != 1 {
		t.Fatalf("blocked: %+v %v calls=%d", blocked, err, s.gw.CreateCalls)
	}
	if s.gw.Count("service_order") != 1 {
		t.Fatal("exactly one order in the ERP")
	}
	// reconciling finds it by the correlation key: confirmed, linked, nothing duplicated
	var rr application.ReconcileResult
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) (e error) {
		rr, e = s.svc.ReconcileExternalRequest(ctx, res.Request.ID)
		return
	}); err != nil {
		t.Fatal(err)
	}
	if rr.Request.State != domain.RequestConfirmedSuccess || rr.Record == nil || *rr.Request.ResolutionSource != "reconcile" {
		t.Fatalf("reconciled: %+v", rr)
	}
	if n := s.count(`SELECT count(*) FROM service_external_links WHERE external_record_id=$1 AND session_id=$2 AND unlinked_at IS NULL`, rr.Record.ID, sess.ID); n != 1 {
		t.Fatalf("linked after reconciling: %d", n)
	}
	if s.gw.CreateCalls != 1 || s.gw.Count("service_order") != 1 {
		t.Fatal("reconciling never writes to the ERP")
	}
	// and the session is free again
	s.gw.Mode = gatewaytest.Succeed
	next, err := s.request(s.admin, orderInput(integ, sess.ID, "next-"+uuid.NewString()))
	if err != nil || next.Outcome != application.OutcomeCreated {
		t.Fatalf("after reconciling the session can ask again: %+v %v", next, err)
	}
}

func TestReconcilingAnUnknownThatTheERPDoesNotHaveWaitsForTheAge(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	s.gw.Mode = gatewaytest.DropRequest // it never arrived
	res, err := s.request(s.admin, orderInput(integ, sess.ID, "drop-"+uuid.NewString()))
	if err != nil || res.Outcome != application.OutcomeUnknown {
		t.Fatal(res, err)
	}
	rec := func() (rr application.ReconcileResult) {
		if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) (e error) {
			rr, e = s.svc.ReconcileExternalRequest(ctx, res.Request.ID)
			return
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	if rr := rec(); !rr.TooEarly || rr.Request.State != domain.RequestOutcomeUnknown {
		t.Fatalf("\"the ERP does not know it yet\" proves nothing on a request this young: %+v", rr)
	}
	s.ageRequest(res.Request.ID, time.Hour)
	rr := rec()
	if rr.Request.State != domain.RequestConfirmedFailure || *rr.Request.FailureClass != "not_found_by_correlation" {
		t.Fatalf("old enough and the ERP has no trace: %+v", rr)
	}
	s.gw.Mode = gatewaytest.Succeed
	if next, err := s.request(s.admin, orderInput(integ, sess.ID, "retry-"+uuid.NewString())); err != nil || next.Outcome != application.OutcomeCreated {
		t.Fatalf("freed: %+v %v", next, err)
	}
	// an ERP that does not answer the lookup teaches nothing
	conv2, _ := s.conversation()
	sess2 := s.mustOpen(s.admin, conv2)
	s.gw.Mode = gatewaytest.DropRequest
	r2, _ := s.request(s.admin, orderInput(integ, sess2.ID, "drop2-"+uuid.NewString()))
	s.ageRequest(r2.Request.ID, time.Hour)
	s.gw.Down = true
	var rr2 application.ReconcileResult
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) (e error) {
		rr2, e = s.svc.ReconcileExternalRequest(ctx, r2.Request.ID)
		return
	}); err != nil {
		t.Fatal(err)
	}
	if rr2.Request.State != domain.RequestOutcomeUnknown {
		t.Fatalf("no answer, no conclusion: %+v", rr2)
	}
}

func TestAnERPThatCannotLookUpByCorrelationIsReconciledByHand(t *testing.T) {
	s := newStack(t)
	s.gw.Lookup = false // like K3G: no way to find a record again
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	s.gw.Mode = gatewaytest.LoseAnswer
	res, err := s.request(s.admin, orderInput(integ, sess.ID, "k3g-"+uuid.NewString()))
	if err != nil || res.Outcome != application.OutcomeUnknown {
		t.Fatal(res, err)
	}
	var rr application.ReconcileResult
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) (e error) {
		rr, e = s.svc.ReconcileExternalRequest(ctx, res.Request.ID)
		return
	}); err != nil || !rr.NeedsManual {
		t.Fatalf("no lookup capability: %+v %v", rr, err)
	}
	resolve := func(in domain.ResolveInput) (out application.ReconcileResult, err error) {
		err = s.try(s.admin, s.env.TenantA, func(ctx context.Context) (e error) {
			out, e = s.svc.ResolveExternalRequest(ctx, res.Request.ID, in)
			return
		})
		return
	}
	if _, err := resolve(domain.ResolveInput{Created: true}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("created needs the ERP id: %v", err)
	}
	if _, err := resolve(domain.ResolveInput{Created: true, ExternalID: "424242"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("the ERP must confirm the id the person typed: %v", err)
	}
	if _, err := resolve(domain.ResolveInput{Created: false}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("saying nothing was created needs a reason: %v", err)
	}
	made := s.erpOrder("1001", "Aberta", domain.StatusOpen, time.Now().UTC()) // the order the lost answer made, found by the person in the ERP
	out, err := resolve(domain.ResolveInput{Created: true, ExternalID: made.ExternalID})
	if err != nil || out.Request.State != domain.RequestConfirmedSuccess || *out.Request.ResolutionSource != "manual" || out.Record == nil {
		t.Fatalf("settled by hand: %+v %v", out, err)
	}
	if _, err := resolve(domain.ResolveInput{Created: false, Reason: "tarde demais"}); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("a settled request is final: %v", err)
	}
	// the other way: nothing was created
	conv2, _ := s.conversation()
	sess2 := s.mustOpen(s.admin, conv2)
	s.gw.Mode = gatewaytest.DropRequest
	r2, _ := s.request(s.admin, orderInput(integ, sess2.ID, "k3g2-"+uuid.NewString()))
	var none application.ReconcileResult
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) (e error) {
		none, e = s.svc.ResolveExternalRequest(ctx, r2.Request.ID, domain.ResolveInput{Created: false, Reason: "conferi no ERP, não existe"})
		return
	}); err != nil || none.Request.State != domain.RequestConfirmedFailure || *none.Request.FailureClass != "not_created" {
		t.Fatalf("nothing created: %+v %v", none, err)
	}
	var note string
	if err := s.env.Seed.QueryRow(context.Background(), `SELECT note FROM context_events WHERE entity_id=$1 AND event_type='external_request.state_changed' AND note <> ''`, r2.Request.ID).Scan(&note); err != nil || !strings.Contains(note, "conferi") {
		t.Fatalf("the reason is in the history: %q %v", note, err)
	}
}

func TestADefinitiveRejectionFreesTheSessionAndAPanicIsAnUnknown(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	s.gw.Mode = gatewaytest.Reject
	rej, err := s.request(s.admin, orderInput(integ, sess.ID, "rej-"+uuid.NewString()))
	if err != nil || rej.Outcome != application.OutcomeRejected || rej.Request.State != domain.RequestConfirmedFailure || *rej.Request.FailureClass != "invalid_customer" {
		t.Fatalf("rejected: %+v %v", rej, err)
	}
	s.gw.Mode = gatewaytest.Succeed
	if ok, err := s.request(s.admin, orderInput(integ, sess.ID, "rej2-"+uuid.NewString())); err != nil || ok.Outcome != application.OutcomeCreated {
		t.Fatalf("a definitive refusal frees the session at once: %+v %v", ok, err)
	}
	conv2, _ := s.conversation()
	sess2 := s.mustOpen(s.admin, conv2)
	s.gw.Mode = gatewaytest.Panic
	pan, err := s.request(s.admin, orderInput(integ, sess2.ID, "pan-"+uuid.NewString()))
	if err != nil || pan.Outcome != application.OutcomeUnknown {
		t.Fatalf("a crash inside the adapter after the request left is an unknown outcome, never a failure: %+v %v", pan, err)
	}
}

// The attempt is durable BEFORE the ERP is called: a crash during the call leaves a request in flight that somebody can reconcile.
func TestTheAttemptIsDurableBeforeTheERPIsCalled(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	s.gw.Mode = gatewaytest.Hang
	var wg sync.WaitGroup
	wg.Add(1)
	var res application.RequestResult
	var rerr error
	go func() {
		defer wg.Done()
		res, rerr = s.request(s.admin, orderInput(integ, sess.ID, "dur-"+uuid.NewString()))
	}()
	select {
	case <-s.gw.Called:
	case <-time.After(10 * time.Second):
		t.Fatal("the ERP was never called")
	}
	// the call is in progress and the REQUEST's own transaction has not committed: the attempt is already visible to everybody else
	if n := s.count(`SELECT count(*) FROM external_record_requests WHERE session_id=$1 AND state='in_flight'`, sess.ID); n != 1 {
		t.Fatalf("the in-flight attempt is committed before the ERP answers: %d", n)
	}
	// and while it is in flight, asking again does not call the ERP again
	blocked, err := s.request(s.admin, orderInput(integ, sess.ID, "dur2-"+uuid.NewString()))
	if err != nil || blocked.Outcome != application.OutcomeBlocked {
		t.Fatalf("in flight blocks: %+v %v", blocked, err)
	}
	s.gw.Release()
	wg.Wait()
	if rerr != nil || res.Outcome != application.OutcomeCreated || s.gw.CreateCalls != 1 {
		t.Fatalf("the first call completes: %+v %v calls=%d", res, rerr, s.gw.CreateCalls)
	}
}

// ---- security ------------------------------------------------------------------------------------------------------------------------------------

func TestOnlyAdministratorsAndSupervisorsAskTheERPForAnything(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	if _, err := s.request(s.agent, orderInput(integ, sess.ID, "agent-"+uuid.NewString())); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an attendant has no service.request_external: %v", err)
	}
	if s.gw.CreateCalls != 0 {
		t.Fatal("the ERP was not called")
	}
	if n := s.count(`SELECT count(*) FROM external_record_requests WHERE tenant_id=$1`, s.env.TenantA); n != 0 {
		t.Fatalf("no durable attempt for a refused person: %d", n)
	}
	// the database says the same to plain SQL
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `INSERT INTO external_record_requests(tenant_id, integration_id, session_id, operation, title, idempotency_key, request_hash, correlation_key)
			VALUES($1,$2,$3,'create_service_order','x','forged-key-1','h','forged-correlation-1')`, s.env.TenantA, integ, sess.ID)
	}); err == nil {
		t.Fatal("RLS refuses an attempt written by somebody without service.request_external")
	}
	// an attendant still LINKS an order the ERP already has
	s.erpOrder("1", "Aberta", domain.StatusOpen, time.Now().UTC())
	if _, err := s.link(s.agent, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "1", SessionID: &sess.ID}); err != nil {
		t.Fatalf("service.link_external: %v", err)
	}
	// ... and cannot reconcile or settle a request
	req, err := s.request(s.admin, orderInput(integ, sess.ID, "adm-"+uuid.NewString()))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error {
		_, e := s.svc.ReconcileExternalRequest(ctx, req.Request.ID)
		return e
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("reconcile: %v", err)
	}
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error {
		_, e := s.svc.ResolveExternalRequest(ctx, req.Request.ID, domain.ResolveInput{Created: false, Reason: "tentando"})
		return e
	}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("resolve: %v", err)
	}
}

func TestExternalRecordsAndRequestsNeverCrossCompanies(t *testing.T) {
	s := newStack(t)
	integA := s.integration(s.env.TenantA)
	integB := s.integration(s.env.TenantB)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	s.erpOrder("3001", "Aberta", domain.StatusOpen, time.Now().UTC())
	rec, err := s.link(s.agent, domain.LinkInput{IntegrationID: integA, ExternalType: "service_order", ExternalID: "3001", SessionID: &sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.request(s.admin, orderInput(integA, sess.ID, "iso-"+uuid.NewString()))
	if err != nil {
		t.Fatal(err)
	}
	inB := func(label string, fn func(ctx context.Context) error) {
		t.Helper()
		if err := s.try(s.env.UserB, s.env.TenantB, fn); !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s from B: %v", label, err)
		}
	}
	inB("get record", func(ctx context.Context) error { _, e := s.svc.GetExternalRecord(ctx, rec.Record.ID); return e })
	inB("refresh record", func(ctx context.Context) error { _, e := s.svc.RefreshExternalRecord(ctx, rec.Record.ID); return e })
	inB("unlink", func(ctx context.Context) error { _, e := s.svc.UnlinkExternalRecord(ctx, rec.Link.ID); return e })
	inB("get request", func(ctx context.Context) error { _, e := s.svc.GetExternalRequest(ctx, req.Request.ID); return e })
	inB("reconcile", func(ctx context.Context) error { _, e := s.svc.ReconcileExternalRequest(ctx, req.Request.ID); return e })
	inB("use A's integration", func(ctx context.Context) error {
		_, e := s.svc.LinkExternalRecord(ctx, domain.LinkInput{IntegrationID: integA, ExternalType: "service_order", ExternalID: "3001", SessionID: &sess.ID})
		return e
	})
	inB("request on A's session", func(ctx context.Context) error {
		_, e := s.svc.RequestServiceOrder(ctx, orderInput(integB, sess.ID, "iso2-"+uuid.NewString()))
		return e
	})
	s.env.AsSystem(t, s.env.TenantB, func(ctx context.Context) {
		if _, err := s.svc.GetExternalRecord(ctx, rec.Record.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("a system session of B reads A's record: %v", err)
		}
		if _, err := s.svc.GetExternalRequest(ctx, req.Request.ID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("a system session of B reads A's request: %v", err)
		}
	})
	// the same ERP id under company B's integration is another record, not A's
	s.exec(`INSERT INTO external_records(tenant_id, integration_id, external_system, external_type, external_id) VALUES($1,$2,'fake_erp','service_order','3001')`, s.env.TenantB, integB)
	if n := s.count(`SELECT count(*) FROM external_records WHERE external_id='3001' AND tenant_id IN ($1,$2)`, s.env.TenantA, s.env.TenantB); n != 2 {
		t.Fatalf("an external id is only unique within a company's integration: %d", n)
	}
	// composite keys: a link under B cannot point at A's record
	if _, err := s.env.Seed.Exec(context.Background(), `INSERT INTO service_external_links(tenant_id, external_record_id, session_id) VALUES($1,$2,$3)`, s.env.TenantB, rec.Record.ID, sess.ID); err == nil {
		t.Error("a link in company B cannot join A's record and A's session")
	}
	var list []domain.ExternalRecord
	if err := s.try(s.env.UserB, s.env.TenantB, func(ctx context.Context) (e error) {
		list, _, e = s.svc.ListExternalRecords(ctx, ports.RecordFilter{})
		return
	}); err != nil {
		t.Fatal(err)
	}
	for _, x := range list {
		if x.TenantID == s.env.TenantA {
			t.Errorf("B lists A's record: %+v", x)
		}
	}
}

func TestDatabaseGuardsOfTheExternalSide(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	s.erpOrder("4001", "Aberta", domain.StatusOpen, time.Now().UTC())
	rec, err := s.link(s.admin, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "4001", SessionID: &sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.request(s.admin, orderInput(integ, sess.ID, "guard-"+uuid.NewString()))
	if err != nil {
		t.Fatal(err)
	}
	ex := func(sql string, args ...any) error {
		_, e := s.env.Seed.Exec(context.Background(), sql, args...)
		return e
	}
	for name, stmt := range map[string]string{
		"record identity":              fmt.Sprintf(`UPDATE external_records SET external_id='X' WHERE id='%s'`, rec.Record.ID),
		"record integration":           fmt.Sprintf(`UPDATE external_records SET integration_id='%s' WHERE id='%s'`, uuid.New(), rec.Record.ID),
		"link target is identity":      fmt.Sprintf(`UPDATE service_external_links SET session_id=NULL, case_id=NULL WHERE id='%s'`, rec.Link.ID),
		"unlinking twice":              fmt.Sprintf(`UPDATE service_external_links SET unlinked_at=now() WHERE id='%s'`, rec.Link.ID),
		"request identity":             fmt.Sprintf(`UPDATE external_record_requests SET correlation_key='other-correlation-1' WHERE id='%s'`, req.Request.ID),
		"a confirmed request is final": fmt.Sprintf(`UPDATE external_record_requests SET state='outcome_unknown', external_record_id=NULL, resolved_at=NULL WHERE id='%s'`, req.Request.ID),
	} {
		if name == "unlinking twice" {
			if err := ex(fmt.Sprintf(`UPDATE service_external_links SET unlinked_at=now(), unlinked_by_user_id=NULL WHERE id='%s'`, rec.Link.ID)); err != nil {
				t.Fatalf("the first unlinking: %v", err)
			}
		}
		if err := ex(stmt); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if err := ex(`INSERT INTO service_external_links(tenant_id, external_record_id) VALUES($1,$2)`, s.env.TenantA, rec.Record.ID); err == nil {
		t.Error("a link needs exactly one target")
	}
	// the application role cannot delete any of it
	for _, tbl := range []string{"external_records", "service_external_links", "external_record_requests"} {
		err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, `DELETE FROM `+tbl) })
		if err == nil {
			t.Errorf("the application role must not delete from %s", tbl)
		}
	}
	// the blocking index is the last line of defence against two requests for one session in flight
	conv2, _ := s.conversation()
	sess2 := s.mustOpen(s.admin, conv2)
	if err := ex(`INSERT INTO external_record_requests(tenant_id, integration_id, session_id, operation, title, idempotency_key, request_hash, correlation_key) VALUES($1,$2,$3,'create_service_order','a','blk-key-0001','h','blk-corr-0001')`, s.env.TenantA, integ, sess2.ID); err != nil {
		t.Fatal(err)
	}
	if err := ex(`INSERT INTO external_record_requests(tenant_id, integration_id, session_id, operation, title, idempotency_key, request_hash, correlation_key) VALUES($1,$2,$3,'create_service_order','b','blk-key-0002','h','blk-corr-0002')`, s.env.TenantA, integ, sess2.ID); err == nil {
		t.Error("two requests in flight for one session must be refused by the index")
	}
}

func TestContactContextShowsTheExternalRecordsUnderTheCustomerRule(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	_, contact := s.conversation()
	c1, c2 := uuid.New(), uuid.New()
	for i, c := range []uuid.UUID{c1, c2} {
		s.exec(`INSERT INTO customer_accounts(id, tenant_id, name) VALUES($1,$2,$3)`, c, s.env.TenantA, fmt.Sprintf("Empresa %d", i+1))
		s.exec(`INSERT INTO contact_account_links(tenant_id, contact_id, account_id, source) VALUES($1,$2,$3,'manual')`, s.env.TenantA, contact, c)
	}
	mk := func(c *uuid.UUID, ext string) *domain.ExternalRecord {
		res, err := s.open(s.agent, domain.OpenInput{ContactID: &contact, CustomerID: c, OriginChannel: "phone"})
		if err != nil {
			t.Fatal(err)
		}
		s.erpOrder(ext, "Aberta", domain.StatusOpen, time.Now().UTC())
		r, err := s.link(s.agent, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: ext, SessionID: &res.Session.ID})
		if err != nil {
			t.Fatal(err)
		}
		return r.Record
	}
	r1, r2, r0 := mk(&c1, "A1"), mk(&c2, "B1"), mk(nil, "N1")
	get := func(c *uuid.UUID) (cc *application.ContactContext) {
		if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) { cc, e = s.svc.ContactContext(ctx, contact, c); return }); err != nil {
			t.Fatal(err)
		}
		return
	}
	ids := func(cc *application.ContactContext) map[uuid.UUID]bool {
		m := map[uuid.UUID]bool{}
		for _, x := range cc.ExternalRecords {
			m[x.ID] = true
		}
		return m
	}
	if m := ids(get(nil)); len(m) != 1 || !m[r0.ID] {
		t.Fatalf("before the choice only the order tied to no customer: %v", m)
	}
	if m := ids(get(&c1)); !m[r1.ID] || !m[r0.ID] || m[r2.ID] {
		t.Fatalf("customer 1: %v", m)
	}
}

// ---- HTTP -------------------------------------------------------------------------------------------------------------------------------------------

func TestHTTPExternalRecordsAndOrders(t *testing.T) {
	s := newStack(t)
	mux := s.mux()
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	rs := s.call(mux, s.admin, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, conv))
	sessID := rs.body["id"].(string)
	s.erpOrder("9001", "Aberta", domain.StatusOpen, time.Now().UTC())
	body := fmt.Sprintf(`{"integration_id":"%s","external_type":"service_order","external_id":"9001","session_id":"%s"}`, integ, sessID)
	if r := s.call(mux, s.observer, "POST", "/external-records", body); r.code != http.StatusForbidden {
		t.Fatalf("no key: %d", r.code)
	}
	if r := s.call(mux, s.agent, "POST", "/external-records", fmt.Sprintf(`{"integration_id":"%s","external_type":"service_order","external_id":"9001"}`, integ)); r.code != http.StatusBadRequest {
		t.Fatalf("a target is required: %d", r.code)
	}
	r := s.call(mux, s.agent, "POST", "/external-records", body)
	if r.code != http.StatusCreated || r.body["link_created"] != true || r.header.Get("ETag") == "" {
		t.Fatalf("link: %d %v", r.code, r.body)
	}
	recID := r.body["record"].(map[string]any)["id"].(string)
	linkID := r.body["link"].(map[string]any)["id"].(string)
	if r := s.call(mux, s.agent, "POST", "/external-records", body); r.code != http.StatusOK || r.body["link_created"] != false {
		t.Fatalf("again: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "GET", "/external-records?session_id="+sessID, ""); r.code != http.StatusOK || len(r.body["items"].([]any)) != 1 {
		t.Fatalf("list: %d %v", r.code, r.body)
	}
	s.erpOrder("9001", "Concluída", domain.StatusDone, time.Now().UTC().Add(time.Minute))
	if r := s.call(mux, s.agent, "POST", "/external-records/"+recID+"/refresh", ""); r.code != http.StatusOK || r.body["status_class"] != "done" {
		t.Fatalf("refresh: %d %v", r.code, r.body)
	}
	s.gw.Down = true
	if r := s.call(mux, s.agent, "POST", "/external-records", fmt.Sprintf(`{"integration_id":"%s","external_type":"service_order","external_id":"9001","session_id":"%s"}`, integ, sessID)); r.code != http.StatusBadGateway || r.body["error"] != "external_unavailable" {
		t.Fatalf("linking always asks the ERP to confirm: with it down the answer is 502, never a guess: %d %v", r.code, r.body)
	}
	s.gw.Down = false

	// ordering
	order := fmt.Sprintf(`{"integration_id":"%s","title":"Visita","description":"sem sinal"}`, integ)
	if r := s.call(mux, s.admin, "POST", "/service-sessions/"+sessID+"/external-service-orders", order); r.code != http.StatusBadRequest {
		t.Fatalf("an Idempotency-Key is required to ask the ERP: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "POST", "/service-sessions/"+sessID+"/external-service-orders", order, "Idempotency-Key", "http-ord-"+uuid.NewString()); r.code != http.StatusForbidden {
		t.Fatalf("an attendant: %d", r.code)
	}
	key := "http-ord-" + uuid.NewString()
	r = s.call(mux, s.admin, "POST", "/service-sessions/"+sessID+"/external-service-orders", order, "Idempotency-Key", key)
	if r.code != http.StatusCreated || r.body["outcome"] != "created" || r.body["record"] == nil {
		t.Fatalf("order: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.admin, "POST", "/service-sessions/"+sessID+"/external-service-orders", order, "Idempotency-Key", key); r.code != http.StatusOK || r.header.Get("Idempotent-Replay") != "true" {
		t.Fatalf("replay: %d %v", r.code, r.header)
	}
	s.gw.Mode = gatewaytest.LoseAnswer
	lost := s.call(mux, s.admin, "POST", "/service-sessions/"+sessID+"/external-service-orders", order, "Idempotency-Key", "http-lost-"+uuid.NewString())
	if lost.code != http.StatusAccepted || lost.body["outcome"] != "outcome_unknown" {
		t.Fatalf("lost answer: %d %v", lost.code, lost.body)
	}
	reqID := lost.body["request"].(map[string]any)["id"].(string)
	if r := s.call(mux, s.admin, "POST", "/service-sessions/"+sessID+"/external-service-orders", order, "Idempotency-Key", "http-more-"+uuid.NewString()); r.code != http.StatusConflict || r.body["error"] != "request_blocked" {
		t.Fatalf("blocked: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.admin, "GET", "/external-requests?state=outcome_unknown", ""); r.code != http.StatusOK || len(r.body["items"].([]any)) != 1 {
		t.Fatalf("pending requests: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.admin, "POST", "/external-requests/"+reqID+"/reconcile", ""); r.code != http.StatusOK || r.body["request"].(map[string]any)["state"] != "confirmed_success" {
		t.Fatalf("reconcile: %d %v", r.code, r.body)
	}
	s.gw.Mode = gatewaytest.Reject
	if r := s.call(mux, s.admin, "POST", "/service-sessions/"+sessID+"/external-service-orders", order, "Idempotency-Key", "http-rej-"+uuid.NewString()); r.code != http.StatusUnprocessableEntity || r.body["error"] != "external_rejected" {
		t.Fatalf("rejected: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "POST", "/external-record-links/"+linkID+"/unlink", ""); r.code != http.StatusOK || r.body["unlinked_at"] == nil {
		t.Fatalf("unlink: %d %v", r.code, r.body)
	}

	// with the switch off the routes that ask the ERP do not exist
	off := newOffMux(s)
	if r := s.call(off, s.admin, "POST", "/service-sessions/"+sessID+"/external-service-orders", order, "Idempotency-Key", "http-off-"+uuid.NewString()); r.code != http.StatusNotFound && r.code != http.StatusMethodNotAllowed {
		t.Fatalf("switched off: %d", r.code)
	}
	if r := s.call(off, s.agent, "GET", "/external-records", ""); r.code != http.StatusOK {
		t.Fatalf("records stay readable with the orders switch off: %d", r.code)
	}
}
