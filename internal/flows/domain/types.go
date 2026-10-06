// Package domain is the pure model of the Flow Builder (ADR-0019): no I/O, no SQL, no HTTP.
package domain

import "errors"

type FlowType string

const (
	FlowTypeInbound    FlowType = "INBOUND"
	FlowTypeInternal   FlowType = "INTERNAL"
	FlowTypeSubflow    FlowType = "SUBFLOW"
	FlowTypeEvent      FlowType = "EVENT"
	FlowTypeSurvey     FlowType = "SURVEY"
	FlowTypeAfterHours FlowType = "AFTER_HOURS"
	FlowTypeAIWorkflow FlowType = "AI_WORKFLOW"
)

func (t FlowType) Valid() bool {
	switch t {
	case FlowTypeInbound, FlowTypeInternal, FlowTypeSubflow, FlowTypeEvent, FlowTypeSurvey, FlowTypeAfterHours, FlowTypeAIWorkflow:
		return true
	}
	return false
}

type FlowStatus string

const (
	FlowStatusDraft     FlowStatus = "draft"     // never published
	FlowStatusPublished FlowStatus = "published" // has an active version
	FlowStatusArchived  FlowStatus = "archived"
)

type RestartPolicy string

const (
	RestartNewConversationOnly RestartPolicy = "new_conversation_only"
	RestartAlways              RestartPolicy = "always"
)

type RunStatus string

const (
	RunRunning      RunStatus = "running"
	RunWaitingInput RunStatus = "waiting_input"
	RunWaitingHuman RunStatus = "waiting_human"
	RunCompleted    RunStatus = "completed"
	RunFailed       RunStatus = "failed"
	RunCancelled    RunStatus = "cancelled"
	RunExpired      RunStatus = "expired"
)

// Active runs hold the conversation: at most one per conversation (partial unique index).
func (s RunStatus) Active() bool {
	return s == RunRunning || s == RunWaitingInput || s == RunWaitingHuman
}

type NodeExecStatus string

const (
	ExecCompleted NodeExecStatus = "completed"
	ExecFailed    NodeExecStatus = "failed"
	ExecWaiting   NodeExecStatus = "waiting"
	ExecSkipped   NodeExecStatus = "skipped"
)

// AutomationMode is informational for the UI; who may speak is derived (see ADR-0019 §6).
type AutomationMode string

const (
	AutomationNone         AutomationMode = "none"
	AutomationBot          AutomationMode = "bot"
	AutomationWaitingHuman AutomationMode = "waiting_human"
	AutomationHuman        AutomationMode = "human"
)

// Definition limits (also enforced by the database where cheap).
const (
	MaxDefinitionBytes   = 1 << 20
	MaxNodes             = 200
	MaxEdges             = 400
	MaxVariables         = 100
	MaxNodeConfigBytes   = 16 << 10
	MaxIDLength          = 64
	MaxMessageRunes      = 4096
	DefaultMaxExecutions = 200
	HardMaxExecutions    = 1000
	MaxSubflowDepth      = 3
)

var (
	ErrNotFound         = errors.New("flows: not found")
	ErrSlugTaken        = errors.New("flows: slug already used in this tenant")
	ErrRevisionConflict = errors.New("flows: the draft changed since it was read; reload and retry")
	ErrInvalid          = errors.New("flows: invalid")
	ErrNotPublishable   = errors.New("flows: definition has blocking errors")
	ErrArchived         = errors.New("flows: flow is archived")
	ErrNoSuchVersion    = errors.New("flows: version not found")
	ErrDuplicateEvent   = errors.New("flows: event already processed")
	ErrConversationBusy = errors.New("flows: conversation already has an active run")
)

// NodeType identifies a node kind. The authoritative catalog (ports, config schema, side-effect class) is in nodes.go.
type NodeType string

const (
	NodeTrigger            NodeType = "trigger"
	NodeSendMessage        NodeType = "send_message"
	NodeAsk                NodeType = "ask"
	NodeChoice             NodeType = "choice"
	NodeCondition          NodeType = "condition"
	NodeSwitch             NodeType = "switch"
	NodeSetVariable        NodeType = "set_variable"
	NodeBusinessHours      NodeType = "business_hours"
	NodeResolveContact     NodeType = "resolve_contact"
	NodeResolveCustomerCtx NodeType = "resolve_customer_context"
	NodeCustomerChoice     NodeType = "customer_choice"
	NodeFindOpenTickets    NodeType = "find_open_tickets"
	NodeCreateTicket       NodeType = "create_ticket"
	NodeAssignQueue        NodeType = "assign_queue"
	NodeHumanHandoff       NodeType = "human_handoff"
	NodeSubflow            NodeType = "subflow"
	NodeEnd                NodeType = "end"
)
