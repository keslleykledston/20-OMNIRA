package adapters_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/messages/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

func TestBotMenuIsQueuedAsButtonsOnMetaAndAsPlainTextOnWaha(t *testing.T) {
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
	tenant := uuid.New()
	exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenant, tenant.String())
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM outbox_events WHERE tenant_id=$1`, tenant)
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, tenant)
	})
	conv := func(provider, kind string) uuid.UUID {
		connID, contact, c := uuid.New(), uuid.New(), uuid.New()
		exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
			VALUES($1,$2,'whatsapp',$3,$4,$5,'active','["text"]')`, connID, tenant, provider, kind, "ch-"+connID.String())
		exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164,kind) VALUES($1,$2,'C',$3,'other')`, contact, tenant, fmt.Sprintf("+559299%08d", contact.ID()%100000000))
		exec(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id,status) VALUES($1,$2,$3,$4,'open')`, c, tenant, contact, connID)
		exec(`INSERT INTO messages(id,tenant_id,conversation_id,channel_connection_id,direction,message_type,body,status,created_at,updated_at)
			VALUES($1,$2,$3,$4,'inbound','text','oi','received', now(), now())`, uuid.New(), tenant, c, connID)
		return c
	}
	meta, waha := conv("meta_cloud", "official"), conv("waha", "unofficial")
	sender := messagesapplication.NewSystemSender(messagesadapters.NewPostgresOutboundStore(app))
	send := func(c uuid.UUID, key string, titles ...string) messagesapplication.SystemSendStatus {
		var st messagesapplication.SystemSendStatus
		err := platformdb.WithSystemTenantSession(ctx, app, tenant, func(sc context.Context) error {
			tc, e := tenancydomain.NewTenantContext(tenant, uuid.Nil, tenancydomain.AccessSourceSystem)
			if e != nil {
				return e
			}
			opts := make([]ports.InteractiveOption, len(titles))
			text := "Como ajudar?"
			for i, ti := range titles {
				opts[i] = ports.InteractiveOption{ID: "o" + string(rune('a'+i)), Title: ti}
				text += "\n" + string(rune('1'+i)) + ") " + ti
			}
			var se error
			st, se = sender.SendChoice(tenancydomain.WithTenantContext(sc, tc), c, text, "Como ajudar?", opts, key)
			return se
		})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	count := func(c uuid.UUID) (msgs, itx int) {
		_ = seed.QueryRow(ctx, `SELECT count(*) FROM messages WHERE conversation_id=$1 AND direction='outbound'`, c).Scan(&msgs)
		_ = seed.QueryRow(ctx, `SELECT count(*) FROM message_interactive_sends i JOIN messages m ON m.id=i.message_id AND m.tenant_id=i.tenant_id WHERE m.conversation_id=$1`, c).Scan(&itx)
		return
	}
	if st := send(meta, "menu-key-0001", "Suporte", "Financeiro", "Comercial", "Outro assunto"); st != messagesapplication.SystemQueued {
		t.Fatalf("meta menu: %s", st)
	}
	if m, i := count(meta); m != 1 || i != 1 {
		t.Fatalf("a Meta menu is ONE message with an interactive record: msgs=%d interactive=%d", m, i)
	}
	var opts string
	_ = seed.QueryRow(ctx, `SELECT i.options::text FROM message_interactive_sends i JOIN messages m ON m.id=i.message_id AND m.tenant_id=i.tenant_id WHERE m.conversation_id=$1`, meta).Scan(&opts)
	if opts == "" {
		t.Fatal("options not stored")
	}
	// the same key again is a replay, not a second menu
	if st := send(meta, "menu-key-0001", "Suporte", "Financeiro", "Comercial", "Outro assunto"); st != messagesapplication.SystemReplayed {
		t.Fatalf("replay: %s", st)
	}
	if m, _ := count(meta); m != 1 {
		t.Fatalf("replay duplicated: %d", m)
	}
	// WAHA has no buttons: the numbered text only, no interactive record
	if st := send(waha, "menu-key-0002", "Suporte", "Financeiro"); st != messagesapplication.SystemQueued {
		t.Fatalf("waha menu: %s", st)
	}
	if m, i := count(waha); m != 1 || i != 0 {
		t.Fatalf("waha: msgs=%d interactive=%d, want 1/0", m, i)
	}
	// a title that does not fit the 20-character button limit stays a numbered text menu on Meta too
	metaLong := conv("meta_cloud", "official")
	if st := send(metaLong, "menu-key-0003", "Um título longo demais aqui", "B"); st != messagesapplication.SystemQueued {
		t.Fatalf("long title: %s", st)
	}
	if _, i := count(metaLong); i != 0 {
		t.Fatalf("a menu that does not fit must be plain text, interactive=%d", i)
	}
}
