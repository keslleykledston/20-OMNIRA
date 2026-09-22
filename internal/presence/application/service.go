package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	presencedomain "github.com/omnira/omnira/internal/presence/domain"
	"github.com/omnira/omnira/internal/presence/ports"
)

// HeartbeatTTL is the presence session lifetime (ADR-0010: 120s, two missed
// 30s beats before a session is considered gone).
const HeartbeatTTL = 120 * time.Second

var ErrInvalidHeartbeat = errors.New("presence: valid heartbeat requires tenant, agent profile and session id")
var ErrInvalidTenant = errors.New("presence: valid tenant is required")

// Service is the heartbeat and snapshot use cases. Routing enforcement
// (IAM4.2-B) deliberately has no hook here yet — this service only records
// and reports presence.
type Service struct {
	store     ports.Store
	publisher ports.TransitionPublisher
	lastSeen  ports.LastSeenWriter
	now       func() time.Time
}

func NewService(store ports.Store, publisher ports.TransitionPublisher, lastSeen ports.LastSeenWriter) *Service {
	return &Service{store: store, publisher: publisher, lastSeen: lastSeen, now: time.Now}
}

// Heartbeat registers one session's liveness. tenantID and agentProfileID
// must already be resolved server-side from the authenticated session by the
// caller — this method never accepts client-chosen identity.
func (s *Service) Heartbeat(ctx context.Context, tenantID, agentProfileID uuid.UUID, sessionID string) error {
	if s == nil || s.store == nil || tenantID == uuid.Nil || agentProfileID == uuid.Nil || sessionID == "" {
		return ErrInvalidHeartbeat
	}
	becameOnline, err := s.store.Touch(ctx, tenantID, agentProfileID, sessionID, HeartbeatTTL)
	if err != nil {
		return err
	}
	now := s.now()
	if s.lastSeen != nil {
		s.lastSeen.MarkSeen(tenantID, agentProfileID, now)
	}
	if becameOnline && s.publisher != nil {
		if err := s.publisher.PublishTransition(ctx, tenantID, agentProfileID, presencedomain.StatusOnline); err != nil {
			return err
		}
	}
	return nil
}

// Snapshot lists agents currently online in a tenant, read straight from the
// realtime store.
func (s *Service) Snapshot(ctx context.Context, tenantID uuid.UUID) ([]uuid.UUID, error) {
	if s == nil || s.store == nil || tenantID == uuid.Nil {
		return nil, ErrInvalidTenant
	}
	return s.store.Snapshot(ctx, tenantID)
}
