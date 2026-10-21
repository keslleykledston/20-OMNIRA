package updatemgr_test

// Fixture compartilhado dos testes de updatemgr (F6) — mesmo espírito de internal/hub/adapters'
// `world`: pools reais contra Postgres descartável (nunca omnira_dev), seeds mínimos para simular
// as quatro identidades que a especificação pede testar (operador de plataforma, admin de tenant,
// delegado de Hub, "CRM externo"/API Headless), e helpers finos em volta deles.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
)

type world struct {
	t     *testing.T
	ctx   context.Context
	owner *pgxpool.Pool
	app   *pgxpool.Pool

	roleTenantAdmin uuid.UUID
	roleHubAdmin    uuid.UUID
	roleHubAgent    uuid.UUID
	hub             uuid.UUID
	tenant          uuid.UUID
}

func newWorld(t *testing.T) *world {
	t.Helper()
	ownerURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	w := &world{t: t, ctx: ctx, owner: owner, app: app}
	w.roleTenantAdmin = w.role("tenant_admin")
	w.roleHubAdmin = w.role("hub_admin")
	w.roleHubAgent = w.role("hub_agent")
	w.tenant = uuid.New()
	w.exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, 'World Tenant', 'active')`, w.tenant)
	w.hub = uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'World Hub')`, w.hub)
	return w
}

func (w *world) must(err error) {
	w.t.Helper()
	if err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) exec(sql string, args ...any) {
	w.t.Helper()
	_, err := w.owner.Exec(w.ctx, sql, args...)
	w.must(err)
}

func (w *world) count(sql string, args ...any) int {
	w.t.Helper()
	var n int
	w.must(w.owner.QueryRow(w.ctx, sql, args...).Scan(&n))
	return n
}

func (w *world) role(key string) uuid.UUID {
	w.t.Helper()
	var id uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM roles WHERE tenant_id IS NULL AND key = $1 LIMIT 1`, key).Scan(&id))
	return id
}

func (w *world) user(name string) uuid.UUID {
	w.t.Helper()
	u := uuid.New()
	w.exec(`INSERT INTO users (id, external_subject, email) VALUES ($1, $2, $3)`, u, name+"-"+u.String(), name+"-"+u.String()[:8]+"@example.test")
	return u
}

// operator cria um usuário platform operator ATIVO (ADR-0038) — a única identidade com qualquer
// acesso a este serviço.
func (w *world) operator(name string) uuid.UUID {
	u := w.user(name)
	w.exec(`INSERT INTO platform_operators (user_id, granted_by) VALUES ($1, 'test')`, u)
	return u
}

func (w *world) revoke(operator uuid.UUID) {
	w.exec(`UPDATE platform_operators SET status = 'revoked', revoked_at = now() WHERE user_id = $1`, operator)
}

// resetActiveRuns força um estado terminal em qualquer run "ativo" deixado por OUTRA função de
// teste (platform_update_runs é um espelho GLOBAL, sem installation_id, compartilhado por toda
// função de teste deste pacote na mesma execução de `go test` — nunca recriado por newWorld).
// Só para testes que precisam de uma instalação livre antes de exercitar "só um update ativo
// por instalação" (updatemgr.Service.StartRun); nunca o padrão usado pelo próprio serviço.
func (w *world) resetActiveRuns() {
	w.exec(`UPDATE platform_update_runs SET state = 'SUCCEEDED', updated_at = now()
		WHERE state NOT IN ('SUCCEEDED', 'ROLLED_BACK', 'RECOVERY_REQUIRED', 'BLOCKED', 'CANCELLED', 'DISPATCH_CONFLICT', 'COMPATIBILITY_VOIDED')`)
}

// tenantAdmin cria um usuário com membership tenant_admin do tenant fixture — NÃO é platform
// operator (exatamente a identidade negativa que a especificação pede testar).
func (w *world) tenantAdmin(name string) uuid.UUID {
	u := w.user(name)
	w.exec(`INSERT INTO memberships (tenant_id, user_id, role_id, status) VALUES ($1, $2, $3, 'active')`, w.tenant, u, w.roleTenantAdmin)
	return u
}

// hubDelegate cria um usuário admin do Hub fixture — delegado de Hub, também NÃO é platform
// operator.
func (w *world) hubDelegate(name string) uuid.UUID {
	u := w.user(name)
	w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, w.hub, u, w.roleHubAdmin)
	return u
}
