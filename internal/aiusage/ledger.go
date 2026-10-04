// Package aiusage is the single ledger of external AI calls (ADR-0017 Wave 10). Features that call a model record one row
// per call; the budget guard of the tenant's own provider key reads the month's spend from it.
package aiusage

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// BudgetProvider is the provider whose spend counts against the tenant's monthly budget: the tenant brings and pays for
// its own Gemini key. Providers paid by the platform are recorded (tokens) but never block a tenant.
const BudgetProvider = "gemini"

type Record struct {
	TenantID     uuid.UUID
	Provider     string
	Model        string
	Task         string
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	// NoCost marks a call whose price is unknown / platform-paid: stored as NULL, not as a free call.
	NoCost  bool
	Success bool
	Reason  string
	Ref     uuid.UUID
}

// Ledger records calls and reports the month's spend on the budgeted provider.
type Ledger interface {
	Record(ctx context.Context, r Record) error
	SpentThisMonth(ctx context.Context, tenantID uuid.UUID, now time.Time) (float64, error)
}

// MonthStart is the first instant of now's month in UTC (the budget period).
func MonthStart(now time.Time) time.Time {
	n := now.UTC()
	return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Line is one row of the monthly summary.
type Line struct {
	Provider     string
	Model        string
	Task         string
	Calls        int
	Failures     int
	InputTokens  int64
	OutputTokens int64
	CostUSD      float64
}

// Summary is what an administrator sees for a month.
type Summary struct {
	From, To time.Time
	Lines    []Line
}
