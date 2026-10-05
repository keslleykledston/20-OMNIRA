package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	"github.com/omnira/omnira/internal/identity/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

type env struct {
	t         *testing.T
	seed, app *pgxpool.Pool
	h         *Handler
}

func newEnv(t *testing.T) *env {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(seed.Close)
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return &env{t: t, seed: seed, app: app, h: NewHandler(app, auditadapters.NewPostgresAuditEventRepository(app))}
}

func (e *env) exec(sql string, args ...any) error {
	_, err := e.seed.Exec(context.Background(), sql, args...)
	return err
}

func (e *env) must(sql string, args ...any) {
	e.t.Helper()
	if err := e.exec(sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) n(sql string, args ...any) (n int) {
	e.t.Helper()
	if err := e.seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return
}

func (e *env) tenant() uuid.UUID {
	id := uuid.New()
	e.must(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, id, id.String())
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, id) })
	return id
}

func (e *env) member(tenant uuid.UUID, role, status string) uuid.UUID {
	u := uuid.New()
	e.must(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u.String(), u.String()+"@invalid")
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	var roleID uuid.UUID
	if err := e.seed.QueryRow(context.Background(), `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, role).Scan(&roleID); err != nil {
		e.t.Fatal(err)
	}
	e.must(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,$4)`, tenant, u, roleID, status)
	return u
}

func (e *env) connection(tenant uuid.UUID) uuid.UUID {
	id := uuid.New()
	e.must(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
	        VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','{}')`, id, tenant, "n-"+id.String())
	return id
}

func (e *env) contact(tenant uuid.UUID, phone, email string) uuid.UUID {
	id := uuid.New()
	e.must(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164,email,status) VALUES($1,$2,'Externo',$3,$4,'active')`, id, tenant, phone, email)
	return id
}

func (e *env) do(tenant, user uuid.UUID, method, body string, path map[string]string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := httptest.NewRecorder()
	_ = platformdb.WithTenantSession(context.Background(), e.app, user, false, func(ctx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		req := httptest.NewRequest(method, "/", bytes.NewReader([]byte(body))).WithContext(tenancydomain.WithTenantContext(ctx, tc))
		for k, v := range path {
			req.SetPathValue(k, v)
		}
		fn(rec, req)
		return nil
	})
	return rec
}

func (e *env) find(tenant, user uuid.UUID, typ domain.Type, scope, value string) *domain.Match {
	e.t.Helper()
	var m *domain.Match
	if err := platformdb.WithTenantSession(context.Background(), e.app, user, false, func(ctx context.Context) error {
		tc, _ := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		var err error
		m, err = NewRepository(e.app).FindVerified(tenancydomain.WithTenantContext(ctx, tc), tenant, typ, scope, value)
		return err
	}); err != nil {
		e.t.Fatal(err)
	}
	return m
}

func idOf(t *testing.T, rec *httptest.ResponseRecorder) uuid.UUID {
	t.Helper()
	var d identityDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || d.ID == uuid.Nil {
		t.Fatalf("decode %d %q: %v", rec.Code, rec.Body.String(), err)
	}
	return d.ID
}

func (e *env) create(tenant, admin, user uuid.UUID, typ, value string) *httptest.ResponseRecorder {
	return e.do(tenant, admin, http.MethodPost, fmt.Sprintf(`{"user_id":%q,"identity_type":%q,"value":%q}`, user, typ, value), nil, e.h.Create)
}

func (e *env) verify(tenant, admin, id uuid.UUID, source string) *httptest.ResponseRecorder {
	return e.do(tenant, admin, http.MethodPost, fmt.Sprintf(`{"verification_source":%q}`, source), map[string]string{"identity_id": id.String()}, e.h.Verify)
}

func TestPendingNeverMatchesAndOnlyAdminVerifies(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin, staff := e.member(a, "tenant_admin", "active"), e.member(a, "tenant_agent", "active")
	rec := e.create(a, admin, staff, "phone", "+55 (92) 99999-0001")
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	id := idOf(t, rec)
	var d identityDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if d.Status != "pending" || d.Value != "+5592999990001" || d.VerificationSource != nil {
		t.Fatalf("a new identity must be pending and normalized: %+v", d)
	}
	if e.find(a, admin, domain.TypePhone, "", "+5592999990001") != nil {
		t.Fatal("a PENDING identity must never match")
	}
	// reserved / unknown sources are refused; nothing changes
	for _, src := range []string{"ai", "llm", "challenge", "provider_verified", "import_verified", ""} {
		if rec := e.verify(a, admin, id, src); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("source %q = %d, want 422", src, rec.Code)
		}
	}
	if e.find(a, admin, domain.TypePhone, "", "+5592999990001") != nil || e.n(`SELECT count(*) FROM user_channel_identities WHERE id=$1 AND status='pending'`, id) != 1 {
		t.Fatal("a refused verification changed the identity")
	}
	rec = e.verify(a, admin, id, "admin")
	if rec.Code != 200 {
		t.Fatalf("verify = %d %s", rec.Code, rec.Body.String())
	}
	m := e.find(a, admin, domain.TypePhone, "", "+5592999990001")
	if m == nil || m.UserID != staff || m.HasOpenConflict {
		t.Fatalf("verified match = %+v", m)
	}
	if e.n(`SELECT count(*) FROM user_channel_identities WHERE id=$1 AND verification_source='admin' AND verified_at IS NOT NULL AND verified_by_user_id=$2`, id, admin) != 1 {
		t.Fatal("verification must record source, time and actor")
	}
	if e.n(`SELECT count(*) FROM audit_events WHERE resource_id=$1 AND action IN ('user.identity_created','user.identity_verified')`, id.String()) != 2 {
		t.Fatal("creation and verification must be audited")
	}
	// verifying twice is a state error, not a second verification
	if rec := e.verify(a, admin, id, "admin"); rec.Code != http.StatusConflict {
		t.Errorf("re-verify = %d", rec.Code)
	}
	// membership no longer active → the identity stops matching (the user is not staff here any more)
	e.must(`UPDATE memberships SET status='revoked' WHERE tenant_id=$1 AND user_id=$2`, a, staff)
	if e.find(a, admin, domain.TypePhone, "", "+5592999990001") != nil {
		t.Fatal("a revoked membership must not match")
	}
}

func TestOneVerifiedOwnerPerIdentityPerTenant(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	adminA, adminB := e.member(a, "tenant_admin", "active"), e.member(b, "tenant_admin", "active")
	s1, s2 := e.member(a, "tenant_agent", "active"), e.member(a, "tenant_agent", "active")
	sB := e.member(b, "tenant_agent", "active")
	i1 := idOf(t, e.create(a, adminA, s1, "phone", "+5592999990002"))
	i2 := idOf(t, e.create(a, adminA, s2, "phone", "0055 92 99999-0002")) // same phone, other format, other user
	if rec := e.verify(a, adminA, i1, "admin"); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if rec := e.verify(a, adminA, i2, "admin"); rec.Code != http.StatusConflict {
		t.Fatalf("second verified owner = %d, want 409", rec.Code)
	}
	if e.n(`SELECT count(*) FROM user_channel_identities WHERE id=$1 AND status='pending'`, i2) != 1 {
		t.Fatal("the refused one must stay pending (the savepoint keeps the request usable)")
	}
	// the same user cannot hold the same live identity twice
	if rec := e.create(a, adminA, s1, "phone", "+5592999990002"); rec.Code != http.StatusConflict {
		t.Errorf("duplicate for the same user = %d", rec.Code)
	}
	// the same phone in ANOTHER tenant is independent
	iB := idOf(t, e.create(b, adminB, sB, "phone", "+5592999990002"))
	if rec := e.verify(b, adminB, iB, "admin"); rec.Code != 200 {
		t.Fatalf("other tenant verify = %d", rec.Code)
	}
	if m := e.find(b, adminB, domain.TypePhone, "", "+5592999990002"); m == nil || m.UserID != sB {
		t.Fatalf("tenant B match = %+v", m)
	}
	if m := e.find(a, adminA, domain.TypePhone, "", "+5592999990002"); m == nil || m.UserID != s1 {
		t.Fatalf("tenant A match = %+v", m)
	}
	// once revoked, the identity can be verified for the other user
	if rec := e.do(a, adminA, http.MethodPost, ``, map[string]string{"identity_id": i1.String()}, e.h.Revoke); rec.Code != 200 {
		t.Fatalf("revoke = %d", rec.Code)
	}
	if e.find(a, adminA, domain.TypePhone, "", "+5592999990002") != nil {
		t.Fatal("a revoked identity must not match")
	}
	if rec := e.verify(a, adminA, i2, "admin"); rec.Code != 200 {
		t.Fatalf("verify after revoke = %d %s", rec.Code, rec.Body.String())
	}
	if m := e.find(a, adminA, domain.TypePhone, "", "+5592999990002"); m == nil || m.UserID != s2 {
		t.Fatalf("new owner = %+v", m)
	}
}

func TestVerifiedInternalIdentityThatMatchesAContactOpensAConflict(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin, staff := e.member(a, "tenant_admin", "active"), e.member(a, "tenant_agent", "active")
	c := e.contact(a, "+5592999990003", "")
	cMail := e.contact(a, "+5592999990004", "Ana@K3G.com.br")
	phoneID := idOf(t, e.create(a, admin, staff, "phone", "+5592999990003"))
	rec := e.verify(a, admin, phoneID, "admin")
	var out struct {
		Conflicts []conflictDTO `json:"conflicts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || len(out.Conflicts) != 1 || out.Conflicts[0].ContactID != c {
		t.Fatalf("verify = %d %s", rec.Code, rec.Body.String())
	}
	// the contact is NOT deleted, merged or changed
	if e.n(`SELECT count(*) FROM contacts WHERE id=$1 AND kind='unclassified'`, c) != 1 {
		t.Fatal("the contact must be left alone")
	}
	if m := e.find(a, admin, domain.TypePhone, "", "+5592999990003"); m == nil || !m.HasOpenConflict || m.ConflictConfirmed {
		t.Fatalf("an open conflict must be visible to the resolver: %+v", m)
	}
	if e.n(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='identity.conflict_found'`, a) != 1 {
		t.Fatal("the conflict must be audited")
	}
	// e-mail identity matches case-insensitively
	mailID := idOf(t, e.create(a, admin, staff, "email", " ANA@k3g.com.br "))
	var mailOut struct {
		Conflicts []conflictDTO `json:"conflicts"`
	}
	_ = json.Unmarshal(e.verify(a, admin, mailID, "admin").Body.Bytes(), &mailOut)
	if len(mailOut.Conflicts) != 1 || mailOut.Conflicts[0].ContactID != cMail {
		t.Fatalf("email conflict = %+v", mailOut.Conflicts)
	}
	// resolve: confirmed_internal keeps the identity and clears the "open" flag
	var list struct{ Items []conflictDTO }
	_ = json.Unmarshal(e.do(a, admin, http.MethodGet, "", nil, e.h.ListConflicts).Body.Bytes(), &list)
	if len(list.Items) != 2 {
		t.Fatalf("open conflicts = %d", len(list.Items))
	}
	phoneConflict := out.Conflicts[0].ID
	rec = e.do(a, admin, http.MethodPost, `{"resolution":"confirmed_internal","note":"é a Ana da K3G"}`, map[string]string{"conflict_id": phoneConflict.String()}, e.h.ResolveConflict)
	if rec.Code != 200 {
		t.Fatalf("resolve = %d %s", rec.Code, rec.Body.String())
	}
	if m := e.find(a, admin, domain.TypePhone, "", "+5592999990003"); m == nil || m.HasOpenConflict || !m.ConflictConfirmed {
		t.Fatalf("after confirmation: %+v", m)
	}
	if rec := e.do(a, admin, http.MethodPost, `{"resolution":"confirmed_internal"}`, map[string]string{"conflict_id": phoneConflict.String()}, e.h.ResolveConflict); rec.Code != http.StatusConflict {
		t.Errorf("resolving twice = %d", rec.Code)
	}
	// resolve: identity_revoked revokes the identity
	rec = e.do(a, admin, http.MethodPost, `{"resolution":"identity_revoked"}`, map[string]string{"conflict_id": mailOut.Conflicts[0].ID.String()}, e.h.ResolveConflict)
	if rec.Code != 200 || e.n(`SELECT count(*) FROM user_channel_identities WHERE id=$1 AND status='revoked'`, mailID) != 1 {
		t.Fatalf("revoke resolution = %d", rec.Code)
	}
	if e.find(a, admin, domain.TypeEmail, "", "ana@k3g.com.br") != nil {
		t.Fatal("a revoked identity must not match")
	}
	if rec := e.do(a, admin, http.MethodPost, `{"resolution":"whatever"}`, map[string]string{"conflict_id": uuid.NewString()}, e.h.ResolveConflict); rec.Code != http.StatusNotFound && rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown conflict = %d", rec.Code)
	}
}

func TestProviderParticipantIsScopedToItsConnection(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	admin, staff := e.member(a, "tenant_admin", "active"), e.member(a, "tenant_agent", "active")
	c1, c2, foreign := e.connection(a), e.connection(a), e.connection(b)
	body := func(conn uuid.UUID) string {
		return fmt.Sprintf(`{"user_id":%q,"identity_type":"provider_participant","value":"123456789@LID","provider":"waha","connection_id":%q}`, staff, conn)
	}
	rec := e.do(a, admin, http.MethodPost, body(c1), nil, e.h.Create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.verify(a, admin, idOf(t, rec), "admin"); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	scope := domain.ParticipantScope("waha", c1)
	if e.find(a, admin, domain.TypeProviderParticipant, scope, "123456789@lid") == nil {
		t.Fatal("participant must match inside its own connection")
	}
	if e.find(a, admin, domain.TypeProviderParticipant, domain.ParticipantScope("waha", c2), "123456789@lid") != nil {
		t.Fatal("the same JID on ANOTHER connection is another sender")
	}
	if rec := e.do(a, admin, http.MethodPost, body(foreign), nil, e.h.Create); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("another tenant's connection = %d", rec.Code)
	}
	if rec := e.do(a, admin, http.MethodPost, fmt.Sprintf(`{"user_id":%q,"identity_type":"provider_participant","value":"x"}`, staff), nil, e.h.Create); rec.Code != http.StatusBadRequest {
		t.Errorf("participant without a connection = %d", rec.Code)
	}
}

func TestIdentityAPIPermissionsAndTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	adminA, adminB := e.member(a, "tenant_admin", "active"), e.member(b, "tenant_admin", "active")
	supervisor, agent := e.member(a, "tenant_supervisor", "active"), e.member(a, "tenant_agent", "active")
	revoked := e.member(a, "tenant_admin", "revoked")
	id := idOf(t, e.create(a, adminA, agent, "phone", "+5592999990005"))
	path := map[string]string{"identity_id": id.String()}
	// only identity.manage (admin) may touch identities: supervisors and agents get 403 everywhere
	for name, u := range map[string]uuid.UUID{"agent": agent, "supervisor": supervisor, "revoked admin": revoked} {
		for op, rec := range map[string]*httptest.ResponseRecorder{
			"list":     e.do(a, u, http.MethodGet, "", nil, e.h.List),
			"create":   e.create(a, u, agent, "phone", "+5592999990006"),
			"verify":   e.verify(a, u, id, "admin"),
			"revoke":   e.do(a, u, http.MethodPost, "", path, e.h.Revoke),
			"conflict": e.do(a, u, http.MethodGet, "", nil, e.h.ListConflicts),
		} {
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s = %d, want 403", name, op, rec.Code)
			}
		}
	}
	if e.n(`SELECT count(*) FROM user_channel_identities WHERE tenant_id=$1`, a) != 1 {
		t.Fatal("forbidden calls changed data")
	}
	// B's admin cannot see, verify or revoke A's identity (404, same as unknown) nor create for A's user
	if rec := e.verify(b, adminB, id, "admin"); rec.Code != http.StatusNotFound {
		t.Errorf("foreign verify = %d", rec.Code)
	}
	if rec := e.do(b, adminB, http.MethodPost, "", path, e.h.Revoke); rec.Code != http.StatusNotFound {
		t.Errorf("foreign revoke = %d", rec.Code)
	}
	if rec := e.create(b, adminB, agent, "phone", "+5592999990007"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("A's user inside B = %d, want 422", rec.Code)
	}
	var list struct{ Items []identityDTO }
	_ = json.Unmarshal(e.do(b, adminB, http.MethodGet, "", nil, e.h.List).Body.Bytes(), &list)
	if len(list.Items) != 0 {
		t.Fatal("B listed A's identities")
	}
	// payload hygiene
	for name, body := range map[string]string{
		"tenant in payload": fmt.Sprintf(`{"user_id":%q,"identity_type":"phone","value":"+5592999990008","tenant_id":%q}`, agent, b),
		"bad type":          fmt.Sprintf(`{"user_id":%q,"identity_type":"fax","value":"+5592999990008"}`, agent),
		"bad phone":         fmt.Sprintf(`{"user_id":%q,"identity_type":"phone","value":"banana"}`, agent),
		"bad email":         fmt.Sprintf(`{"user_id":%q,"identity_type":"email","value":"nope"}`, agent),
		"no user":           `{"identity_type":"phone","value":"+5592999990008"}`,
	} {
		if rec := e.do(a, adminA, http.MethodPost, body, nil, e.h.Create); rec.Code < 400 {
			t.Errorf("%s = %d", name, rec.Code)
		}
	}
}

func TestDatabaseRefusesWhatTheAPIWouldNever(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a, "tenant_admin", "active")
	ins := func(status, source, typ, value string) error {
		var src any
		if source != "" {
			src = source
		}
		return e.exec(`INSERT INTO user_channel_identities(tenant_id,user_id,identity_type,raw_value,normalized_value,status,verification_source,verified_at)
			VALUES($1,$2,$3,$4,$4,$5,$6, CASE WHEN $5='verified' THEN now() END)`, a, admin, typ, value, status, src)
	}
	if err := ins("verified", "ai", "phone", "+5592999990010"); err == nil {
		t.Error("an AI verification source must be impossible")
	}
	if err := ins("verified", "", "phone", "+5592999990011"); err == nil {
		t.Error("verified without a source must be impossible")
	}
	if err := ins("pending", "admin", "phone", "+5592999990012"); err == nil {
		t.Error("pending with a source must be impossible")
	}
	if err := ins("pending", "", "phone", "not-a-phone"); err == nil {
		t.Error("a non-E.164 phone must be impossible")
	}
	if err := ins("pending", "", "email", "NoAt"); err == nil {
		t.Error("an e-mail without @ must be impossible")
	}
	if err := ins("pending", "", "phone", "+5592999990013"); err != nil {
		t.Errorf("a valid pending identity must be accepted: %v", err)
	}
	// the runtime role cannot erase identities (no DELETE policy): the trail stays
	var n int64
	_ = platformdb.WithTenantSession(context.Background(), e.app, admin, false, func(ctx context.Context) error {
		tag, _ := platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, `DELETE FROM user_channel_identities WHERE tenant_id=$1`, a)
		n = tag.RowsAffected()
		return nil
	})
	if n != 0 || e.n(`SELECT count(*) FROM user_channel_identities WHERE tenant_id=$1`, a) != 1 {
		t.Fatalf("the runtime role deleted %d identities", n)
	}
}
