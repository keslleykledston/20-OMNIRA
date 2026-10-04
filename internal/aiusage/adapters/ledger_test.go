package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/aiusage"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/testhelpers"
)

type lenv struct {
	t    *testing.T
	ctx  context.Context
	seed *pgxpool.Pool
	app  *pgxpool.Pool
}

func newLEnv(t *testing.T) *lenv {
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
	return &lenv{t: t, ctx: ctx, seed: seed, app: app}
}

func (e *lenv) tenant() uuid.UUID {
	id := uuid.New()
	if _, err := e.seed.Exec(e.ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, id, id.String()); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, id) })
	return id
}

func (e *lenv) admin(tenant uuid.UUID) uuid.UUID {
	u := uuid.New()
	if _, err := e.seed.Exec(e.ctx, `INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid"); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	var role uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT id FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL`).Scan(&role); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.seed.Exec(e.ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenant, u, role); err != nil {
		e.t.Fatal(err)
	}
	return u
}

func TestLedgerRecordsEveryCallAndSumsOnlyTheBudgetedProviderInTheMonth(t *testing.T) {
	e := newLEnv(t)
	a, b := e.tenant(), e.tenant()
	l := NewPostgresLedger(e.app)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	rec := func(tenant uuid.UUID, provider, task string, cost float64, noCost, ok bool) {
		if err := l.Record(e.ctx, aiusage.Record{TenantID: tenant, Provider: provider, Model: "m", Task: task, InputTokens: 100, OutputTokens: 10, CostUSD: cost, NoCost: noCost, Success: ok, Ref: uuid.New()}); err != nil {
			t.Fatal(err)
		}
	}
	rec(a, "gemini", "description", 0.40, false, true)
	rec(a, "gemini", "document_text", 0.25, false, false) // a failed call may still be billed: it counts
	rec(a, "openai", "topic_summary", 0, true, true)      // platform-paid: tokens recorded, never against the tenant budget
	rec(b, "gemini", "description", 5.00, false, true)    // another tenant
	// outside the month: back-date one row
	rec(a, "gemini", "description", 9.00, false, true)
	if _, err := e.seed.Exec(e.ctx, `UPDATE ai_usage SET created_at = '2026-09-30 23:59:59+00' WHERE tenant_id=$1 AND cost_usd = 9.00`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.seed.Exec(e.ctx, `UPDATE ai_usage SET created_at = '2026-10-10 10:00:00+00' WHERE tenant_id=$1 AND created_at > '2026-10-31'`, a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.seed.Exec(e.ctx, `UPDATE ai_usage SET created_at = '2026-10-10 10:00:00+00' WHERE tenant_id IN ($1,$2) AND created_at > '2026-10-01' AND created_at < '2026-10-31'`, a, b); err != nil {
		t.Fatal(err)
	}
	// the rows inserted "now" are in the real current month; move them all into Oct 2026 for a deterministic test
	if _, err := e.seed.Exec(e.ctx, `UPDATE ai_usage SET created_at = '2026-10-10 10:00:00+00' WHERE tenant_id IN ($1,$2) AND cost_usd IS DISTINCT FROM 9.00`, a, b); err != nil {
		t.Fatal(err)
	}
	spent, err := l.SpentThisMonth(e.ctx, a, now)
	if err != nil || spent != 0.65 {
		t.Fatalf("spent = %v (%v), want 0.65 (gemini only, this month only, failures included)", spent, err)
	}
	if got, _ := l.SpentThisMonth(e.ctx, b, now); got != 5.00 {
		t.Fatalf("tenant B spent = %v", got)
	}
	if got, _ := l.SpentThisMonth(e.ctx, a, time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)); got != 9.00 {
		t.Fatalf("september = %v", got)
	}
	if got, _ := l.SpentThisMonth(e.ctx, uuid.New(), now); got != 0 {
		t.Fatalf("unknown tenant = %v", got)
	}
	var nulls int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM ai_usage WHERE tenant_id=$1 AND cost_usd IS NULL`, a).Scan(&nulls)
	if nulls != 1 {
		t.Fatalf("a platform-paid call must store NULL cost, not a free call: %d", nulls)
	}
}

func TestLedgerIsAppendOnlyAndWritableOnlyBySystemAndReadableOnlyByTheTenantAdmin(t *testing.T) {
	e := newLEnv(t)
	a, b := e.tenant(), e.tenant()
	adminA, adminB := e.admin(a), e.admin(b)
	l := NewPostgresLedger(e.app)
	if err := l.Record(e.ctx, aiusage.Record{TenantID: a, Provider: "gemini", Model: "m", Task: "description", CostUSD: 1, Success: true}); err != nil {
		t.Fatal(err)
	}
	asUser := func(user uuid.UUID, fn func(ctx context.Context) error) error {
		return platformdb.WithTenantSession(e.ctx, e.app, user, false, fn)
	}
	// a member (even an admin) cannot insert, update or delete
	for name, sql := range map[string]string{
		"insert": `INSERT INTO ai_usage(tenant_id,provider,task,success) VALUES('` + a.String() + `','gemini','x',true)`,
		"update": `UPDATE ai_usage SET cost_usd = 0 WHERE tenant_id='` + a.String() + `'`,
		"delete": `DELETE FROM ai_usage WHERE tenant_id='` + a.String() + `'`,
	} {
		err := asUser(adminA, func(ctx context.Context) error {
			tag, err := platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, sql)
			if err == nil && tag.RowsAffected() > 0 {
				t.Errorf("%s changed %d rows", name, tag.RowsAffected())
			}
			return err
		})
		_ = err // a denial may surface as an error or as 0 rows; both are refusals
	}
	var cost float64
	_ = e.seed.QueryRow(e.ctx, `SELECT cost_usd::float8 FROM ai_usage WHERE tenant_id=$1`, a).Scan(&cost)
	if cost != 1 {
		t.Fatalf("the ledger row changed: %v", cost)
	}
	// reads: the tenant's own admin sees the row; another tenant's admin sees nothing
	seen := func(user, tenant uuid.UUID) (n int) {
		_ = asUser(user, func(ctx context.Context) error {
			return platformdb.QuerierFromContext(ctx, e.app).QueryRow(ctx, `SELECT count(*) FROM ai_usage`).Scan(&n)
		})
		return n
	}
	if seen(adminA, a) != 1 || seen(adminB, b) != 0 {
		t.Fatalf("visibility: A admin sees %d, B admin sees %d", seen(adminA, a), seen(adminB, b))
	}
	// the summary groups by provider/model/task and runs in the caller's session
	if err := l.Record(e.ctx, aiusage.Record{TenantID: a, Provider: "gemini", Model: "m", Task: "description", CostUSD: 0.5, Success: false, InputTokens: 7, OutputTokens: 3}); err != nil {
		t.Fatal(err)
	}
	_ = asUser(adminA, func(ctx context.Context) error {
		s, err := l.Summarize(ctx, a, time.Now())
		if err != nil || len(s.Lines) != 1 || s.Lines[0].Calls != 2 || s.Lines[0].Failures != 1 || s.Lines[0].CostUSD != 1.5 || s.Lines[0].InputTokens != 7 {
			t.Errorf("summary: %+v %v", s, err)
		}
		return nil
	})
	// a malformed record is refused by the database, not silently stored
	if err := l.Record(e.ctx, aiusage.Record{TenantID: a, Provider: "", Task: "x"}); err == nil {
		t.Error("an empty provider must be refused")
	}
	if err := l.Record(e.ctx, aiusage.Record{TenantID: uuid.New(), Provider: "gemini", Task: "x"}); err == nil {
		t.Error("an unknown tenant must be refused")
	}
	// negative token counts are clamped, never stored
	if err := l.Record(e.ctx, aiusage.Record{TenantID: a, Provider: "gemini", Task: "neg", InputTokens: -5, OutputTokens: -1, Success: true}); err != nil {
		t.Fatal(err)
	}
}

func TestMonthStartIsUTC(t *testing.T) {
	got := aiusage.MonthStart(time.Date(2026, 10, 31, 23, 30, 0, 0, time.FixedZone("x", -3*3600)))
	if !got.Equal(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)) {
		// 23:30 at UTC-3 is already 02:30 UTC on Nov 1st: the budget period is UTC, not local
		t.Fatalf("MonthStart = %v", got)
	}
}
