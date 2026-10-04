package adapters_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

type kindEnv struct {
	t    *testing.T
	ctx  context.Context
	seed *pgxpool.Pool
	app  *pgxpool.Pool
}

func newKindEnv(t *testing.T) *kindEnv {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	return &kindEnv{t: t, ctx: ctx, seed: seed, app: app}
}

func (e *kindEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *kindEnv) tenant() uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, id, id.String())
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, id) })
	return id
}

func (e *kindEnv) member(tenant uuid.UUID, role string) uuid.UUID {
	u := uuid.New()
	e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	var roleID uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, role).Scan(&roleID); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenant, u, roleID)
	return u
}

// conversation seeds a contact of the given kind and an unrouted open conversation for it.
func (e *kindEnv) conversation(tenant uuid.UUID, name, kind, phone string) uuid.UUID {
	contact, conv := uuid.New(), uuid.New()
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164,kind) VALUES($1,$2,$3,$4,$5)`, contact, tenant, name, phone, kind)
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, conv, tenant, contact)
	return conv
}

func TestListConversationsFiltersByContactKindAndKeepsSpamOutOfTheDefaultView(t *testing.T) {
	e := newKindEnv(t)
	tenantA, tenantB := e.tenant(), e.tenant()
	user := e.member(tenantA, "tenant_agent")
	cust := e.conversation(tenantA, "Cliente", "customer", "+5592933330001")
	other := e.conversation(tenantA, "Outro", "other", "+5592933330002")
	spam := e.conversation(tenantA, "Spam", "spam", "+5592933330003")
	e.conversation(tenantB, "Spam de B", "spam", "+5592933330004")

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(e.app), tenancyadapters.NewPostgresTenantRepository(e.app))
	h := inboxadapters.NewInboxAPIHandler(e.app)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations", tenancyadapters.AuthorizationMiddleware(e.app, authz)(http.HandlerFunc(h.ListConversations)))
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}", tenancyadapters.AuthorizationMiddleware(e.app, authz)(http.HandlerFunc(h.GetConversation)))
	get := func(path string) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	list := func(query string) (int, map[string]string) {
		code, body := get("/api/v1/tenants/" + tenantA.String() + "/inbox/conversations" + query)
		kinds := map[string]string{}
		if code == 200 {
			var page struct {
				Items []struct {
					ID          string `json:"id"`
					ContactKind string `json:"contact_kind"`
				} `json:"items"`
			}
			if err := json.Unmarshal(body, &page); err != nil {
				t.Fatal(err)
			}
			for _, it := range page.Items {
				kinds[it.ID] = it.ContactKind
			}
		}
		return code, kinds
	}

	// Default view: customers and others, never spam; every item says what its contact is.
	if code, got := list(""); code != 200 || len(got) != 2 || got[cust.String()] != "customer" || got[other.String()] != "other" {
		t.Fatalf("default view = %d %v", code, got)
	}
	// Spam is kept and reachable on request.
	if code, got := list("?kind=spam"); code != 200 || len(got) != 1 || got[spam.String()] != "spam" {
		t.Fatalf("kind=spam = %d %v (tenant B's spam must not appear)", code, got)
	}
	if code, got := list("?kind=customer"); code != 200 || len(got) != 1 || got[cust.String()] != "customer" {
		t.Fatalf("kind=customer = %d %v", code, got)
	}
	if code, _ := list("?kind=vip"); code != http.StatusBadRequest {
		t.Fatalf("unknown kind = %d, want 400", code)
	}
	// The single-conversation read also carries it (the context pane needs it).
	code, body := get("/api/v1/tenants/" + tenantA.String() + "/inbox/conversations/" + spam.String())
	var one struct {
		ContactKind string `json:"contact_kind"`
	}
	if code != 200 || json.Unmarshal(body, &one) != nil || one.ContactKind != "spam" {
		t.Fatalf("get conversation = %d %s", code, body)
	}
}

// ADR-0014: a spam contact's conversation is stored but never routed to a queue (and never enqueues
// an assignment job); every other conversation is routed exactly as before.
func TestRouteNewSkipsSpamContactsInManualAndRoundRobinQueues(t *testing.T) {
	e := newKindEnv(t)
	store := inboxadapters.NewPostgresInboundStore(e.app)
	route := func(tenant, user, conv uuid.UUID) {
		t.Helper()
		err := platformdb.WithTenantSession(e.ctx, e.app, user, false, func(ctx context.Context) error {
			tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
			if err != nil {
				return err
			}
			return store.RouteNew(tenancydomain.WithTenantContext(ctx, tc), conv)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	queueOf := func(conv uuid.UUID) *uuid.UUID {
		var q *uuid.UUID
		if err := e.seed.QueryRow(e.ctx, `SELECT queue_id FROM conversations WHERE id=$1`, conv).Scan(&q); err != nil {
			t.Fatal(err)
		}
		return q
	}
	jobs := func(conv uuid.UUID) int {
		var n int
		if err := e.seed.QueryRow(e.ctx, `SELECT count(*) FROM outbox_events WHERE event_type='job.routing.assign.v1' AND aggregate_id=$1`, conv.String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	for _, mode := range []string{"manual", "round_robin"} {
		tenant := e.tenant()
		user := e.member(tenant, "tenant_agent")
		queue := uuid.New()
		e.exec(`INSERT INTO queues(id,tenant_id,name,mode,is_default) VALUES($1,$2,'Default',$3,true)`, queue, tenant, mode)
		normal := e.conversation(tenant, "Normal", "other", "+5592944440001")
		customer := e.conversation(tenant, "Cliente", "customer", "+5592944440002")
		spam := e.conversation(tenant, "Spam", "spam", "+5592944440003")
		for _, c := range []uuid.UUID{normal, customer, spam} {
			route(tenant, user, c)
		}
		for name, c := range map[string]uuid.UUID{"other": normal, "customer": customer} {
			if q := queueOf(c); q == nil || *q != queue {
				t.Errorf("%s/%s conversation must be routed to the default queue, got %v", mode, name, q)
			}
		}
		if q := queueOf(spam); q != nil {
			t.Errorf("%s: spam conversation must stay unrouted, got queue %v", mode, *q)
		}
		wantJobs := 0
		if mode == "round_robin" {
			wantJobs = 1
		}
		if jobs(normal) != wantJobs || jobs(customer) != wantJobs || jobs(spam) != 0 {
			t.Errorf("%s: assignment jobs normal=%d customer=%d spam=%d, want %d/%d/0", mode, jobs(normal), jobs(customer), jobs(spam), wantJobs, wantJobs)
		}
	}
}
