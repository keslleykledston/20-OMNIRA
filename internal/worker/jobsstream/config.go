package jobsstream

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Canonical OMNIRA_JOBS policy (PILOT.4D3-C1, frozen in PILOT.4D3-C). Every
// value that governs the stream's retention lives here exactly once —
// Config() assembles them, Ensure() applies and verifies them, and
// MaxAge/ReconciliationGrace are what the reconciler (internal/worker/delivery)
// is wired to in apps/worker/cmd/omnira-worker/main.go. Never redeclare any
// of these values in routing/consumer.go, delivery/consumer.go or main.go.
const (
	Name = "OMNIRA_JOBS"

	// MaxAge: unacknowledged/acknowledged messages older than this are
	// removed unconditionally (proven this session: LimitsPolicy enforces
	// MaxAge regardless of ack state). 7 days bounds disk growth while
	// staying far above delivery's own worst-case retry window
	// (MaxAttempts=8 backoff tops out at 2m/attempt, ~10.6 minutes total).
	MaxAge time.Duration = 7 * 24 * time.Hour

	// MaxBytes caps OMNIRA_JOBS at 8 GiB. Paired with Discard=DiscardNew
	// (not DiscardOld) so exceeding it rejects new publishes visibly
	// (proven this session: code=503 err_code=10077 "maximum bytes
	// exceeded") instead of silently evicting old, possibly-unacked work.
	MaxBytes int64 = 8 * 1024 * 1024 * 1024

	// MaxMsgs has no `omitempty` JSON tag (confirmed against vendored
	// nats.go v1.53.1's jetstream.StreamConfig) — set explicitly to NATS'
	// own "unlimited" sentinel so the wire value is never an accidental
	// Go zero-value.
	MaxMsgs int64 = -1

	Discard    = jetstream.DiscardNew
	Retention  = jetstream.LimitsPolicy
	Storage    = jetstream.FileStorage
	Duplicates = 2 * time.Minute
)

// Subjects is the canonical OMNIRA_JOBS subject filter. A var, not a const
// (Go slices cannot be consts), but assigned exactly once, here only.
var Subjects = []string{"job.>"}

// Config is the single canonical OMNIRA_JOBS jetstream.StreamConfig. Every
// field the live or test stream is judged against traces back to this
// function; nothing else in the codebase may construct a StreamConfig
// literal naming OMNIRA_JOBS.
func Config() jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:       Name,
		Subjects:   Subjects,
		Storage:    Storage,
		Retention:  Retention,
		MaxAge:     MaxAge,
		MaxBytes:   MaxBytes,
		MaxMsgs:    MaxMsgs,
		Discard:    Discard,
		Duplicates: Duplicates,
	}
}

// streamManager is the minimal slice of jetstream.JetStream that Ensure
// actually needs. jetstream.JetStream satisfies it automatically; declaring
// it separately lets tests exercise ensureWithConfig's create/verify logic
// against a small fake instead of having to implement JetStream's entire
// (much larger) interface.
type streamManager interface {
	CreateOrUpdateStream(ctx context.Context, cfg jetstream.StreamConfig) (jetstream.Stream, error)
	Stream(ctx context.Context, name string) (jetstream.Stream, error)
}

// Ensure is the ONLY call site in the worker binary that may configure
// OMNIRA_JOBS' stream policy. It applies Config() (CreateOrUpdateStream
// creates the stream if absent or updates it in place if present — it never
// drops/recreates an existing stream, proven by this session's disposable
// activation test showing message/first_seq/consumer-Created identity
// preserved across the update), reads Info() back, and verifies the server
// actually accepted the canonical policy before handing the stream to any
// caller. Routing/delivery consumers receive the *jetstream.Stream this
// returns and only ever CreateOrUpdateConsumer their own durable consumer —
// never stream policy — on it.
func Ensure(ctx context.Context, js jetstream.JetStream) (jetstream.Stream, error) {
	return ensureWithConfig(ctx, js, Config())
}

// ensureWithConfig is Ensure's real implementation, parameterized on both the
// StreamConfig and the (minimal) streamManager it talks to — the config
// override lets disposable tests exercise this exact create/update/verify
// logic against a short-lived test policy (e.g. a MaxAge of seconds, to
// prove expiry without waiting 7 real days) while Config() itself remains
// the single fixed canonical policy; the streamManager override lets a
// purely local test drive the same logic against a fake, with no NATS
// connection at all, to prove verification failure is surfaced as an error.
func ensureWithConfig(ctx context.Context, js streamManager, cfg jetstream.StreamConfig) (jetstream.Stream, error) {
	if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
		return nil, fmt.Errorf("jobsstream: create/update %s: %w", cfg.Name, err)
	}
	stream, err := js.Stream(ctx, cfg.Name)
	if err != nil {
		return nil, fmt.Errorf("jobsstream: fetch %s after create/update: %w", cfg.Name, err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("jobsstream: read back %s info: %w", cfg.Name, err)
	}
	if err := verifyConfig(cfg, info.Config); err != nil {
		return nil, fmt.Errorf("jobsstream: %s policy verification failed: %w", cfg.Name, err)
	}
	return stream, nil
}

// verifyConfig compares only the fields jobsstream itself governs — never a
// blind reflect.DeepEqual/JSON-string comparison, which would false-fail on
// server-populated fields Config() never sets (e.g. Replicas defaulting to
// 1) and could miss a subtly wrong Subjects slice.
func verifyConfig(want, got jetstream.StreamConfig) error {
	if got.Name != want.Name {
		return fmt.Errorf("name = %q, want %q", got.Name, want.Name)
	}
	if !equalSubjects(got.Subjects, want.Subjects) {
		return fmt.Errorf("subjects = %v, want %v", got.Subjects, want.Subjects)
	}
	if got.Storage != want.Storage {
		return fmt.Errorf("storage = %v, want %v", got.Storage, want.Storage)
	}
	if got.Retention != want.Retention {
		return fmt.Errorf("retention = %v, want %v", got.Retention, want.Retention)
	}
	if got.MaxAge != want.MaxAge {
		return fmt.Errorf("max_age = %v, want %v", got.MaxAge, want.MaxAge)
	}
	if got.MaxBytes != want.MaxBytes {
		return fmt.Errorf("max_bytes = %d, want %d", got.MaxBytes, want.MaxBytes)
	}
	if got.MaxMsgs != want.MaxMsgs {
		return fmt.Errorf("max_msgs = %d, want %d", got.MaxMsgs, want.MaxMsgs)
	}
	if got.Discard != want.Discard {
		return fmt.Errorf("discard = %v, want %v", got.Discard, want.Discard)
	}
	if got.Duplicates != want.Duplicates {
		return fmt.Errorf("duplicate_window = %v, want %v", got.Duplicates, want.Duplicates)
	}
	return nil
}

func equalSubjects(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
