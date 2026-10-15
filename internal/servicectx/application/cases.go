package application

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

// Audit actions for cases and tasks.
const (
	ActionCaseOpened      = "service_case.opened"
	ActionCaseUpdated     = "service_case.updated"
	ActionCaseStatus      = "service_case.status_changed"
	ActionCaseAssigned    = "service_case.assigned"
	ActionCaseLinked      = "service_case.session_linked"
	ActionTaskCreated     = "service_task.created"
	ActionTaskUpdated     = "service_task.updated"
	ActionTaskStatus      = "service_task.status_changed"
	ActionTaskAssigned    = "service_task.assigned"
	ResourceCase          = "service_case"
	ResourceTask          = "service_task"
	defaultContextOpen    = 10
	defaultContextPending = 10
)

func (s *Service) WithCases(r ports.CaseRepository) *Service      { s.cases = r; return s }
func (s *Service) WithTasks(r ports.TaskRepository) *Service      { s.tasks = r; return s }
func (s *Service) WithContext(r ports.ContextRepository) *Service { s.ctxRepo = r; return s }

// links says which entities an event belongs to (the timeline reads by these).
type links struct {
	session  *uuid.UUID
	caseID   *uuid.UUID
	conv     *uuid.UUID
	contact  *uuid.UUID
	customer *uuid.UUID
}

func (s *Service) emitFor(ctx context.Context, c caller, typ, entityType string, entityID uuid.UUID, at time.Time, l links, payload map[string]any, note string) error {
	if payload == nil {
		payload = map[string]any{}
	}
	ev := domain.ContextEvent{
		ID: uuid.New(), TenantID: c.tc.TenantID, EventType: typ, EntityType: entityType, EntityID: entityID,
		SessionID: l.session, CaseID: l.caseID, ConversationID: l.conv, ContactID: l.contact, CustomerID: l.customer,
		ActorType: c.actorKind(), ActorID: c.actorID(), SourceSystem: "omnira", OccurredAt: at, RecordedAt: at,
		SchemaVersion: domain.SchemaVersion, Payload: payload, Note: note,
	}
	if tid, err := uuid.Parse(c.tc.CorrelationID); err == nil {
		ev.CorrelationID = &tid
	}
	return s.events.Record(ctx, ev)
}

// refs checks, under RLS, that every reference of a write exists in this company and is usable.
type refChecks struct {
	customer, category, team, agent *uuid.UUID
}

func (s *Service) checkRefs(ctx context.Context, r refChecks) error {
	if err := s.mustExist(ctx, r.customer, s.repo.CustomerExists, "customer_id"); err != nil {
		return err
	}
	if err := s.mustExist(ctx, r.category, s.repo.ActiveCategoryExists, "category_id"); err != nil {
		return err
	}
	if err := s.mustExist(ctx, r.team, s.repo.QueueExists, "assigned_team_id"); err != nil {
		return err
	}
	return s.mustBeMember(ctx, r.agent, "assigned user")
}

func (s *Service) mustBeMember(ctx context.Context, id *uuid.UUID, field string) error {
	if id == nil {
		return nil
	}
	ok, err := s.cases.IsActiveMember(ctx, *id)
	if err != nil {
		return err
	}
	if !ok {
		return domain.Invalid("%s is not an active member of this company", field)
	}
	return nil
}

// CaseResult says whether the call created the case or returned the one already standing for the request.
type CaseResult struct {
	Case    *domain.Case
	Created bool
	Replay  bool
}

// OpenCase opens a case. It is deliberate (a person, the API or a flow decides): the system never makes one for a conversation by itself.
func (s *Service) OpenCase(ctx context.Context, raw domain.CaseOpenInput) (CaseResult, error) {
	c, err := s.who(ctx)
	if err != nil {
		return CaseResult{}, err
	}
	in, err := raw.Normalize()
	if err != nil {
		return CaseResult{}, err
	}
	if err := s.allow(ctx, c, PermCreate); err != nil {
		return CaseResult{}, err
	}
	if in.AssignedAgentID != nil || in.AssignedTeamID != nil {
		if err := s.allow(ctx, c, PermAssign); err != nil {
			return CaseResult{}, err
		}
	}
	hash := in.Hash()
	rec, err := s.replay(ctx, ports.ScopeCaseOpen, in.IdempotencyKey, hash)
	if err != nil {
		return CaseResult{}, err
	}
	if rec != nil && rec.CaseID != nil {
		prev, err := s.cases.GetCase(ctx, *rec.CaseID)
		if err != nil {
			return CaseResult{}, err
		}
		return CaseResult{Case: prev, Replay: true}, nil
	}
	var origin *domain.Session
	if in.SessionID != nil {
		if origin, err = s.repo.GetSession(ctx, *in.SessionID); err != nil {
			return CaseResult{}, err
		}
	}
	if err := s.checkRefs(ctx, refChecks{customer: in.CustomerID, category: in.CategoryID, team: in.AssignedTeamID, agent: in.AssignedAgentID}); err != nil {
		return CaseResult{}, err
	}
	// a case opened from a session inherits that session's customer when none was given: the customer is the one of the attendance
	customer := in.CustomerID
	if customer == nil && origin != nil {
		customer = origin.CustomerID
	}
	prefix, seq, err := s.repo.NextProtocol(ctx, "case", domain.DefaultCasePrefix, domain.DefaultCaseStart)
	if err != nil {
		return CaseResult{}, err
	}
	now, err := s.repo.Now(ctx)
	if err != nil {
		return CaseResult{}, err
	}
	cs := &domain.Case{
		ID: uuid.New(), TenantID: c.tc.TenantID, CaseNumber: domain.FormatProtocol(prefix, seq), CaseSequence: seq, Title: in.Title, Description: in.Description,
		CustomerID: customer, CategoryID: in.CategoryID, Priority: in.Priority, Status: domain.CaseOpen, AssignedTeamID: in.AssignedTeamID, AssignedAgentID: in.AssignedAgentID,
		OriginSessionID: in.SessionID, CreatedBy: c.actorID(), OpenedAt: now, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.cases.InsertCase(ctx, cs); err != nil {
		return CaseResult{}, err
	}
	if err := s.remember(ctx, ports.ScopeCaseOpen, in.IdempotencyKey, hash, cs.ID); err != nil {
		return CaseResult{}, err
	}
	l := links{caseID: &cs.ID, customer: cs.CustomerID}
	if origin != nil {
		if _, err := s.cases.LinkCaseSession(ctx, cs.ID, origin.ID, domain.CaseRelationOrigin, c.actorID(), now); err != nil {
			return CaseResult{}, err
		}
		l.session, l.conv, l.contact = &origin.ID, origin.ConversationID, origin.ContactID
	}
	if err := s.emitFor(ctx, c, domain.EvCaseOpened, "service_case", cs.ID, now, l,
		map[string]any{"case_number": cs.CaseNumber, "priority": string(cs.Priority), "from_session": origin != nil, "assigned": cs.AssignedAgentID != nil || cs.AssignedTeamID != nil}, ""); err != nil {
		return CaseResult{}, err
	}
	s.record(ctx, c, ActionCaseOpened, ResourceCase, cs.ID, map[string]any{"case_number": cs.CaseNumber, "from_session": origin != nil})
	s.metrics.Inc("service_cases_opened_total")
	return CaseResult{Case: cs, Created: true}, nil
}

func (s *Service) GetCase(ctx context.Context, id uuid.UUID) (*domain.Case, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, err
	}
	return s.cases.GetCase(ctx, id)
}

func (s *Service) ListCases(ctx context.Context, f ports.CaseFilter) ([]domain.Case, string, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, "", err
	}
	if f.Status != "" && !f.Status.Valid() {
		return nil, "", domain.Invalid("status")
	}
	if f.Priority != "" && !f.Priority.Valid() {
		return nil, "", domain.Invalid("priority")
	}
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	return s.cases.ListCases(ctx, f)
}

func caseLinks(cs *domain.Case) links {
	return links{caseID: &cs.ID, customer: cs.CustomerID}
}

// PatchCase edits the descriptive fields of a case that is not final.
func (s *Service) PatchCase(ctx context.Context, id uuid.UUID, raw domain.CasePatchInput, expectedVersion int) (*domain.Case, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermUpdate); err != nil {
		return nil, err
	}
	in, err := raw.Normalize()
	if err != nil {
		return nil, err
	}
	if in.Empty() {
		return nil, domain.Invalid("nothing to change")
	}
	cs, err := s.cases.LockCase(ctx, id)
	if err != nil {
		return nil, err
	}
	if expectedVersion <= 0 {
		return nil, domain.ErrPreconditionNeeded
	}
	if cs.Version != expectedVersion {
		return nil, domain.ErrConflict
	}
	if cs.Status.Final() {
		return nil, domain.ErrInvalidTransition
	}
	if err := s.checkRefs(ctx, refChecks{customer: in.CustomerID, category: in.CategoryID}); err != nil {
		return nil, err
	}
	changed := 0
	if in.Title != nil {
		cs.Title, changed = *in.Title, changed+1
	}
	if in.Description != nil {
		cs.Description, changed = *in.Description, changed+1
	}
	if in.Priority != nil {
		cs.Priority, changed = *in.Priority, changed+1
	}
	if in.CategoryID != nil {
		cs.CategoryID, changed = in.CategoryID, changed+1
	}
	if in.CustomerID != nil {
		cs.CustomerID, changed = in.CustomerID, changed+1
	}
	if err := s.cases.UpdateCase(ctx, cs, expectedVersion); err != nil {
		return nil, err
	}
	at, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.emitFor(ctx, c, domain.EvCaseUpdated, "service_case", cs.ID, at, caseLinks(cs), map[string]any{"fields": changed, "case_number": cs.CaseNumber}, ""); err != nil {
		return nil, err
	}
	s.record(ctx, c, ActionCaseUpdated, ResourceCase, cs.ID, map[string]any{"fields": changed})
	return cs, nil
}

// AssignCase sets who works the case (a team, a person, both or neither). Needs service.assign; the person must be an active member.
func (s *Service) AssignCase(ctx context.Context, id uuid.UUID, team, agent *uuid.UUID, expectedVersion int) (*domain.Case, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermAssign); err != nil {
		return nil, err
	}
	cs, err := s.cases.LockCase(ctx, id)
	if err != nil {
		return nil, err
	}
	if expectedVersion <= 0 {
		return nil, domain.ErrPreconditionNeeded
	}
	if cs.Version != expectedVersion {
		return nil, domain.ErrConflict
	}
	if cs.Status.Final() {
		return nil, domain.ErrInvalidTransition
	}
	if err := s.checkRefs(ctx, refChecks{team: team, agent: agent}); err != nil {
		return nil, err
	}
	cs.AssignedTeamID, cs.AssignedAgentID = team, agent
	if err := s.cases.UpdateCase(ctx, cs, expectedVersion); err != nil {
		return nil, err
	}
	at, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.emitFor(ctx, c, domain.EvCaseAssigned, "service_case", cs.ID, at, caseLinks(cs),
		map[string]any{"case_number": cs.CaseNumber, "team": team != nil, "agent": agent != nil}, ""); err != nil {
		return nil, err
	}
	s.record(ctx, c, ActionCaseAssigned, ResourceCase, cs.ID, map[string]any{"team": team != nil, "agent": agent != nil})
	return cs, nil
}

// SetCaseStatus moves a case. Resolving needs the resolution text; cancelling and reopening a resolved case need a reason. A case that is closed or cancelled
// is final. Nothing here touches a session or a task: the states are independent (ADR-10).
func (s *Service) SetCaseStatus(ctx context.Context, id uuid.UUID, raw domain.CaseStatusInput, expectedVersion int) (*domain.Case, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	cs, err := s.cases.LockCase(ctx, id)
	if err != nil {
		return nil, err
	}
	in, err := raw.Normalize(cs.Status)
	if err != nil {
		return nil, err
	}
	perm := PermUpdate
	if in.To == domain.CaseResolved || in.To == domain.CaseClosed || in.To == domain.CaseCancelled || cs.Status == domain.CaseResolved {
		perm = PermClose
	}
	if err := s.allow(ctx, c, perm); err != nil {
		return nil, err
	}
	if expectedVersion <= 0 {
		return nil, domain.ErrPreconditionNeeded
	}
	if cs.Version != expectedVersion {
		return nil, domain.ErrConflict
	}
	if !domain.CanCaseTransition(cs.Status, in.To) {
		return nil, domain.ErrInvalidTransition
	}
	at, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	from := cs.Status
	note := ""
	reopened := false
	switch {
	case in.To == domain.CaseResolved:
		res := in.Resolution
		cs.Resolution, cs.ResolvedAt = &res, &at
	case in.To == domain.CaseClosed:
		cs.ClosedAt = &at
	case in.To == domain.CaseCancelled:
		r := in.Reason
		cs.CancelReason, cs.CancelledAt, note = &r, &at, in.Reason
	case from == domain.CaseResolved: // reopening
		cs.Resolution, cs.ResolvedAt = nil, nil
		cs.ReopenCount++
		why := in.Reason
		cs.LastReopenReason = &why
		note, reopened = in.Reason, true
	}
	cs.Status = in.To
	if err := s.cases.UpdateCase(ctx, cs, expectedVersion); err != nil {
		return nil, err
	}
	if err := s.emitFor(ctx, c, domain.EvCaseStatus, "service_case", cs.ID, at, caseLinks(cs),
		map[string]any{"case_number": cs.CaseNumber, "from": string(from), "to": string(in.To), "reopen": reopened}, note); err != nil {
		return nil, err
	}
	s.record(ctx, c, ActionCaseStatus, ResourceCase, cs.ID, map[string]any{"from": string(from), "to": string(in.To)})
	s.metrics.Inc("service_case_status_total", string(in.To))
	return cs, nil
}

// LinkSessionToCase records that another attendance refers to the case (a customer calls again about the same problem). Idempotent; the case row does not change.
func (s *Service) LinkSessionToCase(ctx context.Context, caseID, sessionID uuid.UUID) (bool, error) {
	c, err := s.who(ctx)
	if err != nil {
		return false, err
	}
	if err := s.allow(ctx, c, PermUpdate); err != nil {
		return false, err
	}
	cs, err := s.cases.LockCase(ctx, caseID)
	if err != nil {
		return false, err
	}
	if cs.Status.Final() {
		return false, domain.ErrInvalidTransition
	}
	sess, err := s.repo.GetSession(ctx, sessionID)
	if err != nil {
		return false, err
	}
	at, err := s.repo.Now(ctx)
	if err != nil {
		return false, err
	}
	linked, err := s.cases.LinkCaseSession(ctx, caseID, sessionID, domain.CaseRelationReference, c.actorID(), at)
	if err != nil || !linked {
		return false, err
	}
	l := links{session: &sess.ID, caseID: &cs.ID, conv: sess.ConversationID, contact: sess.ContactID, customer: cs.CustomerID}
	if err := s.emitFor(ctx, c, domain.EvCaseSessionLinked, "service_case", cs.ID, at, l, map[string]any{"case_number": cs.CaseNumber, "protocol": sess.Protocol}, ""); err != nil {
		return false, err
	}
	s.record(ctx, c, ActionCaseLinked, ResourceCase, cs.ID, map[string]any{"protocol": sess.Protocol})
	return true, nil
}

func (s *Service) CaseSessions(ctx context.Context, id uuid.UUID) ([]domain.Session, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, err
	}
	if _, err := s.cases.GetCase(ctx, id); err != nil {
		return nil, err
	}
	return s.cases.SessionsOfCase(ctx, id)
}

func (s *Service) CaseTimeline(ctx context.Context, id uuid.UUID, cursor string, limit int, ascending bool) ([]ports.TimelineItem, string, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, "", err
	}
	if _, err := s.cases.GetCase(ctx, id); err != nil {
		return nil, "", err
	}
	return s.timeline.Timeline(ctx, ports.TimelineScope{CaseID: &id}, cursor, s.pageSize(limit), ascending)
}
