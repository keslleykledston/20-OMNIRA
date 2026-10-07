package adapters

import (
	"context"
	"log"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// JobFlowInbound is the outbox/JetStream job that tells the worker a conversation has a new inbound message for the flows.
// The payload carries ids only: no text, no phone, and never a tenant (the worker derives it from the conversation).
const JobFlowInbound = "job.flow.inbound.v1"

// Gate is what the inbound pipeline calls (inbox/application.FlowGate). It must NEVER make ingest fail or lose a message:
// every call runs in a savepoint, errors are logged and swallowed, and "no flow" is the default answer, which leaves the
// legacy routing exactly as it was.
type Gate struct {
	pool *pgxpool.Pool
	repo *PostgresFlowRepository
	logf func(string, ...any)
}

func NewGate(pool *pgxpool.Pool, repo *PostgresFlowRepository) *Gate {
	return &Gate{pool: pool, repo: repo, logf: log.Printf}
}

// Engage is called for a NEW conversation of an external contact, right before default routing. It returns true when a
// published flow will take the conversation: the caller then skips routing (the flow routes it on handoff or on any end).
func (g *Gate) Engage(ctx context.Context, conversationID uuid.UUID) (held bool) {
	err := platformdb.WithSavepoint(ctx, g.pool, func(ctx context.Context) error {
		facts, err := g.repo.LoadConversation(ctx, conversationID) // row lock for the rest of the ingest transaction
		if err != nil {
			return err
		}
		if !application.Startable(facts) || facts.AutomationMode != domain.AutomationNone {
			return nil
		}
		flows, err := g.repo.CandidateFlows(ctx)
		if err != nil {
			return err
		}
		if application.PickFlow(flows, facts) == nil {
			return nil
		}
		if err := g.repo.SetAutomationMode(ctx, conversationID, domain.AutomationBot); err != nil {
			return err
		}
		held = true
		return nil
	})
	if err != nil {
		g.logf("flows: gate engage failed, routing as usual: %v", err)
		return false
	}
	return held
}

// OnInbound enqueues the flow job for a persisted inbound message, in the same transaction as the message itself (so the
// job exists exactly when the message does). Only conversations a flow can act on are enqueued: held by the bot, waiting in
// the queue for a human nobody has taken yet (the contact may still end the attendance, see CustomerExit), or open,
// unassigned external conversations when some published INBOUND flow restarts on every message (the cheap part of
// Startable; an identity conflict is left to the engine, so this filter may over-enqueue but never misses a start).
func (g *Gate) OnInbound(ctx context.Context, conversationID, messageID uuid.UUID, newConversation bool) bool {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return false
	}
	err = platformdb.WithSavepoint(ctx, g.pool, func(ctx context.Context) error {
		_, err := g.repo.q(ctx).Exec(ctx, `
			INSERT INTO outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, payload)
			SELECT $1, $2, $3, 'conversation', $4, $5, jsonb_build_object('message_id', $6::text, 'new_conversation', $7::boolean)
			WHERE EXISTS (SELECT 1 FROM conversations WHERE tenant_id = $2 AND id = $8 AND automation_mode = 'bot')
			   OR EXISTS (SELECT 1 FROM conversations WHERE tenant_id = $2 AND id = $8 AND automation_mode = 'waiting_human'
			              AND status = 'open' AND assigned_to_user_id IS NULL)
			   OR (EXISTS (SELECT 1 FROM flows WHERE tenant_id = $2 AND status = 'published' AND flow_type = 'INBOUND' AND restart_policy = 'always')
			       AND EXISTS (SELECT 1 FROM conversations WHERE tenant_id = $2 AND id = $8 AND status = 'open' AND assigned_to_user_id IS NULL
			                   AND contact_id IS NOT NULL AND conversation_kind IN ('customer_service','unclassified')))`,
			uuid.New(), tc.TenantID, JobFlowInbound, conversationID.String(), uuid.New(), messageID.String(), newConversation, conversationID)
		return err
	})
	if err != nil {
		g.logf("flows: gate could not enqueue the inbound job (message %s): %v", messageID, err)
		return false
	}
	return true
}

// Release takes back the bot hold Engage placed on a new conversation whose job could not be enqueued (the caller then routes
// it the normal way). Best effort and contained: if this fails too, the sweeper's stranded-conversation backstop still applies.
func (g *Gate) Release(ctx context.Context, conversationID uuid.UUID) {
	err := platformdb.WithSavepoint(ctx, g.pool, func(ctx context.Context) error {
		return g.repo.SetAutomationMode(ctx, conversationID, domain.AutomationNone)
	})
	if err != nil {
		g.logf("flows: gate could not release conversation %s (the sweeper will): %v", conversationID, err)
	}
}
