package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/testhelpers"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	inboxapp "github.com/omnira/omnira/internal/inbox/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func TestPostgresInboundStoreIsTenantSafeAndIdempotent(t *testing.T) {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
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
	var roleID uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL LIMIT 1`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	tenantA, tenantB, userA := uuid.New(), uuid.New(), uuid.New()
	if _, err := seed.Exec(ctx, `INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, userA, userA, userA.String()+"@invalid"); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range []uuid.UUID{tenantA, tenantB} {
		if _, err := seed.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenant, tenant.String()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := seed.Exec(ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantA, userA, roleID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id IN ($1,$2)`, tenantA, tenantB)
		_, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userA)
	})
	connectionID := uuid.New()
	queueID := uuid.New()
	if _, err := seed.Exec(ctx, `INSERT INTO queues(id,tenant_id,name,mode,is_default) VALUES($1,$2,'Default','round_robin',true)`, queueID, tenantA); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(ctx, `INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active')`, connectionID, tenantA, connectionID.String()); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresInboundStore(app)
	svc := inboxapp.NewInboundService(store, store, store, TicketStore{store}, store)
	connection := channeldomain.ChannelConnection{ID: connectionID, TenantID: tenantA}
	inbound := channeldomain.InboundMessage{ConnectionID: connectionID.String(), ProviderMessageID: "provider-message-1", FromE164: "+5511999999999", Text: "oi"}
	if err := platformdb.WithTenantSession(ctx, app, userA, false, func(sc context.Context) error {
		tc, e := tenancydomain.NewTenantContext(tenantA, userA, tenancydomain.AccessSourceDirect)
		if e != nil {
			return e
		}
		_, e = svc.Ingest(tenancydomain.WithTenantContext(sc, tc), connection, inbound)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err := platformdb.WithTenantSession(ctx, app, userA, false, func(sc context.Context) error {
		tc, e := tenancydomain.NewTenantContext(tenantA, userA, tenancydomain.AccessSourceDirect)
		if e != nil {
			return e
		}
		result, e := svc.Ingest(tenancydomain.WithTenantContext(sc, tc), connection, inbound)
		if e != nil {
			return e
		}
		if !result.Duplicate {
			t.Fatal("redelivery created a second message")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var messages, tickets int
	if err := seed.QueryRow(ctx, `SELECT count(*) FROM messages WHERE tenant_id=$1`, tenantA).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := seed.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE tenant_id=$1`, tenantA).Scan(&tickets); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || tickets != 1 {
		t.Fatalf("got messages=%d tickets=%d", messages, tickets)
	}
	var routedQueue uuid.UUID
	var routingJobs int
	if err := seed.QueryRow(ctx, `SELECT queue_id FROM conversations WHERE tenant_id=$1 LIMIT 1`, tenantA).Scan(&routedQueue); err != nil {
		t.Fatal(err)
	}
	if routedQueue != queueID {
		t.Fatalf("initial queue=%s want=%s", routedQueue, queueID)
	}
	if err := seed.QueryRow(ctx, `SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type='job.routing.assign.v1'`, tenantA).Scan(&routingJobs); err != nil {
		t.Fatal(err)
	}
	if routingJobs != 1 {
		t.Fatalf("routing jobs=%d", routingJobs)
	}
	intake := NewWebhookIntake(app, channeladapters.NewPostgresWebhookEventStore(app), svc)
	webhookMessage := inbound
	webhookMessage.ProviderMessageID = "provider-message-2"
	duplicate, err := intake.ProcessWebhook(ctx, connection, webhookMessage.ProviderMessageID, "message.any", "digest", &webhookMessage, nil)
	if err != nil || duplicate {
		t.Fatalf("system webhook intake failed: duplicate=%v err=%v", duplicate, err)
	}
	duplicate, err = intake.ProcessWebhook(ctx, connection, webhookMessage.ProviderMessageID, "message.any", "digest", &webhookMessage, nil)
	if err != nil || !duplicate {
		t.Fatalf("system webhook redelivery failed: duplicate=%v err=%v", duplicate, err)
	}
	rollbackMessage := inbound
	rollbackMessage.ProviderMessageID = "provider-message-rollback"
	rollbackMessage.Text = ""
	if _, err := intake.ProcessWebhook(ctx, connection, rollbackMessage.ProviderMessageID, "message.any", "digest", &rollbackMessage, nil); err == nil {
		t.Fatal("invalid message did not abort webhook transaction")
	}
	rollbackMessage.Text = "retry válido"
	duplicate, err = intake.ProcessWebhook(ctx, connection, rollbackMessage.ProviderMessageID, "message.any", "digest", &rollbackMessage, nil)
	if err != nil || duplicate {
		t.Fatalf("rolled-back reservation blocked retry: duplicate=%v err=%v", duplicate, err)
	}
	if _, err := seed.Exec(ctx, `
		INSERT INTO messages(tenant_id,conversation_id,channel_connection_id,direction,message_type,body,provider_message_id,status)
		SELECT tenant_id,id,$2,'outbound','text','resposta','provider-out-1','sent'
		FROM conversations WHERE tenant_id=$1 ORDER BY created_at LIMIT 1`, tenantA, connectionID); err != nil {
		t.Fatal(err)
	}
	status := &channeldomain.DeliveryStatusUpdate{ProviderMessageID: "provider-out-1", State: channeldomain.DeliveryStateDelivered}
	if duplicate, err := intake.ProcessWebhook(ctx, connection, "ack-delivered", "message.ack", "digest", nil, status); err != nil || duplicate {
		t.Fatalf("delivery status intake failed: duplicate=%v err=%v", duplicate, err)
	}
	status.State = channeldomain.DeliveryStateQueued
	if _, err := intake.ProcessWebhook(ctx, connection, "ack-stale", "message.ack", "digest", nil, status); err != nil {
		t.Fatal(err)
	}
	var persistedStatus string
	if err := seed.QueryRow(ctx, `SELECT status FROM messages WHERE tenant_id=$1 AND provider_message_id='provider-out-1'`, tenantA).Scan(&persistedStatus); err != nil {
		t.Fatal(err)
	}
	if persistedStatus != "delivered" {
		t.Fatalf("stale ack downgraded status to %s", persistedStatus)
	}
	if err := platformdb.WithTenantSession(ctx, app, userA, false, func(sc context.Context) error {
		tc, e := tenancydomain.NewTenantContext(tenantB, userA, tenancydomain.AccessSourceDirect)
		if e != nil {
			return e
		}
		_, e = svc.Ingest(tenancydomain.WithTenantContext(sc, tc), connection, inbound)
		if e == nil {
			t.Fatal("tenant B accepted tenant A connection")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// O nome de perfil do remetente batiza um contato que ainda não tem nome, mas
// não pode sobrescrever um nome curado pelo operador — caso contrário quem
// envia escolheria como aparece na inbox alheia (ex.: "Banco Oficial").
func TestInboundContactNameFillsPlaceholderButNeverOverwritesCuratedName(t *testing.T) {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
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

	var roleID uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL LIMIT 1`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	tenant, user := uuid.New(), uuid.New()
	if _, err := seed.Exec(ctx, `INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, user, user, user.String()+"@invalid"); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(ctx, `INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenant, tenant.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenant, user, roleID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, tenant)
		_, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user)
	})
	connectionID, queueID := uuid.New(), uuid.New()
	if _, err := seed.Exec(ctx, `INSERT INTO queues(id,tenant_id,name,mode,is_default) VALUES($1,$2,'Default','manual',true)`, queueID, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(ctx, `INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active')`, connectionID, tenant, connectionID.String()); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresInboundStore(app)
	svc := inboxapp.NewInboundService(store, store, store, TicketStore{store}, store)
	connection := channeldomain.ChannelConnection{ID: connectionID, TenantID: tenant}
	const phone = "+5511988887777"
	ingest := func(providerID, senderName string) {
		t.Helper()
		if err := platformdb.WithTenantSession(ctx, app, user, false, func(sc context.Context) error {
			tc, e := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
			if e != nil {
				return e
			}
			_, e = svc.Ingest(tenancydomain.WithTenantContext(sc, tc), connection, channeldomain.InboundMessage{
				ConnectionID: connectionID.String(), ProviderMessageID: providerID,
				FromE164: phone, SenderName: senderName, Text: "oi",
			})
			return e
		}); err != nil {
			t.Fatal(err)
		}
	}
	name := func() string {
		t.Helper()
		var got string
		if err := seed.QueryRow(ctx, `SELECT display_name FROM contacts WHERE tenant_id=$1 AND phone_e164=$2`, tenant, phone).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	// Sem nome de perfil, o contato nasce com o telefone como placeholder.
	ingest("m-1", "")
	if got := name(); got != phone {
		t.Fatalf("placeholder esperado %q, veio %q", phone, got)
	}

	// Chegando um nome de perfil, o placeholder é substituído.
	ingest("m-2", "K3G Solutions")
	if got := name(); got != "K3G Solutions" {
		t.Fatalf("nome de perfil não aplicado ao placeholder: %q", got)
	}

	// Operador renomeia; a partir daí o remetente não manda mais no rótulo.
	if _, err := seed.Exec(ctx, `UPDATE contacts SET display_name=$3 WHERE tenant_id=$1 AND phone_e164=$2`, tenant, phone, "Cliente VIP"); err != nil {
		t.Fatal(err)
	}
	ingest("m-3", "Banco Oficial Suporte")
	if got := name(); got != "Cliente VIP" {
		t.Fatalf("nome curado foi sobrescrito pelo remetente: %q", got)
	}
}
