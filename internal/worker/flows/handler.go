// Package flows is the worker side of the Flow Builder (ADR-0019): the JetStream consumer for inbound-message jobs and the
// sweeper for timeouts and stranded conversations. Every step is one tenant system session = one transaction.
package flows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// ErrPermanent marks a job that can never succeed (malformed envelope): the consumer terminates it instead of retrying.
var ErrPermanent = errors.New("flows worker: permanent job error")

// ConversationRunner runs fn in a system session of the tenant that OWNS the conversation, read from persisted state.
type ConversationRunner interface {
	RunForConversation(ctx context.Context, conversationID uuid.UUID, fn func(context.Context) error) error
}

type PostgresConversationRunner struct{ pool *pgxpool.Pool }

func NewPostgresConversationRunner(pool *pgxpool.Pool) *PostgresConversationRunner {
	return &PostgresConversationRunner{pool: pool}
}

func (r *PostgresConversationRunner) RunForConversation(ctx context.Context, conversationID uuid.UUID, fn func(context.Context) error) error {
	if r == nil || r.pool == nil || conversationID == uuid.Nil || fn == nil {
		return fmt.Errorf("%w: invalid conversation runner input", ErrPermanent)
	}
	var tenantID uuid.UUID
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(system context.Context) error {
		return platformdb.QuerierFromContext(system, r.pool).QueryRow(system, `SELECT tenant_id FROM conversations WHERE id=$1`, conversationID).Scan(&tenantID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: unknown conversation", ErrPermanent)
	}
	if err != nil {
		return err
	}
	return platformdb.WithSystemTenantSession(ctx, r.pool, tenantID, func(c context.Context) error {
		// A suspended company is not served (ADR-0038): its flows neither start nor advance. The job is simply done (the
		// message is already stored); the share lock keeps the suspension from committing in the middle of a step.
		active, err := platformdb.LockTenantActive(c, platformdb.QuerierFromContext(c, r.pool), tenantID)
		if err != nil {
			return err
		}
		if !active {
			return nil
		}
		return fn(c)
	})
}

type Handler struct {
	runner ConversationRunner
	engine *application.Engine
}

func NewHandler(runner ConversationRunner, engine *application.Engine) (*Handler, error) {
	if runner == nil || engine == nil {
		return nil, errors.New("flows worker: runner and engine are required")
	}
	return &Handler{runner: runner, engine: engine}, nil
}

type envelope struct {
	AggregateID string `json:"aggregate_id"`
	Payload     struct {
		MessageID       string `json:"message_id"`
		NewConversation bool   `json:"new_conversation"`
	} `json:"payload"`
}

// Handle trusts only the persisted conversation reference: a tenant_id in the envelope is ignored and never becomes
// authorization. Redelivery is safe (the engine recognises a consumed message), and losing a race to a twin is success.
func (h *Handler) Handle(ctx context.Context, raw []byte) error {
	var job envelope
	if err := json.Unmarshal(raw, &job); err != nil {
		return fmt.Errorf("%w: malformed envelope", ErrPermanent)
	}
	conversationID, err := uuid.Parse(job.AggregateID)
	if err != nil || conversationID == uuid.Nil {
		return fmt.Errorf("%w: invalid conversation reference", ErrPermanent)
	}
	messageID, err := uuid.Parse(job.Payload.MessageID)
	if err != nil || messageID == uuid.Nil {
		return fmt.Errorf("%w: invalid message reference", ErrPermanent)
	}
	err = h.runner.RunForConversation(ctx, conversationID, func(scoped context.Context) error {
		_, err := h.engine.OnInbound(scoped, application.InboundEvent{ConversationID: conversationID, MessageID: messageID, NewConversation: job.Payload.NewConversation})
		return err
	})
	switch {
	case err == nil,
		errors.Is(err, domain.ErrDuplicateEvent), errors.Is(err, domain.ErrConversationBusy), // a twin already handled it
		errors.Is(err, domain.ErrNotFound): // conversation or message no longer exists: nothing left to do
		return nil
	}
	return err
}
