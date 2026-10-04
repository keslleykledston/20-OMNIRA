package adapters_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	messagedomain "github.com/omnira/omnira/internal/messages/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func TestWahaMessageIDTail(t *testing.T) {
	for in, want := range map[string]string{
		"true_559291740090@c.us_3EB07524D10313F92F67E4":                 "3EB07524D10313F92F67E4",
		"true_175222334484588@lid_3EB07524D10313F92F67E4":               "3EB07524D10313F92F67E4",
		"false_120363000000000001@g.us_3EB0AAAA_5511999@s.whatsapp.net": "3EB0AAAA",
		"3EB07524D10313F92F67E4":                                        "",
		"true_only":                                                     "",
		"maybe_559291740090@c.us_3EB0":                                  "",
		"true_559291740090@c.us_":                                       "",
		"":                                                              "",
	} {
		if got := inboxadapters.WahaMessageIDTail(in); got != want {
			t.Errorf("WahaMessageIDTail(%q) = %q, want %q", in, got, want)
		}
	}
}

// A receipt can name the chat differently from the send answer (phone vs @lid) while carrying the same
// message id. It must still land on the message that was sent - and on nothing else.
func TestDeliveryReceiptsMatchByFullIDOrReservedIDTailWithoutCrossingTenants(t *testing.T) {
	e := newKindEnv(t)
	tenantA, tenantB := e.tenant(), e.tenant()
	user := e.member(tenantA, "tenant_agent")
	connA, connA2, connB := uuid.New(), uuid.New(), uuid.New()
	for conn, tn := range map[uuid.UUID]uuid.UUID{connA: tenantA, connA2: tenantA, connB: tenantB} {
		e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]')`, conn, tn, conn.String())
	}
	conv := e.conversation(tenantA, "Cliente", "customer", "+5592955550001")
	convB := e.conversation(tenantB, "Outro", "customer", "+5592955550002")

	const reserved = "3EB07524D10313F92F67E4"
	msg := func(tenant, convID, conn uuid.UUID, direction, providerID, reservedID, status string) uuid.UUID {
		id := uuid.New()
		e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,channel_connection_id,direction,message_type,body,status,provider_message_id,reserved_provider_message_id) VALUES($1,$2,$3,$4,$5,'text','x',$6,$7,$8)`,
			id, tenant, convID, conn, direction, status, providerID, reservedID)
		return id
	}
	sentAsPhone := msg(tenantA, conv, connA, "outbound", "true_559291740090@c.us_"+reserved, reserved, "sent")
	legacy := msg(tenantA, conv, connA, "outbound", "true_175222334484588@lid_3EB0LEGACY00000001", "", "sent")
	otherMsg := msg(tenantA, conv, connA, "outbound", "true_559291740090@c.us_3EB0OTHER000000000001", "3EB0OTHER000000000001", "sent")
	otherConn := msg(tenantA, conv, connA2, "outbound", "true_559291740090@c.us_"+reserved+"X", reserved+"X", "sent")
	inbound := msg(tenantA, conv, connA, "inbound", "true_559291740090@c.us_"+reserved+"I", "", "received")
	foreign := msg(tenantB, convB, connB, "outbound", "true_559291740090@c.us_"+reserved, reserved, "sent")

	store := inboxadapters.NewPostgresInboundStore(e.app)
	apply := func(tenant uuid.UUID, conn uuid.UUID, id string, status messagedomain.Status) bool {
		t.Helper()
		var ok bool
		err := platformdb.WithSystemTenantSession(e.ctx, e.app, tenant, func(ctx context.Context) error {
			tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
			if err != nil {
				return err
			}
			ok, err = store.ApplyDeliveryStatus(tenancydomain.WithTenantContext(ctx, tc), conn, id, status)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	status := func(id uuid.UUID) string {
		var s string
		if err := e.seed.QueryRow(e.ctx, `SELECT status FROM messages WHERE id=$1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// The receipt names the contact by @lid, the send answer named it by phone: same id tail.
	if !apply(tenantA, connA, "true_175222334484588@lid_"+reserved, messagedomain.StatusDelivered) || status(sentAsPhone) != "delivered" {
		t.Fatalf("a receipt with the same id tail must update the sent message, got %q", status(sentAsPhone))
	}
	// Forward only: a late "delivered" never undoes "read", and a repeat is harmless.
	if !apply(tenantA, connA, "true_175222334484588@lid_"+reserved, messagedomain.StatusRead) || status(sentAsPhone) != "read" {
		t.Fatalf("read = %q", status(sentAsPhone))
	}
	if apply(tenantA, connA, "true_175222334484588@lid_"+reserved, messagedomain.StatusDelivered) || status(sentAsPhone) != "read" {
		t.Fatalf("a late delivered receipt went backwards: %q", status(sentAsPhone))
	}
	// Messages sent before id reservation (no reserved id) still match by their full id.
	if !apply(tenantA, connA, "true_175222334484588@lid_3EB0LEGACY00000001", messagedomain.StatusRead) || status(legacy) != "read" {
		t.Fatalf("legacy full-id match: %q", status(legacy))
	}
	// Nothing else moved: another message, another connection, an inbound message, and the OTHER tenant's
	// message that happens to share the very same id.
	for name, id := range map[string]uuid.UUID{"another message": otherMsg, "another connection": otherConn} {
		if status(id) != "sent" {
			t.Errorf("%s changed to %q", name, status(id))
		}
	}
	if status(inbound) != "received" {
		t.Errorf("an inbound message was touched: %q", status(inbound))
	}
	if status(foreign) != "sent" {
		t.Fatalf("tenant B's message with the same id changed to %q: receipts must never cross tenants", status(foreign))
	}
	// A receipt for an id nobody sent matches nothing.
	if apply(tenantA, connA, "true_559291740090@c.us_3EB0UNKNOWN000000001", messagedomain.StatusRead) {
		t.Fatal("an unknown id must not match")
	}
	// A bare id or an unrelated shape matches only by full value.
	if apply(tenantA, connA, reserved, messagedomain.StatusRead) {
		t.Fatal("a bare id is not the full provider id")
	}
	// The same tenant but the wrong connection does not match.
	if apply(tenantA, connA2, "true_175222334484588@lid_"+reserved, messagedomain.StatusRead) {
		t.Fatal("a receipt on the wrong connection matched")
	}
}
