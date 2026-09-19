package authn

import (
	"context"
	"errors"
	"time"

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

// ResolveIdentity reconciles issuer+subject to an active user, and updates last_login_at.
// Returns error if identity is not found or user is inactive.
func (r *PostgresIdentityResolver) ResolveIdentity(ctx context.Context, issuer, subject string) (uuid.UUID, error) {
	var userID uuid.UUID
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(scoped context.Context) error {
		q := platformdb.QuerierFromContext(scoped, r.pool)
		if err := q.QueryRow(scoped, `
			SELECT user_id FROM user_identities
			WHERE issuer=$1 AND subject=$2
		`, issuer, subject).Scan(&userID); err == pgx.ErrNoRows {
			return errors.New("identity not found")
		} else if err != nil {
			return err
		}
		if _, err := q.Exec(scoped, `
			UPDATE user_identities SET last_login_at=NOW()
			WHERE issuer=$1 AND subject=$2
		`, issuer, subject); err != nil {
			return err
		}
		var status string
		if err := q.QueryRow(scoped, `SELECT status FROM users WHERE id=$1`, userID).Scan(&status); err != nil {
			return err
		}
		if status != "active" {
			return errors.New("user is not active")
		}
		return nil
	})
	return userID, err
}

// ProvisionIdentity creates or updates a UserIdentity, and returns the user ID.
// Idempotent: same issuer+subject always maps to the same user.
// Email/display_name are attributes and are updated on each login.
func (r *PostgresIdentityResolver) ProvisionIdentity(ctx context.Context, issuer, subject, email, displayName string) (uuid.UUID, error) {
	var userID uuid.UUID
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(scoped context.Context) error {
		q := platformdb.QuerierFromContext(scoped, r.pool)
		if err := q.QueryRow(scoped, `
			SELECT user_id FROM user_identities WHERE issuer=$1 AND subject=$2
		`, issuer, subject).Scan(&userID); err == nil {
			if _, err := q.Exec(scoped, `
				UPDATE user_identities
				SET email=$3, display_name=$4, last_login_at=NOW()
				WHERE issuer=$1 AND subject=$2
			`, issuer, subject, email, displayName); err != nil {
				return err
			}
			return nil
		} else if err != pgx.ErrNoRows {
			return err
		}
		userID = uuid.New()
		if _, err := q.Exec(scoped, `
			INSERT INTO users(id, external_subject, email, display_name, status)
			VALUES ($1, $2, $3, $4, 'active')
		`, userID, subject, email, displayName); err != nil {
			return err
		}
		if _, err := q.Exec(scoped, `
			INSERT INTO user_identities(user_id, issuer, subject, email, display_name, last_login_at)
			VALUES ($1, $2, $3, $4, $5, NOW())
		`, userID, issuer, subject, email, displayName); err != nil {
			return err
		}
		return nil
	})
	return userID, err
}
