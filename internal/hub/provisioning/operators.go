package provisioning

import (
	"context"
	"time"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Platform operators (ADR-0038): the humans allowed to create companies and Hubs and to switch them on and off.
// The only way to create or remove one is this service (omnira-hubctl platform-operator ...): there is deliberately
// no HTTP route, so nobody can promote themselves.

type PlatformOperator struct {
	UserID    uuid.UUID
	Email     string
	Status    string
	GrantedBy string
	CreatedAt time.Time
}

// AddPlatformOperator makes an active user a platform operator (or re-activates a revoked one). Idempotent.
func (s *Service) AddPlatformOperator(ctx context.Context, user uuid.UUID) error {
	return s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := requireActiveUser(c, q, user); err != nil {
			return err
		}
		var prev *string
		if err := q.QueryRow(c, `SELECT (SELECT status FROM platform_operators WHERE user_id = $1 FOR UPDATE)`, user).Scan(&prev); err != nil {
			return err
		}
		if prev != nil && *prev == "active" {
			return nil // idempotent: nothing changes, nothing to audit
		}
		if _, err := q.Exec(c, `INSERT INTO platform_operators (user_id, granted_by) VALUES ($1, $2)
		                        ON CONFLICT (user_id) DO UPDATE SET status = 'active', revoked_at = NULL, granted_by = EXCLUDED.granted_by, updated_at = now()`,
			user, s.operator); err != nil {
			return err
		}
		return s.audit(c, q, nil, "platform.operator.added", "platform_operator", user, map[string]any{"user_id": user, "reactivated": prev != nil})
	})
}

// RevokePlatformOperator removes the capability. The row is kept (status=revoked) as the record of who had it.
func (s *Service) RevokePlatformOperator(ctx context.Context, user uuid.UUID) error {
	return s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		var status string
		if err := q.QueryRow(c, `SELECT status FROM platform_operators WHERE user_id = $1 FOR UPDATE`, user).Scan(&status); err != nil {
			return mapNoRows(err, "user %s is not a platform operator", user)
		}
		if status == "revoked" {
			return nil
		}
		if _, err := q.Exec(c, `UPDATE platform_operators SET status = 'revoked', revoked_at = now(), updated_at = now() WHERE user_id = $1`, user); err != nil {
			return err
		}
		return s.audit(c, q, nil, "platform.operator.revoked", "platform_operator", user, map[string]any{"user_id": user})
	})
}

func (s *Service) ListPlatformOperators(ctx context.Context) ([]PlatformOperator, error) {
	var out []PlatformOperator
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		rows, err := q.Query(c, `SELECT o.user_id, COALESCE(u.email, ''), o.status, o.granted_by, o.created_at
		                         FROM platform_operators o JOIN users u ON u.id = o.user_id ORDER BY o.created_at`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o PlatformOperator
			if err := rows.Scan(&o.UserID, &o.Email, &o.Status, &o.GrantedBy, &o.CreatedAt); err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}

// IsPlatformOperator asks the database whether the SESSION user is an active operator. Call it with the request's own
// RLS-scoped querier: the SQL function answers only for the session user, so it cannot be used to probe other people.
func IsPlatformOperator(ctx context.Context, q platformdb.Querier, user uuid.UUID) (bool, error) {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT is_platform_operator($1)`, user).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}
