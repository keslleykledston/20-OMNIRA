package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

type PostgresAssignmentRepository struct{ pool *pgxpool.Pool }

var _ ports.AssignmentRepository = (*PostgresAssignmentRepository)(nil)

func NewPostgresAssignmentRepository(pool *pgxpool.Pool) *PostgresAssignmentRepository {
	return &PostgresAssignmentRepository{pool: pool}
}

// ClaimUnassigned uses one SQL statement. Concurrent callers cannot both
// update the NULL owner, and history is inserted only for the winning update.
func (r *PostgresAssignmentRepository) ClaimUnassigned(ctx context.Context, conversationID, userID uuid.UUID, reason string) (bool, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || userID == uuid.Nil || tc.ActorID != userID {
		return false, errors.New("routing: tenant actor mismatch")
	}
	var claimed bool
	err = platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, `
		WITH claimed AS (
		  UPDATE conversations
		  SET assigned_to_user_id=$3, assigned_at=now(), updated_at=now()
		  WHERE tenant_id=$1 AND id=$2 AND assigned_to_user_id IS NULL
		  RETURNING tenant_id, id
		), recorded AS (
		  INSERT INTO assignment_events(tenant_id,conversation_id,from_user_id,to_user_id,changed_by,reason)
		  SELECT tenant_id,id,NULL,$3,$3,$4 FROM claimed
		  RETURNING id
		)
		SELECT EXISTS(SELECT 1 FROM recorded)`, tc.TenantID, conversationID, userID, reason).Scan(&claimed)
	return claimed, err
}

func (r *PostgresAssignmentRepository) AssignRoundRobin(ctx context.Context, conversationID uuid.UUID, reason string) (uuid.UUID, bool, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.Source != tenancydomain.AccessSourceSystem {
		return uuid.Nil, false, errors.New("routing: system tenant context required")
	}
	var assignedUser *uuid.UUID
	err = platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, `
		WITH target AS (
		  SELECT tenant_id,id,queue_id FROM conversations
		  WHERE tenant_id=$1 AND id=$2 AND assigned_to_user_id IS NULL AND queue_id IS NOT NULL
		  FOR UPDATE
		), candidate AS (
		  SELECT qm.id,qm.user_id
		  FROM queue_members qm JOIN target t ON t.tenant_id=qm.tenant_id AND t.queue_id=qm.queue_id
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
		SELECT (SELECT to_user_id FROM recorded)`, tc.TenantID, conversationID, reason).Scan(&assignedUser)
	if err != nil {
		return uuid.Nil, false, err
	}
	if assignedUser == nil {
		return uuid.Nil, false, nil
	}
	return *assignedUser, true, nil
}
