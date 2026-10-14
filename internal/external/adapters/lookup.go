package adapters

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/external/application"
	"github.com/omnira/omnira/internal/external/domain"
)

// PostgresLookup reads a credential through the SECURITY DEFINER function external_credential_lookup (migration 135): the application role
// has no SELECT on the digest column, and this is the only lookup that happens before the tenant is known.
type PostgresLookup struct {
	pool    *pgxpool.Pool
	mu      sync.Mutex
	touched map[uuid.UUID]time.Time
}

func NewPostgresLookup(pool *pgxpool.Pool) *PostgresLookup {
	return &PostgresLookup{pool: pool, touched: map[uuid.UUID]time.Time{}}
}

func (l *PostgresLookup) Lookup(ctx context.Context, keyID string) (*application.CredentialRecord, error) {
	var rec application.CredentialRecord
	var status string
	err := l.pool.QueryRow(ctx, `
		SELECT credential_id, installation_id, tenant_id, secret_digest, status, scopes, rate_limit_per_min, lease_seconds, allow_outside_hours, installation_name
		FROM external_credential_lookup($1)`, keyID).
		Scan(&rec.CredentialID, &rec.InstallationID, &rec.TenantID, &rec.Digest, &status, &rec.Scopes, &rec.RatePerMin, &rec.LeaseSeconds, &rec.AllowOutsideHours, &rec.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if status != "active" { // the function already filters; a second look costs nothing and survives a future edit of the function
		return nil, nil
	}
	return &rec, nil
}

// Touch records use of a credential at most once a minute per process; failure is ignored (bookkeeping never fails a request).
func (l *PostgresLookup) Touch(ctx context.Context, p *domain.Principal) {
	credentialID := p.CredentialID
	now := time.Now()
	l.mu.Lock()
	if last, ok := l.touched[credentialID]; ok && now.Sub(last) < time.Minute {
		l.mu.Unlock()
		return
	}
	if len(l.touched) > 4096 {
		l.touched = map[uuid.UUID]time.Time{}
	}
	l.touched[credentialID] = now
	l.mu.Unlock()
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = l.pool.Exec(tctx, `SELECT external_credential_touch($1, $2, $3)`, p.TenantID, p.InstallationID, p.CredentialID)
}
