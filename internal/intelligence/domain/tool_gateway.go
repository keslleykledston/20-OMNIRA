package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/platform/untrusted"
)

// Tool gateway (ADR-0017 Wave 12). The AI can only ask for capabilities that really exist in OMNIRA and that are listed
// here; it cannot register tools, run scripts or SQL, send messages or change tickets on its own. Everything a tool does
// is done with the REQUESTING USER's permissions, so the gateway never widens authority.

type ToolRisk string

const (
	RiskRead     ToolRisk = "read"      // changes nothing
	RiskLowWrite ToolRisk = "low_write" // changes OMNIRA data in a bounded, reversible way
)

type ToolSource string

const (
	SourceAIToolCall ToolSource = "ai"
	SourceAgentTool  ToolSource = "agent"
)

type ToolCallStatus string

const (
	ToolExecuted        ToolCallStatus = "executed"
	ToolPendingApproval ToolCallStatus = "pending_approval"
	ToolRejected        ToolCallStatus = "rejected"
	ToolDenied          ToolCallStatus = "denied"
	ToolFailed          ToolCallStatus = "failed"
)

type ToolSpec struct {
	Name        string
	Description string
	Risk        ToolRisk
	// Permission the requesting user must hold (in addition to being able to operate the topic for writes).
	Permission string
	// Args validates and normalises the raw arguments (strictly) and returns them ready to store and execute.
	Args func(raw json.RawMessage) (json.RawMessage, error)
}

var (
	ErrUnknownTool   = errors.New("intelligence: unknown tool")
	ErrInvalidArgs   = errors.New("intelligence: invalid tool arguments")
	ErrToolForbidden = errors.New("intelligence: the user cannot use this tool")
)

// Decision is what the policy does with a request.
type ToolDecision string

const (
	DecisionExecute       ToolDecision = "execute"
	DecisionNeedsApproval ToolDecision = "needs_approval"
)

// DecideTool is the whole policy. A read runs. A write requested by the AI waits for a person; the same write requested
// by a person is the person acting (it is exactly what the corresponding endpoint already allows them to do).
func DecideTool(spec ToolSpec, source ToolSource) ToolDecision {
	if spec.Risk == RiskRead {
		return DecisionExecute
	}
	if source == SourceAgentTool {
		return DecisionExecute
	}
	return DecisionNeedsApproval
}

const maxToolArgsBytes = 1024

// strictArgs decodes into dst refusing unknown fields (so a tenant_id, a url or a sql smuggled in is an error), trailing
// data and oversized input.
func strictArgs(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > maxToolArgsBytes {
		return fmt.Errorf("%w: too large", ErrInvalidArgs)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil || dec.More() {
		return fmt.Errorf("%w", ErrInvalidArgs)
	}
	return nil
}

func normalise(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	return b, err
}

type noArgs struct{}

type applyTicketArgs struct {
	Action string `json:"action"`
}

type limitArgs struct {
	Limit int `json:"limit,omitempty"`
}

type searchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

const (
	MaxMemoryLimit      = 20
	minSearchQueryRunes = 2
	maxSearchQueryRunes = 100
)

// ToolRegistry is the closed list of tools. It is code, not data: a tenant cannot add to it.
func ToolRegistry() map[string]ToolSpec {
	none := func(raw json.RawMessage) (json.RawMessage, error) {
		var a noArgs
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		return normalise(a)
	}
	limited := func(raw json.RawMessage) (json.RawMessage, error) {
		var a limitArgs
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		if a.Limit < 0 || a.Limit > MaxMemoryLimit {
			return nil, fmt.Errorf("%w: limit out of range", ErrInvalidArgs)
		}
		return normalise(a)
	}
	search := func(raw json.RawMessage) (json.RawMessage, error) {
		var a searchArgs
		if err := strictArgs(raw, &a); err != nil {
			return nil, err
		}
		a.Query = strings.TrimSpace(a.Query)
		if n := utf8.RuneCountInString(a.Query); n < minSearchQueryRunes || n > maxSearchQueryRunes || untrusted.ContainsCredential(a.Query) {
			return nil, fmt.Errorf("%w: query must have 2 to 100 characters and no credential", ErrInvalidArgs)
		}
		if a.Limit < 0 || a.Limit > MaxMemoryLimit {
			return nil, fmt.Errorf("%w: limit out of range", ErrInvalidArgs)
		}
		return normalise(a)
	}
	specs := []ToolSpec{
		// ADR-0020: memory of the topic's contact. Read only; the contact is derived from the topic on the server, so there is
		// no argument that can point these at another contact or tenant.
		{Name: "contact.recent_attendances", Description: "Summaries of the contact's earlier finalized attendances", Risk: RiskRead, Permission: "topic.read", Args: limited},
		{Name: "contact.open_followups", Description: "What is still pending or was promised to this contact", Risk: RiskRead, Permission: "topic.read", Args: limited},
		{Name: "contact.search_history", Description: "Find earlier messages of THIS contact by text", Risk: RiskRead, Permission: "topic.read", Args: search},
		{Name: "topic.get_summary", Description: "Latest versions of the topic's summaries", Risk: RiskRead, Permission: "topic.read", Args: none},
		{Name: "topic.list_tickets", Description: "Tickets linked to the topic", Risk: RiskRead, Permission: "topic.read", Args: none},
		{Name: "topic.ticket_policy_advice", Description: "What the ticket policy advises for the topic", Risk: RiskRead, Permission: "topic.read", Args: none},
		{Name: "topic.generate_summary", Description: "Create a machine summary of the topic's current state", Risk: RiskLowWrite, Permission: "topic.manage", Args: none},
		{Name: "topic.apply_ticket_policy", Description: "Adopt, share or open a ticket for the topic as the policy allows", Risk: RiskLowWrite, Permission: "topic.manage",
			Args: func(raw json.RawMessage) (json.RawMessage, error) {
				var a applyTicketArgs
				if err := strictArgs(raw, &a); err != nil {
					return nil, err
				}
				switch TicketAction(a.Action) {
				case TicketActionAdoptActive, TicketActionShareActive, TicketActionCreate:
				default:
					return nil, fmt.Errorf("%w: unknown action", ErrInvalidArgs)
				}
				return normalise(a)
			}},
	}
	m := make(map[string]ToolSpec, len(specs))
	for _, s := range specs {
		m[s.Name] = s
	}
	return m
}

// ToolCall is the stored record of a request.
type ToolCall struct {
	ID             uuid.UUID
	TenantID       uuid.UUID
	TopicThreadID  uuid.UUID
	Tool           string
	Risk           ToolRisk
	Source         ToolSource
	Status         ToolCallStatus
	Args           json.RawMessage
	Result         json.RawMessage
	Error          string
	IdempotencyKey string
	RequestedBy    *uuid.UUID
	DecidedBy      *uuid.UUID
}

// ValidIdempotencyKey: 8..64 printable characters without spaces.
func ValidIdempotencyKey(k string) bool {
	if len(k) < 8 || len(k) > 64 {
		return false
	}
	for _, r := range k {
		if r <= ' ' || r > '~' {
			return false
		}
	}
	return !strings.ContainsAny(k, "\"'\\")
}
