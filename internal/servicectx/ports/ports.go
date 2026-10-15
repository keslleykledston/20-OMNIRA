// Package ports holds what the service-context application needs from the outside world. Every method runs inside the caller's tenant-scoped
// transaction (the TenantContext in ctx); none takes a tenant id, and every statement filters by the context's tenant anyway.
package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

// ConversationFacts is what opening or closing a session needs to know about a conversation, read with a row lock.
type ConversationFacts struct {
	ID         uuid.UUID
	ContactID  *uuid.UUID // nil for an internal (staff) conversation
	Status     string     // open | closed
	Kind       string
	AssignedTo *uuid.UUID
	Channel    string // provider of the line (whatsapp, ...), "" when unknown
}

// Receipt scopes (one per kind of record that takes an Idempotency-Key).
const (
	ScopeSessionOpen = "session.open"
	ScopeCaseOpen    = "case.open"
	ScopeTaskCreate  = "task.create"
	ScopeExternalUpd = "external.update"
	ScopeFlowStep    = "flow.step" // a step of a flow that acted on a session (classify, close)
)

// Receipt is what an Idempotency-Key points to. Exactly one of the ids is set, by scope.
type Receipt struct {
	RequestHash string
	SessionID   *uuid.UUID
	CaseID      *uuid.UUID
	TaskID      *uuid.UUID
	RecordID    *uuid.UUID
}

// SessionFilter narrows a listing. Cursor is opaque to callers (keyset on opened_at, id).
type SessionFilter struct {
	Status         domain.SessionStatus
	ConversationID *uuid.UUID
	ContactID      *uuid.UUID
	CustomerID     *uuid.UUID
	QueueID        *uuid.UUID
	Protocol       string
	OpenedFrom     *time.Time
	OpenedTo       *time.Time
	Cursor         string
	Limit          int
}

// MessageRef is a message that belongs to a session, by reference: no body is copied.
type MessageRef struct {
	ID         uuid.UUID
	Direction  string
	Type       string
	Status     string
	CreatedAt  time.Time
	SentByUser *uuid.UUID
}

type Repository interface {
	// LockConversation reads FOR UPDATE: the lock Finalize and the flow engine take. ErrNotFound when RLS hides it.
	LockConversation(ctx context.Context, id uuid.UUID) (*ConversationFacts, error)
	// ReadConversation is the same without the lock.
	ReadConversation(ctx context.Context, id uuid.UUID) (*ConversationFacts, error)

	// InSavepoint runs fn so that, if it fails, everything it did is undone and the surrounding transaction stays usable (the protocol number goes back too).
	InSavepoint(ctx context.Context, fn func(ctx context.Context) error) error

	// Now is the database clock (clock_timestamp): opened_at and closed_at come from the same clock that stamps messages, never from the app server's.
	Now(ctx context.Context) (time.Time, error)

	// FlowRunAttends says whether the run exists in this company and attends that conversation (the only runs the flow facade accepts).
	FlowRunAttends(ctx context.Context, runID, conversationID uuid.UUID) (bool, error)
	// LockScopedIdempotency serializes requests that carry the same key for one kind of record (transaction-scoped).
	LockScopedIdempotency(ctx context.Context, scope, key string) error
	// ReceiptFor reads the receipt an earlier request with this key left (nil when none); SaveReceipt leaves one. The receipt is left WHETHER the request created
	// the record or was answered with one that already stood, so a retry after the commit always gets the same answer.
	ReceiptFor(ctx context.Context, scope, key string) (*Receipt, error)
	SaveReceipt(ctx context.Context, scope, key, requestHash string, entityID uuid.UUID) error
	ActiveByConversation(ctx context.Context, conversationID uuid.UUID) (*domain.Session, error)
	LatestByConversation(ctx context.Context, conversationID uuid.UUID) (*domain.Session, error)
	GetSession(ctx context.Context, id uuid.UUID) (*domain.Session, error)
	// LockSession reads FOR UPDATE.
	LockSession(ctx context.Context, id uuid.UUID) (*domain.Session, error)
	ListSessions(ctx context.Context, f SessionFilter) ([]domain.Session, string, error)

	// NextProtocol takes the company's counter row (creating it at start) and returns the next number. It must run in the same transaction as InsertSession.
	// A row that only RESERVES a prefix (last_value 0) takes the start as its first number. ErrPrefixTaken when another company already uses the prefix.
	NextProtocol(ctx context.Context, kind, prefix string, start int64) (string, int64, error)
	// SessionPrefix reads the company's session counter: the prefix, the last number issued (0 = only reserved), and whether the row exists.
	SessionPrefix(ctx context.Context) (prefix string, last int64, found bool, err error)
	// TenantName is the company's trade name (or legal name), the source of the suggested prefix.
	TenantName(ctx context.Context) (string, error)
	InsertSession(ctx context.Context, s *domain.Session) error
	// UpdateSession writes the mutable columns when the stored version is expectedVersion; ErrConflict otherwise (ErrNotFound if it is gone or hidden).
	UpdateSession(ctx context.Context, s *domain.Session, expectedVersion int) error

	// Existence checks run under RLS, so a row of another company (or one the caller cannot see) answers false instead of failing a foreign key.
	CustomerExists(ctx context.Context, id uuid.UUID) (bool, error)
	QueueExists(ctx context.Context, id uuid.UUID) (bool, error)
	ContactExists(ctx context.Context, id uuid.UUID) (bool, error)
	ActiveCategoryExists(ctx context.Context, id uuid.UUID) (bool, error)
	// ResolveResult returns the canonical code a result code stands for: built in, or an ACTIVE company code.
	ResolveResult(ctx context.Context, code string) (canonical string, err error)

	// OpenEpisode starts a stretch of the session (the first one at opening, another at each reopening); EndEpisode ends the open one.
	OpenEpisode(ctx context.Context, sessionID uuid.UUID, startedAt time.Time) error
	EndEpisode(ctx context.Context, sessionID uuid.UUID, endedAt time.Time) error

	// FirstHumanReplyAt is the first outbound message sent by a person inside the session's episodes, nil when none.
	FirstHumanReplyAt(ctx context.Context, s *domain.Session) (*time.Time, error)
	// SessionMessages lists the messages created inside the session's episodes, oldest first, by keyset cursor.
	SessionMessages(ctx context.Context, s *domain.Session, cursor string, limit int) ([]MessageRef, string, error)
	// HasLaterSession tells whether the conversation has a session that opened after this one (a reopen is only for the latest).
	HasLaterSession(ctx context.Context, s *domain.Session) (bool, error)
}

// LinkCounter says how many operational links a session gathered; it refines the result of a plain finalization. Optional.
type LinkCounter interface {
	CountLinks(ctx context.Context, sessionID uuid.UUID) (externalOrders, cases, tasks int, err error)
}

// Authorizer answers from the permission matrix (never by role name). In a delegated context it answers from the grant.
type Authorizer interface {
	Has(ctx context.Context, userID uuid.UUID, permission string) (bool, error)
}

// Auditor writes to the append-only audit log inside the same transaction; ids, enums and counts only.
type Auditor interface {
	Record(ctx context.Context, action string, resourceType string, resourceID uuid.UUID, meta map[string]any)
}

// Events appends to the operational timeline in the same transaction as the change it describes. A failure aborts the change: history and state do not diverge.
type Events interface {
	Record(ctx context.Context, e domain.ContextEvent) error
}

// NopEvents is the sink used before the timeline exists (and in unit tests that do not care).
type NopEvents struct{}

func (NopEvents) Record(context.Context, domain.ContextEvent) error { return nil }

// Category and ResultCode are the company's own catalogs.
type Category struct {
	ID        uuid.UUID
	Code      string
	Label     string
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ResultCode struct {
	ID            uuid.UUID
	Code          string
	CanonicalCode string
	Label         string
	Active        bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type CatalogRepository interface {
	// ReserveSessionPrefix sets the company's protocol prefix while no protocol was issued (ErrPrefixLocked afterwards, ErrPrefixTaken when it belongs to another
	// company). The database also asks for tenant.manage.
	ReserveSessionPrefix(ctx context.Context, prefix string) error
	ListCategories(ctx context.Context, includeInactive bool) ([]Category, error)
	CreateCategory(ctx context.Context, c *Category) error
	UpdateCategory(ctx context.Context, id uuid.UUID, label *string, active *bool) (*Category, error)
	ListResultCodes(ctx context.Context, includeInactive bool) ([]ResultCode, error)
	CreateResultCode(ctx context.Context, c *ResultCode) error
	UpdateResultCode(ctx context.Context, id uuid.UUID, label *string, active *bool) (*ResultCode, error)
}

// TimelineScope picks whose history is read: exactly one field is set.
type TimelineScope struct {
	SessionID  *uuid.UUID
	CaseID     *uuid.UUID
	ContactID  *uuid.UUID
	CustomerID *uuid.UUID
}

// TimelineItem is one entry of the timeline. Derived entries are not stored here: they are the routing module's own history (who held the conversation
// when), shown in the session's timeline by reference and never copied.
type TimelineItem struct {
	ID             uuid.UUID
	EventType      string
	EntityType     string
	EntityID       uuid.UUID
	SessionID      *uuid.UUID
	CaseID         *uuid.UUID
	ConversationID *uuid.UUID
	ContactID      *uuid.UUID
	CustomerID     *uuid.UUID
	ActorType      string
	ActorID        *uuid.UUID
	SourceSystem   string
	OccurredAt     time.Time
	SchemaVersion  int
	Payload        map[string]any
	Note           string
	CorrelationID  *uuid.UUID
	Derived        bool
}

type LinkedCustomer struct {
	ID        uuid.UUID
	Name      string
	IsPrimary bool
}

type TimelineRepository interface {
	// Timeline reads one scope, ordered by (occurred_at, id) in the given direction, by keyset cursor.
	Timeline(ctx context.Context, scope TimelineScope, cursor string, limit int, ascending bool) ([]TimelineItem, string, error)
	// LinkedCustomers are the active customer accounts a contact is tied to (a contact may serve several companies).
	LinkedCustomers(ctx context.Context, contactID uuid.UUID) ([]LinkedCustomer, error)
	// SessionsOfContact are the contact's most recent sessions; with a customer, those of that customer plus those with none.
	SessionsOfContact(ctx context.Context, contactID uuid.UUID, customerID *uuid.UUID, limit int) ([]domain.Session, error)
}

// ---- cases and tasks (ADR-0049 phase 3) ----

type CaseFilter struct {
	Status          domain.CaseStatus
	CustomerID      *uuid.UUID
	AssignedAgentID *uuid.UUID
	AssignedTeamID  *uuid.UUID
	SessionID       *uuid.UUID // cases linked to this session
	Priority        domain.Priority
	CaseNumber      string
	OnlyWorking     bool
	Cursor          string
	Limit           int
}

type CaseRepository interface {
	// LockCase reads FOR UPDATE. GetCase is the plain read.
	LockCase(ctx context.Context, id uuid.UUID) (*domain.Case, error)
	GetCase(ctx context.Context, id uuid.UUID) (*domain.Case, error)
	ListCases(ctx context.Context, f CaseFilter) ([]domain.Case, string, error)
	InsertCase(ctx context.Context, c *domain.Case) error
	UpdateCase(ctx context.Context, c *domain.Case, expectedVersion int) error
	// LinkCaseSession records that a session refers to a case. It reports false (and changes nothing) when the link already stood.
	LinkCaseSession(ctx context.Context, caseID, sessionID uuid.UUID, relation string, by *uuid.UUID, at time.Time) (bool, error)
	SessionsOfCase(ctx context.Context, caseID uuid.UUID) ([]domain.Session, error)
	IsActiveMember(ctx context.Context, userID uuid.UUID) (bool, error)
}

type TaskFilter struct {
	Status         domain.TaskStatus
	PendingOnly    bool
	OverdueOnly    bool
	AssignedUserID *uuid.UUID
	AssignedTeamID *uuid.UUID
	SessionID      *uuid.UUID
	CaseID         *uuid.UUID
	ContactID      *uuid.UUID
	Cursor         string
	Limit          int
}

type TaskRepository interface {
	LockTask(ctx context.Context, id uuid.UUID) (*domain.Task, error)
	GetTask(ctx context.Context, id uuid.UUID) (*domain.Task, error)
	ListTasks(ctx context.Context, f TaskFilter, now time.Time) ([]domain.Task, string, error)
	InsertTask(ctx context.Context, t *domain.Task) error
	UpdateTask(ctx context.Context, t *domain.Task, expectedVersion int) error
}

// ContextRepository feeds the aggregated customer context with what is open.
type ContextRepository interface {
	// OpenCasesForContext are the working cases that matter for a contact: the ones its sessions are linked to and, when a customer is selected, that customer's.
	// needsSelection narrows to what is tied to no customer. A customer's case never appears under a contact that is not tied to that customer.
	OpenCasesForContext(ctx context.Context, contactID uuid.UUID, customerID *uuid.UUID, needsSelection bool, limit int) ([]domain.Case, error)
	PendingTasksForContext(ctx context.Context, contactID uuid.UUID, customerID *uuid.UUID, needsSelection bool, limit int) ([]domain.Task, error)
	// ExternalRecordsForContext are the ERP records linked (and not unlinked) to the sessions of the contact and to the cases those sessions belong to, under the same
	// customer rule as the open cases.
	ExternalRecordsForContext(ctx context.Context, contactID uuid.UUID, customerID *uuid.UUID, needsSelection bool, limit int) ([]domain.ExternalRecord, error)
}
