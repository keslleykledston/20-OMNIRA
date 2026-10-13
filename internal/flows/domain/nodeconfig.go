package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DecodeConfig strictly decodes a node config (unknown fields are an error). Shared by the validator and the executors so
// both read the same schema.
func DecodeConfig[T any](raw json.RawMessage) (T, error) {
	var out T
	if len(bytes.TrimSpace(raw)) == 0 {
		return out, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("invalid config: %v", err)
	}
	return out, nil
}

type TriggerConfig struct{}

type SendMessageConfig struct {
	Text string `json:"text"`
}

type AskConfig struct {
	Text           string `json:"text"`
	Variable       string `json:"variable"`
	Validation     string `json:"validation,omitempty"` // none | number | email | phone
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	MaxAttempts    int    `json:"max_attempts,omitempty"`
}

type ChoiceOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Value string `json:"value,omitempty"` // stored in the variable; defaults to the label
}

type ChoiceConfig struct {
	Text           string         `json:"text"`
	Variable       string         `json:"variable"`
	Options        []ChoiceOption `json:"options"`
	TimeoutSeconds int            `json:"timeout_seconds,omitempty"`
	MaxAttempts    int            `json:"max_attempts,omitempty"`
	// Style: "" or "auto" sends buttons/list on channels that support them (WhatsApp oficial) and numbered text elsewhere;
	// "text" always sends the numbered text.
	Style string `json:"style,omitempty"`
	// Reminders (ADR-0045): what to say, one message per silence, before the "timeout" port is taken. Every wait lasts TimeoutSeconds, so two reminders
	// and a 120 s timeout mean: reminder at 2 min, second reminder at 4 min, "timeout" at 6 min. Each reminder is sent together with the menu.
	Reminders []string `json:"reminders,omitempty"`
}

type ConditionConfig struct {
	Variable string `json:"variable"`
	Op       string `json:"op"`
	Value    any    `json:"value,omitempty"`
}

type SwitchCase struct {
	ID    string `json:"id"`
	Op    string `json:"op,omitempty"` // default eq
	Value any    `json:"value,omitempty"`
}

type SwitchConfig struct {
	Variable string       `json:"variable"`
	Cases    []SwitchCase `json:"cases"`
}

type Assignment struct {
	Variable string `json:"variable"`
	Value    any    `json:"value"` // literal, or a string with {{variable}} templates
}

type SetVariableConfig struct {
	Assignments []Assignment `json:"assignments"`
}

type HoursWindow struct {
	Days  []string `json:"days"` // mon..sun
	Start string   `json:"start"`
	End   string   `json:"end"`
}

// BusinessHoursConfig is an inline schedule: the platform has no business-hours module to reuse (verified 2026-10-06).
type BusinessHoursConfig struct {
	Timezone string        `json:"timezone"`
	Windows  []HoursWindow `json:"windows"`
}

type ResolveContactConfig struct{}
type ResolveCustomerContextConfig struct{}
type FindOpenTicketsConfig struct{}

type CustomerChoiceConfig struct {
	Text           string `json:"text,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	MaxAttempts    int    `json:"max_attempts,omitempty"`
}

type CreateTicketConfig struct {
	Subject  string `json:"subject"`
	Priority string `json:"priority,omitempty"` // low | medium | high | critical
}

type AssignQueueConfig struct {
	Queue ResourceField `json:"queue"`
}

type HumanHandoffConfig struct {
	Queue   *ResourceField `json:"queue,omitempty"` // default: keep the conversation's current queue
	Summary string         `json:"summary,omitempty"`
}

// ScheduleCheckConfig names ONE place to ask about: a queue or a sector (by id; a sector or queue that is renamed keeps working, one that is
// gone or deactivated takes the "unavailable" port instead of breaking the flow).
type ScheduleCheckConfig struct {
	Queue  *ResourceField `json:"queue,omitempty"`
	Sector *ResourceField `json:"sector,omitempty"`
}

// SelectSectorConfig asks the contact to choose among the company's ACTIVE sectors, read when the node runs (a new or deactivated sector
// shows up or disappears without editing the flow). The choice is stored in the variable (default "sector": id and name).
type SelectSectorConfig struct {
	Text           string `json:"text,omitempty"`
	Variable       string `json:"variable,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	MaxAttempts    int    `json:"max_attempts,omitempty"`
}

// AssignSectorConfig routes the conversation to a sector: a fixed one, or the one the contact chose (FromVariable).
type AssignSectorConfig struct {
	Sector       *ResourceField `json:"sector,omitempty"`
	FromVariable string         `json:"from_variable,omitempty"`
}

// ApplyOverflowConfig asks the routing engine to evaluate the conversation now, with the overflow policy of its queue.
type ApplyOverflowConfig struct{}

type SubflowConfig struct {
	Flow      string `json:"flow"`                 // slug of a SUBFLOW flow of the tenant
	VersionID string `json:"version_id,omitempty"` // pinned by publish; never written by hand
}

// CloseConversationConfig (ADR-0045): Text is the goodbye sent before closing (optional); Reason is recorded on the closure: no_response (default),
// resolved or other.
type CloseConversationConfig struct {
	Text   string `json:"text,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type EndConfig struct {
	Outcome string `json:"outcome,omitempty"` // resolved | abandoned | informational
}

type AIIntent struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// AIClassifyConfig routes by an AI SUGGESTION: below MinConfidence (default 0.7) the run takes low_confidence, and any failure
// takes error. The AI never chooses severity, queue or ticket data: it only picks one of the author's own ports.
type AIClassifyConfig struct {
	Intents       []AIIntent `json:"intents"`
	MinConfidence float64    `json:"min_confidence,omitempty"`
}

type AIField struct {
	Variable    string `json:"variable"`
	Type        string `json:"type"` // string | number | boolean | email | phone
	Description string `json:"description,omitempty"`
}

type AIExtractConfig struct {
	Fields []AIField `json:"fields"`
}

type AISummarizeConfig struct {
	Variable    string `json:"variable"`
	MaxMessages int    `json:"max_messages,omitempty"`
}

const (
	DefaultAIMinConfidence = 0.7
	MaxAICallsPerRun       = 5
)
