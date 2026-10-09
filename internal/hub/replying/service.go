// Package replying is the Hub's WRITE path: an agent claims a delegated conversation and answers it.
//
// Design (docs/adr/0037-hub-reply-write-path.md):
//
//  1. Authorize in the CALLER's own RLS session: hub membership, the persisted item, the conversation's CURRENT queue,
//     a live grant + contract + queue scope, and the grant's can_reply capability (Hub context, Source=hub).
//  2. Execute in a system tenant session whose tenant comes from the persisted item, never from the request, and
//     re-check the delegation INSIDE that transaction with the same SQL function RLS uses (has_active_hub_access with
//     p_require_reply). A grant revoked between 1 and 2 stops the write.
//  3. No INSERT/UPDATE policy is added to conversations, messages, outbox or idempotency tables for delegated users:
//     the database still refuses every direct write by a Hub agent; this service is the only door and it audits.
package replying

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/application"
	"github.com/omnira/omnira/internal/hub/domain"
	messagesapp "github.com/omnira/omnira/internal/messages/application"
	messagesports "github.com/omnira/omnira/internal/messages/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

var (
	// ErrTenantMismatch: the client's idea of "the company I am answering as" is not the item's real tenant.
	ErrTenantMismatch = errors.New("replying: the company shown to the agent is not the item's company")
	// ErrTaken: someone else already holds the conversation.
	ErrTaken = errors.New("replying: conversation is assigned to another agent")
	// ErrClosed: the attendance was finalized.
	ErrClosed = errors.New("replying: conversation is closed")
)

// ItemLoader reads an inbox item through the caller's RLS session.
type ItemLoader interface {
	GetHubInboxItemByID(ctx context.Context, hubID, itemID uuid.UUID) (*domain.HubInboxItem, error)
}

type Service struct {
	pool   *pgxpool.Pool
	authz  *application.HubAuthorizationService
	items  ItemLoader
	sender *messagesapp.DelegatedSender
}

func New(pool *pgxpool.Pool, authz *application.HubAuthorizationService, items ItemLoader, store messagesports.OutboundStore) *Service {
	return &Service{pool: pool, authz: authz, items: items, sender: messagesapp.NewDelegatedSender(store)}
}

// Target is what a successful authorization resolved on the server.
type Target struct {
	Item       *domain.HubInboxItem
	TenantName string
	QueueID    *uuid.UUID // the conversation's current queue
	Access     *tenancydomain.TenantContext
}

// Authorize is step 1. It must be called with the request context (the caller's own RLS session). The company the
// agent is about to speak as is in the result; expectedTenant (what the screen displayed) must equal it.
func (s *Service) Authorize(ctx context.Context, actor, hubID, itemID, expectedTenant uuid.UUID, correlation string) (*Target, error) {
	if err := s.authz.AuthorizeHubMember(ctx, actor, hubID); err != nil {
		return nil, err
	}
	item, err := s.items.GetHubInboxItemByID(ctx, hubID, itemID)
	if err != nil {
		return nil, fmt.Errorf("load inbox item: %w", err)
	}
	if item == nil {
		return nil, application.ErrAccessDenied
	}
	var queue *uuid.UUID
	var name string
	err = platformdb.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		SELECT c.queue_id, COALESCE(NULLIF(t.trade_name, ''), t.legal_name)
		FROM conversations c JOIN tenants t ON t.id = c.tenant_id
		WHERE c.id = $1 AND c.tenant_id = $2`, item.ConversationID, item.TenantID).Scan(&queue, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, application.ErrAccessDenied // RLS hides it (out of scope / revoked) or it vanished
	}
	if err != nil {
		return nil, fmt.Errorf("load conversation: %w", err)
	}
	tc, err := s.authz.ResolveHubAccess(ctx, application.HubAccessRequest{
		ActorID: actor, HubID: hubID, TenantID: item.TenantID, QueueID: queue, CorrelationID: correlation, RequireReply: true,
	})
	if err != nil {
		return nil, err
	}
	if expectedTenant != item.TenantID {
		return nil, ErrTenantMismatch
	}
	return &Target{Item: item, TenantName: name, QueueID: queue, Access: tc}, nil
}

// delegationHolds re-asks the database, inside the writing transaction, whether the actor still holds a live
// reply-capable delegation for the conversation's CURRENT queue. The conversation row is locked so the queue cannot
// change between this answer and the write.
func (s *Service) delegationHolds(ctx context.Context, t *Target, actor uuid.UUID, lock string) (assigned *uuid.UUID, closed bool, err error) {
	q := platformdb.QuerierFromContext(ctx, s.pool)
	var queue *uuid.UUID
	var status string
	err = q.QueryRow(ctx, `SELECT queue_id, status, assigned_to_user_id FROM conversations WHERE tenant_id = $1 AND id = $2 `+lock,
		t.Item.TenantID, t.Item.ConversationID).Scan(&queue, &status, &assigned)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, application.ErrAccessDenied
	}
	if err != nil {
		return nil, false, fmt.Errorf("lock conversation: %w", err)
	}
	// Pin the authorization rows until this transaction ends. Revoking a grant, ending a contract, removing the person
	// from the Hub, pausing the Hub or suspending the company each UPDATE/DELETE one of these rows, so the change either
	// commits before the answer below (and is seen by it) or waits until this write is done. Without the pin, a
	// revocation committed between the check and the INSERT would still be followed by a write (Codex H1).
	// One statement per table (a JOIN here would let PostgreSQL's re-check drop the row after a concurrent update), always
	// in the same order the administrative paths take them: company, hub, contract, membership, grant.
	for _, pin := range []struct {
		sql  string
		args []any
	}{
		{`SELECT 1 FROM tenants WHERE id = $1 FOR SHARE`, []any{t.Item.TenantID}},
		{`SELECT 1 FROM service_hubs WHERE id = $1 FOR SHARE`, []any{t.Item.HubID}},
		{`SELECT 1 FROM hub_tenant_service_contracts WHERE hub_id = $1 AND tenant_id = $2 FOR SHARE`, []any{t.Item.HubID, t.Item.TenantID}},
		{`SELECT 1 FROM hub_memberships WHERE hub_id = $1 AND user_id = $2 FOR SHARE`, []any{t.Item.HubID, actor}},
		{`SELECT 1 FROM effective_access_grants WHERE hub_id = $1 AND tenant_id = $2 AND user_id = $3 FOR SHARE`, []any{t.Item.HubID, t.Item.TenantID, actor}},
	} {
		rows, err := q.Query(ctx, pin.sql, pin.args...)
		if err != nil {
			return nil, false, fmt.Errorf("pin authorization: %w", err)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, false, fmt.Errorf("pin authorization: %w", err)
		}
	}
	var ok bool
	if err := q.QueryRow(ctx, `SELECT has_active_hub_access($1, $2, $3, $4, true, true)`,
		actor, t.Item.TenantID, queue, t.Item.HubID).Scan(&ok); err != nil {
		return nil, false, fmt.Errorf("re-check delegation: %w", err)
	}
	if !ok {
		return nil, false, application.ErrAccessDenied
	}
	return assigned, status == "closed", nil
}

func (s *Service) audit(ctx context.Context, t *Target, actor uuid.UUID, action, resourceType string, resource uuid.UUID, meta map[string]any) error {
	meta["hub_id"] = t.Item.HubID
	meta["conversation_id"] = t.Item.ConversationID
	meta["grant_id"] = t.Access.EffectiveGrantID
	meta["contract_id"] = t.Access.ServiceContractID
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = platformdb.QuerierFromContext(ctx, s.pool).Exec(ctx, `
		INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, 'success', $7, $8)`,
		uuid.New(), t.Item.TenantID, actor, action, resourceType, resource.String(), t.Access.CorrelationID, raw)
	return err
}

// Claim assigns the conversation to the agent. Idempotent for the holder; a second agent gets ErrTaken, so of two
// simultaneous claimers exactly one wins (the conversation row is locked FOR UPDATE).
func (s *Service) Claim(ctx context.Context, actor uuid.UUID, t *Target) (changed bool, err error) {
	err = platformdb.WithSystemTenantSession(ctx, s.pool, t.Item.TenantID, func(c context.Context) error {
		assigned, closed, err := s.delegationHolds(c, t, actor, "FOR UPDATE")
		if err != nil {
			return err
		}
		if closed {
			return ErrClosed
		}
		if assigned != nil {
			if *assigned == actor {
				return nil
			}
			return ErrTaken
		}
		q := platformdb.QuerierFromContext(c, s.pool)
		if _, err := q.Exec(c, `UPDATE conversations SET assigned_to_user_id = $3, assigned_at = now(), updated_at = now()
		                        WHERE tenant_id = $1 AND id = $2`, t.Item.TenantID, t.Item.ConversationID, actor); err != nil {
			return fmt.Errorf("claim: %w", err)
		}
		if _, err := q.Exec(c, `INSERT INTO assignment_events (tenant_id, conversation_id, from_user_id, to_user_id, changed_by, reason)
		                        VALUES ($1, $2, NULL, $3, $3, 'hub_claim')`, t.Item.TenantID, t.Item.ConversationID, actor); err != nil {
			return fmt.Errorf("claim history: %w", err)
		}
		changed = true
		return s.audit(c, t, actor, "hub.conversation.claimed", "conversation", t.Item.ConversationID, map[string]any{})
	})
	return changed, err
}

// Send queues the agent's text. The conversation must already be theirs (explicit claim).
func (s *Service) Send(ctx context.Context, actor uuid.UUID, t *Target, text, key string) (messagesapp.SendResult, error) {
	var res messagesapp.SendResult
	err := platformdb.WithSystemTenantSession(ctx, s.pool, t.Item.TenantID, func(c context.Context) error {
		r, err := s.sender.Send(c, actor, t.Item.ConversationID, text, key, func(gc context.Context, _ *messagesports.SendContext) error {
			_, closed, err := s.delegationHolds(gc, t, actor, "FOR SHARE")
			if err == nil && closed {
				return ErrClosed // the context was loaded before the lock: the attendance may have been finalized since
			}
			return err
		})
		if err != nil {
			return err
		}
		res = r
		if r.Replayed {
			return nil
		}
		return s.audit(c, t, actor, "hub.message.sent", "message", r.Message.ID, map[string]any{"message_id": r.Message.ID})
	})
	return res, err
}
