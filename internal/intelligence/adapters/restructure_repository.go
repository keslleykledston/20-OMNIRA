package adapters

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

type PostgresRestructureRepository struct{ pool *pgxpool.Pool }

var _ ports.RestructureRepository = (*PostgresRestructureRepository)(nil)

func NewPostgresRestructureRepository(pool *pgxpool.Pool) *PostgresRestructureRepository {
	return &PostgresRestructureRepository{pool: pool}
}

func (r *PostgresRestructureRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

// lockOpen locks the given topics in id order (so two merges in opposite directions cannot deadlock) and requires them open.
func (r *PostgresRestructureRepository) lockOpen(ctx context.Context, tenantID uuid.UUID, ids ...uuid.UUID) error {
	rows, err := r.q(ctx).Query(ctx, `SELECT id, status FROM topic_threads WHERE tenant_id=$1 AND id = ANY($2::uuid[]) ORDER BY id FOR UPDATE`, tenantID, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id uuid.UUID
		var status string
		if err := rows.Scan(&id, &status); err != nil {
			return err
		}
		if status != "open" {
			return fmt.Errorf("%w: topic is %s", domain.ErrInvalidTransition, status)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n != len(ids) {
		return domain.ErrTopicNotFound
	}
	return nil
}

func (r *PostgresRestructureRepository) Merge(ctx context.Context, tenantID, sourceID, targetID uuid.UUID, by *uuid.UUID) (ports.RestructureResult, error) {
	var res ports.RestructureResult
	if sourceID == targetID {
		return res, fmt.Errorf("%w: a topic cannot be merged into itself", domain.ErrInvalidTransition)
	}
	if err := r.lockOpen(ctx, tenantID, sourceID, targetID); err != nil {
		return res, err
	}
	exec := func(sql string, args ...any) (int, error) {
		tag, err := r.q(ctx).Exec(ctx, sql, args...)
		return int(tag.RowsAffected()), mapError(err)
	}
	var err error
	// messages: copied, so the archived source still shows its own history
	if res.Messages, err = exec(`
		INSERT INTO message_topic_links (tenant_id, message_id, topic_thread_id, relation, confidence, decision_source)
		SELECT tenant_id, message_id, $3, relation, confidence, 'agent' FROM message_topic_links WHERE tenant_id=$1 AND topic_thread_id=$2
		ON CONFLICT (tenant_id, message_id, topic_thread_id) DO NOTHING`, tenantID, sourceID, targetID); err != nil {
		return res, err
	}
	if res.GroupMsgs, err = exec(`
		INSERT INTO group_message_topic_links (tenant_id, group_message_id, topic_thread_id, relation, confidence, decision_source)
		SELECT tenant_id, group_message_id, $3, relation, confidence, 'agent' FROM group_message_topic_links WHERE tenant_id=$1 AND topic_thread_id=$2
		ON CONFLICT (tenant_id, group_message_id, topic_thread_id) DO NOTHING`, tenantID, sourceID, targetID); err != nil {
		return res, err
	}
	for _, sql := range []string{
		`INSERT INTO topic_conversation_links (tenant_id, topic_thread_id, conversation_id, relation, first_activity_at, last_activity_at)
		 SELECT tenant_id, $3, conversation_id, 'related', first_activity_at, last_activity_at FROM topic_conversation_links WHERE tenant_id=$1 AND topic_thread_id=$2
		 ON CONFLICT (tenant_id, topic_thread_id, conversation_id) DO UPDATE SET
		   first_activity_at = LEAST(topic_conversation_links.first_activity_at, EXCLUDED.first_activity_at),
		   last_activity_at = GREATEST(topic_conversation_links.last_activity_at, EXCLUDED.last_activity_at)`,
		`INSERT INTO topic_group_links (tenant_id, topic_thread_id, group_id, first_activity_at, last_activity_at)
		 SELECT tenant_id, $3, group_id, first_activity_at, last_activity_at FROM topic_group_links WHERE tenant_id=$1 AND topic_thread_id=$2
		 ON CONFLICT (tenant_id, topic_thread_id, group_id) DO UPDATE SET
		   first_activity_at = LEAST(topic_group_links.first_activity_at, EXCLUDED.first_activity_at),
		   last_activity_at = GREATEST(topic_group_links.last_activity_at, EXCLUDED.last_activity_at)`,
	} {
		if _, err = exec(sql, tenantID, sourceID, targetID); err != nil {
			return res, err
		}
	}
	if res.Entities, err = exec(`
		INSERT INTO topic_entities (tenant_id, topic_thread_id, entity_type, canonical_key, display_value, source_message_id, confidence, source)
		SELECT tenant_id, $3, entity_type, canonical_key, display_value, source_message_id, confidence, source FROM topic_entities WHERE tenant_id=$1 AND topic_thread_id=$2
		ON CONFLICT (tenant_id, topic_thread_id, entity_type, canonical_key) DO NOTHING`, tenantID, sourceID, targetID); err != nil {
		return res, err
	}
	// tickets: the source's PRIMARY ticket becomes 'merged' on the target (never a second primary); the others keep their relation
	if res.Tickets, err = exec(`
		INSERT INTO topic_ticket_links (tenant_id, topic_thread_id, ticket_id, relation, created_by, created_by_user_id)
		SELECT tenant_id, $3, ticket_id, CASE WHEN relation = 'primary' THEN 'merged' ELSE relation END, 'agent', $4 FROM topic_ticket_links WHERE tenant_id=$1 AND topic_thread_id=$2
		ON CONFLICT (tenant_id, topic_thread_id, ticket_id) DO NOTHING`, tenantID, sourceID, targetID, by); err != nil {
		return res, err
	}
	// hints that pointed at the source now point at nothing (they are hints, never authority); open ambiguities keep their candidates
	if _, err = exec(`DELETE FROM conversation_topic_focus WHERE tenant_id=$1 AND topic_thread_id=$2`, tenantID, sourceID); err != nil {
		return res, err
	}
	// pending handoffs of the source can no longer be redeemed into an archived topic: revoke them
	if _, err = exec(`UPDATE topic_handoffs SET status='revoked', revoked_at=now() WHERE tenant_id=$1 AND topic_thread_id=$2 AND status='pending'`, tenantID, sourceID); err != nil {
		return res, err
	}
	if _, err = exec(`UPDATE topic_threads SET status='archived', merged_into_topic_id=$3, merged_at=now(), merged_by_user_id=$4, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, sourceID, targetID, by); err != nil {
		return res, err
	}
	if _, err = exec(`UPDATE topic_threads SET last_activity_at = GREATEST(last_activity_at, (SELECT last_activity_at FROM topic_threads WHERE tenant_id=$1 AND id=$2)), updated_at=now() WHERE tenant_id=$1 AND id=$3`, tenantID, sourceID, targetID); err != nil {
		return res, err
	}
	res.SourceStatus = domain.TopicArchived
	return res, nil
}

func linkTable(k ports.MessageKind) (table, col string) {
	if k == ports.KindGroup {
		return "group_message_topic_links", "group_message_id"
	}
	return "message_topic_links", "message_id"
}

func (r *PostgresRestructureRepository) Split(ctx context.Context, tenantID, sourceID uuid.UUID, nt *domain.TopicThread, messages []ports.MessageRef, by *uuid.UUID) (ports.RestructureResult, error) {
	var res ports.RestructureResult
	if len(messages) == 0 {
		return res, fmt.Errorf("%w: choose at least one message", domain.ErrInvalidTopic)
	}
	if err := r.lockOpen(ctx, tenantID, sourceID); err != nil {
		return res, err
	}
	// every message must belong to the source right now, and the source must keep at least one
	var total int
	if err := r.q(ctx).QueryRow(ctx, `
		SELECT (SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND topic_thread_id=$2)
		     + (SELECT count(*) FROM group_message_topic_links WHERE tenant_id=$1 AND topic_thread_id=$2)`, tenantID, sourceID).Scan(&total); err != nil {
		return res, err
	}
	seen := map[ports.MessageRef]bool{}
	for _, m := range messages {
		if seen[m] {
			return res, fmt.Errorf("%w: duplicated message", domain.ErrInvalidTopic)
		}
		seen[m] = true
		table, col := linkTable(m.Kind)
		var ok bool
		if err := r.q(ctx).QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE tenant_id=$1 AND topic_thread_id=$2 AND %s=$3)`, table, col), tenantID, sourceID, m.ID).Scan(&ok); err != nil {
			return res, err
		}
		if !ok {
			return res, domain.ErrReferenceNotFound
		}
	}
	if len(messages) >= total {
		return res, fmt.Errorf("%w: the source topic must keep at least one message", domain.ErrInvalidTransition)
	}
	if _, err := r.q(ctx).Exec(ctx, `
		INSERT INTO topic_threads (id, tenant_id, primary_contact_id, origin_conversation_id, title, intent, category, status, privacy_policy, source, last_activity_at, created_by_user_id, split_from_topic_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'open',$8,$9,now(),$10,$11)`,
		nt.ID, tenantID, nt.PrimaryContactID, nt.OriginConversationID, nt.Title, nt.Intent, nt.Category, string(nt.PrivacyPolicy), string(nt.Source), by, sourceID); err != nil {
		return res, mapError(err)
	}
	for _, m := range messages {
		table, col := linkTable(m.Kind)
		// the placement is now a person's decision: the router's earlier decision for this message is superseded
		if _, err := r.q(ctx).Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE tenant_id=$1 AND topic_thread_id=$2 AND %s=$3`, table, col), tenantID, sourceID, m.ID); err != nil {
			return res, err
		}
		if _, err := r.q(ctx).Exec(ctx, fmt.Sprintf(`
			INSERT INTO %s (tenant_id, %s, topic_thread_id, relation, decision_source, confidence) VALUES ($1,$2,$3,'primary','agent',1)
			ON CONFLICT DO NOTHING`, table, col), tenantID, m.ID, nt.ID); err != nil {
			return res, mapError(err)
		}
		if _, err := r.q(ctx).Exec(ctx, fmt.Sprintf(`UPDATE routing_decisions SET overridden_at=now(), overridden_by_user_id=$3 WHERE tenant_id=$1 AND %s=$2 AND applied AND overridden_at IS NULL`, map[bool]string{false: "message_id", true: "group_message_id"}[m.Kind == ports.KindGroup]), tenantID, m.ID, by); err != nil {
			return res, err
		}
		if m.Kind == ports.KindGroup {
			res.GroupMsgs++
		} else {
			res.Messages++
		}
	}
	// the new topic lives where its messages live
	if _, err := r.q(ctx).Exec(ctx, `
		INSERT INTO topic_conversation_links (tenant_id, topic_thread_id, conversation_id, relation)
		SELECT DISTINCT m.tenant_id, $2::uuid, m.conversation_id, 'origin' FROM message_topic_links l JOIN messages m ON m.tenant_id=l.tenant_id AND m.id=l.message_id
		WHERE l.tenant_id=$1 AND l.topic_thread_id=$2 ON CONFLICT DO NOTHING`, tenantID, nt.ID); err != nil {
		return res, mapError(err)
	}
	if _, err := r.q(ctx).Exec(ctx, `
		INSERT INTO topic_group_links (tenant_id, topic_thread_id, group_id)
		SELECT DISTINCT g.tenant_id, $2::uuid, g.group_id FROM group_message_topic_links l JOIN wa_group_messages g ON g.tenant_id=l.tenant_id AND g.id=l.group_message_id
		WHERE l.tenant_id=$1 AND l.topic_thread_id=$2 ON CONFLICT DO NOTHING`, tenantID, nt.ID); err != nil {
		return res, mapError(err)
	}
	nt.SplitFromTopicID = &sourceID
	res.NewTopic, res.SourceStatus = nt, domain.TopicOpen
	return res, nil
}

func (r *PostgresRestructureRepository) MessageTexts(ctx context.Context, tenantID uuid.UUID, refs []ports.MessageRef) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	for _, m := range refs {
		var text string
		var err error
		if m.Kind == ports.KindGroup {
			err = r.q(ctx).QueryRow(ctx, `SELECT body FROM wa_group_messages WHERE tenant_id=$1 AND id=$2`, tenantID, m.ID).Scan(&text)
		} else {
			err = r.q(ctx).QueryRow(ctx, `SELECT body FROM messages WHERE tenant_id=$1 AND id=$2`, tenantID, m.ID).Scan(&text)
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		out[m.ID] = text
	}
	return out, nil
}
