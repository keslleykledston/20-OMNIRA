package adapters_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	"github.com/omnira/omnira/internal/messages/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/testhelpers"
)

// ADR-0048 H5: the statement that queues an application's message proves, by itself, that THIS installation holds the open conversation at THIS version.
func TestInsertQueuedExternalProvesTheHoldTheVersionAndTheOpenConversation(t *testing.T) {
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
	tn, other := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{tn, other} {
		exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, id, id.String())
	}
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM outbox_events WHERE tenant_id IN ($1,$2)`, tn, other)
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1,$2)`, tn, other)
	})
	inst, rival := uuid.New(), uuid.New()
	for _, id := range []uuid.UUID{inst, rival} {
		exec(`INSERT INTO external_app_installations(id,tenant_id,name,scopes) VALUES($1,$2,$3,ARRAY['message.send'])`, id, tn, "CRM "+id.String()[:8])
	}
	conn, contact, conv := uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`, conn, tn, "n-"+conn.String())
	exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164,kind) VALUES($1,$2,'C','+5592966660124','other')`, contact, tn)
	exec(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status) VALUES($1,$2,$3,$4,'open')`, conv, tn, contact, conn)
	// the application holds it at version 4 (the owner role may write the typed columns directly; the application role goes through the control service)
	// (the guard wants the version to advance by exactly one at each change of holder: claim, release, claim again ...)
	exec(`UPDATE conversations SET control_kind='crm_application', control_holder_id=$2, control_version=1, control_since=now(), control_origin='crm' WHERE id=$1`, conv, inst)
	exec(`UPDATE conversations SET control_kind=NULL, control_holder_id=NULL, control_since=NULL, control_origin=NULL, control_version=2 WHERE id=$1`, conv)
	exec(`UPDATE conversations SET control_kind='crm_application', control_holder_id=$2, control_version=3, control_since=now(), control_origin='crm' WHERE id=$1`, conv, inst)
	exec(`UPDATE conversations SET control_kind=NULL, control_holder_id=NULL, control_since=NULL, control_origin=NULL, control_version=4 WHERE id=$1`, conv)
	exec(`UPDATE conversations SET control_kind='crm_application', control_holder_id=$2, control_version=5, control_since=now(), control_origin='crm' WHERE id=$1`, conv, inst)

	store := messagesadapters.NewPostgresOutboundStore(app)
	sc := ports.SendContext{ConversationID: conv, ConnectionID: &conn}
	try := func(holder uuid.UUID, version int64) (*ports.QueuedMessage, error) {
		var m *ports.QueuedMessage
		err := platformdb.WithSystemTenantSession(ctx, app, tn, func(ctx context.Context) error {
			var err error
			m, err = store.InsertQueuedExternal(ctx, holder, version, sc, "oi", "hash")
			return err
		})
		return m, err
	}
	count := func() int {
		var n int
		_ = seed.QueryRow(ctx, `SELECT count(*) FROM messages WHERE conversation_id=$1`, conv).Scan(&n)
		return n
	}
	for name, c := range map[string]struct {
		holder  uuid.UUID
		version int64
	}{"another installation": {rival, 5}, "an older version": {inst, 4}, "a newer version": {inst, 6}} {
		if _, err := try(c.holder, c.version); !errors.Is(err, ports.ErrConversationChanged) || count() != 0 {
			t.Errorf("%s: err=%v messages=%d, want ErrConversationChanged and no message", name, err, count())
		}
	}
	m, err := try(inst, 5)
	if err != nil || m == nil || count() != 1 {
		t.Fatalf("the holder at the right version: %v messages=%d", err, count())
	}
	var byInst *uuid.UUID
	var byUser *uuid.UUID
	var key *string
	if err := seed.QueryRow(ctx, `SELECT sent_by_installation_id, sent_by_user_id, idempotency_key FROM messages WHERE id=$1`, m.ID).Scan(&byInst, &byUser, &key); err != nil || byInst == nil || *byInst != inst || byUser != nil || key != nil {
		t.Errorf("sender columns: %v %v %v (%v)", byInst, byUser, key, err)
	}
	// a closed conversation takes nothing, even from the holder at the right version
	exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, conv)
	var v int64
	_ = seed.QueryRow(ctx, `SELECT control_version FROM conversations WHERE id=$1`, conv).Scan(&v)
	if _, err := try(inst, v); !errors.Is(err, ports.ErrConversationChanged) || count() != 1 {
		t.Errorf("closed conversation: err=%v messages=%d", err, count())
	}
	// another company's session sees nothing of it
	if err := platformdb.WithSystemTenantSession(ctx, app, other, func(ctx context.Context) error {
		_, err := store.InsertQueuedExternal(ctx, inst, v, sc, "oi", "hash")
		return err
	}); !errors.Is(err, ports.ErrConversationChanged) {
		t.Errorf("another company: %v", err)
	}
}

// The statement that queues a template re-proves the template, and the one that queues free text re-proves the provider's 24 h window.
func TestInsertQueuedExternalReprovesTheTemplateAndTheWindow(t *testing.T) {
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
	tn := uuid.New()
	exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String())
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM outbox_events WHERE tenant_id=$1`, tn)
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, tn)
	})
	inst := uuid.New()
	exec(`INSERT INTO external_app_installations(id,tenant_id,name,scopes) VALUES($1,$2,$3,ARRAY['message.send'])`, inst, tn, "CRM "+inst.String()[:8])
	conn, contact, conv, tpl := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','meta_cloud','official',$3,'active','["text","template"]')`, conn, tn, "m-"+conn.String())
	exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164,kind) VALUES($1,$2,'C','+5592966660125','other')`, contact, tn)
	exec(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status) VALUES($1,$2,$3,$4,'open')`, conv, tn, contact, conn)
	exec(`UPDATE conversations SET control_kind='crm_application', control_holder_id=$2, control_version=1, control_since=now(), control_origin='crm' WHERE id=$1`, conv, inst)
	exec(`INSERT INTO channel_message_templates(id,tenant_id,connection_id,name,language,category,status,body_text,variable_count,sendable) VALUES($1,$2,$3,'n','pt_BR','UTILITY','APPROVED','Olá',0,true)`, tpl, tn, conn)
	// the contact wrote 25 hours ago: the window is closed
	exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status,created_at) VALUES(gen_random_uuid(),$1,$2,'inbound','text','oi','received', now() - interval '25 hours')`, tn, conv)

	store := messagesadapters.NewPostgresOutboundStore(app)
	sc := ports.SendContext{ConversationID: conv, ConnectionID: &conn, Provider: "meta_cloud"}
	run := func(fn func(ctx context.Context) error) error {
		return platformdb.WithSystemTenantSession(ctx, app, tn, fn)
	}
	count := func() int {
		var n int
		_ = seed.QueryRow(ctx, `SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, conv).Scan(&n)
		return n
	}
	// free text outside the window: refused by the statement itself, nothing queued
	err = run(func(ctx context.Context) error {
		_, err := store.InsertQueuedExternal(ctx, inst, 1, sc, "oi", "h")
		return err
	})
	if !errors.Is(err, ports.ErrConversationChanged) || count() != 0 {
		t.Fatalf("free text outside the window: err=%v messages=%d", err, count())
	}
	// the same line, a template: allowed outside the window
	good := ports.TemplateSend{Name: "n", Language: "pt_BR", Params: []string{}, ID: tpl, ConnectionID: conn}
	if err := run(func(ctx context.Context) error {
		_, err := store.InsertQueuedExternalTemplate(ctx, inst, 1, sc, "Olá", "h", good)
		return err
	}); err != nil || count() != 1 {
		t.Fatalf("approved template outside the window: err=%v messages=%d", err, count())
	}
	// it stopped being approved after it was read
	exec(`UPDATE channel_message_templates SET status='PAUSED' WHERE id=$1`, tpl)
	err = run(func(ctx context.Context) error {
		_, err := store.InsertQueuedExternalTemplate(ctx, inst, 1, sc, "Olá", "h", good)
		return err
	})
	if !errors.Is(err, ports.ErrTemplateGone) || count() != 1 {
		t.Fatalf("template no longer approved: err=%v messages=%d", err, count())
	}
	exec(`UPDATE channel_message_templates SET status='APPROVED', sendable=false WHERE id=$1`, tpl)
	err = run(func(ctx context.Context) error {
		_, err := store.InsertQueuedExternalTemplate(ctx, inst, 1, sc, "Olá", "h", good)
		return err
	})
	if !errors.Is(err, ports.ErrTemplateGone) || count() != 1 {
		t.Fatalf("template no longer sendable: err=%v messages=%d", err, count())
	}
	// a template of another line is not this line's
	exec(`UPDATE channel_message_templates SET sendable=true WHERE id=$1`, tpl)
	other := good
	other.ConnectionID = uuid.New()
	err = run(func(ctx context.Context) error {
		_, err := store.InsertQueuedExternalTemplate(ctx, inst, 1, sc, "Olá", "h", other)
		return err
	})
	if !errors.Is(err, ports.ErrTemplateGone) {
		t.Fatalf("template of another line: %v", err)
	}
	// the contact writes again: free text is allowed
	exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES(gen_random_uuid(),$1,$2,'inbound','text','voltei','received')`, tn, conv)
	if err := run(func(ctx context.Context) error {
		_, err := store.InsertQueuedExternal(ctx, inst, 1, sc, "oi", "h")
		return err
	}); err != nil || count() != 2 {
		t.Fatalf("free text inside the window: err=%v messages=%d", err, count())
	}
}
