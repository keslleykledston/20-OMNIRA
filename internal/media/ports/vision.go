package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// VisionInput is one attachment for the external vision model. Data is the CLEARED file (antivirus passed, mime
// verified from the bytes), never anything from quarantine.
type VisionInput struct {
	Data []byte
	Mime string
	Kind string // description (image) | document_text (PDF)
}

// Usage is what the provider reported for one call.
type Usage struct {
	InputTokens  int
	OutputTokens int
}

// VisionAnalyzer reads an image or a PDF with an external model. The text it returns is untrusted data.
type VisionAnalyzer interface {
	Analyze(ctx context.Context, apiKey, model string, in VisionInput) (Analysis, Usage, error)
}

var (
	// ErrProviderTransient: timeout, rate limit, 5xx. Worth retrying later.
	ErrProviderTransient = errors.New("vision: provider temporarily unavailable")
	// ErrProviderRejected: the key or request was refused (401/403/400). Retrying cannot help.
	ErrProviderRejected = errors.New("vision: provider rejected the request")
	// ErrNothingReadable: the model found nothing intelligible (or blocked the content). A result, not a failure.
	ErrNothingReadable = errors.New("vision: nothing readable in the attachment")
)

// TenantAI is the tenant's own external-AI configuration (ADR-0016). It exists only when the tenant administrator has
// supplied a key, recorded the consent and switched it on.
type TenantAI struct {
	APIKey    string
	Model     string
	BudgetUSD float64
}

// TenantAIResolver returns nil when the tenant has not opted in (or the key cannot be read): nothing leaves the server.
type TenantAIResolver interface {
	Resolve(ctx context.Context, tenantID uuid.UUID) (*TenantAI, error)
}

// UsageRecord is one external AI call, for accounting and the budget.
type UsageRecord struct {
	TenantID     uuid.UUID
	Provider     string
	Model        string
	Task         string
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	Success      bool
	Reason       string
	Ref          uuid.UUID // the analysis row
}

// UsageLedger records calls and reports the month's spend (Wave 10).
type UsageLedger interface {
	Record(ctx context.Context, u UsageRecord) error
	SpentThisMonth(ctx context.Context, tenantID uuid.UUID, now time.Time) (float64, error)
}

// VisionRepository is the part of the analysis storage the vision step adds.
type VisionRepository interface {
	// EnqueueVision creates pending description / document_text jobs for cleared, still-present images and PDFs of
	// tenants that have opted in. It never creates one for a tenant that has not.
	EnqueueVision(ctx context.Context, newerThan time.Time, limit int) (int, error)
	// SkipAnalysis records that the job will not run, with the reason (for example the budget).
	SkipAnalysis(ctx context.Context, w AnalysisWork, reason string) error
}
