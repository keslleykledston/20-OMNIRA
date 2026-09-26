package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	crmevidenceports "github.com/omnira/omnira/internal/crmevidence/ports"
	ticketsapplication "github.com/omnira/omnira/internal/tickets/application"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
)

// PRODUCT.7B2B: proves CreateTicket's post-success evidence hook is truly
// best-effort — it records durable Contact<->Company evidence only for the
// two outcomes that prove a real success, derives contact_id server-side
// (never from the browser), and can NEVER alter the ticket Result/HTTP
// response the caller already decided, even when evidence persistence
// itself fails.

type fakeConversationContacts struct {
	byID map[uuid.UUID]uuid.UUID
	err  error
}

func (f *fakeConversationContacts) ContactIDFor(ctx context.Context, conversationID uuid.UUID) (uuid.UUID, bool, error) {
	if f.err != nil {
		return uuid.Nil, false, f.err
	}
	id, ok := f.byID[conversationID]
	return id, ok, nil
}

type fakeEvidenceStore struct {
	calls int
	last  crmevidenceports.RecordTicketSelectionInput
	err   error
}

func (f *fakeEvidenceStore) RecordTicketSelection(ctx context.Context, in crmevidenceports.RecordTicketSelectionInput) error {
	f.calls++
	f.last = in
	return f.err
}

// R/P (7B2B checklist): a fresh OutcomeCreated records evidence with the
// server-derived contact_id and the runtime's own ConnectionID — never a
// browser-supplied contact_id (there is none in the request contract).
func TestCreateTicketHTTPRecordsEvidenceOnFreshCreate(t *testing.T) {
	h := newExternalTicketHarness(t)
	wantConnection := uuid.New()
	h.runtime.connectionID = wantConnection
	wantContact := uuid.New()
	contacts := &fakeConversationContacts{byID: map[uuid.UUID]uuid.UUID{h.convID: wantContact}}
	evidence := &fakeEvidenceStore{}
	h.handler.SetConversationContactReader(contacts)
	h.handler.SetEvidenceStore(evidence)

	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if evidence.calls != 1 {
		t.Fatalf("evidence.calls = %d, want 1", evidence.calls)
	}
	if evidence.last.ContactID != wantContact {
		t.Fatalf("evidence contact_id = %s, want %s (server-derived, never from the request)", evidence.last.ContactID, wantContact)
	}
	if evidence.last.ConnectionID != wantConnection {
		t.Fatalf("evidence connection_id = %s, want %s", evidence.last.ConnectionID, wantConnection)
	}
	if evidence.last.ExternalCompanyID != httpTestCompanyID {
		t.Fatalf("evidence company = %q, want %q", evidence.last.ExternalCompanyID, httpTestCompanyID)
	}
	if evidence.last.ActorUserID != h.actorID {
		t.Fatalf("evidence actor = %s, want %s", evidence.last.ActorUserID, h.actorID)
	}
}

// S: a replay (OutcomeReplaySuccess) also records/advances evidence — not
// just a fresh create.
func TestCreateTicketHTTPRecordsEvidenceOnReplay(t *testing.T) {
	h := newExternalTicketHarness(t)
	h.runtime.connectionID = uuid.New()
	contacts := &fakeConversationContacts{byID: map[uuid.UUID]uuid.UUID{h.convID: uuid.New()}}
	evidence := &fakeEvidenceStore{}
	h.handler.SetConversationContactReader(contacts)
	h.handler.SetEvidenceStore(evidence)

	body := `{"selected_customer_external_id":"` + httpTestCompanyID + `","subject":"s"}`
	rec1 := httptest.NewRecorder()
	h.mux().ServeHTTP(rec1, h.request(body, validKey))
	if rec1.Code != http.StatusCreated {
		t.Fatalf("first call status = %d, want 201: %s", rec1.Code, rec1.Body.String())
	}
	rec2 := httptest.NewRecorder()
	h.mux().ServeHTTP(rec2, h.request(body, validKey))
	if rec2.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200 (replay_success): %s", rec2.Code, rec2.Body.String())
	}
	if evidence.calls != 2 {
		t.Fatalf("evidence.calls = %d, want 2 (fresh create + replay, both eligible outcomes)", evidence.calls)
	}
}

// T: outcomes that do NOT prove a real success never record evidence.
// Uses the already-linked branch (OutcomeAlreadyLinked), which never even
// reaches company validation.
func TestCreateTicketHTTPDoesNotRecordEvidenceForIneligibleOutcome(t *testing.T) {
	h := newExternalTicketHarness(t)
	provider, externalID := "k3g", "28182"
	svc := ticketsapplication.NewService(h.perms, h.conversation, newHTTPFakeAttempts(),
		&httpFakeLocalTickets{candidate: &ticketsdomain.Ticket{ID: uuid.New(), Provider: &provider, ExternalTicketID: &externalID}}, h.runtime)
	h.handler.SetExternalTicketService(svc)
	contacts := &fakeConversationContacts{byID: map[uuid.UUID]uuid.UUID{h.convID: uuid.New()}}
	evidence := &fakeEvidenceStore{}
	h.handler.SetConversationContactReader(contacts)
	h.handler.SetEvidenceStore(evidence)

	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (already_linked): %s", rec.Code, rec.Body.String())
	}
	if evidence.calls != 0 {
		t.Fatalf("evidence.calls = %d, want 0 for a non-eligible outcome", evidence.calls)
	}
}

// Q/U: evidence-write failure NEVER alters the ticket's own HTTP outcome —
// the response is byte-for-byte the same 201 with the same body regardless
// of whether the evidence store succeeds or fails. No extra provider
// write happens either way (the fake evidence store never touches a
// network).
func TestCreateTicketHTTPEvidenceFailureNeverAltersTicketResult(t *testing.T) {
	// local_ticket_id is randomized per harness (httpFakeLocalTickets uses
	// uuid.New()) and irrelevant to this proof — every OTHER field of the
	// response must be identical regardless of evidence outcome.
	run := func(evidenceErr error) externalTicketCreateResponse {
		h := newExternalTicketHarness(t)
		h.runtime.connectionID = uuid.New()
		contacts := &fakeConversationContacts{byID: map[uuid.UUID]uuid.UUID{h.convID: uuid.New()}}
		h.handler.SetConversationContactReader(contacts)
		h.handler.SetEvidenceStore(&fakeEvidenceStore{err: evidenceErr})
		rec := httptest.NewRecorder()
		h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
		}
		var body externalTicketCreateResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if strings.Contains(rec.Body.String(), "evidence") {
			t.Fatalf("evidence failure detail must never leak into the ticket HTTP response: %s", rec.Body.String())
		}
		return body
	}
	ok := run(nil)
	failed := run(errors.New("evidence store: connection reset"))
	if ok.ExternalTicketID != failed.ExternalTicketID || ok.Provider != failed.Provider ||
		ok.SyncStatus != failed.SyncStatus || ok.Replayed != failed.Replayed {
		t.Fatalf("ticket result fields differ based on evidence outcome: ok=%+v failed=%+v", ok, failed)
	}
}

// The hook must also be a safe no-op when evidence dependencies are simply
// not wired (e.g. an older deployment before PRODUCT.7B2B) — never a
// crash, never a changed ticket outcome.
func TestCreateTicketHTTPSkipsEvidenceWhenDependenciesUnwired(t *testing.T) {
	h := newExternalTicketHarness(t) // SetConversationContactReader/SetEvidenceStore never called
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}

// D: an eligible outcome that unexpectedly lacks ConnectionID (should
// never happen when the runtime resolver is correctly wired, but the hook
// must fail closed rather than attempt an INSERT that would only fail on
// the connection_id FK) skips evidence without affecting the ticket
// outcome.
func TestCreateTicketHTTPEvidenceSkippedWhenConnectionIDMissing(t *testing.T) {
	h := newExternalTicketHarness(t) // h.runtime.connectionID left at uuid.Nil
	evidence := &fakeEvidenceStore{}
	h.handler.SetConversationContactReader(&fakeConversationContacts{byID: map[uuid.UUID]uuid.UUID{h.convID: uuid.New()}})
	h.handler.SetEvidenceStore(evidence)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if evidence.calls != 0 {
		t.Fatalf("evidence.calls = %d, want 0 when ConnectionID is missing", evidence.calls)
	}
}

// contact_id resolution failure is also best-effort: it must not affect
// the ticket outcome.
func TestCreateTicketHTTPEvidenceSkippedWhenContactLookupFails(t *testing.T) {
	h := newExternalTicketHarness(t)
	h.runtime.connectionID = uuid.New()
	evidence := &fakeEvidenceStore{}
	h.handler.SetConversationContactReader(&fakeConversationContacts{err: errors.New("db down")})
	h.handler.SetEvidenceStore(evidence)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(`{"selected_customer_external_id":"`+httpTestCompanyID+`","subject":"s"}`, validKey))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if evidence.calls != 0 {
		t.Fatalf("evidence.calls = %d, want 0 when contact_id cannot be derived", evidence.calls)
	}
}
