package adapters

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/contextwindow"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresContextWindowStore runs in system sessions (the worker has no human actor); the tables' write policies
// admit only those — see migration 000176.
type PostgresContextWindowStore struct{ pool *pgxpool.Pool }

var _ ports.ContextWindowStore = (*PostgresContextWindowStore)(nil)

func NewPostgresContextWindowStore(pool *pgxpool.Pool) *PostgresContextWindowStore {
	return &PostgresContextWindowStore{pool: pool}
}

func (s *PostgresContextWindowStore) system(ctx context.Context, fn func(ctx context.Context, q platformdb.Querier) error) error {
	return platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(sctx context.Context) error {
		return fn(sctx, platformdb.QuerierFromContext(sctx, s.pool))
	})
}

// isUniqueViolation matches Postgres SQLSTATE 23505 (unique_violation) — used only for the open-window race in Touch.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *PostgresContextWindowStore) ResolveMessage(ctx context.Context, messageID uuid.UUID) (ports.ResolvedMessage, bool, error) {
	var msg ports.ResolvedMessage
	found := false
	err := s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		// ADR-0018: a conversation with staff has no contact and is never customer content — TopicRepository.
		// ConversationInfo already excludes it the same way, but this is a SEPARATE path (the consumer decides
		// whether to open/extend a window at all) that never applied the same guard: an internal conversation's
		// inbound message would otherwise enter the customer-context pipeline and get analyzed/labeled "cliente"
		// (Codex review round 9).
		err := q.QueryRow(ctx, `
			SELECT m.tenant_id, m.conversation_id, m.created_at FROM messages m
			JOIN conversations c ON c.tenant_id = m.tenant_id AND c.id = m.conversation_id
			WHERE m.id=$1 AND m.direction='inbound' AND c.internal_user_id IS NULL`, messageID).
			Scan(&msg.TenantID, &msg.ConversationID, &msg.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		found = true
		return nil
	})
	return msg, found, err
}

func (s *PostgresContextWindowStore) Touch(ctx context.Context, policy contextwindow.Policy, tenantID, conversationID, messageID uuid.UUID, messageAt time.Time) error {
	const maxAttempts = 5 // generous: Postgres guarantees the winner of the race has already committed by the time a loser sees 23505
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err = s.touchOnce(ctx, policy, tenantID, conversationID, messageID, messageAt); err == nil {
			return nil
		}
		if !isUniqueViolation(err) {
			return err
		}
		// Another caller opened the window between our SELECT and INSERT; the next attempt's SELECT sees it.
	}
	return fmt.Errorf("context window store: touch could not settle the open-window race after %d attempts: %w", maxAttempts, err)
}

func (s *PostgresContextWindowStore) touchOnce(ctx context.Context, policy contextwindow.Policy, tenantID, conversationID, messageID uuid.UUID, messageAt time.Time) error {
	return s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		var exists bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM context_analysis_window_messages WHERE tenant_id=$1 AND message_id=$2)`,
			tenantID, messageID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			// Redelivered event for a message already recorded — in this window, or in an earlier one that has
			// since closed. A message belongs to exactly one window ever (PRIMARY KEY (tenant_id, message_id) on
			// context_analysis_window_messages, migration 000176): skip rather than double-count it into a new
			// window, which would analyze it twice.
			return nil
		}
		var (
			windowID        uuid.UUID
			firstAt, lastAt time.Time
			count           int
		)
		err := q.QueryRow(ctx, `
			SELECT id, first_message_at, last_message_at, message_count
			FROM context_analysis_windows WHERE tenant_id=$1 AND conversation_id=$2 AND state='open' FOR UPDATE`,
			tenantID, conversationID).Scan(&windowID, &firstAt, &lastAt, &count)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			// An existing open window: record membership, then recompute due_at for this new message. The
			// ON CONFLICT is a safety net for a concurrent duplicate racing the EXISTS check above, not the
			// expected path (that one already returned above).
			tag, ierr := q.Exec(ctx, `
				INSERT INTO context_analysis_window_messages (tenant_id, window_id, message_id, message_at) VALUES ($1,$2,$3,$4)
				ON CONFLICT (tenant_id, message_id) DO NOTHING`, tenantID, windowID, messageID, messageAt)
			if ierr != nil {
				return ierr
			}
			if tag.RowsAffected() == 0 {
				return nil // concurrent duplicate already recorded this message
			}
			snap := contextwindow.Snapshot{FirstAt: firstAt, LastAt: lastAt, Count: count + 1}
			if messageAt.After(lastAt) {
				snap.LastAt = messageAt
			}
			decision, derr := policy.Evaluate(snap, time.Now().UTC(), contextwindow.TriggerNone)
			if derr != nil {
				return derr
			}
			state := "open"
			if decision.Ready {
				state = "due"
			}
			_, uerr := q.Exec(ctx, `
				UPDATE context_analysis_windows
				SET last_message_at = GREATEST(last_message_at, $3), last_message_id = CASE WHEN $3 >= last_message_at THEN $4 ELSE last_message_id END,
				    message_count = $5, due_at = $6, state = $7, updated_at = now()
				WHERE tenant_id=$1 AND id=$2`,
				tenantID, windowID, messageAt, messageID, snap.Count, decision.DueAt, state)
			return uerr
		}
		// No open window (pgx.ErrNoRows above): start generation = previous + 1 (1 if this conversation never had one).
		snap := contextwindow.Snapshot{FirstAt: messageAt, LastAt: messageAt, Count: 1}
		decision, derr := policy.Evaluate(snap, time.Now().UTC(), contextwindow.TriggerNone)
		if derr != nil {
			return derr
		}
		state := "open"
		if decision.Ready {
			state = "due"
		}
		newID := uuid.New()
		if _, ierr := q.Exec(ctx, `
			INSERT INTO context_analysis_windows (id, tenant_id, conversation_id, generation, state, first_message_at, first_message_id,
			                                       last_message_at, last_message_id, message_count, due_at)
			SELECT $1, $2, $3, COALESCE((SELECT max(generation) FROM context_analysis_windows WHERE tenant_id=$2 AND conversation_id=$3), 0) + 1,
			       $4, $5, $6, $5, $6, 1, $7`,
			newID, tenantID, conversationID, state, messageAt, messageID, decision.DueAt); ierr != nil {
			return ierr
		}
		// No ON CONFLICT here: a concurrent duplicate that races the EXISTS check above must fail with 23505 and
		// roll back this whole transaction — including the window row just inserted — rather than leave an
		// orphan window whose message_count says 1 but which owns zero actual window_messages rows. Touch's
		// retry loop treats a unique violation as "retry", and the retry's EXISTS check then sees it and no-ops.
		_, ierr := q.Exec(ctx, `INSERT INTO context_analysis_window_messages (tenant_id, window_id, message_id, message_at) VALUES ($1,$2,$3,$4)`,
			tenantID, newID, messageID, messageAt)
		return ierr
	})
}

func (s *PostgresContextWindowStore) forceWith(ctx context.Context, q platformdb.Querier, tenantID, conversationID uuid.UUID, trigger contextwindow.Trigger) (bool, error) {
	var forced bool
	err := q.QueryRow(ctx, `SELECT force_context_window($1, $2, $3)`, tenantID, conversationID, string(trigger)).Scan(&forced)
	return forced, err
}

// Force reuses the caller's OWN session/connection when one is already active on ctx — its real caller, the
// manual-refresh HTTP handler, runs inside the REQUEST's own (member-level) WithTenantSession. Force used to
// unconditionally open its own system session on top of that, a second nested pool connection that could stall
// every refresh on a small pool and exhaust a larger one under concurrent refreshes (Codex review round 8, the
// same class of bug as MessageIDs/round 4). force_context_window (migration 000176) is SECURITY DEFINER, so it
// can perform the privileged UPDATE on the caller's own connection regardless of that session's own privilege
// level — no escalation needed, no second connection. Only a standalone caller with no session of its own (a
// direct test call, or a future system-session caller) falls back to the self-contained system session.
func (s *PostgresContextWindowStore) Force(ctx context.Context, tenantID, conversationID uuid.UUID, trigger contextwindow.Trigger) (bool, error) {
	if q := platformdb.QuerierFromContext(ctx, nil); q != nil {
		return s.forceWith(ctx, q, tenantID, conversationID, trigger)
	}
	var forced bool
	err := s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		f, ferr := s.forceWith(ctx, q, tenantID, conversationID, trigger)
		forced = f
		return ferr
	})
	return forced, err
}

func (s *PostgresContextWindowStore) Sweep(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 1
	}
	var n int
	err := s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		tag, err := q.Exec(ctx, `
			WITH due AS (
			  SELECT w.id FROM context_analysis_windows w
			  JOIN tenants t ON t.id = w.tenant_id AND t.status = 'active'
			  WHERE w.state = 'open' AND w.due_at <= $1
			  ORDER BY w.due_at
			  LIMIT $2
			  FOR UPDATE OF w SKIP LOCKED
			  FOR SHARE OF t SKIP LOCKED
			)
			UPDATE context_analysis_windows w SET state='due', updated_at=now() FROM due WHERE w.id = due.id`, now, limit)
		if err != nil {
			return err
		}
		n = int(tag.RowsAffected())
		return nil
	})
	return n, err
}

func (s *PostgresContextWindowStore) Claim(ctx context.Context, limit int, lease time.Duration) ([]ports.ContextWindow, error) {
	if limit <= 0 {
		limit = 1
	}
	var out []ports.ContextWindow
	err := s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		rows, err := q.Query(ctx, `
			WITH due AS (
			  SELECT w.id FROM context_analysis_windows w
			  JOIN tenants t ON t.id = w.tenant_id AND t.status = 'active'
			  WHERE (w.state = 'due' AND w.due_at <= now()) OR (w.state = 'running' AND w.locked_until < now())
			  ORDER BY w.due_at, w.created_at
			  LIMIT $1
			  FOR UPDATE OF w SKIP LOCKED
			  FOR SHARE OF t SKIP LOCKED
			)
			UPDATE context_analysis_windows w
			SET state='running', attempt = w.attempt + 1, started_at = now(), locked_until = now() + make_interval(secs => $2::float8), updated_at = now()
			FROM due WHERE w.id = due.id
			RETURNING w.id, w.tenant_id, w.conversation_id, w.generation, w.trigger, w.first_message_at, w.first_message_id,
			          w.last_message_at, w.last_message_id, w.message_count, w.due_at, w.attempt`, limit, lease.Seconds())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var w ports.ContextWindow
			var trig *string
			if err := rows.Scan(&w.ID, &w.TenantID, &w.ConversationID, &w.Generation, &trig, &w.FirstMessageAt, &w.FirstMessageID,
				&w.LastMessageAt, &w.LastMessageID, &w.MessageCount, &w.DueAt, &w.Attempt); err != nil {
				return err
			}
			if trig != nil {
				w.Trigger = contextwindow.Trigger(*trig)
			}
			out = append(out, w)
		}
		return rows.Err()
	})
	return out, err
}

func (s *PostgresContextWindowStore) messageIDsWith(ctx context.Context, q platformdb.Querier, w ports.ContextWindow) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `
		SELECT message_id FROM context_analysis_window_messages WHERE tenant_id=$1 AND window_id=$2 ORDER BY message_at, message_id`,
		w.TenantID, w.ID)
	if err != nil {
		return nil, err
	}
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

// MessageIDs reuses the caller's OWN session/connection when one is already active on ctx (its only real caller,
// WindowAnalyzerService.Analyze, always runs inside the runner's session) — opening ANOTHER nested session here
// would acquire a SECOND pool connection per in-flight analysis; with up to ClaimLimit analyses running at once,
// that alone can exhaust a pool no bigger than the claim batch and deadlock the whole runner (every goroutine
// holding its outer connection while blocked acquiring a second one — Codex review round 4). A standalone caller
// with no session of its own (none in production; only a direct test call) still gets the old self-contained
// system session as a fallback.
func (s *PostgresContextWindowStore) MessageIDs(ctx context.Context, w ports.ContextWindow) ([]uuid.UUID, error) {
	if q := platformdb.QuerierFromContext(ctx, nil); q != nil {
		return s.messageIDsWith(ctx, q, w)
	}
	var out []uuid.UUID
	err := s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		ids, err := s.messageIDsWith(ctx, q, w)
		out = ids
		return err
	})
	return out, err
}

// move changes the state only for the holder of the current claim (fenced by attempt, exactly like intelligence_jobs).
func (s *PostgresContextWindowStore) move(ctx context.Context, w ports.ContextWindow, sql string, args ...any) (bool, error) {
	var ok bool
	err := s.system(ctx, func(ctx context.Context, q platformdb.Querier) error {
		tag, err := q.Exec(ctx, sql, append([]any{w.ID, w.Attempt}, args...)...)
		if err != nil {
			return err
		}
		ok = tag.RowsAffected() == 1
		return nil
	})
	return ok, err
}

func (s *PostgresContextWindowStore) Complete(ctx context.Context, w ports.ContextWindow) (bool, error) {
	return s.move(ctx, w, `UPDATE context_analysis_windows SET state='completed', completed_at=now(), locked_until=NULL, last_error_class=NULL, updated_at=now()
		WHERE id=$1 AND attempt=$2 AND state='running'`)
}

func (s *PostgresContextWindowStore) Retry(ctx context.Context, w ports.ContextWindow, class string, delay time.Duration) (bool, error) {
	return s.move(ctx, w, `UPDATE context_analysis_windows SET state='due', last_error_class=$3, due_at=now()+make_interval(secs => $4::float8), locked_until=NULL, updated_at=now()
		WHERE id=$1 AND attempt=$2 AND state='running'`, class, delay.Seconds())
}

func (s *PostgresContextWindowStore) Dead(ctx context.Context, w ports.ContextWindow, class string) (bool, error) {
	return s.move(ctx, w, `UPDATE context_analysis_windows SET state='dead', last_error_class=$3, locked_until=NULL, completed_at=now(), updated_at=now()
		WHERE id=$1 AND attempt=$2 AND state='running'`, class)
}
