package application

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
	"github.com/omnira/omnira/internal/tickets/ports"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// ADR-0018 Wave 7: the ticket targets the OMNIRA account of the company the DIRECTORY validated.

type fakeAccounts struct {
	calls   int32
	id      uuid.UUID
	err     error
	gotConn uuid.UUID
	gotComp ports.Company
}

func (f *fakeAccounts) ResolveForCompany(_ context.Context, _, conn uuid.UUID, c ports.Company) (uuid.UUID, error) {
	atomic.AddInt32(&f.calls, 1)
	f.gotConn, f.gotComp = conn, c
	return f.id, f.err
}

// fakeAccountTickets is fakeLocalTickets plus the optional account setter.
type fakeAccountTickets struct {
	*fakeLocalTickets
	setCalls int32
	setID    uuid.UUID
	setTo    uuid.UUID
	setErr   error
}

func (f *fakeAccountTickets) SetCustomerAccount(_ context.Context, ticketID, accountID uuid.UUID) error {
	atomic.AddInt32(&f.setCalls, 1)
	f.setID, f.setTo = ticketID, accountID
	return f.setErr
}

func newAccountHarness(accounts *fakeAccounts) (*harness, *fakeAccountTickets) {
	h := newHarness(false, uuid.Nil)
	h.companies.companies = []ports.Company{{ExternalID: testCompanyID, Name: "ACME Telecom (diretório)", CNPJ: "11.111.111/0001-11", Active: true}}
	at := &fakeAccountTickets{fakeLocalTickets: h.localTickets}
	h.svc = NewService(h.perms, h.conversation, h.attempts, at, h.runtime).WithAccounts(accounts)
	return h, at
}

func TestTicketTargetsTheAccountOfTheDirectoryCompany(t *testing.T) {
	acc := &fakeAccounts{id: uuid.New()}
	h, tickets := newAccountHarness(acc)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil || res.Outcome != OutcomeCreated {
		t.Fatalf("create: %+v %v", res, err)
	}
	// the company handed to the resolver is the DIRECTORY's (name and all), on the runtime's connection
	if acc.calls != 1 || acc.gotComp.ExternalID != testCompanyID || acc.gotComp.Name != "ACME Telecom (diretório)" || acc.gotConn != testConnectionID {
		t.Fatalf("resolver got %+v on %s (calls=%d)", acc.gotComp, acc.gotConn, acc.calls)
	}
	if tickets.setCalls != 1 || tickets.setID != h.localTickets.candidate.ID || tickets.setTo != acc.id || res.CustomerAccountID != acc.id {
		t.Fatalf("the ticket must target the resolved account: set=%d to=%v result=%v", tickets.setCalls, tickets.setTo, res.CustomerAccountID)
	}
	// the same idempotent request replayed still reports (and keeps) the same account
	res2, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil || res2.Outcome != OutcomeReplaySuccess || res2.CustomerAccountID != acc.id {
		t.Fatalf("replay: %+v %v", res2, err)
	}
	if h.ticketing.calls != 1 {
		t.Fatalf("the provider must be called exactly once, got %d", h.ticketing.calls)
	}
}

func TestAnInvalidOrInactiveCompanyNeverReachesTheAccountResolver(t *testing.T) {
	acc := &fakeAccounts{id: uuid.New()}
	h, tickets := newAccountHarness(acc)
	h.companies.companies = []ports.Company{{ExternalID: testCompanyID, Name: "Inativa", Active: false}}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	if _, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd); !errors.Is(err, ErrInvalidCompany) {
		t.Fatalf("inactive company: %v", err)
	}
	cmd2 := testCommand(func(c *CreateExternalTicketCommand) { c.SelectedCustomerExternalID = "not-in-the-directory" })
	h.conversation.assignedTo = &cmd2.ActorUserID
	if _, err := h.svc.CreateExternalTicket(withTenantContext(cmd2.TenantID, cmd2.ActorUserID), cmd2); !errors.Is(err, ErrInvalidCompany) {
		t.Fatalf("unknown company: %v", err)
	}
	if acc.calls != 0 || tickets.setCalls != 0 || h.ticketing.calls != 0 {
		t.Fatalf("nothing may be materialized or written: resolver=%d set=%d provider=%d", acc.calls, tickets.setCalls, h.ticketing.calls)
	}
}

func TestAlreadyLinkedTicketIsNotRetargeted(t *testing.T) {
	acc := &fakeAccounts{id: uuid.New()}
	h, tickets := newAccountHarness(acc)
	provider, externalID := "k3g", "28182"
	h.localTickets.candidate = &ticketsdomain.Ticket{ID: uuid.New(), Provider: &provider, ExternalTicketID: &externalID}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil || res.Outcome != OutcomeAlreadyLinked {
		t.Fatalf("%+v %v", res, err)
	}
	if acc.calls != 0 || tickets.setCalls != 0 || res.CustomerAccountID != uuid.Nil {
		t.Fatal("an already-linked ticket belongs to whatever company it was created for: no resolution, no projection")
	}
}

func TestAProviderRejectionLeavesTheTicketWithoutAnAccount(t *testing.T) {
	acc := &fakeAccounts{id: uuid.New()}
	h, tickets := newAccountHarness(acc)
	h.ticketing.result, h.ticketing.err = nil, &connectors.TicketingError{Code: connectors.TicketingValidationError, Message: "bad"}
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil || res.Outcome != OutcomeDefinitiveFailure {
		t.Fatalf("%+v %v", res, err)
	}
	if tickets.setCalls != 0 || res.CustomerAccountID != uuid.Nil {
		t.Fatal("no ticket was created: it must not target an account")
	}
}

func TestAccountResolutionFailureStopsBeforeTheProviderWrite(t *testing.T) {
	acc := &fakeAccounts{err: errors.New("db down")}
	h, _ := newAccountHarness(acc)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	if _, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd); err == nil {
		t.Fatal("expected an error")
	}
	if h.ticketing.calls != 0 || len(h.attempts.byKey) != 0 {
		t.Fatalf("no attempt and no provider write before the local resolution succeeds: provider=%d attempts=%d", h.ticketing.calls, len(h.attempts.byKey))
	}
}

func TestAFailedAccountProjectionNeverFailsAnAlreadyCreatedTicket(t *testing.T) {
	acc := &fakeAccounts{id: uuid.New()}
	h, tickets := newAccountHarness(acc)
	tickets.setErr = errors.New("projection failed")
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil || res.Outcome != OutcomeCreated || res.ExternalTicketID == "" {
		t.Fatalf("the external ticket exists and is recorded: %+v %v", res, err)
	}
	if res.CustomerAccountID != uuid.Nil {
		t.Fatal("the result must not claim an account that was not stored")
	}
}

func TestWithoutAnAccountResolverTicketsKeepWorkingAsBefore(t *testing.T) {
	h := newHarness(false, uuid.Nil)
	cmd := testCommand(nil)
	h.conversation.assignedTo = &cmd.ActorUserID
	res, err := h.svc.CreateExternalTicket(withTenantContext(cmd.TenantID, cmd.ActorUserID), cmd)
	if err != nil || res.Outcome != OutcomeCreated || res.CustomerAccountID != uuid.Nil {
		t.Fatalf("%+v %v", res, err)
	}
}
