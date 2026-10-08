package adapters

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	ticketsapplication "github.com/omnira/omnira/internal/tickets/application"
	ticketsdomain "github.com/omnira/omnira/internal/tickets/domain"
)

type noticeCall struct {
	conversation, localTicket uuid.UUID
	number                    string
}

type fakeTicketNotifier struct {
	calls []noticeCall
	err   error
}

func (f *fakeTicketNotifier) NotifyTicketOpened(_ context.Context, conversationID, localTicketID uuid.UUID, number string) error {
	f.calls = append(f.calls, noticeCall{conversationID, localTicketID, number})
	return f.err
}

const noticeBody = `{"selected_customer_external_id":"` + httpTestCompanyID + `","subject":"s"}`

// A created ticket tells the customer, in the same conversation, under the number the CRM/ERP gave it.
func TestCreateTicketHTTPNotifiesTheCustomerOnCreate(t *testing.T) {
	h := newExternalTicketHarness(t)
	n := &fakeTicketNotifier{}
	h.handler.SetTicketOpenNotifier(n)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(noticeBody, validKey))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(n.calls) != 1 || n.calls[0].conversation != h.convID || n.calls[0].number != "28180" || n.calls[0].localTicket == uuid.Nil {
		t.Fatalf("notice calls: %+v", n.calls)
	}
}

// A replay asks again with the same ticket id, so a notice that failed once gets another chance and the notifier's own
// key (the local ticket) keeps it from being sent twice.
func TestCreateTicketHTTPRetriesTheNoticeOnReplayWithTheSameTicketKey(t *testing.T) {
	h := newExternalTicketHarness(t)
	n := &fakeTicketNotifier{}
	h.handler.SetTicketOpenNotifier(n)
	for i, want := range []int{http.StatusCreated, http.StatusOK} {
		rec := httptest.NewRecorder()
		h.mux().ServeHTTP(rec, h.request(noticeBody, validKey))
		if rec.Code != want {
			t.Fatalf("call %d: status = %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	if len(n.calls) != 2 || n.calls[0].localTicket != n.calls[1].localTicket {
		t.Fatalf("notice calls: %+v", n.calls)
	}
}

// Only a ticket that really exists is announced: not a provider rejection, not an existing link, not an unresolved write.
func TestCreateTicketHTTPDoesNotNotifyWhenNoTicketWasOpened(t *testing.T) {
	t.Run("already linked", func(t *testing.T) {
		h := newExternalTicketHarness(t)
		provider, externalID := "k3g", "28182"
		h.handler.SetExternalTicketService(ticketsapplication.NewService(h.perms, h.conversation, newHTTPFakeAttempts(),
			&httpFakeLocalTickets{candidate: &ticketsdomain.Ticket{ID: uuid.New(), Provider: &provider, ExternalTicketID: &externalID}}, h.runtime))
		n := &fakeTicketNotifier{}
		h.handler.SetTicketOpenNotifier(n)
		rec := httptest.NewRecorder()
		h.mux().ServeHTTP(rec, h.request(noticeBody, validKey))
		if rec.Code != http.StatusConflict || len(n.calls) != 0 {
			t.Fatalf("status=%d calls=%+v", rec.Code, n.calls)
		}
	})
	t.Run("provider rejected", func(t *testing.T) {
		h := newExternalTicketHarness(t)
		h.ticketing.result = nil
		h.ticketing.err = errors.New("rejected")
		n := &fakeTicketNotifier{}
		h.handler.SetTicketOpenNotifier(n)
		rec := httptest.NewRecorder()
		h.mux().ServeHTTP(rec, h.request(noticeBody, validKey))
		if rec.Code == http.StatusCreated || len(n.calls) != 0 {
			t.Fatalf("status=%d calls=%+v", rec.Code, n.calls)
		}
	})
	t.Run("invalid request", func(t *testing.T) {
		h := newExternalTicketHarness(t)
		n := &fakeTicketNotifier{}
		h.handler.SetTicketOpenNotifier(n)
		rec := httptest.NewRecorder()
		h.mux().ServeHTTP(rec, h.request(noticeBody, ""))
		if rec.Code != http.StatusBadRequest || len(n.calls) != 0 {
			t.Fatalf("status=%d calls=%+v", rec.Code, n.calls)
		}
	})
}

// The notice is a courtesy after a durable success: whatever goes wrong with it, the ticket answer is the same.
func TestCreateTicketHTTPNoticeFailureNeverAltersTheTicketResult(t *testing.T) {
	for name, err := range map[string]error{"window closed": errors.New("window"), "channel down": errors.New("channel"), "db": errors.New("pg: aborted")} {
		t.Run(name, func(t *testing.T) {
			h := newExternalTicketHarness(t)
			h.handler.SetTicketOpenNotifier(&fakeTicketNotifier{err: err})
			rec := httptest.NewRecorder()
			h.mux().ServeHTTP(rec, h.request(noticeBody, validKey))
			if rec.Code != http.StatusCreated {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// Without a notifier (feature off) the flow is exactly what it was.
func TestCreateTicketHTTPWithoutNotifierIsUnchanged(t *testing.T) {
	h := newExternalTicketHarness(t)
	rec := httptest.NewRecorder()
	h.mux().ServeHTTP(rec, h.request(noticeBody, validKey))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}
