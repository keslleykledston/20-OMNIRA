package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// FlowRun is one execution of one pinned FlowVersion. A run never migrates to a newer version.
type FlowRun struct {
	ID                      uuid.UUID
	TenantID                uuid.UUID
	FlowID                  uuid.UUID
	FlowVersionID           uuid.UUID
	ConversationID          uuid.UUID
	ContactID               *uuid.UUID
	ActiveCustomerAccountID *uuid.UUID
	Status                  RunStatus
	CurrentNodeID           string
	Variables               map[string]any
	CallStack               []CallFrame
	WaitUntil               *time.Time
	TriggerEventID          string // inbound message id: the idempotency key of run creation
	LastEventID             string // last inbound message id consumed (idempotency of resume)
	NodeExecCount           int
	Error                   string
	StartedAt               time.Time
	UpdatedAt               time.Time
	CompletedAt             *time.Time
}

// CallFrame is a subflow return point; versions are pinned at publish time.
type CallFrame struct {
	FlowVersionID uuid.UUID `json:"flow_version_id"`
	ReturnNodeID  string    `json:"return_node_id"`
}

// NodeExecution is the append-only audit record of one node step (already redacted).
type NodeExecution struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	FlowRunID     uuid.UUID
	Seq           int
	FlowVersionID uuid.UUID
	NodeID        string
	NodeType      NodeType
	Status        NodeExecStatus
	Port          string
	Input         json.RawMessage
	Output        json.RawMessage
	Error         string
	StartedAt     time.Time
	CompletedAt   time.Time
	DurationMs    int
}

// TemplateInstallation records provenance only; the installed flow has no runtime link to the template.
type TemplateInstallation struct {
	ID                 uuid.UUID
	TenantID           uuid.UUID
	TemplateSlug       string
	TemplateVersion    int
	FlowID             uuid.UUID
	PackInstallationID *uuid.UUID
	Mappings           map[string]string
	InstalledBy        *uuid.UUID
	InstalledAt        time.Time
}

type PackInstallation struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	PackSlug          string
	PackVersion       int
	SelectedTemplates []string
	Mappings          map[string]string
	InstalledBy       *uuid.UUID
	InstalledAt       time.Time
}
