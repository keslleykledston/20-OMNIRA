package adapters

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	aiports "github.com/omnira/omnira/internal/ai/ports"
	"github.com/omnira/omnira/internal/aiusage"
	"github.com/omnira/omnira/internal/groupai/application"
	intelapp "github.com/omnira/omnira/internal/intelligence/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

// Real Postgres, runtime role under RLS. Tenant A is the caller; tenant B owns data that must stay untouched.

type env struct {
	t    *testing.T
	ctx  context.Context
	seed *pgxpool.Pool // the owner: fixtures only
	app  *pgxpool.Pool // the runtime role under RLS
}

func newEnv(t *testing.T) *env {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	return &env{t: t, ctx: ctx, seed: seed, app: app}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.seed.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// tenant creates a tenant with one active WAHA connection.
func (e *env) tenant() (tenant, conn uuid.UUID) {
	tenant, conn = uuid.New(), uuid.New()
	e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenant, tenant.String())
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`,
		conn, tenant, conn.String())
	e.t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.seed.Exec(bg, `DELETE FROM wa_group_imports WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM wa_group_proposals WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM wa_groups WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM service_tasks WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM service_cases WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM channel_connections WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM tenants WHERE id=$1`, tenant)
	})
	return tenant, conn
}

func (e *env) member(tenant uuid.UUID, role, status string) uuid.UUID {
	u := uuid.New()
	e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	var roleID uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, role).Scan(&roleID); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,$4)`, tenant, u, roleID, status)
	return u
}

var jidSeq = 0

// group creates a collected group; ai says whether the AI opt-in is on.
func (e *env) group(tenant, conn uuid.UUID, name string, ai bool) uuid.UUID {
	jidSeq++
	id := uuid.New()
	jid := fmt.Sprintf("1203630000%08d@g.us", jidSeq+int(time.Now().UnixNano()%1000000))
	e.exec(`INSERT INTO wa_groups(id,tenant_id,channel_connection_id,provider_group_id,name,enabled,ai_enabled,last_message_at) VALUES($1,$2,$3,$4,$5,true,$6,now())`, id, tenant, conn, jid, name, ai)
	return id
}

// msg stores a group message created at `at` (created_at drives the cursor of the reading).
func (e *env) msg(tenant, group uuid.UUID, author, body string, at time.Time) string {
	pid := "m-" + uuid.NewString()
	e.exec(`INSERT INTO wa_group_messages(tenant_id,group_id,provider_message_id,author_jid,author_name,from_me,message_type,body,sent_at,created_at) VALUES($1,$2,$3,$4,$5,false,'text',$6,$7,$7)`,
		tenant, group, pid, author+"@lid", author, body, at)
	return pid
}

func (e *env) sessions() application.Sessions {
	return func(ctx context.Context, tenantID uuid.UUID, fn func(context.Context) error) error {
		return platformdb.WithSystemTenantSession(ctx, e.app, tenantID, fn)
	}
}

// inSession runs fn as a real user of the tenant (the API's path).
func (e *env) inSession(tenant, user uuid.UUID, fn func(ctx context.Context)) {
	e.t.Helper()
	err := platformdb.WithTenantSession(e.ctx, e.app, user, false, func(c context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		fn(tenancydomain.WithTenantContext(c, tc))
		return nil
	})
	if err != nil {
		e.t.Fatalf("tenant session: %v", err)
	}
}

// tryIn is inSession for a block that deliberately makes a statement fail (a refused write): the aborted transaction rolls back, which is the point.
func (e *env) tryIn(tenant, user uuid.UUID, fn func(ctx context.Context)) {
	e.t.Helper()
	err := platformdb.WithTenantSession(e.ctx, e.app, user, false, func(c context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		fn(tenancydomain.WithTenantContext(c, tc))
		return nil
	})
	if err != nil && !errors.Is(err, pgx.ErrTxCommitRollback) {
		e.t.Fatalf("tenant session: %v", err)
	}
}

// ---- fakes

// fakeModel plays the provider: it records every input and answers with the scripted outputs in order (the last one repeats).
type fakeModel struct {
	mu      sync.Mutex
	outputs []string
	inputs  []string
	err     error
}

func (f *fakeModel) Generate(_ context.Context, req aiports.GenerateRequest) (aiports.GenerateResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, req.Input)
	if f.err != nil {
		return aiports.GenerateResponse{}, f.err
	}
	out := `{"ops":[]}`
	if len(f.outputs) > 0 {
		out = f.outputs[0]
		if len(f.outputs) > 1 {
			f.outputs = f.outputs[1:]
		}
	}
	return aiports.GenerateResponse{OutputText: out, InputTokens: 100, OutputTokens: 20}, nil
}

func (f *fakeModel) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inputs)
}

func (f *fakeModel) lastInput() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.inputs) == 0 {
		return ""
	}
	return f.inputs[len(f.inputs)-1]
}

func routerFor(m aiports.TextGenerator) *intelapp.ModelRouter {
	r := intelapp.NewModelRouter()
	for _, t := range []intelapp.Task{intelapp.TaskGroupMemory, intelapp.TaskGroupConsolidate, intelapp.TaskGroupProposal} {
		r.Set(t, intelapp.ModelRoute{Provider: "fake", Model: "fake-1", Generator: m, MaxOutputTokens: 1000, Timeout: 5 * time.Second})
	}
	return r
}

type fakeLedger struct {
	mu   sync.Mutex
	recs []aiusage.Record
}

func (l *fakeLedger) Record(_ context.Context, r aiusage.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = append(l.recs, r)
	return nil
}
func (l *fakeLedger) SpentThisMonth(context.Context, uuid.UUID, time.Time) (float64, error) {
	return 0, nil
}

type fakeScanner struct {
	infected bool
	err      error
}

func (s fakeScanner) Scan(context.Context, []byte) (bool, string, error) {
	return s.infected, "Eicar-Test", s.err
}

// fakeBridge stands in for the service module but writes REAL rows (the proposal's foreign keys point at them), as the owner.
type fakeBridge struct {
	e      *env
	tenant uuid.UUID
	mu     sync.Mutex
	cases  map[string]uuid.UUID
	tasks  map[string]uuid.UUID
	err    error
	create int
	seq    int
}

func (e *env) newFakeBridge(tenant uuid.UUID) *fakeBridge {
	return &fakeBridge{e: e, tenant: tenant, cases: map[string]uuid.UUID{}, tasks: map[string]uuid.UUID{}}
}

func (b *fakeBridge) OpenCase(_ context.Context, title, _, _, key string) (uuid.UUID, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return uuid.Nil, b.err
	}
	if id, ok := b.cases[key]; ok {
		return id, nil
	}
	b.create++
	b.seq++
	id := uuid.New()
	b.e.exec(`INSERT INTO service_cases(id,tenant_id,case_number,case_sequence,title) VALUES($1,$2,$3,$4,$5)`, id, b.tenant, fmt.Sprintf("OC-%d", b.seq), b.seq, title)
	b.cases[key] = id
	return id, nil
}

func (b *fakeBridge) CreateTask(_ context.Context, title, _, _ string, _ *time.Time, key string) (uuid.UUID, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return uuid.Nil, b.err
	}
	if id, ok := b.tasks[key]; ok {
		return id, nil
	}
	b.create++
	id := uuid.New()
	b.e.exec(`INSERT INTO service_tasks(id,tenant_id,title,origin_type) VALUES($1,$2,$3,'ai')`, id, b.tenant, title)
	b.tasks[key] = id
	return id, nil
}

// call runs an HTTP handler as a real user of the tenant, in a tenant session.
func call(t *testing.T, pool *pgxpool.Pool, tenant, user uuid.UUID, method, target string, body []byte, path map[string]string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	err := platformdb.WithTenantSession(context.Background(), pool, user, false, func(sessionCtx context.Context) error {
		tc, tcErr := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if tcErr != nil {
			return tcErr
		}
		req := httptest.NewRequest(method, target, bytes.NewReader(body)).WithContext(tenancydomain.WithTenantContext(sessionCtx, tc))
		for k, v := range path {
			req.SetPathValue(k, v)
		}
		fn(rec, req)
		return nil
	})
	if err != nil {
		t.Fatalf("tenant session: %v", err)
	}
	return rec
}

func testFlags() application.Flags {
	f := application.DefaultFlags()
	f.MemoryEnabled, f.ImportEnabled, f.ProposalsEnabled, f.AutoAnalyze = true, true, true, true
	f.ImportLocation = time.UTC
	return f
}

func testCipher(t *testing.T) *ImportCipher {
	t.Helper()
	key := bytes.Repeat([]byte{7}, 32)
	c, err := NewImportCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
