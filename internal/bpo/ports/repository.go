package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/domain"
)

// AccountRepository — interface para operações com contas BPO
type AccountRepository interface {
	// Store — salva nova account
	Store(ctx context.Context, account *domain.Account) error

	// FindByID — busca account por ID
	FindByID(ctx context.Context, id domain.AccountID) (*domain.Account, error)

	// FindByTenant — lista accounts de um tenant
	FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.Account, error)

	// Update — atualiza account
	Update(ctx context.Context, account *domain.Account) error

	// Delete — deleta account
	Delete(ctx context.Context, id domain.AccountID) error

	// CountByTenant — conta accounts de um tenant
	CountByTenant(ctx context.Context, tenantID uuid.UUID) (int, error)
}

// TicketRepository — interface para operações com tickets
type TicketRepository interface {
	// Store — salva novo ticket
	Store(ctx context.Context, ticket *domain.Ticket) error

	// FindByID — busca ticket por ID
	FindByID(ctx context.Context, id domain.TicketID) (*domain.Ticket, error)

	// FindByAccount — lista tickets de uma account
	FindByAccount(ctx context.Context, accountID domain.AccountID, limit, offset int) ([]*domain.Ticket, error)

	// FindByAccountAndStatus — lista tickets com status específico
	FindByAccountAndStatus(ctx context.Context, accountID domain.AccountID, status domain.TicketStatus, limit, offset int) ([]*domain.Ticket, error)

	// Update — atualiza ticket
	Update(ctx context.Context, ticket *domain.Ticket) error

	// CountByStatus — conta tickets por status
	CountByStatus(ctx context.Context, accountID domain.AccountID, status domain.TicketStatus) (int, error)

	// FindOverdue — lista tickets atrasados
	FindOverdue(ctx context.Context, accountID domain.AccountID) ([]*domain.Ticket, error)
}
