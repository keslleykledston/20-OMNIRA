package application

import (
	"context"
	"testing"

	"github.com/google/uuid"
	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	messagedomain "github.com/omnira/omnira/internal/messages/domain"
)

type gateStub struct {
	hold      bool
	engaged   []uuid.UUID
	inbound   []gateCall
	onInbound func()
}

type gateCall struct {
	conversation, message uuid.UUID
	newConversation       bool
}

func (g *gateStub) Engage(_ context.Context, id uuid.UUID) bool {
	g.engaged = append(g.engaged, id)
	return g.hold
}
func (g *gateStub) OnInbound(_ context.Context, conv, msg uuid.UUID, isNew bool) {
	g.inbound = append(g.inbound, gateCall{conv, msg, isNew})
}

// distinctMessages stores every message it is given (the shared fake keeps a single one and reports the next as a duplicate),
// except a repeated provider id, which it reports as the duplicate it is.
type distinctMessages struct {
	*memoryStores
	seen map[string]bool
}

func (d *distinctMessages) StoreInbound(ctx context.Context, m *messagedomain.Message) (*messagedomain.Message, bool, error) {
	if d.seen == nil {
		d.seen = map[string]bool{}
	}
	if d.seen[m.ProviderMessageID] {
		return m, true, nil
	}
	d.seen[m.ProviderMessageID] = true
	return m, false, nil
}

func flowIngest(t *testing.T, g *gateStub) (*memoryStores, *InboundService) {
	stores := &memoryStores{}
	svc := NewInboundService(stores, stores, &distinctMessages{memoryStores: stores}, ticketAdapter{stores}, stores)
	if g != nil {
		svc.WithFlows(g)
	}
	return stores, svc
}

func ingestOne(t *testing.T, svc *InboundService, tenantID, connectionID uuid.UUID, providerID string) *InboundResult {
	t.Helper()
	res, err := svc.Ingest(inboundContext(t, tenantID), channeldomain.ChannelConnection{ID: connectionID, TenantID: tenantID},
		channeldomain.InboundMessage{ConnectionID: connectionID.String(), ProviderMessageID: providerID, FromE164: "+5511999999999", Text: "oi"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A flow that takes the conversation replaces the default routing for that new conversation only.
func TestIngestSkipsDefaultRoutingWhenAFlowHoldsTheConversation(t *testing.T) {
	g := &gateStub{hold: true}
	stores, svc := flowIngest(t, g)
	tenant, conn := uuid.New(), uuid.New()
	first := ingestOne(t, svc, tenant, conn, "wamid-1")
	if stores.routeCalls != 0 {
		t.Fatalf("a held conversation must not be routed by default: %d", stores.routeCalls)
	}
	if len(g.engaged) != 1 || g.engaged[0] != first.Conversation.ID {
		t.Fatalf("engage must be asked once for the new conversation: %v", g.engaged)
	}
	if len(g.inbound) != 1 || !g.inbound[0].newConversation || g.inbound[0].message != first.Message.ID || g.inbound[0].conversation != first.Conversation.ID {
		t.Fatalf("first message must be reported as the start of a new conversation: %+v", g.inbound)
	}
	// The next message of the same conversation: no engage, reported as a follow-up.
	second := ingestOne(t, svc, tenant, conn, "wamid-2")
	if len(g.engaged) != 1 || len(g.inbound) != 2 || g.inbound[1].newConversation || g.inbound[1].message != second.Message.ID {
		t.Fatalf("follow-up: engaged=%v inbound=%+v", g.engaged, g.inbound)
	}
}

func TestIngestRoutesAsUsualWhenNoFlowTakesIt(t *testing.T) {
	g := &gateStub{hold: false}
	stores, svc := flowIngest(t, g)
	ingestOne(t, svc, uuid.New(), uuid.New(), "wamid-1")
	if stores.routeCalls != 1 {
		t.Fatalf("without a flow the legacy routing must run exactly as before: %d", stores.routeCalls)
	}
}

func TestIngestRedeliveryNeverNotifiesTheGateTwice(t *testing.T) {
	g := &gateStub{hold: true}
	_, svc := flowIngest(t, g)
	tenant, conn := uuid.New(), uuid.New()
	ingestOne(t, svc, tenant, conn, "wamid-1")
	got := ingestOne(t, svc, tenant, conn, "wamid-1") // the provider redelivers the same message
	if !got.Duplicate || len(g.inbound) != 1 || len(g.engaged) != 1 {
		t.Fatalf("a duplicate must not reach the flows: dup=%v engaged=%d inbound=%d", got.Duplicate, len(g.engaged), len(g.inbound))
	}
}

func TestIngestWithoutAGateIsUnchanged(t *testing.T) {
	stores, svc := flowIngest(t, nil)
	res := ingestOne(t, svc, uuid.New(), uuid.New(), "wamid-1")
	if stores.routeCalls != 1 || res.Ticket == nil {
		t.Fatalf("no gate => exactly the previous behaviour: routes=%d ticket=%v", stores.routeCalls, res.Ticket)
	}
}
