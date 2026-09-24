// PRODUCT.6-O1: the conversation-scoped, LOCAL-ONLY ticket read. It never
// calls TicketingRuntimeResolver or TicketingConnector — this is a pure
// projection read, always available regardless of provider health
// (PRODUCT.6-O0 section 2: READ STRATEGY = LOCAL + EXPLICIT REFRESH; this
// slice implements only the LOCAL half). Provider refresh is a separate,
// later capability (PRODUCT.6-O1R).
package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/tickets/ports"
)

var (
	// ErrNoActiveTicket: the conversation has no active (open/in_progress/
	// waiting) local ticket at all — distinct from ErrNoLocalTicketToEnrich
	// (PRODUCT.6-K2's create-path name for the identical "nothing to act
	// on" condition); kept separate so read and create failures never share
	// an identifier that would couple their HTTP mappings by accident.
	ErrNoActiveTicket = errors.New("tickets: conversation has no active ticket")
	// ErrInconsistentExternalLink: exactly one of provider/external_ticket_id
	// is set on the active local ticket. A data-integrity state, never
	// interpreted as "safe to offer CREATE" (linked=false) or "safe to
	// treat as linked" (linked=true) — fail closed instead.
	ErrInconsistentExternalLink = errors.New("tickets: active ticket has inconsistent external linkage")
)

// ReadConversationTicketCommand identifies exactly which conversation's
// ticket to read, scoped to the authenticated tenant/actor — never a
// tenant-wide ticket.read query (PRODUCT.6-O0 section 3/PRODUCT.6-F:
// ticket.read stays reserved for the tenant-wide list/export surface).
type ReadConversationTicketCommand struct {
	TenantID       uuid.UUID
	ConversationID uuid.UUID
	ActorUserID    uuid.UUID
}

// TicketReadResult is the provider-neutral response shape. Linked=false
// means the active local ticket exists but has no ERP link yet (safe to
// offer CREATE); Linked=true means it does (CREATE must not be offered
// again for this conversation).
type TicketReadResult struct {
	LocalTicketID       uuid.UUID
	Linked              bool
	Provider            string
	ExternalTicketID    string
	ExternalStatus      string
	ExternalStatusLabel string
	SyncStatus          string
	LastSyncedAt        *time.Time
}

// ReadConversationTicketService implements PRODUCT.6-O1. It depends only
// on ports.ConversationAuthorizer/PermissionChecker/LocalTicketStore —
// never a TicketingRuntimeResolver, never a TicketingConnector.
type ReadConversationTicketService struct {
	perms        ports.PermissionChecker
	conversation ports.ConversationAuthorizer
	localTickets ports.LocalTicketStore
}

func NewReadConversationTicketService(perms ports.PermissionChecker, conversation ports.ConversationAuthorizer, localTickets ports.LocalTicketStore) *ReadConversationTicketService {
	return &ReadConversationTicketService{perms: perms, conversation: conversation, localTickets: localTickets}
}

// ReadConversationTicket implements PRODUCT.6-O1 sections 2-4: authorize by
// conversation ownership (never tenant-wide ticket.read — the exact
// pattern already established by CreateExternalTicket/messages.Sender),
// then read the local projection only.
func (s *ReadConversationTicketService) ReadConversationTicket(ctx context.Context, cmd ReadConversationTicketCommand) (*TicketReadResult, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.TenantID != cmd.TenantID || tc.ActorID != cmd.ActorUserID || tc.Source != tenancydomain.AccessSourceDirect {
		return nil, ErrForbidden
	}

	assignedTo, found, err := s.conversation.LoadAssignment(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrConversationNotFound
	}
	if assignedTo == nil {
		return nil, ErrUnassigned
	}
	if *assignedTo != cmd.ActorUserID {
		canManage, err := s.perms.HasPermission(ctx, cmd.ActorUserID, PermissionConversationManage)
		if err != nil {
			return nil, err
		}
		if !canManage {
			return nil, ErrNotAssignedToYou
		}
	}

	ticket, err := s.localTickets.FindActiveByConversation(ctx, cmd.ConversationID)
	if err != nil {
		return nil, err
	}
	if ticket == nil {
		return nil, ErrNoActiveTicket
	}

	hasProvider := ticket.Provider != nil && strings.TrimSpace(*ticket.Provider) != ""
	hasExternalID := ticket.ExternalTicketID != nil && strings.TrimSpace(*ticket.ExternalTicketID) != ""
	if hasProvider != hasExternalID {
		return nil, ErrInconsistentExternalLink
	}
	if !hasProvider {
		return &TicketReadResult{LocalTicketID: ticket.ID, Linked: false}, nil
	}
	return &TicketReadResult{
		LocalTicketID:       ticket.ID,
		Linked:              true,
		Provider:            *ticket.Provider,
		ExternalTicketID:    *ticket.ExternalTicketID,
		ExternalStatus:      derefOr(ticket.ExternalStatus, ""),
		ExternalStatusLabel: derefOr(ticket.ExternalStatusLabel, ""),
		SyncStatus:          derefOr(ticket.SyncStatus, ""),
		LastSyncedAt:        ticket.LastSyncedAt,
	}, nil
}
