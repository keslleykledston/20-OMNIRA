package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresRoutingRepository serves the routing use case inside a tenant session (RLS) and filters by tenant itself.
type PostgresRoutingRepository struct{ pool *pgxpool.Pool }

var _ ports.RoutingRepository = (*PostgresRoutingRepository)(nil)

func NewPostgresRoutingRepository(pool *pgxpool.Pool) *PostgresRoutingRepository {
	return &PostgresRoutingRepository{pool: pool}
}

func (r *PostgresRoutingRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

// col is the column of routing_decisions / ambiguity_cases that references the message.
func col(k ports.MessageKind) string {
	if k == ports.KindGroup {
		return "group_message_id"
	}
	return "message_id"
}

func (r *PostgresRoutingRepository) LoadRoutable(ctx context.Context, tenantID uuid.UUID, ref ports.MessageRef) (*ports.RoutableMessage, error) {
	m := &ports.RoutableMessage{Ref: ref}
	var reply *uuid.UUID
	var err error
	if ref.Kind == ports.KindGroup {
		err = r.q(ctx).QueryRow(ctx, `
			SELECT g.group_id, g.sender_channel_participant_id, g.body, NOT g.from_me, g.sent_at, g.reply_to_group_message_id
			FROM wa_group_messages g WHERE g.tenant_id=$1 AND g.id=$2`, tenantID, ref.ID).
			Scan(&m.ContainerID, &m.ParticipantID, &m.Text, &m.Inbound, &m.CreatedAt, &reply)
	} else {
		err = r.q(ctx).QueryRow(ctx, `
			SELECT m.conversation_id, c.contact_id, c.internal_user_id IS NOT NULL, m.sender_channel_participant_id, m.body, m.direction='inbound', m.created_at, m.reply_to_message_id
			FROM messages m JOIN conversations c ON c.tenant_id=m.tenant_id AND c.id=m.conversation_id
			WHERE m.tenant_id=$1 AND m.id=$2`, tenantID, ref.ID).
			Scan(&m.ContainerID, &m.ContactID, &m.Internal, &m.ParticipantID, &m.Text, &m.Inbound, &m.CreatedAt, &reply)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrReferenceNotFound
	}
	if err != nil {
		return nil, err
	}
	if reply != nil {
		m.ReplyTo = &ports.MessageRef{Kind: ref.Kind, ID: *reply}
	}
	return m, nil
}

func (r *PostgresRoutingRepository) ActiveDecision(ctx context.Context, tenantID uuid.UUID, ref ports.MessageRef) (*ports.StoredDecision, error) {
	var d ports.StoredDecision
	var status string
	err := r.q(ctx).QueryRow(ctx, fmt.Sprintf(`
		SELECT id, status, selected_topic_thread_id, applied FROM routing_decisions
		WHERE tenant_id=$1 AND %s=$2 AND applied AND overridden_at IS NULL`, col(ref.Kind)), tenantID, ref.ID).
		Scan(&d.ID, &status, &d.Selected, &d.Applied)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	d.Status = domain.RoutingStatus(status)
	return &d, err
}

func (r *PostgresRoutingRepository) TopicsOfMessage(ctx context.Context, tenantID uuid.UUID, ref ports.MessageRef) ([]uuid.UUID, error) {
	table, c := "message_topic_links", "message_id"
	if ref.Kind == ports.KindGroup {
		table, c = "group_message_topic_links", "group_message_id"
	}
	rows, err := r.q(ctx).Query(ctx, fmt.Sprintf(`
		SELECT l.topic_thread_id FROM %s l JOIN topic_threads t ON t.tenant_id=l.tenant_id AND t.id=l.topic_thread_id
		WHERE l.tenant_id=$1 AND l.%s=$2 AND l.relation IN ('primary','secondary','supporting') AND t.status <> 'archived'
		ORDER BY (l.relation='primary') DESC, l.created_at`, table, c), tenantID, ref.ID)
	if err != nil {
		return nil, err
	}
	return scanUUIDs(rows)
}

func scanUUIDs(rows pgx.Rows) ([]uuid.UUID, error) {
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *PostgresRoutingRepository) EntityTopics(ctx context.Context, tenantID uuid.UUID, ref ports.MessageRef, container uuid.UUID, contact *uuid.UUID, entities []domain.Entity) (map[string][]uuid.UUID, error) {
	out := map[string][]uuid.UUID{}
	// the scope: the topic already lives in this container, or belongs to the same contact
	link, c := "topic_conversation_links", "conversation_id"
	if ref.Kind == ports.KindGroup {
		link, c = "topic_group_links", "group_id"
	}
	scope := fmt.Sprintf(`(EXISTS (SELECT 1 FROM %s sl WHERE sl.tenant_id=t.tenant_id AND sl.topic_thread_id=t.id AND sl.%s=$4)
		        OR ($5::uuid IS NOT NULL AND t.primary_contact_id = $5::uuid))`, link, c)
	for _, e := range entities {
		rows, err := r.q(ctx).Query(ctx, fmt.Sprintf(`
			SELECT topic_thread_id FROM (
			  SELECT e.topic_thread_id, t.last_activity_at FROM topic_entities e
			  JOIN topic_threads t ON t.tenant_id=e.tenant_id AND t.id=e.topic_thread_id
			  WHERE e.tenant_id=$1 AND e.entity_type=$2 AND e.canonical_key=$3 AND t.status='open' AND %[1]s
			  UNION
			  SELECT k.topic_thread_id, t.last_activity_at FROM topic_ticket_links k
			  JOIN tickets tk ON tk.tenant_id=k.tenant_id AND tk.id=k.ticket_id
			  JOIN topic_threads t ON t.tenant_id=k.tenant_id AND t.id=k.topic_thread_id
			  WHERE k.tenant_id=$1 AND $2='ticket' AND tk.external_ticket_id=$3 AND t.status='open' AND %[1]s
			) x ORDER BY last_activity_at DESC LIMIT 5`, scope), tenantID, string(e.Type), e.Key, container, contact)
		if err != nil {
			return nil, err
		}
		ids, err := scanUUIDs(rows)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			out[e.String()] = ids
		}
	}
	return out, nil
}

func (r *PostgresRoutingRepository) ParticipantTopics(ctx context.Context, tenantID uuid.UUID, ref ports.MessageRef, container, participant uuid.UUID, limit int) ([]domain.ParticipantTopic, error) {
	var sql string
	if ref.Kind == ports.KindGroup {
		sql = `SELECT l.topic_thread_id, max(m.sent_at) FROM group_message_topic_links l
		  JOIN wa_group_messages m ON m.tenant_id=l.tenant_id AND m.id=l.group_message_id
		  JOIN topic_threads t ON t.tenant_id=l.tenant_id AND t.id=l.topic_thread_id
		  WHERE l.tenant_id=$1 AND m.group_id=$2 AND m.sender_channel_participant_id=$3 AND m.id <> $4
		    AND l.relation IN ('primary','secondary') AND t.status='open'
		  GROUP BY l.topic_thread_id ORDER BY max(m.sent_at) DESC LIMIT $5`
	} else {
		sql = `SELECT l.topic_thread_id, max(m.created_at) FROM message_topic_links l
		  JOIN messages m ON m.tenant_id=l.tenant_id AND m.id=l.message_id
		  JOIN topic_threads t ON t.tenant_id=l.tenant_id AND t.id=l.topic_thread_id
		  WHERE l.tenant_id=$1 AND m.conversation_id=$2 AND m.sender_channel_participant_id=$3 AND m.id <> $4
		    AND l.relation IN ('primary','secondary') AND t.status='open'
		  GROUP BY l.topic_thread_id ORDER BY max(m.created_at) DESC LIMIT $5`
	}
	rows, err := r.q(ctx).Query(ctx, sql, tenantID, container, participant, ref.ID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ParticipantTopic
	for rows.Next() {
		var p domain.ParticipantTopic
		if err := rows.Scan(&p.TopicID, &p.At); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *PostgresRoutingRepository) Focus(ctx context.Context, tenantID uuid.UUID, ref ports.MessageRef, container uuid.UUID, participant *uuid.UUID) ([]domain.FocusHint, error) {
	column := "conversation_id"
	if ref.Kind == ports.KindGroup {
		column = "group_id"
	}
	rows, err := r.q(ctx).Query(ctx, fmt.Sprintf(`
		SELECT f.topic_thread_id, COALESCE(f.confidence::float8, 1), f.channel_participant_id IS NOT NULL, f.expires_at
		FROM conversation_topic_focus f JOIN topic_threads t ON t.tenant_id=f.tenant_id AND t.id=f.topic_thread_id
		WHERE f.tenant_id=$1 AND f.%s=$2 AND t.status='open' AND (f.expires_at IS NULL OR f.expires_at > now())
		  AND (f.channel_participant_id IS NULL OR f.channel_participant_id = $3)`, column), tenantID, container, participant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.FocusHint
	for rows.Next() {
		var h domain.FocusHint
		if err := rows.Scan(&h.TopicID, &h.Confidence, &h.ParticipantScoped, &h.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (r *PostgresRoutingRepository) OpenTopics(ctx context.Context, tenantID uuid.UUID, ref ports.MessageRef, container uuid.UUID, limit int) ([]domain.TopicBrief, error) {
	link, c := "topic_conversation_links", "conversation_id"
	if ref.Kind == ports.KindGroup {
		link, c = "topic_group_links", "group_id"
	}
	rows, err := r.q(ctx).Query(ctx, fmt.Sprintf(`
		SELECT t.id, t.title, COALESCE(t.intent,''), t.last_activity_at FROM topic_threads t
		JOIN %s l ON l.tenant_id=t.tenant_id AND l.topic_thread_id=t.id
		WHERE t.tenant_id=$1 AND l.%s=$2 AND t.status='open'
		ORDER BY t.last_activity_at DESC LIMIT $3`, link, c), tenantID, container, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.TopicBrief
	for rows.Next() {
		var b domain.TopicBrief
		if err := rows.Scan(&b.ID, &b.Title, &b.Intent, &b.LastActivityAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (r *PostgresRoutingRepository) SaveDecision(ctx context.Context, tenantID uuid.UUID, d ports.DecisionRecord) (uuid.UUID, bool, error) {
	c := col(d.Ref.Kind)
	var messageID, groupID *uuid.UUID
	if d.Ref.Kind == ports.KindGroup {
		groupID = &d.Ref.ID
	} else {
		messageID = &d.Ref.ID
	}
	values := []any{tenantID, messageID, groupID, string(d.Status), d.Selected, string(d.Source), d.Applied, d.Confidence, string(d.Signals),
		d.ModelProvider, d.ModelName, d.PromptVersion, d.InputTokens, d.OutputTokens, d.LatencyMS}
	const insert = `INSERT INTO routing_decisions (tenant_id, message_id, group_message_id, status, selected_topic_thread_id, decision_source, applied,
		confidence, signals, model_provider, model_name, prompt_version, input_tokens, output_tokens, latency_ms)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10,$11,$12,$13,$14,$15)`
	var id uuid.UUID
	if d.Applied {
		err := r.q(ctx).QueryRow(ctx, insert+fmt.Sprintf(` ON CONFLICT (tenant_id, %s) WHERE applied AND overridden_at IS NULL AND %s IS NOT NULL DO NOTHING RETURNING id`, c, c), values...).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) { // an active decision already exists: a replay
			existing, qerr := r.ActiveDecision(ctx, tenantID, d.Ref)
			if qerr != nil || existing == nil {
				return uuid.Nil, false, firstError(qerr, errors.New("routing: active decision vanished"))
			}
			return existing.ID, false, nil
		}
		return id, err == nil, mapError(err)
	}
	err := r.q(ctx).QueryRow(ctx, insert+fmt.Sprintf(` ON CONFLICT (tenant_id, %s, decision_source) WHERE NOT applied AND %s IS NOT NULL DO UPDATE SET
		status=EXCLUDED.status, selected_topic_thread_id=EXCLUDED.selected_topic_thread_id, confidence=EXCLUDED.confidence, signals=EXCLUDED.signals,
		model_provider=EXCLUDED.model_provider, model_name=EXCLUDED.model_name, prompt_version=EXCLUDED.prompt_version,
		input_tokens=EXCLUDED.input_tokens, output_tokens=EXCLUDED.output_tokens, latency_ms=EXCLUDED.latency_ms, created_at=now()
		RETURNING id`, c, c), values...).Scan(&id)
	return id, err == nil, mapError(err)
}

func (r *PostgresRoutingRepository) ProposalExists(ctx context.Context, tenantID uuid.UUID, ref ports.MessageRef, source domain.DecisionSource) (bool, error) {
	var ok bool
	err := r.q(ctx).QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM routing_decisions WHERE tenant_id=$1 AND %s=$2 AND decision_source=$3 AND NOT applied)`, col(ref.Kind)), tenantID, ref.ID, string(source)).Scan(&ok)
	return ok, err
}

func firstError(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func (r *PostgresRoutingRepository) LinkMessage(ctx context.Context, tenantID uuid.UUID, ref ports.MessageRef, topicID uuid.UUID, relation domain.MessageRelation, source domain.DecisionSource, confidence *float64, decisionID *uuid.UUID) error {
	table, c := "message_topic_links", "message_id"
	if ref.Kind == ports.KindGroup {
		table, c = "group_message_topic_links", "group_message_id"
	}
	rankNew := fmt.Sprintf(decisionRankSQL, "EXCLUDED.decision_source")
	rankOld := fmt.Sprintf(decisionRankSQL, table+".decision_source")
	_, err := r.q(ctx).Exec(ctx, fmt.Sprintf(`
		INSERT INTO %[1]s (tenant_id, %[2]s, topic_thread_id, relation, confidence, decision_source, routing_decision_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (tenant_id, %[2]s, topic_thread_id) DO UPDATE SET
		  relation = EXCLUDED.relation, confidence = EXCLUDED.confidence, decision_source = EXCLUDED.decision_source,
		  routing_decision_id = COALESCE(EXCLUDED.routing_decision_id, %[1]s.routing_decision_id)
		WHERE %[3]s >= %[4]s`, table, c, rankNew, rankOld),
		tenantID, ref.ID, topicID, string(relation), confidence, string(source), decisionID)
	return mapError(err)
}

func (r *PostgresRoutingRepository) LinkContainer(ctx context.Context, tenantID uuid.UUID, ref ports.MessageRef, container, topicID uuid.UUID) error {
	var err error
	if ref.Kind == ports.KindGroup {
		_, err = r.q(ctx).Exec(ctx, `
			INSERT INTO topic_group_links (tenant_id, topic_thread_id, group_id) VALUES ($1,$2,$3)
			ON CONFLICT (tenant_id, topic_thread_id, group_id) DO UPDATE SET last_activity_at = now()`, tenantID, topicID, container)
	} else {
		_, err = r.q(ctx).Exec(ctx, `
			INSERT INTO topic_conversation_links (tenant_id, topic_thread_id, conversation_id, relation) VALUES ($1,$2,$3,'active')
			ON CONFLICT (tenant_id, topic_thread_id, conversation_id) DO UPDATE SET last_activity_at = now()`, tenantID, topicID, container)
	}
	return mapError(err)
}

func (r *PostgresRoutingRepository) UpsertEntities(ctx context.Context, tenantID, topicID uuid.UUID, entities []domain.Entity, sourceMessage *uuid.UUID, source string) error {
	for _, e := range entities {
		display := e.Display
		if len([]rune(display)) > 200 {
			display = string([]rune(display)[:200])
		}
		if _, err := r.q(ctx).Exec(ctx, `
			INSERT INTO topic_entities (tenant_id, topic_thread_id, entity_type, canonical_key, display_value, source_message_id, source)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (tenant_id, topic_thread_id, entity_type, canonical_key) DO UPDATE SET updated_at = now()`,
			tenantID, topicID, string(e.Type), e.Key, display, sourceMessage, source); err != nil {
			return mapError(err)
		}
	}
	return nil
}

func (r *PostgresRoutingRepository) SetFocus(ctx context.Context, tenantID uuid.UUID, ref ports.MessageRef, container uuid.UUID, participant *uuid.UUID, topicID uuid.UUID, source string, confidence float64, expires time.Time) error {
	var conv, grp *uuid.UUID
	if ref.Kind == ports.KindGroup {
		grp = &container
	} else {
		conv = &container
	}
	_, err := r.q(ctx).Exec(ctx, `
		INSERT INTO conversation_topic_focus (tenant_id, conversation_id, group_id, channel_participant_id, topic_thread_id, source, confidence, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (tenant_id, COALESCE(conversation_id, group_id), COALESCE(channel_participant_id, '00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE SET
		  topic_thread_id = EXCLUDED.topic_thread_id, source = EXCLUDED.source, confidence = EXCLUDED.confidence, expires_at = EXCLUDED.expires_at, updated_at = now()
		WHERE conversation_topic_focus.source = 'router' OR EXCLUDED.source <> 'router'
		   OR (conversation_topic_focus.expires_at IS NOT NULL AND conversation_topic_focus.expires_at < now())`,
		tenantID, conv, grp, participant, topicID, source, confidence, expires)
	return mapError(err)
}

func (r *PostgresRoutingRepository) TouchTopic(ctx context.Context, tenantID, topicID uuid.UUID) error {
	_, err := r.q(ctx).Exec(ctx, `UPDATE topic_threads SET last_activity_at = now(), updated_at = now() WHERE tenant_id=$1 AND id=$2`, tenantID, topicID)
	return err
}

func (r *PostgresRoutingRepository) CreateAmbiguity(ctx context.Context, tenantID uuid.UUID, ref ports.MessageRef, candidates json.RawMessage) (uuid.UUID, error) {
	c := col(ref.Kind)
	var messageID, groupID *uuid.UUID
	if ref.Kind == ports.KindGroup {
		groupID = &ref.ID
	} else {
		messageID = &ref.ID
	}
	var id uuid.UUID
	err := r.q(ctx).QueryRow(ctx, fmt.Sprintf(`
		INSERT INTO ambiguity_cases (tenant_id, message_id, group_message_id, candidate_topics) VALUES ($1,$2,$3,$4::jsonb)
		ON CONFLICT (tenant_id, %[1]s) WHERE status = 'open' AND %[1]s IS NOT NULL DO UPDATE SET candidate_topics = EXCLUDED.candidate_topics
		RETURNING id`, c), tenantID, messageID, groupID, string(candidates)).Scan(&id)
	return id, mapError(err)
}

const ambiguitySelect = `
	SELECT a.id, a.message_id, a.group_message_id, a.status, a.candidate_topics, a.created_at, a.resolved_topic_thread_id,
	       COALESCE(m.conversation_id, gm.group_id), c.assigned_to_user_id
	FROM ambiguity_cases a
	LEFT JOIN messages m ON m.tenant_id=a.tenant_id AND m.id=a.message_id
	LEFT JOIN conversations c ON c.tenant_id=m.tenant_id AND c.id=m.conversation_id
	LEFT JOIN wa_group_messages gm ON gm.tenant_id=a.tenant_id AND gm.id=a.group_message_id`

func scanAmbiguity(row pgx.Row) (*ports.Ambiguity, error) {
	var a ports.Ambiguity
	var msg, grp *uuid.UUID
	if err := row.Scan(&a.ID, &msg, &grp, &a.Status, &a.Candidates, &a.CreatedAt, &a.ResolvedTo, &a.Container, &a.AssignedTo); err != nil {
		return nil, err
	}
	if grp != nil {
		a.Ref = ports.MessageRef{Kind: ports.KindGroup, ID: *grp}
	} else if msg != nil {
		a.Ref = ports.MessageRef{Kind: ports.KindConversation, ID: *msg}
	}
	return &a, nil
}

func (r *PostgresRoutingRepository) GetAmbiguity(ctx context.Context, tenantID, id uuid.UUID) (*ports.Ambiguity, error) {
	a, err := scanAmbiguity(r.q(ctx).QueryRow(ctx, ambiguitySelect+` WHERE a.tenant_id=$1 AND a.id=$2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrReferenceNotFound
	}
	return a, err
}

func (r *PostgresRoutingRepository) ListOpenAmbiguities(ctx context.Context, tenantID uuid.UUID, kind ports.MessageKind, container uuid.UUID) ([]ports.Ambiguity, error) {
	where := ` WHERE a.tenant_id=$1 AND a.status='open' AND m.conversation_id=$2 ORDER BY a.created_at`
	if kind == ports.KindGroup {
		where = ` WHERE a.tenant_id=$1 AND a.status='open' AND gm.group_id=$2 ORDER BY a.created_at`
	}
	rows, err := r.q(ctx).Query(ctx, ambiguitySelect+where, tenantID, container)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ports.Ambiguity{}
	for rows.Next() {
		a, err := scanAmbiguity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (r *PostgresRoutingRepository) ResolveAmbiguity(ctx context.Context, tenantID, id, topicID uuid.UUID, source string, byUser *uuid.UUID) (bool, error) {
	tag, err := r.q(ctx).Exec(ctx, `
		UPDATE ambiguity_cases SET status='resolved', resolved_topic_thread_id=$3, resolution_source=$4, resolved_by_user_id=$5, resolved_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status='open'`, tenantID, id, topicID, source, byUser)
	if err != nil {
		return false, mapError(err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *PostgresRoutingRepository) MarkDecisionOverridden(ctx context.Context, tenantID uuid.UUID, ref ports.MessageRef, byUser *uuid.UUID) error {
	_, err := r.q(ctx).Exec(ctx, fmt.Sprintf(`
		UPDATE routing_decisions SET overridden_at = now(), overridden_by_user_id = $3
		WHERE tenant_id=$1 AND %s=$2 AND applied AND overridden_at IS NULL`, col(ref.Kind)), tenantID, ref.ID, byUser)
	return err
}
