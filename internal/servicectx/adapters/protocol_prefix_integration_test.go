package adapters_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

// The protocol of a company reads <PREFIX>-<number>: the prefix is derived from the company's name (or chosen by its administrator), is unique among
// companies, and is frozen once the first protocol exists. Numbers start at 1001.

func (s *stack) nameTenants(a, b string) {
	s.t.Helper()
	s.exec(`UPDATE tenants SET trade_name = $2 WHERE id = $1`, s.env.TenantA, a)
	s.exec(`UPDATE tenants SET trade_name = $2 WHERE id = $1`, s.env.TenantB, b)
}

func (s *stack) openIn(tenant, user uuid.UUID, conv uuid.UUID) (*domain.Session, error) {
	var sess *domain.Session
	err := s.try(user, tenant, func(ctx context.Context) error {
		res, e := s.svc.Open(ctx, domain.OpenInput{ConversationID: &conv})
		sess = res.Session
		return e
	})
	return sess, err
}

func TestThePrefixIsDerivedFromTheCompanyNameAndNoTwoCompaniesShareOne(t *testing.T) {
	s := newStack(t)
	s.nameTenants("Zyxwv Telecom", "Zyxel Net")
	convA, _ := s.conversation()
	a, err := s.openIn(s.env.TenantA, s.agent, convA)
	if err != nil || a.Protocol != "ZYX-1001" || a.ProtocolSequence != 1001 {
		t.Fatalf("company A: %v %+v", err, a)
	}
	// B would also be ZYX: it takes the next candidate instead of failing or sharing it
	convB, _ := s.env.SeedConversation(t, s.env.TenantB, "none")
	b, err := s.openIn(s.env.TenantB, s.env.UserB, convB)
	if err != nil || b.Protocol != "ZYXE-1001" {
		t.Fatalf("company B: %v %+v", err, b)
	}
	// the prefix does not change with the name afterwards
	s.nameTenants("Outro Nome", "Zyxel Net")
	conv2, _ := s.conversation()
	a2, err := s.openIn(s.env.TenantA, s.agent, conv2)
	if err != nil || a2.Protocol != "ZYX-1002" {
		t.Fatalf("the prefix stays with the company: %v %+v", err, a2)
	}
}

func TestTheAdministratorChoosesThePrefixBeforeTheFirstProtocolAndItFreezesAfterwards(t *testing.T) {
	s := newStack(t)
	s.nameTenants("Kyz Solutions", "Other Co")
	var cfg any
	get := func(user uuid.UUID) (v struct {
		Prefix     string
		Configured bool
		Locked     bool
		Next       string
	}, err error) {
		err = s.try(user, s.env.TenantA, func(ctx context.Context) error {
			p, e := s.catalog.ProtocolSettings(ctx)
			v.Prefix, v.Configured, v.Locked, v.Next = p.Prefix, p.Configured, p.Locked, p.Next
			return e
		})
		return
	}
	set := func(user uuid.UUID, prefix string) (string, error) {
		var next string
		err := s.try(user, s.env.TenantA, func(ctx context.Context) error {
			p, e := s.catalog.SetProtocolPrefix(ctx, prefix)
			next = p.Next
			return e
		})
		return next, err
	}
	_ = cfg

	v, err := get(s.agent)
	if err != nil || v.Prefix != "KYZ" || v.Configured || v.Locked || v.Next != "KYZ-1001" {
		t.Fatalf("before anything: the suggestion only: %v %+v", err, v)
	}
	if _, err := set(s.agent, "KTG"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an attendant does not configure the company: %v", err)
	}
	if _, err := get(s.observer); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("reading needs service.read: %v", err)
	}
	for _, bad := range []string{"K3G", "A", "ABCDEFG", "", "KT G", "AÇB"} {
		if _, err := set(s.admin, bad); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%q must be refused (letters only, 2 to 6): %v", bad, err)
		}
	}
	if next, err := set(s.admin, " ktg "); err != nil || next != "KTG-1001" {
		t.Fatalf("typed in lower case with spaces, stored as KTG: %v %s", err, next)
	}
	if v, err = get(s.agent); err != nil || v.Prefix != "KTG" || !v.Configured || v.Locked {
		t.Fatalf("reserved, not yet final: %v %+v", err, v)
	}
	if next, err := set(s.admin, "KTX"); err != nil || next != "KTX-1001" {
		t.Fatalf("the prefix can still change while no protocol exists: %v %s", err, next)
	}
	conv, _ := s.conversation()
	if sess := s.mustOpen(s.agent, conv); sess.Protocol != "KTX-1001" {
		t.Fatalf("the first protocol uses the chosen prefix and starts at 1001: %s", sess.Protocol)
	}
	if v, err = get(s.agent); err != nil || !v.Locked || v.Next != "KTX-1002" {
		t.Fatalf("frozen after the first protocol: %v %+v", err, v)
	}
	if _, err := set(s.admin, "ABC"); !errors.Is(err, domain.ErrPrefixLocked) {
		t.Fatalf("another prefix after a protocol exists: %v", err)
	}
	if _, err := set(s.admin, "KTX"); err != nil {
		t.Fatalf("repeating the current prefix is harmless: %v", err)
	}
	if n := s.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND action = 'service_settings.protocol_prefix_set'`, s.env.TenantA); n < 3 {
		t.Fatalf("every accepted change is audited: %d", n)
	}
}

func TestAPrefixOfAnotherCompanyIsRefusedAndLeavesNothingBehind(t *testing.T) {
	s := newStack(t)
	s.nameTenants("Alfa", "Beta")
	if err := s.try(s.env.UserB, s.env.TenantB, func(ctx context.Context) error { _, e := s.catalog.SetProtocolPrefix(ctx, "QQK"); return e }); err != nil {
		t.Fatalf("B reserves QQK: %v", err)
	}
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error { _, e := s.catalog.SetProtocolPrefix(ctx, "qqk"); return e }); !errors.Is(err, domain.ErrPrefixTaken) {
		t.Fatalf("A cannot take B's prefix: %v", err)
	}
	if n := s.count(`SELECT count(*) FROM service_counters WHERE tenant_id = $1`, s.env.TenantA); n != 0 {
		t.Fatalf("a refused reservation leaves no row: %d", n)
	}
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error { _, e := s.catalog.SetProtocolPrefix(ctx, "QQL"); return e }); err != nil {
		t.Fatalf("a free one works right after: %v", err)
	}
}

// The rules live in the database too: a person with the key of an operation but not of the configuration cannot move the prefix or the counter with raw SQL.
func TestTheDatabaseGuardsThePrefixAndTheCounter(t *testing.T) {
	s := newStack(t)
	s.nameTenants("Guard Co", "Other Guard")
	if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error { _, e := s.catalog.SetProtocolPrefix(ctx, "GRD"); return e }); err != nil {
		t.Fatal(err)
	}
	raw := func(user uuid.UUID, sql string, args ...any) error {
		return s.try(user, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, sql, args...) })
	}
	// reserved (no protocol yet): changing it needs tenant.manage, which the attendant does not have
	if err := raw(s.agent, `UPDATE service_counters SET prefix = 'GRX' WHERE tenant_id = $1 AND kind = 'session'`, s.env.TenantA); err == nil || !strings.Contains(err.Error(), "tenant.manage") {
		t.Fatalf("an attendant cannot change a reserved prefix: %v", err)
	}
	// an attendant cannot reserve a prefix for a company that has none either
	if err := s.try(s.agent, s.env.TenantB, func(ctx context.Context) error {
		return execInSession(ctx, `INSERT INTO service_counters(tenant_id, kind, prefix, last_value) VALUES($1,'session','HAX',0)`, s.env.TenantB)
	}); err == nil {
		t.Fatal("a reservation (last_value 0) needs tenant.manage")
	}
	conv, _ := s.conversation()
	s.mustOpen(s.agent, conv) // GRD-1001: now it is frozen
	if err := raw(s.admin, `UPDATE service_counters SET prefix = 'GRX' WHERE tenant_id = $1 AND kind = 'session'`, s.env.TenantA); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("not even the administrator changes a prefix in use: %v", err)
	}
	if err := raw(s.agent, `UPDATE service_counters SET last_value = 5 WHERE tenant_id = $1 AND kind = 'session'`, s.env.TenantA); err == nil || !strings.Contains(err.Error(), "forward") {
		t.Fatalf("a counter never goes back: %v", err)
	}
	// the global index: another company cannot take a prefix that exists, even by raw SQL
	if err := s.try(s.env.UserB, s.env.TenantB, func(ctx context.Context) error {
		return execInSession(ctx, `INSERT INTO service_counters(tenant_id, kind, prefix, last_value) VALUES($1,'session','GRD',0)`, s.env.TenantB)
	}); err == nil {
		t.Fatal("two companies cannot share a session prefix")
	}
	// the case counter is not part of it: every company numbers its cases OC-n
	if n := s.count(`SELECT count(*) FROM service_counters WHERE kind = 'case' AND prefix <> 'OC'`); n != 0 {
		t.Fatalf("the case prefix stays OC: %d", n)
	}
}

func TestThePrefixRoutesAreForMembersAndSpeakHTTP(t *testing.T) {
	s := newStack(t)
	s.nameTenants("Route Co", "Other Route")
	mux := s.mux()
	const path = "/service-settings/protocol-prefix"
	if r := s.call(mux, s.agent, "GET", path, ""); r.code != http.StatusOK || r.body["prefix"] != "ROU" || r.body["configured"] != false || r.body["next_protocol"] != "ROU-1001" {
		t.Fatalf("suggestion: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "PUT", path, `{"prefix":"KTG"}`); r.code != http.StatusForbidden {
		t.Fatalf("an attendant: %d", r.code)
	}
	if r := s.call(mux, s.admin, "PUT", path, `{"prefix":"K3G"}`); r.code != http.StatusBadRequest {
		t.Fatalf("digits: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.admin, "PUT", path, `{"prefix":"KTG","tenant_id":"x"}`); r.code != http.StatusBadRequest {
		t.Fatalf("unknown field: %d", r.code)
	}
	if r := s.call(mux, s.admin, "PUT", path, `{"prefix":"htg"}`); r.code != http.StatusOK || r.body["prefix"] != "HTG" || r.body["configured"] != true || r.body["next_protocol"] != "HTG-1001" {
		t.Fatalf("set: %d %v", r.code, r.body)
	}
	conv, _ := s.conversation()
	if r := s.call(mux, s.agent, "POST", "/service-sessions", `{"conversation_id":"`+conv.String()+`"}`, "Idempotency-Key", "prefix-route-"+uuid.NewString()); r.code != http.StatusCreated || r.body["protocol"] != "HTG-1001" {
		t.Fatalf("open: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.admin, "PUT", path, `{"prefix":"ABC"}`); r.code != http.StatusConflict || r.body["error"] != "prefix_locked" {
		t.Fatalf("locked: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "GET", path, ""); r.code != http.StatusOK || r.body["locked"] != true || r.body["next_protocol"] != "HTG-1002" {
		t.Fatalf("after: %d %v", r.code, r.body)
	}
}

// A closed attendance can be reopened for 24 hours (the default); after that the customer who comes back gets a new protocol.
func TestADefaultServiceReopensWithinTwentyFourHoursAndNotAfter(t *testing.T) {
	s := newStack(t)
	closeIt := func(hoursAgo int) (*domain.Session, int) {
		conv, _ := s.conversation()
		sess := s.mustOpen(s.agent, conv)
		closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, sess.Version)
		if err != nil {
			t.Fatal(err)
		}
		// the whole stretch moves back together, so the session stays consistent (it closed after it opened)
		s.noTriggers(`UPDATE service_sessions SET opened_at = opened_at - make_interval(hours => $2), closed_at = closed_at - make_interval(hours => $2) WHERE id = $1`, sess.ID, hoursAgo)
		return sess, closed.Version
	}
	late, lateVersion := closeIt(25)
	if _, err := s.reopen(s.svc, s.agent, late.ID, lateVersion); !errors.Is(err, domain.ErrReopenNotAllowed) {
		t.Fatalf("25 hours after closing: %v", err)
	}
	soon, soonVersion := closeIt(23)
	if r, err := s.reopen(s.svc, s.agent, soon.ID, soonVersion); err != nil || r.Status != domain.SessionInProgress {
		t.Fatalf("23 hours after closing: %v", err)
	}
}

// Whoever manages the company configures the prefix even without service.create (the owner of the company is not necessarily an attendant).
func TestSomeoneWhoOnlyManagesTheCompanyCanReserveAndChangeThePrefix(t *testing.T) {
	s := newStack(t)
	manager, role := uuid.New(), uuid.New()
	s.exec(`INSERT INTO roles(id, tenant_id, key, name) VALUES($1,$2,'manager-only','Manager only')`, role, s.env.TenantA)
	s.exec(`INSERT INTO role_permissions(role_id, permission_key) VALUES($1,'tenant.manage')`, role)
	s.exec(`INSERT INTO users(id, external_subject, email, status) VALUES($1,$2,$3,'active')`, manager, manager.String(), manager.String()+"@invalid")
	s.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, s.env.TenantA, manager, role)
	t.Cleanup(func() { _, _ = s.env.Seed.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, manager) })
	set := func(prefix string) error {
		return s.try(manager, s.env.TenantA, func(ctx context.Context) error { _, e := s.catalog.SetProtocolPrefix(ctx, prefix); return e })
	}
	stored := func() string {
		var p string
		if err := s.env.Seed.QueryRow(context.Background(), `SELECT prefix FROM service_counters WHERE tenant_id = $1 AND kind = 'session'`, s.env.TenantA).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if err := set("MGA"); err != nil || stored() != "MGA" {
		t.Fatalf("first reservation: %v %s", err, stored())
	}
	if err := set("MGB"); err != nil || stored() != "MGB" {
		t.Fatalf("changing the reservation needs tenant.manage and nothing else: %v %s", err, stored())
	}
}

func TestACounterMovesForwardByOneNeverByJumps(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	s.mustOpen(s.agent, conv) // 1001
	raw := func(sql string) error {
		return s.try(s.agent, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, sql, s.env.TenantA) })
	}
	if err := raw(`UPDATE service_counters SET last_value = last_value + 5 WHERE tenant_id = $1 AND kind = 'session'`); err == nil || !strings.Contains(err.Error(), "forward by one") {
		t.Fatalf("a jump is refused: %v", err)
	}
	if err := raw(`UPDATE service_counters SET last_value = last_value + 1 WHERE tenant_id = $1 AND kind = 'session'`); err != nil {
		t.Fatalf("one step is what the application does: %v", err)
	}
}

// A refused prefix must not poison the transaction of whoever asked (the unique violation runs under a savepoint).
func TestARefusedPrefixLeavesTheCallersTransactionUsable(t *testing.T) {
	s := newStack(t)
	if err := s.try(s.env.UserB, s.env.TenantB, func(ctx context.Context) error { _, e := s.catalog.SetProtocolPrefix(ctx, "SVP"); return e }); err != nil {
		t.Fatal(err)
	}
	err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error {
		if _, e := s.catalog.SetProtocolPrefix(ctx, "SVP"); !errors.Is(e, domain.ErrPrefixTaken) {
			return errors.New("expected ErrPrefixTaken, got " + e.Error())
		}
		_, e := s.catalog.SetProtocolPrefix(ctx, "SVQ") // same transaction, right after the refusal
		return e
	})
	if err != nil {
		t.Fatalf("the transaction survives a taken prefix: %v", err)
	}
}

// Two companies with the same name opening their first protocol at the same instant end up with different prefixes.
func TestTwoCompaniesWithTheSameNameGetDifferentPrefixesEvenAtTheSameInstant(t *testing.T) {
	s := newStack(t)
	s.nameTenants("Race Telecom", "Race Telecom")
	convA, _ := s.conversation()
	convB, _ := s.env.SeedConversation(t, s.env.TenantB, "none")
	var wg sync.WaitGroup
	var a, b *domain.Session
	var ea, eb error
	wg.Add(2)
	go func() { defer wg.Done(); a, ea = s.openIn(s.env.TenantA, s.agent, convA) }()
	go func() { defer wg.Done(); b, eb = s.openIn(s.env.TenantB, s.env.UserB, convB) }()
	wg.Wait()
	if ea != nil || eb != nil {
		t.Fatalf("both open: %v %v", ea, eb)
	}
	if a.Protocol == b.Protocol || !strings.HasSuffix(a.Protocol, "-1001") || !strings.HasSuffix(b.Protocol, "-1001") {
		t.Fatalf("different prefixes, both numbering from 1001: %s %s", a.Protocol, b.Protocol)
	}
	if got := []string{a.Protocol, b.Protocol}; !(got[0] == "RAC-1001" || got[1] == "RAC-1001") {
		t.Fatalf("one of them has the first choice: %v", got)
	}
}
