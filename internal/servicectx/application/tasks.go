package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

// TaskResult says whether the call created the task or returned the one already standing for the request (same key, or the same flow step replayed).
type TaskResult struct {
	Task    *domain.Task
	Created bool
	Replay  bool
}

func taskLinks(t *domain.Task) links {
	return links{session: t.SessionID, caseID: t.CaseID, contact: t.ContactID}
}

// CreateTask makes a pending action. It needs no case and no service order, and it can outlive the attendance that made it. Who created it (human, flow, ai,
// api) is decided by the trusted caller, never by a request body; an AI task only ever arrives here through an approved tool call (phase 6).
func (s *Service) CreateTask(ctx context.Context, raw domain.TaskOpenInput) (TaskResult, error) {
	c, err := s.who(ctx)
	if err != nil {
		return TaskResult{}, err
	}
	now, err := s.repo.Now(ctx)
	if err != nil {
		return TaskResult{}, err
	}
	in, err := raw.Normalize(now)
	if err != nil {
		return TaskResult{}, err
	}
	if err := s.allow(ctx, c, PermCreate); err != nil {
		return TaskResult{}, err
	}
	self := c.actorID()
	if in.AssignedTeamID != nil || (in.AssignedUserID != nil && (self == nil || *in.AssignedUserID != *self)) {
		if err := s.allow(ctx, c, PermAssign); err != nil {
			return TaskResult{}, err
		}
	}
	// only the flow engine's system session makes a flow task; a person cannot claim to be a flow
	if in.OriginType == domain.OriginFlow && !c.system {
		return TaskResult{}, domain.ErrForbidden
	}
	// a flow step is idempotent through the same receipt machine as everything else: its key IS the step, so a replayed step (even a concurrent one) returns the task
	// it already made, and the same step with other content is refused
	if in.FlowRunID != nil {
		if in.IdempotencyKey != "" {
			return TaskResult{}, domain.Invalid("a flow task takes its idempotency from the step, not from a key")
		}
		in.IdempotencyKey = fmt.Sprintf("flow:%s:%d", in.FlowRunID, *in.FlowSeq)
	}
	hash := in.Hash()
	rec, err := s.replay(ctx, ports.ScopeTaskCreate, in.IdempotencyKey, hash)
	if err != nil {
		return TaskResult{}, err
	}
	if rec != nil && rec.TaskID != nil {
		prev, err := s.tasks.GetTask(ctx, *rec.TaskID)
		if err != nil {
			return TaskResult{}, err
		}
		return TaskResult{Task: prev, Replay: true}, nil
	}
	contact := in.ContactID
	if in.ContactID != nil { // a contact the caller names has to exist (and be visible to them)
		if err := s.mustExist(ctx, in.ContactID, s.repo.ContactExists, "contact_id"); err != nil {
			return TaskResult{}, err
		}
	}
	if in.SessionID != nil {
		sess, err := s.repo.GetSession(ctx, *in.SessionID)
		if err != nil {
			return TaskResult{}, err
		}
		if contact == nil {
			contact = sess.ContactID // valid by the session's own foreign key
		} else if sess.ContactID != nil && *sess.ContactID != *contact {
			return TaskResult{}, domain.Invalid("contact_id is not the contact of the session")
		}
	}
	if in.CaseID != nil {
		cs, err := s.cases.GetCase(ctx, *in.CaseID)
		if err != nil {
			return TaskResult{}, err
		}
		if cs.Status.Final() {
			return TaskResult{}, domain.ErrInvalidTransition // a closed or cancelled case takes no new work
		}
	}
	if err := s.checkRefs(ctx, refChecks{team: in.AssignedTeamID, agent: in.AssignedUserID}); err != nil {
		return TaskResult{}, err
	}
	t := &domain.Task{
		ID: uuid.New(), TenantID: c.tc.TenantID, Title: in.Title, Description: in.Description, Status: domain.TaskOpen, Priority: in.Priority,
		AssignedUserID: in.AssignedUserID, AssignedTeamID: in.AssignedTeamID, DueAt: in.DueAt, OriginType: in.OriginType,
		SessionID: in.SessionID, CaseID: in.CaseID, ContactID: contact, FlowRunID: in.FlowRunID, FlowSeq: in.FlowSeq,
		CreatedBy: c.actorID(), Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.tasks.InsertTask(ctx, t); err != nil {
		return TaskResult{}, err
	}
	if err := s.remember(ctx, ports.ScopeTaskCreate, in.IdempotencyKey, hash, t.ID); err != nil {
		return TaskResult{}, err
	}
	if err := s.emitFor(ctx, c, domain.EvTaskCreated, "service_task", t.ID, now, taskLinks(t),
		map[string]any{"origin": t.OriginType, "priority": string(t.Priority), "assigned": t.AssignedUserID != nil || t.AssignedTeamID != nil, "has_due": t.DueAt != nil}, ""); err != nil {
		return TaskResult{}, err
	}
	s.record(ctx, c, ActionTaskCreated, ResourceTask, t.ID, map[string]any{"origin": t.OriginType})
	s.metrics.Inc("service_tasks_created_total", t.OriginType)
	return TaskResult{Task: t, Created: true}, nil
}

func (s *Service) GetTask(ctx context.Context, id uuid.UUID) (*domain.Task, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, err
	}
	return s.tasks.GetTask(ctx, id)
}

func (s *Service) ListTasks(ctx context.Context, f ports.TaskFilter) ([]domain.Task, string, error) {
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
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	now, err := s.repo.Now(ctx)
	if err != nil {
		return nil, "", err
	}
	return s.tasks.ListTasks(ctx, f, now)
}

// lockedTask locks and version-checks a task.
func (s *Service) lockedTask(ctx context.Context, id uuid.UUID, expectedVersion int) (*domain.Task, error) {
	t, err := s.tasks.LockTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if expectedVersion <= 0 {
		return nil, domain.ErrPreconditionNeeded
	}
	if t.Version != expectedVersion {
		return nil, domain.ErrConflict
	}
	return t, nil
}

func (s *Service) PatchTask(ctx context.Context, id uuid.UUID, raw domain.TaskPatchInput, expectedVersion int) (*domain.Task, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermUpdate); err != nil {
		return nil, err
	}
	now, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	in, err := raw.Normalize(now)
	if err != nil {
		return nil, err
	}
	if in.Empty() {
		return nil, domain.Invalid("nothing to change")
	}
	t, err := s.lockedTask(ctx, id, expectedVersion)
	if err != nil {
		return nil, err
	}
	if !t.Status.Pending() {
		return nil, domain.ErrInvalidTransition
	}
	changed := 0
	if in.Title != nil {
		t.Title, changed = *in.Title, changed+1
	}
	if in.Description != nil {
		t.Description, changed = *in.Description, changed+1
	}
	if in.Priority != nil {
		t.Priority, changed = *in.Priority, changed+1
	}
	if in.DueAt != nil {
		t.DueAt, changed = in.DueAt, changed+1
	}
	if in.ClearDue {
		t.DueAt, changed = nil, changed+1
	}
	if err := s.tasks.UpdateTask(ctx, t, expectedVersion); err != nil {
		return nil, err
	}
	if err := s.emitFor(ctx, c, domain.EvTaskUpdated, "service_task", t.ID, now, taskLinks(t), map[string]any{"fields": changed}, ""); err != nil {
		return nil, err
	}
	s.record(ctx, c, ActionTaskUpdated, ResourceTask, t.ID, map[string]any{"fields": changed})
	return t, nil
}

// AssignTask sets who does the task. Needs service.assign (a person may take a task for themselves when they CREATE it, not later).
func (s *Service) AssignTask(ctx context.Context, id uuid.UUID, user, team *uuid.UUID, expectedVersion int) (*domain.Task, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermAssign); err != nil {
		return nil, err
	}
	t, err := s.lockedTask(ctx, id, expectedVersion)
	if err != nil {
		return nil, err
	}
	if !t.Status.Pending() {
		return nil, domain.ErrInvalidTransition
	}
	if err := s.checkRefs(ctx, refChecks{team: team, agent: user}); err != nil {
		return nil, err
	}
	t.AssignedUserID, t.AssignedTeamID = user, team
	if err := s.tasks.UpdateTask(ctx, t, expectedVersion); err != nil {
		return nil, err
	}
	at, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.emitFor(ctx, c, domain.EvTaskAssigned, "service_task", t.ID, at, taskLinks(t), map[string]any{"user": user != nil, "team": team != nil}, ""); err != nil {
		return nil, err
	}
	s.record(ctx, c, ActionTaskAssigned, ResourceTask, t.ID, map[string]any{"user": user != nil, "team": team != nil})
	return t, nil
}

// TaskAction is what a person does to a task's state.
type TaskAction string

const (
	TaskStart    TaskAction = "start"
	TaskComplete TaskAction = "complete"
	TaskCancel   TaskAction = "cancel"
	TaskReopen   TaskAction = "reopen"
)

// ActTask starts, completes, cancels or reopens a task. Completing it never resolves a case (ADR-10): this method touches the task and nothing else.
// Cancelling and reopening need a reason (kept as the note of the history event).
func (s *Service) ActTask(ctx context.Context, id uuid.UUID, action TaskAction, reason string, expectedVersion int) (*domain.Task, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	perm := PermClose
	var to domain.TaskStatus
	switch action {
	case TaskStart:
		perm, to = PermUpdate, domain.TaskInProgress
	case TaskComplete:
		to = domain.TaskDone
	case TaskCancel:
		to = domain.TaskCancelled
	case TaskReopen:
		to = domain.TaskOpen
	default:
		return nil, domain.Invalid("action")
	}
	note := ""
	if action == TaskCancel || action == TaskReopen {
		if note, err = domain.TaskReason(reason); err != nil {
			return nil, err
		}
	}
	if err := s.allow(ctx, c, perm); err != nil {
		return nil, err
	}
	t, err := s.lockedTask(ctx, id, expectedVersion)
	if err != nil {
		return nil, err
	}
	if !domain.CanTaskTransition(t.Status, to) {
		return nil, domain.ErrInvalidTransition
	}
	at, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	from := t.Status
	switch action {
	case TaskComplete:
		t.CompletedAt, t.CompletedBy = &at, c.actorID()
	case TaskCancel:
		r := note
		t.CancelledAt, t.CancelReason = &at, &r
	case TaskReopen:
		t.CompletedAt, t.CompletedBy = nil, nil
		t.ReopenCount++
		why := note
		t.LastReopenReason = &why
	}
	t.Status = to
	if err := s.tasks.UpdateTask(ctx, t, expectedVersion); err != nil {
		return nil, err
	}
	if err := s.emitFor(ctx, c, domain.EvTaskStatus, "service_task", t.ID, at, taskLinks(t),
		map[string]any{"from": string(from), "to": string(to), "action": string(action)}, note); err != nil {
		return nil, err
	}
	s.record(ctx, c, ActionTaskStatus, ResourceTask, t.ID, map[string]any{"from": string(from), "to": string(to)})
	s.metrics.Inc("service_task_status_total", string(to))
	return t, nil
}
