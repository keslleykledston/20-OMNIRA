package domain

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// DefaultRawRetention: how long the encrypted raw of an import is kept (ADR-0060 §6). A tenant may configure another value.
const DefaultRawRetention = 90 * 24 * time.Hour

type ImportStatus string

const (
	ImportUploaded   ImportStatus = "uploaded"
	ImportExtracting ImportStatus = "extracting"
	ImportExtracted  ImportStatus = "extracted"
	ImportFailed     ImportStatus = "failed"
	ImportDiscarded  ImportStatus = "discarded"
)

type DiscardReason string

const (
	DiscardConfirmed      DiscardReason = "confirmed"
	DiscardExpired        DiscardReason = "expired"
	DiscardHistoryDeleted DiscardReason = "history_deleted"
	DiscardAIDisabled     DiscardReason = "group_ai_disabled"
	DiscardMemoryErased   DiscardReason = "memory_erased"
)

var (
	ErrCoverageIncomplete = errors.New("groupai: the import is not fully covered: it cannot be discarded before it expires")
	ErrAlreadyDiscarded   = errors.New("groupai: the import was already discarded")
)

// Import is the metadata of an imported export (never its content).
type Import struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	GroupID         uuid.UUID
	Status          ImportStatus
	Format          string
	SourceFilename  string
	SHA256          string
	SizeBytes       int64
	ImportedBy      *uuid.UUID
	ParserVersion   string
	MsgsTotal       int
	MsgsParsed      int
	MsgsRejected    int
	MsgsDuplicate   int
	RejectedReason  map[string]int
	RangeFrom       *time.Time
	RangeTo         *time.Time
	BatchesTotal    int
	BatchesOK       int
	BatchesFailed   int
	ItemsCreated    int
	PromptVersion   string
	ExtractRuns     int
	CreatedAt       time.Time
	StatusChangedAt time.Time
	ExtractedAt     *time.Time
	ExpiresAt       time.Time
	DiscardedAt     *time.Time
	DiscardReason   *DiscardReason
	Version         int
}

// CoverageComplete is the honest claim the system can make about an import: EVERY message passed through an extraction batch and none
// of the batches failed. It does NOT mean everything was captured - the model extracts what looks relevant - only that everything was read.
func (i Import) CoverageComplete() bool {
	return i.Status == ImportExtracted && i.BatchesTotal > 0 && i.BatchesFailed == 0 && i.BatchesOK == i.BatchesTotal
}

// DiscardDecision applies the mechanical rule of ADR-0060 §6: the raw may be discarded when (a) a person confirmed it AND the
// coverage is complete, or (b) it expired. It returns the reason to record, or an error saying why not.
func (i Import) DiscardDecision(now time.Time, confirmedByPerson bool) (DiscardReason, error) {
	if i.Status == ImportDiscarded {
		return "", ErrAlreadyDiscarded
	}
	if !now.Before(i.ExpiresAt) {
		return DiscardExpired, nil
	}
	if confirmedByPerson {
		if !i.CoverageComplete() {
			return "", ErrCoverageIncomplete
		}
		return DiscardConfirmed, nil
	}
	return "", ErrCoverageIncomplete
}
