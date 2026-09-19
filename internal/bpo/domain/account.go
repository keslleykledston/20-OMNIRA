package domain

import (
	"time"

	"github.com/google/uuid"
)

// AccountID — identificador único de conta BPO
type AccountID = uuid.UUID

// Account — conta BPO (operador ou centro de atendimento)
type Account struct {
	ID              AccountID
	Name            string
	Description     string
	Type            AccountType       // operator, contact_center, reseller
	Status          AccountStatus     // active, inactive, suspended
	TenantID        uuid.UUID         // Tenant que owns esta account BPO
	OperatorID      uuid.UUID         // Usuário operator responsável
	MaxTeamMembers  int               // Limite de agentes
	MaxTicketsMonth int               // Limite de tickets/mês
	SLAConfig       SLAConfiguration  // Configuração de SLA
	Metadata        map[string]string // Custom fields
	CreatedAt       time.Time
	UpdatedAt       time.Time
	CreatedBy       uuid.UUID
	UpdatedBy       uuid.UUID
}

// AccountType — tipo de account BPO
type AccountType string

const (
	AccountTypeOperator      AccountType = "operator"      // Operador interno
	AccountTypeContactCenter AccountType = "contact_center" // Centro de atendimento externo
	AccountTypeReseller      AccountType = "reseller"       // Revendedor
)

// AccountStatus — status da account
type AccountStatus string

const (
	AccountStatusActive    AccountStatus = "active"
	AccountStatusInactive  AccountStatus = "inactive"
	AccountStatusSuspended AccountStatus = "suspended"
)

// SLAConfiguration — configuração de SLA para account
type SLAConfiguration struct {
	FirstResponseTime      int    `json:"first_response_time_minutes"`      // Tempo máximo para primeira resposta
	ResolutionTime         int    `json:"resolution_time_hours"`             // Tempo máximo para resolução
	ResponseTimeSLO        int    `json:"response_time_slo_percent"`         // SLO % (ex: 95)
	AvailabilitySLO        int    `json:"availability_slo_percent"`          // SLO % uptime
	OnCallSupport          bool   `json:"on_call_support"`                   // 24/7 support
	EscalationChainsCount  int    `json:"escalation_chains_count"`           // Número de escalações permitidas
	IncidentResponseTarget int    `json:"incident_response_target_minutes"` // Tempo alvo para incidente crítico
}

// NewAccount — cria nova account BPO
func NewAccount(
	tenantID, operatorID, createdBy uuid.UUID,
	name, description string,
	accountType AccountType,
	maxTeamMembers, maxTicketsMonth int,
	slaConfig SLAConfiguration,
) *Account {
	return &Account{
		ID:              uuid.New(),
		Name:            name,
		Description:     description,
		Type:            accountType,
		Status:          AccountStatusActive,
		TenantID:        tenantID,
		OperatorID:      operatorID,
		MaxTeamMembers:  maxTeamMembers,
		MaxTicketsMonth: maxTicketsMonth,
		SLAConfig:       slaConfig,
		Metadata:        make(map[string]string),
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
		CreatedBy:       createdBy,
		UpdatedBy:       createdBy,
	}
}

// IsActive — verifica se account está ativa
func (a *Account) IsActive() bool {
	return a.Status == AccountStatusActive
}

// Suspend — suspende account
func (a *Account) Suspend(suspendedBy uuid.UUID, reason string) {
	a.Status = AccountStatusSuspended
	a.Metadata["suspension_reason"] = reason
	a.UpdatedAt = time.Now()
	a.UpdatedBy = suspendedBy
}

// Reactivate — reativa account
func (a *Account) Reactivate(reactivatedBy uuid.UUID) {
	a.Status = AccountStatusActive
	delete(a.Metadata, "suspension_reason")
	a.UpdatedAt = time.Now()
	a.UpdatedBy = reactivatedBy
}

// UpdateSLA — atualiza configuração de SLA
func (a *Account) UpdateSLA(slaConfig SLAConfiguration, updatedBy uuid.UUID) {
	a.SLAConfig = slaConfig
	a.UpdatedAt = time.Now()
	a.UpdatedBy = updatedBy
}

// CanAddTeamMember — verifica se pode adicionar novo membro
func (a *Account) CanAddTeamMember(currentCount int) bool {
	return currentCount < a.MaxTeamMembers
}

// CanCreateTicket — verifica se pode criar novo ticket este mês
func (a *Account) CanCreateTicket(ticketsThisMonth int) bool {
	return ticketsThisMonth < a.MaxTicketsMonth
}
