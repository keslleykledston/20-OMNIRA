package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/flows/ports"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	ticketdomain "github.com/omnira/omnira/internal/tickets/domain"
)

// SystemSender is the bot's way to talk to the contact (messages.SystemSender satisfies it).
type SystemSender interface {
	Send(ctx context.Context, conversationID uuid.UUID, text, idempotencyKey string) (messagesapplication.SystemSendStatus, error)
}

// PostgresEffects performs the data/action side of nodes inside the engine's transaction. It owns the SQL so nodes never
// do; every statement filters by the session tenant and relies on RLS.
type PostgresEffects struct {
	pool   *pgxpool.Pool
	sender SystemSender
}

var _ ports.Effects = (*PostgresEffects)(nil)

func NewPostgresEffects(pool *pgxpool.Pool, sender SystemSender) *PostgresEffects {
	return &PostgresEffects{pool: pool, sender: sender}
}

func (e *PostgresEffects) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, e.pool)
}

func (e *PostgresEffects) SendText(ctx context.Context, conversationID uuid.UUID, text, key string) (ports.SendStatus, error) {
	if e.sender == nil {
		return "", errors.New("flows: no system sender configured")
	}
	st, err := e.sender.Send(ctx, conversationID, text, key)
	if err != nil {
		return "", err
	}
	switch st {
	case messagesapplication.SystemQueued:
		return ports.SendQueued, nil
	case messagesapplication.SystemReplayed:
		return ports.SendReplayed, nil
	case messagesapplication.SystemWindowClosed:
		return ports.SendWindowClosed, nil
	}
	return ports.SendNoChannel, nil
}

func (e *PostgresEffects) CustomerCandidates(ctx context.Context, contactID uuid.UUID) ([]ports.CustomerCandidate, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := e.q(ctx).Query(ctx, `
		SELECT a.id, a.name
		FROM contact_account_links l
		JOIN customer_accounts a ON a.tenant_id = l.tenant_id AND a.id = l.account_id
		WHERE l.tenant_id = $1 AND l.contact_id = $2 AND l.status = 'active' AND a.status = 'active'
		ORDER BY l.is_primary DESC, lower(a.name), a.id`, tenantID, contactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ports.CustomerCandidate
	for rows.Next() {
		var c ports.CustomerCandidate
		if err := rows.Scan(&c.AccountID, &c.Name); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetActiveCustomer records the company a conversation is about. It only succeeds when the conversation's own contact has
// an ACTIVE link to that account: a forged or stale id (another contact's company, another tenant's) matches nothing.
func (e *PostgresEffects) SetActiveCustomer(ctx context.Context, conversationID, accountID uuid.UUID) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	tag, err := e.q(ctx).Exec(ctx, `
		UPDATE conversations c SET active_customer_account_id = $3, updated_at = now()
		WHERE c.tenant_id = $1 AND c.id = $2
		  AND EXISTS (SELECT 1 FROM contact_account_links l
		              JOIN customer_accounts a ON a.tenant_id = l.tenant_id AND a.id = l.account_id
		              WHERE l.tenant_id = c.tenant_id AND l.contact_id = c.contact_id AND l.account_id = $3
		                AND l.status = 'active' AND a.status = 'active')`, tenantID, conversationID, accountID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("account %s is not an active company of this conversation's contact", accountID)
	}
	return nil
}

func (e *PostgresEffects) OpenTickets(ctx context.Context, f *ports.ConversationFacts) (ports.TicketSummary, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return ports.TicketSummary{}, err
	}
	var sum ports.TicketSummary
	if f.ContactID == nil {
		return sum, nil
	}
	var first, subject *string
	err = e.q(ctx).QueryRow(ctx, `
		SELECT count(*), (array_agg(t.id::text ORDER BY t.created_at DESC))[1], (array_agg(t.subject ORDER BY t.created_at DESC))[1]
		FROM tickets t JOIN conversations c ON c.tenant_id = t.tenant_id AND c.id = t.conversation_id
		WHERE t.tenant_id = $1 AND c.contact_id = $2 AND t.status IN ('open','in_progress','waiting')
		  AND `+ticketdomain.RealTicketSQL("t")+`
		  AND ($3::uuid IS NULL OR t.customer_account_id = $3)`, tenantID, *f.ContactID, f.ActiveCustomerAccountID).Scan(&sum.Count, &first, &subject)
	if err != nil {
		return sum, err
	}
	if first != nil {
		sum.FirstID = *first
	}
	if subject != nil {
		sum.FirstSubject = *subject
	}
	return sum, nil
}

// EnsureTicket makes the conversation's ticket real. It is idempotent: the conversation has at most one active ticket
// (tickets_active_conversation_uq); a placeholder is adopted (subject/priority/company set), a ticket that is already real
// is left exactly as it is (a flow never overwrites an operator's or the ERP's ticket), and a missing one is created.
func (e *PostgresEffects) EnsureTicket(ctx context.Context, conversationID uuid.UUID, subject, priority string) (uuid.UUID, bool, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return uuid.Nil, false, err
	}
	q := e.q(ctx)
	var id uuid.UUID
	var cur string
	var external *string
	err = q.QueryRow(ctx, `SELECT id, subject, external_ticket_id FROM tickets
		WHERE tenant_id=$1 AND conversation_id=$2 AND status IN ('open','in_progress','waiting') AND NOT topic_scoped FOR UPDATE`, tenantID, conversationID).Scan(&id, &cur, &external)
	switch {
	case err == nil:
		if external != nil || strings.TrimSpace(cur) != "" {
			return id, false, nil
		}
		_, err = q.Exec(ctx, `UPDATE tickets SET subject=$3, priority=$4, updated_at=now(),
			customer_account_id = COALESCE(customer_account_id, (SELECT active_customer_account_id FROM conversations WHERE tenant_id=$1 AND id=$2))
			WHERE tenant_id=$1 AND id=$5`, tenantID, conversationID, subject, priority, id)
		return id, err == nil, err
	case !errors.Is(err, pgx.ErrNoRows):
		return uuid.Nil, false, err
	}
	id = uuid.New()
	_, err = q.Exec(ctx, `INSERT INTO tickets (id, tenant_id, conversation_id, subject, priority, customer_account_id)
		SELECT $1, c.tenant_id, c.id, $3, $4, c.active_customer_account_id FROM conversations c WHERE c.tenant_id=$2 AND c.id=$5`,
		id, tenantID, subject, priority, conversationID)
	if err != nil {
		return uuid.Nil, false, err
	}
	return id, true, nil
}

// assignQueueSQL mirrors the ingest's RouteNew (same columns, same round-robin job) but accepts an explicit queue. A NULL
// $3 means "the tenant's default queue, only when the conversation has none yet" so a handoff never reverts an earlier choice.
const assignQueueSQL = `
	WITH q AS (
	  SELECT id, mode FROM queues
	  WHERE tenant_id = $1 AND (($3::uuid IS NULL AND is_default) OR id = $3)
	  ORDER BY id LIMIT 1
	), routed AS (
	  UPDATE conversations c SET queue_id = q.id, updated_at = now(),
	    routing_retry_at = CASE WHEN q.mode = 'round_robin' THEN now() + interval '90 seconds' ELSE NULL END
	  FROM q
	  WHERE c.tenant_id = $1 AND c.id = $2 AND ($3::uuid IS NOT NULL OR c.queue_id IS NULL)
	    AND c.internal_user_id IS NULL
	  RETURNING c.id, q.mode
	), job AS (
	  INSERT INTO outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, payload)
	  SELECT $4, $1, 'job.routing.assign.v1', 'conversation', id::text, $5, '{}'::jsonb FROM routed WHERE mode = 'round_robin'
	  RETURNING id
	)
	SELECT count(*) FROM routed`

func (e *PostgresEffects) AssignQueue(ctx context.Context, conversationID uuid.UUID, queueID *uuid.UUID) error {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var n int
	if err := e.q(ctx).QueryRow(ctx, assignQueueSQL, tenantID, conversationID, queueID, uuid.New(), uuid.New()).Scan(&n); err != nil {
		return err
	}
	if n == 0 && queueID != nil {
		return fmt.Errorf("queue %s does not exist in this tenant", *queueID)
	}
	return nil // no default queue configured / queue already set: nothing to route, exactly like RouteNew
}

func (e *PostgresEffects) Handoff(ctx context.Context, conversationID uuid.UUID, queueID *uuid.UUID) error {
	if err := e.AssignQueue(ctx, conversationID, queueID); err != nil {
		return err
	}
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = e.q(ctx).Exec(ctx, `UPDATE conversations SET automation_mode='waiting_human', updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, conversationID)
	return err
}
