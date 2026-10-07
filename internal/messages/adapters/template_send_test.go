package adapters_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

func TestSendTemplateIsAtomicTenantScopedAndOutsideTheWindow(t *testing.T) {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx := context.Background()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := seed.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	a, b := uuid.New(), uuid.New()
	for _, tn := range []uuid.UUID{a, b} {
		exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String())
	}
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM outbox_events WHERE tenant_id IN ($1,$2)`, a, b)
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1,$2)`, a, b)
	})
	agent := uuid.New()
	exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, agent, agent, agent.String()+"@invalid")
	t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, agent) })
	var role uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, a, agent, role)

	conn := func(tn uuid.UUID) uuid.UUID {
		id := uuid.New()
		exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
			VALUES($1,$2,'whatsapp','meta_cloud','official',$3,'active','["text","template"]')`, id, tn, "tpl-"+id.String())
		return id
	}
	connA, connB := conn(a), conn(b)
	contact, convID := uuid.New(), uuid.New()
	exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164,kind) VALUES($1,$2,'C','+5592966660123','other')`, contact, a)
	exec(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status,assigned_to_user_id) VALUES($1,$2,$3,$4,'open',$5)`, convID, a, contact, connA, agent)
	// the contact wrote 3 days ago: the 24 h window is closed
	exec(`INSERT INTO messages(id,tenant_id,conversation_id,channel_connection_id,direction,message_type,body,status,created_at,updated_at)
		VALUES($1,$2,$3,$4,'inbound','text','oi','received', now() - interval '3 days', now() - interval '3 days')`, uuid.New(), a, convID, connA)
	tpl := func(tn, c uuid.UUID, status string) uuid.UUID {
		id := uuid.New()
		exec(`INSERT INTO channel_message_templates(id,tenant_id,connection_id,name,language,category,status,body_text,variable_count,sendable)
			VALUES($1,$2,$3,'boas_vindas','pt_BR','UTILITY',$4,'Olá {{1}}, chamado {{2}}.',2,true)`, id, tn, c, status)
		return id
	}
	good, pending, foreign := tpl(a, connA, "APPROVED"), uuid.Nil, tpl(b, connB, "APPROVED")
	exec(`INSERT INTO channel_message_templates(id,tenant_id,connection_id,name,language,category,status,body_text,variable_count,sendable)
		VALUES($1,$2,$3,'pendente','pt_BR','UTILITY','PENDING','x',0,true)`, uuid.New(), a, connA)
	_ = pending

	sender := messagesapplication.NewSender(messagesadapters.NewPostgresOutboundStore(app), channeladapters.NewPostgresPermissionChecker(app))
	send := func(tid uuid.UUID, params []string, key string) (messagesapplication.SendResult, error) {
		var res messagesapplication.SendResult
		err := platformdb.WithTenantSession(ctx, app, agent, false, func(sc context.Context) error {
			tc, e := tenancydomain.NewTenantContext(a, agent, tenancydomain.AccessSourceDirect)
			if e != nil {
				return e
			}
			var se error
			res, se = sender.SendTemplate(tenancydomain.WithTenantContext(sc, tc), convID, tid, params, key)
			return se
		})
		return res, err
	}

	res, err := send(good, []string{"Ana", "123"}, "tplkey-0001")
	if err != nil {
		t.Fatalf("a template must go out although the window is closed: %v", err)
	}
	var name string
	var params string
	if err := seed.QueryRow(ctx, `SELECT template_name, params::text FROM message_template_sends WHERE tenant_id=$1 AND message_id=$2`, a, res.Message.ID).Scan(&name, &params); err != nil || name != "boas_vindas" || params != `["Ana", "123"]` {
		t.Fatalf("template record: %q %q %v", name, params, err)
	}
	var body string
	var jobs int
	_ = seed.QueryRow(ctx, `SELECT body FROM messages WHERE id=$1`, res.Message.ID).Scan(&body)
	_ = seed.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND aggregate_id=$2`, a, res.Message.ID.String()).Scan(&jobs)
	if body != "Olá Ana, chamado 123." || jobs != 1 {
		t.Fatalf("body=%q jobs=%d", body, jobs)
	}
	// same key + same request is a replay, not a second message
	again, err := send(good, []string{"Ana", "123"}, "tplkey-0001")
	if err != nil || !again.Replayed || again.Message.ID != res.Message.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	// a template of ANOTHER tenant's line is refused and nothing is written
	if _, err := send(foreign, []string{"Ana", "123"}, "tplkey-0002"); !errors.Is(err, messagesapplication.ErrTemplateNotAllowed) {
		t.Fatalf("foreign template: %v", err)
	}
	var n int
	_ = seed.QueryRow(ctx, `SELECT count(*) FROM message_template_sends WHERE tenant_id=$1`, a).Scan(&n)
	if n != 1 {
		t.Fatalf("template records = %d, want 1", n)
	}
	// free text is still refused in the same conversation: the template did not open the window
	if _, err := send(good, []string{"Ana"}, "tplkey-0003"); !errors.Is(err, messagesapplication.ErrTemplateParams) {
		t.Fatalf("wrong variable count: %v", err)
	}
}
