package application

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
)

// readHarness reuses the exact same fakes as create_external_ticket_test.go
// (same package) — no duplicate test doubles.
type readHarness struct {
	perms        *fakePerms
	conversation *fakeConversation
	localTickets *fakeLocalTickets
	svc          *ReadConversationTicketService
	tenantID     uuid.UUID
	actorID      uuid.UUID
	convID       uuid.UUID
}

func newReadHarness(assignedTo *uuid.UUID) *readHarness {
	h := &readHarness{
		perms:        &fakePerms{granted: map[string]bool{}},
		conversation: &fakeConversation{found: true, assignedTo: assignedTo},
		localTickets: &fakeLocalTickets{},
		tenantID:     uuid.New(), actorID: uuid.New(), convID: uuid.New(),
	}
	h.svc = NewReadConversationTicketService(h.perms, h.conversation, h.localTickets)
	return h
}

func (h *readHarness) cmd() ReadConversationTicketCommand {
	return ReadConversationTicketCommand{TenantID: h.tenantID, ConversationID: h.convID, ActorUserID: h.actorID}
}

// A. own assigned conversation can read.
func TestReadConversationTicketOwnAssignmentAllowed(t *testing.T) {
	h := newReadHarness(nil)
	h.conversation.assignedTo = &h.actorID
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: uuid.New()}
	res, err := h.svc.ReadConversationTicket(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Linked {
		t.Fatalf("result = %+v, want linked=false", res)
	}
}

// B. conversation.manage can read another agent's conversation.
func TestReadConversationTicketManagePermissionAllowed(t *testing.T) {
	h := newReadHarness(nil)
	other := uuid.New()
	h.conversation.assignedTo = &other
	h.perms.granted[PermissionConversationManage] = true
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: uuid.New()}
	_, err := h.svc.ReadConversationTicket(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// C. another agent without manage is denied.
func TestReadConversationTicketOtherAssignmentWithoutManageDenied(t *testing.T) {
	h := newReadHarness(nil)
	other := uuid.New()
	h.conversation.assignedTo = &other
	_, err := h.svc.ReadConversationTicket(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if !errors.Is(err, ErrNotAssignedToYou) {
		t.Fatalf("err = %v, want ErrNotAssignedToYou", err)
	}
}

// D. no active local ticket -> not found.
func TestReadConversationTicketNoActiveTicketNotFound(t *testing.T) {
	h := newReadHarness(nil)
	h.conversation.assignedTo = &h.actorID
	h.localTickets.candidate = nil
	_, err := h.svc.ReadConversationTicket(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if !errors.Is(err, ErrNoActiveTicket) {
		t.Fatalf("err = %v, want ErrNoActiveTicket", err)
	}
}

// E. active local ticket without external link: linked=false.
func TestReadConversationTicketUnlinkedTicket(t *testing.T) {
	h := newReadHarness(nil)
	h.conversation.assignedTo = &h.actorID
	ticketID := uuid.New()
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: ticketID}
	res, err := h.svc.ReadConversationTicket(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Linked || res.LocalTicketID != ticketID {
		t.Fatalf("result = %+v, want linked=false/%s", res, ticketID)
	}
}

// F. linked active ticket: exact fields.
func TestReadConversationTicketLinkedTicketExactFields(t *testing.T) {
	h := newReadHarness(nil)
	h.conversation.assignedTo = &h.actorID
	provider, externalID, status, label, sync := "k3g", "28182", "1", "Novo", "synced"
	ticketID := uuid.New()
	h.localTickets.candidate = &ticketsdomain.Ticket{
		ID: ticketID, Provider: &provider, ExternalTicketID: &externalID,
		ExternalStatus: &status, ExternalStatusLabel: &label, SyncStatus: &sync,
	}
	res, err := h.svc.ReadConversationTicket(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Linked || res.LocalTicketID != ticketID || res.Provider != provider || res.ExternalTicketID != externalID ||
		res.ExternalStatus != status || res.ExternalStatusLabel != label || res.SyncStatus != sync {
		t.Fatalf("unexpected result: %+v", res)
	}
}

// G. inconsistent linkage: fail closed.
func TestReadConversationTicketInconsistentLinkageFailsClosed(t *testing.T) {
	externalID := "28182"
	cases := []struct {
		name string
		set  func(*ticketsdomain.Ticket)
	}{
		{"external_ticket_id without provider", func(tk *ticketsdomain.Ticket) { tk.ExternalTicketID = &externalID }},
		{"provider without external_ticket_id", func(tk *ticketsdomain.Ticket) { p := "k3g"; tk.Provider = &p }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newReadHarness(nil)
			h.conversation.assignedTo = &h.actorID
			ticket := &ticketsdomain.Ticket{ID: uuid.New()}
			c.set(ticket)
			h.localTickets.candidate = ticket
			_, err := h.svc.ReadConversationTicket(withTenantContext(h.tenantID, h.actorID), h.cmd())
			if !errors.Is(err, ErrInconsistentExternalLink) {
				t.Fatalf("err = %v, want ErrInconsistentExternalLink", err)
			}
		})
	}
}

// H. no provider/runtime dependency is invoked — structural proof:
// ReadConversationTicketService has no field of type
// ports.TicketingRuntimeResolver/connectors.TicketingConnector at all, so
// no such call is even possible from this code path. Runtime evidence:
// the fakes above never expose a TicketingConnector/runtime resolver, and
// every test above passes without configuring one.
func TestReadConversationTicketNeverUsesProviderRuntime(t *testing.T) {
	h := newReadHarness(nil)
	h.conversation.assignedTo = &h.actorID
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: uuid.New()}
	// NewReadConversationTicketService's signature itself
	// (perms, conversation, localTickets) has no runtime/connector
	// parameter to pass one into — this call would not compile otherwise.
	if _, err := h.svc.ReadConversationTicket(withTenantContext(h.tenantID, h.actorID), h.cmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
