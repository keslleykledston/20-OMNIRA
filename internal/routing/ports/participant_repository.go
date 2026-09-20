package ports

import (
	"context"

	"github.com/google/uuid"
	routingdomain "github.com/omnira/omnira/internal/routing/domain"
)

// ParticipantRepository gerencia participants de conversations.
type ParticipantRepository interface {
	// Create insere um novo participante (invitation ou assignment).
	Create(context.Context, *routingdomain.ConversationParticipant) error

	// FindByConversation retorna todos os participantes de uma conversation.
	FindByConversation(context.Context, uuid.UUID) ([]*routingdomain.ConversationParticipant, error)

	// FindAssignee retorna o ASSIGNEE atual de uma conversation (nil se nenhum).
	FindAssignee(context.Context, uuid.UUID) (*routingdomain.ConversationParticipant, error)

	// FindByUserAndConversation retorna o participant específico.
	FindByUserAndConversation(context.Context, uuid.UUID, uuid.UUID) (*routingdomain.ConversationParticipant, error)

	// UpdateRole altera o role de um participant.
	UpdateRole(context.Context, uuid.UUID, routingdomain.ParticipantRole) error

	// MarkAsJoined define o joined_at timestamp (para co-attendees).
	MarkAsJoined(context.Context, uuid.UUID) error

	// MarkAsLeft define o left_at timestamp.
	MarkAsLeft(context.Context, uuid.UUID) error

	// ListActiveParticipants retorna todos os participantes ativos (sem left_at).
	ListActiveParticipants(context.Context, uuid.UUID) ([]*routingdomain.ConversationParticipant, error)
}
