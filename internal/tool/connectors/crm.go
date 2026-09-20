package connectors

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// CRMTicket — representação local de ticket no CRM
type CRMTicket struct {
	ID        string
	CustomerID string
	Subject   string
	Status    string // open, in_progress, resolved, closed
	CreatedAt time.Time
	UpdatedAt time.Time
}

// CRMConnector — porta canônica de CRM/ERP. O core não conhece campos de
// IXC/SGP/Hubsoft: cada adapter traduz para o seu sistema e guarda a
// referência externa por conta própria.
type CRMConnector interface {
	Name() string
	Authenticate(ctx context.Context, credentials map[string]interface{}) error
	// FindCustomer localiza o cliente por um identificador de busca. O que
	// serve como identificador é decisão do adapter: e-mail no mock, e em um
	// ERP de provedor normalmente telefone, CPF/CNPJ ou contrato — que é o
	// dado que o atendimento por WhatsApp tem em mãos.
	FindCustomer(ctx context.Context, query string) (customerID string, err error)
	CreateTicket(ctx context.Context, customerID, subject string) (ticketID string, err error)
	GetTicket(ctx context.Context, ticketID string) (*CRMTicket, error)
	UpdateTicket(ctx context.Context, ticketID, status string) error
	CloseTicket(ctx context.Context, ticketID string) error
}

// MockCRMConnector — implementação mock de CRM (memória, para MVP)
type MockCRMConnector struct {
	mu        sync.RWMutex
	tickets   map[string]*CRMTicket
	customers map[string]string // email -> customerID
}

// NewMockCRMConnector — cria novo mock CRM
func NewMockCRMConnector() *MockCRMConnector {
	return &MockCRMConnector{
		tickets:   make(map[string]*CRMTicket),
		customers: make(map[string]string),
	}
}

// Name — retorna nome do conector
func (m *MockCRMConnector) Name() string {
	return "crm_mock"
}

// Authenticate — valida credenciais (mock sempre passa)
func (m *MockCRMConnector) Authenticate(ctx context.Context, credentials map[string]interface{}) error {
	// Mock sempre autentica sem validação
	return nil
}

// FindCustomer — procura cliente por email (ou cria novo)
func (m *MockCRMConnector) FindCustomer(ctx context.Context, email string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if customerID, exists := m.customers[email]; exists {
		return customerID, nil
	}

	// Criar novo cliente mock
	customerID := "cust_" + uuid.New().String()
	m.customers[email] = customerID
	return customerID, nil
}

// CreateTicket — cria novo ticket
func (m *MockCRMConnector) CreateTicket(ctx context.Context, customerID, subject string) (string, error) {
	if customerID == "" || subject == "" {
		return "", fmt.Errorf("crm: customer_id and subject required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	ticketID := "ticket_" + uuid.New().String()
	now := time.Now().UTC()
	m.tickets[ticketID] = &CRMTicket{
		ID:        ticketID,
		CustomerID: customerID,
		Subject:   subject,
		Status:    "open",
		CreatedAt: now,
		UpdatedAt: now,
	}

	return ticketID, nil
}

// GetTicket — obtém ticket
func (m *MockCRMConnector) GetTicket(ctx context.Context, ticketID string) (*CRMTicket, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ticket, exists := m.tickets[ticketID]
	if !exists {
		return nil, fmt.Errorf("crm: ticket not found")
	}

	return ticket, nil
}

// UpdateTicket — atualiza status do ticket
func (m *MockCRMConnector) UpdateTicket(ctx context.Context, ticketID, status string) error {
	if status == "" {
		return fmt.Errorf("crm: status required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	ticket, exists := m.tickets[ticketID]
	if !exists {
		return fmt.Errorf("crm: ticket not found")
	}

	ticket.Status = status
	ticket.UpdatedAt = time.Now().UTC()
	return nil
}

// CloseTicket — fecha o ticket
func (m *MockCRMConnector) CloseTicket(ctx context.Context, ticketID string) error {
	return m.UpdateTicket(ctx, ticketID, "closed")
}
