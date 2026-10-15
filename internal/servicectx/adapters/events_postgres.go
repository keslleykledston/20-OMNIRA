package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

var (
	_ ports.Events             = (*PostgresRepository)(nil)
	_ ports.TimelineRepository = (*PostgresRepository)(nil)
)

// Record appends one event to the operational timeline, in the caller's transaction. It validates the shape (ids, enums and counts only, capped size) and
// is idempotent by dedup_key when one is given. A person can only sign an event as themselves (the policy asks for it too).
func (r *PostgresRepository) Record(ctx context.Context, e domain.ContextEvent) error {
	if err := e.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return err
	}
	var dedup *string
	if e.DedupKey != "" {
		dedup = &e.DedupKey
	}
	_, err = r.q(ctx).Exec(ctx, `
		INSERT INTO context_events (id, tenant_id, event_type, entity_type, entity_id, session_id, case_id, conversation_id, contact_id, customer_id,
			actor_type, actor_id, source_system, occurred_at, recorded_at, schema_version, payload, note, correlation_id, causation_id, dedup_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		ON CONFLICT (tenant_id, dedup_key) WHERE dedup_key IS NOT NULL DO NOTHING`,
		e.ID, e.TenantID, e.EventType, e.EntityType, e.EntityID, e.SessionID, e.CaseID, e.ConversationID, e.ContactID, e.CustomerID,
		e.ActorType, e.ActorID, nonEmpty(e.SourceSystem, "omnira"), e.OccurredAt, e.RecordedAt, nonZero(e.SchemaVersion, 1), payload, e.Note, e.CorrelationID, e.CausationID, dedup)
	return mapErr(err)
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func nonZero(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

const timelineColumns = `id, event_type, entity_type, entity_id, session_id, case_id, conversation_id, contact_id, customer_id, actor_type, actor_id,
	source_system, occurred_at, schema_version, payload, note, correlation_id, derived`

// assignmentProjection shows who held the conversation, from the routing module's own table, only for the moments the session was in course. It is read, never copied.
const assignmentProjection = `
	SELECT ae.id, 'conversation.assignment_changed'::text AS event_type, 'conversation'::text AS entity_type, ae.conversation_id AS entity_id,
	       s.id AS session_id, NULL::uuid AS case_id, ae.conversation_id, s.contact_id, s.customer_id,
	       CASE WHEN ae.changed_by IS NULL THEN 'system' ELSE 'user' END AS actor_type, ae.changed_by AS actor_id,
	       'omnira.routing'::text AS source_system, ae.created_at AS occurred_at, 1 AS schema_version,
	       jsonb_build_object('from_user_id', ae.from_user_id, 'to_user_id', ae.to_user_id) AS payload, ''::text AS note, NULL::uuid AS correlation_id, true AS derived
	FROM service_sessions s
	JOIN assignment_events ae ON ae.tenant_id = s.tenant_id AND ae.conversation_id = s.conversation_id
	WHERE s.tenant_id = $1 AND s.id = $2
	  AND EXISTS (SELECT 1 FROM service_session_episodes e WHERE e.tenant_id = s.tenant_id AND e.session_id = s.id
	              AND ae.created_at >= e.started_at AND (e.ended_at IS NULL OR ae.created_at < e.ended_at))`

func (r *PostgresRepository) Timeline(ctx context.Context, scope ports.TimelineScope, cursor string, limit int, ascending bool) ([]ports.TimelineItem, string, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, "", err
	}
	stored := `SELECT id, event_type, entity_type, entity_id, session_id, case_id, conversation_id, contact_id, customer_id, actor_type, actor_id,
		source_system, occurred_at, schema_version, payload, note, correlation_id, false AS derived FROM context_events WHERE tenant_id = $1 AND `
	var inner string
	args := []any{tenant}
	switch {
	case scope.SessionID != nil:
		args = append(args, *scope.SessionID)
		inner = stored + `session_id = $2 UNION ALL ` + assignmentProjection
	case scope.CaseID != nil:
		args = append(args, *scope.CaseID)
		inner = stored + `case_id = $2`
	case scope.ContactID != nil:
		args = append(args, *scope.ContactID)
		inner = stored + `contact_id = $2`
	case scope.CustomerID != nil:
		args = append(args, *scope.CustomerID)
		inner = stored + `customer_id = $2`
	default:
		return nil, "", domain.Invalid("a timeline needs a scope")
	}
	op, dir := "<", "DESC"
	if ascending {
		op, dir = ">", "ASC"
	}
	cond := ""
	if cursor != "" {
		t, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, t, id)
		cond = fmt.Sprintf(` WHERE (occurred_at, id) %s ($%d, $%d)`, op, len(args)-1, len(args))
	}
	args = append(args, limit+1)
	rows, err := r.q(ctx).Query(ctx, `SELECT `+timelineColumns+` FROM (`+inner+`) t`+cond+
		fmt.Sprintf(` ORDER BY occurred_at %s, id %s LIMIT $%d`, dir, dir, len(args)), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []ports.TimelineItem
	for rows.Next() {
		var it ports.TimelineItem
		var payload []byte
		if err := rows.Scan(&it.ID, &it.EventType, &it.EntityType, &it.EntityID, &it.SessionID, &it.CaseID, &it.ConversationID, &it.ContactID, &it.CustomerID,
			&it.ActorType, &it.ActorID, &it.SourceSystem, &it.OccurredAt, &it.SchemaVersion, &payload, &it.Note, &it.CorrelationID, &it.Derived); err != nil {
			return nil, "", err
		}
		if len(payload) > 0 {
			_ = json.Unmarshal(payload, &it.Payload)
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeCursor(last.OccurredAt, last.ID)
	}
	return out, next, nil
}

func (r *PostgresRepository) LinkedCustomers(ctx context.Context, contactID uuid.UUID) ([]ports.LinkedCustomer, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.q(ctx).Query(ctx, `
		SELECT a.id, a.name, l.is_primary FROM contact_account_links l
		JOIN customer_accounts a ON a.tenant_id = l.tenant_id AND a.id = l.account_id
		WHERE l.tenant_id = $1 AND l.contact_id = $2 AND l.status = 'active' AND a.status = 'active'
		ORDER BY l.is_primary DESC, a.name, a.id`, tenant, contactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ports.LinkedCustomer
	for rows.Next() {
		var c ports.LinkedCustomer
		if err := rows.Scan(&c.ID, &c.Name, &c.IsPrimary); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) SessionsOfContact(ctx context.Context, contactID uuid.UUID, customerID *uuid.UUID, limit int) ([]domain.Session, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	where := []string{"tenant_id = $1", "contact_id = $2"}
	args := []any{tenant, contactID}
	if customerID != nil {
		args = append(args, *customerID)
		where = append(where, fmt.Sprintf("(customer_id = $%d OR customer_id IS NULL)", len(args)))
	}
	args = append(args, limit)
	rows, err := r.q(ctx).Query(ctx, `SELECT `+sessionColumns+` FROM service_sessions WHERE `+strings.Join(where, " AND ")+
		fmt.Sprintf(` ORDER BY opened_at DESC, id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}
