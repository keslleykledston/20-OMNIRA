package adapters

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// conversation seeds a minimal contact + conversation so AlertChannelForConversation has a real tenant to
// resolve from (ADR-0058: it must never trust a caller-supplied tenant id).
func (e *env) conversation(tenant uuid.UUID, conn uuid.UUID) uuid.UUID {
	e.t.Helper()
	contact := uuid.New()
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'Alice','+5511999999999')`, contact, tenant)
	conv := uuid.New()
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,channel_connection_id) VALUES($1,$2,$3,$4)`, conv, tenant, contact, conn)
	return conv
}

func TestAlertChannelForConversationFindsTheConfiguredGroup(t *testing.T) {
	e := newEnv(t)
	tenant, conn := e.tenant()
	gid := e.group(tenant, conn, jid1, "Canal interno", true)
	e.exec(`UPDATE wa_groups SET is_alert_channel=true, purpose='internal_management' WHERE id=$1`, gid)
	conv := e.conversation(tenant, conn.ID)

	repo := NewPostgresAlertChannelRepository(e.app)
	ch, err := repo.AlertChannelForConversation(context.Background(), conv)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if ch == nil || ch.GroupID != gid || ch.ConnectionID != conn.ID || ch.ProviderGroupID != jid1 {
		t.Fatalf("channel = %+v", ch)
	}
}

func TestAlertChannelForConversationReturnsNilWhenNoneConfigured(t *testing.T) {
	e := newEnv(t)
	tenant, conn := e.tenant()
	e.group(tenant, conn, jid1, "Grupo comum", true) // not the alert channel
	conv := e.conversation(tenant, conn.ID)

	repo := NewPostgresAlertChannelRepository(e.app)
	ch, err := repo.AlertChannelForConversation(context.Background(), conv)
	if err != nil || ch != nil {
		t.Fatalf("channel = %+v, err = %v, want nil, nil", ch, err)
	}
}

func TestAlertChannelForConversationReturnsNilForAnUnknownConversation(t *testing.T) {
	e := newEnv(t)
	repo := NewPostgresAlertChannelRepository(e.app)
	ch, err := repo.AlertChannelForConversation(context.Background(), uuid.New())
	if err != nil || ch != nil {
		t.Fatalf("channel = %+v, err = %v, want nil, nil", ch, err)
	}
}

// Tenant B's conversation must never resolve tenant A's alert channel group — the lookup is always scoped
// by the conversation's OWN, database-resolved tenant, never by anything the caller passes in.
func TestAlertChannelForConversationNeverCrossesTenants(t *testing.T) {
	e := newEnv(t)
	tenantA, connA := e.tenant()
	gidA := e.group(tenantA, connA, jid1, "Canal A", true)
	e.exec(`UPDATE wa_groups SET is_alert_channel=true, purpose='internal_management' WHERE id=$1`, gidA)

	tenantB, connB := e.tenant()
	convB := e.conversation(tenantB, connB.ID)

	repo := NewPostgresAlertChannelRepository(e.app)
	ch, err := repo.AlertChannelForConversation(context.Background(), convB)
	if err != nil || ch != nil {
		t.Fatalf("B's conversation resolved A's channel: %+v, err = %v", ch, err)
	}
}

// ADR-0038: a suspended tenant's conversations never trigger anything, including an alert send.
func TestAlertChannelForConversationIsSilentForASuspendedTenant(t *testing.T) {
	e := newEnv(t)
	tenant, conn := e.tenant()
	gid := e.group(tenant, conn, jid1, "Canal interno", true)
	e.exec(`UPDATE wa_groups SET is_alert_channel=true, purpose='internal_management' WHERE id=$1`, gid)
	conv := e.conversation(tenant, conn.ID)
	e.exec(`UPDATE tenants SET status='suspended' WHERE id=$1`, tenant)

	repo := NewPostgresAlertChannelRepository(e.app)
	ch, err := repo.AlertChannelForConversation(context.Background(), conv)
	if err != nil || ch != nil {
		t.Fatalf("channel = %+v, err = %v, want nil, nil (tenant suspended)", ch, err)
	}
}
