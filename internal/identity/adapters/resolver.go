package adapters

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	channeldomain "github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/identity/domain"
	inboxapp "github.com/omnira/omnira/internal/inbox/application"
)

// SenderResolver implements the inbound resolver on top of VERIFIED internal identities (ADR-0018). It never uses a
// display name, never a phone it only "saw" in a profile, and never an AI; a pending or revoked identity, or the
// identity of a member whose membership is no longer active, resolves to nothing.
type SenderResolver struct {
	repo    *Repository
	enabled bool
}

var _ inboxapp.IdentityResolver = (*SenderResolver)(nil)

func NewSenderResolver(pool *pgxpool.Pool, enabled bool) *SenderResolver {
	return &SenderResolver{repo: NewRepository(pool), enabled: enabled}
}

// ResolveSender looks the sender up by phone and by provider participant id. Two DIFFERENT staff members matching the
// same sender is ambiguous and is treated as a conflict (fail closed): nobody is assumed to be anybody.
func (r *SenderResolver) ResolveSender(ctx context.Context, connection channeldomain.ChannelConnection, phoneE164, participantID string) (inboxapp.SenderIdentity, error) {
	if r == nil || !r.enabled {
		return inboxapp.SenderIdentity{}, nil
	}
	var matches []*domain.Match
	if phone, err := domain.NormalizePhone(phoneE164); err == nil {
		m, err := r.repo.FindVerified(ctx, connection.TenantID, domain.TypePhone, "", phone)
		if err != nil {
			return inboxapp.SenderIdentity{}, err
		}
		if m != nil {
			matches = append(matches, m)
		}
	}
	if participantID != "" {
		if norm, err := domain.NormalizeParticipant(participantID); err == nil {
			m, err := r.repo.FindVerified(ctx, connection.TenantID, domain.TypeProviderParticipant, domain.ParticipantScope(string(connection.Provider), connection.ID), norm)
			if err != nil {
				return inboxapp.SenderIdentity{}, err
			}
			if m != nil {
				matches = append(matches, m)
			}
		}
	}
	if len(matches) == 0 {
		return inboxapp.SenderIdentity{}, nil
	}
	user := matches[0].UserID
	for _, m := range matches {
		if m.UserID != user { // two staff members claim the same sender
			return inboxapp.SenderIdentity{Conflict: true}, nil
		}
		if m.HasOpenConflict { // a human has not decided yet whether this is the staff member or the external contact
			return inboxapp.SenderIdentity{Conflict: true}, nil
		}
	}
	return inboxapp.SenderIdentity{Internal: true, UserID: user}, nil
}

var _ = uuid.Nil
