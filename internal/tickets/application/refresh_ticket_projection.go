// Package application implements PRODUCT.6-O1R: the EXPLICIT REFRESH half
// of PRODUCT.6-O0's read strategy (LOCAL + EXPLICIT REFRESH). Unlike
// ReadConversationTicketService (PRODUCT.6-O1, pure local projection read,
// always available), this service makes exactly one outbound call —
// TicketingConnector.GetTicket — and writes only provider-owned projection
// metadata back onto the already-linked local ticket. It never creates,
// updates, or closes an external ticket, and it never establishes a link
// that did not already exist.
package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/tickets/ports"
	"github.com/omnira/omnira/internal/tool/connectors"
)

var (
	// ErrTicketNotLinked: the active local ticket exists but has no
	// external link yet (Linked=false). Refresh is a projection freshness
	// operation for an EXISTING link — it is never a path to CREATE one.
	ErrTicketNotLinked = errors.New("tickets: active ticket is not linked to an external ticket, nothing to refresh")
	// ErrProviderMismatch: the tenant's currently resolved TicketingConnector
	// is not the same provider that durably owns this ticket's external
	// link. Never query a different provider than the one that created the
	// link — no silent provider substitution (mirrors createAndRecord's
	// recoverProjection guard).
	ErrProviderMismatch = errors.New("tickets: resolved provider does not match the ticket's linked provider")
	// ErrExternalTicketNotFound: the provider reported NOT_FOUND or
	// NOT_MIGRATED for the linked external_ticket_id. The durable local
	// link is historical evidence and is deliberately preserved — never
	// cleared automatically, never a trigger for CREATE.
	ErrExternalTicketNotFound = errors.New("tickets: provider reports the linked external ticket is not found or not migrated")
	// ErrExternalIDMismatch: the provider's response names a different
	// ExternalID than the durable local link. The local projection is left
	// untouched — never overwrite a durable external identity from a
	// mismatched answer.
	ErrExternalIDMismatch = errors.New("tickets: provider response external id does not match the linked ticket")
	// ErrProviderUnavailable wraps any other GetTicket failure (transport,
	// 5xx, malformed response, unauthorized, validation, unknown). The
	// projection is left exactly as it was — never falsely marked synced.
	ErrProviderUnavailable = errors.New("tickets: ticketing provider unavailable")
)

type RefreshTicketProjectionCommand struct {
	TenantID       uuid.UUID
	ConversationID uuid.UUID
	ActorUserID    uuid.UUID
}

// RefreshTicketProjectionService implements PRODUCT.6-O1R. It reuses the
// same conversation-ownership authorization primitives as
// ReadConversationTicketService and CreateExternalTicket's Service, and the
// same ports.TicketingRuntimeResolver + connectors.TicketingConnector
// boundary as CreateExternalTicket — no new adapter surface.
type RefreshTicketProjectionService struct {
	perms        ports.PermissionChecker
	conversation ports.ConversationAuthorizer
	localTickets ports.LocalTicketStore
	runtime      ports.TicketingRuntimeResolver
}

func NewRefreshTicketProjectionService(perms ports.PermissionChecker, conversation ports.ConversationAuthorizer, localTickets ports.LocalTicketStore, runtime ports.TicketingRuntimeResolver) *RefreshTicketProjectionService {
	return &RefreshTicketProjectionService{perms: perms, conversation: conversation, localTickets: localTickets, runtime: runtime}
}

// RefreshTicketProjection implements PRODUCT.6-O1R sections 2-7 in order:
// authorize (conversation ownership + ticket.create, the same capability
// CreateExternalTicket requires to interact with the provider on this
// ticket's behalf — never tenant-wide ticket.read) -> load the active local
// ticket -> require a CONSISTENT existing link (never linked=false, never
// partial) -> resolve the tenant's ticketing runtime -> require the
// resolved provider to match the ticket's linked provider -> exactly one
// TicketingConnector.GetTicket call -> require the response's ExternalID to
// match the durable local external_ticket_id -> write only provider-owned
// projection metadata.
func (s *RefreshTicketProjectionService) RefreshTicketProjection(ctx context.Context, cmd RefreshTicketProjectionCommand) (*TicketReadResult, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.TenantID != cmd.TenantID || tc.ActorID != cmd.ActorUserID || tc.Source != tenancydomain.AccessSourceDirect {
		return nil, ErrForbidden
	}

	// PRODUCT.6-O1R section 3: reuses ticket.create — the same capability
	// CreateExternalTicket already requires to interact with the provider
	// on this conversation's ticket. Never tenant-wide ticket.read
	// (PRODUCT.6-F: tenant_agent has ticket.create, not ticket.read).
	canRefresh, err := s.perms.HasPermission(ctx, cmd.ActorUserID, PermissionTicketCreate)
	if err != nil {
		return nil, err
	}
	if !canRefresh {
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
		return nil, ErrTicketNotLinked
	}

	// PRODUCT.6-L: resolve THIS tenant's TicketingConnector fresh for this
	// call, after authorization, exactly like CreateExternalTicket.
	rt, err := s.runtime.Resolve(ctx, cmd.TenantID)
	if err != nil {
		return nil, err
	}

	if rt.TicketingConnector.Name() != *ticket.Provider {
		return nil, ErrProviderMismatch
	}

	snapshot, err := rt.TicketingConnector.GetTicket(ctx, *ticket.ExternalTicketID)
	if err != nil {
		code := connectors.TicketingErrorCodeOf(err)
		if code == connectors.TicketingNotFound || code == connectors.TicketingNotMigrated {
			// Section 9: the durable link is historical evidence — never
			// cleared, never a CREATE fallback.
			return nil, ErrExternalTicketNotFound
		}
		// Section 8: PROVIDER_UNAVAILABLE / UNAUTHORIZED / VALIDATION_ERROR /
		// UNKNOWN_PROVIDER_ERROR — the projection is left untouched, never
		// falsely marked synced.
		return nil, ErrProviderUnavailable
	}

	if snapshot.ExternalID != *ticket.ExternalTicketID {
		return nil, ErrExternalIDMismatch
	}

	now := time.Now().UTC()
	if err := s.localTickets.EnrichExternalProjection(ctx, ticket.ID, *ticket.Provider, *ticket.ExternalTicketID, snapshot.ExternalStatus, snapshot.ExternalStatusLabel, now); err != nil {
		return nil, err
	}

	return &TicketReadResult{
		LocalTicketID:       ticket.ID,
		Linked:              true,
		Provider:            *ticket.Provider,
		ExternalTicketID:    *ticket.ExternalTicketID,
		ExternalStatus:      snapshot.ExternalStatus,
		ExternalStatusLabel: snapshot.ExternalStatusLabel,
		SyncStatus:          "synced",
		LastSyncedAt:        &now,
	}, nil
}
