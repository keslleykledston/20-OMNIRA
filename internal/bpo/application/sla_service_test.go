package application

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/bpo/domain"
)

func TestGetSLAComplianceReport(t *testing.T) {
	ticketRepo := NewMockTicketRepository()
	svc := NewSLAService(ticketRepo)

	ctx := context.Background()
	accountID := uuid.New()

	// Criar ticket com SLA met
	ticket := domain.NewTicket(accountID, uuid.New(), uuid.New(), "Test", "", domain.TicketPriorityMedium, domain.SLAConfiguration{ResolutionTime: 24})
	ticket.RecordFirstResponse()
	ticket.Resolve()

	ticketRepo.Store(ctx, ticket)

	report, err := svc.GetSLAComplianceReport(ctx, accountID, time.Now().Add(-24*time.Hour), time.Now())

	if err != nil {
		t.Errorf("failed to get report: %v", err)
	}

	if report == nil {
		t.Errorf("report should not be nil")
	}

	if report.TotalTickets != 1 {
		t.Errorf("expected 1 ticket, got %d", report.TotalTickets)
	}
}

func TestGetSLASummary(t *testing.T) {
	ticketRepo := NewMockTicketRepository()
	svc := NewSLAService(ticketRepo)

	ctx := context.Background()
	accountID := uuid.New()

	// Criar ticket
	ticket := domain.NewTicket(accountID, uuid.New(), uuid.New(), "Test", "", domain.TicketPriorityMedium, domain.SLAConfiguration{ResolutionTime: 24})
	ticketRepo.Store(ctx, ticket)

	summary, err := svc.GetSLASummary(ctx, accountID)

	if err != nil {
		t.Errorf("failed to get summary: %v", err)
	}

	if summary == nil {
		t.Errorf("summary should not be nil")
	}

	if summary.AccountID != accountID {
		t.Errorf("account id should match")
	}
}

func TestSLASummaryStatus(t *testing.T) {
	ticketRepo := NewMockTicketRepository()
	svc := NewSLAService(ticketRepo)

	ctx := context.Background()
	accountID := uuid.New()

	summary, err := svc.GetSLASummary(ctx, accountID)

	if err != nil {
		t.Errorf("failed to get summary: %v", err)
	}

	// Empty account should be compliant
	if summary.Status != "compliant" {
		t.Errorf("expected compliant status for empty account, got %s", summary.Status)
	}

	if summary.CompliancePercentage != 100 {
		t.Errorf("expected 100%% compliance for empty account, got %.2f", summary.CompliancePercentage)
	}
}
