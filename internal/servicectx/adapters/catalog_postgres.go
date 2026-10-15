package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

var _ ports.CatalogRepository = (*PostgresRepository)(nil)

// duplicate says a unique violation (the company already has that code).
func duplicate(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

func (r *PostgresRepository) ListCategories(ctx context.Context, includeInactive bool) ([]ports.Category, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT id, code, label, active, created_at, updated_at FROM service_categories
		WHERE tenant_id=$1 AND ($2 OR active) ORDER BY label, id`, tenant, includeInactive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ports.Category
	for rows.Next() {
		var c ports.Category
		if err := rows.Scan(&c.ID, &c.Code, &c.Label, &c.Active, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CreateCategory(ctx context.Context, c *ports.Category) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	// a savepoint-free existence check: the request transaction must survive a duplicate (the tenant middleware commits even on a 4xx)
	var dup bool
	if err := r.q(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM service_categories WHERE tenant_id=$1 AND code=$2)`, tenant, c.Code).Scan(&dup); err != nil {
		return err
	}
	if dup {
		return domain.Invalid("a category with this code already exists")
	}
	_, err = r.q(ctx).Exec(ctx, `INSERT INTO service_categories (id, tenant_id, code, label, active, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		c.ID, tenant, c.Code, c.Label, c.Active, c.CreatedAt, c.UpdatedAt)
	return mapErr(err)
}

func (r *PostgresRepository) UpdateCategory(ctx context.Context, id uuid.UUID, label *string, active *bool) (*ports.Category, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var c ports.Category
	err = r.q(ctx).QueryRow(ctx, `UPDATE service_categories SET label=COALESCE($3,label), active=COALESCE($4,active), updated_at=now()
		WHERE tenant_id=$1 AND id=$2 RETURNING id, code, label, active, created_at, updated_at`, tenant, id, label, active).
		Scan(&c.ID, &c.Code, &c.Label, &c.Active, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return &c, mapErr(err)
}

func (r *PostgresRepository) ListResultCodes(ctx context.Context, includeInactive bool) ([]ports.ResultCode, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.q(ctx).Query(ctx, `SELECT id, code, canonical_code, label, active, created_at, updated_at FROM service_resolution_codes
		WHERE tenant_id=$1 AND ($2 OR active) ORDER BY label, id`, tenant, includeInactive)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ports.ResultCode
	for rows.Next() {
		var c ports.ResultCode
		if err := rows.Scan(&c.ID, &c.Code, &c.CanonicalCode, &c.Label, &c.Active, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) CreateResultCode(ctx context.Context, c *ports.ResultCode) error {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	var dup bool
	if err := r.q(ctx).QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM service_resolution_codes WHERE tenant_id=$1 AND code=$2)`, tenant, c.Code).Scan(&dup); err != nil {
		return err
	}
	if dup {
		return domain.Invalid("a result code with this code already exists")
	}
	_, err = r.q(ctx).Exec(ctx, `INSERT INTO service_resolution_codes (id, tenant_id, code, canonical_code, label, active, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		c.ID, tenant, c.Code, c.CanonicalCode, c.Label, c.Active, c.CreatedAt, c.UpdatedAt)
	return mapErr(err)
}

func (r *PostgresRepository) UpdateResultCode(ctx context.Context, id uuid.UUID, label *string, active *bool) (*ports.ResultCode, error) {
	tenant, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	var c ports.ResultCode
	err = r.q(ctx).QueryRow(ctx, `UPDATE service_resolution_codes SET label=COALESCE($3,label), active=COALESCE($4,active), updated_at=now()
		WHERE tenant_id=$1 AND id=$2 RETURNING id, code, canonical_code, label, active, created_at, updated_at`, tenant, id, label, active).
		Scan(&c.ID, &c.Code, &c.CanonicalCode, &c.Label, &c.Active, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	return &c, mapErr(err)
}
