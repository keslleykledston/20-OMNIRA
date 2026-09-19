package domain_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tickets/domain"
)

func TestTicketLifecycle(t *testing.T) {
	ticket, err := domain.NewTicket(uuid.New(), uuid.New(), "Atendimento")
	if err != nil || ticket.Status != domain.StatusOpen || ticket.Priority != domain.PriorityMedium {
		t.Fatalf("unexpected ticket: %+v %v", ticket, err)
	}
	ticket.Resolve()
	if ticket.Status != domain.StatusResolved || ticket.ResolvedAt == nil {
		t.Fatal("ticket did not resolve")
	}
}
