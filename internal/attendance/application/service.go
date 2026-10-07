// Package application finalizes attendances and manages what is pending or promised to a contact (ADR-0020).
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/attendance/domain"
	"github.com/omnira/omnira/internal/attendance/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Permissions (existing keys: no migration, no change to the role matrix).
const (
	PermissionClaim  = "conversation.claim"
	PermissionManage = "conversation.manage"
)

// Audit actions and resource types (plain strings: the shared audit domain file stays untouched).
const (
	ActionConversationClosed = "conversation.closed"
	ActionFollowUpResolved   = "followup.resolved"
	ResourceConversation     = "conversation"
	ResourceFollowUp         = "follow_up"
)

type Service struct {
	repo  ports.Repository
	authz ports.Authorizer
	audit ports.Auditor
	now   func() time.Time
	// suggester is optional (the intelligence module); nil means the AI suggestion is not offered.
	suggester ports.ClosingSuggester
}

func NewService(repo ports.Repository, authz ports.Authorizer, audit ports.Auditor) *Service {
	return &Service{repo: repo, authz: authz, audit: audit, now: func() time.Time { return time.Now().UTC() }}
}

// WithSuggester enables the AI closing suggestion.
func (s *Service) WithSuggester(g ports.ClosingSuggester) *Service {
	s.suggester = g
	return s
}

// SuggestClosing asks the model for a DRAFT summary and follow-up items. Same authorization as finalizing (the owner, or a
// supervisor), because what it reads is the conversation the person is about to close. Read-only: it takes no lock (a model call
// is slow), stores nothing, and a finalized conversation has nothing left to suggest.
func (s *Service) SuggestClosing(ctx context.Context, conversationID uuid.UUID) (domain.ClosingSuggestion, error) {
	tc, err := actor(ctx)
	if err != nil {
		return domain.ClosingSuggestion{}, err
	}
	if s.suggester == nil {
		return domain.ClosingSuggestion{}, domain.ErrSuggestionDisabled
	}
	claim, err := s.has(ctx, tc.ActorID, PermissionClaim)
	if err != nil {
		return domain.ClosingSuggestion{}, err
	}
	manage, err := s.has(ctx, tc.ActorID, PermissionManage)
	if err != nil {
		return domain.ClosingSuggestion{}, err
	}
	if !claim && !manage {
		return domain.ClosingSuggestion{}, domain.ErrForbidden
	}
	facts, err := s.repo.ReadConversation(ctx, conversationID)
	if err != nil {
		return domain.ClosingSuggestion{}, err
	}
	if facts.ContactID == nil || facts.Kind == "internal" {
		return domain.ClosingSuggestion{}, domain.ErrNotAContact
	}
	if owner := facts.AssignedTo != nil && *facts.AssignedTo == tc.ActorID; !owner && !manage {
		return domain.ClosingSuggestion{}, domain.ErrForbidden
	}
	if facts.Status == "closed" {
		return domain.ClosingSuggestion{}, domain.ErrNothingToSuggest
	}
	return s.suggester.SuggestClosing(ctx, conversationID)
}

type FinalizeResult struct {
	Closure   *domain.Closure
	FollowUps []domain.FollowUp
	// Changed is false when the conversation was already finalized (the call is idempotent and returns the original).
	Changed bool
}

func actor(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		return nil, errors.New("attendance: authenticated tenant context required")
	}
	return tc, nil
}

func (s *Service) has(ctx context.Context, user uuid.UUID, permission string) (bool, error) {
	return s.authz.Has(ctx, user, permission)
}

// Finalize ends the attendance of one conversation: closes it, closes its local tickets, records the summary and what
// remains pending or was promised. One transaction (the request's), serialized with the flow engine by the row lock.
func (s *Service) Finalize(ctx context.Context, raw domain.FinalizeInput) (FinalizeResult, error) {
	tc, err := actor(ctx)
	if err != nil {
		return FinalizeResult{}, err
	}
	in, err := raw.Normalize()
	if err != nil {
		return FinalizeResult{}, err
	}
	claim, err := s.has(ctx, tc.ActorID, PermissionClaim)
	if err != nil {
		return FinalizeResult{}, err
	}
	manage, err := s.has(ctx, tc.ActorID, PermissionManage)
	if err != nil {
		return FinalizeResult{}, err
	}
	// Baseline first: a role with neither permission learns nothing about the conversation.
	if !claim && !manage {
		return FinalizeResult{}, domain.ErrForbidden
	}
	facts, err := s.repo.LockConversation(ctx, in.ConversationID)
	if err != nil {
		return FinalizeResult{}, err
	}
	if facts.ContactID == nil || facts.Kind == "internal" {
		return FinalizeResult{}, domain.ErrNotAContact
	}
	owner := facts.AssignedTo != nil && *facts.AssignedTo == tc.ActorID
	if !owner && !manage {
		// somebody else's or an unassigned conversation: only a supervisor
		return FinalizeResult{}, domain.ErrForbidden
	}
	if facts.Status == "closed" {
		existing, err := s.repo.ClosureByConversation(ctx, in.ConversationID)
		if err != nil {
			return FinalizeResult{}, err
		}
		res := FinalizeResult{Closure: existing}
		if existing != nil {
			if res.FollowUps, err = s.repo.FollowUpsOfClosure(ctx, existing.ID); err != nil {
				return FinalizeResult{}, err
			}
		}
		return res, nil
	}
	for _, f := range in.FollowUps {
		if f.OwnerUserID == nil {
			continue
		}
		ok, err := s.repo.IsActiveMember(ctx, *f.OwnerUserID)
		if err != nil {
			return FinalizeResult{}, err
		}
		if !ok {
			return FinalizeResult{}, errors.Join(domain.ErrInvalid, errors.New("a follow-up owner is not an active member of this tenant"))
		}
	}
	closed, kept, err := s.repo.CloseLocalTickets(ctx, in.ConversationID)
	if err != nil {
		return FinalizeResult{}, err
	}
	if err := s.repo.MarkClosed(ctx, in.ConversationID); err != nil {
		return FinalizeResult{}, err
	}
	source := domain.SourceAgent
	if !owner {
		source = domain.SourceSupervisor
	}
	by := tc.ActorID
	now := s.now()
	closure := &domain.Closure{
		ID: uuid.New(), TenantID: tc.TenantID, ConversationID: in.ConversationID, ContactID: *facts.ContactID, ClosedBy: &by,
		Source: source, Reason: in.Reason, Note: in.Note, Summary: in.Summary, SummaryTruth: in.SummaryTruth,
		LocalTicketsClosed: closed, TicketsKept: kept, CreatedAt: now,
	}
	if err := s.repo.InsertClosure(ctx, closure); err != nil {
		return FinalizeResult{}, err
	}
	items := make([]domain.FollowUp, 0, len(in.FollowUps))
	for _, f := range in.FollowUps {
		cid := closure.ID
		items = append(items, domain.FollowUp{
			ID: uuid.New(), TenantID: tc.TenantID, ContactID: *facts.ContactID, ConversationID: in.ConversationID, ClosureID: &cid,
			Kind: f.Kind, Text: f.Text, OwnerUserID: f.OwnerUserID, DueAt: f.DueAt, Status: domain.StatusOpen,
			// the person listed (or confirmed) them while finalizing: that is what "agent_confirmed" means
			Truth: domain.TruthAgentConfirmed, CreatedBy: &by, CreatedAt: now,
		})
	}
	if err := s.repo.InsertFollowUps(ctx, items); err != nil {
		return FinalizeResult{}, err
	}
	s.audit.Record(ctx, ActionConversationClosed, ResourceConversation, in.ConversationID, map[string]any{
		"reason": string(in.Reason), "source": string(source), "local_tickets_closed": closed, "tickets_kept": kept, "follow_ups": len(items),
	})
	return FinalizeResult{Closure: closure, FollowUps: items, Changed: true}, nil
}

// FinalizeBySystem ends the attendance because the CONTACT asked for it (the flow engine is the actor, with a system
// TenantContext). Same effects as an agent's finalize (conversation closed, local tickets closed, ERP and topic tickets left
// alone) but recorded with source "system", no author, reason "other" and the command in the note. Idempotent: an already
// closed conversation returns its existing closure.
func (s *Service) FinalizeBySystem(ctx context.Context, conversationID uuid.UUID, note string) (FinalizeResult, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.Source != tenancydomain.AccessSourceSystem {
		return FinalizeResult{}, domain.ErrForbidden
	}
	in, err := domain.FinalizeInput{ConversationID: conversationID, Reason: domain.ReasonOther, Note: note, SummaryTruth: domain.TruthAgentConfirmed}.Normalize()
	if err != nil {
		return FinalizeResult{}, err
	}
	facts, err := s.repo.LockConversation(ctx, conversationID)
	if err != nil {
		return FinalizeResult{}, err
	}
	if facts.ContactID == nil || facts.Kind == "internal" {
		return FinalizeResult{}, domain.ErrNotAContact
	}
	if facts.Status == "closed" {
		existing, err := s.repo.ClosureByConversation(ctx, conversationID)
		return FinalizeResult{Closure: existing}, err
	}
	closed, kept, err := s.repo.CloseLocalTickets(ctx, conversationID)
	if err != nil {
		return FinalizeResult{}, err
	}
	if err := s.repo.MarkClosed(ctx, conversationID); err != nil {
		return FinalizeResult{}, err
	}
	closure := &domain.Closure{
		ID: uuid.New(), TenantID: tc.TenantID, ConversationID: conversationID, ContactID: *facts.ContactID, ClosedBy: nil,
		Source: domain.SourceSystem, Reason: in.Reason, Note: in.Note, SummaryTruth: in.SummaryTruth,
		LocalTicketsClosed: closed, TicketsKept: kept, CreatedAt: s.now(),
	}
	if err := s.repo.InsertClosure(ctx, closure); err != nil {
		return FinalizeResult{}, err
	}
	return FinalizeResult{Closure: closure, Changed: true}, nil
}

// CloseForCustomer is FinalizeBySystem without the result, for callers that only need the closing (the flow engine).
func (s *Service) CloseForCustomer(ctx context.Context, conversationID uuid.UUID, note string) error {
	_, err := s.FinalizeBySystem(ctx, conversationID, note)
	return err
}

// ClosureView is a closure with its follow-ups, for the history panel.
type ClosureView struct {
	Closure   domain.Closure
	FollowUps []domain.FollowUp
}

// History is what the next attendant (and, in ADR-0020 wave 3, the copilot) needs to know about a contact.
type History struct {
	ContactID     uuid.UUID
	Attendances   []ClosureView
	OpenFollowUps []domain.FollowUp
}

const (
	defaultHistoryLimit = 5
	maxHistoryLimit     = 20
	maxFollowUpList     = 50
)

// History of the contact of a conversation. Any active member may read it (same visibility as the Inbox); RLS scopes the
// tenant. `exclude` leaves one conversation out (the one being looked at).
func (s *Service) HistoryOfConversation(ctx context.Context, conversationID uuid.UUID, limit int) (History, error) {
	if _, err := actor(ctx); err != nil {
		return History{}, err
	}
	contactID, err := s.repo.ContactOfConversation(ctx, conversationID)
	if err != nil {
		return History{}, err
	}
	if contactID == uuid.Nil {
		return History{}, domain.ErrNotAContact
	}
	return s.HistoryOfContact(ctx, contactID, limit)
}

func (s *Service) HistoryOfContact(ctx context.Context, contactID uuid.UUID, limit int) (History, error) {
	if _, err := actor(ctx); err != nil {
		return History{}, err
	}
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	if limit > maxHistoryLimit {
		limit = maxHistoryLimit
	}
	closures, err := s.repo.ListClosures(ctx, contactID, limit)
	if err != nil {
		return History{}, err
	}
	h := History{ContactID: contactID, Attendances: make([]ClosureView, 0, len(closures))}
	for _, c := range closures {
		fu, err := s.repo.FollowUpsOfClosure(ctx, c.ID)
		if err != nil {
			return History{}, err
		}
		h.Attendances = append(h.Attendances, ClosureView{Closure: c, FollowUps: fu})
	}
	if h.OpenFollowUps, err = s.repo.ListFollowUps(ctx, contactID, domain.StatusOpen, maxFollowUpList); err != nil {
		return History{}, err
	}
	return h, nil
}

// SearchHistoryOfContact finds earlier messages of one contact. Any active member may read (same visibility as the Inbox);
// the caller supplies a contact it can already see (derived from a conversation or a topic, never from user input).
func (s *Service) SearchHistoryOfContact(ctx context.Context, contactID uuid.UUID, raw domain.SearchInput) ([]domain.HistoryHit, error) {
	if _, err := actor(ctx); err != nil {
		return nil, err
	}
	in, err := raw.Normalize()
	if err != nil {
		return nil, err
	}
	return s.repo.SearchContactMessages(ctx, contactID, in)
}

// SearchHistoryOfConversation searches the contact of a conversation, leaving that conversation out.
func (s *Service) SearchHistoryOfConversation(ctx context.Context, conversationID uuid.UUID, query string, limit int) ([]domain.HistoryHit, error) {
	if _, err := actor(ctx); err != nil {
		return nil, err
	}
	contactID, err := s.repo.ContactOfConversation(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	if contactID == uuid.Nil {
		return nil, domain.ErrNotAContact
	}
	return s.SearchHistoryOfContact(ctx, contactID, domain.SearchInput{Query: query, Limit: limit, ExcludeConversationID: &conversationID})
}

// OpenFollowUpsOfContact lists the contact's open items (dated first), at most `limit` (default and cap: 50).
func (s *Service) OpenFollowUpsOfContact(ctx context.Context, contactID uuid.UUID, limit int) ([]domain.FollowUp, error) {
	if _, err := actor(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > maxFollowUpList {
		limit = maxFollowUpList
	}
	return s.repo.ListFollowUps(ctx, contactID, domain.StatusOpen, limit)
}

// ResolveFollowUp marks an open item done or dropped. Needs conversation.claim or conversation.manage.
func (s *Service) ResolveFollowUp(ctx context.Context, id uuid.UUID, raw domain.ResolveFollowUpInput) (*domain.FollowUp, error) {
	tc, err := actor(ctx)
	if err != nil {
		return nil, err
	}
	in, err := raw.Normalize()
	if err != nil {
		return nil, err
	}
	claim, err := s.has(ctx, tc.ActorID, PermissionClaim)
	if err != nil {
		return nil, err
	}
	manage, err := s.has(ctx, tc.ActorID, PermissionManage)
	if err != nil {
		return nil, err
	}
	if !claim && !manage {
		return nil, domain.ErrForbidden
	}
	item, err := s.repo.LockFollowUp(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.Status != domain.StatusOpen {
		return item, domain.ErrAlreadyHandled
	}
	if err := s.repo.ResolveFollowUp(ctx, id, in.Status, in.Note, tc.ActorID); err != nil {
		return nil, err
	}
	now := s.now()
	item.Status, item.ResolvedAt, item.ResolvedBy, item.ResolutionNote = in.Status, &now, &tc.ActorID, in.Note
	s.audit.Record(ctx, ActionFollowUpResolved, ResourceFollowUp, id, map[string]any{"status": string(in.Status)})
	return item, nil
}
