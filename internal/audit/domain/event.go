package domain

import (
	"time"

	"github.com/google/uuid"
)

// AuditAction — tipo de ação auditada.
type AuditAction string

const (
	ActionTenantCreated       AuditAction = "tenant.created"
	ActionTenantDeactivated   AuditAction = "tenant.deactivated"
	ActionTenantSuspended     AuditAction = "tenant.suspended"
	ActionTenantInboxSettings AuditAction = "tenant.inbox_settings_updated"
	ActionContactKindChanged  AuditAction = "contact.kind_changed"
	ActionContactUpdated      AuditAction = "contact.updated"
	ActionContactNoteChanged  AuditAction = "contact.note_changed"
	// ADR-0018: accounts, classification, company links and internal identities.
	ActionAccountCreated           AuditAction = "account.created"
	ActionAccountUpdated           AuditAction = "account.updated"
	ActionContactClassified        AuditAction = "contact.classified"
	ActionContactReclassified      AuditAction = "contact.reclassified"
	ActionContactAccountLinked     AuditAction = "contact.account_linked"
	ActionContactAccountUnlinked   AuditAction = "contact.account_unlinked"
	ActionContactPrimaryAccount    AuditAction = "contact.primary_account_changed"
	ActionUserIdentityCreated      AuditAction = "user.identity_created"
	ActionUserIdentityVerified     AuditAction = "user.identity_verified"
	ActionUserIdentityRevoked      AuditAction = "user.identity_revoked"
	ActionIdentityConflictFound    AuditAction = "identity.conflict_found"
	ActionIdentityConflictResolved AuditAction = "identity.conflict_resolved"
	ActionConversationKindChanged  AuditAction = "conversation.kind_changed"
	ActionTopicAccountLinked       AuditAction = "topic.account_linked"
	ActionTopicAccountUnlinked     AuditAction = "topic.account_unlinked"
	ActionTenantAIIntegration      AuditAction = "tenant.ai_integration_updated"
	ActionGroupEnabled             AuditAction = "group.enabled"
	ActionGroupDisabled            AuditAction = "group.disabled"
	ActionGroupHistoryDeleted      AuditAction = "group.history_deleted"
	ActionMembershipGranted        AuditAction = "membership.granted"
	ActionMembershipRevoked        AuditAction = "membership.revoked"
	ActionMembershipDeactivated    AuditAction = "membership.deactivated"
	ActionMembershipReactivated    AuditAction = "membership.reactivated"
	ActionMembershipRoleChanged    AuditAction = "membership.role_changed"
	ActionInvitationCreated        AuditAction = "membership.invitation.created"
	ActionInvitationRevoked        AuditAction = "membership.invitation.revoked"
	ActionInvitationResent         AuditAction = "membership.invitation.resent"
	ActionInvitationAccepted       AuditAction = "membership.invitation.accepted"
	ActionConversationAssigned     AuditAction = "conversation.assigned"
	ActionConversationUnassigned   AuditAction = "conversation.unassigned"
	ActionConversationTransferred  AuditAction = "conversation.transferred"
	ActionAgentEnabled             AuditAction = "agent.enabled"
	ActionAgentDisabled            AuditAction = "agent.disabled"
	ActionAgentQueueAssigned       AuditAction = "agent.queue_assigned"
	ActionAgentQueueRemoved        AuditAction = "agent.queue_removed"
	ActionAgentQueueAvailability   AuditAction = "agent.queue_availability_changed"
	ActionAgentQueueCapacity       AuditAction = "agent.queue_capacity_changed"
	ActionQueueCreated             AuditAction = "queue.created"
	ActionQueueUpdated             AuditAction = "queue.updated"
	ActionQueueDefaultChanged      AuditAction = "queue.default_changed"
	ActionQueueDeleted             AuditAction = "queue.deleted"
	ActionParticipantInvited       AuditAction = "participant.invited"
	ActionParticipantAccepted      AuditAction = "participant.accepted"
	ActionParticipantRejected      AuditAction = "participant.rejected"
	ActionParticipantLeft          AuditAction = "participant.left"
	ActionChannelConnectionCreated AuditAction = "channel.connection_created"
	ActionChannelSessionStarted    AuditAction = "channel.session_started"
	ActionChannelSessionStopped    AuditAction = "channel.session_stopped"
	// ActionAIConversationSummarize (PRODUCT.7C1): recorded twice per
	// request — once BEFORE the provider call (metadata phase="requested",
	// never skippable: internal/ai/adapters.SummaryHandler refuses to call
	// the provider at all if this write fails) and once AFTER (metadata
	// phase="completed", with the real outcome/result_category). Metadata
	// never contains message content, the generated summary, or any PII —
	// see internal/ai/adapters/http.go's audit helpers.
	ActionAIConversationSummarize AuditAction = "ai.conversation.summarize"
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
	ResourceAgentProfile      ResourceType = "agent_profile"
	ResourceQueue             ResourceType = "queue"
	ResourceContact           ResourceType = "contact"
	ResourceGroup             ResourceType = "wa_group"
	ResourceAccount           ResourceType = "customer_account"
	ResourceUserIdentity      ResourceType = "user_channel_identity"
	ResourceIdentityConflict  ResourceType = "identity_conflict"
	ResourceTopic             ResourceType = "topic_thread"
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
