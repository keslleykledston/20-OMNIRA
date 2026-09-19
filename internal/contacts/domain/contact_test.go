package domain_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/contacts/domain"
)

func TestNewContactRequiresCanonicalPhoneAndTenant(t *testing.T) {
	if _, err := domain.NewContact(uuid.Nil, "+5511999999999", "Ana"); err == nil {
		t.Fatal("nil tenant accepted")
	}
	if _, err := domain.NewContact(uuid.New(), "5511999999999", "Ana"); err == nil {
		t.Fatal("non-E.164 phone accepted")
	}
	contact, err := domain.NewContact(uuid.New(), "+5511999999999", "")
	if err != nil || contact.DisplayName != contact.PhoneE164 || contact.Status != domain.StatusActive {
		t.Fatalf("unexpected contact: %+v, %v", contact, err)
	}
}

func TestContactStateChangesUpdateTimestamp(t *testing.T) {
	contact, err := domain.NewContact(uuid.New(), "+5511999999999", "Ana")
	if err != nil {
		t.Fatal(err)
	}
	before := contact.UpdatedAt
	contact.Block()
	if contact.Status != domain.StatusBlocked || !contact.UpdatedAt.After(before) {
		t.Fatal("block did not update state/timestamp")
	}
	if err := contact.SetDisplayName("Ana Silva"); err != nil || contact.DisplayName != "Ana Silva" {
		t.Fatalf("display name update failed: %v", err)
	}
}
