package adapters

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// presenceChunkSize bounds both the DB page size and the Valkey OnlineMembers
// batch size per iteration (IAM4.2-B1). It is an optimization, never a
// functional limit: the candidate walk keeps fetching further DB pages and
// checking further Valkey batches until either an assignment succeeds or the
// DB is genuinely exhausted (a page shorter than presenceChunkSize).
const presenceChunkSize = 50

// PostgresAssignmentRepository. presence is nil-safe: when nil (Valkey never
// configured for this process), a tenant with routing_require_presence=true
// fails closed (ports.ErrPresenceUnavailable) rather than silently skipping
// the check — never inferred online.
type PostgresAssignmentRepository struct {
	pool     *pgxpool.Pool
	presence ports.PresenceChecker
}

var _ ports.AssignmentRepository = (*PostgresAssignmentRepository)(nil)

func NewPostgresAssignmentRepository(pool *pgxpool.Pool, presence ports.PresenceChecker) *PostgresAssignmentRepository {
	return &PostgresAssignmentRepository{pool: pool, presence: presence}
}

// ClaimUnassigned uses one SQL statement. Concurrent callers cannot both
// update the NULL owner, and history is inserted only for the winning update.
// Manual claim: never presence-gated, regardless of routing_require_presence.
func (r *PostgresAssignmentRepository) ClaimUnassigned(ctx context.Context, conversationID, userID uuid.UUID, reason string) (bool, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || userID == uuid.Nil || tc.ActorID != userID {
		return false, errors.New("routing: tenant actor mismatch")
	}
	var claimed bool
	err = platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, `
		WITH eligible AS (
		  SELECT 1 FROM conversations c
		  JOIN memberships m ON m.tenant_id=c.tenant_id AND m.user_id=$3 AND m.status='active'
		  JOIN agent_profiles ap ON ap.tenant_id=m.tenant_id AND ap.membership_id=m.id AND ap.status='active'
		  WHERE c.tenant_id=$1 AND c.id=$2 AND (c.queue_id IS NULL OR EXISTS (
		    SELECT 1 FROM queue_members qm WHERE qm.tenant_id=c.tenant_id AND qm.queue_id=c.queue_id AND qm.user_id=$3
		    AND qm.active AND qm.available AND (SELECT count(*) FROM conversations active WHERE active.tenant_id=qm.tenant_id AND active.assigned_to_user_id=qm.user_id AND active.status='open') < qm.capacity
		  ))
		), claimed AS (
		  UPDATE conversations
		  SET assigned_to_user_id=$3, assigned_at=now(), updated_at=now()
		  WHERE tenant_id=$1 AND id=$2 AND assigned_to_user_id IS NULL AND EXISTS(SELECT 1 FROM eligible)
		  RETURNING tenant_id, id
		), recorded AS (
		  INSERT INTO assignment_events(tenant_id,conversation_id,from_user_id,to_user_id,changed_by,reason)
		  SELECT tenant_id,id,NULL,$3,$3,$4 FROM claimed
		  RETURNING id
		)
		SELECT EXISTS(SELECT 1 FROM recorded)`, tc.TenantID, conversationID, userID, reason).Scan(&claimed)
	return claimed, err
}

// AssignRoundRobin branches on the tenant's routing_require_presence flag,
// read once at the top. Flag off runs the exact IAM4.1 query, unchanged,
// with zero Valkey involvement — this is what guarantees the "flag off ==
// today" contract (verified by a dedicated regression test asserting zero
// presence calls).
func (r *PostgresAssignmentRepository) AssignRoundRobin(ctx context.Context, conversationID uuid.UUID, reason string) (uuid.UUID, bool, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.Source != tenancydomain.AccessSourceSystem {
		return uuid.Nil, false, errors.New("routing: system tenant context required")
	}
	q := platformdb.QuerierFromContext(ctx, r.pool)
	var requirePresence bool
	if err := q.QueryRow(ctx, `SELECT routing_require_presence FROM tenants WHERE id=$1`, tc.TenantID).Scan(&requirePresence); err != nil {
		return uuid.Nil, false, err
	}
	if !requirePresence {
		return r.assignRoundRobinLegacy(ctx, q, tc.TenantID, conversationID, reason)
	}
	return r.assignRoundRobinPresenceAware(ctx, q, tc.TenantID, conversationID, reason)
}

// assignRoundRobinLegacy is byte-identical to the query IAM4.1 shipped —
// available is queue eligibility, never human presence.
func (r *PostgresAssignmentRepository) assignRoundRobinLegacy(ctx context.Context, q platformdb.Querier, tenantID, conversationID uuid.UUID, reason string) (uuid.UUID, bool, error) {
	var assignedUser *uuid.UUID
	err := q.QueryRow(ctx, `
		WITH target AS (
		  SELECT tenant_id,id,queue_id FROM conversations
		  WHERE tenant_id=$1 AND id=$2 AND assigned_to_user_id IS NULL AND queue_id IS NOT NULL
		  FOR UPDATE
		), candidate AS (
		  SELECT qm.id,qm.user_id
		  FROM queue_members qm JOIN target t ON t.tenant_id=qm.tenant_id AND t.queue_id=qm.queue_id
		  JOIN memberships m ON m.tenant_id=qm.tenant_id AND m.user_id=qm.user_id AND m.status='active'
		  JOIN agent_profiles ap ON ap.tenant_id=m.tenant_id AND ap.membership_id=m.id AND ap.status='active'
		  WHERE qm.active AND qm.available
		    AND (SELECT count(*) FROM conversations active
		         WHERE active.tenant_id=qm.tenant_id AND active.assigned_to_user_id=qm.user_id AND active.status='open') < qm.capacity
		  ORDER BY qm.last_assigned_at ASC NULLS FIRST, qm.user_id
		  FOR UPDATE OF qm SKIP LOCKED LIMIT 1
		), assigned AS (
		  UPDATE conversations c SET assigned_to_user_id=candidate.user_id,assigned_at=now(),updated_at=now()
		  FROM target,candidate WHERE c.tenant_id=target.tenant_id AND c.id=target.id AND c.assigned_to_user_id IS NULL
		  RETURNING c.tenant_id,c.id,candidate.id AS member_id,candidate.user_id
		), touched AS (
		  UPDATE queue_members qm SET last_assigned_at=now() FROM assigned a WHERE qm.id=a.member_id RETURNING a.*
		), recorded AS (
		  INSERT INTO assignment_events(tenant_id,conversation_id,from_user_id,to_user_id,changed_by,reason,actor_source)
		  SELECT tenant_id,id,NULL,user_id,NULL,$3,'system' FROM touched RETURNING to_user_id
		)
		SELECT COALESCE(
		  (SELECT to_user_id FROM recorded),
		  (SELECT assigned_to_user_id FROM conversations WHERE tenant_id=$1 AND id=$2)
		)`, tenantID, conversationID, reason).Scan(&assignedUser)
	if err != nil {
		return uuid.Nil, false, err
	}
	if assignedUser == nil {
		return uuid.Nil, false, nil
	}
	return *assignedUser, true, nil
}

type presenceCandidate struct {
	userID         uuid.UUID
	agentProfileID uuid.UUID
	sortKey        time.Time
}

// assignRoundRobinPresenceAware walks the queue's DB-eligible candidates in
// presenceChunkSize pages, fetched from Postgres one page at a time (never
// the whole queue up front — a queue has no product-imposed size cap, so
// materializing it fully would cost O(queue size) on every routing attempt
// even when the first page already has an online candidate), checking
// Valkey once per page (never once per candidate). The atomic assignment
// itself still uses the same FOR UPDATE OF qm SKIP LOCKED + capacity recheck
// + assignment_events guarantees as IAM4.1 — presence only narrows which
// single candidate that atomic step is attempted against.
func (r *PostgresAssignmentRepository) assignRoundRobinPresenceAware(ctx context.Context, q platformdb.Querier, tenantID, conversationID uuid.UUID, reason string) (uuid.UUID, bool, error) {
	if r.presence == nil {
		return uuid.Nil, false, ports.ErrPresenceUnavailable
	}
	var queueID uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT queue_id FROM conversations
		WHERE tenant_id=$1 AND id=$2 AND assigned_to_user_id IS NULL AND queue_id IS NOT NULL
		FOR UPDATE`, tenantID, conversationID).Scan(&queueID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Not routable (already assigned, no queue, or unknown) — same
		// "return whatever is there" fallback as the legacy path.
		return r.currentAssignee(ctx, q, tenantID, conversationID)
	}
	if err != nil {
		return uuid.Nil, false, err
	}

	// DB-side keyset chunking: the queue's candidate population has no
	// product-imposed cap (scale target explicitly includes 10k+ agents), so
	// fetching it in full up front would cost O(queue size) on every routing
	// attempt even when the very first chunk already contains an online
	// candidate. Never materialize more than one chunk at a time.
	var cursor *presenceCursor
	for {
		chunk, err := r.fetchCandidateChunk(ctx, q, tenantID, queueID, cursor, presenceChunkSize)
		if err != nil {
			return uuid.Nil, false, err
		}
		if len(chunk) == 0 {
			return uuid.Nil, false, nil
		}
		agentProfileIDs := make([]uuid.UUID, len(chunk))
		for i, c := range chunk {
			agentProfileIDs[i] = c.agentProfileID
		}
		online, err := r.presence.OnlineMembers(ctx, tenantID, agentProfileIDs)
		if err != nil {
			return uuid.Nil, false, ports.ErrPresenceUnavailable
		}
		for _, c := range chunk {
			if !online[c.agentProfileID] {
				continue
			}
			assignedUser, assigned, err := r.tryAssign(ctx, q, tenantID, queueID, conversationID, c.userID, reason)
			if err != nil {
				return uuid.Nil, false, err
			}
			if assigned {
				return assignedUser, true, nil
			}
			// Lost the race (capacity/lock) or the conversation was already
			// claimed/assigned by something else concurrently — either way,
			// find out which and act accordingly rather than guessing.
			current, alreadyAssigned, err := r.currentAssignee(ctx, q, tenantID, conversationID)
			if err != nil {
				return uuid.Nil, false, err
			}
			if alreadyAssigned {
				return current, true, nil
			}
			// Otherwise just this candidate lost the race; keep walking.
		}
		if len(chunk) < presenceChunkSize {
			return uuid.Nil, false, nil // fewer rows than requested: DB genuinely exhausted
		}
		last := chunk[len(chunk)-1]
		cursor = &presenceCursor{sortKey: last.sortKey, userID: last.userID}
	}
}

// presenceCursor is the keyset position after the last row of the previous
// chunk, in the same (sortKey, userID) order the query itself uses.
type presenceCursor struct {
	sortKey time.Time
	userID  uuid.UUID
}

// fetchCandidateChunk returns up to limit DB-eligible candidates strictly
// after cursor, in fairness order (last_assigned_at ASC NULLS FIRST, then
// user_id). sortKey is COALESCE(last_assigned_at, '-infinity') so the keyset
// comparison never has to reason about NULLs directly — Postgres's row
// comparison (sortKey, user_id) > (cursor.sortKey, cursor.userID) is exact
// and total, so no candidate is ever skipped or repeated across pages.
func (r *PostgresAssignmentRepository) fetchCandidateChunk(ctx context.Context, q platformdb.Querier, tenantID, queueID uuid.UUID, cursor *presenceCursor, limit int) ([]presenceCandidate, error) {
	var cursorSortKey *time.Time
	var cursorUserID *uuid.UUID
	if cursor != nil {
		cursorSortKey = &cursor.sortKey
		cursorUserID = &cursor.userID
	}
	rows, err := q.Query(ctx, `
		SELECT qm.user_id, ap.id, COALESCE(qm.last_assigned_at, 'epoch'::timestamptz) AS sort_key
		FROM queue_members qm
		JOIN memberships m ON m.tenant_id=qm.tenant_id AND m.user_id=qm.user_id AND m.status='active'
		JOIN agent_profiles ap ON ap.tenant_id=m.tenant_id AND ap.membership_id=m.id AND ap.status='active'
		WHERE qm.tenant_id=$1 AND qm.queue_id=$2 AND qm.active AND qm.available
		  AND (SELECT count(*) FROM conversations active
		       WHERE active.tenant_id=qm.tenant_id AND active.assigned_to_user_id=qm.user_id AND active.status='open') < qm.capacity
		  AND ($3::timestamptz IS NULL OR
		       (COALESCE(qm.last_assigned_at, 'epoch'::timestamptz), qm.user_id) > ($3::timestamptz, $4::uuid))
		ORDER BY sort_key ASC, qm.user_id ASC
		LIMIT $5`, tenantID, queueID, cursorSortKey, cursorUserID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []presenceCandidate
	for rows.Next() {
		var c presenceCandidate
		if err := rows.Scan(&c.userID, &c.agentProfileID, &c.sortKey); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// tryAssign is the same atomic assign/touch/record chain as the legacy
// query, constrained to one specific candidate.
func (r *PostgresAssignmentRepository) tryAssign(ctx context.Context, q platformdb.Querier, tenantID, queueID, conversationID, candidateUserID uuid.UUID, reason string) (uuid.UUID, bool, error) {
	var assignedUser *uuid.UUID
	err := q.QueryRow(ctx, `
		WITH candidate AS (
		  SELECT qm.id,qm.user_id
		  FROM queue_members qm
		  WHERE qm.tenant_id=$1 AND qm.queue_id=$2 AND qm.user_id=$5 AND qm.active AND qm.available
		    AND (SELECT count(*) FROM conversations active
		         WHERE active.tenant_id=qm.tenant_id AND active.assigned_to_user_id=qm.user_id AND active.status='open') < qm.capacity
		  FOR UPDATE OF qm SKIP LOCKED
		), assigned AS (
		  UPDATE conversations c SET assigned_to_user_id=candidate.user_id,assigned_at=now(),updated_at=now()
		  FROM candidate WHERE c.tenant_id=$1 AND c.id=$4 AND c.assigned_to_user_id IS NULL
		  RETURNING c.tenant_id,c.id,candidate.id AS member_id,candidate.user_id
		), touched AS (
		  UPDATE queue_members qm SET last_assigned_at=now() FROM assigned a WHERE qm.id=a.member_id RETURNING a.*
		), recorded AS (
		  INSERT INTO assignment_events(tenant_id,conversation_id,from_user_id,to_user_id,changed_by,reason,actor_source)
		  SELECT tenant_id,id,NULL,user_id,NULL,$3,'system' FROM touched RETURNING to_user_id
		)
		SELECT to_user_id FROM recorded`, tenantID, queueID, reason, conversationID, candidateUserID).Scan(&assignedUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	if assignedUser == nil {
		return uuid.Nil, false, nil
	}
	return *assignedUser, true, nil
}

func (r *PostgresAssignmentRepository) currentAssignee(ctx context.Context, q platformdb.Querier, tenantID, conversationID uuid.UUID) (uuid.UUID, bool, error) {
	var assignedUser *uuid.UUID
	err := q.QueryRow(ctx, `SELECT assigned_to_user_id FROM conversations WHERE tenant_id=$1 AND id=$2`, tenantID, conversationID).Scan(&assignedUser)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, err
	}
	if assignedUser == nil {
		return uuid.Nil, false, nil
	}
	return *assignedUser, true, nil
}
