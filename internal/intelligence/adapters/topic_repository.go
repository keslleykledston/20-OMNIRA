package adapters

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
)

// PostgresTopicRepository runs inside the request's tenant session, so RLS decides what exists; every query also
// filters by tenant_id explicitly.
type PostgresTopicRepository struct{ pool *pgxpool.Pool }

var _ ports.TopicRepository = (*PostgresTopicRepository)(nil)

func NewPostgresTopicRepository(pool *pgxpool.Pool) *PostgresTopicRepository {
	return &PostgresTopicRepository{pool: pool}
}

func (r *PostgresTopicRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

const topicColumns = `t.id, t.tenant_id, t.primary_contact_id, t.origin_conversation_id, t.title, t.intent, t.category, t.status,
	t.privacy_policy, t.source, t.routing_confidence::float8, t.legacy_unsegmented, t.last_activity_at, t.created_by_user_id,
	t.created_at, t.updated_at, t.resolved_at, t.merged_into_topic_id, t.split_from_topic_id`

func scanTopic(row pgx.Row, extra ...any) (*domain.TopicThread, error) {
	var t domain.TopicThread
	var status, privacy, source string
	dest := append([]any{&t.ID, &t.TenantID, &t.PrimaryContactID, &t.OriginConversationID, &t.Title, &t.Intent, &t.Category, &status,
		&privacy, &source, &t.RoutingConfidence, &t.LegacyUnsegmented, &t.LastActivityAt, &t.CreatedByUserID,
		&t.CreatedAt, &t.UpdatedAt, &t.ResolvedAt, &t.MergedIntoTopicID, &t.SplitFromTopicID}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	t.Status, t.PrivacyPolicy, t.Source = domain.TopicStatus(status), domain.PrivacyPolicy(privacy), domain.TopicSource(source)
	return &t, nil
}

// mapError turns foreign-key and uniqueness violations into domain errors without leaking SQL details.
func mapError(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23503":
			return domain.ErrReferenceNotFound
		case "23505":
			if pg.ConstraintName == "topic_ticket_links_one_primary_uq" {
				return domain.ErrPrimaryTicketTaken
			}
		}
	}
	return err
}

func (r *PostgresTopicRepository) CreateTopic(ctx context.Context, t *domain.TopicThread) error {
	q := r.q(ctx)
	if _, err := q.Exec(ctx, `
		INSERT INTO topic_threads (id, tenant_id, primary_contact_id, origin_conversation_id, title, intent, category, status,
		       privacy_policy, source, routing_confidence, legacy_unsegmented, last_activity_at, created_by_user_id, created_at, updated_at, resolved_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		t.ID, t.TenantID, t.PrimaryContactID, t.OriginConversationID, t.Title, t.Intent, t.Category, string(t.Status),
		string(t.PrivacyPolicy), string(t.Source), t.RoutingConfidence, t.LegacyUnsegmented, t.LastActivityAt, t.CreatedByUserID,
		t.CreatedAt, t.UpdatedAt, t.ResolvedAt); err != nil {
		return mapError(err)
	}
	if t.OriginConversationID != nil {
		if _, err := q.Exec(ctx, `
			INSERT INTO topic_conversation_links (tenant_id, topic_thread_id, conversation_id, relation)
			VALUES ($1,$2,$3,'origin') ON CONFLICT DO NOTHING`, t.TenantID, t.ID, *t.OriginConversationID); err != nil {
			return mapError(err)
		}
	}
	return nil
}

func (r *PostgresTopicRepository) GetTopic(ctx context.Context, tenantID, id uuid.UUID) (*domain.TopicThread, error) {
	t, err := scanTopic(r.q(ctx).QueryRow(ctx, `SELECT `+topicColumns+` FROM topic_threads t WHERE t.tenant_id=$1 AND t.id=$2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrTopicNotFound
	}
	return t, err
}

func (r *PostgresTopicRepository) UpdateTopic(ctx context.Context, t *domain.TopicThread) error {
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE topic_threads SET title=$3, intent=$4, category=$5, status=$6, privacy_policy=$7, resolved_at=$8,
		       last_activity_at=$9, updated_at=$10
		WHERE tenant_id=$1 AND id=$2`,
		t.TenantID, t.ID, t.Title, t.Intent, t.Category, string(t.Status), string(t.PrivacyPolicy), t.ResolvedAt, t.LastActivityAt, t.UpdatedAt)
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() != 1 {
		return domain.ErrTopicNotFound
	}
	return nil
}

const listCounts = `,
	(SELECT count(*) FROM message_topic_links l WHERE l.tenant_id=t.tenant_id AND l.topic_thread_id=t.id),
	(SELECT count(*) FROM topic_ticket_links k WHERE k.tenant_id=t.tenant_id AND k.topic_thread_id=t.id),
	(SELECT max(m.created_at) FROM message_topic_links l JOIN messages m ON m.tenant_id=l.tenant_id AND m.id=l.message_id
	   WHERE l.tenant_id=t.tenant_id AND l.topic_thread_id=t.id)`

func (r *PostgresTopicRepository) scanList(rows pgx.Rows) ([]ports.TopicListItem, error) {
	defer rows.Close()
	out := []ports.TopicListItem{}
	for rows.Next() {
		var item ports.TopicListItem
		t, err := scanTopic(rows, &item.MessageCount, &item.TicketCount, &item.LastMessageAt)
		if err != nil {
			return nil, err
		}
		item.Topic = *t
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *PostgresTopicRepository) ListConversationTopics(ctx context.Context, tenantID, conversationID uuid.UUID) ([]ports.TopicListItem, error) {
	rows, err := r.q(ctx).Query(ctx, `
		SELECT `+topicColumns+listCounts+`
		FROM topic_threads t
		JOIN topic_conversation_links c ON c.tenant_id=t.tenant_id AND c.topic_thread_id=t.id
		WHERE t.tenant_id=$1 AND c.conversation_id=$2 AND t.status <> 'archived'
		ORDER BY (t.status='open') DESC, t.last_activity_at DESC, t.id`, tenantID, conversationID)
	if err != nil {
		return nil, err
	}
	return r.scanList(rows)
}

func (r *PostgresTopicRepository) ListContactTopics(ctx context.Context, tenantID, contactID uuid.UUID, status *domain.TopicStatus, limit int) ([]ports.TopicListItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var st *string
	if status != nil {
		s := string(*status)
		st = &s
	}
	rows, err := r.q(ctx).Query(ctx, `
		SELECT `+topicColumns+listCounts+`
		FROM topic_threads t
		WHERE t.tenant_id=$1 AND t.primary_contact_id=$2 AND t.status <> 'archived' AND ($3::text IS NULL OR t.status=$3)
		ORDER BY (t.status='open') DESC, t.last_activity_at DESC, t.id LIMIT $4`, tenantID, contactID, st, limit)
	if err != nil {
		return nil, err
	}
	return r.scanList(rows)
}

// decisionRank orders decision sources by authority: a human or the customer beats an explicit signal, which beats
// a heuristic, which beats a model, which beats history. The same expression is used in SQL below.
const decisionRankSQL = `CASE %s
	WHEN 'agent' THEN 6 WHEN 'customer' THEN 6
	WHEN 'explicit' THEN 5 WHEN 'handoff' THEN 5 WHEN 'reply' THEN 5
	WHEN 'entity' THEN 4 WHEN 'rule' THEN 3 WHEN 'ai' THEN 2 ELSE 1 END`

func (r *PostgresTopicRepository) LinkMessage(ctx context.Context, l *domain.MessageTopicLink) error {
	q := r.q(ctx)
	rankNew := fmt.Sprintf(decisionRankSQL, "EXCLUDED.decision_source")
	rankOld := fmt.Sprintf(decisionRankSQL, "message_topic_links.decision_source")
	if _, err := q.Exec(ctx, `
		INSERT INTO message_topic_links (tenant_id, message_id, topic_thread_id, relation, confidence, decision_source, routing_decision_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (tenant_id, message_id, topic_thread_id) DO UPDATE SET
		  relation = EXCLUDED.relation, confidence = EXCLUDED.confidence, decision_source = EXCLUDED.decision_source,
		  routing_decision_id = COALESCE(EXCLUDED.routing_decision_id, message_topic_links.routing_decision_id)
		WHERE `+rankNew+` >= `+rankOld,
		l.TenantID, l.MessageID, l.TopicThreadID, string(l.Relation), l.Confidence, string(l.DecisionSource), l.RoutingDecisionID); err != nil {
		return mapError(err)
	}
	// A topic that owns a message also spans that message's conversation.
	if _, err := q.Exec(ctx, `
		INSERT INTO topic_conversation_links (tenant_id, topic_thread_id, conversation_id, relation)
		SELECT m.tenant_id, $3, m.conversation_id, 'active' FROM messages m WHERE m.tenant_id=$1 AND m.id=$2
		ON CONFLICT (tenant_id, topic_thread_id, conversation_id) DO UPDATE SET last_activity_at = now()`,
		l.TenantID, l.MessageID, l.TopicThreadID); err != nil {
		return mapError(err)
	}
	_, err := q.Exec(ctx, `UPDATE topic_threads SET last_activity_at = now() WHERE tenant_id=$1 AND id=$2`, l.TenantID, l.TopicThreadID)
	return mapError(err)
}

func (r *PostgresTopicRepository) UnlinkMessage(ctx context.Context, tenantID, messageID, topicID uuid.UUID) (bool, error) {
	tag, err := r.q(ctx).Exec(ctx, `DELETE FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2 AND topic_thread_id=$3`, tenantID, messageID, topicID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (r *PostgresTopicRepository) LinkConversation(ctx context.Context, l *domain.TopicConversationLink) error {
	_, err := r.q(ctx).Exec(ctx, `
		INSERT INTO topic_conversation_links (tenant_id, topic_thread_id, conversation_id, relation)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (tenant_id, topic_thread_id, conversation_id) DO UPDATE SET last_activity_at = now()`,
		l.TenantID, l.TopicThreadID, l.ConversationID, string(l.Relation))
	return mapError(err)
}

func (r *PostgresTopicRepository) LinkTicket(ctx context.Context, l *domain.TopicTicketLink) error {
	_, err := r.q(ctx).Exec(ctx, `
		INSERT INTO topic_ticket_links (tenant_id, topic_thread_id, ticket_id, relation, created_by, created_by_user_id)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tenant_id, topic_thread_id, ticket_id) DO NOTHING`,
		l.TenantID, l.TopicThreadID, l.TicketID, string(l.Relation), string(l.CreatedBy), l.CreatedByUserID)
	return mapError(err)
}

func (r *PostgresTopicRepository) ListTopicMessages(ctx context.Context, tenantID, topicID uuid.UUID, cursor *pagination.Cursor, limit int) ([]ports.TopicMessage, bool, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args := []any{tenantID, topicID, limit + 1}
	where := ""
	if cursor != nil {
		where = " AND (m.created_at, m.id) < ($4, $5)"
		cid, err := uuid.Parse(cursor.ID)
		if err != nil {
			return nil, false, fmt.Errorf("invalid cursor")
		}
		ts := cursor.Timestamp
		args = append(args, ts, cid)
	}
	rows, err := r.q(ctx).Query(ctx, `
		SELECT m.id, m.conversation_id, m.direction, m.message_type, m.body, m.mime_type, m.status, m.created_at,
		       l.relation, l.confidence::float8, l.decision_source
		FROM message_topic_links l
		JOIN messages m ON m.tenant_id=l.tenant_id AND m.id=l.message_id
		WHERE l.tenant_id=$1 AND l.topic_thread_id=$2`+where+`
		ORDER BY m.created_at DESC, m.id DESC LIMIT $3`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := []ports.TopicMessage{}
	for rows.Next() {
		var m ports.TopicMessage
		var rel, src string
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Direction, &m.MessageType, &m.Body, &m.MimeType, &m.Status, &m.CreatedAt, &rel, &m.Confidence, &src); err != nil {
			return nil, false, err
		}
		m.Relation, m.DecisionSource = domain.MessageRelation(rel), domain.DecisionSource(src)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

func (r *PostgresTopicRepository) ListTopicTickets(ctx context.Context, tenantID, topicID uuid.UUID) ([]ports.TopicTicket, error) {
	rows, err := r.q(ctx).Query(ctx, `
		SELECT tk.id, tk.status, tk.priority, tk.subject, tk.provider, tk.external_ticket_id, k.relation, k.created_at
		FROM topic_ticket_links k
		JOIN tickets tk ON tk.tenant_id=k.tenant_id AND tk.id=k.ticket_id
		WHERE k.tenant_id=$1 AND k.topic_thread_id=$2
		ORDER BY (k.relation='primary') DESC, k.created_at, tk.id`, tenantID, topicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ports.TopicTicket{}
	for rows.Next() {
		var t ports.TopicTicket
		var rel string
		if err := rows.Scan(&t.ID, &t.Status, &t.Priority, &t.Subject, &t.Provider, &t.ExternalTicketID, &rel, &t.LinkedAt); err != nil {
			return nil, err
		}
		t.Relation = domain.TicketRelation(rel)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r *PostgresTopicRepository) ConversationInfo(ctx context.Context, tenantID, conversationID uuid.UUID) (ports.ConversationInfo, error) {
	var info ports.ConversationInfo
	err := r.q(ctx).QueryRow(ctx, `SELECT COALESCE(contact_id, '00000000-0000-0000-0000-000000000000'::uuid), assigned_to_user_id FROM conversations WHERE tenant_id=$1 AND id=$2`,
		tenantID, conversationID).Scan(&info.ContactID, &info.AssignedTo)
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ConversationInfo{}, nil
	}
	info.Exists = err == nil
	return info, err
}

func (r *PostgresTopicRepository) ActorOperatesTopic(ctx context.Context, tenantID, topicID, userID uuid.UUID) (bool, error) {
	var ok bool
	err := r.q(ctx).QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM topic_conversation_links c
		  JOIN conversations cv ON cv.tenant_id=c.tenant_id AND cv.id=c.conversation_id
		  WHERE c.tenant_id=$1 AND c.topic_thread_id=$2 AND cv.assigned_to_user_id=$3)`, tenantID, topicID, userID).Scan(&ok)
	return ok, err
}

func (r *PostgresTopicRepository) MessageKindInTopic(ctx context.Context, tenantID, topicID, messageID uuid.UUID) (ports.MessageKind, error) {
	var kind string
	err := r.q(ctx).QueryRow(ctx, `
		SELECT 'conversation' FROM message_topic_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND message_id=$3
		UNION ALL
		SELECT 'group' FROM group_message_topic_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND group_message_id=$3 LIMIT 1`, tenantID, topicID, messageID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrReferenceNotFound
	}
	return ports.MessageKind(kind), err
}
