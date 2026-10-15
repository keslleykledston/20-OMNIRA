package domain

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// StatusClass is the ERP's own status normalized to a few classes the rules of this module can use (an order "done" is a fact; the ERP's label stays in
// ExternalStatus). The ERP is the authority: this is a projection.
type StatusClass string

const (
	StatusOpen       StatusClass = "open"
	StatusInProgress StatusClass = "in_progress"
	StatusDone       StatusClass = "done"
	StatusCancelled  StatusClass = "cancelled"
	StatusUnknown    StatusClass = "unknown"
)

func (s StatusClass) Valid() bool {
	switch s {
	case StatusOpen, StatusInProgress, StatusDone, StatusCancelled, StatusUnknown:
		return true
	}
	return false
}

const (
	SyncSynced   = "synced"
	SyncPending  = "pending"
	SyncStale    = "stale"
	SyncConflict = "conflict"
	SyncError    = "error"

	ExternalTypeServiceOrder = "service_order"
)

type ExternalRecord struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	IntegrationID   uuid.UUID
	ExternalSystem  string
	ExternalType    string
	ExternalID      string
	DisplayRef      string
	ExternalStatus  string
	StatusClass     StatusClass
	SyncState       string
	SourceUpdatedAt *time.Time
	LastSyncedAt    *time.Time
	CreatedBy       *uuid.UUID
	Version         int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ExternalLink struct {
	ID               uuid.UUID
	TenantID         uuid.UUID
	ExternalRecordID uuid.UUID
	SessionID        *uuid.UUID
	CaseID           *uuid.UUID
	TaskID           *uuid.UUID
	LinkedBy         *uuid.UUID
	LinkedAt         time.Time
	UnlinkedAt       *time.Time
	UnlinkedBy       *uuid.UUID
}

// Snapshot is what the ERP says about a record right now. UpdatedAt is the PROVIDER's own update time: it orders snapshots, so a late one never rolls the projection back.
type Snapshot struct {
	ExternalType   string
	ExternalID     string
	DisplayRef     string
	ExternalStatus string
	StatusClass    StatusClass
	UpdatedAt      *time.Time
}

func (s Snapshot) Clean() (Snapshot, error) {
	out := s
	if !IsToken(out.ExternalType) {
		return Snapshot{}, invalid("the ERP answered with an unusable record type")
	}
	out.ExternalID = strings.TrimSpace(out.ExternalID)
	if out.ExternalID == "" || utf8.RuneCountInString(out.ExternalID) > 120 {
		return Snapshot{}, invalid("the ERP answered with an unusable record id")
	}
	if utf8.RuneCountInString(out.DisplayRef) > 120 {
		out.DisplayRef = string([]rune(out.DisplayRef)[:120])
	}
	if utf8.RuneCountInString(out.ExternalStatus) > 80 {
		out.ExternalStatus = string([]rune(out.ExternalStatus)[:80])
	}
	if !out.StatusClass.Valid() {
		out.StatusClass = StatusUnknown
	}
	if out.UpdatedAt != nil { // the database keeps microseconds: compare what it can keep
		t := out.UpdatedAt.UTC().Truncate(time.Microsecond)
		out.UpdatedAt = &t
	}
	return out, nil
}

// LinkInput ties an ERP record to exactly one session, case or task.
type LinkInput struct {
	IntegrationID uuid.UUID
	ExternalType  string
	ExternalID    string
	SessionID     *uuid.UUID
	CaseID        *uuid.UUID
	TaskID        *uuid.UUID
}

func (in LinkInput) Normalize() (LinkInput, error) {
	out := in
	if out.IntegrationID == uuid.Nil {
		return LinkInput{}, invalid("integration_id is required")
	}
	if !IsToken(out.ExternalType) {
		return LinkInput{}, invalid("external_type must be a lower case token")
	}
	out.ExternalID = strings.TrimSpace(out.ExternalID)
	if out.ExternalID == "" || utf8.RuneCountInString(out.ExternalID) > 120 || strings.ContainsAny(out.ExternalID, "\n\r\t") {
		return LinkInput{}, invalid("external_id is required (up to 120 characters, one line)")
	}
	n := 0
	for _, p := range []*uuid.UUID{out.SessionID, out.CaseID, out.TaskID} {
		if p != nil {
			n++
		}
	}
	if n != 1 {
		return LinkInput{}, invalid("a record is linked to exactly one of session_id, case_id, task_id")
	}
	return out, nil
}

type RequestState string

const (
	RequestInFlight         RequestState = "in_flight"
	RequestConfirmedSuccess RequestState = "confirmed_success"
	RequestConfirmedFailure RequestState = "confirmed_failure"
	RequestOutcomeUnknown   RequestState = "outcome_unknown"
)

// Blocking says a new request for the same session must wait: the first one may have been processed by the ERP.
func (s RequestState) Blocking() bool { return s == RequestInFlight || s == RequestOutcomeUnknown }

const OperationCreateServiceOrder = "create_service_order"

type ExternalRequest struct {
	ID               uuid.UUID
	TenantID         uuid.UUID
	IntegrationID    uuid.UUID
	SessionID        uuid.UUID
	CaseID           *uuid.UUID
	Operation        string
	Title            string
	IdempotencyKey   string
	RequestHash      string
	CorrelationKey   string
	State            RequestState
	ExternalRecordID *uuid.UUID
	FailureClass     *string
	ResolutionSource *string
	RequestedBy      *uuid.UUID
	ResolvedBy       *uuid.UUID
	ResolvedAt       *time.Time
	Version          int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// RequestInput asks the ERP to create a service order for a session. Title and Description are what the ERP will receive; only the title is kept here.
type RequestInput struct {
	IntegrationID  uuid.UUID
	SessionID      uuid.UUID
	CaseID         *uuid.UUID
	Title          string
	Description    string
	IdempotencyKey string
}

func (in RequestInput) Normalize() (RequestInput, error) {
	out := in
	if out.IntegrationID == uuid.Nil || out.SessionID == uuid.Nil {
		return RequestInput{}, invalid("integration_id and the session are required")
	}
	if !ValidIdempotencyKey(out.IdempotencyKey) {
		return RequestInput{}, invalid("an Idempotency-Key of 8-128 characters of letters, digits . _ : - is required to ask the ERP for anything")
	}
	title, err := CleanText(in.Title, 120, "title")
	if err != nil {
		return RequestInput{}, err
	}
	if title == "" || strings.ContainsAny(title, "\n") {
		return RequestInput{}, invalid("a one-line title is required")
	}
	out.Title = title
	if out.Description, err = CleanText(in.Description, 2000, "description"); err != nil {
		return RequestInput{}, err
	}
	return out, nil
}

func (in RequestInput) Hash() string {
	return fingerprint("ext-request", in.IntegrationID.String(), in.SessionID.String(), id(in.CaseID), in.Title, in.Description)
}

// ResolveInput settles by hand a request whose outcome is unknown, after somebody checked the ERP.
type ResolveInput struct {
	Created    bool
	ExternalID string // the id of the record the ERP made (when Created)
	Reason     string // why nothing was created (when not)
}

func (in ResolveInput) Normalize() (ResolveInput, error) {
	out := in
	if in.Created {
		out.ExternalID = strings.TrimSpace(in.ExternalID)
		if out.ExternalID == "" || utf8.RuneCountInString(out.ExternalID) > 120 {
			return ResolveInput{}, invalid("external_id of the record the ERP made is required")
		}
		return out, nil
	}
	r, err := CleanText(in.Reason, MaxReasonLen, "reason")
	if err != nil {
		return ResolveInput{}, err
	}
	if utf8.RuneCountInString(r) < MinReasonLen {
		return ResolveInput{}, invalid("a reason is required to say that nothing was created")
	}
	out.Reason = r
	return out, nil
}

// Events of the external side.
const (
	EvExternalLinked        = "external_record.linked"
	EvExternalUnlinked      = "external_record.unlinked"
	EvExternalStatusChanged = "external_record.status_changed"
	EvExternalRequested     = "external_request.created"
	EvExternalRequestState  = "external_request.state_changed"
)
