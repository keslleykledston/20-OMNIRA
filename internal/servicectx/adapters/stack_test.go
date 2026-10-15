package adapters_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/flowstest"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/servicectx/adapters"
	"github.com/omnira/omnira/internal/servicectx/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// stack is a real Postgres (a disposable DB) with two tenants. Tenant A also has an attendant (the stock tenant_agent role), a member whose role holds NO
// key at all (observer), and a member who may read, open and edit but NOT close (limited).
type stack struct {
	t                 *testing.T
	env               *flowstest.Env
	svc               *application.Service
	catalog           *application.Catalog
	handler           *adapters.Handler
	admin, agent      uuid.UUID
	observer, limited uuid.UUID
}

func newStack(t *testing.T) *stack {
	t.Helper()
	env := flowstest.New(t)
	s := &stack{t: t, env: env, admin: env.UserA, agent: uuid.New(), observer: uuid.New(), limited: uuid.New()}
	s.svc, s.catalog, s.handler = adapters.Build(env.App)
	ctx := context.Background()
	var agentRole uuid.UUID
	if err := env.Seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL LIMIT 1`).Scan(&agentRole); err != nil {
		t.Fatal(err)
	}
	observerRole, limitedRole := uuid.New(), uuid.New()
	s.exec(`INSERT INTO roles(id, tenant_id, key, name) VALUES($1,$2,'observer-test','Observer'), ($3,$2,'limited-test','Limited')`, observerRole, env.TenantA, limitedRole)
	for _, k := range []string{"service.read", "service.create", "service.update", "conversation.claim"} {
		s.exec(`INSERT INTO role_permissions(role_id, permission_key) VALUES($1,$2)`, limitedRole, k)
	}
	// the observer holds one unrelated key, so the join on role permissions is not empty: only the missing service.read keeps it out
	s.exec(`INSERT INTO role_permissions(role_id, permission_key) VALUES($1,'tenant.read')`, observerRole)
	for u, role := range map[uuid.UUID]uuid.UUID{s.agent: agentRole, s.observer: observerRole, s.limited: limitedRole} {
		s.exec(`INSERT INTO users(id, external_subject, email, status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
		s.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, env.TenantA, u, role)
	}
	t.Cleanup(func() { // tenants cascade; users are global
		_, _ = env.Seed.Exec(context.Background(), `DELETE FROM users WHERE id IN ($1,$2,$3)`, s.agent, s.observer, s.limited)
	})
	return s
}

func (s *stack) exec(sql string, args ...any) {
	s.t.Helper()
	if _, err := s.env.Seed.Exec(context.Background(), sql, args...); err != nil {
		s.t.Fatalf("%v\n%s", err, sql)
	}
}

func (s *stack) count(sql string, args ...any) (n int) {
	s.t.Helper()
	if err := s.env.Seed.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		s.t.Fatal(err)
	}
	return n
}

func (s *stack) conversation() (conv, contact uuid.UUID) {
	s.t.Helper()
	return s.env.SeedConversation(s.t, s.env.TenantA, "none")
}

// message inserts a message at a precise instant, so tests can place it before, inside or after a session's interval.
func (s *stack) message(conv uuid.UUID, direction string, at time.Time, by *uuid.UUID) uuid.UUID {
	s.t.Helper()
	id := uuid.New()
	status := map[string]string{"inbound": "received", "outbound": "sent"}[direction]
	s.exec(`INSERT INTO messages(id, tenant_id, conversation_id, direction, body, status, created_at, updated_at, sent_by_user_id, provider_message_id)
	        VALUES($1,$2,$3,$4,'oi',$5,$6,$6,$7,$8)`, id, s.env.TenantA, conv, direction, status, at, by, id.String())
	return id
}

func (s *stack) as(user, tenant uuid.UUID, fn func(ctx context.Context)) {
	s.t.Helper()
	s.env.AsUser(s.t, tenant, user, fn)
}

// try runs fn in a tenant session and returns its error: the transaction is rolled back when fn fails (a database refusal aborts it anyway). Safe from goroutines.
func (s *stack) try(user, tenant uuid.UUID, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return platformdb.WithTenantSession(ctx, s.env.App, user, false, func(sc context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		return fn(tenancydomain.WithTenantContext(sc, tc))
	})
}
