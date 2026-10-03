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

func TestPlaceholderTicketRule(t *testing.T) {
	external := "EXT-1"
	cases := []struct {
		name        string
		subject     string
		external    *string
		placeholder bool
	}{
		{"implicit ticket from the inbound flow", "", nil, true},
		{"whitespace-only subject is still implicit", "  \t\n", nil, true},
		{"a subject makes it real", "Problema com pagamento", nil, false},
		{"an ERP link makes it real even without a subject", "", &external, false},
		{"subject and ERP link", "Chamado", &external, false},
	}
	for _, c := range cases {
		ticket, err := domain.NewTicket(uuid.New(), uuid.New(), c.subject)
		if err != nil {
			t.Fatal(err)
		}
		ticket.ExternalTicketID = c.external
		if got := ticket.IsPlaceholder(); got != c.placeholder {
			t.Errorf("%s: IsPlaceholder() = %v, want %v", c.name, got, c.placeholder)
		}
	}
}

func TestRealTicketSQLQualifiesTheAlias(t *testing.T) {
	if got, want := domain.RealTicketSQL(""), "(external_ticket_id IS NOT NULL OR btrim(subject) <> '')"; got != want {
		t.Errorf("no alias: %q, want %q", got, want)
	}
	if got, want := domain.RealTicketSQL("t"), "(t.external_ticket_id IS NOT NULL OR btrim(t.subject) <> '')"; got != want {
		t.Errorf("alias t: %q, want %q", got, want)
	}
}
