// Package delivery is the worker-side outbound channel delivery. A job carries
// only a message reference; text, recipient and connection are read from the
// database inside a tenant session derived from that persisted message.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ErrPermanent marks a job that can never succeed (malformed reference); the
// consumer terminates it instead of redelivering.
var ErrPermanent = errors.New("channel delivery: permanent job error")

// OutboundJob is the persisted state needed to deliver one queued message.
type OutboundJob struct {
	MessageID    uuid.UUID
	ConnectionID uuid.UUID
	ToE164       string
	// ProviderChatID é o endereço da conversa no provedor. Quando presente, é
	// ele que endereça o envio; derivar do telefone falha silenciosamente com
	// contatos endereçados por LID no WhatsApp.
	ProviderChatID string
	Text           string
	Status         string
	// ReservedProviderMessageID is the stable, pre-reserved provider message
	// id (PILOT.4A1) — set BEFORE the first SendText attempt, in its own
	// committed transaction, distinct from ProviderMessageID below. Empty
	// until a reservation exists (e.g. the provider doesn't support one, or
	// none has been made yet).
	ReservedProviderMessageID string
	// ProviderMessageID is set ONLY after a confirmed successful provider
	// response (MarkSent) — this semantic is unchanged by PILOT.4A1.
	ProviderMessageID string
	ConnectionActive  bool
	// TenantSuspended is true when the company was suspended at the moment the message was locked. The store takes a
	// share lock on the company row, so the suspension cannot commit while this attempt is still deciding (ADR-0038).
	TenantSuspended bool
	// Template is set when this message is an approved-template send (WhatsApp Cloud API); Text then only holds the
	// rendered preview shown in the inbox and is NOT what the provider receives.
	Template *TemplateJob
	// Media is set when the message carries a file an operator uploaded (ADR-0024); Text is then its caption.
	Media *MediaJob
	// Interactive is set when the bot's menu goes out as buttons/list; Text is then the numbered-text version of the SAME
	// menu, used as the fallback on a provider without buttons.
	Interactive *InteractiveJob
}

// MediaJob is the record of the file to deliver; the bytes are read from the outbound file store and must match Size and SHA256.
type MediaJob struct {
	AttachmentID uuid.UUID
	TenantID     uuid.UUID
	Kind         string
	Mime         string
	FileName     string
	Size         int64
	SHA256       string
}

// MediaFiles reads an outbound file back, verified against its record.
type MediaFiles interface {
	ReadVerified(tenantID, id uuid.UUID, size int64, sha256Hex string) ([]byte, error)
}

// InteractiveJob is what the provider needs to render a menu as buttons/list.
type InteractiveJob struct {
	Body      string
	ListLabel string
	Options   []domain.InteractiveOption
}

// TemplateJob is what the provider needs to send an approved template.
type TemplateJob struct {
	Name     string
	Language string
	Params   []string
}

// OutboundStore is the persistence boundary of the worker. Every method after
// RunForMessage executes inside the tenant session it opens.
type OutboundStore interface {
	// RunForMessage resolves the tenant from the persisted message (never from
	// the NATS envelope) and runs fn inside SET LOCAL tenant context.
	RunForMessage(ctx context.Context, messageID uuid.UUID, fn func(context.Context) error) error
	// LockOutbound reads the message under a row lock; (nil, nil) when absent.
	LockOutbound(ctx context.Context, messageID uuid.UUID) (*OutboundJob, error)
	MarkSent(ctx context.Context, messageID uuid.UUID, providerMessageID string) error
	MarkFailed(ctx context.Context, messageID uuid.UUID, reason string) error
	// MarkUncertain (PILOT.4A2) records that the provider outcome could not
	// be proven — never a confirmed rejection. provider_message_id is left
	// untouched (empty, since no confirmed success occurred) and
	// reserved_provider_message_id is retained, not cleared: a future
	// reconciliation path (out of scope here) can still match an
	// after-the-fact provider ack against the retained reservation.
	MarkUncertain(ctx context.Context, messageID uuid.UUID, reason string) error
	// EnsureReservedProviderMessageID durably persists a stable provider
	// message id for messageID in its OWN committed transaction, separate
	// from any transaction that later calls the provider — this is the
	// central durability property PILOT.4A1 depends on (see package doc on
	// Handle). If a reservation already exists (even from a prior attempt
	// that later crashed before SendText ran), that exact value is returned
	// and generate is never called. Race-safe: if two callers race to
	// reserve concurrently, exactly one persisted value wins and the other
	// caller reads and reuses it — generate may be called more than once
	// across racing callers, but at most one resulting id is ever persisted
	// or used for delivery.
	EnsureReservedProviderMessageID(ctx context.Context, messageID uuid.UUID, generate func(context.Context) (string, error)) (string, error)
}

type TextSender interface {
	SendText(ctx context.Context, connectionID uuid.UUID, msg domain.OutboundTextMessage) (*domain.SendResult, error)
	// NewMessageID reserves a stable, provider-generated message id with no
	// delivery side effect. Returns ports.ErrCapabilityNotSupported if the
	// provider behind connectionID does not offer this.
	NewMessageID(ctx context.Context, connectionID uuid.UUID) (string, error)
	// SendTemplate sends an approved template with its body variables (Meta Cloud API).
	SendTemplate(ctx context.Context, connectionID uuid.UUID, msg domain.OutboundTemplateMessage) (*domain.SendResult, error)
	// SendInteractive sends buttons/list; ports.ErrCapabilityNotSupported when the provider has none (the caller sends text).
	SendInteractive(ctx context.Context, connectionID uuid.UUID, msg domain.OutboundInteractiveMessage) (*domain.SendResult, error)
	// SendMedia sends an operator's file. No provider deduplicates media by id, so an ambiguous failure is ports.ErrOutcomeUnknown.
	SendMedia(ctx context.Context, connectionID uuid.UUID, msg domain.OutboundMediaMessage) (*domain.SendResult, error)
}

type Handler struct {
	store       OutboundStore
	sender      TextSender
	maxAttempts int
	outcomes    metric.Int64Counter
	files       MediaFiles // nil: a media message cannot be delivered and ends as failed
}

// WithMediaFiles enables delivery of operator files (ADR-0024).
func (h *Handler) WithMediaFiles(f MediaFiles) *Handler { h.files = f; return h }

// NewHandler builds the delivery handler. After maxAttempts deliveries of a
// still-retryable failure the message is marked failed instead of retried.
func NewHandler(store OutboundStore, sender TextSender, maxAttempts int) (*Handler, error) {
	if store == nil || sender == nil || maxAttempts < 1 {
		return nil, errors.New("channel delivery: store, sender and maxAttempts are required")
	}
	counter, _ := otel.Meter("omnira/delivery").Int64Counter("channel_delivery_total")
	return &Handler{store: store, sender: sender, maxAttempts: maxAttempts, outcomes: counter}, nil
}

type envelope struct {
	AggregateID string `json:"aggregate_id"`
}

// Handle delivers one queued message. nil acks the job (delivered, recorded as
// failed, or already processed); ErrPermanent terminates it; any other error
// asks for a redelivery.
//
// Two phases, PILOT.4A1:
//
//  1. RESERVATION — obtain (or reuse) a stable provider message id and commit
//     it to Postgres in its own transaction, BEFORE any provider call.
//  2. DELIVERY — call the provider with that exact id, then mark the result.
//
// Why the split matters: a worker crash (or any error) between a successful
// SendText and the commit of MarkSent used to leave no durable trace that the
// send had already happened — a redelivery would call SendText again, and
// without a stable id the provider had no way to recognize it as the same
// message (previously documented here as an accepted, if narrow, duplicate
// window). Because the reserved id is committed BEFORE delivery starts, it
// survives that exact crash: a redelivery reads the same persisted id and
// resends it unchanged. WAHA/GOWS 2026.8.2 has been runtime-proven
// (PILOT.4A0) to deduplicate a repeated send carrying an identical message id
// down to exactly one visible WhatsApp delivery — so reusing the same id on
// retry is safe against duplicate customer-visible sends for this exact
// provider/version, not a general at-least-once-becomes-exactly-once claim.
//
// Terminal outcome model (PILOT.4A2): OMNIRA only ever writes a status it can
// actually prove.
//
//   - sent      — CONFIRMED_SUCCESS: the provider returned the exact reserved id.
//   - failed    — CONFIRMED_FAILURE: a deterministic provider rejection
//     (authentication, permanent validation, session disconnected,
//     configuration). Retrying would not help; the provider proved the
//     request was never dispatched.
//   - uncertain — OUTCOME_UNKNOWN: every attempt was non-confirming (transport
//     ambiguity, provider unavailable, rate limited, or a malformed/empty
//     response) and the retry budget is exhausted, OR the provider returned a
//     message id that didn't match what was reserved (terminal immediately —
//     see ports.ErrProviderIDMismatch, not safe to auto-retry). Never
//     reported as 'failed': OMNIRA cannot prove the message wasn't delivered.
//
// A confirmed rejection on any attempt still terminates as 'failed' even if
// earlier attempts on the same message were non-confirming — each attempt is
// classified independently, and whichever branch a given attempt reaches
// decides the outcome; no cross-attempt state is needed.
func (h *Handler) Handle(ctx context.Context, raw []byte, attempt int) error {
	var job envelope
	if err := json.Unmarshal(raw, &job); err != nil {
		return fmt.Errorf("%w: malformed envelope", ErrPermanent)
	}
	messageID, err := uuid.Parse(job.AggregateID)
	if err != nil || messageID == uuid.Nil {
		return fmt.Errorf("%w: invalid message reference", ErrPermanent)
	}

	// Read-only peek (own short transaction) to learn the connection and
	// whether a reservation or terminal status already exists, before
	// deciding whether phase 1 is even needed. Also captures tenant_id
	// (PILOT.4B) for the log lines below, which run outside any tenant
	// session — RunForMessage's tenant context does not outlive its closure.
	var peek *OutboundJob
	tenantID := "unknown"
	if err := h.store.RunForMessage(ctx, messageID, func(scoped context.Context) error {
		var err error
		peek, err = h.store.LockOutbound(scoped, messageID)
		tenantID = tenantIDString(scoped)
		return err
	}); err != nil {
		return err
	}
	if peek == nil {
		return fmt.Errorf("%w: unknown message", ErrPermanent)
	}
	if peek.Status != "queued" || peek.ProviderMessageID != "" {
		h.count(ctx, "already_processed")
		log.Printf("channel delivery: skipped, already terminal message_id=%s tenant_id=%s status=%s", messageID, tenantID, peek.Status)
		return nil
	}
	if peek.TenantSuspended {
		return h.failSuspended(ctx, messageID)
	}
	if !peek.ConnectionActive || peek.ConnectionID == uuid.Nil {
		h.count(ctx, "channel_inactive")
		return h.store.RunForMessage(ctx, messageID, func(scoped context.Context) error {
			return h.store.MarkFailed(scoped, messageID, "channel_not_active")
		})
	}
	log.Printf("channel delivery: attempt started message_id=%s tenant_id=%s attempt=%d", messageID, tenantID, attempt)

	// PHASE 1 — RESERVATION. Skipped entirely (no provider call, no new id)
	// when a reservation already exists — this is what makes redelivery,
	// timeout retry and worker-restart safe: reuse, never re-mint.
	//
	// Runs inside its own tenant session (RunForMessage) — NOT the row lock
	// LockOutbound takes — because resolving the connection/provider (needed
	// to call the provider at all) goes through RLS and requires a tenant
	// GUC to be set. What must never happen, and doesn't here, is holding the
	// message row's FOR UPDATE lock while this network call is in flight.
	reservedID := peek.ReservedProviderMessageID
	if peek.Media != nil && reservedID == "" {
		// No provider deduplicates a media send by id, so there is nothing to reserve; the message id is only a correlation key.
		reservedID = messageID.String()
	}
	if reservedID == "" {
		err = h.store.RunForMessage(ctx, messageID, func(scoped context.Context) error {
			var genErr error
			reservedID, genErr = h.store.EnsureReservedProviderMessageID(scoped, messageID, func(genCtx context.Context) (string, error) {
				return h.sender.NewMessageID(genCtx, peek.ConnectionID)
			})
			return genErr
		})
		switch {
		case errors.Is(err, ports.ErrCapabilityNotSupported):
			// Provider has no stable-id mechanism: fall back to the
			// pre-PILOT.4A1 behavior (message id as the idempotency key) —
			// unchanged for any provider without this capability.
			reservedID = messageID.String()
		case err != nil:
			return err
		}
	}

	// PHASE 2 — DELIVERY. Re-lock: status may have changed since the peek
	// (e.g. another delivery already completed it), so the terminal-state
	// guard runs again for real, inside the transaction that decides the
	// outcome.
	return h.store.RunForMessage(ctx, messageID, func(scoped context.Context) error {
		out, err := h.store.LockOutbound(scoped, messageID)
		if err != nil {
			return err
		}
		if out == nil {
			return fmt.Errorf("%w: unknown message", ErrPermanent)
		}
		if out.Status != "queued" || out.ProviderMessageID != "" {
			h.count(scoped, "already_processed")
			return nil
		}
		if out.TenantSuspended {
			h.count(scoped, "company_suspended")
			return h.store.MarkFailed(scoped, messageID, "company_suspended")
		}
		if !out.ConnectionActive || out.ConnectionID == uuid.Nil {
			h.count(scoped, "channel_inactive")
			return h.store.MarkFailed(scoped, messageID, "channel_not_active")
		}
		var result *domain.SendResult
		var sendErr error
		if out.Media != nil {
			result, sendErr = h.sendMedia(scoped, out, reservedID)
			if errors.Is(sendErr, errMediaUnreadable) {
				h.count(scoped, "failed")
				log.Printf("channel delivery: failed message_id=%s tenant_id=%s outcome=failed error_class=media_unavailable", messageID, tenantIDString(scoped))
				return h.store.MarkFailed(scoped, messageID, "media_unavailable")
			}
		} else if out.Template != nil {
			result, sendErr = h.sender.SendTemplate(scoped, out.ConnectionID, domain.OutboundTemplateMessage{
				ToE164: out.ToE164, TemplateName: out.Template.Name, LanguageCode: out.Template.Language, Params: out.Template.Params, IdempotencyKey: reservedID,
			})
		} else {
			sendPlain := func() (*domain.SendResult, error) {
				return h.sender.SendText(scoped, out.ConnectionID, domain.OutboundTextMessage{
					ToE164: out.ToE164, ProviderChatID: out.ProviderChatID, Text: out.Text, IdempotencyKey: reservedID,
				})
			}
			if out.Interactive != nil {
				result, sendErr = h.sender.SendInteractive(scoped, out.ConnectionID, domain.OutboundInteractiveMessage{
					ToE164: out.ToE164, Body: out.Interactive.Body, ListLabel: out.Interactive.ListLabel, Options: out.Interactive.Options,
				})
				if errors.Is(sendErr, ports.ErrCapabilityNotSupported) {
					// no buttons on this provider: the very same menu as numbered text (nothing was sent yet)
					result, sendErr = sendPlain()
				}
			} else {
				result, sendErr = sendPlain()
			}
		}
		switch {
		case sendErr == nil:
			h.count(scoped, "sent")
			log.Printf("channel delivery: sent message_id=%s tenant_id=%s outcome=sent", messageID, tenantIDString(scoped))
			return h.store.MarkSent(scoped, messageID, result.ProviderMessageID)
		case errors.Is(sendErr, ports.ErrOutcomeUnknown):
			// The provider (Meta Cloud) has no idempotency key: after an ambiguous failure a second attempt could
			// deliver the message twice. Terminal 'uncertain', never retried automatically, a person decides.
			h.count(scoped, "uncertain")
			h.logUncertain(scoped, messageID, "outcome_unknown:ambiguous_send")
			return h.store.MarkUncertain(scoped, messageID, "outcome_unknown:ambiguous_send")
		case errors.Is(sendErr, ports.ErrProviderIDMismatch):
			// OUTCOME_UNKNOWN_TERMINAL (PILOT.4A2): never retried automatically
			// — the provider may already have dispatched something under the
			// unexpected id, so resending with the reserved id is not proven
			// safe the way same-id redelivery is (PILOT.4A0 only covers a
			// repeated submission of the SAME id). Terminal on the very first
			// occurrence, not after exhausting the retry budget.
			h.count(scoped, "uncertain")
			h.logUncertain(scoped, messageID, "outcome_unknown:provider_id_mismatch")
			return h.store.MarkUncertain(scoped, messageID, "outcome_unknown:provider_id_mismatch")
		case Retryable(sendErr) && attempt < h.maxAttempts:
			h.count(scoped, "retry")
			log.Printf("channel delivery: retry message_id=%s tenant_id=%s attempt=%d outcome=retry error_class=%s", messageID, tenantIDString(scoped), attempt, Classify(sendErr))
			return sendErr // rolls the transaction back; the message stays queued, reservation stays committed
		case Retryable(sendErr):
			// OUTCOME_UNKNOWN_RETRYABLE_WITH_STABLE_ID, budget exhausted: every
			// attempt was non-confirming (transport ambiguity, provider
			// unavailable, rate limited, or a malformed/empty response) — never
			// a proven rejection, so this is 'uncertain', not 'failed'.
			h.count(scoped, "uncertain")
			reason := "outcome_unknown:" + Classify(sendErr)
			h.logUncertain(scoped, messageID, reason)
			return h.store.MarkUncertain(scoped, messageID, reason)
		default:
			// CONFIRMED_FAILURE: a deterministic provider rejection.
			h.count(scoped, "failed")
			log.Printf("channel delivery: failed message_id=%s tenant_id=%s outcome=failed error_class=%s", messageID, tenantIDString(scoped), Classify(sendErr))
			return h.store.MarkFailed(scoped, messageID, Classify(sendErr))
		}
	})
}

// errMediaUnreadable: the file is gone, altered or the worker has no media store - a deterministic failure, nothing was sent.
var errMediaUnreadable = errors.New("channel delivery: outbound media is unreadable")

func (h *Handler) sendMedia(ctx context.Context, out *OutboundJob, correlationID string) (*domain.SendResult, error) {
	if h.files == nil {
		return nil, errMediaUnreadable
	}
	data, err := h.files.ReadVerified(out.Media.TenantID, out.Media.AttachmentID, out.Media.Size, out.Media.SHA256)
	if err != nil {
		return nil, errMediaUnreadable
	}
	return h.sender.SendMedia(ctx, out.ConnectionID, domain.OutboundMediaMessage{
		ToE164: out.ToE164, ProviderChatID: out.ProviderChatID, Kind: domain.MediaKind(out.Media.Kind),
		Mime: out.Media.Mime, FileName: out.Media.FileName, Data: data, Caption: out.Text, IdempotencyKey: correlationID,
	})
}

func (h *Handler) count(ctx context.Context, outcome string) {
	if h.outcomes != nil {
		h.outcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
	}
}

// logUncertain (PILOT.4A2 §20) is the minimal detection hook for an unproven
// terminal outcome — a full alert/observability pipeline is PILOT.4B's job,
// not this slice's. Only opaque identifiers and the normalized reason are
// logged: never message body, phone number, or raw provider payload.
func (h *Handler) logUncertain(ctx context.Context, messageID uuid.UUID, reason string) {
	log.Printf("channel delivery: uncertain message_id=%s tenant_id=%s outcome=uncertain error_class=%s", messageID, tenantIDString(ctx), reason)
}

// tenantIDString (PILOT.4B) — best-effort, non-fatal tenant lookup for log
// lines: logging must never fail or block delivery over a missing tenant
// context, so this returns "unknown" instead of an error.
func tenantIDString(ctx context.Context) string {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil {
		return "unknown"
	}
	return tc.TenantID.String()
}

// Retryable is the worker retry policy for provider failures. Authentication,
// configuration, permanent validation, and logged-out sessions are terminal.
//
// ErrUnknown (PILOT.4A2) is included here: a malformed/undecodable response
// or an empty response id is safe to retry with the SAME reserved provider
// message id — the request was already sent carrying that stable id, and
// PILOT.4A0 proved the provider deduplicates a repeated submission of it.
// ports.ErrProviderIDMismatch is deliberately NOT included — see its doc and
// the dedicated switch case in Handle: it terminates immediately as
// 'uncertain' instead, because that anomaly is not covered by the same-id
// dedup proof.
func Retryable(err error) bool {
	return errors.Is(err, ports.ErrTransient) ||
		errors.Is(err, ports.ErrRateLimited) ||
		errors.Is(err, ports.ErrProviderUnavailable) ||
		errors.Is(err, ports.ErrUnknown)
}

// Classify maps a provider error to a low-cardinality reason. Provider error
// text is never persisted or exposed: it may contain identifiers.
func Classify(err error) string {
	switch {
	case errors.Is(err, ports.ErrAuthentication):
		return "authentication"
	case errors.Is(err, ports.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, ports.ErrSessionDisconnected):
		return "session_disconnected"
	case errors.Is(err, ports.ErrProviderUnavailable):
		return "provider_unavailable"
	case errors.Is(err, ports.ErrNotConfigured), errors.Is(err, ports.ErrConfiguration):
		return "configuration"
	case errors.Is(err, ports.ErrSessionWindowClosed):
		return "window_closed"
	case errors.Is(err, ports.ErrTransient):
		return "transient"
	case errors.Is(err, ports.ErrPermanent):
		return "rejected"
	default:
		return "unknown"
	}
}

// failSuspended ends the attempt for a message whose company is suspended: nothing reaches the provider and the message
// is marked failed with a reason a person can read. Nothing is held back to be sent later: after a reactivation a stale
// answer must not go out on its own, the agent decides whether to write again.
func (h *Handler) failSuspended(ctx context.Context, messageID uuid.UUID) error {
	h.count(ctx, "company_suspended")
	return h.store.RunForMessage(ctx, messageID, func(scoped context.Context) error {
		return h.store.MarkFailed(scoped, messageID, "company_suspended")
	})
}
