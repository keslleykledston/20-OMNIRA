package adapters

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresContextRepository reads only what is linked to the topic: conversation messages through message_topic_links and
// group messages through group_message_topic_links. A message of another topic cannot be selected by construction.
type PostgresContextRepository struct{ pool *pgxpool.Pool }

var _ ports.ContextRepository = (*PostgresContextRepository)(nil)

func NewPostgresContextRepository(pool *pgxpool.Pool) *PostgresContextRepository {
	return &PostgresContextRepository{pool: pool}
}

func (r *PostgresContextRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

// The text of a media-only message is the machine reading of its attachment (flagged FromMedia), never the raw file.
const topicRowsSQL = `
	SELECT id, kind, role, pkey, text, at, relation, from_media FROM (
	  SELECT m.id AS id, 'conversation' AS kind,
	         CASE WHEN m.direction = 'inbound' THEN 'customer' ELSE 'agent' END AS role,
	         COALESCE(m.sender_channel_participant_id::text, '') AS pkey,
	         CASE WHEN m.body <> '' THEN m.body ELSE COALESCE(
	           (SELECT ma.body FROM message_media_analysis ma WHERE ma.tenant_id = m.tenant_id AND ma.message_id = m.id AND ma.status = 'done' LIMIT 1), '') END AS text,
	         m.created_at AS at, l.relation AS relation,
	         (m.body = '' AND EXISTS (SELECT 1 FROM message_media_analysis ma WHERE ma.tenant_id = m.tenant_id AND ma.message_id = m.id AND ma.status = 'done')) AS from_media
	  FROM message_topic_links l JOIN messages m ON m.tenant_id = l.tenant_id AND m.id = l.message_id
	  WHERE l.tenant_id = $1 AND l.topic_thread_id = $2 AND l.relation IN ('primary','secondary','supporting')
	  UNION ALL
	  SELECT g.id, 'group', CASE WHEN g.from_me THEN 'agent' ELSE 'participant' END,
	         COALESCE(g.sender_channel_participant_id::text, ''), g.body, g.sent_at, l.relation, false
	  FROM group_message_topic_links l JOIN wa_group_messages g ON g.tenant_id = l.tenant_id AND g.id = l.group_message_id
	  WHERE l.tenant_id = $1 AND l.topic_thread_id = $2 AND l.relation IN ('primary','secondary','supporting')
	) x WHERE text <> ''`

func scanRows(rows pgx.Rows) ([]ports.ContextRow, error) {
	defer rows.Close()
	var out []ports.ContextRow
	for rows.Next() {
		var c ports.ContextRow
		var kind, role, rel string
		if err := rows.Scan(&c.ID, &kind, &role, &c.ParticipantKey, &c.Text, &c.At, &rel, &c.FromMedia); err != nil {
			return nil, err
		}
		c.Kind, c.Role, c.Relation = ports.MessageKind(kind), domain.Role(role), domain.MessageRelation(rel)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *PostgresContextRepository) RecentTopicMessages(ctx context.Context, tenantID, topicID uuid.UUID, limit int) ([]ports.ContextRow, error) {
	rows, err := r.q(ctx).Query(ctx, topicRowsSQL+` ORDER BY at DESC, id DESC LIMIT $3`, tenantID, topicID, limit)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

func (r *PostgresContextRepository) RelevantTopicMessages(ctx context.Context, tenantID, topicID uuid.UUID, exclude []uuid.UUID, words []string, limit int) ([]ports.ContextRow, error) {
	if len(words) == 0 {
		return nil, nil
	}
	patterns := make([]string, 0, len(words))
	for _, w := range words {
		patterns = append(patterns, "%"+escapeLike(w)+"%")
	}
	rows, err := r.q(ctx).Query(ctx, topicRowsSQL+` AND NOT (id = ANY($3::uuid[])) AND text ILIKE ANY($4::text[]) ORDER BY at DESC, id DESC LIMIT $5`,
		tenantID, topicID, exclude, patterns, limit)
	if err != nil {
		return nil, err
	}
	return scanRows(rows)
}

func escapeLike(s string) string {
	out := make([]rune, 0, len(s))
	for _, c := range s {
		if c == '%' || c == '_' || c == '\\' {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return string(out)
}

func (r *PostgresContextRepository) TopicStats(ctx context.Context, tenantID, topicID uuid.UUID) (ports.TopicStats, error) {
	var st ports.TopicStats
	var last *time.Time
	err := r.q(ctx).QueryRow(ctx, `
		SELECT count(*), count(DISTINCT pkey) FILTER (WHERE pkey <> ''), max(at) FROM (
		  SELECT COALESCE(m.sender_channel_participant_id::text, '') AS pkey, m.created_at AS at
		  FROM message_topic_links l JOIN messages m ON m.tenant_id = l.tenant_id AND m.id = l.message_id
		  WHERE l.tenant_id = $1 AND l.topic_thread_id = $2 AND l.relation IN ('primary','secondary','supporting')
		  UNION ALL
		  SELECT COALESCE(g.sender_channel_participant_id::text, ''), g.sent_at
		  FROM group_message_topic_links l JOIN wa_group_messages g ON g.tenant_id = l.tenant_id AND g.id = l.group_message_id
		  WHERE l.tenant_id = $1 AND l.topic_thread_id = $2 AND l.relation IN ('primary','secondary','supporting')
		) x`, tenantID, topicID).Scan(&st.Messages, &st.Participants, &last)
	st.LastAt = last
	return st, err
}

func (r *PostgresContextRepository) TopicEntities(ctx context.Context, tenantID, topicID uuid.UUID) ([]domain.EntityContext, error) {
	rows, err := r.q(ctx).Query(ctx, `SELECT entity_type, canonical_key, source FROM topic_entities WHERE tenant_id=$1 AND topic_thread_id=$2 ORDER BY entity_type, canonical_key LIMIT 30`, tenantID, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.EntityContext
	for rows.Next() {
		var typ, key, source string
		if err := rows.Scan(&typ, &key, &source); err != nil {
			return nil, err
		}
		out = append(out, domain.EntityContext{Type: domain.EntityType(typ), Key: key, Truth: truthOfEntitySource(source)})
	}
	return out, rows.Err()
}

// A rule or a model read the entity from customer text, so it is an inference; a person or the customer confirming it
// raises it; only the system's own records are verified.
func truthOfEntitySource(source string) domain.TruthLevel {
	switch source {
	case "agent":
		return domain.TruthAgentConfirmed
	case "customer":
		return domain.TruthCustomerConfirmed
	case "system":
		return domain.TruthSystemVerified
	}
	return domain.TruthAIInferred
}

func (r *PostgresContextRepository) TopicTickets(ctx context.Context, tenantID, topicID uuid.UUID) ([]domain.TicketContext, error) {
	rows, err := r.q(ctx).Query(ctx, `
		SELECT k.relation, tk.status, tk.priority FROM topic_ticket_links k
		JOIN tickets tk ON tk.tenant_id = k.tenant_id AND tk.id = k.ticket_id
		WHERE k.tenant_id=$1 AND k.topic_thread_id=$2 ORDER BY (k.relation='primary') DESC, k.created_at LIMIT 10`, tenantID, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TicketContext
	for rows.Next() {
		var rel string
		var t domain.TicketContext
		if err := rows.Scan(&rel, &t.Status, &t.Priority); err != nil {
			return nil, err
		}
		t.Relation = domain.TicketRelation(rel)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *PostgresContextRepository) TopicMedia(ctx context.Context, tenantID, topicID uuid.UUID, limit int) ([]domain.MediaContext, error) {
	rows, err := r.q(ctx).Query(ctx, `
		SELECT ma.message_id, ma.kind, ma.body FROM message_topic_links l
		JOIN message_media_analysis ma ON ma.tenant_id = l.tenant_id AND ma.message_id = l.message_id AND ma.status = 'done'
		JOIN messages m ON m.tenant_id = l.tenant_id AND m.id = l.message_id
		WHERE l.tenant_id=$1 AND l.topic_thread_id=$2 AND l.relation IN ('primary','secondary','supporting') AND m.body <> ''
		ORDER BY ma.created_at DESC LIMIT $3`, tenantID, topicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MediaContext
	for rows.Next() {
		var m domain.MediaContext
		if err := rows.Scan(&m.MessageID, &m.Kind, &m.Text); err != nil {
			return nil, err
		}
		m.Truth = domain.TruthAIInferred
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *PostgresContextRepository) TopicMessage(ctx context.Context, tenantID, topicID uuid.UUID, ref ports.MessageRef) (*ports.ContextRow, error) {
	rows, err := r.q(ctx).Query(ctx, topicRowsSQL+` AND id = $3 LIMIT 1`, tenantID, topicID, ref.ID)
	if err != nil {
		return nil, err
	}
	got, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if len(got) == 0 {
		return nil, domain.ErrReferenceNotFound
	}
	return &got[0], nil
}
