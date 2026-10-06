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

// users.external_subject predates the multi-IdP model of migration 000028 and
// is UNIQUE, so storing a bare subject collides when two issuers emit the same
// one. It is legacy compatibility only — user_identities is the canonical
// identity for authentication.
func legacyExternalSubject(issuer, subject string) string { return issuer + "|" + subject }

// ResolveUserID maps a validated OIDC identity to its user. The identity is
// (issuer, subject) — two IdPs may issue the same subject for different people,
// so the subject alone is not an identity.
//
// It runs on every authenticated request, so unlike ResolveIdentity it writes
// nothing: touching last_login_at here would mean a write per request.
func (r *PostgresIdentityResolver) ResolveUserID(ctx context.Context, issuer, subject string) (uuid.UUID, error) {
	var id uuid.UUID
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(scoped context.Context) error {
		return platformdb.QuerierFromContext(scoped, r.pool).QueryRow(scoped, `
			SELECT u.id FROM user_identities i
			JOIN users u ON u.id = i.user_id
			WHERE i.issuer=$1 AND i.subject=$2 AND u.status='active'`, issuer, subject).Scan(&id)
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
		SELECT u.id::text, COALESCE(u.email,''), COALESCE(u.display_name,''), t.id::text, t.legal_name, u.password_expires_at
		FROM users u
		JOIN memberships m ON m.user_id=u.id AND m.status='active'
		JOIN tenants t ON t.id=m.tenant_id AND t.status='active'
		WHERE u.id=$1 AND u.status='active'
		ORDER BY m.created_at, t.id
		LIMIT 1`, userID).Scan(&profile.User.ID, &profile.User.Email, &profile.User.Name, &profile.Tenant.ID, &profile.Tenant.Name, &profile.User.PasswordExpiresAt)
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
func (r *PostgresIdentityResolver) ProvisionIdentity(ctx context.Context, issuer, subject, email, displayName string, emailVerified bool) (uuid.UUID, error) {
	var userID uuid.UUID
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(scoped context.Context) error {
		q := platformdb.QuerierFromContext(scoped, r.pool)
		if err := q.QueryRow(scoped, `
			SELECT user_id FROM user_identities WHERE issuer=$1 AND subject=$2
		`, issuer, subject).Scan(&userID); err == nil {
			if _, err := q.Exec(scoped, `
				UPDATE user_identities
				SET email=$3, display_name=$4, email_verified=$5, last_login_at=NOW()
				WHERE issuer=$1 AND subject=$2
			`, issuer, subject, email, displayName, emailVerified); err != nil {
				return err
			}
			// Os atributos vêm do IdP a cada login e users é o que o resto da
			// aplicação lê (SessionProfile, lista de agentes), então a linha de
			// users acompanha a identidade em vez de congelar no primeiro login.
			if _, err := q.Exec(scoped, `
				UPDATE users SET email=$2, display_name=$3, updated_at=NOW() WHERE id=$1
			`, userID, email, displayName); err != nil {
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
		`, userID, legacyExternalSubject(issuer, subject), email, displayName); err != nil {
			return err
		}
		if _, err := q.Exec(scoped, `
			INSERT INTO user_identities(user_id, issuer, subject, email, display_name, email_verified, last_login_at)
			VALUES ($1, $2, $3, $4, $5, $6, NOW())
		`, userID, issuer, subject, email, displayName, emailVerified); err != nil {
			return err
		}
		return nil
	})
	return userID, err
}
