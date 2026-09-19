package application

import (
	"context"
	"fmt"
	"time"

	"github.com/omnira/omnira/internal/bpo/domain"
	"github.com/omnira/omnira/internal/bpo/ports"
)

// SLAService — serviço para compliance de SLA
type SLAService struct {
	ticketRepo ports.TicketRepository
}

// NewSLAService — cria novo SLAService
func NewSLAService(ticketRepo ports.TicketRepository) *SLAService {
	return &SLAService{
		ticketRepo: ticketRepo,
	}
}

// TicketSLAMetrics — métricas SLA de um ticket
type TicketSLAMetrics struct {
	ID                    domain.TicketID `json:"id"`
	Subject               string          `json:"subject"`
	Priority              string          `json:"priority"`
	Status                string          `json:"status"`
	FirstResponseTarget   string          `json:"first_response_target"`
	FirstResponseMet      bool            `json:"first_response_met"`
	FirstResponseHours    *float64        `json:"first_response_hours"`
	ResolutionTarget      string          `json:"resolution_target"`
	ResolutionMet         bool            `json:"resolution_met"`
	ResolutionTimeHours   *float64        `json:"resolution_time_hours"`
	Breached              bool            `json:"breached"`
	BreachedAt            *string         `json:"breached_at"`
	CreatedAt             string          `json:"created_at"`
}

// SLAComplianceReport — relatório de compliance SLA
type SLAComplianceReport struct {
	AccountID              domain.AccountID   `json:"account_id"`
	ReportGeneratedAt      string             `json:"report_generated_at"`
	TotalTickets           int                `json:"total_tickets"`
	CompletedTickets       int                `json:"completed_tickets"`
	FirstResponseMet       int                `json:"first_response_met"`
	FirstResponseViolated  int                `json:"first_response_violated"`
	ResolutionMet          int                `json:"resolution_met"`
	ResolutionViolated     int                `json:"resolution_violated"`
	FirstResponseRate      float64            `json:"first_response_rate"`
	ResolutionRate         float64            `json:"resolution_rate"`
	AverageFirstResponse   *float64           `json:"average_first_response_hours"`
	AverageResolutionTime  *float64           `json:"average_resolution_time_hours"`
	TicketMetrics          []TicketSLAMetrics `json:"tickets"`
}

// GetSLAComplianceReport — obtém relatório de compliance SLA
func (s *SLAService) GetSLAComplianceReport(ctx context.Context, accountID domain.AccountID, startDate, endDate time.Time) (*SLAComplianceReport, error) {
	// Para este MVP, vamos listar todos os tickets da account
	// Em produção, teríamos filtro por data
	tickets, err := s.ticketRepo.FindByAccount(ctx, accountID, 10000, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tickets: %w", err)
	}

	report := &SLAComplianceReport{
		AccountID:         accountID,
		ReportGeneratedAt: time.Now().UTC().Format(time.RFC3339),
		TotalTickets:      len(tickets),
		TicketMetrics:     []TicketSLAMetrics{},
	}

	var totalFirstResponse float64
	var totalResolution float64
	completedCount := 0

	for _, ticket := range tickets {
		metrics := TicketSLAMetrics{
			ID:       ticket.ID,
			Subject:  ticket.Subject,
			Priority: string(ticket.Priority),
			Status:   string(ticket.Status),
			CreatedAt: ticket.CreatedAt.Format(time.RFC3339),
		}

		// First Response
		if !ticket.SLAMetrics.FirstResponseTarget.IsZero() {
			metrics.FirstResponseTarget = ticket.SLAMetrics.FirstResponseTarget.Format(time.RFC3339)
			metrics.FirstResponseMet = ticket.SLAMetrics.FirstResponseMet

			if ticket.FirstResponseAt != nil {
				hours := ticket.FirstResponseAt.Sub(ticket.CreatedAt).Hours()
				metrics.FirstResponseHours = &hours
				totalFirstResponse += hours
			}

			if metrics.FirstResponseMet {
				report.FirstResponseMet++
			} else {
				report.FirstResponseViolated++
			}
		}

		// Resolution
		if !ticket.SLAMetrics.ResolutionTarget.IsZero() {
			metrics.ResolutionTarget = ticket.SLAMetrics.ResolutionTarget.Format(time.RFC3339)
			metrics.ResolutionMet = ticket.SLAMetrics.ResolutionMet

			if ticket.ResolvedAt != nil {
				hours := ticket.ResolvedAt.Sub(ticket.CreatedAt).Hours()
				metrics.ResolutionTimeHours = &hours
				totalResolution += hours
				completedCount++
			}

			if metrics.ResolutionMet {
				report.ResolutionMet++
			} else {
				report.ResolutionViolated++
			}
		}

		// Breach
		if ticket.SLAMetrics.BreachedAt != nil {
			metrics.Breached = true
			breachedStr := ticket.SLAMetrics.BreachedAt.Format(time.RFC3339)
			metrics.BreachedAt = &breachedStr
		}

		report.TicketMetrics = append(report.TicketMetrics, metrics)
	}

	// Calcular taxas
	report.CompletedTickets = completedCount

	if report.FirstResponseMet+report.FirstResponseViolated > 0 {
		report.FirstResponseRate = float64(report.FirstResponseMet) / float64(report.FirstResponseMet+report.FirstResponseViolated) * 100
	}

	if report.ResolutionMet+report.ResolutionViolated > 0 {
		report.ResolutionRate = float64(report.ResolutionMet) / float64(report.ResolutionMet+report.ResolutionViolated) * 100
	}

	// Calcular médias
	if report.FirstResponseMet+report.FirstResponseViolated > 0 {
		avg := totalFirstResponse / float64(report.FirstResponseMet+report.FirstResponseViolated)
		report.AverageFirstResponse = &avg
	}

	if completedCount > 0 {
		avg := totalResolution / float64(completedCount)
		report.AverageResolutionTime = &avg
	}

	return report, nil
}

// SLASummary — sumário de SLA para dashboard
type SLASummary struct {
	AccountID            domain.AccountID `json:"account_id"`
	CompliancePercentage float64          `json:"compliance_percentage"`
	TotalViolations      int              `json:"total_violations"`
	CriticalViolations   int              `json:"critical_violations"`
	HighViolations       int              `json:"high_violations"`
	Status               string           `json:"status"` // "compliant", "warning", "critical"
}

// GetSLASummary — obtém sumário de SLA
func (s *SLAService) GetSLASummary(ctx context.Context, accountID domain.AccountID) (*SLASummary, error) {
	tickets, err := s.ticketRepo.FindByAccount(ctx, accountID, 10000, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tickets: %w", err)
	}

	summary := &SLASummary{
		AccountID: accountID,
	}

	if len(tickets) == 0 {
		summary.Status = "compliant"
		summary.CompliancePercentage = 100
		return summary, nil
	}

	var totalChecked int
	var totalMet int

	for _, ticket := range tickets {
		if ticket.SLAMetrics.FirstResponseMet || ticket.SLAMetrics.ResolutionMet {
			totalChecked++
			if ticket.SLAMetrics.FirstResponseMet && ticket.SLAMetrics.ResolutionMet {
				totalMet++
			}
		}

		if !ticket.SLAMetrics.FirstResponseMet {
			summary.TotalViolations++
			if ticket.Priority == domain.TicketPriorityCritical {
				summary.CriticalViolations++
			} else if ticket.Priority == domain.TicketPriorityHigh {
				summary.HighViolations++
			}
		}

		if !ticket.SLAMetrics.ResolutionMet {
			summary.TotalViolations++
			if ticket.Priority == domain.TicketPriorityCritical {
				summary.CriticalViolations++
			} else if ticket.Priority == domain.TicketPriorityHigh {
				summary.HighViolations++
			}
		}
	}

	if totalChecked > 0 {
		summary.CompliancePercentage = float64(totalMet) / float64(totalChecked) * 100
	}

	// Determinar status
	if summary.CompliancePercentage >= 95 && summary.CriticalViolations == 0 {
		summary.Status = "compliant"
	} else if summary.CompliancePercentage >= 85 || summary.CriticalViolations <= 1 {
		summary.Status = "warning"
	} else {
		summary.Status = "critical"
	}

	return summary, nil
}
