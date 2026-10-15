// Package adapters is the PostgreSQL and HTTP edge of Service Context & Operations.
package adapters

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// PostgresRepository runs every statement on the request's tenant-scoped transaction (RLS) and ALSO filters by the tenant of the TenantContext: the
// second barrier, never the only one.
type PostgresRepository struct{ pool *pgxpool.Pool }

var _ ports.Repository = (*PostgresRepository)(nil)

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

func tenantOf(ctx context.Context) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, errors.New("servicectx: tenant context required")
	}
	return tc.TenantID, nil
}

// mapErr turns the database's refusals into the module's errors. 42501 is a row guard or a policy; 23P01 is the disjoint-intervals guard.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "42501":
			return domain.ErrForbidden
		case "23P01":
			return domain.ErrConflict
		case "23514":
			return fmt.Errorf("%w: %s", domain.ErrInvalid, pe.Message)
		}
	}
	return err
}

func (r *PostgresRepository) Now(ctx context.Context) (time.Time, error) {
	var t time.Time
	if err := r.q(ctx).QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&t); err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

const conversationFactsSQL = `
	SELECT c.id, c.contact_id, c.status, c.conversation_kind, c.assigned_to_user_id, COALESCE(cc.provider, '')
	FROM conversations c
	LEFT JOIN channel_connections cc ON cc.tenant_id = c.tenant_id AND cc.id = c.channel_connection_id
	WHERE c.tenant_id = $1 AND c.id = $2`

func (r *PostgresRepository) conversation(ctx context.Context, id uuid.UUID, lock bool) (*ports.ConversationFacts, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	sql := conversationFactsSQL
	if lock {
		sql += ` FOR UPDATE OF c`
	}
	f := &ports.ConversationFacts{}
	if err := r.q(ctx).QueryRow(ctx, sql, tenant, id).Scan(&f.ID, &f.ContactID, &f.Status, &f.Kind, &f.AssignedTo, &f.Channel); err != nil {
		return nil, mapErr(err)
	}
	return f, nil
}

func (r *PostgresRepository) LockConversation(ctx context.Context, id uuid.UUID) (*ports.ConversationFacts, error) {
	return r.conversation(ctx, id, true)
}

func (r *PostgresRepository) ReadConversation(ctx context.Context, id uuid.UUID) (*ports.ConversationFacts, error) {
	return r.conversation(ctx, id, false)
}

const sessionColumns = `id, tenant_id, protocol, protocol_sequence, external_protocol, conversation_id, contact_id, customer_id, queue_id, category_id,
	status, resolution_code, resolution_canonical, resolution_note, origin_channel, opened_source, closed_source, opened_by_user_id, closed_by_user_id,
	opened_at, first_response_at, closed_at, reopen_count, version, created_at, updated_at`

func scanSession(row pgx.Row) (*domain.Session, error) {
	s := &domain.Session{}
	var status string
	var note *string
	if err := row.Scan(&s.ID, &s.TenantID, &s.Protocol, &s.ProtocolSequence, &s.ExternalProtocol, &s.ConversationID, &s.ContactID, &s.CustomerID, &s.QueueID,
		&s.CategoryID, &status, &s.ResolutionCode, &s.ResolutionCanonical, &note, &s.OriginChannel, &s.OpenedSource, &s.ClosedSource, &s.OpenedBy, &s.ClosedBy,
		&s.OpenedAt, &s.FirstResponseAt, &s.ClosedAt, &s.ReopenCount, &s.Version, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	s.Status = domain.SessionStatus(status)
	if note != nil {
		s.ResolutionNote = *note
	}
	return s, nil
}

func (r *PostgresRepository) one(ctx context.Context, where string, lock bool, args ...any) (*domain.Session, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	sql := `SELECT ` + sessionColumns + ` FROM service_sessions WHERE tenant_id = $1 AND ` + where
	if lock {
		sql += ` FOR UPDATE`
	}
	s, err := scanSession(r.q(ctx).QueryRow(ctx, sql, append([]any{tenant}, args...)...))
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (r *PostgresRepository) optional(s *domain.Session, err error) (*domain.Session, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func (r *PostgresRepository) ActiveByConversation(ctx context.Context, conversationID uuid.UUID) (*domain.Session, error) {
	return r.optional(r.one(ctx, `conversation_id = $2 AND status IN ('open','in_progress','waiting')`, false, conversationID))
}

func (r *PostgresRepository) LatestByConversation(ctx context.Context, conversationID uuid.UUID) (*domain.Session, error) {
	return r.optional(r.one(ctx, `conversation_id = $2 ORDER BY opened_at DESC, id DESC LIMIT 1`, false, conversationID))
}

func (r *PostgresRepository) GetSession(ctx context.Context, id uuid.UUID) (*domain.Session, error) {
	s, err := r.one(ctx, `id = $2`, false, id)
	return s, mapErr(err)
}

func (r *PostgresRepository) LockSession(ctx context.Context, id uuid.UUID) (*domain.Session, error) {
	s, err := r.one(ctx, `id = $2`, true, id)
	return s, mapErr(err)
}

func (r *PostgresRepository) NextProtocol(ctx context.Context, kind, prefix string, start int64) (string, int64, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return "", 0, err
	}
	var gotPrefix string
	var v int64
	err = r.q(ctx).QueryRow(ctx, `
		INSERT INTO service_counters (tenant_id, kind, prefix, last_value) VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, kind) DO UPDATE SET last_value = service_counters.last_value + 1, updated_at = now()
		RETURNING prefix, last_value`, tenant, kind, prefix, start).Scan(&gotPrefix, &v)
	if err != nil {
		return "", 0, mapErr(err)
	}
	return gotPrefix, v, nil
}

func (r *PostgresRepository) InsertSession(ctx context.Context, s *domain.Session) error {
	_, err := r.q(ctx).Exec(ctx, `
		INSERT INTO service_sessions (id, tenant_id, protocol, protocol_sequence, external_protocol, conversation_id, contact_id, customer_id, queue_id, category_id,
			status, origin_channel, opened_source, opened_by_user_id, opened_at, version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
		s.ID, s.TenantID, s.Protocol, s.ProtocolSequence, s.ExternalProtocol, s.ConversationID, s.ContactID, s.CustomerID, s.QueueID, s.CategoryID,
		string(s.Status), s.OriginChannel, s.OpenedSource, s.OpenedBy, s.OpenedAt, s.Version, s.CreatedAt, s.UpdatedAt)
	return mapErr(err)
}

func (r *PostgresRepository) UpdateSession(ctx context.Context, s *domain.Session, expectedVersion int) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var note *string
	if s.ResolutionNote != "" {
		n := s.ResolutionNote
		note = &n
	}
	tag := r.q(ctx).QueryRow(ctx, `
		UPDATE service_sessions SET customer_id=$3, queue_id=$4, category_id=$5, status=$6, resolution_code=$7, resolution_canonical=$8, resolution_note=$9,
			closed_source=$10, closed_by_user_id=$11, closed_at=$12, first_response_at=$13, reopen_count=$14
		WHERE tenant_id=$1 AND id=$2 AND version=$15
		RETURNING version, updated_at`,
		tenant, s.ID, s.CustomerID, s.QueueID, s.CategoryID, string(s.Status), s.ResolutionCode, s.ResolutionCanonical, note,
		s.ClosedSource, s.ClosedBy, s.ClosedAt, s.FirstResponseAt, s.ReopenCount, expectedVersion)
	if err := tag.Scan(&s.Version, &s.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var one int
			if e := r.q(ctx).QueryRow(ctx, `SELECT 1 FROM service_sessions WHERE tenant_id=$1 AND id=$2`, tenant, s.ID).Scan(&one); e != nil {
				return domain.ErrNotFound
			}
			return domain.ErrConflict
		}
		return mapErr(err)
	}
	return nil
}

func (r *PostgresRepository) exists(ctx context.Context, sql string, id uuid.UUID) (bool, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return false, err
	}
	var ok bool
	if err := r.q(ctx).QueryRow(ctx, `SELECT EXISTS (`+sql+`)`, tenant, id).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}

func (r *PostgresRepository) CustomerExists(ctx context.Context, id uuid.UUID) (bool, error) {
	return r.exists(ctx, `SELECT 1 FROM customer_accounts WHERE tenant_id=$1 AND id=$2`, id)
}
func (r *PostgresRepository) QueueExists(ctx context.Context, id uuid.UUID) (bool, error) {
	return r.exists(ctx, `SELECT 1 FROM queues WHERE tenant_id=$1 AND id=$2`, id)
}
func (r *PostgresRepository) ContactExists(ctx context.Context, id uuid.UUID) (bool, error) {
	return r.exists(ctx, `SELECT 1 FROM contacts WHERE tenant_id=$1 AND id=$2`, id)
}
func (r *PostgresRepository) ActiveCategoryExists(ctx context.Context, id uuid.UUID) (bool, error) {
	return r.exists(ctx, `SELECT 1 FROM service_categories WHERE tenant_id=$1 AND id=$2 AND active`, id)
}

func (r *PostgresRepository) ResolveResult(ctx context.Context, code string) (string, error) {
	if domain.IsCanonicalResolution(code) {
		return code, nil
	}
	tenant, err := tenantOf(ctx)
	if err != nil {
		return "", err
	}
	var canonical string
	err = r.q(ctx).QueryRow(ctx, `SELECT canonical_code FROM service_resolution_codes WHERE tenant_id=$1 AND code=$2 AND active`, tenant, code).Scan(&canonical)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrUnknownResolution
	}
	return canonical, err
}

func (r *PostgresRepository) OpenEpisode(ctx context.Context, sessionID uuid.UUID, startedAt time.Time) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.q(ctx).Exec(ctx, `
		INSERT INTO service_session_episodes (tenant_id, session_id, seq, started_at)
		VALUES ($1, $2, COALESCE((SELECT max(seq) FROM service_session_episodes WHERE tenant_id=$1 AND session_id=$2), 0) + 1, $3)`, tenant, sessionID, startedAt)
	return mapErr(err)
}

func (r *PostgresRepository) EndEpisode(ctx context.Context, sessionID uuid.UUID, endedAt time.Time) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	tag, err := r.q(ctx).Exec(ctx, `UPDATE service_session_episodes SET ended_at=$3 WHERE tenant_id=$1 AND session_id=$2 AND ended_at IS NULL`, tenant, sessionID, endedAt)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("servicectx: session %s has no open episode to end", sessionID)
	}
	return nil
}

// inEpisode is the membership rule of a message in a session: created inside one of its episodes, half-open.
const inEpisode = `EXISTS (SELECT 1 FROM service_session_episodes e WHERE e.tenant_id = m.tenant_id AND e.session_id = %s
	AND m.created_at >= e.started_at AND (e.ended_at IS NULL OR m.created_at < e.ended_at))`

func (r *PostgresRepository) FirstHumanReplyAt(ctx context.Context, s *domain.Session) (*time.Time, error) {
	if s.ConversationID == nil {
		return nil, nil
	}
	var t *time.Time
	err := r.q(ctx).QueryRow(ctx, `
		SELECT min(m.created_at) FROM messages m
		WHERE m.tenant_id=$1 AND m.conversation_id=$2 AND m.direction='outbound' AND m.sent_by_user_id IS NOT NULL AND `+fmt.Sprintf(inEpisode, "$3"),
		s.TenantID, *s.ConversationID, s.ID).Scan(&t)
	return t, err
}

func (r *PostgresRepository) HasLaterSession(ctx context.Context, s *domain.Session) (bool, error) {
	if s.ConversationID == nil {
		return false, nil
	}
	var ok bool
	err := r.q(ctx).QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM service_sessions WHERE tenant_id=$1 AND conversation_id=$2 AND id<>$3 AND opened_at >= $4)`,
		s.TenantID, *s.ConversationID, s.ID, s.OpenedAt).Scan(&ok)
	return ok, err
}

// cursor is "<RFC3339Nano>|<uuid>" in base64: opaque to callers, stable under inserts (keyset, never OFFSET).
func encodeCursor(t time.Time, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(t.UTC().Format(time.RFC3339Nano) + "|" + id.String()))
}

func decodeCursor(c string) (time.Time, uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return time.Time{}, uuid.Nil, domain.Invalid("cursor")
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, domain.Invalid("cursor")
	}
	t, err1 := time.Parse(time.RFC3339Nano, parts[0])
	id, err2 := uuid.Parse(parts[1])
	if err1 != nil || err2 != nil {
		return time.Time{}, uuid.Nil, domain.Invalid("cursor")
	}
	return t, id, nil
}

func (r *PostgresRepository) ListSessions(ctx context.Context, f ports.SessionFilter) ([]domain.Session, string, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, "", err
	}
	where := []string{"tenant_id = $1"}
	args := []any{tenant}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", fmt.Sprintf("$%d", len(args))))
	}
	if f.Status != "" {
		add("status = ?", string(f.Status))
	}
	if f.ConversationID != nil {
		add("conversation_id = ?", *f.ConversationID)
	}
	if f.ContactID != nil {
		add("contact_id = ?", *f.ContactID)
	}
	if f.CustomerID != nil {
		add("customer_id = ?", *f.CustomerID)
	}
	if f.QueueID != nil {
		add("queue_id = ?", *f.QueueID)
	}
	if f.Protocol != "" {
		add("protocol = ?", strings.ToUpper(strings.TrimSpace(f.Protocol)))
	}
	if f.OpenedFrom != nil {
		add("opened_at >= ?", *f.OpenedFrom)
	}
	if f.OpenedTo != nil {
		add("opened_at < ?", *f.OpenedTo)
	}
	if f.Cursor != "" {
		t, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, t, id)
		where = append(where, fmt.Sprintf("(opened_at, id) < ($%d, $%d)", len(args)-1, len(args)))
	}
	args = append(args, f.Limit+1)
	rows, err := r.q(ctx).Query(ctx, `SELECT `+sessionColumns+` FROM service_sessions WHERE `+strings.Join(where, " AND ")+
		fmt.Sprintf(` ORDER BY opened_at DESC, id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []domain.Session
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = encodeCursor(last.OpenedAt, last.ID)
	}
	return out, next, nil
}

func (r *PostgresRepository) SessionMessages(ctx context.Context, s *domain.Session, cursor string, limit int) ([]ports.MessageRef, string, error) {
	if s.ConversationID == nil {
		return nil, "", nil
	}
	args := []any{s.TenantID, *s.ConversationID, s.ID}
	cond := ""
	if cursor != "" {
		t, id, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, t, id)
		cond = ` AND (m.created_at, m.id) > ($4, $5)`
	}
	args = append(args, limit+1)
	rows, err := r.q(ctx).Query(ctx, `
		SELECT m.id, m.direction, m.message_type, m.status, m.created_at, m.sent_by_user_id FROM messages m
		WHERE m.tenant_id=$1 AND m.conversation_id=$2 AND `+fmt.Sprintf(inEpisode, "$3")+cond+
		fmt.Sprintf(` ORDER BY m.created_at, m.id LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []ports.MessageRef
	for rows.Next() {
		var m ports.MessageRef
		if err := rows.Scan(&m.ID, &m.Direction, &m.Type, &m.Status, &m.CreatedAt, &m.SentByUser); err != nil {
			return nil, "", err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}

func (r *PostgresRepository) LockScopedIdempotency(ctx context.Context, scope, key string) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	_, err = r.q(ctx).Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, tenant.String()+":service-"+scope+"-idem:"+key)
	return err
}

func (r *PostgresRepository) ReceiptFor(ctx context.Context, scope, key string) (*ports.Receipt, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rec := &ports.Receipt{}
	err = r.q(ctx).QueryRow(ctx, `SELECT request_hash, session_id, case_id, task_id FROM service_command_receipts WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
		tenant, scope, key).Scan(&rec.RequestHash, &rec.SessionID, &rec.CaseID, &rec.TaskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return rec, err
}

func (r *PostgresRepository) SaveReceipt(ctx context.Context, scope, key, requestHash string, entityID uuid.UUID) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var sess, cs, task *uuid.UUID
	switch scope {
	case ports.ScopeSessionOpen:
		sess = &entityID
	case ports.ScopeCaseOpen:
		cs = &entityID
	case ports.ScopeTaskCreate:
		task = &entityID
	default:
		return fmt.Errorf("servicectx: unknown receipt scope %q", scope)
	}
	_, err = r.q(ctx).Exec(ctx, `INSERT INTO service_command_receipts (tenant_id, scope, idempotency_key, request_hash, session_id, case_id, task_id) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		tenant, scope, key, requestHash, sess, cs, task)
	return mapErr(err)
}
