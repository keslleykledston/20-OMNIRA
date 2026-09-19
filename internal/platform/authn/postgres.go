package authn

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

type PostgresIdentityResolver struct{ pool *pgxpool.Pool }

func NewPostgresIdentityResolver(pool *pgxpool.Pool) *PostgresIdentityResolver {
	return &PostgresIdentityResolver{pool: pool}
}

func (r *PostgresIdentityResolver) ResolveUserID(ctx context.Context, subject string) (uuid.UUID, error) {
	var id uuid.UUID
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(scoped context.Context) error {
		return platformdb.QuerierFromContext(scoped, r.pool).QueryRow(scoped,
			`SELECT id FROM users WHERE external_subject=$1 AND status='active'`, subject).Scan(&id)
	})
	if err == pgx.ErrNoRows {
		return uuid.Nil, errors.New("identity not provisioned")
	}
	return id, err
}

func (r *PostgresIdentityResolver) SessionProfile(ctx context.Context, userID uuid.UUID) (SessionProfile, error) {
	var profile SessionProfile
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(scoped context.Context) error {
		return platformdb.QuerierFromContext(scoped, r.pool).QueryRow(scoped, `
		SELECT u.id::text, COALESCE(u.email,''), COALESCE(u.display_name,''), t.id::text, t.legal_name
		FROM users u
		JOIN memberships m ON m.user_id=u.id AND m.status='active'
		JOIN tenants t ON t.id=m.tenant_id AND t.status='active'
		WHERE u.id=$1 AND u.status='active'
		ORDER BY m.created_at, t.id
		LIMIT 1`, userID).Scan(&profile.User.ID, &profile.User.Email, &profile.User.Name, &profile.Tenant.ID, &profile.Tenant.Name)
	})
	return profile, err
}
