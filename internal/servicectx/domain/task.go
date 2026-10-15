package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

type TaskStatus string

const (
	TaskOpen       TaskStatus = "open"
	TaskInProgress TaskStatus = "in_progress"
	TaskDone       TaskStatus = "done"
	TaskCancelled  TaskStatus = "cancelled"
)

func (s TaskStatus) Valid() bool {
	return s == TaskOpen || s == TaskInProgress || s == TaskDone || s == TaskCancelled
}

// Pending is a task still to be done (what the context lists and what "overdue" is about).
func (s TaskStatus) Pending() bool { return s == TaskOpen || s == TaskInProgress }

var taskTransitions = map[TaskStatus][]TaskStatus{
	TaskOpen:       {TaskInProgress, TaskDone, TaskCancelled},
	TaskInProgress: {TaskOpen, TaskDone, TaskCancelled},
	TaskDone:       {TaskOpen}, // reopened by somebody with service.close; cancelled is final
}

func CanTaskTransition(from, to TaskStatus) bool {
	for _, t := range taskTransitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// Task origins say WHO or WHAT created the task; where it belongs is in the real foreign keys (session, case, flow run), never in a loose (type, id) pair.
const (
	OriginHuman = "human"
	OriginFlow  = "flow"
	OriginAI    = "ai"
	OriginAPI   = "api"
)

func ValidOrigin(o string) bool {
	return o == OriginHuman || o == OriginFlow || o == OriginAI || o == OriginAPI
}

type Task struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	Title          string
	Description    string
	Status         TaskStatus
	Priority       Priority
	AssignedUserID *uuid.UUID
	AssignedTeamID *uuid.UUID
	DueAt          *time.Time
	CompletedAt    *time.Time
	CompletedBy    *uuid.UUID
	CancelledAt    *time.Time
	CancelReason   *string
	OriginType     string
	SessionID      *uuid.UUID
	CaseID         *uuid.UUID
	ContactID      *uuid.UUID
	FlowRunID      *uuid.UUID
	FlowSeq        *int
	CreatedBy      *uuid.UUID
	IdempotencyKey *string
	RequestHash    *string
	ReopenCount    int
	Version        int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Overdue says whether the task is pending past its due time.
func (t Task) Overdue(now time.Time) bool {
	return t.Status.Pending() && t.DueAt != nil && t.DueAt.Before(now)
}

type TaskOpenInput struct {
	Title          string
	Description    string
	Priority       Priority
	AssignedUserID *uuid.UUID
	AssignedTeamID *uuid.UUID
	DueAt          *time.Time
	SessionID      *uuid.UUID
	CaseID         *uuid.UUID
	ContactID      *uuid.UUID
	OriginType     string // decided by the trusted caller (the HTTP handler says human, a flow node says flow ...), never read from a request body
	FlowRunID      *uuid.UUID
	FlowSeq        *int
	IdempotencyKey string
}

const maxDueHorizon = 5 * 365 * 24 * time.Hour

func (in TaskOpenInput) Normalize(now time.Time) (TaskOpenInput, error) {
	out := in
	title, err := CleanText(in.Title, MaxTitleLen, "title")
	if err != nil {
		return TaskOpenInput{}, err
	}
	if title == "" || strings.ContainsAny(title, "\n") {
		return TaskOpenInput{}, invalid("a task needs a one-line title")
	}
	out.Title = title
	if out.Description, err = CleanText(in.Description, MaxLongTextLen, "description"); err != nil {
		return TaskOpenInput{}, err
	}
	if out.Priority == "" {
		out.Priority = PriorityNormal
	}
	if !out.Priority.Valid() {
		return TaskOpenInput{}, invalid("priority")
	}
	if out.OriginType == "" {
		out.OriginType = OriginHuman
	}
	if !ValidOrigin(out.OriginType) {
		return TaskOpenInput{}, invalid("origin")
	}
	if (out.OriginType == OriginFlow) != (out.FlowRunID != nil) || (out.FlowRunID == nil) != (out.FlowSeq == nil) {
		return TaskOpenInput{}, invalid("a flow task carries its run and step, and only a flow task does")
	}
	if out.DueAt != nil {
		d := out.DueAt.UTC()
		if d.After(now.Add(maxDueHorizon)) {
			return TaskOpenInput{}, invalid("due_at is too far away")
		}
		out.DueAt = &d
	}
	if out.IdempotencyKey != "" && !ValidIdempotencyKey(out.IdempotencyKey) {
		return TaskOpenInput{}, invalid("Idempotency-Key must be 8-128 characters of letters, digits . _ : -")
	}
	return out, nil
}

func (in TaskOpenInput) Hash() string {
	due := "-"
	if in.DueAt != nil {
		due = in.DueAt.UTC().Format(time.RFC3339Nano)
	}
	return fingerprint("task", in.Title, in.Description, string(in.Priority), id(in.AssignedUserID), id(in.AssignedTeamID), due, id(in.SessionID), id(in.CaseID), id(in.ContactID), in.OriginType)
}

type TaskPatchInput struct {
	Title       *string
	Description *string
	Priority    *Priority
	DueAt       *time.Time
	ClearDue    bool
}

func (in TaskPatchInput) Empty() bool {
	return in.Title == nil && in.Description == nil && in.Priority == nil && in.DueAt == nil && !in.ClearDue
}

func (in TaskPatchInput) Normalize(now time.Time) (TaskPatchInput, error) {
	out := in
	if in.Title != nil {
		t, err := CleanText(*in.Title, MaxTitleLen, "title")
		if err != nil {
			return TaskPatchInput{}, err
		}
		if t == "" || strings.ContainsAny(t, "\n") {
			return TaskPatchInput{}, invalid("a task needs a one-line title")
		}
		out.Title = &t
	}
	if in.Description != nil {
		d, err := CleanText(*in.Description, MaxLongTextLen, "description")
		if err != nil {
			return TaskPatchInput{}, err
		}
		out.Description = &d
	}
	if in.Priority != nil && !in.Priority.Valid() {
		return TaskPatchInput{}, invalid("priority")
	}
	if in.DueAt != nil {
		if in.ClearDue {
			return TaskPatchInput{}, invalid("due_at and clear_due together")
		}
		d := in.DueAt.UTC()
		if d.After(now.Add(maxDueHorizon)) {
			return TaskPatchInput{}, invalid("due_at is too far away")
		}
		out.DueAt = &d
	}
	return out, nil
}

// TaskReason is the text that cancelling (or reopening) a task needs.
func TaskReason(raw string) (string, error) {
	r, err := CleanText(raw, MaxReasonLen, "reason")
	if err != nil {
		return "", err
	}
	if len([]rune(r)) < MinReasonLen {
		return "", invalid("a reason is required")
	}
	return r, nil
}
