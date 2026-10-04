package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// AnalysisWork is one claimed derived-text job (ADR-0016 M2: audio transcript).
type AnalysisWork struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	MessageID uuid.UUID
	MediaID   uuid.UUID // the message_media row whose cleared file is the input
	Kind      string
	Attempts  int
	Mime      string
}

// Analysis is what an engine produced. Text is untrusted data.
type Analysis struct {
	Text       string
	Language   string
	Model      string
	Suspicious bool
	Reason     string // for example "truncated_at_10min"
}

// AnalysisRepository persists derived text. Writes are system-session only.
type AnalysisRepository interface {
	ClaimAnalysis(ctx context.Context, kind string, limit int, lease time.Duration) ([]AnalysisWork, error)
	SaveAnalysis(ctx context.Context, w AnalysisWork, a Analysis) error
	// SaveAnalysisEmpty records that the file held nothing intelligible (silence, music).
	SaveAnalysisEmpty(ctx context.Context, w AnalysisWork, reason string) error
	FailAnalysis(ctx context.Context, w AnalysisWork, reason string) error
	RetryAnalysis(ctx context.Context, w AnalysisWork, delay time.Duration, reason string) error
}

// CleanFiles opens a cleared file for reading; it never looks in quarantine.
type CleanFiles interface {
	ReadClean(tenantID, mediaID uuid.UUID, maxBytes int64) ([]byte, error)
}

// Transcriber turns audio into text. ErrNoSpeech means "nothing intelligible", which is a result, not a failure.
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte, mime string) (Analysis, error)
}

var ErrNoSpeech = errors.New("no intelligible speech")
