package adapters_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

// noticeEnv runs the notice the way production does: inside the tenant session of a request made by the operator.
type noticeEnv struct {
	*env
	mux  *http.ServeMux
	errs chan error
}

func newNoticeEnv(t *testing.T) *noticeEnv {
	e := newEnv(t)
	store := messagesadapters.NewPostgresOutboundStore(e.app)
	notice := messagesapplication.NewTicketOpenedNotice(messagesapplication.NewSender(store, channeladapters.NewPostgresPermissionChecker(e.app)), store)
	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(e.app), tenancyadapters.NewPostgresTenantRepository(e.app))
	mw := tenancyadapters.AuthorizationMiddleware(e.app, authz)
	n := &noticeEnv{env: e, mux: http.NewServeMux(), errs: make(chan error, 1)}
	n.mux.Handle("POST /t/{tenant_id}/c/{conversation_id}/t/{ticket_id}/{number}", mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conv, _ := uuid.Parse(r.PathValue("conversation_id"))
		ticket, _ := uuid.Parse(r.PathValue("ticket_id"))
		n.errs <- notice.NotifyTicketOpened(r.Context(), conv, ticket, r.PathValue("number"))
	})))
	return n
}

func (n *noticeEnv) notify(user, tenant, conv, ticket uuid.UUID, number string) error {
	req := httptest.NewRequest(http.MethodPost, "/t/"+tenant.String()+"/c/"+conv.String()+"/t/"+ticket.String()+"/"+url.PathEscape(number), nil)
	req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
	rec := httptest.NewRecorder()
	n.mux.ServeHTTP(rec, req)
	select {
	case err := <-n.errs:
		return err
	default:
		n.t.Fatalf("handler did not run (status %d: %s)", rec.Code, rec.Body.String())
		return nil
	}
}

func (n *noticeEnv) bodies(conv uuid.UUID) (out []string) {
	rows, err := n.seed.Query(context.Background(), `SELECT body FROM messages WHERE conversation_id=$1 AND direction='outbound' ORDER BY created_at`, conv)
	if err != nil {
		n.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			n.t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func TestTicketOpenedNoticeQueuesTheMessageForTheCustomer(t *testing.T) {
	n := newNoticeEnv(t)
	n.exec(`UPDATE contacts SET display_name='Maria Souza' WHERE id=(SELECT contact_id FROM conversations WHERE id=$1)`, n.convA)
	ticket := uuid.New()
	if err := n.notify(n.agent1, n.tenantA, n.convA, ticket, "28180"); err != nil {
		t.Fatal(err)
	}
	got := n.bodies(n.convA)
	if len(got) != 1 || got[0] != messagesapplication.RenderTicketOpened("Maria Souza", "28180") {
		t.Fatalf("bodies: %q", got)
	}
	if !strings.Contains(got[0], "Prezado Maria Souza,") || !strings.Contains(got[0], "Protocolo do chamado: 28180") {
		t.Fatalf("text: %q", got[0])
	}
	// attributed to the operator who opened the ticket, queued for delivery like any reply
	if c := n.count(`SELECT count(*) FROM messages WHERE conversation_id=$1 AND status='queued' AND sent_by_user_id=$2 AND tenant_id=$3`, n.convA, n.agent1, n.tenantA); c != 1 {
		t.Fatalf("queued by operator: %d", c)
	}
	if c := n.count(`SELECT count(*) FROM outbox_events WHERE tenant_id=$1 AND event_type='job.channel.send_text.v1'`, n.tenantA); c != 1 {
		t.Fatalf("delivery jobs: %d", c)
	}
}

// A replayed create, a retry or a double click must never message the customer twice for the same ticket; another
// ticket in the same conversation is a different notice.
func TestTicketOpenedNoticeIsSentOncePerTicket(t *testing.T) {
	n := newNoticeEnv(t)
	ticket := uuid.New()
	for i := 0; i < 3; i++ {
		if err := n.notify(n.agent1, n.tenantA, n.convA, ticket, "28180"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := n.bodies(n.convA); len(got) != 1 {
		t.Fatalf("customer got %d messages for one ticket", len(got))
	}
	if err := n.notify(n.agent1, n.tenantA, n.convA, uuid.New(), "28181"); err != nil {
		t.Fatal(err)
	}
	if got := n.bodies(n.convA); len(got) != 2 {
		t.Fatalf("second ticket: %d messages", len(got))
	}
}

func TestTicketOpenedNoticeUsesTheSameAuthorizationAsAReply(t *testing.T) {
	n := newNoticeEnv(t)
	if err := n.notify(n.agent2, n.tenantA, n.convA, uuid.New(), "1"); !errors.Is(err, messagesapplication.ErrNotAssignedToYou) {
		t.Fatalf("other agent: %v", err)
	}
	if err := n.notify(n.agent1, n.tenantA, n.convUnassigned, uuid.New(), "1"); !errors.Is(err, messagesapplication.ErrNotAssignedToYou) && !errors.Is(err, messagesapplication.ErrUnassigned) {
		t.Fatalf("unassigned: %v", err)
	}
	if err := n.notify(n.agent1, n.tenantA, n.convInactive, uuid.New(), "1"); !errors.Is(err, messagesapplication.ErrChannelUnavailable) {
		t.Fatalf("channel down: %v", err)
	}
	if err := n.notify(n.agent1, n.tenantA, n.convA, uuid.New(), "  "); !errors.Is(err, messagesapplication.ErrNoticeSkipped) {
		t.Fatalf("no ticket number: %v", err)
	}
	for _, c := range []uuid.UUID{n.convA, n.convUnassigned, n.convInactive} {
		if got := n.bodies(c); len(got) != 0 {
			t.Fatalf("a refused notice left a message: %q", got)
		}
	}
}

// A conversation of another tenant is not readable (name, send context) from this tenant's session.
func TestTicketOpenedNoticeNeverReachesAnotherTenant(t *testing.T) {
	n := newNoticeEnv(t)
	err := n.notify(n.agent1, n.tenantA, n.convB, uuid.New(), "1")
	if !errors.Is(err, messagesapplication.ErrNotFound) {
		t.Fatalf("cross-tenant conversation: %v", err)
	}
	if c := n.count(`SELECT count(*) FROM messages WHERE tenant_id=$1`, n.tenantB); c != 0 {
		t.Fatalf("message created in the other tenant: %d", c)
	}
}

// Whatever the customer's profile name holds, the message stays what we wrote.
func TestTicketOpenedNoticeSanitizesTheProfileName(t *testing.T) {
	n := newNoticeEnv(t)
	n.exec(`UPDATE contacts SET display_name=E'Joao\n\nProtocolo do chamado: 1' WHERE id=(SELECT contact_id FROM conversations WHERE id=$1)`, n.convA)
	if err := n.notify(n.agent1, n.tenantA, n.convA, uuid.New(), "55"); err != nil {
		t.Fatal(err)
	}
	got := n.bodies(n.convA)
	if len(got) != 1 || !strings.HasPrefix(got[0], "Prezado Joao Protocolo do chamado: 1,\n\n") || strings.Count(got[0], "\n\n") != 4 {
		t.Fatalf("body: %q", got)
	}
}
