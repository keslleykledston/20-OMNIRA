package connectors

import (
	"context"
	"testing"
)

func TestMockCRMConnector_FindCustomer(t *testing.T) {
	ctx := context.Background()
	crm := NewMockCRMConnector()

	// First call creates customer
	cid1, err := crm.FindCustomer(ctx, "test@example.com")
	if err != nil {
		t.Fatalf("FindCustomer failed: %v", err)
	}
	if cid1 == "" {
		t.Fatal("expected non-empty customer ID")
	}

	// Second call for same email returns same customer ID
	cid2, err := crm.FindCustomer(ctx, "test@example.com")
	if err != nil {
		t.Fatalf("FindCustomer failed: %v", err)
	}
	if cid1 != cid2 {
		t.Fatalf("expected same customer ID, got %s and %s", cid1, cid2)
	}

	// Different email gets different customer
	cid3, err := crm.FindCustomer(ctx, "other@example.com")
	if err != nil {
		t.Fatalf("FindCustomer failed: %v", err)
	}
	if cid3 == cid1 {
		t.Fatal("expected different customer IDs for different emails")
	}
}

func TestMockCRMConnector_CreateTicket(t *testing.T) {
	ctx := context.Background()
	crm := NewMockCRMConnector()

	custID, _ := crm.FindCustomer(ctx, "test@example.com")
	ticket, err := crm.CreateTicket(ctx, custID, "Test Subject")
	if err != nil {
		t.Fatalf("CreateTicket failed: %v", err)
	}
	if ticket == "" {
		t.Fatal("expected non-empty ticket ID")
	}

	// Verify ticket created
	retrieved, err := crm.GetTicket(ctx, ticket)
	if err != nil {
		t.Fatalf("GetTicket failed: %v", err)
	}
	if retrieved.Status != "open" {
		t.Fatalf("expected status 'open', got %s", retrieved.Status)
	}
	if retrieved.Subject != "Test Subject" {
		t.Fatalf("expected subject 'Test Subject', got %s", retrieved.Subject)
	}
}

func TestMockCRMConnector_UpdateTicket(t *testing.T) {
	ctx := context.Background()
	crm := NewMockCRMConnector()

	custID, _ := crm.FindCustomer(ctx, "test@example.com")
	ticketID, _ := crm.CreateTicket(ctx, custID, "Test")

	// Update to in_progress
	err := crm.UpdateTicket(ctx, ticketID, "in_progress")
	if err != nil {
		t.Fatalf("UpdateTicket failed: %v", err)
	}

	// Verify status changed
	ticket, _ := crm.GetTicket(ctx, ticketID)
	if ticket.Status != "in_progress" {
		t.Fatalf("expected status 'in_progress', got %s", ticket.Status)
	}

	// Update to resolved
	err = crm.UpdateTicket(ctx, ticketID, "resolved")
	if err != nil {
		t.Fatalf("UpdateTicket failed: %v", err)
	}

	ticket, _ = crm.GetTicket(ctx, ticketID)
	if ticket.Status != "resolved" {
		t.Fatalf("expected status 'resolved', got %s", ticket.Status)
	}
}

func TestMockCRMConnector_CloseTicket(t *testing.T) {
	ctx := context.Background()
	crm := NewMockCRMConnector()

	custID, _ := crm.FindCustomer(ctx, "test@example.com")
	ticketID, _ := crm.CreateTicket(ctx, custID, "Test")

	// Close ticket
	err := crm.CloseTicket(ctx, ticketID)
	if err != nil {
		t.Fatalf("CloseTicket failed: %v", err)
	}

	// Verify closed
	ticket, _ := crm.GetTicket(ctx, ticketID)
	if ticket.Status != "closed" {
		t.Fatalf("expected status 'closed', got %s", ticket.Status)
	}
}

func TestMockCRMConnector_Authenticate(t *testing.T) {
	ctx := context.Background()
	crm := NewMockCRMConnector()

	// Mock always passes
	err := crm.Authenticate(ctx, map[string]interface{}{})
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
}

func TestMockCRMConnector_Isolation(t *testing.T) {
	ctx := context.Background()
	crm := NewMockCRMConnector()

	// Multiple conversations/tenants using same CRM
	custID1, _ := crm.FindCustomer(ctx, "tenant1@example.com")
	custID2, _ := crm.FindCustomer(ctx, "tenant2@example.com")

	ticketID1, _ := crm.CreateTicket(ctx, custID1, "Tenant 1 Issue")
	ticketID2, _ := crm.CreateTicket(ctx, custID2, "Tenant 2 Issue")

	// Tickets are separate
	if ticketID1 == ticketID2 {
		t.Fatal("expected different ticket IDs")
	}

	// Verify isolation
	ticket1, _ := crm.GetTicket(ctx, ticketID1)
	ticket2, _ := crm.GetTicket(ctx, ticketID2)

	if ticket1.CustomerID == ticket2.CustomerID {
		t.Fatal("expected different customer IDs")
	}
	if ticket1.Subject != "Tenant 1 Issue" {
		t.Fatalf("expected Tenant 1 Issue, got %s", ticket1.Subject)
	}
	if ticket2.Subject != "Tenant 2 Issue" {
		t.Fatalf("expected Tenant 2 Issue, got %s", ticket2.Subject)
	}
}
