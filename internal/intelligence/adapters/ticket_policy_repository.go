package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

type PostgresTicketPolicyRepository struct{ pool *pgxpool.Pool }

var _ ports.TicketPolicyRepository = (*PostgresTicketPolicyRepository)(nil)

func NewPostgresTicketPolicyRepository(pool *pgxpool.Pool) *PostgresTicketPolicyRepository {
	return &PostgresTicketPolicyRepository{pool: pool}
}

func (r *PostgresTicketPolicyRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

func (r *PostgresTicketPolicyRepository) TopicPolicyFacts(ctx context.Context, tenantID, topicID uuid.UUID) (ports.PolicyFacts, error) {
	var f ports.PolicyFacts
	var status string
	err := r.q(ctx).QueryRow(ctx, `SELECT status FROM topic_threads WHERE tenant_id=$1 AND id=$2`, tenantID, topicID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, domain.ErrTopicNotFound
	}
	if err != nil {
		return f, err
	}
	f.Open = status == "open"
	if err := r.q(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM topic_ticket_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND relation='primary')`, tenantID, topicID).Scan(&f.HasPrimary); err != nil {
		return f, err
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT conversation_id FROM topic_conversation_links WHERE tenant_id=$1 AND topic_thread_id=$2 ORDER BY created_at`, tenantID, topicID)
	if err != nil {
		return f, err
	}
	for rows.Next() {
		var c uuid.UUID
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return f, err
		}
		f.Conversations = append(f.Conversations, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return f, err
	}
	if err := r.q(ctx).QueryRow(ctx, `SELECT count(*) FROM topic_group_links WHERE tenant_id=$1 AND topic_thread_id=$2`, tenantID, topicID).Scan(&f.GroupLinks); err != nil {
		return f, err
	}
	if len(f.Conversations) == 1 {
		if err := r.q(ctx).QueryRow(ctx, `
			SELECT count(*) FROM topic_threads t JOIN topic_conversation_links l ON l.tenant_id=t.tenant_id AND l.topic_thread_id=t.id
			WHERE t.tenant_id=$1 AND l.conversation_id=$2 AND t.status='open' AND t.id <> $3`, tenantID, f.Conversations[0], topicID).Scan(&f.OtherOpenTopics); err != nil {
			return f, err
		}
	}
	err = r.q(ctx).QueryRow(ctx, `
		SELECT (SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND relation IN ('primary','secondary','supporting'))
		     + (SELECT count(*) FROM group_message_topic_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND relation IN ('primary','secondary','supporting'))`, tenantID, topicID).Scan(&f.MessageCount)
	return f, err
}

func (r *PostgresTicketPolicyRepository) ActiveTicket(ctx context.Context, tenantID, conversationID uuid.UUID) (*domain.ActiveTicketFacts, error) {
	var f domain.ActiveTicketFacts
	err := r.q(ctx).QueryRow(ctx, `
		SELECT t.id, (SELECT l.topic_thread_id FROM topic_ticket_links l WHERE l.tenant_id=t.tenant_id AND l.ticket_id=t.id AND l.relation='primary')
		FROM tickets t WHERE t.tenant_id=$1 AND t.conversation_id=$2 AND t.status IN ('open','in_progress','waiting')
		ORDER BY t.topic_scoped ASC, t.updated_at DESC, t.id DESC LIMIT 1`, tenantID, conversationID).Scan(&f.TicketID, &f.PrimaryTopicID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &f, err
}

func (r *PostgresTicketPolicyRepository) LockConversation(ctx context.Context, tenantID, conversationID uuid.UUID) error {
	_, err := r.q(ctx).Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ticket-policy:' || $1::text || ':' || $2::text, 0))`, tenantID.String(), conversationID.String())
	return err
}

func (r *PostgresTicketPolicyRepository) LegacyTicket(ctx context.Context, tenantID, conversationID uuid.UUID) (*ports.LegacyTicket, error) {
	var t ports.LegacyTicket
	err := r.q(ctx).QueryRow(ctx, `
		SELECT t.id, t.subject FROM tickets t
		WHERE t.tenant_id=$1 AND t.conversation_id=$2 AND t.status IN ('open','in_progress','waiting')
		  AND NOT EXISTS (SELECT 1 FROM topic_ticket_links l WHERE l.tenant_id=t.tenant_id AND l.ticket_id=t.id)
		ORDER BY t.updated_at DESC, t.id DESC LIMIT 1`, tenantID, conversationID).Scan(&t.ID, &t.Subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &t, err
}
