package adapters

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/bpo/domain"
)

// PostgresAccountRepository — implementação PostgreSQL de AccountRepository
type PostgresAccountRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresAccountRepository — cria novo repositório
func NewPostgresAccountRepository(pool *pgxpool.Pool) *PostgresAccountRepository {
	return &PostgresAccountRepository{pool: pool}
}

// Store — salva nova account
func (r *PostgresAccountRepository) Store(ctx context.Context, account *domain.Account) error {
	sql := `
		INSERT INTO bpo_accounts
		(id, tenant_id, operator_id, name, description, account_type, status,
		 max_team_members, max_tickets_month, sla_config, created_at, created_by, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`

	_, err := r.pool.Exec(ctx, sql,
		account.ID,
		account.TenantID,
		account.OperatorID,
		account.Name,
		account.Description,
		account.Type,
		account.Status,
		account.MaxTeamMembers,
		account.MaxTicketsMonth,
		account.SLAConfig,
		account.CreatedAt,
		account.CreatedBy,
		account.Metadata,
	)

	if err != nil {
		return fmt.Errorf("failed to store account: %w", err)
	}

	return nil
}

// FindByID — busca account por ID
func (r *PostgresAccountRepository) FindByID(ctx context.Context, id domain.AccountID) (*domain.Account, error) {
	sql := `
		SELECT id, tenant_id, operator_id, name, description, account_type, status,
		       max_team_members, max_tickets_month, sla_config, created_at, created_by,
		       updated_at, metadata
		FROM bpo_accounts WHERE id = $1
	`

	row := r.pool.QueryRow(ctx, sql, id)

	var account domain.Account
	err := row.Scan(
		&account.ID,
		&account.TenantID,
		&account.OperatorID,
		&account.Name,
		&account.Description,
		&account.Type,
		&account.Status,
		&account.MaxTeamMembers,
		&account.MaxTicketsMonth,
		&account.SLAConfig,
		&account.CreatedAt,
		&account.CreatedBy,
		&account.UpdatedAt,
		&account.Metadata,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to find account: %w", err)
	}

	return &account, nil
}

// FindByTenant — lista accounts de um tenant
func (r *PostgresAccountRepository) FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.Account, error) {
	sql := `
		SELECT id, tenant_id, operator_id, name, description, account_type, status,
		       max_team_members, max_tickets_month, sla_config, created_at, created_by,
		       updated_at, metadata
		FROM bpo_accounts WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3
	`

	rows, err := r.pool.Query(ctx, sql, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query accounts: %w", err)
	}
	defer rows.Close()

	var accounts []*domain.Account
	for rows.Next() {
		var account domain.Account
		err := rows.Scan(
			&account.ID,
			&account.TenantID,
			&account.OperatorID,
			&account.Name,
			&account.Description,
			&account.Type,
			&account.Status,
			&account.MaxTeamMembers,
			&account.MaxTicketsMonth,
			&account.SLAConfig,
			&account.CreatedAt,
			&account.CreatedBy,
			&account.UpdatedAt,
			&account.Metadata,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan account: %w", err)
		}
		accounts = append(accounts, &account)
	}

	return accounts, nil
}

// Update — atualiza account
func (r *PostgresAccountRepository) Update(ctx context.Context, account *domain.Account) error {
	sql := `
		UPDATE bpo_accounts
		SET status = $1, sla_config = $2, updated_at = $3, metadata = $4
		WHERE id = $5
	`

	_, err := r.pool.Exec(ctx, sql,
		account.Status,
		account.SLAConfig,
		account.UpdatedAt,
		account.Metadata,
		account.ID,
	)

	if err != nil {
		return fmt.Errorf("failed to update account: %w", err)
	}

	return nil
}

// Delete — deleta account
func (r *PostgresAccountRepository) Delete(ctx context.Context, id domain.AccountID) error {
	_, err := r.pool.Exec(ctx, "DELETE FROM bpo_accounts WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("failed to delete account: %w", err)
	}
	return nil
}

// CountByTenant — conta accounts de um tenant
func (r *PostgresAccountRepository) CountByTenant(ctx context.Context, tenantID uuid.UUID) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM bpo_accounts WHERE tenant_id = $1", tenantID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count accounts: %w", err)
	}
	return count, nil
}

// PostgresTicketRepository — implementação PostgreSQL de TicketRepository
type PostgresTicketRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresTicketRepository — cria novo repositório
func NewPostgresTicketRepository(pool *pgxpool.Pool) *PostgresTicketRepository {
	return &PostgresTicketRepository{pool: pool}
}

// Store — salva novo ticket
func (r *PostgresTicketRepository) Store(ctx context.Context, ticket *domain.Ticket) error {
	sql := `
		INSERT INTO bpo_tickets
		(id, account_id, tenant_id, customer_id, subject, description, priority, status,
		 assigned_to_id, first_response_at, resolved_at, closed_at, created_at, sla_metrics)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`

	_, err := r.pool.Exec(ctx, sql,
		ticket.ID,
		ticket.AccountID,
		ticket.TenantID,
		ticket.CustomerID,
		ticket.Subject,
		ticket.Description,
		ticket.Priority,
		ticket.Status,
		ticket.AssignedToID,
		ticket.FirstResponseAt,
		ticket.ResolvedAt,
		ticket.ClosedAt,
		ticket.CreatedAt,
		ticket.SLAMetrics,
	)

	if err != nil {
		return fmt.Errorf("failed to store ticket: %w", err)
	}

	return nil
}

// FindByID — busca ticket por ID
func (r *PostgresTicketRepository) FindByID(ctx context.Context, id domain.TicketID) (*domain.Ticket, error) {
	sql := `
		SELECT id, account_id, tenant_id, customer_id, subject, description, priority, status,
		       assigned_to_id, first_response_at, resolved_at, closed_at, created_at, updated_at, sla_metrics
		FROM bpo_tickets WHERE id = $1
	`

	row := r.pool.QueryRow(ctx, sql, id)

	var ticket domain.Ticket
	err := row.Scan(
		&ticket.ID,
		&ticket.AccountID,
		&ticket.TenantID,
		&ticket.CustomerID,
		&ticket.Subject,
		&ticket.Description,
		&ticket.Priority,
		&ticket.Status,
		&ticket.AssignedToID,
		&ticket.FirstResponseAt,
		&ticket.ResolvedAt,
		&ticket.ClosedAt,
		&ticket.CreatedAt,
		&ticket.UpdatedAt,
		&ticket.SLAMetrics,
	)

	if err == pgx.ErrNoRows {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("failed to find ticket: %w", err)
	}

	return &ticket, nil
}

// FindByAccount — lista tickets de uma account
func (r *PostgresTicketRepository) FindByAccount(ctx context.Context, accountID domain.AccountID, limit, offset int) ([]*domain.Ticket, error) {
	sql := `
		SELECT id, account_id, tenant_id, customer_id, subject, description, priority, status,
		       assigned_to_id, first_response_at, resolved_at, closed_at, created_at, updated_at, sla_metrics
		FROM bpo_tickets WHERE account_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3
	`

	rows, err := r.pool.Query(ctx, sql, accountID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query tickets: %w", err)
	}
	defer rows.Close()

	var tickets []*domain.Ticket
	for rows.Next() {
		var ticket domain.Ticket
		err := rows.Scan(
			&ticket.ID,
			&ticket.AccountID,
			&ticket.TenantID,
			&ticket.CustomerID,
			&ticket.Subject,
			&ticket.Description,
			&ticket.Priority,
			&ticket.Status,
			&ticket.AssignedToID,
			&ticket.FirstResponseAt,
			&ticket.ResolvedAt,
			&ticket.ClosedAt,
			&ticket.CreatedAt,
			&ticket.UpdatedAt,
			&ticket.SLAMetrics,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan ticket: %w", err)
		}
		tickets = append(tickets, &ticket)
	}

	return tickets, nil
}

// FindByAccountAndStatus — lista tickets com status específico
func (r *PostgresTicketRepository) FindByAccountAndStatus(ctx context.Context, accountID domain.AccountID, status domain.TicketStatus, limit, offset int) ([]*domain.Ticket, error) {
	sql := `
		SELECT id, account_id, tenant_id, customer_id, subject, description, priority, status,
		       assigned_to_id, first_response_at, resolved_at, closed_at, created_at, updated_at, sla_metrics
		FROM bpo_tickets WHERE account_id = $1 AND status = $2 ORDER BY created_at DESC LIMIT $3 OFFSET $4
	`

	rows, err := r.pool.Query(ctx, sql, accountID, status, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query tickets: %w", err)
	}
	defer rows.Close()

	var tickets []*domain.Ticket
	for rows.Next() {
		var ticket domain.Ticket
		err := rows.Scan(
			&ticket.ID,
			&ticket.AccountID,
			&ticket.TenantID,
			&ticket.CustomerID,
			&ticket.Subject,
			&ticket.Description,
			&ticket.Priority,
			&ticket.Status,
			&ticket.AssignedToID,
			&ticket.FirstResponseAt,
			&ticket.ResolvedAt,
			&ticket.ClosedAt,
			&ticket.CreatedAt,
			&ticket.UpdatedAt,
			&ticket.SLAMetrics,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan ticket: %w", err)
		}
		tickets = append(tickets, &ticket)
	}

	return tickets, nil
}

// Update — atualiza ticket
func (r *PostgresTicketRepository) Update(ctx context.Context, ticket *domain.Ticket) error {
	sql := `
		UPDATE bpo_tickets
		SET status = $1, assigned_to_id = $2, first_response_at = $3,
		    resolved_at = $4, closed_at = $5, updated_at = $6, sla_metrics = $7
		WHERE id = $8
	`

	_, err := r.pool.Exec(ctx, sql,
		ticket.Status,
		ticket.AssignedToID,
		ticket.FirstResponseAt,
		ticket.ResolvedAt,
		ticket.ClosedAt,
		ticket.UpdatedAt,
		ticket.SLAMetrics,
		ticket.ID,
	)

	if err != nil {
		return fmt.Errorf("failed to update ticket: %w", err)
	}

	return nil
}

// CountByStatus — conta tickets por status
func (r *PostgresTicketRepository) CountByStatus(ctx context.Context, accountID domain.AccountID, status domain.TicketStatus) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM bpo_tickets WHERE account_id = $1 AND status = $2",
		accountID, status).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to count tickets: %w", err)
	}
	return count, nil
}

// FindOverdue — lista tickets atrasados
func (r *PostgresTicketRepository) FindOverdue(ctx context.Context, accountID domain.AccountID) ([]*domain.Ticket, error) {
	sql := `
		SELECT id, account_id, tenant_id, customer_id, subject, description, priority, status,
		       assigned_to_id, first_response_at, resolved_at, closed_at, created_at, updated_at, sla_metrics
		FROM bpo_tickets
		WHERE account_id = $1
		  AND status NOT IN ('resolved', 'closed')
		  AND (sla_metrics->>'resolution_target')::timestamp < now()
		ORDER BY created_at DESC
	`

	rows, err := r.pool.Query(ctx, sql, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to query overdue tickets: %w", err)
	}
	defer rows.Close()

	var tickets []*domain.Ticket
	for rows.Next() {
		var ticket domain.Ticket
		err := rows.Scan(
			&ticket.ID,
			&ticket.AccountID,
			&ticket.TenantID,
			&ticket.CustomerID,
			&ticket.Subject,
			&ticket.Description,
			&ticket.Priority,
			&ticket.Status,
			&ticket.AssignedToID,
			&ticket.FirstResponseAt,
			&ticket.ResolvedAt,
			&ticket.ClosedAt,
			&ticket.CreatedAt,
			&ticket.UpdatedAt,
			&ticket.SLAMetrics,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan ticket: %w", err)
		}
		tickets = append(tickets, &ticket)
	}

	return tickets, nil
}
