package application

import (
	"context"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/domain"
)

// MockAccountRepository — mock para testes
type MockAccountRepository struct {
	accounts map[string]*domain.Account
}

// NewMockAccountRepository — cria novo mock
func NewMockAccountRepository() *MockAccountRepository {
	return &MockAccountRepository{
		accounts: make(map[string]*domain.Account),
	}
}

func (m *MockAccountRepository) Store(ctx context.Context, account *domain.Account) error {
	m.accounts[account.ID.String()] = account
	return nil
}

func (m *MockAccountRepository) FindByID(ctx context.Context, id domain.AccountID) (*domain.Account, error) {
	acc, ok := m.accounts[id.String()]
	if !ok {
		return nil, nil
	}
	return acc, nil
}

func (m *MockAccountRepository) FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.Account, error) {
	var result []*domain.Account
	for _, acc := range m.accounts {
		if acc.TenantID == tenantID {
			result = append(result, acc)
		}
	}
	return result, nil
}

func (m *MockAccountRepository) Update(ctx context.Context, account *domain.Account) error {
	m.accounts[account.ID.String()] = account
	return nil
}

func (m *MockAccountRepository) Delete(ctx context.Context, id domain.AccountID) error {
	delete(m.accounts, id.String())
	return nil
}

func (m *MockAccountRepository) CountByTenant(ctx context.Context, tenantID uuid.UUID) (int, error) {
	count := 0
	for _, acc := range m.accounts {
		if acc.TenantID == tenantID {
			count++
		}
	}
	return count, nil
}

// MockTicketRepository — mock para testes
type MockTicketRepository struct {
	tickets map[string]*domain.Ticket
}

// NewMockTicketRepository — cria novo mock
func NewMockTicketRepository() *MockTicketRepository {
	return &MockTicketRepository{
		tickets: make(map[string]*domain.Ticket),
	}
}

func (m *MockTicketRepository) Store(ctx context.Context, ticket *domain.Ticket) error {
	m.tickets[ticket.ID.String()] = ticket
	return nil
}

func (m *MockTicketRepository) FindByID(ctx context.Context, id domain.TicketID) (*domain.Ticket, error) {
	t, ok := m.tickets[id.String()]
	if !ok {
		return nil, nil
	}
	return t, nil
}

func (m *MockTicketRepository) FindByAccount(ctx context.Context, accountID domain.AccountID, limit, offset int) ([]*domain.Ticket, error) {
	var result []*domain.Ticket
	count := 0
	for _, t := range m.tickets {
		if t.AccountID == accountID {
			if count >= offset && (limit == 0 || count < offset+limit) {
				result = append(result, t)
			}
			count++
		}
	}
	return result, nil
}

func (m *MockTicketRepository) FindByAccountAndStatus(ctx context.Context, accountID domain.AccountID, status domain.TicketStatus, limit, offset int) ([]*domain.Ticket, error) {
	var result []*domain.Ticket
	count := 0
	for _, t := range m.tickets {
		if t.AccountID == accountID && t.Status == status {
			if count >= offset && (limit == 0 || count < offset+limit) {
				result = append(result, t)
			}
			count++
		}
	}
	return result, nil
}

func (m *MockTicketRepository) Update(ctx context.Context, ticket *domain.Ticket) error {
	m.tickets[ticket.ID.String()] = ticket
	return nil
}

func (m *MockTicketRepository) CountByStatus(ctx context.Context, accountID domain.AccountID, status domain.TicketStatus) (int, error) {
	count := 0
	for _, t := range m.tickets {
		if t.AccountID == accountID && t.Status == status {
			count++
		}
	}
	return count, nil
}

func (m *MockTicketRepository) FindOverdue(ctx context.Context, accountID domain.AccountID) ([]*domain.Ticket, error) {
	var result []*domain.Ticket
	for _, t := range m.tickets {
		if t.AccountID == accountID && t.IsOverdue() {
			result = append(result, t)
		}
	}
	return result, nil
}

// MockAuditRepository — mock para testes
type MockAuditRepository struct {
	events []*domain.AuditEvent
}

// NewMockAuditRepository — cria novo mock
func NewMockAuditRepository() *MockAuditRepository {
	return &MockAuditRepository{
		events: []*domain.AuditEvent{},
	}
}

func (m *MockAuditRepository) Store(ctx context.Context, event *domain.AuditEvent) error {
	m.events = append(m.events, event)
	return nil
}

func (m *MockAuditRepository) FindByAccount(ctx context.Context, accountID domain.AccountID, limit, offset int) ([]*domain.AuditEvent, error) {
	var result []*domain.AuditEvent
	count := 0
	for _, e := range m.events {
		if e.AccountID == accountID {
			if count >= offset && (limit == 0 || count < offset+limit) {
				result = append(result, e)
			}
			count++
		}
	}
	return result, nil
}

func (m *MockAuditRepository) FindByTicket(ctx context.Context, ticketID domain.TicketID, limit, offset int) ([]*domain.AuditEvent, error) {
	var result []*domain.AuditEvent
	count := 0
	for _, e := range m.events {
		if e.TicketID != nil && *e.TicketID == ticketID {
			if count >= offset && (limit == 0 || count < offset+limit) {
				result = append(result, e)
			}
			count++
		}
	}
	return result, nil
}

func (m *MockAuditRepository) FindByAction(ctx context.Context, accountID domain.AccountID, action domain.AuditAction, limit, offset int) ([]*domain.AuditEvent, error) {
	var result []*domain.AuditEvent
	count := 0
	for _, e := range m.events {
		if e.AccountID == accountID && e.Action == action {
			if count >= offset && (limit == 0 || count < offset+limit) {
				result = append(result, e)
			}
			count++
		}
	}
	return result, nil
}

func (m *MockAuditRepository) CountByAccount(ctx context.Context, accountID domain.AccountID) (int, error) {
	count := 0
	for _, e := range m.events {
		if e.AccountID == accountID {
			count++
		}
	}
	return count, nil
}

// MockSupervisorRoleRepository — mock para testes
type MockSupervisorRoleRepository struct {
	roles map[string]*domain.SupervisorRole
}

// NewMockSupervisorRoleRepository — cria novo mock
func NewMockSupervisorRoleRepository() *MockSupervisorRoleRepository {
	return &MockSupervisorRoleRepository{
		roles: make(map[string]*domain.SupervisorRole),
	}
}

func (m *MockSupervisorRoleRepository) Store(ctx context.Context, role *domain.SupervisorRole) error {
	m.roles[role.ID.String()] = role
	return nil
}

func (m *MockSupervisorRoleRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.SupervisorRole, error) {
	role, ok := m.roles[id.String()]
	if !ok {
		return nil, nil
	}
	return role, nil
}

func (m *MockSupervisorRoleRepository) FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.SupervisorRole, error) {
	var result []*domain.SupervisorRole
	for _, role := range m.roles {
		if role.TenantID == tenantID {
			result = append(result, role)
		}
	}
	return result, nil
}

func (m *MockSupervisorRoleRepository) Update(ctx context.Context, role *domain.SupervisorRole) error {
	m.roles[role.ID.String()] = role
	return nil
}

func (m *MockSupervisorRoleRepository) Delete(ctx context.Context, id uuid.UUID) error {
	delete(m.roles, id.String())
	return nil
}

// MockSupervisorAssignmentRepository — mock para testes
type MockSupervisorAssignmentRepository struct {
	assignments map[string]*domain.SupervisorAssignment
}

// NewMockSupervisorAssignmentRepository — cria novo mock
func NewMockSupervisorAssignmentRepository() *MockSupervisorAssignmentRepository {
	return &MockSupervisorAssignmentRepository{
		assignments: make(map[string]*domain.SupervisorAssignment),
	}
}

func (m *MockSupervisorAssignmentRepository) Store(ctx context.Context, assignment *domain.SupervisorAssignment) error {
	m.assignments[assignment.ID.String()] = assignment
	return nil
}

func (m *MockSupervisorAssignmentRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.SupervisorAssignment, error) {
	assignment, ok := m.assignments[id.String()]
	if !ok {
		return nil, nil
	}
	return assignment, nil
}

func (m *MockSupervisorAssignmentRepository) FindByUser(ctx context.Context, userID uuid.UUID) ([]*domain.SupervisorAssignment, error) {
	var result []*domain.SupervisorAssignment
	for _, assignment := range m.assignments {
		if assignment.UserID == userID {
			result = append(result, assignment)
		}
	}
	return result, nil
}

func (m *MockSupervisorAssignmentRepository) FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.SupervisorAssignment, error) {
	var result []*domain.SupervisorAssignment
	for _, assignment := range m.assignments {
		if assignment.TenantID == tenantID {
			result = append(result, assignment)
		}
	}
	return result, nil
}

func (m *MockSupervisorAssignmentRepository) Update(ctx context.Context, assignment *domain.SupervisorAssignment) error {
	m.assignments[assignment.ID.String()] = assignment
	return nil
}

func (m *MockSupervisorAssignmentRepository) Delete(ctx context.Context, id uuid.UUID) error {
	delete(m.assignments, id.String())
	return nil
}
