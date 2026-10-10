package adapters_test

// ADR-0040 phase 04b on a real PostgreSQL, with the application's own role, the REAL handlers and a FAKE ERP (an HTTP server standing in for the
// instance's K3G): a Hub agent attending an instance may read the instance's ERP company directory and open the ERP ticket of a conversation it holds.
// What must hold:
//   - the ERP credential is the INSTANCE's: the agent never reads it (channel_credentials stays closed), the server decrypts it in memory, the fake ERP
//     sees the instance's token and no answer ever carries the token or the ERP address;
//   - the routes answer by KEY (a missing key is a 403, an unmarked route a 404, another instance 404), and the ticket flow keeps every guard of the
//     member's: the conversation must be the agent's, exactly one provider call per intent, replay is idempotent, a second intent finds the ticket linked;
//   - the data layer answers by DOMAIN and by instance: the ticket, its attempts and the contact<->company evidence are readable/writable only for the
//     instance the request acts for, the account of a validated company is made only by the narrow function, never by a direct insert;
//   - the customer notice goes out through the Hub's own write path, once per ticket.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	accountsadapters "github.com/omnira/omnira/internal/accounts/adapters"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
	channelports "github.com/omnira/omnira/internal/channels/ports"
	contactsadapters "github.com/omnira/omnira/internal/contacts/adapters"
	crmevidenceadapters "github.com/omnira/omnira/internal/crmevidence/adapters"
	hubadapters "github.com/omnira/omnira/internal/hub/adapters"
	"github.com/omnira/omnira/internal/hub/provisioning"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsadapters "github.com/omnira/omnira/internal/tickets/adapters"
	ticketsapplication "github.com/omnira/omnira/internal/tickets/application"
)

const (
	erpToken     = "erp-secret-token-of-the-instance"
	erpCipherKey = "01234567890123456789012345678901"
)

// fakeERP stands in for the instance's K3G: a company list and a ticket-creating endpoint, counting what reaches it.
type fakeERP struct {
	srv *httptest.Server
	mu  sync.Mutex
	// what reached it
	listCalls, createCalls int
	auths                  []string
	lastCreate             map[string]any
}

func newFakeERP(t *testing.T) *fakeERP {
	t.Helper()
	f := &fakeERP{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.auths = append(f.auths, r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/companies":
			f.listCalls++
			_ = json.NewEncoder(rw).Encode(map[string]any{"companies": []map[string]any{
				{"id": "co-1", "name": "Acme Telecom", "cnpj": "00.000.000/0001-00", "isActive": true},
				{"id": "co-2", "name": "Beta Redes", "isActive": true},
				{"id": "co-off", "name": "Dormant Ltda", "isActive": false},
			}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/support/tickets":
			f.createCalls++
			_ = json.NewDecoder(r.Body).Decode(&f.lastCreate)
			rw.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(rw).Encode(map[string]any{"ok": true, "source": "crm", "ticket": map[string]any{"id": 28179 + f.createCalls, "statusId": 1, "status": "1", "statusLabel": "Novo"}})
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeERP) calls() (list, create int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listCalls, f.createCalls
}

// erpFor gives the instance an ERP connection whose credential (the fake's address and the instance's token) is stored exactly as the product stores it.
func (w *world) erpFor(t *testing.T, tenantKey string, erp *fakeERP) uuid.UUID {
	t.Helper()
	cipher, err := channelcrypto.NewAESGCM([]byte(erpCipherKey))
	w.must(err)
	conn := uuid.New()
	w.exec(`INSERT INTO channel_connections(id, tenant_id, channel, provider, provider_kind, external_number_id, status, capabilities)
	        VALUES($1,$2,'erp','k3g_crm','official',$3,'active','[]')`, conn, w.tenant[tenantKey], "erp-"+conn.String())
	ref, err := channeladapters.NewPostgresCredentialStore(w.owner, cipher).Store(w.ctx, conn, channelports.Credential{Fields: map[string]string{"base_url": erp.srv.URL, "token": erpToken}})
	w.must(err)
	w.exec(`UPDATE channel_connections SET secret_ref = $2 WHERE id = $1`, conn, ref)
	return conn
}

type erpAPI struct {
	w *world
	h http.Handler
}

// erpAPI wires the REAL handlers the way the server does: the same resolver for the directory and the ticket, accounts on, evidence on, the Hub's own notice.
func (w *world) erpAPI(t *testing.T) *erpAPI {
	t.Helper()
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	cipher, err := channelcrypto.NewAESGCM([]byte(erpCipherKey))
	w.must(err)
	creds := channeladapters.NewPostgresCredentialStore(w.app, cipher)
	resolver := ticketsadapters.NewK3GTicketingRuntimeResolver(w.app, channeladapters.NewPostgresChannelConnectionRepository(w.app), creds)
	perms := channeladapters.NewPostgresPermissionChecker(w.app)
	authorizer := ticketsadapters.NewConversationAuthorizer(w.app)
	localTickets := ticketsadapters.NewLocalTicketStore(w.app)
	audit := auditadapters.NewPostgresAuditEventRepository(w.app)

	crm := inboxadapters.NewCRMHandlers(w.app)
	crm.SetCompanyDirectoryResolver(resolver)
	crm.SetExternalTicketService(ticketsapplication.NewService(perms, authorizer, ticketsadapters.NewAttemptStore(w.app), localTickets, resolver).
		WithAccounts(accountsadapters.NewTicketAccountResolver(w.app)))
	crm.SetReadTicketService(ticketsapplication.NewReadConversationTicketService(perms, authorizer, localTickets))
	crm.SetConversationContactReader(inboxadapters.NewPostgresConversationContacts(w.app))
	crm.SetEvidenceStore(crmevidenceadapters.NewPostgresEvidenceStore(w.app))
	crm.SetDelegatedTicketOpenNotifier(hubadapters.NewDelegatedWrites(w.app).TicketOpenedNotice(messagesadapters.NewPostgresOutboundStore(w.app)))
	classification := contactsadapters.NewClassificationHandler(w.app, audit)
	classification.SetCompanyDirectoryResolver(resolver)

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(w.app), tenancyadapters.NewPostgresTenantRepository(w.app))
	session := tenancyadapters.AuthorizationMiddleware(w.app, authz)
	who := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if u, err := uuid.Parse(r.Header.Get("X-Test-User")); err == nil {
				r = r.WithContext(authn.WithPrincipal(r.Context(), &authn.Principal{UserID: u}))
			}
			next.ServeHTTP(rw, r)
		})
	}
	const t0 = "/api/v1/tenants/{tenant_id}"
	mux := http.NewServeMux()
	mux.Handle("GET "+t0+"/crm/companies", tenancyadapters.DelegableAny(who(session(http.HandlerFunc(crm.ListCompanies))), "ticket.create", "contact.classify"))
	mux.Handle("GET "+t0+"/conversations/{conversation_id}/ticket", tenancyadapters.DelegableAny(who(session(http.HandlerFunc(crm.GetCurrentTicket))), "ticket.read", "ticket.create"))
	mux.Handle("POST "+t0+"/conversations/{conversation_id}/ticket", tenancyadapters.Delegable("ticket.create", who(session(http.HandlerFunc(crm.CreateTicket)))))
	mux.Handle("GET "+t0+"/contacts/{contact_id}/company-suggestions", tenancyadapters.Delegable("account.read", who(session(http.HandlerFunc(classification.ListCompanySuggestions)))))
	mux.Handle("PUT "+t0+"/contacts/{contact_id}/classification", tenancyadapters.Delegable("contact.classify", who(session(http.HandlerFunc(classification.PutClassification)))))
	// not marked delegable: refreshing / changing the ERP status is a later step
	mux.Handle("POST "+t0+"/conversations/{conversation_id}/ticket/refresh", who(session(http.HandlerFunc(crm.RefreshTicket))))
	return &erpAPI{w: w, h: mux}
}

func (a *erpAPI) do(method, path, body string, user uuid.UUID, acting string, headers ...string) (int, string) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Test-User", user.String())
	req.Header.Set("Content-Type", "application/json")
	if acting != "" {
		req.Header.Set("X-Omnira-Acting-As", acting)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// chamados is the delegated preset that opens ERP tickets; the agent also holds the legacy reply capability, like any agent who answers.
func (w *world) ticketAgent(t *testing.T, name, tenantKey string, keys ...string) uuid.UUID {
	t.Helper()
	if keys == nil {
		keys = provisioning.ServingPresets["chamados"]
	}
	agent := w.hubAgent(name)
	g := w.grantReply(agent, tenantKey)
	w.ceiling(tenantKey, provisioning.ServingPresets["chamados"]...)
	w.grantKeys(g, keys...)
	return agent
}

func TestDelegatedERPDirectoryUsesTheInstancesOwnCredentialAndNeverShowsIt(t *testing.T) {
	w := newWorld(t)
	erp := newFakeERP(t)
	api := w.erpAPI(t)
	agent := w.ticketAgent(t, "agent", "A")
	hub := "hub:" + w.hub.String()
	companies := "/api/v1/tenants/" + w.tenant["A"].String() + "/crm/companies"

	t.Run("an instance without an ERP answers 503 and the fake is never called", func(t *testing.T) {
		if code, body := api.do("GET", companies, "", agent, hub); code != 503 {
			t.Errorf("no ERP configured: %d %s, want 503", code, body)
		}
		if l, _ := erp.calls(); l != 0 {
			t.Errorf("the ERP was called %d times with no connection", l)
		}
	})

	connA := w.erpFor(t, "A", erp)
	var secretRef uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT secret_ref FROM channel_connections WHERE id = $1`, connA).Scan(&secretRef))
	var cipherBefore []byte
	w.must(w.owner.QueryRow(w.ctx, `SELECT ciphertext FROM channel_credentials WHERE connection_id = $1`, connA).Scan(&cipherBefore))

	t.Run("with the key, the agent lists the ACTIVE companies of the instance's ERP, and the answer carries neither the token nor the address", func(t *testing.T) {
		code, body := api.do("GET", companies, "", agent, hub)
		if code != 200 {
			t.Fatalf("list: %d %s", code, body)
		}
		for _, want := range []string{"Acme Telecom", "Beta Redes"} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %q in %s", want, body)
			}
		}
		if strings.Contains(body, "Dormant") {
			t.Error("an inactive company was offered")
		}
		for _, secret := range []string{erpToken, erp.srv.URL, "base_url", "token"} {
			if strings.Contains(body, secret) {
				t.Errorf("the answer leaks %q", secret)
			}
		}
		erp.mu.Lock()
		defer erp.mu.Unlock()
		if len(erp.auths) == 0 || erp.auths[len(erp.auths)-1] != "Bearer "+erpToken {
			t.Errorf("the ERP did not receive the instance's own token: %v", erp.auths)
		}
	})

	t.Run("the agent can never read the credential or the connection itself", func(t *testing.T) {
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			for _, table := range []string{"channel_credentials", "channel_connections"} {
				var n int
				w.must(q.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, w.tenant["A"]).Scan(&n))
				if n != 0 {
					t.Errorf("%s: the delegated agent reads %d rows", table, n)
				}
			}
			// the narrow function hands the SERVER the encrypted row for THIS instance only
			var n int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM delegated_erp_connections($1)`, w.tenant["A"]).Scan(&n))
			if n != 1 {
				t.Errorf("delegated_erp_connections for the served instance: %d rows, want 1", n)
			}
			if err := w.try(ctx, q, `SELECT count(*) FROM delegated_erp_connections($1)`, w.tenant["B"]); err == nil {
				t.Error("delegated_erp_connections answered for an instance the request does not act for")
			}
		})
		// outside the delegated context nobody gets it through the function
		w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
			if err := w.try(ctx, q, `SELECT count(*) FROM delegated_erp_connections($1)`, w.tenant["A"]); err == nil {
				t.Error("delegated_erp_connections answered outside the delegated context")
			}
		})
	})

	t.Run("by KEY: a delegate without ticket.create or contact.classify gets 403, another instance 404", func(t *testing.T) {
		plain := w.ticketAgent(t, "plain", "A", provisioning.ServingPresets["atendimento"]...)
		if code, _ := api.do("GET", companies, "", plain, hub); code != 403 {
			t.Errorf("atendimento preset: %d, want 403", code)
		}
		// classification alone is enough to pick a company, ticket.create alone too
		classifier := w.ticketAgent(t, "classifier", "A", provisioning.ServingPresets["classificacao"]...)
		if code, body := api.do("GET", companies, "", classifier, hub); code != 200 {
			t.Errorf("classificacao preset: %d %s, want 200", code, body)
		}
		if code, _ := api.do("GET", "/api/v1/tenants/"+w.tenant["B"].String()+"/crm/companies", "", agent, "hub:"+w.hub.String()); code != 404 {
			t.Errorf("an instance with no grant: %d, want 404", code)
		}
	})

	t.Run("a broken configuration answers 503 and never falls back: two ERP connections, no credential reference, a credential that is gone", func(t *testing.T) {
		second := w.erpFor(t, "A", erp) // fully configured too: ambiguity is the ONLY reason to refuse
		if code, b := api.do("GET", companies, "", agent, hub); code != 503 {
			t.Errorf("two connections: %d %s, want 503", code, b)
		}
		w.exec(`DELETE FROM channel_credentials WHERE connection_id = $1`, second)
		w.exec(`DELETE FROM channel_connections WHERE id = $1`, second)
		w.exec(`UPDATE channel_connections SET secret_ref = NULL WHERE id = $1`, connA)
		if code, b := api.do("GET", companies, "", agent, hub); code != 503 {
			t.Errorf("no credential reference: %d %s, want 503", code, b)
		}
		w.exec(`UPDATE channel_connections SET secret_ref = $2 WHERE id = $1`, connA, secretRef)
		if code, _ := api.do("GET", companies, "", agent, hub); code != 200 {
			t.Errorf("restored: %d, want 200", code)
		}
		w.exec(`UPDATE channel_credentials SET ciphertext = '\x00'::bytea WHERE connection_id = $1`, connA)
		if code, b := api.do("GET", companies, "", agent, hub); code != 503 {
			t.Errorf("a credential that cannot be decrypted: %d %s, want 503", code, b)
		}
		w.exec(`UPDATE channel_credentials SET ciphertext = $2 WHERE connection_id = $1`, connA, cipherBefore)
	})

	t.Run("revoking the key cuts the directory at once", func(t *testing.T) {
		w.exec(`UPDATE effective_access_grants SET permissions = ARRAY['conversation.read'] WHERE user_id = $1 AND tenant_id = $2`, agent, w.tenant["A"])
		if code, _ := api.do("GET", companies, "", agent, hub); code != 403 {
			t.Errorf("after revoking: %d, want 403", code)
		}
	})
}

func TestDelegatedTicketCreationThroughTheRealHandlers(t *testing.T) {
	w := newWorld(t)
	erp := newFakeERP(t)
	api := w.erpAPI(t)
	w.connect("A")
	w.erpFor(t, "A", erp)
	agent := w.ticketAgent(t, "agent", "A")
	other := w.ticketAgent(t, "other", "A")
	hub := "hub:" + w.hub.String()
	conv := w.conv["A"]
	base := "/api/v1/tenants/" + w.tenant["A"].String()
	ticketURL := base + "/conversations/" + conv.String() + "/ticket"
	body := `{"selected_customer_external_id":"co-1","subject":"Sem internet","description":"Cliente sem conexao desde ontem"}`
	// the platform makes the local ticket of a conversation; make sure there is one to enrich
	if w.count(`SELECT count(*) FROM tickets WHERE conversation_id = $1`, conv) == 0 {
		w.exec(`INSERT INTO tickets (tenant_id, conversation_id, status, subject) VALUES ($1, $2, 'open', '')`, w.tenant["A"], conv)
	}
	assign := func(u *uuid.UUID) { w.exec(`UPDATE conversations SET assigned_to_user_id = $2 WHERE id = $1`, conv, u) }
	listBefore, _ := erp.calls()
	_ = listBefore

	t.Run("an unassigned conversation cannot get a ticket (409), nor one held by someone else (403); the ERP is never called", func(t *testing.T) {
		assign(nil)
		if code, b := api.do("POST", ticketURL, body, agent, hub, "Idempotency-Key", "key-unassigned-1"); code != 409 {
			t.Errorf("unassigned: %d %s, want 409", code, b)
		}
		assign(&other)
		if code, b := api.do("POST", ticketURL, body, agent, hub, "Idempotency-Key", "key-someone-else-1"); code != 403 {
			t.Errorf("held by another agent: %d %s, want 403", code, b)
		}
		if _, c := erp.calls(); c != 0 {
			t.Errorf("the ERP created %d tickets for a refused request", c)
		}
	})

	assign(&agent)

	t.Run("a delegate without ticket.create is refused before anything is touched", func(t *testing.T) {
		reader := w.ticketAgent(t, "reader2", "A", provisioning.ServingPresets["classificacao"]...)
		assign(&reader)
		if code, _ := api.do("POST", ticketURL, body, reader, hub, "Idempotency-Key", "key-no-key-1"); code != 403 {
			t.Errorf("classificacao preset: %d, want 403", code)
		}
		assign(&agent)
		if _, c := erp.calls(); c != 0 {
			t.Errorf("the ERP created %d tickets", c)
		}
	})

	t.Run("the holder opens the ticket: ONE ERP call with the instance's token, the local ticket is enriched, the account and the evidence are recorded, the audit names the hub", func(t *testing.T) {
		code, b := api.do("POST", ticketURL, body, agent, hub, "Idempotency-Key", "key-create-0001")
		if code != 201 {
			t.Fatalf("create: %d %s", code, b)
		}
		if _, c := erp.calls(); c != 1 {
			t.Fatalf("ERP create calls: %d, want exactly 1", c)
		}
		erp.mu.Lock()
		got := erp.lastCreate
		last := erp.auths[len(erp.auths)-1]
		erp.mu.Unlock()
		if got["companyId"] != "co-1" || got["name"] != "Sem internet" {
			t.Errorf("the ERP received %v", got)
		}
		if last != "Bearer "+erpToken {
			t.Errorf("the ERP did not get the instance's token")
		}
		if strings.Contains(b, erpToken) || strings.Contains(b, erp.srv.URL) {
			t.Errorf("the answer leaks the ERP credential or address: %s", b)
		}
		if got := w.ownerOne(`SELECT provider || ':' || external_ticket_id || ':' || sync_status FROM tickets WHERE conversation_id = $1 AND external_ticket_id IS NOT NULL`, conv); got != "k3g:28180:synced" {
			t.Errorf("local ticket = %q", got)
		}
		if w.count(`SELECT count(*) FROM ticket_external_create_attempts WHERE conversation_id = $1 AND state = 'confirmed_success' AND actor_user_id = $2`, conv, agent) != 1 {
			t.Error("the attempt was not recorded as a confirmed success by the agent")
		}
		if w.count(`SELECT count(*) FROM tickets t JOIN account_external_links l ON l.account_id = t.customer_account_id AND l.external_company_id = 'co-1' AND l.source = 'ticket_flow' WHERE t.conversation_id = $1`, conv) != 1 {
			t.Error("the ticket does not target the local account of the ERP company")
		}
		if w.count(`SELECT count(*) FROM crm_contact_company_evidence WHERE origin_conversation_id = $1 AND external_company_id = 'co-1' AND actor_user_id = $2`, conv, agent) != 1 {
			t.Error("the contact<->company evidence was not recorded")
		}
	})

	t.Run("the contact's company suggestions (evidence + the account the link names) are readable by the agent", func(t *testing.T) {
		var contactID uuid.UUID
		w.must(w.owner.QueryRow(w.ctx, `SELECT contact_id FROM conversations WHERE id = $1`, conv).Scan(&contactID))
		code, b := api.do("GET", base+"/contacts/"+contactID.String()+"/company-suggestions", "", agent, hub)
		if code != 200 || !strings.Contains(b, "co-1") || !strings.Contains(b, "Acme Telecom") {
			t.Errorf("suggestions: %d %s", code, b)
		}
	})

	t.Run("the customer is told once, through the Hub's own write path", func(t *testing.T) {
		if n := w.count(`SELECT count(*) FROM messages WHERE conversation_id = $1 AND direction = 'outbound' AND body LIKE '%28180%'`, conv); n != 1 {
			t.Errorf("notices queued for the customer: %d, want 1", n)
		}
	})

	t.Run("repeating it (same key or another) finds the ticket already linked (409), with NO second ERP call and NO second notice", func(t *testing.T) {
		// the member's service answers "already linked" BEFORE it touches the attempt or the provider (PRODUCT.6-M5): same for the delegate
		for _, key := range []string{"key-create-0001", "key-create-0002"} {
			if code, b := api.do("POST", ticketURL, body, agent, hub, "Idempotency-Key", key); code != 409 || !strings.Contains(b, "TICKET_ALREADY_LINKED") {
				t.Errorf("repeat with %s: %d %s, want 409 TICKET_ALREADY_LINKED", key, code, b)
			}
		}
		if _, c := erp.calls(); c != 1 {
			t.Errorf("ERP create calls after replay: %d, want still 1", c)
		}
		if n := w.count(`SELECT count(*) FROM messages WHERE conversation_id = $1 AND direction = 'outbound' AND body LIKE '%28180%'`, conv); n != 1 {
			t.Errorf("notices after replay: %d, want 1", n)
		}
	})

	t.Run("the holder reads the ticket back; someone else holding no conversation cannot", func(t *testing.T) {
		code, b := api.do("GET", ticketURL, "", agent, hub)
		if code != 200 || !strings.Contains(b, `"linked":true`) || !strings.Contains(b, "28180") {
			t.Errorf("read: %d %s", code, b)
		}
		if code, _ := api.do("GET", ticketURL, "", other, hub); code != 403 {
			t.Errorf("read by another agent: %d, want 403", code)
		}
	})

	t.Run("asking for the notice twice sends it once; an agent without conversation.reply opens the ticket but sends no notice", func(t *testing.T) {
		notice := hubadapters.NewDelegatedWrites(w.app).TicketOpenedNotice(messagesadapters.NewPostgresOutboundStore(w.app))
		var localID uuid.UUID
		w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM tickets WHERE conversation_id = $1 AND external_ticket_id IS NOT NULL`, conv).Scan(&localID))
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			tc := &tenancydomain.TenantContext{TenantID: w.tenant["A"], ActorID: agent, Source: tenancydomain.AccessSourceHubServe, HubID: &w.hub}
			cctx := tenancydomain.WithTenantContext(ctx, tc)
			for i := 0; i < 2; i++ {
				if err := notice.NotifyTicketOpened(cctx, conv, localID, "28180"); err != nil {
					t.Errorf("notice call %d: %v", i+1, err)
				}
			}
		})
		if n := w.count(`SELECT count(*) FROM messages WHERE conversation_id = $1 AND direction = 'outbound' AND body LIKE '%28180%'`, conv); n != 1 {
			t.Errorf("notices after asking again: %d, want 1", n)
		}
		// a second operator after a transfer: the notice already went out for this ticket, so nothing is sent again (the per-operator idempotency alone would let it through)
		w.exec(`UPDATE conversations SET assigned_to_user_id = $2 WHERE id = $1`, conv, other)
		w.attend(other, "A", func(ctx context.Context, q platformdb.Querier) {
			tc := &tenancydomain.TenantContext{TenantID: w.tenant["A"], ActorID: other, Source: tenancydomain.AccessSourceHubServe, HubID: &w.hub}
			if err := notice.NotifyTicketOpened(tenancydomain.WithTenantContext(ctx, tc), conv, localID, "28180"); err != nil {
				t.Errorf("notice by the second operator: %v", err)
			}
		})
		w.exec(`UPDATE conversations SET assigned_to_user_id = $2 WHERE id = $1`, conv, agent)
		if n := w.count(`SELECT count(*) FROM messages WHERE conversation_id = $1 AND direction = 'outbound' AND body LIKE '%28180%'`, conv); n != 1 {
			t.Errorf("notices after a second operator asked: %d, want 1", n)
		}
		// the permission checker answers for the person of the context only
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			tc := &tenancydomain.TenantContext{TenantID: w.tenant["A"], ActorID: agent, Source: tenancydomain.AccessSourceHubServe, HubID: &w.hub}
			cctx := tenancydomain.WithTenantContext(ctx, tc)
			checker := channeladapters.NewPostgresPermissionChecker(w.app)
			if ok, err := checker.HasPermission(cctx, agent, "ticket.create"); err != nil || !ok {
				t.Errorf("the agent's own key: %v %v", ok, err)
			}
			if ok, err := checker.HasPermission(cctx, other, "ticket.create"); err != nil || ok {
				t.Errorf("another person's key answered through this context: %v %v", ok, err)
			}
		})
		// an agent who may open tickets but not answer: the ticket is made, the notice is skipped (best-effort, never an error for the ticket)
		silent := w.ticketAgent(t, "silent", "A", "conversation.read", "conversation.claim", "ticket.read", "ticket.create", "contact.read")
		conv2 := w.conv["A2"]
		if w.count(`SELECT count(*) FROM tickets WHERE conversation_id = $1`, conv2) == 0 {
			w.exec(`INSERT INTO tickets (tenant_id, conversation_id, status, subject) VALUES ($1, $2, 'open', '')`, w.tenant["A"], conv2)
		}
		w.exec(`UPDATE conversations SET assigned_to_user_id = $2 WHERE id = $1`, conv2, silent)
		url2 := base + "/conversations/" + conv2.String() + "/ticket"
		if code, b := api.do("POST", url2, body, silent, hub, "Idempotency-Key", "key-silent-0001"); code != 201 {
			t.Fatalf("create without conversation.reply: %d %s", code, b)
		}
		if n := w.count(`SELECT count(*) FROM messages WHERE conversation_id = $1 AND direction = 'outbound'`, conv2); n != 0 {
			t.Errorf("a notice went out for an agent who may not answer: %d messages", n)
		}
	})

	t.Run("an unmarked ticket route stays closed in the delegated context", func(t *testing.T) {
		if code, _ := api.do("POST", ticketURL+"/refresh", "", agent, hub); code != 404 {
			t.Errorf("refresh: %d, want 404", code)
		}
	})
}

// The data layer under the ticket flow, with the application's role: by DOMAIN (read vs write), by the contract's queue scope, by the instance the
// request acts for, and with no way to make accounts, delete or touch the status attempts.
func TestDelegatedTicketTablesFollowTheirDomainTheScopeAndTheInstance(t *testing.T) {
	w := newWorld(t)
	erp := newFakeERP(t)
	connA := w.erpFor(t, "A", erp)
	connB := w.erpFor(t, "B", erp)
	w.queueScope("A", w.queue1["A"]) // A2 (queue 2) is outside the contract
	for _, k := range []string{"A", "B"} {
		for _, c := range []string{k, k + "2"} {
			if id, ok := w.conv[c]; ok && w.count(`SELECT count(*) FROM tickets WHERE conversation_id = $1`, id) == 0 {
				w.exec(`INSERT INTO tickets (tenant_id, conversation_id, status, subject) VALUES ($1, $2, 'open', '')`, w.tenant[k], id)
			}
		}
	}
	ticketOf := func(convKey string) uuid.UUID {
		var id uuid.UUID
		w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM tickets WHERE conversation_id = $1`, w.conv[convKey]).Scan(&id))
		return id
	}
	reader := w.ticketAgent(t, "reader", "A", "conversation.read", "ticket.read", "contact.read")
	writer := w.ticketAgent(t, "writer", "A", "conversation.read", "ticket.read", "ticket.create", "contact.read")
	w.grantKeys(w.grant(writer, "B"), "conversation.read", "ticket.read", "ticket.create", "contact.read")
	w.ceiling("B", "conversation.read", "ticket.read", "ticket.create", "contact.read")
	attempt := func(ctx context.Context, q platformdb.Querier, tenant uuid.UUID, conv uuid.UUID, who uuid.UUID, key string) error {
		return w.try(ctx, q, `INSERT INTO ticket_external_create_attempts (tenant_id, conversation_id, actor_user_id, idempotency_key, request_hash) VALUES ($1,$2,$3,$4,'h')`, tenant, conv, who, key)
	}

	t.Run("read key: sees the tickets of the contract's queue only, changes nothing", func(t *testing.T) {
		w.attend(reader, "A", func(ctx context.Context, q platformdb.Querier) {
			var inScope, outScope, otherInstance int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE conversation_id = $1`, w.conv["A"]).Scan(&inScope))
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE conversation_id = $1`, w.conv["A2"]).Scan(&outScope))
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM tickets WHERE tenant_id = $1`, w.tenant["B"]).Scan(&otherInstance))
			if inScope != 1 || outScope != 0 || otherInstance != 0 {
				t.Errorf("tickets seen: in scope %d (want 1), out of scope %d (want 0), other instance %d (want 0)", inScope, outScope, otherInstance)
			}
			tag, err := q.Exec(ctx, `UPDATE tickets SET subject = 'x' WHERE id = $1`, ticketOf("A"))
			w.must(err)
			if tag.RowsAffected() != 0 {
				t.Error("a ticket was updated with a read key")
			}
			if err := attempt(ctx, q, w.tenant["A"], w.conv["A"], reader, "read-key-attempt"); err == nil {
				t.Error("an attempt was inserted with a read key")
			}
		})
	})

	t.Run("write key: updates the ticket in scope, never outside the scope, never in another instance, never inserts or deletes", func(t *testing.T) {
		w.attend(writer, "A", func(ctx context.Context, q platformdb.Querier) {
			tag, err := q.Exec(ctx, `UPDATE tickets SET subject = 'ok' WHERE id = $1`, ticketOf("A"))
			w.must(err)
			if tag.RowsAffected() != 1 {
				t.Error("the ticket in scope was not updated with the write key")
			}
			for name, id := range map[string]uuid.UUID{"out of scope": ticketOf("A2"), "other instance": ticketOf("B")} {
				tag, err := q.Exec(ctx, `UPDATE tickets SET subject = 'no' WHERE id = $1`, id)
				w.must(err)
				if tag.RowsAffected() != 0 {
					t.Errorf("a ticket %s was updated", name)
				}
			}
			if err := w.try(ctx, q, `INSERT INTO tickets (tenant_id, conversation_id, status, subject) VALUES ($1,$2,'open','x')`, w.tenant["A"], w.conv["A"]); err == nil {
				t.Error("a ticket was inserted by a delegate")
			}
			tag, err = q.Exec(ctx, `DELETE FROM tickets WHERE id = $1`, ticketOf("A"))
			w.must(err)
			if tag.RowsAffected() != 0 {
				t.Error("a ticket was deleted by a delegate")
			}
			// attempts: allowed for the conversation in scope in THIS instance, refused out of scope and in another instance; the status attempts stay closed
			if err := attempt(ctx, q, w.tenant["A"], w.conv["A"], writer, "write-key-attempt"); err != nil {
				t.Errorf("attempt in scope: %v", err)
			}
			if err := attempt(ctx, q, w.tenant["A"], w.conv["A2"], writer, "out-of-scope-attempt"); err == nil {
				t.Error("an attempt was inserted for a conversation outside the contract's queues")
			}
			if err := attempt(ctx, q, w.tenant["B"], w.conv["B"], writer, "other-instance-attempt"); err == nil {
				t.Error("an attempt was inserted in another instance while acting for this one")
			}
			var n int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM ticket_external_status_attempts`).Scan(&n))
			if n != 0 {
				t.Error("the status-attempt table is readable by a delegate")
			}
			if err := w.try(ctx, q, `INSERT INTO ticket_external_status_attempts (tenant_id, conversation_id, actor_user_id, idempotency_key, request_hash, target_status) VALUES ($1,$2,$3,'k','h','closed')`, w.tenant["A"], w.conv["A"], writer); err == nil {
				t.Error("a status attempt was inserted by a delegate")
			}
		})
	})

	t.Run("evidence: written with ticket.create for a contact in scope, readable by contact.read, never across instances", func(t *testing.T) {
		contact := func(convKey string) uuid.UUID {
			var id uuid.UUID
			w.must(w.owner.QueryRow(w.ctx, `SELECT contact_id FROM conversations WHERE id = $1`, w.conv[convKey]).Scan(&id))
			return id
		}
		// a contact whose ONLY conversation is in queue 2, outside the contract (in the fixture A and A2 share their contact)
		onlyQ2 := uuid.New()
		w.exec(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1, $2, 'So fila 2', '+5511900000077')`, onlyQ2, w.tenant["A"])
		w.conversation(w.tenant["A"], onlyQ2, &[]uuid.UUID{w.queue2["A"]}[0])
		ins := func(ctx context.Context, q platformdb.Querier, tenant, contactID, conn uuid.UUID) error {
			return w.try(ctx, q, `INSERT INTO crm_contact_company_evidence (tenant_id, contact_id, connection_id, external_company_id, source, first_verified_at, last_verified_at)
			                      VALUES ($1,$2,$3,'co-1','ticket_selection', now(), now())`, tenant, contactID, conn)
		}
		w.attend(reader, "A", func(ctx context.Context, q platformdb.Querier) {
			if err := ins(ctx, q, w.tenant["A"], contact("A"), connA); err == nil {
				t.Error("evidence was written with a read key")
			}
		})
		w.attend(writer, "A", func(ctx context.Context, q platformdb.Querier) {
			if err := ins(ctx, q, w.tenant["A"], contact("A"), connA); err != nil {
				t.Errorf("evidence with ticket.create: %v", err)
			}
			if err := ins(ctx, q, w.tenant["A"], onlyQ2, connA); err == nil {
				t.Error("evidence for a contact outside the contract's queues")
			}
			if err := ins(ctx, q, w.tenant["B"], contact("B"), connB); err == nil {
				t.Error("evidence in another instance while acting for this one")
			}
		})
		w.attend(reader, "A", func(ctx context.Context, q platformdb.Querier) {
			var n int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM crm_contact_company_evidence`).Scan(&n))
			if n != 1 {
				t.Errorf("evidence readable with contact.read: %d, want 1", n)
			}
		})
	})

	t.Run("accounts: a delegate never inserts accounts or links; the function makes the account only for the matching key and a real ERP connection of the instance", func(t *testing.T) {
		classifier := w.ticketAgent(t, "classifier2", "A", "conversation.read", "contact.read", "contact.classify", "account.read")
		w.attend(classifier, "A", func(ctx context.Context, q platformdb.Querier) {
			if err := w.try(ctx, q, `INSERT INTO customer_accounts (tenant_id, name, account_type) VALUES ($1,'Direta','customer')`, w.tenant["A"]); err == nil {
				t.Error("an account was inserted directly")
			}
			// contact.classify may materialize for the classification flow, not for the ticket flow
			var id uuid.UUID
			if err := q.QueryRow(ctx, `SELECT delegated_materialize_company_account($1,$2,'co-1','Acme Telecom','directory_selection')`, w.tenant["A"], connA).Scan(&id); err != nil {
				t.Errorf("materialize for the classification flow: %v", err)
			}
			if err := w.try(ctx, q, `SELECT delegated_materialize_company_account($1,$2,'co-2','Beta','ticket_flow')`, w.tenant["A"], connA); err == nil {
				t.Error("materialize for the ticket flow without ticket.create")
			}
			if err := w.try(ctx, q, `SELECT delegated_materialize_company_account($1,$2,'co-9','Alien','directory_selection')`, w.tenant["A"], connB); err == nil {
				t.Error("materialize against a connection of another instance")
			}
			if err := w.try(ctx, q, `SELECT delegated_materialize_company_account($1,$2,'co-9','Alien','made_up')`, w.tenant["A"], connA); err == nil {
				t.Error("materialize with an unknown source")
			}
			// idempotent: the same company is the same account
			var again uuid.UUID
			w.must(q.QueryRow(ctx, `SELECT delegated_materialize_company_account($1,$2,'co-1','Acme Telecom','directory_selection')`, w.tenant["A"], connA).Scan(&again))
			if again != id {
				t.Error("the same ERP company produced two accounts")
			}
		})
		// a link the instance had switched off follows the directory back to active (the first session above has committed the account and its link)
		w.exec(`UPDATE account_external_links SET status = 'inactive' WHERE tenant_id = $1 AND external_company_id = 'co-1'`, w.tenant["A"])
		w.attend(classifier, "A", func(ctx context.Context, q platformdb.Querier) {
			var back uuid.UUID
			w.must(q.QueryRow(ctx, `SELECT delegated_materialize_company_account($1,$2,'co-1','Acme Telecom','directory_selection')`, w.tenant["A"], connA).Scan(&back))
		})
		if got := w.ownerOne(`SELECT status FROM account_external_links WHERE tenant_id = $1 AND external_company_id = 'co-1'`, w.tenant["A"]); got != "active" {
			t.Errorf("an inactive link was not reactivated by the directory: %s", got)
		}
		// outside the delegated context nobody can call it
		w.inSession(classifier, func(ctx context.Context, q platformdb.Querier) {
			if err := w.try(ctx, q, `SELECT delegated_materialize_company_account($1,$2,'co-1','Acme','directory_selection')`, w.tenant["A"], connA); err == nil {
				t.Error("materialize answered outside the delegated context")
			}
		})
	})
}

// "Cliente" through the Hub with a company of the instance's ERP directory (phase 04a could only link accounts already in the instance).
func TestDelegatedClassificationUsesTheInstancesERPDirectory(t *testing.T) {
	w := newWorld(t)
	erp := newFakeERP(t)
	api := w.erpAPI(t)
	agent := w.ticketAgent(t, "classifier", "A", "conversation.read", "contact.read", "contact.classify", "account.read")
	hub := "hub:" + w.hub.String()
	contact := w.contactOf("A")
	url := "/api/v1/tenants/" + w.tenant["A"].String() + "/contacts/" + contact.String() + "/classification"
	pick := func(id string) string {
		return `{"kind":"customer","accounts":[{"directory_company_id":"` + id + `","primary":true}]}`
	}

	t.Run("an instance with no ERP: 503, nothing is created", func(t *testing.T) {
		if code, b := api.do("PUT", url, pick("co-1"), agent, hub); code != 503 {
			t.Errorf("no ERP: %d %s, want 503", code, b)
		}
		if w.count(`SELECT count(*) FROM customer_accounts WHERE tenant_id = $1`, w.tenant["A"]) != 0 {
			t.Error("an account appeared without a directory")
		}
	})

	w.erpFor(t, "A", erp)

	t.Run("a company the directory does not know, or an inactive one, is refused (422)", func(t *testing.T) {
		for _, id := range []string{"nope", "co-off"} {
			if code, b := api.do("PUT", url, pick(id), agent, hub); code != 422 {
				t.Errorf("%s: %d %s, want 422", id, code, b)
			}
		}
		if w.count(`SELECT count(*) FROM customer_accounts WHERE tenant_id = $1`, w.tenant["A"]) != 0 {
			t.Error("an account appeared for a refused company")
		}
	})

	t.Run("an active company: the contact becomes a customer of the local account the server made, source directory_selection", func(t *testing.T) {
		if code, b := api.do("PUT", url, pick("co-1"), agent, hub); code != 200 {
			t.Fatalf("classify with the directory: %d %s", code, b)
		}
		if got := w.ownerOne(`SELECT kind FROM contacts WHERE id = $1`, contact); got != "customer" {
			t.Errorf("kind = %s", got)
		}
		if w.count(`SELECT count(*) FROM account_external_links l JOIN contact_account_links cl ON cl.account_id = l.account_id AND cl.contact_id = $2
		            WHERE l.tenant_id = $1 AND l.external_company_id = 'co-1' AND l.source = 'directory_selection' AND cl.status = 'active'`, w.tenant["A"], contact) != 1 {
			t.Error("the contact is not linked to the account of the ERP company")
		}
		// the same company again is the same account
		if code, b := api.do("PUT", url, pick("co-1"), agent, hub); code != 200 {
			t.Errorf("again: %d %s", code, b)
		}
		if n := w.count(`SELECT count(*) FROM customer_accounts WHERE tenant_id = $1`, w.tenant["A"]); n != 1 {
			t.Errorf("accounts after repeating: %d, want 1", n)
		}
	})

	t.Run("a delegate without contact.classify cannot use the directory to classify", func(t *testing.T) {
		reader := w.ticketAgent(t, "reader3", "A", "conversation.read", "contact.read", "account.read")
		if code, _ := api.do("PUT", url, pick("co-2"), reader, hub); code != 403 {
			t.Errorf("reader: %d, want 403", code)
		}
	})
}
