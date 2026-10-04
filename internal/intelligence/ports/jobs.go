package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Job is one unit of message processing claimed by a worker. Attempt is the claim number: only the worker holding the
// current attempt may move the job, so a worker whose lease expired cannot overwrite the one that took over.
type Job struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	Ref             MessageRef
	PipelineVersion string
	Attempt         int
}

// JobStore is the durable queue behind the intelligence pipeline.
type JobStore interface {
	// EnsureFromEvent creates the job for a persisted message (idempotent: a redelivered event creates nothing). The tenant
	// is read from the stored message, never from the event. domain.ErrReferenceNotFound when the message does not exist.
	EnsureFromEvent(ctx context.Context, ref MessageRef, pipelineVersion string) (created bool, err error)
	// Claim leases up to limit due jobs (pending, or running with an expired lease = a crashed worker) with SKIP LOCKED.
	Claim(ctx context.Context, limit int, lease time.Duration) ([]Job, error)
	Complete(ctx context.Context, j Job) (bool, error)
	Retry(ctx context.Context, j Job, errorClass string, delay time.Duration) (bool, error)
	Dead(ctx context.Context, j Job, errorClass string) (bool, error)
}
