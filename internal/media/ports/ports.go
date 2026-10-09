// Package ports declares what the media pipeline needs from the outside world (ADR-0016): the repository
// of media rows, the byte store, the provider the files come from and the antivirus.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Status mirrors message_media.status.
type Status string

const (
	StatusPending     Status = "pending"
	StatusQuarantined Status = "quarantined"
	StatusClean       Status = "clean"
	StatusInfected    Status = "infected"
	StatusRejected    Status = "rejected"
	StatusSourceGone  Status = "source_gone"
	StatusFailed      Status = "failed"
)

// Work is one claimed row. MediaRef is provider-internal and never leaves the worker.
type Work struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	MessageID    uuid.UUID
	Status       Status
	Attempts     int
	MediaRef     string
	DeclaredMime string
	// ConnectionID is the channel connection the message came in on: it tells which provider (and which credential)
	// the file must be fetched with.
	ConnectionID uuid.UUID
	CreatedAt    time.Time
}

// Quarantined describes a fetched, type-checked file waiting for the antivirus.
type Quarantined struct {
	Kind      string
	Mime      string
	SizeBytes int64
	SHA256    string
}

// Repository persists the state machine. Every verdict is written by the worker's system session; the
// HTTP layer only reads.
type Repository interface {
	// Claim leases up to limit due rows (pending or quarantined) so no other worker takes them for lease.
	Claim(ctx context.Context, limit int, lease time.Duration) ([]Work, error)
	MarkQuarantined(ctx context.Context, w Work, q Quarantined) error
	MarkClean(ctx context.Context, w Work) error
	// MarkTerminal records infected / rejected / source_gone / failed together with its audit event.
	MarkTerminal(ctx context.Context, w Work, status Status, reason string) error
	// Retry gives the row back to the queue after delay without changing its status.
	Retry(ctx context.Context, w Work, delay time.Duration, reason string) error
	// ExpiredFiles returns rows whose file is older than the retention window and not yet purged.
	ExpiredFiles(ctx context.Context, olderThan time.Time, limit int) ([]Work, error)
	MarkPurged(ctx context.Context, w Work) error
}

// TenantGate is an OPTIONAL capability of a repository: whether the company is still active (ADR-0038). Processors ask it right
// before each external operation (fetching a file, scanning, calling an AI provider, transcribing) and before storing a result;
// a repository that does not implement it (a test fake) is treated as "always active". A company that is suspended is not served:
// the claimed row is left alone (its lease expires and it is claimed again once the company is active), nothing leaves the server.
type TenantGate interface {
	TenantActive(ctx context.Context, tenant uuid.UUID) (bool, error)
}

// Store keeps the bytes. Quarantined files are never readable by the API; only Promote moves a file to the
// area the API serves from.
type Store interface {
	PutQuarantine(w Work, data []byte) error
	GetQuarantine(w Work) ([]byte, error)
	Promote(w Work) error
	// Remove deletes the file wherever it is (quarantine or clean). A missing file is not an error.
	Remove(w Work) error
}

// Fetcher downloads the file from the provider.
type Fetcher interface {
	Fetch(ctx context.Context, mediaRef string) (data []byte, declaredMime string, err error)
}

// WorkFetcher is a Fetcher that needs the whole claimed row (the connection) to pick the provider and credential.
type WorkFetcher interface {
	FetchWork(ctx context.Context, w Work) (data []byte, declaredMime string, err error)
}

// ErrSourceGone means the provider no longer has the file (permanent for the purposes of capture).
var ErrSourceGone = errors.New("media source gone")

// Verdict is the antivirus answer.
type Verdict struct {
	Infected  bool
	Signature string
}

// Scanner is the antivirus. Any error means "not scanned": the caller must treat the file as unsafe.
type Scanner interface {
	Scan(ctx context.Context, data []byte) (Verdict, error)
}

// ErrMediaNotFound means the message has no media row (or belongs to another tenant: RLS hides it).
var ErrMediaNotFound = errors.New("media not found")

// ServedMedia is what the HTTP layer needs to answer a download. File is non-nil only for a cleared file.
type ServedMedia struct {
	Status string
	Mime   string
	Size   int64
	// SHA256 identifies the content; the file never changes once cleared, so it doubles as the ETag.
	SHA256 string
	File   ReadSeekCloser
}

// ReadSeekCloser lets the HTTP layer honour Range requests (audio seeking).
type ReadSeekCloser interface {
	Read(p []byte) (int, error)
	Seek(offset int64, whence int) (int64, error)
	Close() error
}

// MediaReader is the API-side view: it reads under the caller's tenant session (RLS) and only ever opens
// files from the clean area.
type MediaReader interface {
	Open(ctx context.Context, tenantID, messageID uuid.UUID) (*ServedMedia, error)
}
