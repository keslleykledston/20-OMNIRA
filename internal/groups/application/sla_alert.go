// ADR-0058: the ONE exception to "nothing is ever sent to a group". When a conversation's SLA breaches
// (ADR-0044/0057), the tenant's single, explicitly-opted-in, non-client alert channel group (if any) gets a
// real WhatsApp message. No conversation/message row is created for it — it is not shaped like a 1:1 send
// (internal/worker/delivery is tied to the conversations model on purpose); this is its own small, dedicated
// path, same reasoning as the ADR-0055 observer not reusing internal/intelligence.
package application

import (
	"context"
	"fmt"
	"log"

	"github.com/google/uuid"

	channelsdomain "github.com/omnira/omnira/internal/channels/domain"
)

// AlertChannel is the tenant's single alert-channel group, as the repository finds it.
type AlertChannel struct {
	GroupID         uuid.UUID
	ConnectionID    uuid.UUID
	ProviderGroupID string
}

// AlertChannelRepository resolves a conversation's real tenant (never trusting a caller-supplied tenant id —
// same hardening as internal/worker/routing.PostgresConversationRunner) and that tenant's alert channel, if
// any is configured and the tenant is active.
type AlertChannelRepository interface {
	// AlertChannelForConversation returns (nil, nil) when the conversation is unknown, the tenant is
	// suspended, or no group is currently the alert channel — all three are "do nothing", never an error.
	AlertChannelForConversation(ctx context.Context, conversationID uuid.UUID) (*AlertChannel, error)
}

// Sender is the narrow slice of internal/channels/application.ChannelService this package depends on —
// kept as an interface so this package never needs to know about provider registries or connection lookups.
type Sender interface {
	SendText(ctx context.Context, connID uuid.UUID, msg channelsdomain.OutboundTextMessage) (*channelsdomain.SendResult, error)
}

// SLAAlertMetrics is the minimal counter surface; nil is fine (no-op).
type SLAAlertMetrics interface {
	Alerted(outcome string)
}

type noAlertMetrics struct{}

func (noAlertMetrics) Alerted(string) {}

var metricLabel = map[string]string{
	"first_response": "primeira resposta",
	"queue_wait":     "espera na fila",
	"assignment":     "atribuição",
	"resolution":     "resolução",
}

type SLAAlertService struct {
	repo    AlertChannelRepository
	sender  Sender
	metrics SLAAlertMetrics
}

func NewSLAAlertService(repo AlertChannelRepository, sender Sender, m SLAAlertMetrics) *SLAAlertService {
	if m == nil {
		m = noAlertMetrics{}
	}
	return &SLAAlertService{repo: repo, sender: sender, metrics: m}
}

// HandleBreach is best-effort (ADR-0058 decision 7): a failure here is logged and swallowed, never propagated
// to the caller — it must never hold up or retry-storm the job queue over a WhatsApp/WAHA hiccup. The durable
// record of the breach itself is the outbox event (ADR-0044); this is only a side-effect notification.
func (s *SLAAlertService) HandleBreach(ctx context.Context, conversationID uuid.UUID, metric, idempotencyKey string) {
	ch, err := s.repo.AlertChannelForConversation(ctx, conversationID)
	if err != nil {
		s.metrics.Alerted("lookup_error")
		log.Printf("groups: sla alert lookup failed (%v)", err)
		return
	}
	if ch == nil {
		s.metrics.Alerted("no_channel")
		return
	}
	label := metricLabel[metric]
	if label == "" {
		label = "SLA"
	}
	text := fmt.Sprintf("⚠️ SLA de %s estourado numa conversa. Verifique o Inbox para atuar.", label)
	if _, err := s.sender.SendText(ctx, ch.ConnectionID, channelsdomain.OutboundTextMessage{
		ProviderChatID: ch.ProviderGroupID,
		Text:           text,
		IdempotencyKey: idempotencyKey,
	}); err != nil {
		s.metrics.Alerted("send_error")
		log.Printf("groups: sla alert send failed (%v)", err)
		return
	}
	s.metrics.Alerted("sent")
}
