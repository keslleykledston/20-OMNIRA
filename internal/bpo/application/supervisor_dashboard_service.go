package application

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/domain"
	"github.com/omnira/omnira/internal/bpo/ports"
)

// SupervisorDashboardService — serviço para dashboard de supervisor
type SupervisorDashboardService struct {
	supervisorSvc  *SupervisorService
	bpoSvc         *BPOService
	slaSvc         *SLAService
	assignmentRepo ports.SupervisorAssignmentRepository
	accountRepo    ports.AccountRepository
	ticketRepo     ports.TicketRepository
}

// NewSupervisorDashboardService — cria novo serviço
func NewSupervisorDashboardService(
	supervisorSvc *SupervisorService,
	bpoSvc *BPOService,
	slaSvc *SLAService,
	assignmentRepo ports.SupervisorAssignmentRepository,
	accountRepo ports.AccountRepository,
	ticketRepo ports.TicketRepository,
) *SupervisorDashboardService {
	return &SupervisorDashboardService{
		supervisorSvc:  supervisorSvc,
		bpoSvc:         bpoSvc,
		slaSvc:         slaSvc,
		assignmentRepo: assignmentRepo,
		accountRepo:    accountRepo,
		ticketRepo:     ticketRepo,
	}
}

// AccountSnapshot — snapshot de uma account
type AccountSnapshot struct {
	ID               domain.AccountID `json:"id"`
	Name             string           `json:"name"`
	Status           string           `json:"status"`
	OpenTickets      int              `json:"open_tickets"`
	InProgressTickets int             `json:"in_progress_tickets"`
	OverdueTickets   int              `json:"overdue_tickets"`
	SLAComplianceRate float64         `json:"sla_compliance_rate"`
	Team             int              `json:"team_count"`
}

// SupervisorDashboard — dashboard agregado para supervisor
type SupervisorDashboard struct {
	UserID                uuid.UUID         `json:"user_id"`
	TotalAccounts         int               `json:"total_accounts"`
	TotalOpenTickets      int               `json:"total_open_tickets"`
	TotalOverdueTickets   int               `json:"total_overdue_tickets"`
	OverallSLACompliance  float64           `json:"overall_sla_compliance"`
	CriticalAlerts        int               `json:"critical_alerts"`
	Accounts              []AccountSnapshot `json:"accounts"`
}

// GetSupervisorDashboard — obtém dashboard para supervisor
func (s *SupervisorDashboardService) GetSupervisorDashboard(ctx context.Context, userID uuid.UUID) (*SupervisorDashboard, error) {
	assignments, err := s.assignmentRepo.FindByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get assignments: %w", err)
	}

	dashboard := &SupervisorDashboard{
		UserID:   userID,
		Accounts: []AccountSnapshot{},
	}

	if len(assignments) == 0 {
		return dashboard, nil
	}

	var totalSLAMet int
	var totalSLAChecked int

	for _, assignment := range assignments {
		if !assignment.IsActive() {
			continue
		}

		for _, accountID := range assignment.AssignedAccounts {
			// Obter account
			account, err := s.accountRepo.FindByID(ctx, accountID)
			if err != nil || account == nil {
				continue
			}

			snapshot := AccountSnapshot{
				ID:     account.ID,
				Name:   account.Name,
				Status: string(account.Status),
			}

			// Contar tickets por status
			openCount, _ := s.ticketRepo.CountByStatus(ctx, accountID, domain.TicketStatusOpen)
			inProgressCount, _ := s.ticketRepo.CountByStatus(ctx, accountID, domain.TicketStatusInProgress)

			snapshot.OpenTickets = openCount
			snapshot.InProgressTickets = inProgressCount

			dashboard.TotalOpenTickets += openCount + inProgressCount

			// Tickets overdue
			overdueTickets, _ := s.ticketRepo.FindOverdue(ctx, accountID)
			snapshot.OverdueTickets = len(overdueTickets)
			dashboard.TotalOverdueTickets += len(overdueTickets)

			if len(overdueTickets) > 3 {
				dashboard.CriticalAlerts++
			}

			// SLA Compliance
			report, err := s.slaSvc.GetSLAComplianceReport(ctx, accountID, assignment.AssignedAt, assignment.AssignedAt)
			if err == nil && report != nil {
				snapshot.SLAComplianceRate = report.FirstResponseRate
				totalSLAMet += int(report.FirstResponseRate)
				totalSLAChecked++
			}

			dashboard.Accounts = append(dashboard.Accounts, snapshot)
		}
	}

	dashboard.TotalAccounts = len(dashboard.Accounts)

	if totalSLAChecked > 0 {
		dashboard.OverallSLACompliance = float64(totalSLAMet) / float64(totalSLAChecked)
	}

	return dashboard, nil
}

// SupervisorAlert — alerta para supervisor
type SupervisorAlert struct {
	ID          uuid.UUID `json:"id"`
	AccountID   domain.AccountID `json:"account_id"`
	AccountName string    `json:"account_name"`
	Type        string    `json:"type"` // "overdue", "sla_breach", "critical_priority"
	Severity    string    `json:"severity"` // "low", "medium", "high", "critical"
	Title       string    `json:"title"`
	Description string    `json:"description"`
	CreatedAt   string    `json:"created_at"`
}

// GetSupervisorAlerts — obtém alertas para supervisor
func (s *SupervisorDashboardService) GetSupervisorAlerts(ctx context.Context, userID uuid.UUID) ([]SupervisorAlert, error) {
	assignments, err := s.assignmentRepo.FindByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get assignments: %w", err)
	}

	var alerts []SupervisorAlert

	for _, assignment := range assignments {
		if !assignment.IsActive() {
			continue
		}

		for _, accountID := range assignment.AssignedAccounts {
			// Obter account
			account, err := s.accountRepo.FindByID(ctx, accountID)
			if err != nil || account == nil {
				continue
			}

			// Check overdue tickets
			overdueTickets, _ := s.ticketRepo.FindOverdue(ctx, accountID)
			if len(overdueTickets) > 0 {
				alert := SupervisorAlert{
					ID:          uuid.New(),
					AccountID:   accountID,
					AccountName: account.Name,
					Type:        "overdue",
					Title:       fmt.Sprintf("%d Overdue Tickets", len(overdueTickets)),
					Description: fmt.Sprintf("Account has %d tickets past SLA deadline", len(overdueTickets)),
					CreatedAt:   assignment.AssignedAt.Format("2006-01-02T15:04:05Z07:00"),
				}

				if len(overdueTickets) > 5 {
					alert.Severity = "critical"
				} else if len(overdueTickets) > 3 {
					alert.Severity = "high"
				} else {
					alert.Severity = "medium"
				}

				alerts = append(alerts, alert)
			}

			// Check SLA compliance
			report, _ := s.slaSvc.GetSLAComplianceReport(ctx, accountID, assignment.AssignedAt, assignment.AssignedAt)
			if report != nil && report.FirstResponseRate < 85 {
				alert := SupervisorAlert{
					ID:          uuid.New(),
					AccountID:   accountID,
					AccountName: account.Name,
					Type:        "sla_breach",
					Title:       fmt.Sprintf("Low SLA Compliance: %.1f%%", report.FirstResponseRate),
					Description: "Account SLA compliance below acceptable threshold",
					CreatedAt:   assignment.AssignedAt.Format("2006-01-02T15:04:05Z07:00"),
				}

				if report.FirstResponseRate < 70 {
					alert.Severity = "critical"
				} else {
					alert.Severity = "high"
				}

				alerts = append(alerts, alert)
			}
		}
	}

	return alerts, nil
}
