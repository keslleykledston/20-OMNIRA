package adapters_test

// ADR-0040 phase 04a on a real PostgreSQL, with the application's own role: a Hub agent attending an instance may CLASSIFY and EDIT the contact
// (key contact.classify), and nothing more. What must hold:
//   - the data layer answers by DOMAIN (contact, write), only for the instances the grant covers, only in the delegated context, and the
//     contact inherits the visibility of the conversation it belongs to (the contract's queue scope);
//   - contacts can be UPDATED but never inserted or deleted, links can be added and ended but never deleted, accounts can be READ but never written;
//   - the two side effects of a reclassification that touch conversations go through narrow functions that check the key themselves;
//   - through the REAL handlers: kind, details, classification, link/end/primary, the account list and /me/access answer in the delegated context
//     by KEY (a missing key is a 403, an unmarked route a 404), a directory (ERP) company and the `internal` kind are refused, and a member who is
//     also a delegate gets in each context exactly what that context gives.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	accountsadapters "github.com/omnira/omnira/internal/accounts/adapters"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	contactsadapters "github.com/omnira/omnira/internal/contacts/adapters"
	"github.com/omnira/omnira/internal/hub/provisioning"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

func (w *world) contactOf(tenantKey string) uuid.UUID {
	w.t.Helper()
	var id uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM contacts WHERE tenant_id = $1`, w.tenant[tenantKey]).Scan(&id))
	return id
}

func (w *world) account(tenantKey, name string) uuid.UUID {
	w.t.Helper()
	id := uuid.New()
	w.exec(`INSERT INTO customer_accounts (id, tenant_id, name, account_type) VALUES ($1, $2, $3, 'customer')`, id, w.tenant[tenantKey], name)
	return id
}

// ownerOne reads one scalar as the database owner (to check what really happened).
func (w *world) ownerOne(sql string, args ...any) string {
	w.t.Helper()
	var s string
	w.must(w.owner.QueryRow(w.ctx, sql, args...).Scan(&s))
	return s
}

// try runs one statement that is EXPECTED to be refused inside a savepoint, so the refusal does not abort the surrounding transaction.
func (w *world) try(ctx context.Context, q platformdb.Querier, sql string, args ...any) error {
	_, err := q.Exec(ctx, "SAVEPOINT try")
	w.must(err)
	_, err = q.Exec(ctx, sql, args...)
	if err != nil {
		_, rerr := q.Exec(ctx, "ROLLBACK TO SAVEPOINT try")
		w.must(rerr)
	}
	return err
}

// attend runs fn as the agent already inside the delegated context for one instance.
func (w *world) attend(user uuid.UUID, tenantKey string, fn func(ctx context.Context, q platformdb.Querier)) {
	w.t.Helper()
	w.inSession(user, func(ctx context.Context, q platformdb.Querier) {
		if !w.enter(ctx, q, user, tenantKey) {
			w.t.Fatalf("could not enter the delegated context for %s", tenantKey)
		}
		fn(ctx, q)
	})
}

func TestDelegatedContactWritesFollowTheContactDomain(t *testing.T) {
	w := newWorld(t)
	contactA, contactB := w.contactOf("A"), w.contactOf("B")
	accA, accB := w.account("A", "Empresa A"), w.account("B", "Empresa B")
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "contact.read", "contact.classify", "account.read", "conversation.read")

	update := func(ctx context.Context, q platformdb.Querier, tenant, contact uuid.UUID) int64 {
		tag, err := q.Exec(ctx, `UPDATE contacts SET alias = 'x' WHERE tenant_id = $1 AND id = $2`, tenant, contact)
		w.must(err)
		return tag.RowsAffected()
	}

	t.Run("contact.classify updates the contact of its own instance and no other", func(t *testing.T) {
		w.grantKeys(g, "contact.classify")
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			if n := update(ctx, q, w.tenant["A"], contactA); n != 1 {
				t.Errorf("own instance: %d rows, want 1", n)
			}
			if n := update(ctx, q, w.tenant["B"], contactB); n != 0 {
				t.Errorf("another instance: %d rows, want 0", n)
			}
		})
	})

	t.Run("read keys never write", func(t *testing.T) {
		w.grantKeys(g, "contact.read", "account.read", "conversation.read")
		other := w.account("A", "Terceira Empresa A")
		var existing uuid.UUID // a link a member made earlier
		w.must(w.owner.QueryRow(w.ctx, `INSERT INTO contact_account_links (tenant_id, contact_id, account_id, relationship_type, source) VALUES ($1,$2,$3,'other','manual') RETURNING id`,
			w.tenant["A"], contactA, accA).Scan(&existing))
		defer w.exec(`DELETE FROM contact_account_links WHERE id = $1`, existing)
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			if tag, err := q.Exec(ctx, `UPDATE contact_account_links SET is_primary = true WHERE id = $1`, existing); err != nil || tag.RowsAffected() != 0 {
				t.Errorf("read-only keys updated a link (%d rows, %v)", tag.RowsAffected(), err)
			}
			if n := update(ctx, q, w.tenant["A"], contactA); n != 0 {
				t.Errorf("read-only keys updated %d contacts", n)
			}
			// another account than the existing link's: the unique index must not be what refuses it
			if err := w.try(ctx, q, `INSERT INTO contact_account_links (tenant_id, contact_id, account_id, relationship_type, source) VALUES ($1,$2,$3,'other','manual')`, w.tenant["A"], contactA, other); err == nil {
				t.Error("read-only keys inserted a link")
			}
		})
	})

	t.Run("not acting for the hub, the same person writes nothing", func(t *testing.T) {
		w.grantKeys(g, "contact.classify")
		w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
			if n := update(ctx, q, w.tenant["A"], contactA); n != 0 {
				t.Errorf("outside the delegated context: %d rows, want 0", n)
			}
		})
	})

	t.Run("never inserted, never deleted", func(t *testing.T) {
		w.grantKeys(g, "contact.classify")
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			try := func(sql string, args ...any) error { return w.try(ctx, q, sql, args...) }
			if try(`INSERT INTO contacts (tenant_id, display_name, phone_e164) VALUES ($1, 'novo', '+5511999990000')`, w.tenant["A"]) == nil {
				t.Error("a delegate inserted a contact")
			}
			tag, err := q.Exec(ctx, `DELETE FROM contacts WHERE tenant_id = $1 AND id = $2`, w.tenant["A"], contactA)
			w.must(err)
			if tag.RowsAffected() != 0 {
				t.Error("a delegate deleted a contact")
			}
			if try(`UPDATE contacts SET tenant_id = $2 WHERE tenant_id = $1 AND id = $3`, w.tenant["A"], w.tenant["B"], contactA) == nil {
				t.Error("a delegate moved a contact to another instance")
			}
			if try(`INSERT INTO customer_accounts (tenant_id, name, account_type) VALUES ($1, 'nova', 'customer')`, w.tenant["A"]) == nil {
				t.Error("a delegate created an account")
			}
			tag, err = q.Exec(ctx, `UPDATE customer_accounts SET name = 'x' WHERE tenant_id = $1 AND id = $2`, w.tenant["A"], accA)
			w.must(err)
			if tag.RowsAffected() != 0 {
				t.Error("a delegate renamed an account")
			}
		})
	})

	t.Run("links: add, promote and end in its own instance; the account of another instance cannot be linked", func(t *testing.T) {
		w.grantKeys(g, "contact.classify")
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			var link uuid.UUID
			w.must(q.QueryRow(ctx, `INSERT INTO contact_account_links (tenant_id, contact_id, account_id, relationship_type, source) VALUES ($1,$2,$3,'other','manual') RETURNING id`,
				w.tenant["A"], contactA, accA).Scan(&link))
			tag, err := q.Exec(ctx, `UPDATE contact_account_links SET is_primary = true WHERE id = $1`, link)
			w.must(err)
			if tag.RowsAffected() != 1 {
				t.Errorf("promote: %d rows", tag.RowsAffected())
			}
			tag, err = q.Exec(ctx, `UPDATE contact_account_links SET status = 'ended', ended_at = now(), is_primary = false WHERE id = $1`, link)
			w.must(err)
			if tag.RowsAffected() != 1 {
				t.Errorf("end: %d rows", tag.RowsAffected())
			}
			if tag, err = q.Exec(ctx, `DELETE FROM contact_account_links WHERE id = $1`, link); err != nil || tag.RowsAffected() != 0 {
				t.Errorf("a link was deleted (%d rows, %v)", tag.RowsAffected(), err)
			}
			// the account of ANOTHER instance is not even readable, and a link to it cannot be made for this one's contact
			var n int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM customer_accounts WHERE id = $1`, accB).Scan(&n))
			if n != 0 {
				t.Error("the delegate read an account of another instance")
			}
		})
	})

	t.Run("the contract's queue scope narrows which contacts can be written", func(t *testing.T) {
		w.grantKeys(g, "contact.classify")
		w.queueScope("A", w.queue2["A"]) // the contact also has a conversation in queue 2 (A2)
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			if n := update(ctx, q, w.tenant["A"], contactA); n != 1 {
				t.Errorf("contact with a conversation in the contract's queue: %d rows, want 1", n)
			}
		})
		w.queueScope("A") // an empty allow-list: nothing at all
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			if n := update(ctx, q, w.tenant["A"], contactA); n != 0 {
				t.Errorf("empty allow-list: %d rows, want 0", n)
			}
			if err := w.try(ctx, q, `INSERT INTO contact_account_links (tenant_id, contact_id, account_id, relationship_type, source) VALUES ($1,$2,$3,'other','manual')`, w.tenant["A"], contactA, accA); err == nil {
				t.Error("empty allow-list: a link was added to a contact the contract does not reach")
			}
		})
		w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = '{}' WHERE id = $1`, w.contract["A"])
	})

	t.Run("a revoked grant writes nothing", func(t *testing.T) {
		w.grantKeys(g, "contact.classify")
		w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g)
		defer w.exec(`UPDATE effective_access_grants SET status = 'active' WHERE id = $1`, g)
		w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
			if w.enter(ctx, q, agent, "A") {
				t.Error("the delegated context opened for a revoked grant")
			}
			if n := update(ctx, q, w.tenant["A"], contactA); n != 0 {
				t.Errorf("revoked: %d rows", n)
			}
		})
	})
}

// The two effects of a reclassification that touch conversations: narrow functions that check the key themselves.
func TestTheDelegatedReclassificationSideEffectsCheckTheKeyThemselves(t *testing.T) {
	w := newWorld(t)
	contactA := w.contactOf("A")
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "contact.read", "contact.classify", "conversation.read")
	// the conversation of the contact in queue 1 is open and unassigned; make the contact spam, as a member would have
	w.exec(`UPDATE contacts SET kind = 'spam' WHERE id = $1`, contactA)

	recompute := func(ctx context.Context, q platformdb.Querier, tenant uuid.UUID) error {
		return w.try(ctx, q, `SELECT * FROM delegated_recompute_contact_kinds($1, $2)`, tenant, contactA)
	}
	dequeue := func(ctx context.Context, q platformdb.Querier, tenant uuid.UUID) (int64, error) {
		_, err := q.Exec(ctx, "SAVEPOINT try")
		w.must(err)
		var n int64
		err = q.QueryRow(ctx, `SELECT delegated_dequeue_spam($1, $2)`, tenant, contactA).Scan(&n)
		if err != nil {
			_, rerr := q.Exec(ctx, "ROLLBACK TO SAVEPOINT try")
			w.must(rerr)
		}
		return n, err
	}

	t.Run("outside the delegated context both refuse", func(t *testing.T) {
		w.grantKeys(g, "contact.classify")
		w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
			if err := recompute(ctx, q, w.tenant["A"]); err == nil {
				t.Error("recompute worked outside the delegated context")
			}
		})
		w.inSession(agent, func(ctx context.Context, q platformdb.Querier) {
			if _, err := dequeue(ctx, q, w.tenant["A"]); err == nil {
				t.Error("dequeue worked outside the delegated context")
			}
		})
	})

	t.Run("a plain member calling them directly is refused too (they exist for the delegated context only)", func(t *testing.T) {
		member := w.user("member")
		w.directMember(member, "A")
		w.inSession(member, func(ctx context.Context, q platformdb.Querier) {
			if err := recompute(ctx, q, w.tenant["A"]); err == nil {
				t.Error("recompute worked for a member outside the delegated context")
			}
		})
		w.inSession(member, func(ctx context.Context, q platformdb.Querier) {
			if _, err := dequeue(ctx, q, w.tenant["A"]); err == nil {
				t.Error("dequeue worked for a member outside the delegated context")
			}
		})
	})

	t.Run("a member who is also a delegate, calling them as a member (not acting), is refused too", func(t *testing.T) {
		both := w.hubAgent("member-and-delegate")
		w.directMember(both, "A")
		gb := w.grant(both, "A")
		w.grantKeys(gb, "contact.classify")
		w.inSession(both, func(ctx context.Context, q platformdb.Querier) {
			if err := recompute(ctx, q, w.tenant["A"]); err == nil {
				t.Error("recompute worked outside the delegated context for a member who also has a grant")
			}
		})
		w.inSession(both, func(ctx context.Context, q platformdb.Querier) {
			if _, err := dequeue(ctx, q, w.tenant["A"]); err == nil {
				t.Error("dequeue worked outside the delegated context for a member who also has a grant")
			}
		})
	})

	t.Run("without contact.classify both refuse, even with the read keys", func(t *testing.T) {
		w.grantKeys(g, "contact.read", "conversation.read")
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			if err := recompute(ctx, q, w.tenant["A"]); err == nil {
				t.Error("recompute worked without contact.classify")
			}
		})
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			if _, err := dequeue(ctx, q, w.tenant["A"]); err == nil {
				t.Error("dequeue worked without contact.classify")
			}
		})
	})

	t.Run("for another instance both refuse", func(t *testing.T) {
		w.grantKeys(g, "contact.classify")
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			if err := recompute(ctx, q, w.tenant["B"]); err == nil {
				t.Error("recompute worked for an instance the grant does not cover")
			}
		})
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			if _, err := dequeue(ctx, q, w.tenant["B"]); err == nil {
				t.Error("dequeue worked for an instance the grant does not cover")
			}
		})
	})

	t.Run("with the key they take only the open, unassigned conversations out of the queue, and recompute the derived kind", func(t *testing.T) {
		w.grantKeys(g, "contact.classify")
		someone := w.user("someone")
		w.exec(`UPDATE conversations SET status = 'closed' WHERE id = $1`, w.conv["A2"])
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			n, err := dequeue(ctx, q, w.tenant["A"])
			w.must(err)
			if n != 1 { // A is open and unassigned; A2 is closed; A0 has no queue
				t.Errorf("with a closed conversation: dequeued %d, want 1", n)
			}
		})
		w.exec(`UPDATE conversations SET status = 'open', assigned_to_user_id = $2 WHERE id = $1`, w.conv["A2"], someone)
		w.exec(`UPDATE conversations SET queue_id = $2 WHERE id = $1`, w.conv["A"], w.queue1["A"])
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			n, err := dequeue(ctx, q, w.tenant["A"])
			w.must(err)
			if n != 1 { // A is dequeued again; A2 is held by someone
				t.Errorf("with an assigned conversation: dequeued %d, want 1", n)
			}
			w.must(recompute(ctx, q, w.tenant["A"]))
		})
		if got := w.ownerOne(`SELECT (queue_id IS NOT NULL)::text FROM conversations WHERE id = $1`, w.conv["A2"]); got != "true" {
			t.Errorf("the conversation somebody holds lost its queue")
		}
		if got := w.ownerOne(`SELECT conversation_kind FROM conversations WHERE id = $1`, w.conv["A"]); got != "external_other" {
			t.Errorf("conversation_kind = %s, want external_other (spam is not customer service)", got)
		}
	})

	t.Run("a contact that is not spam is never taken out of a queue", func(t *testing.T) {
		w.exec(`UPDATE contacts SET kind = 'other' WHERE id = $1`, contactA)
		w.exec(`UPDATE conversations SET queue_id = $2 WHERE id = $1`, w.conv["A"], w.queue1["A"])
		w.grantKeys(g, "contact.classify")
		w.attend(agent, "A", func(ctx context.Context, q platformdb.Querier) {
			n, err := dequeue(ctx, q, w.tenant["A"])
			w.must(err)
			if n != 0 {
				t.Errorf("dequeued %d conversations of a contact that is not spam", n)
			}
		})
	})
}

type contactsAPI struct {
	w *world
	h http.Handler
}

func (w *world) contactsAPI(t *testing.T) *contactsAPI {
	t.Helper()
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	audit := auditadapters.NewPostgresAuditEventRepository(w.app)
	contacts := contactsadapters.NewContactsAPIHandler(w.app).WithAudit(audit)
	classification := contactsadapters.NewClassificationHandler(w.app, audit)
	accounts := accountsadapters.NewHandler(w.app, audit)
	team := tenancyadapters.NewTeamHandler(w.app, audit, false)
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
	route := func(key string, h http.HandlerFunc) http.Handler {
		return tenancyadapters.Delegable(key, who(session(h)))
	}
	const t0 = "/api/v1/tenants/{tenant_id}"
	mux := http.NewServeMux()
	mux.Handle("PATCH "+t0+"/contacts/{contact_id}", route("contact.classify", contacts.SetKind))
	mux.Handle("PUT "+t0+"/contacts/{contact_id}/details", route("contact.classify", contacts.UpdateDetails))
	mux.Handle("GET "+t0+"/contacts/{contact_id}/classification", route("account.read", classification.GetClassification))
	mux.Handle("PUT "+t0+"/contacts/{contact_id}/classification", route("contact.classify", classification.PutClassification))
	mux.Handle("POST "+t0+"/contacts/{contact_id}/accounts", route("contact.classify", classification.LinkAccount))
	mux.Handle("POST "+t0+"/contacts/{contact_id}/accounts/{link_id}/end", route("contact.classify", classification.EndLink))
	mux.Handle("POST "+t0+"/contacts/{contact_id}/accounts/{link_id}/primary", route("contact.classify", classification.SetPrimary))
	mux.Handle("GET "+t0+"/accounts", route("account.read", accounts.ListAccounts))
	mux.Handle("GET "+t0+"/me/access", route("conversation.read", team.MyAccess))
	// not marked delegable: notes have no key in the catalog yet
	mux.Handle("GET "+t0+"/contacts/{contact_id}/notes", who(session(http.HandlerFunc(contacts.ListNotes))))
	return &contactsAPI{w: w, h: mux}
}

func (a *contactsAPI) do(method, path, body string, user uuid.UUID, acting string) (int, string) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Test-User", user.String())
	req.Header.Set("Content-Type", "application/json")
	if acting != "" {
		req.Header.Set("X-Omnira-Acting-As", acting)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestDelegatedClassificationThroughTheRealHandlers(t *testing.T) {
	w := newWorld(t)
	api := w.contactsAPI(t)
	contactA, contactB := w.contactOf("A"), w.contactOf("B")
	accA := w.account("A", "Empresa A")
	w.account("A", "Outra Empresa A")
	w.account("B", "Empresa B")
	agent := w.hubAgent("agent")
	g := w.grant(agent, "A")
	w.ceiling("A", "conversation.read", "contact.read", "contact.classify", "account.read", "conversation.claim")
	w.grantKeys(g, "conversation.read", "contact.read", "contact.classify", "account.read")
	hub := "hub:" + w.hub.String()
	base := "/api/v1/tenants/" + w.tenant["A"].String()
	contact := base + "/contacts/" + contactA.String()
	kindOf := func(id uuid.UUID) string { return w.ownerOne(`SELECT kind FROM contacts WHERE id = $1`, id) }
	audited := func(action string) int {
		return w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND actor_id = $2 AND action = $3 AND metadata->>'acting_as' = $4`, w.tenant["A"], agent, action, hub)
	}

	t.Run("kind: other, then spam (which takes the unassigned conversations out of the queue), audited with the hub context", func(t *testing.T) {
		if code, body := api.do("PATCH", contact, `{"kind":"other"}`, agent, hub); code != 200 {
			t.Fatalf("kind other: %d %s", code, body)
		}
		if kindOf(contactA) != "other" {
			t.Errorf("kind = %s", kindOf(contactA))
		}
		if code, body := api.do("PATCH", contact, `{"kind":"spam"}`, agent, hub); code != 200 {
			t.Fatalf("kind spam: %d %s", code, body)
		}
		if n := w.count(`SELECT count(*) FROM conversations WHERE contact_id = $1 AND status = 'open' AND assigned_to_user_id IS NULL AND queue_id IS NOT NULL`, contactA); n != 0 {
			t.Errorf("%d unassigned conversations still in a queue after marking spam", n)
		}
		if audited("contact.kind_changed") != 2 {
			t.Errorf("audit rows naming the hub context: %d, want 2", audited("contact.kind_changed"))
		}
		if code, _ := api.do("PATCH", contact, `{"kind":"unclassified"}`, agent, hub); code != 200 {
			t.Errorf("restore: %d", code)
		}
	})

	t.Run("customer needs an account: through the existing-account reference, never through the directory", func(t *testing.T) {
		if code, _ := api.do("PUT", contact+"/classification", `{"kind":"customer"}`, agent, hub); code != 422 {
			t.Errorf("customer without an account: %d, want 422", code)
		}
		if code, body := api.do("PUT", contact+"/classification", `{"kind":"customer","accounts":[{"directory_company_id":"123"}]}`, agent, hub); code != 403 {
			t.Errorf("a directory (ERP) company through the hub: %d %s, want 403", code, body)
		}
		if code, body := api.do("PUT", contact+"/classification", `{"kind":"customer","accounts":[{"account_id":"`+accA.String()+`","primary":true}]}`, agent, hub); code != 200 {
			t.Fatalf("customer with an existing account: %d %s", code, body)
		}
		if kindOf(contactA) != "customer" {
			t.Errorf("kind = %s", kindOf(contactA))
		}
		if got := w.ownerOne(`SELECT conversation_kind FROM conversations WHERE id = $1`, w.conv["A"]); got != "customer_service" {
			t.Errorf("the derived conversation kind = %s, want customer_service", got)
		}
		if code, body := api.do("GET", contact+"/classification", "", agent, hub); code != 200 || !strings.Contains(body, "Empresa A") {
			t.Errorf("classification view: %d %s", code, body)
		}
	})

	t.Run("an account of another instance cannot be linked", func(t *testing.T) {
		var accB uuid.UUID
		w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM customer_accounts WHERE tenant_id = $1`, w.tenant["B"]).Scan(&accB))
		if code, _ := api.do("POST", contact+"/accounts", `{"account_id":"`+accB.String()+`"}`, agent, hub); code == 200 || code == 201 {
			t.Errorf("an account of another instance was linked: %d", code)
		}
	})

	t.Run("the account list is the instance's own", func(t *testing.T) {
		code, body := api.do("GET", base+"/accounts", "", agent, hub)
		if code != 200 || !strings.Contains(body, "Empresa A") || strings.Contains(body, "Empresa B") {
			t.Errorf("accounts: %d %s", code, body)
		}
	})

	t.Run("the kind internal is the instance's own call", func(t *testing.T) {
		if code, _ := api.do("PUT", contact+"/classification", `{"kind":"internal","internal_role":"team"}`, agent, hub); code != 403 {
			t.Errorf("internal through the hub: %d, want 403", code)
		}
	})

	t.Run("a contact the instance declared internal is not reclassified by a hub agent, a member still can", func(t *testing.T) {
		w.exec(`UPDATE contacts SET kind = 'internal', internal_role = 'team' WHERE id = $1`, contactA)
		defer w.exec(`UPDATE contacts SET kind = 'unclassified', internal_role = NULL WHERE id = $1`, contactA)
		if code, _ := api.do("PATCH", contact, `{"kind":"other"}`, agent, hub); code != 403 {
			t.Errorf("PATCH away from internal through the hub: %d, want 403", code)
		}
		if code, _ := api.do("PUT", contact+"/classification", `{"kind":"other"}`, agent, hub); code != 403 {
			t.Errorf("PUT away from internal through the hub: %d, want 403", code)
		}
		if kindOf(contactA) != "internal" {
			t.Errorf("the internal contact became %s", kindOf(contactA))
		}
		member := w.hubAgent("member2")
		w.directMember(member, "A")
		if code, body := api.do("PATCH", contact, `{"kind":"other"}`, member, "member"); code != 200 {
			t.Errorf("a member reclassifies the internal contact as before: %d %s", code, body)
		}
	})

	t.Run("details: alias and e-mail", func(t *testing.T) {
		if code, body := api.do("PUT", contact+"/details", `{"alias":"Fulano da Obra","email":"f@example.com"}`, agent, hub); code != 200 {
			t.Fatalf("details: %d %s", code, body)
		}
		if got := w.ownerOne(`SELECT COALESCE(alias,'') || '|' || COALESCE(email,'') FROM contacts WHERE id = $1`, contactA); got != "Fulano da Obra|f@example.com" {
			t.Errorf("stored %q", got)
		}
	})

	t.Run("another instance's contact is not reachable through this instance's URL", func(t *testing.T) {
		if code, _ := api.do("PATCH", base+"/contacts/"+contactB.String(), `{"kind":"spam"}`, agent, hub); code != 404 {
			t.Errorf("B's contact through A: %d, want 404", code)
		}
		if kindOf(contactB) != "unclassified" {
			t.Errorf("B's contact changed to %s", kindOf(contactB))
		}
		if code, _ := api.do("PATCH", "/api/v1/tenants/"+w.tenant["B"].String()+"/contacts/"+contactB.String(), `{"kind":"spam"}`, agent, hub); code != 404 {
			t.Errorf("an instance with no grant: %d, want 404", code)
		}
	})

	t.Run("by key: without contact.classify every write is a 403, reads follow their own key", func(t *testing.T) {
		w.grantKeys(g, "conversation.read", "contact.read", "account.read")
		for _, c := range []struct{ method, path, body string }{
			{"PATCH", contact, `{"kind":"other"}`},
			{"PUT", contact + "/details", `{"alias":"x"}`},
			{"PUT", contact + "/classification", `{"kind":"other"}`},
			{"POST", contact + "/accounts", `{"account_id":"` + accA.String() + `"}`},
		} {
			if code, _ := api.do(c.method, c.path, c.body, agent, hub); code != 403 {
				t.Errorf("%s %s without contact.classify: %d, want 403", c.method, c.path, code)
			}
		}
		if code, _ := api.do("GET", contact+"/classification", "", agent, hub); code != 200 {
			t.Errorf("classification read with account.read: %d", code)
		}
		w.grantKeys(g, "conversation.read", "contact.read")
		if code, _ := api.do("GET", base+"/accounts", "", agent, hub); code != 403 {
			t.Errorf("account list without account.read: %d, want 403", code)
		}
		// account.read alone is enough to list the accounts: the data layer answers by the contact domain, not by what else the grant holds
		w.grantKeys(g, "account.read")
		if code, body := api.do("GET", base+"/accounts", "", agent, hub); code != 200 || !strings.Contains(body, "Empresa A") {
			t.Errorf("account list with account.read alone: %d %s", code, body)
		}
		w.grantKeys(g, "conversation.read", "contact.read", "contact.classify", "account.read")
	})

	t.Run("an unmarked route (notes) is a 404 for the delegate", func(t *testing.T) {
		if code, _ := api.do("GET", contact+"/notes", "", agent, hub); code != 404 {
			t.Errorf("notes in the delegated context: %d, want 404", code)
		}
	})

	t.Run("/me/access in the delegated context is the delegated keys, never the membership's", func(t *testing.T) {
		both := w.hubAgent("both")
		w.directMember(both, "A") // tenant_agent: has conversation.claim and more
		gb := w.grant(both, "A")
		w.grantKeys(gb, "conversation.read", "contact.read")
		code, body := api.do("GET", base+"/me/access", "", both, hub)
		if code != 200 {
			t.Fatalf("me/access acting: %d %s", code, body)
		}
		var got struct {
			RoleKey     string   `json:"role_key"`
			Permissions []string `json:"permissions"`
		}
		w.must(json.Unmarshal([]byte(body), &got))
		if got.RoleKey != "hub_delegate" || strings.Join(sorted(got.Permissions), ",") != "contact.read,conversation.read" {
			t.Errorf("acting: role %q keys %v, want hub_delegate with exactly the two delegated keys", got.RoleKey, got.Permissions)
		}
		code, body = api.do("GET", base+"/me/access", "", both, "member")
		if code != 200 || strings.Contains(body, "hub_delegate") || !strings.Contains(body, "conversation.claim") {
			t.Errorf("as a member: %d %s", code, body)
		}
	})

	t.Run("a member who is also a delegate: each context gives exactly its own", func(t *testing.T) {
		both := w.hubAgent("both2")
		w.directMember(both, "A")
		gb := w.grant(both, "A")
		w.grantKeys(gb, "conversation.read", "contact.read") // no contact.classify as a delegate
		if code, body := api.do("PATCH", contact, `{"kind":"other"}`, both, "member"); code != 200 {
			t.Errorf("as a member the kind change works as before: %d %s", code, body)
		}
		if code, _ := api.do("PATCH", contact, `{"kind":"spam"}`, both, hub); code != 403 {
			t.Errorf("acting for the hub without contact.classify the membership must give nothing: %d, want 403", code)
		}
	})

	t.Run("revocation refuses the very next write", func(t *testing.T) {
		w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE id = $1`, g)
		if code, _ := api.do("PATCH", contact, `{"kind":"other"}`, agent, hub); code != 404 {
			t.Errorf("after revoking: %d, want 404", code)
		}
	})
}

func sorted(in []string) []string { return sortedCopy(in) }

// Every named preset of hubctl is a set of keys the catalogue really delegates, and a grant made from it is exactly what it names.
func TestEveryServingPresetCanBeProvisionedAndIsWhatItNames(t *testing.T) {
	w := newWorld(t)
	svc := w.provisioner(t)
	agent := w.hubAgent("agent")
	w.grant(agent, "A")
	for name, keys := range provisioning.ServingPresets {
		w.must(svc.SetCeiling(w.ctx, w.hub, w.tenant["A"], keys))
		w.must(svc.SetServing(w.ctx, w.hub, w.tenant["A"], agent, keys))
		if got, want := strings.Join(w.delegated(agent, "A"), ","), strings.Join(sortedCopy(keys), ","); got != want {
			t.Errorf("preset %s: the agent holds %s, want %s", name, got, want)
		}
	}
	if keys := provisioning.ServingPresets["classificacao"]; !strings.Contains(strings.Join(keys, ","), "contact.classify") || !strings.Contains(strings.Join(keys, ","), "account.read") {
		t.Errorf("the classificacao preset lost its keys: %v", keys)
	}
	// no preset ever carries a secret- or administration-class key
	for name, keys := range provisioning.ServingPresets {
		for _, k := range keys {
			for _, banned := range []string{"membership.", "channel.", "integration.", "billing.", "role.", "audit.", "settings."} {
				if strings.HasPrefix(k, banned) {
					t.Errorf("preset %s carries %s", name, k)
				}
			}
		}
	}
}

// Codex review of phase 04a (HIGH-1): a person served by TWO hubs on the same instance, with disjoint queue scopes, acts for ONE hub at a time and sees
// (and can change) only what THAT hub's contract covers; the contact and file policies inherit it.
func TestAPersonServedByTwoHubsActsForOneAtATime(t *testing.T) {
	w := newWorld(t)
	w.mediaOf("A", "A")  // in queue 1
	w.mediaOf("A", "A2") // in queue 2
	// a contact whose ONLY conversation is in queue 2
	onlyQ2 := uuid.New()
	w.exec(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1, $2, 'So fila 2', '+5511900000099')`, onlyQ2, w.tenant["A"])
	convQ2 := w.conversation(w.tenant["A"], onlyQ2, &[]uuid.UUID{w.queue2["A"]}[0])
	// hub 1 serves queue 1 only; hub 2 serves queue 2 only; the same person holds a grant through each
	person := w.hubAgent("both-hubs")
	g1 := w.grant(person, "A")
	w.ceiling("A", "conversation.read", "media.read", "contact.read", "contact.classify")
	w.grantKeys(g1, "conversation.read", "media.read", "contact.read", "contact.classify")
	w.queueScope("A", w.queue1["A"])
	hub2 := uuid.New()
	w.exec(`INSERT INTO service_hubs(id, name) VALUES($1, $2)`, hub2, "Other "+hub2.String()[:8])
	raw, _ := json.Marshal(map[string]any{"queue_ids": []string{w.queue2["A"].String()}})
	w.exec(`INSERT INTO hub_tenant_service_contracts(id, hub_id, tenant_id, valid_from, service_scope, delegable_permissions)
	        VALUES($1,$2,$3, now() - interval '1 day', $4::jsonb, ARRAY['conversation.read','media.read','contact.read','contact.classify'])`, uuid.New(), hub2, w.tenant["A"], string(raw))
	w.exec(`INSERT INTO hub_memberships(hub_id, user_id, role_id) VALUES($1,$2,$3)`, hub2, person, w.roleHubAgent)
	w.exec(`INSERT INTO effective_access_grants(hub_id, user_id, tenant_id, service_contract_id, valid_from, permissions)
	        SELECT $1, $2, $3, c.id, now() - interval '1 day', ARRAY['conversation.read','media.read','contact.read','contact.classify'] FROM hub_tenant_service_contracts c WHERE c.hub_id = $1 AND c.tenant_id = $3`, hub2, person, w.tenant["A"])

	actFor := func(hub uuid.UUID, fn func(ctx context.Context, q platformdb.Querier)) {
		w.inSession(person, func(ctx context.Context, q platformdb.Querier) {
			var ok *bool
			w.must(q.QueryRow(ctx, `SELECT lock_served_tenant($1,$2,$3)`, w.tenant["A"], person, hub).Scan(&ok))
			if ok == nil || !*ok {
				t.Fatalf("could not act for hub %s", hub)
			}
			fn(ctx, q)
		})
	}
	count := func(ctx context.Context, q platformdb.Querier, table, where string, args ...any) int {
		var n int
		w.must(q.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1 `+where, append([]any{w.tenant["A"]}, args...)...).Scan(&n))
		return n
	}
	t.Run("acting for hub 1: only hub 1's queue", func(t *testing.T) {
		actFor(w.hub, func(ctx context.Context, q platformdb.Querier) {
			if n := count(ctx, q, "conversations", "AND id = ANY($2)", []uuid.UUID{w.conv["A"], w.conv["A2"], convQ2}); n != 1 {
				t.Errorf("conversations seen: %d, want 1 (only queue 1)", n)
			}
			if n := count(ctx, q, "contacts", "AND id = $2", onlyQ2); n != 0 {
				t.Error("the contact that exists only in hub 2's queue was visible")
			}
			if n := count(ctx, q, "message_media", ""); n != 1 {
				t.Errorf("files seen: %d, want 1 (only queue 1)", n)
			}
			tag, err := q.Exec(ctx, `UPDATE contacts SET alias = 'x' WHERE id = $1`, onlyQ2)
			w.must(err)
			if tag.RowsAffected() != 0 {
				t.Error("hub 1 updated a contact that only hub 2 reaches")
			}
		})
	})
	t.Run("acting for hub 2: only hub 2's queue", func(t *testing.T) {
		actFor(hub2, func(ctx context.Context, q platformdb.Querier) {
			if n := count(ctx, q, "conversations", "AND id = ANY($2)", []uuid.UUID{w.conv["A"], w.conv["A2"], convQ2}); n != 2 {
				t.Errorf("conversations seen: %d, want 2 (queue 2)", n)
			}
			if n := count(ctx, q, "contacts", "AND id = $2", onlyQ2); n != 1 {
				t.Error("hub 2's own contact was not visible")
			}
			if n := count(ctx, q, "message_media", ""); n != 1 {
				t.Errorf("files seen: %d, want 1 (only queue 2)", n)
			}
			if n := count(ctx, q, "messages", "AND conversation_id = $2", w.conv["A"]); n != 0 {
				t.Error("hub 2 read the messages of hub 1's queue")
			}
		})
	})
	t.Run("the side-effect functions cannot be pointed at a contact outside the hub's scope", func(t *testing.T) {
		w.exec(`UPDATE contacts SET kind = 'spam' WHERE id = $1`, onlyQ2)
		defer w.exec(`UPDATE contacts SET kind = 'unclassified' WHERE id = $1`, onlyQ2)
		actFor(w.hub, func(ctx context.Context, q platformdb.Querier) {
			if err := w.try(ctx, q, `SELECT delegated_dequeue_spam($1, $2)`, w.tenant["A"], onlyQ2); err == nil {
				t.Error("dequeue worked for a contact outside the hub's scope")
			}
			if err := w.try(ctx, q, `SELECT * FROM delegated_recompute_contact_kinds($1, $2)`, w.tenant["A"], onlyQ2); err == nil {
				t.Error("recompute worked for a contact outside the hub's scope")
			}
		})
		if got := w.ownerOne(`SELECT (queue_id IS NOT NULL)::text FROM conversations WHERE id = $1`, convQ2); got != "true" {
			t.Error("the conversation outside the scope lost its queue")
		}
	})
	t.Run("not acting for a hub the restrictive policies change nothing", func(t *testing.T) {
		member := w.user("plain-member")
		w.directMember(member, "A")
		w.inSession(member, func(ctx context.Context, q platformdb.Querier) {
			if n := count(ctx, q, "conversations", ""); n < 4 {
				t.Errorf("a member sees %d conversations, want all 4", n)
			}
		})
	})
}
