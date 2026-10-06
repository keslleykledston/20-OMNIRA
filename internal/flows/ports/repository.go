// Package ports holds the interfaces the flows application depends on. Every method runs inside the caller's tenant
// session (platformdb.WithTenantSession): the tenant is read from the TenantContext, never from an argument.
package ports

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/domain"
)

type ListFilter struct {
	Status domain.FlowStatus
	Type   domain.FlowType
	Limit  int
	Offset int
}

// Settings are the resolver-facing attributes of a flow, edited separately from the definition.
type Settings struct {
	Priority      int
	IsDefault     bool
	TriggerFilter domain.TriggerFilter
	RestartPolicy domain.RestartPolicy
}

type FlowRepository interface {
	CreateFlow(ctx context.Context, f *domain.Flow) error
	GetFlow(ctx context.Context, id uuid.UUID) (*domain.Flow, error)
	GetFlowBySlug(ctx context.Context, slug string) (*domain.Flow, error)
	ListFlows(ctx context.Context, filter ListFilter) ([]*domain.Flow, error)
	// SaveDraft is optimistic: it only applies when the stored revision equals expectedRevision.
	SaveDraft(ctx context.Context, id uuid.UUID, expectedRevision int, name, description string, definition json.RawMessage) (*domain.Flow, error)
	UpdateSettings(ctx context.Context, id uuid.UUID, s Settings) (*domain.Flow, error)
	// Publish snapshots the draft at expectedRevision into a new immutable version and makes it the active one,
	// atomically and serialized per flow. It never publishes content other than the revision the caller validated.
	Publish(ctx context.Context, id uuid.UUID, expectedRevision int, note string, by *uuid.UUID, pins map[string]uuid.UUID) (*domain.FlowVersion, error)
	// ActivateVersion points new runs at an existing version (rollback); history is untouched.
	ActivateVersion(ctx context.Context, flowID uuid.UUID, version int) (*domain.Flow, *domain.FlowVersion, error)
	Archive(ctx context.Context, id uuid.UUID) error
	// ExistsDefault tells whether the tenant already has a live (non-archived) default flow of this type.
	ExistsDefault(ctx context.Context, t domain.FlowType) (bool, error)
	// ActiveSubflow resolves a SUBFLOW flow of the tenant by slug to its currently active version (used to pin at publish).
	ActiveSubflow(ctx context.Context, slug string) (*domain.Flow, *domain.FlowVersion, error)
	GetVersion(ctx context.Context, id uuid.UUID) (*domain.FlowVersion, error)
	GetVersionByNumber(ctx context.Context, flowID uuid.UUID, version int) (*domain.FlowVersion, error)
	ListVersions(ctx context.Context, flowID uuid.UUID, limit, offset int) ([]*domain.FlowVersion, error)
	RecordPackInstallation(ctx context.Context, p *domain.PackInstallation) error
	RecordTemplateInstallation(ctx context.Context, t *domain.TemplateInstallation) error
	ListTemplateInstallations(ctx context.Context, flowID uuid.UUID) ([]*domain.TemplateInstallation, error)
}

// ResourceChecker confirms that referenced resources belong to the SESSION tenant (a foreign or unknown id is simply absent).
type ResourceChecker interface {
	ExistingQueues(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error)
	ExistingConnections(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error)
}

// Auditor records administrative actions in the append-only audit log. Failures must not hide the action's own result.
type Auditor interface {
	Record(ctx context.Context, action string, flowID uuid.UUID, meta map[string]any)
}

// Atomic runs fn so that, when it returns an error, everything fn wrote is undone, while the caller's transaction (which the
// request middleware may still commit) stays usable. The Postgres implementation is a savepoint.
type Atomic interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// RunSummary is one row of the runs list (read model).
type RunSummary struct {
	ID             uuid.UUID  `json:"id"`
	FlowID         uuid.UUID  `json:"flow_id"`
	FlowSlug       string     `json:"flow_slug"`
	FlowName       string     `json:"flow_name"`
	FlowVersion    int        `json:"flow_version"`
	ConversationID uuid.UUID  `json:"conversation_id"`
	Status         string     `json:"status"`
	CurrentNodeID  string     `json:"current_node_id,omitempty"`
	NodeExecCount  int        `json:"node_executions"`
	Error          string     `json:"error,omitempty"`
	StartedAt      time.Time  `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	DurationMs     *int64     `json:"duration_ms,omitempty"`
}

type RunFilter struct {
	FlowID         *uuid.UUID
	ConversationID *uuid.UUID
	Status         string
	Limit, Offset  int
}

// ExecutionView is one step of a run's timeline (inputs/outputs were redacted before they were stored).
type ExecutionView struct {
	Seq        int             `json:"seq"`
	NodeID     string          `json:"node_id"`
	NodeType   string          `json:"node_type"`
	Status     string          `json:"status"`
	Port       string          `json:"port,omitempty"`
	Input      json.RawMessage `json:"input"`
	Output     json.RawMessage `json:"output"`
	Error      string          `json:"error,omitempty"`
	StartedAt  time.Time       `json:"started_at"`
	DurationMs int             `json:"duration_ms"`
}

type RunDetail struct {
	RunSummary
	Variables  map[string]any  `json:"variables"`
	Handoff    map[string]any  `json:"handoff,omitempty"`
	Executions []ExecutionView `json:"timeline"`
}

type NodeCount struct {
	NodeID   string `json:"node_id"`
	NodeType string `json:"node_type,omitempty"`
	Count    int    `json:"count"`
}

// FlowAnalytics are measured numbers only: with no runs every figure is zero (nothing is estimated or invented).
type FlowAnalytics struct {
	Since        time.Time      `json:"since"`
	Days         int            `json:"days"`
	Runs         int            `json:"runs"`
	ByStatus     map[string]int `json:"by_status"`
	Completed    int            `json:"completed"`
	Failed       int            `json:"failed"`
	HumanHandoff int            `json:"human_handoffs"`
	AvgDuration  *int64         `json:"avg_duration_ms"` // completed runs only; null when none
	NodeErrors   []NodeCount    `json:"node_errors"`
	DropOff      []NodeCount    `json:"drop_off_by_node"` // where failed/cancelled/expired runs stopped
}

// RunReader is the read side used by the runs/timeline/analytics endpoints.
type RunReader interface {
	ListRuns(ctx context.Context, f RunFilter) ([]RunSummary, error)
	GetRunDetail(ctx context.Context, id uuid.UUID) (*RunDetail, error)
	FlowAnalytics(ctx context.Context, flowID uuid.UUID, days int) (*FlowAnalytics, error)
}
