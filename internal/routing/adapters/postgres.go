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
