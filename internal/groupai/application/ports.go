// Package application holds the use cases of the group copilot (ADR-0050): the engine that reads a group and keeps its memory (it runs in
// the worker, never inside a request transaction), and the service the HTTP layer calls (items, imports, proposals). It depends on ports,
// never on a database, a provider or an HTTP type.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/groupai/domain"
)

var (
	ErrNotFound          = errors.New("groupai: not found")
	ErrDisabled          = errors.New("groupai: the group copilot is disabled")
	ErrGroupAIOff        = errors.New("groupai: the AI is not enabled for this group")
	ErrConflict          = errors.New("groupai: the record changed; reload and try again")
	ErrDuplicateImport   = errors.New("groupai: this file was already imported into this group")
	ErrInfected          = errors.New("groupai: the file did not pass the antivirus")
	ErrNotScanned        = errors.New("groupai: the file could not be scanned")
	ErrModelUnavailable  = errors.New("groupai: no model is available")
	ErrBudget            = errors.New("groupai: the daily limit of AI calls for this group was reached")
	ErrProposalDecided   = errors.New("groupai: the proposal was already decided")
	ErrForbiddenCreation = errors.New("groupai: you lack the permission to create this record")
	ErrDuplicateItem     = errors.New("groupai: an equal item already exists")
	// ErrAborted: the work was cancelled meanwhile (the memory was erased, the AI turned off, the import discarded); nothing more is written.
	ErrAborted = errors.New("groupai: the work was cancelled meanwhile")
)

type GroupInfo struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Name      string
	Enabled   bool // collection opt-in (ADR-0015)
	AIEnabled bool // AI opt-in (ADR-0050 §1)
}

// GroupMessage is a stored message of a group, as the engine reads it.
type GroupMessage struct {
	ID                uuid.UUID
	ProviderMessageID string
	Author            string
	FromMe            bool
	Type              string
	Body              string
	SentAt            time.Time
	CreatedAt         time.Time
}

// GroupRef names a group across tenants, for the scheduler.
type GroupRef struct{ TenantID, GroupID uuid.UUID }

type ImportRef struct{ TenantID, GroupID, ImportID uuid.UUID }

type ItemFilter struct {
	Status       []domain.Status // empty = everything not superseded
	Kinds        []domain.Kind
	Tier         domain.Tier
	IncludeSuper bool
	OldestFirst  bool
	Query        string
	Limit        int
	OrderByRank  bool
}

// State is where the incremental reading of a group stopped.
type State struct {
	LastProcessedAt        *time.Time
	LastProcessedMessageID *uuid.UUID
	LastConsolidatedAt     *time.Time
	LastRunAt              *time.Time
	LastRunOK              *bool
	ConsecutiveFailures    int
	AnalyzeRequestedAt     *time.Time
	AnalyzeRequestedBy     *uuid.UUID
	ConsolidateRequestedAt *time.Time
	PromptVersion          string
	Runs                   int
	InputTokens            int64
	OutputTokens           int64
	BudgetDay              *time.Time
	BudgetCalls            int
}

// RunRecord is what a run adds to the state.
type RunRecord struct {
	At               time.Time
	OK               bool
	Failed           bool // an unusable answer at this cursor: counts toward the skip threshold
	PromptVersion    string
	InputTokens      int
	OutputTokens     int
	Calls            int
	LastProcessedAt  *time.Time // nil = unchanged
	LastProcessedID  *uuid.UUID
	Consolidated     bool
	ClearAnalyzeAsk  bool
	ClearConsolidAsk bool
}

type EventType string

const (
	EventCreated      EventType = "created"
	EventUpdated      EventType = "updated"
	EventResolved     EventType = "resolved"
	EventReopened     EventType = "reopened"
	EventConfirmed    EventType = "confirmed"
	EventPinned       EventType = "pinned"
	EventUnpinned     EventType = "unpinned"
	EventSuperseded   EventType = "superseded"
	EventConsolidated EventType = "consolidated"
	EventQuotesPurged EventType = "quotes_purged"
	EventDeleted      EventType = "deleted"
	EventMemoryErased EventType = "memory_erased"
)

type ItemEvent struct {
	TenantID  uuid.UUID
	GroupID   uuid.UUID
	ItemID    *uuid.UUID
	Type      EventType
	ActorType string // ai | human | system
	ActorUser *uuid.UUID
	Payload   map[string]any // ids, enums, counts only
}

// Store is everything the engine and the service persist. Every method runs on the querier of the context: a tenant session in the API
// and a system tenant session in the worker. A method never opens a transaction of its own, and none is held across a model call.
type Store interface {
	// groups and their messages
	Group(ctx context.Context, tenantID, groupID uuid.UUID) (GroupInfo, error)
	SetAIEnabled(ctx context.Context, tenantID, groupID uuid.UUID, enabled bool, by uuid.UUID) error
	MessagesAfter(ctx context.Context, tenantID, groupID uuid.UUID, afterAt *time.Time, afterID *uuid.UUID, limit int) ([]GroupMessage, error)
	CountMessagesAfter(ctx context.Context, tenantID, groupID uuid.UUID, afterAt *time.Time) (int, *time.Time, error)
	MessageKeys(ctx context.Context, tenantID, groupID uuid.UUID, from, to time.Time) (map[string]bool, error)

	// memory items
	ListItems(ctx context.Context, tenantID, groupID uuid.UUID, f ItemFilter) ([]domain.Item, error)
	GetItem(ctx context.Context, tenantID, itemID uuid.UUID, forUpdate bool) (domain.Item, error)
	// InsertItem stores a new item. When an equal live item exists (same fingerprint) nothing is inserted: the existing one is returned with created=false.
	InsertItem(ctx context.Context, it domain.Item) (stored domain.Item, created bool, err error)
	UpdateItem(ctx context.Context, it domain.Item, expectedVersion int) error
	// ReinforceItem raises the relevance of an item that was used or seen again (and may add evidence) WITHOUT bumping its version: a reinforcement
	// must not make a person's concurrent edit look stale.
	ReinforceItem(ctx context.Context, tenantID, itemID uuid.UUID, relevance float64, at time.Time, evidence []domain.Evidence) error
	DeleteItem(ctx context.Context, tenantID, itemID uuid.UUID) error
	EraseMemory(ctx context.Context, tenantID, groupID uuid.UUID) (int64, error)
	PurgeQuotes(ctx context.Context, tenantID, groupID uuid.UUID) (int64, error)
	AppendEvent(ctx context.Context, ev ItemEvent) error

	// state and scheduling
	GetState(ctx context.Context, tenantID, groupID uuid.UUID) (State, error)
	RecordRun(ctx context.Context, tenantID, groupID uuid.UUID, r RunRecord) error
	RequestAnalyze(ctx context.Context, tenantID, groupID, by uuid.UUID) error
	RequestConsolidate(ctx context.Context, tenantID, groupID uuid.UUID) error
	// BumpBudget counts one model call against the group's daily limit and reports whether it is still allowed.
	BumpBudget(ctx context.Context, tenantID, groupID uuid.UUID, day time.Time, limit int) (bool, error)
	// DueGroups lists, across tenants, the AI-enabled groups worth a look: asked for, or (auto) with unread messages, or due for consolidation.
	DueGroups(ctx context.Context, now time.Time, auto bool, limit int) ([]GroupRef, error)
	Touch(ctx context.Context, tenantID, groupID uuid.UUID, now time.Time) error

	// imports
	InsertImport(ctx context.Context, imp domain.Import, rawEnc, normalizedEnc []byte) (domain.Import, error)
	GetImport(ctx context.Context, tenantID, importID uuid.UUID, forUpdate bool) (domain.Import, error)
	ListImports(ctx context.Context, tenantID, groupID uuid.UUID, limit int) ([]domain.Import, error)
	UpdateImport(ctx context.Context, imp domain.Import) error
	// ImportNormalized returns the sealed normalized copy of an import. The raw is never read back by any use case.
	ImportNormalized(ctx context.Context, tenantID, importID uuid.UUID) (normalizedEnc []byte, err error)
	DiscardImport(ctx context.Context, tenantID, importID uuid.UUID, reason domain.DiscardReason, by *uuid.UUID, now time.Time) error
	DiscardGroupImports(ctx context.Context, tenantID, groupID uuid.UUID, reason domain.DiscardReason, now time.Time) (int64, error)
	PendingImports(ctx context.Context, limit int) ([]ImportRef, error)
	ExpiredImports(ctx context.Context, now time.Time, limit int) ([]ImportRef, error)

	// proposals
	InsertProposal(ctx context.Context, p domain.Proposal) (stored domain.Proposal, created bool, err error)
	GetProposal(ctx context.Context, tenantID, proposalID uuid.UUID, forUpdate bool) (domain.Proposal, error)
	LiveProposal(ctx context.Context, tenantID, itemID uuid.UUID, kind domain.ProposalKind) (domain.Proposal, bool, error)
	ListProposals(ctx context.Context, tenantID, groupID uuid.UUID, status string, limit int) ([]domain.Proposal, error)
	UpdateProposal(ctx context.Context, p domain.Proposal, expectedVersion int) error
}

// Sessions runs fn in a system session scoped to one tenant. The worker uses it; the API already runs inside the caller's session.
type Sessions func(ctx context.Context, tenantID uuid.UUID, fn func(ctx context.Context) error) error

// Cipher seals and opens the raw of an import (ADR-0050 §6).
type Cipher interface {
	Seal(plain []byte) ([]byte, error)
	Open(sealed []byte) ([]byte, error)
}

// Scanner is the antivirus (the media module's port): any error means "not scanned".
type Scanner interface {
	Scan(ctx context.Context, data []byte) (infected bool, signature string, err error)
}

// ServiceBridge is the way an approved proposal becomes a case or a task (ADR-0049). It runs under the approving person's session, so the
// module asks that person's own keys (service.create): the AI has none. The key makes a repeated approval return the same record.
type ServiceBridge interface {
	OpenCase(ctx context.Context, title, description, priority, idempotencyKey string) (uuid.UUID, error)
	CreateTask(ctx context.Context, title, description, priority string, due *time.Time, idempotencyKey string) (uuid.UUID, error)
}
