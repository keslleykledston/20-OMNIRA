package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

type CaseStatus string

const (
	CaseOpen          CaseStatus = "open"
	CaseInvestigating CaseStatus = "investigating"
	CaseWaiting       CaseStatus = "waiting"
	CaseResolved      CaseStatus = "resolved"
	CaseClosed        CaseStatus = "closed"
	CaseCancelled     CaseStatus = "cancelled"
)

func (s CaseStatus) Valid() bool {
	switch s {
	case CaseOpen, CaseInvestigating, CaseWaiting, CaseResolved, CaseClosed, CaseCancelled:
		return true
	}
	return false
}

// Final: nothing leaves these. A problem that comes back after a case was closed is a NEW case, linked by the people who see it.
func (s CaseStatus) Final() bool { return s == CaseClosed || s == CaseCancelled }

// Working is a case still being worked (not resolved, not final).
func (s CaseStatus) Working() bool {
	return s == CaseOpen || s == CaseInvestigating || s == CaseWaiting
}

var caseTransitions = map[CaseStatus][]CaseStatus{
	CaseOpen:          {CaseInvestigating, CaseWaiting, CaseResolved, CaseCancelled},
	CaseInvestigating: {CaseWaiting, CaseResolved, CaseCancelled},
	CaseWaiting:       {CaseInvestigating, CaseResolved, CaseCancelled},
	// resolved is the one place a case can come back from: it is REOPENED (to investigating) with a reason, or closed for good
	CaseResolved: {CaseClosed, CaseInvestigating},
}

func CanCaseTransition(from, to CaseStatus) bool {
	for _, t := range caseTransitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

func (p Priority) Valid() bool {
	return p == PriorityLow || p == PriorityNormal || p == PriorityHigh || p == PriorityUrgent
}

const (
	DefaultCasePrefix           = "OC"
	DefaultCaseStart      int64 = 1000
	MaxTitleLen                 = 200
	MaxLongTextLen              = 4000
	MaxReasonLen                = 500
	MinReasonLen                = 3
	CaseRelationOrigin          = "origin"
	CaseRelationReference       = "reference"
)

type Case struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	CaseNumber      string
	CaseSequence    int64
	Title           string
	Description     string
	CustomerID      *uuid.UUID
	CategoryID      *uuid.UUID
	Priority        Priority
	Status          CaseStatus
	AssignedTeamID  *uuid.UUID
	AssignedAgentID *uuid.UUID
	OriginSessionID *uuid.UUID
	Resolution      *string
	CancelReason    *string
	CreatedBy       *uuid.UUID
	OpenedAt        time.Time
	ResolvedAt      *time.Time
	ClosedAt        *time.Time
	CancelledAt     *time.Time
	ReopenCount     int
	Version         int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CaseOpenInput starts a case. A case is NEVER created for a conversation by itself: a person, the API or a flow decides it is worth one.
type CaseOpenInput struct {
	Title           string
	Description     string
	CustomerID      *uuid.UUID
	CategoryID      *uuid.UUID
	Priority        Priority
	AssignedTeamID  *uuid.UUID
	AssignedAgentID *uuid.UUID
	SessionID       *uuid.UUID // the session it was opened from (kept as the origin link)
	IdempotencyKey  string
}

func (in CaseOpenInput) Normalize() (CaseOpenInput, error) {
	out := in
	title, err := CleanText(in.Title, MaxTitleLen, "title")
	if err != nil {
		return CaseOpenInput{}, err
	}
	if title == "" || strings.ContainsAny(title, "\n") {
		return CaseOpenInput{}, invalid("a case needs a one-line title")
	}
	out.Title = title
	if out.Description, err = CleanText(in.Description, MaxLongTextLen, "description"); err != nil {
		return CaseOpenInput{}, err
	}
	if out.Priority == "" {
		out.Priority = PriorityNormal
	}
	if !out.Priority.Valid() {
		return CaseOpenInput{}, invalid("priority")
	}
	if out.IdempotencyKey != "" && !ValidIdempotencyKey(out.IdempotencyKey) {
		return CaseOpenInput{}, invalid("Idempotency-Key must be 8-128 characters of letters, digits . _ : -")
	}
	return out, nil
}

func (in CaseOpenInput) Hash() string {
	return fingerprint("case", in.Title, in.Description, id(in.CustomerID), id(in.CategoryID), string(in.Priority), id(in.AssignedTeamID), id(in.AssignedAgentID), id(in.SessionID))
}

type CasePatchInput struct {
	Title       *string
	Description *string
	Priority    *Priority
	CategoryID  *uuid.UUID
	CustomerID  *uuid.UUID
}

func (in CasePatchInput) Empty() bool {
	return in.Title == nil && in.Description == nil && in.Priority == nil && in.CategoryID == nil && in.CustomerID == nil
}

func (in CasePatchInput) Normalize() (CasePatchInput, error) {
	out := in
	if in.Title != nil {
		t, err := CleanText(*in.Title, MaxTitleLen, "title")
		if err != nil {
			return CasePatchInput{}, err
		}
		if t == "" || strings.ContainsAny(t, "\n") {
			return CasePatchInput{}, invalid("a case needs a one-line title")
		}
		out.Title = &t
	}
	if in.Description != nil {
		d, err := CleanText(*in.Description, MaxLongTextLen, "description")
		if err != nil {
			return CasePatchInput{}, err
		}
		out.Description = &d
	}
	if in.Priority != nil && !in.Priority.Valid() {
		return CasePatchInput{}, invalid("priority")
	}
	return out, nil
}

// CaseStatusInput moves a case. Resolving needs the resolution; cancelling needs a reason; reopening (resolved -> investigating) needs a reason.
type CaseStatusInput struct {
	To         CaseStatus
	Resolution string
	Reason     string
}

func (in CaseStatusInput) Normalize(from CaseStatus) (CaseStatusInput, error) {
	out := in
	if !in.To.Valid() {
		return CaseStatusInput{}, invalid("status")
	}
	var err error
	switch {
	case in.To == CaseResolved:
		if out.Resolution, err = CleanText(in.Resolution, MaxLongTextLen, "resolution"); err != nil {
			return CaseStatusInput{}, err
		}
		if out.Resolution == "" {
			return CaseStatusInput{}, invalid("resolving a case needs the resolution")
		}
	case in.To == CaseCancelled || (from == CaseResolved && in.To == CaseInvestigating):
		if out.Reason, err = CleanText(in.Reason, MaxReasonLen, "reason"); err != nil {
			return CaseStatusInput{}, err
		}
		if len([]rune(out.Reason)) < MinReasonLen {
			return CaseStatusInput{}, invalid("cancelling or reopening a case needs a reason")
		}
	}
	return out, nil
}
