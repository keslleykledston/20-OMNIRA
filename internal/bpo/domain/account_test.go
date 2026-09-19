package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewAccount(t *testing.T) {
	tenantID := uuid.New()
	operatorID := uuid.New()
	createdBy := uuid.New()

	slaConfig := SLAConfiguration{
		FirstResponseTime: 30,
		ResolutionTime:    24,
		ResponseTimeSLO:   95,
		AvailabilitySLO:   99,
	}

	account := NewAccount(
		tenantID, operatorID, createdBy,
		"Support Team", "Main support account",
		AccountTypeOperator,
		50, 10000,
		slaConfig,
	)

	if account.Status != AccountStatusActive {
		t.Errorf("new account should be active")
	}

	if account.MaxTeamMembers != 50 {
		t.Errorf("max team members should be 50")
	}

	if account.SLAConfig.FirstResponseTime != 30 {
		t.Errorf("SLA first response time should be 30")
	}
}

func TestAccountSuspend(t *testing.T) {
	account := NewAccount(uuid.New(), uuid.New(), uuid.New(), "test", "", AccountTypeOperator, 10, 1000, SLAConfiguration{})

	suspendedBy := uuid.New()
	account.Suspend(suspendedBy, "Payment overdue")

	if account.Status != AccountStatusSuspended {
		t.Errorf("account should be suspended")
	}

	if account.Metadata["suspension_reason"] != "Payment overdue" {
		t.Errorf("suspension reason not recorded")
	}
}

func TestAccountReactivate(t *testing.T) {
	account := NewAccount(uuid.New(), uuid.New(), uuid.New(), "test", "", AccountTypeOperator, 10, 1000, SLAConfiguration{})

	account.Suspend(uuid.New(), "Test suspension")
	account.Reactivate(uuid.New())

	if account.Status != AccountStatusActive {
		t.Errorf("account should be active")
	}

	if _, exists := account.Metadata["suspension_reason"]; exists {
		t.Errorf("suspension reason should be cleared")
	}
}

func TestAccountCanAddTeamMember(t *testing.T) {
	account := NewAccount(uuid.New(), uuid.New(), uuid.New(), "test", "", AccountTypeOperator, 5, 1000, SLAConfiguration{})

	if !account.CanAddTeamMember(3) {
		t.Errorf("should allow adding member when count < max")
	}

	if account.CanAddTeamMember(5) {
		t.Errorf("should not allow adding member when at limit")
	}
}

func TestAccountCanCreateTicket(t *testing.T) {
	account := NewAccount(uuid.New(), uuid.New(), uuid.New(), "test", "", AccountTypeOperator, 10, 100, SLAConfiguration{})

	if !account.CanCreateTicket(50) {
		t.Errorf("should allow creating ticket when count < max")
	}

	if account.CanCreateTicket(100) {
		t.Errorf("should not allow creating ticket when at limit")
	}
}
