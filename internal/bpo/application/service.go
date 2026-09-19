package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/domain"
	"github.com/omnira/omnira/internal/bpo/ports"
)

// BPOService — serviço de aplicação para BPO
type BPOService struct {
	accountRepo ports.AccountRepository
	ticketRepo  ports.TicketRepository
}

// NewBPOService — cria novo BPOService
func NewBPOService(accountRepo ports.AccountRepository, ticketRepo ports.TicketRepository) *BPOService {
	return &BPOService{
		accountRepo: accountRepo,
		ticketRepo:  ticketRepo,
	}
}

// CreateAccount — cria nova conta BPO
func (s *BPOService) CreateAccount(
	ctx context.Context,
	tenantID, operatorID, createdBy uuid.UUID,
	name, description string,
	accountType domain.AccountType,
	maxTeamMembers, maxTicketsMonth int,
	slaConfig domain.SLAConfiguration,
) (*domain.Account, error) {
	account := domain.NewAccount(
		tenantID, operatorID, createdBy,
		name, description,
		accountType,
		maxTeamMembers, maxTicketsMonth,
		slaConfig,
	)

	if err := s.accountRepo.Store(ctx, account); err != nil {
		return nil, fmt.Errorf("failed to create account: %w", err)
	}

	return account, nil
}

// GetAccount — obtém conta por ID
func (s *BPOService) GetAccount(ctx context.Context, id domain.AccountID) (*domain.Account, error) {
	account, err := s.accountRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get account: %w", err)
	}

	return account, nil
}

// ListAccounts — lista contas de um tenant
func (s *BPOService) ListAccounts(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.Account, error) {
	accounts, err := s.accountRepo.FindByTenant(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list accounts: %w", err)
	}

	return accounts, nil
}

// SuspendAccount — suspende uma conta
func (s *BPOService) SuspendAccount(ctx context.Context, id domain.AccountID, reason string, suspendedBy uuid.UUID) error {
	account, err := s.accountRepo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("account not found: %w", err)
	}

	account.Suspend(suspendedBy, reason)

	if err := s.accountRepo.Update(ctx, account); err != nil {
		return fmt.Errorf("failed to suspend account: %w", err)
	}

	return nil
}

// ReactivateAccount — reativa uma conta
func (s *BPOService) ReactivateAccount(ctx context.Context, id domain.AccountID, reactivatedBy uuid.UUID) error {
	account, err := s.accountRepo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("account not found: %w", err)
	}

	account.Reactivate(reactivatedBy)

	if err := s.accountRepo.Update(ctx, account); err != nil {
		return fmt.Errorf("failed to reactivate account: %w", err)
	}

	return nil
}

// CreateTicket — cria novo ticket
func (s *BPOService) CreateTicket(
	ctx context.Context,
	accountID, tenantID, customerID uuid.UUID,
	subject, description string,
	priority domain.TicketPriority,
) (*domain.Ticket, error) {
	// Verificar que account existe e está ativa
	account, err := s.accountRepo.FindByID(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("account not found: %w", err)
	}

	if !account.IsActive() {
		return nil, fmt.Errorf("account is not active")
	}

	// Verificar limite de tickets
	openCount, err := s.ticketRepo.CountByStatus(ctx, accountID, domain.TicketStatusOpen)
	if err != nil {
		return nil, fmt.Errorf("failed to check ticket count: %w", err)
	}

	// Count open + in progress
	inProgressCount, err := s.ticketRepo.CountByStatus(ctx, accountID, domain.TicketStatusInProgress)
	if err != nil {
		return nil, fmt.Errorf("failed to check ticket count: %w", err)
	}

	totalOpen := openCount + inProgressCount
	if !account.CanCreateTicket(totalOpen) {
		return nil, fmt.Errorf("account has reached ticket limit")
	}

	ticket := domain.NewTicket(accountID, tenantID, customerID, subject, description, priority, account.SLAConfig)

	if err := s.ticketRepo.Store(ctx, ticket); err != nil {
		return nil, fmt.Errorf("failed to create ticket: %w", err)
	}

	return ticket, nil
}

// GetTicket — obtém ticket por ID
func (s *BPOService) GetTicket(ctx context.Context, id domain.TicketID) (*domain.Ticket, error) {
	ticket, err := s.ticketRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get ticket: %w", err)
	}

	return ticket, nil
}

// AssignTicket — atribui ticket a um agente
func (s *BPOService) AssignTicket(ctx context.Context, id domain.TicketID, agentID uuid.UUID) error {
	ticket, err := s.ticketRepo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("ticket not found: %w", err)
	}

	ticket.Assign(agentID)

	if err := s.ticketRepo.Update(ctx, ticket); err != nil {
		return fmt.Errorf("failed to assign ticket: %w", err)
	}

	return nil
}

// RecordResponse — registra primeira resposta
func (s *BPOService) RecordResponse(ctx context.Context, id domain.TicketID) error {
	ticket, err := s.ticketRepo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("ticket not found: %w", err)
	}

	ticket.RecordFirstResponse()

	if err := s.ticketRepo.Update(ctx, ticket); err != nil {
		return fmt.Errorf("failed to record response: %w", err)
	}

	return nil
}

// ResolveTicket — marca ticket como resolvido
func (s *BPOService) ResolveTicket(ctx context.Context, id domain.TicketID) error {
	ticket, err := s.ticketRepo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("ticket not found: %w", err)
	}

	ticket.Resolve()

	if err := s.ticketRepo.Update(ctx, ticket); err != nil {
		return fmt.Errorf("failed to resolve ticket: %w", err)
	}

	return nil
}

// CloseTicket — fecha ticket
func (s *BPOService) CloseTicket(ctx context.Context, id domain.TicketID) error {
	ticket, err := s.ticketRepo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("ticket not found: %w", err)
	}

	ticket.Close()

	if err := s.ticketRepo.Update(ctx, ticket); err != nil {
		return fmt.Errorf("failed to close ticket: %w", err)
	}

	return nil
}

// ListOverdueTickets — lista tickets atrasados
func (s *BPOService) ListOverdueTickets(ctx context.Context, accountID domain.AccountID) ([]*domain.Ticket, error) {
	tickets, err := s.ticketRepo.FindOverdue(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to list overdue tickets: %w", err)
	}

	return tickets, nil
}

// GetAccountMetrics — obtém métricas de uma conta
func (s *BPOService) GetAccountMetrics(ctx context.Context, accountID domain.AccountID) (map[string]interface{}, error) {
	openCount, err := s.ticketRepo.CountByStatus(ctx, accountID, domain.TicketStatusOpen)
	if err != nil {
		return nil, fmt.Errorf("failed to get metrics: %w", err)
	}

	inProgressCount, err := s.ticketRepo.CountByStatus(ctx, accountID, domain.TicketStatusInProgress)
	if err != nil {
		return nil, fmt.Errorf("failed to get metrics: %w", err)
	}

	resolvedCount, err := s.ticketRepo.CountByStatus(ctx, accountID, domain.TicketStatusResolved)
	if err != nil {
		return nil, fmt.Errorf("failed to get metrics: %w", err)
	}

	overdueTickets, err := s.ticketRepo.FindOverdue(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to get metrics: %w", err)
	}

	return map[string]interface{}{
		"open":       openCount,
		"in_progress": inProgressCount,
		"resolved":   resolvedCount,
		"overdue":    len(overdueTickets),
	}, nil
}
