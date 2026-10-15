package adapters_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/adapters"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/gatewaytest"
)

// memberWith adds a member of company A whose role holds exactly the keys.
func (s *stack) memberWith(keys ...string) uuid.UUID {
	s.t.Helper()
	role, user := uuid.New(), uuid.New()
	s.exec(`INSERT INTO roles(id, tenant_id, key, name) VALUES($1,$2,$3,'custom')`, role, s.env.TenantA, "custom-"+role.String()[:8])
	for _, k := range keys {
		s.exec(`INSERT INTO role_permissions(role_id, permission_key) VALUES($1,$2)`, role, k)
	}
	s.exec(`INSERT INTO users(id, external_subject, email, status) VALUES($1,$2,$3,'active')`, user, user.String(), user.String()+"@invalid")
	s.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, s.env.TenantA, user, role)
	s.t.Cleanup(func() { _, _ = s.env.Seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user) })
	return user
}

// ---- the two ERP keys are separate in the database ----------------------------------------------------------------------------------------------

func TestAskingTheERPAndKeepingItsAnswerAreSeparateKeysEnforcedByTheDatabase(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	requester := s.memberWith("service.read", "service.request_external") // may ask, may not tie the answer to our work
	linker := s.memberWith("service.read", "service.link_external")       // may tie, may not ask
	both := s.memberWith("service.read", "service.request_external", "service.link_external")

	if _, err := s.request(requester, orderInput(integ, sess.ID, "req-only-"+uuid.NewString())); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("asking needs both keys, because the answer has to be kept: %v", err)
	}
	if _, err := s.request(linker, orderInput(integ, sess.ID, "lnk-only-"+uuid.NewString())); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("keeping an answer is not asking: %v", err)
	}
	if s.gw.CreateCalls != 0 {
		t.Fatal("the ERP was not called for either")
	}
	// plain SQL: the requester cannot forge a record, an update of a record or a link; the linker cannot forge a request
	s.erpOrder("4242", "Aberta", domain.StatusOpen, time.Now().UTC())
	made, err := s.link(linker, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "4242", SessionID: &sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	for name, stmt := range map[string]string{
		"forge a record": `INSERT INTO external_records(tenant_id, integration_id, external_system, external_type, external_id) VALUES($1,$2,'fake_erp','service_order','FORGED')`,
		"forge a link":   `INSERT INTO service_external_links(tenant_id, external_record_id, session_id) SELECT $1, id, (SELECT id FROM service_sessions WHERE tenant_id=$1 LIMIT 1) FROM external_records WHERE tenant_id=$1 AND integration_id=$2 AND external_id <> 'x' LIMIT 1`,
	} {
		if err := s.try(requester, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, stmt, s.env.TenantA, integ) }); err == nil {
			t.Errorf("%s: a holder of service.request_external ALONE must be refused", name)
		}
	}
	// (an UPDATE the policy does not allow does not fail: it touches no row)
	for name, stmt := range map[string]string{
		"edit a record": `UPDATE external_records SET external_status='Concluída', status_class='done' WHERE tenant_id=$1 AND integration_id=$2`,
		"unlink":        `UPDATE service_external_links SET unlinked_at=now() WHERE tenant_id=$1 AND $2::uuid IS NOT NULL`,
	} {
		var rows int64
		if err := s.try(requester, s.env.TenantA, func(ctx context.Context) error {
			tag, e := platformQuerier(ctx).Exec(ctx, stmt, s.env.TenantA, integ)
			rows = tag.RowsAffected()
			return e
		}); err != nil || rows != 0 {
			t.Errorf("%s: a holder of service.request_external ALONE touches nothing (rows=%d err=%v)", name, rows, err)
		}
	}
	if n := s.count(`SELECT count(*) FROM external_records WHERE tenant_id=$1 AND status_class='done'`, s.env.TenantA); n != 0 {
		t.Errorf("no record was edited: %d", n)
	}
	if err := s.try(linker, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `INSERT INTO external_record_requests(tenant_id, integration_id, session_id, operation, title, idempotency_key, request_hash, correlation_key) VALUES($1,$2,$3,'create_service_order','t','lnk-forge-001','h','lnk-forge-corr-1')`, s.env.TenantA, integ, sess.ID)
	}); err == nil {
		t.Error("a holder of service.link_external ALONE cannot write a request to the ERP")
	}
	// somebody with both does the whole thing
	res, err := s.request(both, orderInput(integ, sess.ID, "both-"+uuid.NewString()))
	if err != nil || res.Outcome != application.OutcomeCreated {
		t.Fatalf("both keys: %+v %v", res, err)
	}
	_ = made
}

// ---- what the ERP answers is checked against what was asked ---------------------------------------------------------------------------------

func TestAnAnswerThatIsNotTheRecordAskedForIsNeverTrusted(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	s.erpOrder("6600", "Aberta", domain.StatusOpen, time.Now().UTC())
	rec, err := s.link(s.admin, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "6600", SessionID: &sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	s.gw.WrongAnswer = true // an adapter that answers with another object than the one asked for
	// linking, refreshing
	if _, err := s.link(s.admin, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "6601", SessionID: &sess.ID}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("link: %v", err)
	}
	var refreshed *domain.ExternalRecord
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) (e error) {
		refreshed, e = s.svc.RefreshExternalRecord(ctx, rec.Record.ID)
		return
	}); err != nil {
		t.Fatal(err)
	}
	if refreshed.SyncState != domain.SyncError || refreshed.StatusClass != domain.StatusOpen {
		t.Fatalf("a refresh answered with another record marks the sync as an error and changes nothing else: %+v", refreshed)
	}
	// creating: the ERP "answers" with a customer: that is NOT a created order, and not a refusal either
	s.gw.Mode = gatewaytest.Succeed
	res, err := s.request(s.admin, orderInput(integ, sess.ID, "wrong-"+uuid.NewString()))
	if err != nil || res.Outcome != application.OutcomeUnknown || res.Record != nil {
		t.Fatalf("an answer that is not a service order is an unknown outcome: %+v %v", res, err)
	}
	if n := s.count(`SELECT count(*) FROM external_records WHERE tenant_id=$1 AND external_type='customer'`, s.env.TenantA); n != 0 {
		t.Fatalf("no record of the wrong kind was made: %d", n)
	}
	// reconciling: the lookup answers with the wrong kind of record: nothing is learned
	var rr application.ReconcileResult
	s.ageRequest(res.Request.ID, time.Hour)
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) (e error) {
		rr, e = s.svc.ReconcileExternalRequest(ctx, res.Request.ID)
		return
	}); err != nil {
		t.Fatal(err)
	}
	if rr.Request.State != domain.RequestOutcomeUnknown {
		t.Fatalf("reconcile with a wrong answer: %+v", rr)
	}
	// settling by hand with an id the ERP answers for with something else
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		_, e := s.svc.ResolveExternalRequest(ctx, res.Request.ID, domain.ResolveInput{Created: true, ExternalID: "9999"})
		return e
	}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("manual resolve with a mismatching answer: %v", err)
	}
	// the adapter is always resolved by the company AND the integration
	s.gw.WrongAnswer = false
	found := false
	for _, pair := range s.gw.ResolvedFor {
		if pair[0] != s.env.TenantA {
			t.Errorf("the resolver was asked for another company: %v", pair)
		}
		if pair[1] == integ {
			found = true
		}
	}
	if !found {
		t.Fatal("the resolver was never asked for this integration")
	}
}

// ---- snapshots are ordered by the provider's time ------------------------------------------------------------------------------------------------

func TestSnapshotsWithoutATimeOrWithTheSameTimeNeverRollBackOrChurn(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	t0 := time.Now().UTC().Add(-time.Hour)
	s.erpOrder("5500", "Concluída", domain.StatusDone, t0)
	rec, err := s.link(s.admin, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "5500", SessionID: &sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	apply := func(delivery string, snap domain.Snapshot) application.UpdateResult {
		var res application.UpdateResult
		var err error
		s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) {
			res, err = s.svc.ApplyExternalUpdate(ctx, application.ExternalUpdate{IntegrationID: integ, DeliveryID: delivery, Snapshot: snap})
		})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	get := func() *domain.ExternalRecord {
		var x *domain.ExternalRecord
		s.as(s.admin, s.env.TenantA, func(ctx context.Context) { x, _ = s.svc.GetExternalRecord(ctx, rec.Record.ID) })
		return x
	}
	v0 := get().Version
	// no provider time at all: cannot be placed, never overwrites a record that has one
	if r := apply("delivery-notime-1", domain.Snapshot{ExternalType: "service_order", ExternalID: "5500", ExternalStatus: "Aberta", StatusClass: domain.StatusOpen}); r.Applied {
		t.Fatalf("a snapshot with no time was applied: %+v", r)
	}
	if x := get(); x.StatusClass != domain.StatusDone || x.Version != v0 {
		t.Fatalf("unchanged: %+v", x)
	}
	// the same time and the same content: nothing happens (no churn of the version)
	same := domain.Snapshot{ExternalType: "service_order", ExternalID: "5500", ExternalStatus: "Concluída", StatusClass: domain.StatusDone, UpdatedAt: &t0}
	if r := apply("delivery-same-001", same); r.Applied {
		t.Fatalf("the same snapshot again changed the record: %+v", r)
	}
	if x := get(); x.Version != v0 {
		t.Fatalf("no version churn: %d -> %d", v0, x.Version)
	}
	// the same time but a different content: the ERP corrected itself within the instant, and the correction wins
	fixed := same
	fixed.ExternalStatus, fixed.StatusClass = "Cancelada", domain.StatusCancelled
	if r := apply("delivery-fixed-01", fixed); !r.Applied {
		t.Fatalf("a correction at the same instant: %+v", r)
	}
	if x := get(); x.StatusClass != domain.StatusCancelled {
		t.Fatalf("corrected: %+v", x)
	}
	// a record that never had a provider time accepts the first snapshot, with or without one
	s.erpOrder("5501", "Aberta", domain.StatusOpen, t0)
	r2, err := s.link(s.admin, domain.LinkInput{IntegrationID: integ, ExternalType: "service_order", ExternalID: "5501", SessionID: &sess.ID})
	if err != nil {
		t.Fatal(err)
	}
	s.noTriggers(`UPDATE external_records SET source_updated_at = NULL WHERE id=$1`, r2.Record.ID)
	if r := apply("delivery-first-01", domain.Snapshot{ExternalType: "service_order", ExternalID: "5501", ExternalStatus: "Em andamento", StatusClass: domain.StatusInProgress}); !r.Applied {
		t.Fatalf("a record with no provider time yet takes the first snapshot: %+v", r)
	}
}

// ---- the reconciler never races the call it is reconciling ------------------------------------------------------------------------------------------

func TestAReconcilerWaitsForARequestThatMayStillBeAnswered(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	s.gw.Mode = gatewaytest.DropRequest
	res, err := s.request(s.admin, orderInput(integ, sess.ID, "race-"+uuid.NewString()))
	if err != nil || res.Outcome != application.OutcomeUnknown {
		t.Fatal(res, err)
	}
	// an in-flight request (its own call may answer at any moment) is left alone while that call can still be alive, even when it is already older than the
	// minimum age for an unknown outcome: 35 s is past the floor of 30 s (twice the 15 s limit) but inside the lease of 45 s
	lease, _, _ := adapters.Build(s.env.App, adapters.Options{ExternalOrders: true, Gateways: gatewaytest.Resolver{F: s.gw}})
	lease.WithExternalTimings(15*time.Second, 0)
	s.forceRequestState(res.Request.ID, "in_flight") // (a state the application never goes back to: crafted with the triggers off)
	s.ageRequest(res.Request.ID, 35*time.Second)
	var rr application.ReconcileResult
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) (e error) {
		rr, e = lease.ReconcileExternalRequest(ctx, res.Request.ID)
		return
	}); err != nil {
		t.Fatal(err)
	}
	if !rr.TooEarly || rr.Request.State != domain.RequestInFlight {
		t.Fatalf("a young in-flight request is left alone: %+v", rr)
	}
	s.ageRequest(res.Request.ID, 60*time.Second) // now its call is certainly over
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) (e error) {
		rr, e = lease.ReconcileExternalRequest(ctx, res.Request.ID)
		return
	}); err != nil {
		t.Fatal(err)
	}
	if rr.Request.State != domain.RequestConfirmedFailure {
		t.Fatalf("an abandoned in-flight request is settled by reconciliation: %+v", rr)
	}
	// a new one for the next part of the test
	conv2, _ := s.conversation()
	sess2 := s.mustOpen(s.admin, conv2)
	s.gw.Mode = gatewaytest.DropRequest
	res, err = s.request(s.admin, orderInput(integ, sess2.ID, "race2-"+uuid.NewString()))
	if err != nil || res.Outcome != application.OutcomeUnknown {
		t.Fatal(res, err)
	}
	// the minimum age is never shorter than twice the call's time limit
	tight, _, _ := adapters.Build(s.env.App, adapters.Options{ExternalOrders: true, Gateways: gatewaytest.Resolver{F: s.gw}})
	tight.WithExternalTimings(10*time.Second, time.Second) // asked for 1 s, the call may last 10 s: the floor is 20 s
	s.forceRequestState(res.Request.ID, "outcome_unknown")
	s.ageRequest(res.Request.ID, 15*time.Second)
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) (e error) {
		rr, e = tight.ReconcileExternalRequest(ctx, res.Request.ID)
		return
	}); err != nil {
		t.Fatal(err)
	}
	if !rr.TooEarly || rr.Request.State != domain.RequestOutcomeUnknown {
		t.Fatalf("15 s is under the floor of 20 s: %+v", rr)
	}
	s.ageRequest(res.Request.ID, 30*time.Second)
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) (e error) {
		rr, e = tight.ReconcileExternalRequest(ctx, res.Request.ID)
		return
	}); err != nil {
		t.Fatal(err)
	}
	if rr.Request.State != domain.RequestConfirmedFailure {
		t.Fatalf("old enough: %+v", rr)
	}
}

// ---- a failing outcome write leaves a request that reconciliation can settle; the same key in parallel calls the ERP once -------------------------------

func TestWhenTheOutcomeCannotBeWrittenTheAttemptStaysAndIsReconciled(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	svc, _, _ := adapters.Build(s.env.App, adapters.Options{ExternalOrders: true, Gateways: gatewaytest.Resolver{F: s.gw}, TxLockTimeout: 300 * time.Millisecond})

	// while the ERP call is in progress somebody holds the request row (an ops query, a stuck transaction): the outcome write cannot get its lock
	ctx := context.Background()
	var holder interface{ Rollback(context.Context) error }
	s.gw.OnCall = func() {
		tx, err := s.env.Seed.Begin(ctx)
		if err != nil {
			t.Error(err)
			return
		}
		holder = tx
		if _, err := tx.Exec(ctx, `SELECT 1 FROM external_record_requests WHERE session_id=$1 FOR UPDATE`, sess.ID); err != nil {
			t.Error(err)
		}
	}
	err := s.try(s.admin, s.env.TenantA, func(c context.Context) error {
		_, e := svc.RequestServiceOrder(c, orderInput(integ, sess.ID, "stuck-"+uuid.NewString()))
		return e
	})
	if err == nil {
		t.Fatal("the outcome write could not get its lock: the call must fail, not pretend")
	}
	if holder != nil {
		_ = holder.Rollback(ctx)
	}
	s.gw.OnCall = nil
	if s.gw.Count("service_order") != 1 {
		t.Fatal("(the ERP did make the order)")
	}
	if n := s.count(`SELECT count(*) FROM external_record_requests WHERE session_id=$1 AND state='in_flight'`, sess.ID); n != 1 {
		t.Fatalf("the attempt is still there, in flight, because it was made durable BEFORE the call: %d", n)
	}
	// it blocks the session until reconciled ...
	if blocked, err := s.request(s.admin, orderInput(integ, sess.ID, "next-"+uuid.NewString())); err != nil || blocked.Outcome != application.OutcomeBlocked {
		t.Fatalf("blocked: %+v %v", blocked, err)
	}
	// ... and reconciliation settles it once its call can no longer be alive
	var id uuid.UUID
	if err := s.env.Seed.QueryRow(ctx, `SELECT id FROM external_record_requests WHERE session_id=$1`, sess.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	s.ageRequest(id, time.Hour)
	var rr application.ReconcileResult
	if err := s.try(s.admin, s.env.TenantA, func(c context.Context) (e error) { rr, e = s.svc.ReconcileExternalRequest(c, id); return }); err != nil {
		t.Fatal(err)
	}
	if rr.Request.State != domain.RequestConfirmedSuccess || rr.Record == nil {
		t.Fatalf("the order the ERP made is found by its correlation key: %+v", rr)
	}
	if s.gw.CreateCalls != 1 {
		t.Fatalf("the ERP was called once: %d", s.gw.CreateCalls)
	}
}

func TestTheSameKeyInParallelCallsTheERPOnce(t *testing.T) {
	s := newStack(t)
	integ := s.integration(s.env.TenantA)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.admin, conv)
	key := "par-ord-" + uuid.NewString()
	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	outcomes := map[application.RequestOutcome]int{}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := s.request(s.admin, orderInput(integ, sess.ID, key))
			if err != nil {
				t.Errorf("parallel request: %v", err)
				return
			}
			mu.Lock()
			outcomes[res.Outcome]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if s.gw.CreateCalls != 1 || s.gw.Count("service_order") != 1 {
		t.Fatalf("the ERP was called once and made one order: calls=%d orders=%d", s.gw.CreateCalls, s.gw.Count("service_order"))
	}
	if outcomes[application.OutcomeCreated] != 1 {
		t.Fatalf("exactly one caller created it: %v", outcomes)
	}
	for o := range outcomes {
		if o != application.OutcomeCreated && o != application.OutcomeReplay && o != application.OutcomeUnknown {
			t.Errorf("unexpected outcome %s", o)
		}
	}
}
