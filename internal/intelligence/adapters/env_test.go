package adapters

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

// Real Postgres, runtime role under FORCE RLS. Tenant A is the caller; Tenant B owns data that must stay untouched.

type env struct {
	t    *testing.T
	ctx  context.Context
	seed *pgxpool.Pool
	app  *pgxpool.Pool
}

func newEnv(t *testing.T) *env {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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

func (e *env) count(sql string, args ...any) (n int) {
	e.t.Helper()
	if err := e.seed.QueryRow(e.ctx, sql, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return n
}

type tenantFixture struct {
	id           uuid.UUID
	contact      uuid.UUID
	conversation uuid.UUID
}

func (e *env) tenant() tenantFixture {
	f := tenantFixture{id: uuid.New(), contact: uuid.New(), conversation: uuid.New()}
	e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, f.id, f.id.String())
	e.t.Cleanup(func() {
		bg := context.Background()
		for _, q := range []string{
			`DELETE FROM tickets WHERE tenant_id=$1`, `DELETE FROM messages WHERE tenant_id=$1`,
			`DELETE FROM topic_threads WHERE tenant_id=$1`, `DELETE FROM conversations WHERE tenant_id=$1`,
			`DELETE FROM contacts WHERE tenant_id=$1`, `DELETE FROM roles WHERE tenant_id=$1`, `DELETE FROM tenants WHERE id=$1`,
		} {
			_, _ = e.seed.Exec(bg, q, f.id)
		}
	})
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164,status) VALUES($1,$2,'C',$3,'active')`, f.contact, f.id, fmt.Sprintf("+55119%08d", rand.Intn(100000000)))
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, f.conversation, f.id, f.contact)
	return f
}

// conversationFor adds another conversation (another contact) to the tenant, optionally assigned to a user.
func (e *env) conversationFor(tenant uuid.UUID, assignee *uuid.UUID) uuid.UUID {
	contact, conv := uuid.New(), uuid.New()
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164,status) VALUES($1,$2,'C2',$3,'active')`, contact, tenant, fmt.Sprintf("+55119%08d", rand.Intn(100000000)))
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status,assigned_to_user_id) VALUES($1,$2,$3,'open',$4)`, conv, tenant, contact, assignee)
	return conv
}

func (e *env) message(tenant, conversation uuid.UUID, body string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES($1,$2,$3,'inbound','text',$4,'received')`, id, tenant, conversation, body)
	return id
}

func (e *env) ticket(tenant, conversation uuid.UUID, status string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO tickets(id,tenant_id,conversation_id,status,subject) VALUES($1,$2,$3,$4,'assunto')`, id, tenant, conversation, status)
	return id
}

func (e *env) member(tenant uuid.UUID, role string) uuid.UUID {
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

// readOnlyMember has only tenant.read (no topic permissions) through a tenant-specific role.
func (e *env) readOnlyMember(tenant uuid.UUID) uuid.UUID {
	u, role := uuid.New(), uuid.New()
	e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	e.exec(`INSERT INTO roles(id,tenant_id,key,name) VALUES($1,$2,'viewer_no_topics','Viewer')`, role, tenant)
	e.exec(`INSERT INTO role_permissions(role_id,permission_key) VALUES($1,'tenant.read')`, role)
	e.exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenant, u, role)
	return u
}

// session runs fn inside a real tenant session of the runtime role (RLS applies) with a trusted tenant context.
func (e *env) session(tenant, user uuid.UUID, fn func(ctx context.Context)) {
	e.t.Helper()
	err := platformdb.WithTenantSession(e.ctx, e.app, user, false, func(sessionCtx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		fn(tenancydomain.WithTenantContext(sessionCtx, tc))
		return nil
	})
	if err != nil {
		e.t.Fatalf("session: %v", err)
	}
}

func (e *env) handler() *TopicHandler {
	repo := NewPostgresTopicRepository(e.app)
	return NewTopicHandler(e.app, application.NewTopicService(repo), repo)
}

func (e *env) call(tenant, user uuid.UUID, method, body string, path map[string]string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := httptest.NewRecorder()
	// A statement error inside a request aborts its transaction (the response is already written), so use the tolerant session.
	e.attempt(tenant, user, func(ctx context.Context) {
		req := httptest.NewRequest(method, "/", bytes.NewReader([]byte(body))).WithContext(ctx)
		for k, v := range path {
			req.SetPathValue(k, v)
		}
		fn(rec, req)
	})
	return rec
}

// attempt runs fn in its own session and tolerates the rollback that follows a statement error: in production every
// request is one transaction, so a failed statement correctly aborts that request.
func (e *env) attempt(tenant, user uuid.UUID, fn func(ctx context.Context)) {
	e.t.Helper()
	_ = platformdb.WithTenantSession(e.ctx, e.app, user, false, func(sessionCtx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		fn(tenancydomain.WithTenantContext(sessionCtx, tc))
		return nil
	})
}

// --- group fixtures (ADR-0015 keeps group messages apart from conversations) ---

type groupFixture struct{ conn, group uuid.UUID }

func (e *env) group(tenant uuid.UUID) groupFixture {
	g := groupFixture{conn: uuid.New(), group: uuid.New()}
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
	        VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','{}')`, g.conn, tenant, "wa-"+g.conn.String())
	e.t.Cleanup(func() {
		bg := context.Background()
		_, _ = e.seed.Exec(bg, `DELETE FROM conversation_topic_focus WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM wa_groups WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM channel_participants WHERE tenant_id=$1`, tenant)
		_, _ = e.seed.Exec(bg, `DELETE FROM channel_connections WHERE tenant_id=$1`, tenant)
	})
	e.exec(`INSERT INTO wa_groups(id,tenant_id,channel_connection_id,provider_group_id,name,enabled) VALUES($1,$2,$3,'120363000000000001@g.us','Grupo',true)`, g.group, tenant, g.conn)
	return g
}

func (e *env) participant(tenant uuid.UUID, g groupFixture, external, name string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO channel_participants(id,tenant_id,channel_connection_id,provider,external_participant_id,display_name) VALUES($1,$2,$3,'waha',$4,$5)`, id, tenant, g.conn, external, name)
	return id
}

var groupSeq int

func (e *env) groupMessage(tenant uuid.UUID, g groupFixture, author uuid.UUID, body string, replyTo *uuid.UUID) uuid.UUID {
	groupSeq++
	id := uuid.New()
	e.exec(`INSERT INTO wa_group_messages(id,tenant_id,group_id,provider_message_id,author_jid,author_name,message_type,body,sent_at,sender_channel_participant_id,reply_to_group_message_id)
	        VALUES($1,$2,$3,$4,'a@lid','x','text',$5,now(),$6,$7)`, id, tenant, g.group, fmt.Sprintf("gm-%d-%s", groupSeq, id), body, author, replyTo)
	return id
}
