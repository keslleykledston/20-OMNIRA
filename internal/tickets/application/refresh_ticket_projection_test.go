package application

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
	"github.com/omnira/omnira/internal/tickets/ports"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// refreshHarness reuses the exact same fakes as create_external_ticket_test.go
// (same package) — no duplicate test doubles.
type refreshHarness struct {
	perms        *fakePerms
	conversation *fakeConversation
	localTickets *fakeLocalTickets
	ticketing    *fakeTicketing
	runtime      *fakeRuntimeResolver
	svc          *RefreshTicketProjectionService
	tenantID     uuid.UUID
	actorID      uuid.UUID
	convID       uuid.UUID
}

const (
	refreshProvider   = "k3g"
	refreshExternalID = "28182"
)

func newRefreshHarness() *refreshHarness {
	linkedTicket := &ticketsdomain.Ticket{ID: uuid.New()}
	provider, externalID := refreshProvider, refreshExternalID
	linkedTicket.Provider, linkedTicket.ExternalTicketID = &provider, &externalID

	h := &refreshHarness{
		perms:        &fakePerms{granted: map[string]bool{PermissionTicketCreate: true}},
		conversation: &fakeConversation{found: true},
		localTickets: &fakeLocalTickets{candidate: linkedTicket},
		ticketing: &fakeTicketing{
			name:      refreshProvider,
			getResult: &connectors.ExternalTicket{ExternalID: refreshExternalID, ExternalStatus: "2", ExternalStatusLabel: "Em atendimento"},
		},
		tenantID: uuid.New(), actorID: uuid.New(), convID: uuid.New(),
	}
	h.runtime = &fakeRuntimeResolver{ticketing: h.ticketing}
	h.svc = NewRefreshTicketProjectionService(h.perms, h.conversation, h.localTickets, h.runtime)
	return h
}

func (h *refreshHarness) cmd() RefreshTicketProjectionCommand {
	return RefreshTicketProjectionCommand{TenantID: h.tenantID, ConversationID: h.convID, ActorUserID: h.actorID}
}

// ---- authorization ---------------------------------------------------------

func TestRefreshTicketProjectionOwnAssignmentAllowed(t *testing.T) {
	h := newRefreshHarness()
	h.conversation.assignedTo = &h.actorID
	res, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Linked {
		t.Fatalf("result = %+v, want linked=true", res)
	}
}

func TestRefreshTicketProjectionManagePermissionAllowed(t *testing.T) {
	h := newRefreshHarness()
	other := uuid.New()
	h.conversation.assignedTo = &other
	h.perms.granted[PermissionConversationManage] = true
	_, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRefreshTicketProjectionOtherAssignmentWithoutManageDenied(t *testing.T) {
	h := newRefreshHarness()
	other := uuid.New()
	h.conversation.assignedTo = &other
	_, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if !errors.Is(err, ErrNotAssignedToYou) {
		t.Fatalf("err = %v, want ErrNotAssignedToYou", err)
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.getCalls)
	}
}

func TestRefreshTicketProjectionRejectsMissingTicketCreate(t *testing.T) {
	h := newRefreshHarness()
	h.conversation.assignedTo = &h.actorID
	h.perms.granted[PermissionTicketCreate] = false
	_, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.getCalls)
	}
}

// A. no active local ticket -> no provider call.
func TestRefreshTicketProjectionNoActiveTicketNeverCallsProvider(t *testing.T) {
	h := newRefreshHarness()
	h.conversation.assignedTo = &h.actorID
	h.localTickets.candidate = nil
	_, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if !errors.Is(err, ErrNoActiveTicket) {
		t.Fatalf("err = %v, want ErrNoActiveTicket", err)
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.getCalls)
	}
	if h.runtime.calls != 0 {
		t.Fatalf("runtime resolver must not be called, got %d", h.runtime.calls)
	}
}

// B. active local ticket but linked=false -> no provider call.
func TestRefreshTicketProjectionUnlinkedTicketNeverCallsProvider(t *testing.T) {
	h := newRefreshHarness()
	h.conversation.assignedTo = &h.actorID
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: uuid.New()}
	_, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if !errors.Is(err, ErrTicketNotLinked) {
		t.Fatalf("err = %v, want ErrTicketNotLinked", err)
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.getCalls)
	}
	if h.runtime.calls != 0 {
		t.Fatalf("runtime resolver must not be called, got %d", h.runtime.calls)
	}
}

// C. inconsistent provider/external ID -> fail closed, no provider call.
func TestRefreshTicketProjectionInconsistentLinkageFailsClosed(t *testing.T) {
	externalID := refreshExternalID
	cases := []struct {
		name string
		set  func(*ticketsdomain.Ticket)
	}{
		{"external_ticket_id without provider", func(tk *ticketsdomain.Ticket) { tk.ExternalTicketID = &externalID }},
		{"provider without external_ticket_id", func(tk *ticketsdomain.Ticket) { p := refreshProvider; tk.Provider = &p }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newRefreshHarness()
			h.conversation.assignedTo = &h.actorID
			ticket := &ticketsdomain.Ticket{ID: uuid.New()}
			c.set(ticket)
			h.localTickets.candidate = ticket
			_, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
			if !errors.Is(err, ErrInconsistentExternalLink) {
				t.Fatalf("err = %v, want ErrInconsistentExternalLink", err)
			}
			if h.ticketing.getCalls != 0 {
				t.Fatalf("provider must not be called, got %d", h.ticketing.getCalls)
			}
		})
	}
}

// D. resolved provider != linked provider -> fail closed, GetTicket=0.
func TestRefreshTicketProjectionProviderMismatchNeverCallsGetTicket(t *testing.T) {
	h := newRefreshHarness()
	h.conversation.assignedTo = &h.actorID
	h.ticketing.name = "a-different-provider"
	_, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if !errors.Is(err, ErrProviderMismatch) {
		t.Fatalf("err = %v, want ErrProviderMismatch", err)
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("GetTicket must not be called, got %d", h.ticketing.getCalls)
	}
}

// E. successful refresh: GetTicket=1, status/label updated, synced timestamp.
func TestRefreshTicketProjectionSuccessUpdatesProjection(t *testing.T) {
	h := newRefreshHarness()
	h.conversation.assignedTo = &h.actorID
	res, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.ticketing.getCalls != 1 {
		t.Fatalf("GetTicket called %d times, want exactly 1", h.ticketing.getCalls)
	}
	if res.ExternalStatus != "2" || res.ExternalStatusLabel != "Em atendimento" {
		t.Fatalf("result = %+v, want ExternalStatus=2/Em atendimento", res)
	}
	if res.SyncStatus != "synced" || res.LastSyncedAt == nil {
		t.Fatalf("result = %+v, want SyncStatus=synced and a non-nil LastSyncedAt", res)
	}
	if h.localTickets.gotExternalStatus != "2" || h.localTickets.gotExternalStatusLabel != "Em atendimento" {
		t.Fatalf("projected external_status=%q external_status_label=%q, want 2/Em atendimento",
			h.localTickets.gotExternalStatus, h.localTickets.gotExternalStatusLabel)
	}
	if h.localTickets.gotProvider != refreshProvider || h.localTickets.gotExternalTicketID != refreshExternalID {
		t.Fatalf("projected provider=%q external_ticket_id=%q, want %s/%s",
			h.localTickets.gotProvider, h.localTickets.gotExternalTicketID, refreshProvider, refreshExternalID)
	}
	// Never a create/update/close call: fakeTicketing only exposes
	// GetTicket/CreateTicket — CreateTicket must remain uncalled (item J).
	if h.ticketing.calls != 0 {
		t.Fatalf("CreateTicket called %d times, want 0", h.ticketing.calls)
	}
}

// F. returned external ID mismatch -> projection not updated.
func TestRefreshTicketProjectionExternalIDMismatchNeverUpdatesProjection(t *testing.T) {
	h := newRefreshHarness()
	h.conversation.assignedTo = &h.actorID
	h.ticketing.getResult = &connectors.ExternalTicket{ExternalID: "99999", ExternalStatus: "2", ExternalStatusLabel: "Em atendimento"}
	_, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if !errors.Is(err, ErrExternalIDMismatch) {
		t.Fatalf("err = %v, want ErrExternalIDMismatch", err)
	}
	if h.localTickets.enrichCalls != 0 {
		t.Fatalf("EnrichExternalProjection called %d times, want 0 (mismatch must not reach projection)", h.localTickets.enrichCalls)
	}
}

// G. provider unavailable -> projection not falsely marked synced.
func TestRefreshTicketProjectionProviderUnavailableNeverMarksSynced(t *testing.T) {
	h := newRefreshHarness()
	h.conversation.assignedTo = &h.actorID
	h.ticketing.getErr = &connectors.TicketingError{Code: connectors.TicketingProviderUnavailable, Message: "down"}
	_, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}
	if h.localTickets.enrichCalls != 0 {
		t.Fatalf("EnrichExternalProjection called %d times, want 0 (never falsely marked synced)", h.localTickets.enrichCalls)
	}
}

// H. NOT_FOUND -> link preserved, no CREATE (structurally: fakeTicketing has
// no create-triggering path from this service at all — see item J).
func TestRefreshTicketProjectionNotFoundPreservesLinkNoCreate(t *testing.T) {
	h := newRefreshHarness()
	h.conversation.assignedTo = &h.actorID
	h.ticketing.getErr = &connectors.TicketingError{Code: connectors.TicketingNotFound, Message: "not found"}
	_, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if !errors.Is(err, ErrExternalTicketNotFound) {
		t.Fatalf("err = %v, want ErrExternalTicketNotFound", err)
	}
	if h.localTickets.enrichCalls != 0 {
		t.Fatalf("EnrichExternalProjection called %d times, want 0 (link preserved, untouched)", h.localTickets.enrichCalls)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("CreateTicket called %d times, want 0", h.ticketing.calls)
	}
}

// I. NOT_MIGRATED -> link preserved, no CREATE.
func TestRefreshTicketProjectionNotMigratedPreservesLinkNoCreate(t *testing.T) {
	h := newRefreshHarness()
	h.conversation.assignedTo = &h.actorID
	h.ticketing.getErr = &connectors.TicketingError{Code: connectors.TicketingNotMigrated, Message: "not migrated"}
	_, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
	if !errors.Is(err, ErrExternalTicketNotFound) {
		t.Fatalf("err = %v, want ErrExternalTicketNotFound", err)
	}
	if h.localTickets.enrichCalls != 0 {
		t.Fatalf("EnrichExternalProjection called %d times, want 0 (link preserved, untouched)", h.localTickets.enrichCalls)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("CreateTicket called %d times, want 0", h.ticketing.calls)
	}
}

// J. CreateTicket calls always 0 across every branch of this service —
// structural proof: RefreshTicketProjectionService never holds a code path
// that invokes TicketingConnector.CreateTicket at all (grep-verifiable:
// refresh_ticket_projection.go calls GetTicket exactly once and nothing
// else on the connector). This test exercises the success path, the
// branch most likely to tempt a future "create if missing" shortcut.
func TestRefreshTicketProjectionNeverCallsCreateTicket(t *testing.T) {
	h := newRefreshHarness()
	h.conversation.assignedTo = &h.actorID
	if _, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.ticketing.calls != 0 {
		t.Fatalf("CreateTicket called %d times, want 0", h.ticketing.calls)
	}
}

// PRODUCT.6-L: runtime resolution failure must propagate before any
// provider call, resolved fresh per call.
func TestRefreshTicketProjectionPropagatesRuntimeResolutionFailure(t *testing.T) {
	h := newRefreshHarness()
	h.conversation.assignedTo = &h.actorID
	h.runtime.err = &ports.ResolutionError{Code: ports.ResolutionNoConfiguration, Message: "no K3G connection configured"}
	_, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd())
	var resErr *ports.ResolutionError
	if !errors.As(err, &resErr) || resErr.Code != ports.ResolutionNoConfiguration {
		t.Fatalf("err = %v, want *ports.ResolutionError{Code: NO_CONFIGURATION}", err)
	}
	if h.ticketing.getCalls != 0 {
		t.Fatalf("provider must not be called, got %d", h.ticketing.getCalls)
	}
	if h.runtime.calls != 1 {
		t.Fatalf("runtime resolver called %d times, want 1", h.runtime.calls)
	}
}

// Never uses tenant-wide ticket.read — structural proof via the exact
// permission constant checked (PermissionTicketCreate), mirroring
// TestReadConversationTicketNeverUsesProviderRuntime's style.
func TestRefreshTicketProjectionNeverChecksTicketRead(t *testing.T) {
	h := newRefreshHarness()
	h.conversation.assignedTo = &h.actorID
	// Only ticket.create is granted; if the service checked a different
	// permission key (e.g. "ticket.read") it would be denied here.
	h.perms.granted = map[string]bool{PermissionTicketCreate: true}
	if _, err := h.svc.RefreshTicketProjection(withTenantContext(h.tenantID, h.actorID), h.cmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
