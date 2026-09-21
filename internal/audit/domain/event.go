package domain

import (
	"time"

	"github.com/google/uuid"
)

// AuditAction — tipo de ação auditada.
type AuditAction string

const (
	ActionTenantCreated            AuditAction = "tenant.created"
	ActionTenantDeactivated        AuditAction = "tenant.deactivated"
	ActionTenantSuspended          AuditAction = "tenant.suspended"
	ActionMembershipGranted        AuditAction = "membership.granted"
	ActionMembershipRevoked        AuditAction = "membership.revoked"
	ActionMembershipDeactivated    AuditAction = "membership.deactivated"
	ActionMembershipReactivated    AuditAction = "membership.reactivated"
	ActionMembershipRoleChanged    AuditAction = "membership.role_changed"
	ActionInvitationCreated        AuditAction = "membership.invitation.created"
	ActionInvitationRevoked        AuditAction = "membership.invitation.revoked"
	ActionInvitationAccepted       AuditAction = "membership.invitation.accepted"
	ActionConversationAssigned     AuditAction = "conversation.assigned"
	ActionConversationUnassigned   AuditAction = "conversation.unassigned"
	ActionConversationTransferred  AuditAction = "conversation.transferred"
	ActionParticipantInvited       AuditAction = "participant.invited"
	ActionParticipantAccepted      AuditAction = "participant.accepted"
	ActionParticipantRejected      AuditAction = "participant.rejected"
	ActionParticipantLeft          AuditAction = "participant.left"
	ActionChannelConnectionCreated AuditAction = "channel.connection_created"
	ActionChannelSessionStarted    AuditAction = "channel.session_started"
	ActionChannelSessionStopped    AuditAction = "channel.session_stopped"
)

// AuditOutcome — resultado da operação.
type AuditOutcome string

const (
	OutcomeSuccess AuditOutcome = "success"
	OutcomeFailure AuditOutcome = "failure"
)

// ResourceType — tipo de recurso sendo modificado.
type ResourceType string

const (
	ResourceTenant            ResourceType = "tenant"
	ResourceMembership        ResourceType = "membership"
	ResourceRole              ResourceType = "role"
	ResourceConversation      ResourceType = "conversation"
	ResourceChannelConnection ResourceType = "channel_connection"
)

// AuditEvent — evento de auditoria.
type AuditEvent struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	ActorID       uuid.UUID // UserID que executou a ação
	Action        AuditAction
	ResourceType  ResourceType
	ResourceID    uuid.UUID
	Outcome       AuditOutcome
	CorrelationID uuid.UUID // ID para correlacionar operações relacionadas
	CausationID   uuid.UUID // ID da operação que causou esta (para rastreabilidade)
	Metadata      map[string]interface{}
	CreatedAt     time.Time
}

// NewAuditEvent — factory com validação básica.
func NewAuditEvent(
	tenantID uuid.UUID,
	actorID uuid.UUID,
	action AuditAction,
	resourceType ResourceType,
	resourceID uuid.UUID,
	outcome AuditOutcome,
	correlationID uuid.UUID,
) (*AuditEvent, error) {
	if tenantID == uuid.Nil {
		return nil, ErrInvalidTenantID
	}
	if actorID == uuid.Nil {
		return nil, ErrInvalidActorID
	}

	// Se correlationID não for fornecido, gerar um novo
	if correlationID == uuid.Nil {
		correlationID = uuid.New()
	}

	return &AuditEvent{
		ID:            uuid.New(),
		TenantID:      tenantID,
		ActorID:       actorID,
		Action:        action,
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		Outcome:       outcome,
		CorrelationID: correlationID,
		Metadata:      make(map[string]interface{}),
		CreatedAt:     time.Now().UTC(),
	}, nil
}

// SetMetadata — adiciona metadados ao evento.
func (e *AuditEvent) SetMetadata(key string, value interface{}) {
	e.Metadata[key] = value
}

// SetCausationID — rastreia origem da ação.
func (e *AuditEvent) SetCausationID(id uuid.UUID) {
	e.CausationID = id
}
