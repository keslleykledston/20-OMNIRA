package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	routingdomain "github.com/omnira/omnira/internal/routing/domain"
	"github.com/omnira/omnira/internal/routing/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ParticipantService orquestra convites, transferências e co-atendimentos.
type ParticipantService struct {
	repo  ports.ParticipantRepository
	conv  ports.ConversationAssigner
	audit ports.AuditRecorder
}

func NewParticipantService(
	repo ports.ParticipantRepository,
	conv ports.ConversationAssigner,
	audit ports.AuditRecorder,
) *ParticipantService {
	return &ParticipantService{repo: repo, conv: conv, audit: audit}
}

// InviteResult = resultado de um convite.
type InviteResult struct {
	ParticipantID uuid.UUID
	TargetUserID  uuid.UUID
	Role          routingdomain.ParticipantRole
	Changed       bool
}

// Invite convida um técnico para co-atender (conversation.manage).
func (s *ParticipantService) Invite(
	ctx context.Context,
	conversationID, targetUserID uuid.UUID,
) (InviteResult, error) {
	if conversationID == uuid.Nil || targetUserID == uuid.Nil {
		return InviteResult{}, errors.New("participant: conversation and target are required")
	}

	tc, err := tenantFromContext(ctx)
	if err != nil {
		return InviteResult{}, err
	}

	// Verificar permissão conversation.manage
	if ok, err := s.conv.HasPermission(ctx, tc.ActorID, "conversation.manage"); err != nil || !ok {
		return InviteResult{}, errors.New("participant: forbidden")
	}

	// Verificar se conversation existe
	if _, found, err := s.conv.LockAssignee(ctx, conversationID); err != nil || !found {
		return InviteResult{}, errors.New("participant: conversation not found")
	}

	// Verificar se target é eligible (conversation.claim)
	if ok, err := s.conv.HasPermission(ctx, targetUserID, "conversation.claim"); err != nil || !ok {
		return InviteResult{}, errors.New("participant: target is not an eligible agent")
	}

	// Se já existe participant, retornar idempotente (no-op)
	existing, _ := s.repo.FindByUserAndConversation(ctx, conversationID, targetUserID)
	if existing != nil {
		return InviteResult{
			ParticipantID: existing.ID,
			TargetUserID:  targetUserID,
			Role:          existing.Role,
			Changed:       false,
		}, nil
	}

	// Criar novo invite
	p, err := routingdomain.NewInvite(tc.TenantID, conversationID, targetUserID)
	if err != nil {
		return InviteResult{}, err
	}

	if err := s.repo.Create(ctx, p); err != nil {
		return InviteResult{}, err
	}

	// Auditar
	if s.audit != nil {
		s.audit.ParticipantInvited(ctx, conversationID, tc.ActorID, targetUserID)
	}

	return InviteResult{
		ParticipantID: p.ID,
		TargetUserID:  targetUserID,
		Role:          p.Role,
		Changed:       true,
	}, nil
}

// TransferResult = resultado de transferência.
type TransferResult struct {
	PreviousAssignee *uuid.UUID
	NewAssignee      *uuid.UUID
	Changed          bool
}

// Transfer move a conversation para outro técnico (conversation.manage).
// Buscador anterior é removido ou vira CO_ATTENDEE (dependendo da config).
func (s *ParticipantService) Transfer(
	ctx context.Context,
	conversationID, targetUserID uuid.UUID,
) (TransferResult, error) {
	if conversationID == uuid.Nil || targetUserID == uuid.Nil {
		return TransferResult{}, errors.New("participant: conversation and target are required")
	}

	tc, err := tenantFromContext(ctx)
	if err != nil {
		return TransferResult{}, err
	}

	// Verificar permissão conversation.manage
	if ok, err := s.conv.HasPermission(ctx, tc.ActorID, "conversation.manage"); err != nil || !ok {
		return TransferResult{}, errors.New("participant: forbidden")
	}

	// Encontrar assignee atual
	current, found, err := s.conv.LockAssignee(ctx, conversationID)
	if err != nil || !found {
		return TransferResult{}, errors.New("participant: conversation not found")
	}

	// Verificar se target é eligible
	if ok, err := s.conv.HasPermission(ctx, targetUserID, "conversation.claim"); err != nil || !ok {
		return TransferResult{}, errors.New("participant: target is not an eligible agent")
	}

	// Se já atribuído ao target, retornar idempotente
	if current != nil && *current == targetUserID {
		return TransferResult{
			PreviousAssignee: current,
			NewAssignee:      current,
			Changed:          false,
		}, nil
	}

	// Atualizar assignment no banco (via existing Assigner)
	change := ports.AssignmentChange{
		ConversationID: conversationID,
		From:           current,
		To:             &targetUserID,
		Actor:          tc.ActorID,
		Reason:         "transfer",
	}

	if err := s.conv.SetAssignee(ctx, change); err != nil {
		return TransferResult{}, err
	}

	// Criar novo participant para novo assignee se não existir
	existingTarget, _ := s.repo.FindByUserAndConversation(ctx, conversationID, targetUserID)
	if existingTarget == nil {
		newP, err := routingdomain.NewInvite(tc.TenantID, conversationID, targetUserID)
		if err == nil {
			newP.Role = routingdomain.RoleAssignee
			newP.JoinedAt = nil
			s.repo.Create(ctx, newP) // Ignorar erro, best-effort
		}
	} else if existingTarget.Role != routingdomain.RoleAssignee {
		s.repo.UpdateRole(ctx, existingTarget.ID, routingdomain.RoleAssignee)
	}

	// Auditar
	if s.audit != nil {
		s.audit.ConversationTransferred(ctx, conversationID, tc.ActorID, current, &targetUserID)
	}

	return TransferResult{
		PreviousAssignee: current,
		NewAssignee:      &targetUserID,
		Changed:          true,
	}, nil
}

// AcceptResult = resultado de aceitação de convite.
type AcceptResult struct {
	ParticipantID uuid.UUID
	JoinedAt      *time.Time
}

// AcceptInvite aceita um convite para co-atender.
func (s *ParticipantService) AcceptInvite(
	ctx context.Context,
	conversationID uuid.UUID,
) (AcceptResult, error) {
	if conversationID == uuid.Nil {
		return AcceptResult{}, errors.New("participant: conversation is required")
	}

	tc, err := tenantFromContext(ctx)
	if err != nil {
		return AcceptResult{}, err
	}

	// Encontrar convite do usuário atual
	p, err := s.repo.FindByUserAndConversation(ctx, conversationID, tc.ActorID)
	if err != nil || p == nil {
		return AcceptResult{}, errors.New("participant: invitation not found")
	}

	if p.Role != routingdomain.RoleInvited {
		return AcceptResult{}, errors.New("participant: only pending invitations can be accepted")
	}

	// Aceitar convite
	if err := p.AcceptInvite(); err != nil {
		return AcceptResult{}, err
	}

	// Marcar joined_at
	if err := s.repo.MarkAsJoined(ctx, p.ID); err != nil {
		return AcceptResult{}, err
	}

	// Auditar
	if s.audit != nil {
		s.audit.ParticipantAccepted(ctx, conversationID, tc.ActorID)
	}

	return AcceptResult{
		ParticipantID: p.ID,
		JoinedAt:      p.JoinedAt,
	}, nil
}

// RejectInvite rejeita um convite pendente.
func (s *ParticipantService) RejectInvite(
	ctx context.Context,
	conversationID uuid.UUID,
) error {
	if conversationID == uuid.Nil {
		return errors.New("participant: conversation is required")
	}

	tc, err := tenantFromContext(ctx)
	if err != nil {
		return err
	}

	// Encontrar convite do usuário atual
	p, err := s.repo.FindByUserAndConversation(ctx, conversationID, tc.ActorID)
	if err != nil || p == nil {
		return errors.New("participant: invitation not found")
	}

	if p.Role != routingdomain.RoleInvited {
		return errors.New("participant: only pending invitations can be rejected")
	}

	// Marcar como saído (soft-delete)
	if err := s.repo.MarkAsLeft(ctx, p.ID); err != nil {
		return err
	}

	// Auditar
	if s.audit != nil {
		s.audit.ParticipantRejected(ctx, conversationID, tc.ActorID)
	}

	return nil
}

// Leave remove um co-attendee do atendimento.
func (s *ParticipantService) Leave(
	ctx context.Context,
	conversationID uuid.UUID,
) error {
	if conversationID == uuid.Nil {
		return errors.New("participant: conversation is required")
	}

	tc, err := tenantFromContext(ctx)
	if err != nil {
		return err
	}

	// Encontrar participant
	p, err := s.repo.FindByUserAndConversation(ctx, conversationID, tc.ActorID)
	if err != nil || p == nil {
		return errors.New("participant: participant not found")
	}

	// Verificar se é CO_ATTENDEE
	if p.Role != routingdomain.RoleCoAttendee {
		return errors.New("participant: only co-attendees can leave")
	}

	// Marcar como saído
	if err := s.repo.MarkAsLeft(ctx, p.ID); err != nil {
		return err
	}

	// Auditar
	if s.audit != nil {
		s.audit.ParticipantLeft(ctx, conversationID, tc.ActorID)
	}

	return nil
}

func tenantFromContext(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		return nil, errors.New("participant: valid tenant context required")
	}
	return tc, nil
}
