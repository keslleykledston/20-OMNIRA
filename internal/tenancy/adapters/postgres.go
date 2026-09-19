package adapters

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/tenancy/ports"
)

// PostgresTenantRepository — implementação PostgreSQL de TenantRepository.
type PostgresTenantRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresTenantRepository — cria um novo PostgresTenantRepository.
func NewPostgresTenantRepository(pool *pgxpool.Pool) ports.TenantRepository {
	return &PostgresTenantRepository{pool: pool}
}

func (r *PostgresTenantRepository) Store(ctx context.Context, tenant *domain.Tenant) error {
	const query = `
		INSERT INTO tenants (id, legal_name, trade_name, tax_id, isolation_profile, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO NOTHING
	`
	_, err := db.QuerierFromContext(ctx, r.pool).Exec(ctx, query,
		tenant.ID,
		tenant.LegalName,
		tenant.TradeName,
		tenant.TaxID,
		string(tenant.IsolationProfile),
		string(tenant.Status),
		tenant.CreatedAt,
		tenant.UpdatedAt,
	)
	return err
}

func (r *PostgresTenantRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Tenant, error) {
	const query = `
		SELECT id, legal_name, trade_name, tax_id, isolation_profile, status, created_at, updated_at
		FROM tenants
		WHERE id = $1
	`
	row := db.QuerierFromContext(ctx, r.pool).QueryRow(ctx, query, id)
	tenant := &domain.Tenant{}
	err := row.Scan(
		&tenant.ID,
		&tenant.LegalName,
		&tenant.TradeName,
		&tenant.TaxID,
		(*string)(&tenant.IsolationProfile),
		(*string)(&tenant.Status),
		&tenant.CreatedAt,
		&tenant.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return tenant, err
}

func (r *PostgresTenantRepository) FindAll(ctx context.Context, limit, offset int) ([]*domain.Tenant, error) {
	const query = `
		SELECT id, legal_name, trade_name, tax_id, isolation_profile, status, created_at, updated_at
		FROM tenants
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`
	rows, err := db.QuerierFromContext(ctx, r.pool).Query(ctx, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tenants []*domain.Tenant
	for rows.Next() {
		tenant := &domain.Tenant{}
		err := rows.Scan(
			&tenant.ID,
			&tenant.LegalName,
			&tenant.TradeName,
			&tenant.TaxID,
			(*string)(&tenant.IsolationProfile),
			(*string)(&tenant.Status),
			&tenant.CreatedAt,
			&tenant.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		tenants = append(tenants, tenant)
	}
	return tenants, rows.Err()
}

func (r *PostgresTenantRepository) Update(ctx context.Context, tenant *domain.Tenant) error {
	const query = `
		UPDATE tenants
		SET legal_name = $1, trade_name = $2, tax_id = $3, isolation_profile = $4, status = $5, updated_at = $6
		WHERE id = $7
	`
	_, err := db.QuerierFromContext(ctx, r.pool).Exec(ctx, query,
		tenant.LegalName,
		tenant.TradeName,
		tenant.TaxID,
		string(tenant.IsolationProfile),
		string(tenant.Status),
		tenant.UpdatedAt,
		tenant.ID,
	)
	return err
}

// PostgresMembershipRepository — implementação PostgreSQL de MembershipRepository.
type PostgresMembershipRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresMembershipRepository — cria um novo PostgresMembershipRepository.
func NewPostgresMembershipRepository(pool *pgxpool.Pool) ports.MembershipRepository {
	return &PostgresMembershipRepository{pool: pool}
}

func (r *PostgresMembershipRepository) Store(ctx context.Context, membership *domain.Membership) error {
	const query = `
		INSERT INTO memberships (id, tenant_id, user_id, role_id, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (id) DO NOTHING
	`
	_, err := db.QuerierFromContext(ctx, r.pool).Exec(ctx, query,
		membership.ID,
		membership.TenantID,
		membership.UserID,
		membership.RoleID,
		string(membership.Status),
		membership.CreatedAt,
		membership.UpdatedAt,
	)
	return err
}

func (r *PostgresMembershipRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Membership, error) {
	const query = `
		SELECT id, tenant_id, user_id, role_id, status, created_at, updated_at
		FROM memberships
		WHERE id = $1
	`
	row := db.QuerierFromContext(ctx, r.pool).QueryRow(ctx, query, id)
	membership := &domain.Membership{}
	err := row.Scan(
		&membership.ID,
		&membership.TenantID,
		&membership.UserID,
		&membership.RoleID,
		(*string)(&membership.Status),
		&membership.CreatedAt,
		&membership.UpdatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return membership, err
}

func (r *PostgresMembershipRepository) FindByTenantAndUser(ctx context.Context, tenantID, userID uuid.UUID) ([]*domain.Membership, error) {
	const query = `
		SELECT id, tenant_id, user_id, role_id, status, created_at, updated_at
		FROM memberships
		WHERE tenant_id = $1 AND user_id = $2
	`
	rows, err := db.QuerierFromContext(ctx, r.pool).Query(ctx, query, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memberships []*domain.Membership
	for rows.Next() {
		membership := &domain.Membership{}
		err := rows.Scan(
			&membership.ID,
			&membership.TenantID,
			&membership.UserID,
			&membership.RoleID,
			(*string)(&membership.Status),
			&membership.CreatedAt,
			&membership.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		memberships = append(memberships, membership)
	}
	return memberships, rows.Err()
}

func (r *PostgresMembershipRepository) FindByUser(ctx context.Context, userID uuid.UUID) ([]*domain.Membership, error) {
	const query = `
		SELECT id, tenant_id, user_id, role_id, status, created_at, updated_at
		FROM memberships
		WHERE user_id = $1 AND status = 'active'
	`
	rows, err := db.QuerierFromContext(ctx, r.pool).Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memberships []*domain.Membership
	for rows.Next() {
		membership := &domain.Membership{}
		err := rows.Scan(
			&membership.ID,
			&membership.TenantID,
			&membership.UserID,
			&membership.RoleID,
			(*string)(&membership.Status),
			&membership.CreatedAt,
			&membership.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		memberships = append(memberships, membership)
	}
	return memberships, rows.Err()
}

func (r *PostgresMembershipRepository) FindByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.Membership, error) {
	const query = `
		SELECT id, tenant_id, user_id, role_id, status, created_at, updated_at
		FROM memberships
		WHERE tenant_id = $1
	`
	rows, err := db.QuerierFromContext(ctx, r.pool).Query(ctx, query, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memberships []*domain.Membership
	for rows.Next() {
		membership := &domain.Membership{}
		err := rows.Scan(
			&membership.ID,
			&membership.TenantID,
			&membership.UserID,
			&membership.RoleID,
			(*string)(&membership.Status),
			&membership.CreatedAt,
			&membership.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		memberships = append(memberships, membership)
	}
	return memberships, rows.Err()
}

func (r *PostgresMembershipRepository) Update(ctx context.Context, membership *domain.Membership) error {
	const query = `
		UPDATE memberships
		SET status = $1, updated_at = $2
		WHERE id = $3
	`
	_, err := db.QuerierFromContext(ctx, r.pool).Exec(ctx, query, string(membership.Status), membership.UpdatedAt, membership.ID)
	return err
}

func (r *PostgresMembershipRepository) Delete(ctx context.Context, id uuid.UUID) error {
	const query = `DELETE FROM memberships WHERE id = $1`
	_, err := db.QuerierFromContext(ctx, r.pool).Exec(ctx, query, id)
	return err
}

// PostgresRoleRepository — implementação PostgreSQL de RoleRepository.
type PostgresRoleRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRoleRepository — cria um novo PostgresRoleRepository.
func NewPostgresRoleRepository(pool *pgxpool.Pool) ports.RoleRepository {
	return &PostgresRoleRepository{pool: pool}
}

func (r *PostgresRoleRepository) FindByKey(ctx context.Context, key string) (*domain.Role, error) {
	const query = `SELECT id, tenant_id, key, name FROM roles WHERE key = $1 AND tenant_id IS NULL LIMIT 1`
	row := db.QuerierFromContext(ctx, r.pool).QueryRow(ctx, query, key)
	role := &domain.Role{}
	err := row.Scan(&role.ID, &role.TenantID, &role.Key, &role.Name)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return role, err
}

func (r *PostgresRoleRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Role, error) {
	const query = `SELECT id, tenant_id, key, name FROM roles WHERE id = $1`
	row := db.QuerierFromContext(ctx, r.pool).QueryRow(ctx, query, id)
	role := &domain.Role{}
	err := row.Scan(&role.ID, &role.TenantID, &role.Key, &role.Name)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return role, err
}

func (r *PostgresRoleRepository) FindAll(ctx context.Context) ([]*domain.Role, error) {
	const query = `SELECT id, tenant_id, key, name FROM roles ORDER BY name`
	rows, err := db.QuerierFromContext(ctx, r.pool).Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []*domain.Role
	for rows.Next() {
		role := &domain.Role{}
		if err := rows.Scan(&role.ID, &role.TenantID, &role.Key, &role.Name); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}
