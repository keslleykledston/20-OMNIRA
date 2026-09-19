package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewTicket(t *testing.T) {
	accountID := uuid.New()
	tenantID := uuid.New()
	customerID := uuid.New()

	slaConfig := SLAConfiguration{
		FirstResponseTime: 30,
		ResolutionTime:    24,
	}

	ticket := NewTicket(accountID, tenantID, customerID, "API Error", "500 error", TicketPriorityHigh, slaConfig)

	if ticket.Status != TicketStatusOpen {
		t.Errorf("new ticket should be open")
	}

	if ticket.Priority != TicketPriorityHigh {
		t.Errorf("priority should be high")
	}

	if ticket.SLAMetrics.FirstResponseTarget.Before(ticket.CreatedAt) {
		t.Errorf("first response target should be in future")
	}
}

func TestTicketAssign(t *testing.T) {
	ticket := NewTicket(uuid.New(), uuid.New(), uuid.New(), "test", "", TicketPriorityMedium, SLAConfiguration{})
	agentID := uuid.New()

	ticket.Assign(agentID)

	if ticket.Status != TicketStatusInProgress {
		t.Errorf("status should be in_progress after assignment")
	}

	if ticket.AssignedToID == nil || *ticket.AssignedToID != agentID {
		t.Errorf("assigned agent should match")
	}
}

func TestTicketFirstResponse(t *testing.T) {
	slaConfig := SLAConfiguration{FirstResponseTime: 30}
	ticket := NewTicket(uuid.New(), uuid.New(), uuid.New(), "test", "", TicketPriorityHigh, slaConfig)

	ticket.RecordFirstResponse()

	if ticket.FirstResponseAt == nil {
		t.Errorf("first response time should be recorded")
	}

	if !ticket.SLAMetrics.FirstResponseMet {
		t.Errorf("first response should be met immediately after creation")
	}
}

func TestTicketResolve(t *testing.T) {
	ticket := NewTicket(uuid.New(), uuid.New(), uuid.New(), "test", "", TicketPriorityMedium, SLAConfiguration{ResolutionTime: 24})

	ticket.Resolve()

	if ticket.Status != TicketStatusResolved {
		t.Errorf("status should be resolved")
	}

	if ticket.ResolvedAt == nil {
		t.Errorf("resolved time should be recorded")
	}

	if ticket.SLAMetrics.ResolutionTimeHours == nil {
		t.Errorf("resolution time should be calculated")
	}
}

func TestTicketClose(t *testing.T) {
	ticket := NewTicket(uuid.New(), uuid.New(), uuid.New(), "test", "", TicketPriorityLow, SLAConfiguration{})

	ticket.Close()

	if ticket.Status != TicketStatusClosed {
		t.Errorf("status should be closed")
	}

	if ticket.ClosedAt == nil {
		t.Errorf("closed time should be recorded")
	}
}

func TestTicketIsOverdue(t *testing.T) {
	// Create ticket with very short resolution time
	slaConfig := SLAConfiguration{ResolutionTime: 0} // 0 hours = already overdue
	ticket := NewTicket(uuid.New(), uuid.New(), uuid.New(), "test", "", TicketPriorityHigh, slaConfig)

	if !ticket.IsOverdue() {
		t.Errorf("ticket should be overdue with 0-hour SLA")
	}

	// Close ticket - should no longer be overdue (status is closed)
	ticket.Close()
	if ticket.IsOverdue() {
		t.Errorf("closed ticket should not be overdue")
	}
}

func TestTicketDaysOpen(t *testing.T) {
	ticket := NewTicket(uuid.New(), uuid.New(), uuid.New(), "test", "", TicketPriorityMedium, SLAConfiguration{})

	// Should be 0 days (just created)
	if ticket.DaysOpen() != 0 {
		t.Errorf("just created ticket should be 0 days old")
	}

	// Manually set created time to 2 days ago
	ticket.CreatedAt = time.Now().Add(-48 * time.Hour)

	days := ticket.DaysOpen()
	if days < 2 {
		t.Errorf("expected at least 2 days, got %d", days)
	}
}

func TestTicketSLABreach(t *testing.T) {
	// Create ticket with SLA ending very soon (in the past)
	slaConfig := SLAConfiguration{ResolutionTime: 0}
	ticket := NewTicket(uuid.New(), uuid.New(), uuid.New(), "test", "", TicketPriorityCritical, slaConfig)

	// Wait a bit and resolve
	time.Sleep(10 * time.Millisecond)
	ticket.Resolve()

	if ticket.SLAMetrics.ResolutionMet {
		t.Errorf("SLA should not be met when resolved after deadline")
	}

	if ticket.SLAMetrics.BreachedAt == nil {
		t.Errorf("breach time should be recorded")
	}
}
