package domain

import (
	"time"

	"github.com/google/uuid"
)

// TicketID — identificador único de ticket
type TicketID = uuid.UUID

// Ticket — ticket de suporte/operação em uma conta BPO
type Ticket struct {
	ID              TicketID
	AccountID       AccountID
	TenantID        uuid.UUID
	Subject         string
	Description     string
	Priority        TicketPriority  // critical, high, medium, low
	Status          TicketStatus    // open, in_progress, waiting, resolved, closed
	AssignedToID    *uuid.UUID      // User ID do agente
	CustomerID      uuid.UUID       // ID do cliente/organização
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ResolvedAt      *time.Time
	ClosedAt        *time.Time
	FirstResponseAt *time.Time
	SLAMetrics      SLAMetrics
}

// TicketPriority — prioridade do ticket
type TicketPriority string

const (
	TicketPriorityCritical TicketPriority = "critical"
	TicketPriorityHigh     TicketPriority = "high"
	TicketPriorityMedium   TicketPriority = "medium"
	TicketPriorityLow      TicketPriority = "low"
)

// TicketStatus — status do ticket
type TicketStatus string

const (
	TicketStatusOpen       TicketStatus = "open"
	TicketStatusInProgress TicketStatus = "in_progress"
	TicketStatusWaiting    TicketStatus = "waiting"
	TicketStatusResolved   TicketStatus = "resolved"
	TicketStatusClosed     TicketStatus = "closed"
)

// SLAMetrics — métricas de SLA do ticket
type SLAMetrics struct {
	FirstResponseTarget  time.Time  // Deadline para primeira resposta
	ResolutionTarget     time.Time  // Deadline para resolução
	FirstResponseMet     bool       // Primeira resposta no prazo?
	ResolutionMet        bool       // Resolução no prazo?
	BreachedAt           *time.Time // Quando SLA foi quebrado
	ResolutionTimeHours  *float64   // Tempo real de resolução
	WaitingTimeMinutes   int        // Tempo em espera do cliente
	HandlingTimeMinutes  int        // Tempo de trabalho do agente
}

// NewTicket — cria novo ticket
func NewTicket(
	accountID, tenantID, customerID uuid.UUID,
	subject, description string,
	priority TicketPriority,
	slaConfig SLAConfiguration,
) *Ticket {
	now := time.Now()
	firstResponseTarget := now.Add(time.Duration(slaConfig.FirstResponseTime) * time.Minute)
	resolutionTarget := now.Add(time.Duration(slaConfig.ResolutionTime) * time.Hour)

	return &Ticket{
		ID:           uuid.New(),
		AccountID:    accountID,
		TenantID:     tenantID,
		Subject:      subject,
		Description:  description,
		Priority:     priority,
		Status:       TicketStatusOpen,
		CustomerID:   customerID,
		CreatedAt:    now,
		UpdatedAt:    now,
		SLAMetrics: SLAMetrics{
			FirstResponseTarget: firstResponseTarget,
			ResolutionTarget:    resolutionTarget,
		},
	}
}

// Assign — atribui ticket a um agente
func (t *Ticket) Assign(agentID uuid.UUID) {
	t.AssignedToID = &agentID
	t.Status = TicketStatusInProgress
	t.UpdatedAt = time.Now()
}

// RecordFirstResponse — registra primeira resposta
func (t *Ticket) RecordFirstResponse() {
	now := time.Now()
	t.FirstResponseAt = &now

	// Verificar se SLA foi cumprido
	t.SLAMetrics.FirstResponseMet = now.Before(t.SLAMetrics.FirstResponseTarget)

	if !t.SLAMetrics.FirstResponseMet && t.SLAMetrics.BreachedAt == nil {
		t.SLAMetrics.BreachedAt = &now
	}
}

// Resolve — marca ticket como resolvido
func (t *Ticket) Resolve() {
	now := time.Now()
	t.Status = TicketStatusResolved
	t.ResolvedAt = &now
	t.UpdatedAt = now

	// Calcular tempo de resolução
	if duration := now.Sub(t.CreatedAt).Hours(); duration > 0 {
		t.SLAMetrics.ResolutionTimeHours = &duration
	}

	// Verificar se SLA foi cumprido
	t.SLAMetrics.ResolutionMet = now.Before(t.SLAMetrics.ResolutionTarget)

	if !t.SLAMetrics.ResolutionMet && t.SLAMetrics.BreachedAt == nil {
		t.SLAMetrics.BreachedAt = &now
	}
}

// Close — fecha ticket
func (t *Ticket) Close() {
	now := time.Now()
	t.Status = TicketStatusClosed
	t.ClosedAt = &now
	t.UpdatedAt = now
}

// IsOverdue — verifica se ticket está atrasado
func (t *Ticket) IsOverdue() bool {
	now := time.Now()
	return t.Status != TicketStatusClosed && now.After(t.SLAMetrics.ResolutionTarget)
}

// DaysOpen — retorna número de dias em aberto
func (t *Ticket) DaysOpen() int {
	var until time.Time
	if t.ClosedAt != nil {
		until = *t.ClosedAt
	} else {
		until = time.Now()
	}
	return int(until.Sub(t.CreatedAt).Hours() / 24)
}
