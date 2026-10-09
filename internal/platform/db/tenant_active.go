package db

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LockTenantActive reports whether the company is active AND keeps it that way until the transaction ends: it takes a
// share lock on the tenant row, which conflicts with the UPDATE that suspends the company. So a background write that
// asked first either finishes before the suspension commits, or finds the company suspended and does nothing; there is
// no window where something is written after the suspension became visible (ADR-0038: a suspended company is not served).
//
// Call it first inside a tenant/system session, before any write. A company that does not exist counts as not active.
func LockTenantActive(ctx context.Context, q Querier, tenantID uuid.UUID) (bool, error) {
	var active bool
	err := q.QueryRow(ctx, `SELECT status = 'active' FROM tenants WHERE id = $1 FOR SHARE`, tenantID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return active, err
}
