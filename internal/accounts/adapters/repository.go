package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/accounts/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresRepository runs inside the caller's tenant session (RLS); every statement also filters by tenant_id.
type PostgresRepository struct{ pool *pgxpool.Pool }

func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) q(ctx context.Context) platformdb.Querier {
	return platformdb.QuerierFromContext(ctx, r.pool)
}

const accountColumns = `id, tenant_id, name, account_type, status, metadata, created_at, updated_at, archived_at`

func scanAccount(row pgx.Row) (*domain.Account, error) {
	var a domain.Account
	var typ, status string
	if err := row.Scan(&a.ID, &a.TenantID, &a.Name, &typ, &status, &a.Metadata, &a.CreatedAt, &a.UpdatedAt, &a.ArchivedAt); err != nil {
		return nil, err
	}
	a.Type, a.Status = domain.AccountType(typ), domain.AccountStatus(status)
	return &a, nil
}

func (r *PostgresRepository) CreateAccount(ctx context.Context, tenantID uuid.UUID, name string, typ domain.AccountType) (*domain.Account, error) {
	n, err := domain.NormalizeName(name)
	if err != nil || !typ.Valid() {
		return nil, domain.ErrInvalidAccount
	}
	return scanAccount(r.q(ctx).QueryRow(ctx, `INSERT INTO customer_accounts (tenant_id, name, account_type) VALUES ($1,$2,$3) RETURNING `+accountColumns, tenantID, n, string(typ)))
}

func (r *PostgresRepository) GetAccount(ctx context.Context, tenantID, id uuid.UUID) (*domain.Account, error) {
	a, err := scanAccount(r.q(ctx).QueryRow(ctx, `SELECT `+accountColumns+` FROM customer_accounts WHERE tenant_id=$1 AND id=$2`, tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAccountNotFound
	}
	return a, err
}

type ListFilter struct {
	Query  string
	Status *domain.AccountStatus
	Limit  int
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ListAccounts: archived accounts are left out unless asked for by status.
func (r *PostgresRepository) ListAccounts(ctx context.Context, tenantID uuid.UUID, f ListFilter) ([]domain.Account, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	args := []any{tenantID}
	where := "tenant_id=$1"
	if f.Status != nil {
		args = append(args, string(*f.Status))
		where += fmt.Sprintf(" AND status=$%d", len(args))
	} else {
		where += " AND status <> 'archived'"
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+escapeLike(strings.ToLower(q))+"%")
		where += fmt.Sprintf(" AND lower(name) LIKE $%d", len(args))
	}
	args = append(args, f.Limit)
	rows, err := r.q(ctx).Query(ctx, `SELECT `+accountColumns+` FROM customer_accounts WHERE `+where+fmt.Sprintf(` ORDER BY lower(name), id LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// UpdateAccount changes name, type and/or status. Archiving sets archived_at; reactivating clears it.
func (r *PostgresRepository) UpdateAccount(ctx context.Context, tenantID, id uuid.UUID, name *string, typ *domain.AccountType, status *domain.AccountStatus) (*domain.Account, error) {
	cur, err := r.GetAccount(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if name != nil {
		n, err := domain.NormalizeName(*name)
		if err != nil {
			return nil, err
		}
		cur.Name = n
	}
	if typ != nil {
		if !typ.Valid() {
			return nil, domain.ErrInvalidAccount
		}
		cur.Type = *typ
	}
	if status != nil {
		if !status.Valid() {
			return nil, domain.ErrInvalidAccount
		}
		cur.Status = *status
	}
	a, err := scanAccount(r.q(ctx).QueryRow(ctx, `
		UPDATE customer_accounts SET name=$3, account_type=$4, status=$5, updated_at=now(),
		       archived_at = CASE WHEN $5='archived' THEN COALESCE(archived_at, now()) ELSE NULL END
		WHERE tenant_id=$1 AND id=$2 RETURNING `+accountColumns, tenantID, id, cur.Name, string(cur.Type), string(cur.Status)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrAccountNotFound
	}
	return a, err
}

const linkColumns = `id, tenant_id, account_id, provider, connection_id, external_company_id, external_name_snapshot, status, source, verified_at, created_at, updated_at`

func scanLink(row pgx.Row) (*domain.ExternalLink, error) {
	var l domain.ExternalLink
	var status, source string
	if err := row.Scan(&l.ID, &l.TenantID, &l.AccountID, &l.Provider, &l.ConnectionID, &l.ExternalCompanyID, &l.ExternalNameSnapshot, &status, &source, &l.VerifiedAt, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	l.Status, l.Source = domain.LinkStatus(status), domain.LinkSource(source)
	return &l, nil
}

// UpsertExternalLink is idempotent: linking the same provider company to the same account again returns the existing
// link (and refreshes the name snapshot); linking it to a DIFFERENT account is domain.ErrExternalLinkTaken.
func (r *PostgresRepository) UpsertExternalLink(ctx context.Context, l domain.ExternalLink) (*domain.ExternalLink, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	l.ExternalCompanyID = strings.TrimSpace(l.ExternalCompanyID)
	if _, err := r.GetAccount(ctx, l.TenantID, l.AccountID); err != nil {
		return nil, err
	}
	got, err := scanLink(r.q(ctx).QueryRow(ctx, `
		INSERT INTO account_external_links (tenant_id, account_id, provider, connection_id, external_company_id, external_name_snapshot, source, verified_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (tenant_id, provider, connection_id, external_company_id) DO UPDATE
		   SET external_name_snapshot = COALESCE(EXCLUDED.external_name_snapshot, account_external_links.external_name_snapshot),
		       verified_at = COALESCE(EXCLUDED.verified_at, account_external_links.verified_at), updated_at = now()
		   WHERE account_external_links.account_id = EXCLUDED.account_id
		RETURNING `+linkColumns, l.TenantID, l.AccountID, l.Provider, l.ConnectionID, l.ExternalCompanyID, l.ExternalNameSnapshot, string(l.Source), l.VerifiedAt))
	if errors.Is(err, pgx.ErrNoRows) { // the conflicting row belongs to another account
		return nil, domain.ErrExternalLinkTaken
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23503" { // a connection that is not this tenant's
		return nil, domain.ErrInvalidAccount
	}
	return got, err
}

// FindByExternal resolves the local account of a provider company (nil when none is linked).
func (r *PostgresRepository) FindByExternal(ctx context.Context, tenantID uuid.UUID, provider string, connectionID uuid.UUID, externalCompanyID string) (*domain.ExternalLink, error) {
	l, err := scanLink(r.q(ctx).QueryRow(ctx, `SELECT `+linkColumns+` FROM account_external_links WHERE tenant_id=$1 AND provider=$2 AND connection_id=$3 AND external_company_id=$4`,
		tenantID, provider, connectionID, strings.TrimSpace(externalCompanyID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return l, err
}

func (r *PostgresRepository) ListExternalLinks(ctx context.Context, tenantID, accountID uuid.UUID) ([]domain.ExternalLink, error) {
	rows, err := r.q(ctx).Query(ctx, `SELECT `+linkColumns+` FROM account_external_links WHERE tenant_id=$1 AND account_id=$2 ORDER BY created_at, id`, tenantID, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ExternalLink{}
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// SetLinkStatus marks a link active/inactive (the provider company was deactivated or restored); history stays.
func (r *PostgresRepository) SetLinkStatus(ctx context.Context, tenantID, linkID uuid.UUID, status domain.LinkStatus) error {
	tag, err := r.q(ctx).Exec(ctx, `UPDATE account_external_links SET status=$3, updated_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, linkID, string(status))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrAccountNotFound
	}
	return nil
}

// MaterializeDelegatedCompanyAccount is find-or-create-and-activate for the account of a provider company, for a Hub agent attending the instance
// (ADR-0040 phase 04b). The agent has no INSERT on accounts or links: delegated_materialize_company_account does exactly this, after checking the key that
// matches the reason (source ticket_flow -> ticket.create, directory_selection -> contact.classify). The caller has ALREADY validated the company against the
// instance's own directory; this only records it.
func (r *PostgresRepository) MaterializeDelegatedCompanyAccount(ctx context.Context, tenantID, connectionID uuid.UUID, externalCompanyID, name string, source domain.LinkSource) (uuid.UUID, error) {
	var id uuid.UUID
	err := r.q(ctx).QueryRow(ctx, `SELECT delegated_materialize_company_account($1,$2,$3,$4,$5)`, tenantID, connectionID, externalCompanyID, name, string(source)).Scan(&id)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "42501" || pg.Code == "22023") {
		return uuid.Nil, domain.ErrInvalidAccount
	}
	return id, err
}
