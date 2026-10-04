package ports

import (
	"context"

	"github.com/google/uuid"
)

// Evaluation is the aggregate, content-free report that tells an administrator whether each automation deserves to be
// turned on: how often the deterministic router is overridden by people, how often the AI shadow agrees with what finally
// happened, and how people treat summaries, ambiguities and tool requests. It contains numbers only, never message text.
type Evaluation struct {
	Days   int `json:"days"`
	Router struct {
		Decisions  int            `json:"decisions"`
		Applied    int            `json:"applied"`
		Overridden int            `json:"overridden_by_people"`
		ByStatus   map[string]int `json:"by_status"`
		BySource   map[string]int `json:"by_source"`
		// OverrideRate is overridden / applied (0 when nothing was applied).
		OverrideRate float64 `json:"override_rate"`
	} `json:"router"`
	AIShadow struct {
		Proposals          int            `json:"proposals"`
		ByStatus           map[string]int `json:"by_status"`
		WithFinalPlacement int            `json:"with_final_placement"`
		Agreed             int            `json:"agreed"`
		AgreementRate      float64        `json:"agreement_rate"`
		ByConfidence       []ConfBucket   `json:"by_confidence"`
		TokensIn           int64          `json:"input_tokens"`
		TokensOut          int64          `json:"output_tokens"`
	} `json:"ai_shadow"`
	Ambiguities struct {
		Opened          int `json:"opened"`
		ResolvedByAgent int `json:"resolved_by_agent"`
		ResolvedByCust  int `json:"resolved_by_customer"`
		StillOpen       int `json:"still_open"`
	} `json:"ambiguities"`
	Summaries struct {
		Versions   int `json:"versions"`
		AIInferred int `json:"ai_inferred"`
		Confirmed  int `json:"confirmed"`
		Corrected  int `json:"corrected"`
		// CorrectionRate is corrected / (confirmed + corrected): how often a person rewrote instead of accepting.
		CorrectionRate float64 `json:"correction_rate"`
	} `json:"summaries"`
	ToolCalls map[string]int `json:"tool_calls_by_status"`
	Handoffs  map[string]int `json:"handoffs_by_status"`
}

type ConfBucket struct {
	Range              string  `json:"range"`
	WithFinalPlacement int     `json:"with_final_placement"`
	Agreed             int     `json:"agreed"`
	AgreementRate      float64 `json:"agreement_rate"`
}

type EvaluationRepository interface {
	Report(ctx context.Context, tenantID uuid.UUID, days int) (*Evaluation, error)
}
