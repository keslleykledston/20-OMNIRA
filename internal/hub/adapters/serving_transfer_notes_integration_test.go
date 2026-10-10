package adapters_test

// ADR-0040 phase 04c on a real PostgreSQL, with the application's own role and the REAL handlers: a Hub agent attending an instance may hand over a
// conversation THEY HOLD (key conversation.transfer) or give it back to the queue (conversation.claim), and may read and write the notes of a contact
// (key contact.note). What must hold:
//   - the routes answer by KEY (a missing key is a 403, an unmarked route a 404, another instance 404) and the legacy reply-capable grant is still needed;
//   - only the current holder hands over; the person chosen must be able to take the conversation (a live reply-capable grant for that instance and queue);
//     the directory offered is the hub's own people, never the instance's members; a member's request reaches the ORIGINAL handler untouched;
//   - the data layer answers notes by the KEY itself, not by the domain: a contact reader or classifier cannot read the notes; only the author edits or
//     removes a note; another instance and a queue outside the contract's scope show nothing; no policy opens `conversations` for writing.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	contactsadapters "github.com/omnira/omnira/internal/contacts/adapters"
	adapters "github.com/omnira/omnira/internal/hub/adapters"
	"github.com/omnira/omnira/internal/hub/provisioning"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

type transferAPI struct {
	w        *world
	h        http.Handler
	original atomic.Int32 // how many times an ORIGINAL (member) handler ran
}

func (w *world) transferAPI(t *testing.T) *transferAPI {
	t.Helper()
	tenancyadapters.EnableDelegatedServing(true)
	t.Cleanup(func() { tenancyadapters.EnableDelegatedServing(false) })
	a := &transferAPI{w: w}
	dw := adapters.NewDelegatedWrites(w.app)
	contacts := contactsadapters.NewContactsAPIHandler(w.app).WithAudit(auditadapters.NewPostgresAuditEventRepository(w.app))
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
	orig := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { a.original.Add(1); rw.WriteHeader(http.StatusNoContent) })
	const t0 = "/api/v1/tenants/{tenant_id}"
	const c0 = t0 + "/inbox/conversations/{conversation_id}"
	mux := http.NewServeMux()
	mux.Handle("POST "+c0+"/assign", tenancyadapters.Delegable("conversation.claim", who(session(dw.Claim(orig)))))
	mux.Handle("POST "+c0+"/unassign", tenancyadapters.Delegable("conversation.claim", who(session(dw.Release(orig)))))
	mux.Handle("POST "+c0+"/transfer", tenancyadapters.Delegable("conversation.transfer", who(session(dw.Transfer(orig)))))
	mux.Handle("GET "+c0+"/transfer-candidates", tenancyadapters.Delegable("conversation.transfer", who(session(dw.TransferCandidates()))))
	mux.Handle("GET "+t0+"/contacts/{contact_id}/notes", tenancyadapters.Delegable("contact.note", who(session(http.HandlerFunc(contacts.ListNotes)))))
	mux.Handle("POST "+t0+"/contacts/{contact_id}/notes", tenancyadapters.Delegable("contact.note", who(session(http.HandlerFunc(contacts.AddNote)))))
	mux.Handle("PUT "+t0+"/contacts/{contact_id}/notes/{note_id}", tenancyadapters.Delegable("contact.note", who(session(http.HandlerFunc(contacts.EditNote)))))
	mux.Handle("DELETE "+t0+"/contacts/{contact_id}/notes/{note_id}", tenancyadapters.Delegable("contact.note", who(session(http.HandlerFunc(contacts.DeleteNote)))))
	a.h = mux
	return a
}

func (a *transferAPI) do(method, path, body string, user uuid.UUID, acting string) (int, string) {
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

func (a *transferAPI) conv(tenantKey, convKey, action string) string {
	return "/api/v1/tenants/" + a.w.tenant[tenantKey].String() + "/inbox/conversations/" + a.w.conv[convKey].String() + "/" + action
}

// colleague is a hub person who may attend the instance: a live, reply-capable grant with the keys of the "completo" preset.
func (w *world) colleague(name, tenantKey string, keys ...string) uuid.UUID {
	if keys == nil {
		keys = provisioning.ServingPresets["completo"]
	}
	u := w.hubAgent(name)
	w.grantKeys(w.grantReply(u, tenantKey), keys...)
	return u
}

func TestDelegatedTransferAndReleaseThroughTheInstanceRoutes(t *testing.T) {
	w := newWorld(t)
	api := w.transferAPI(t)
	w.ceiling("A", provisioning.ServingPresets["completo"]...)
	w.ceiling("B", provisioning.ServingPresets["completo"]...)
	hub := "hub:" + w.hub.String()
	holder := w.colleague("holder", "A")
	peer := w.colleague("peer", "A")
	readOnly := w.hubAgent("readonly") // a grant WITHOUT the reply capability
	w.grantKeys(w.grant(readOnly, "A"), provisioning.ServingPresets["completo"]...)
	onlyB := w.colleague("onlyb", "B")
	noGrant := w.hubAgent("nogrant")
	_ = onlyB
	_ = noGrant
	t.Run("every key of the completo preset is delegable (it has a data domain)", func(t *testing.T) {
		got, want := w.delegated(holder, "A"), append([]string(nil), provisioning.ServingPresets["completo"]...)
		sort.Strings(want)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("delegated keys = %v, want the whole preset %v", got, want)
		}
	})
	claim := func(user uuid.UUID, convKey string) {
		t.Helper()
		if code, body := api.do("POST", api.conv("A", convKey, "assign"), `{}`, user, hub); code != 200 {
			t.Fatalf("claim: %d %s", code, body)
		}
	}
	holderIs := func(convKey string, want *uuid.UUID) {
		t.Helper()
		got := w.assignee(convKey)
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Fatalf("assignee of %s = %v, want %v", convKey, got, want)
		}
	}

	t.Run("only the holder asks for the directory, and it is the hub's own people who can take THIS conversation", func(t *testing.T) {
		if code, _ := api.do("GET", api.conv("A", "A", "transfer-candidates"), "", holder, hub); code != 409 {
			t.Errorf("candidates before holding the conversation: %d, want 409", code)
		}
		claim(holder, "A")
		code, body := api.do("GET", api.conv("A", "A", "transfer-candidates"), "", holder, hub)
		if code != 200 {
			t.Fatalf("candidates: %d %s", code, body)
		}
		if !strings.Contains(body, peer.String()) {
			t.Errorf("the colleague with a live reply grant is missing: %s", body)
		}
		for name, id := range map[string]uuid.UUID{"self": holder, "read-only grant": readOnly, "grant on another instance only": onlyB, "no grant": noGrant} {
			if strings.Contains(body, id.String()) {
				t.Errorf("%s is offered as a candidate: %s", name, body)
			}
		}
		// the instance's own members are not a directory reachable from here
		member := w.user("member")
		w.directMember(member, "A")
		if _, body := api.do("GET", api.conv("A", "A", "transfer-candidates"), "", holder, hub); strings.Contains(body, member.String()) {
			t.Errorf("a member of the instance is offered: %s", body)
		}
	})

	t.Run("a person with the claim key but not the transfer key can give back but not hand over, and sees no directory", func(t *testing.T) {
		light := w.colleague("light", "A", provisioning.ServingPresets["atendimento"]...)
		claim(light, "A2")
		if code, _ := api.do("GET", api.conv("A", "A2", "transfer-candidates"), "", light, hub); code != 403 {
			t.Errorf("candidates without the key: %d, want 403", code)
		}
		if code, _ := api.do("POST", api.conv("A", "A2", "transfer"), `{"target_user_id":"`+peer.String()+`"}`, light, hub); code != 403 {
			t.Errorf("transfer without the key: %d, want 403", code)
		}
		holderIs("A2", &light)
		if code, body := api.do("POST", api.conv("A", "A2", "unassign"), `{}`, light, hub); code != 200 {
			t.Fatalf("release with the claim key: %d %s", code, body)
		}
		holderIs("A2", nil)
	})

	t.Run("handing over: only the holder, only to someone who can take it, and it is audited and in the history", func(t *testing.T) {
		holderIs("A", &holder)
		// not the holder
		if code, _ := api.do("POST", api.conv("A", "A", "transfer"), `{"target_user_id":"`+holder.String()+`"}`, peer, hub); code != 409 {
			t.Errorf("transfer by someone who does not hold it: %d, want 409", code)
		}
		holderIs("A", &holder)
		// chosen people who cannot take it, and the holder themself
		for name, target := range map[string]uuid.UUID{"read-only grant": readOnly, "grant on another instance only": onlyB, "no grant": noGrant, "self": holder, "a user with no access at all": w.user("outsider")} {
			if code, body := api.do("POST", api.conv("A", "A", "transfer"), `{"target_user_id":"`+target.String()+`"}`, holder, hub); code != 409 {
				t.Errorf("transfer to %s: %d %s, want 409", name, code, body)
			}
		}
		for name, body := range map[string]string{"empty": `{}`, "null": `{"target_user_id":null}`, "not a uuid": `{"target_user_id":"x"}`} {
			if code, _ := api.do("POST", api.conv("A", "A", "transfer"), body, holder, hub); code != 400 {
				t.Errorf("transfer with %s target: %d, want 400", name, code)
			}
		}
		holderIs("A", &holder)

		code, body := api.do("POST", api.conv("A", "A", "transfer"), `{"target_user_id":"`+peer.String()+`"}`, holder, hub)
		if code != 200 || !strings.Contains(body, peer.String()) {
			t.Fatalf("transfer: %d %s", code, body)
		}
		holderIs("A", &peer)
		if n := w.count(`SELECT count(*) FROM assignment_events WHERE conversation_id = $1 AND reason = 'hub_transfer' AND to_user_id = $2 AND from_user_id = $3`, w.conv["A"], peer, holder); n != 1 {
			t.Errorf("assignment history rows for the transfer = %d, want 1", n)
		}
		if n := w.count(`SELECT count(*) FROM audit_events WHERE actor_id = $1 AND tenant_id = $2 AND action = 'hub.conversation.transferred'`, holder, w.tenant["A"]); n != 1 {
			t.Errorf("audit rows for the transfer = %d, want 1", n)
		}
		// the previous holder no longer holds it: neither hands over nor gives back
		if code, _ := api.do("POST", api.conv("A", "A", "transfer"), `{"target_user_id":"`+holder.String()+`"}`, holder, hub); code != 409 {
			t.Errorf("a second transfer by the previous holder: %d, want 409", code)
		}
		if code, _ := api.do("POST", api.conv("A", "A", "unassign"), `{}`, holder, hub); code != 409 {
			t.Errorf("release by the previous holder: %d, want 409", code)
		}
		holderIs("A", &peer)
	})

	t.Run("giving back: the new holder releases to the queue, once", func(t *testing.T) {
		if code, body := api.do("POST", api.conv("A", "A", "unassign"), `{}`, peer, hub); code != 200 {
			t.Fatalf("release: %d %s", code, body)
		}
		holderIs("A", nil)
		if n := w.count(`SELECT count(*) FROM assignment_events WHERE conversation_id = $1 AND reason = 'hub_release' AND from_user_id = $2 AND to_user_id IS NULL`, w.conv["A"], peer); n != 1 {
			t.Errorf("assignment history rows for the release = %d, want 1", n)
		}
		if code, _ := api.do("POST", api.conv("A", "A", "unassign"), `{}`, peer, hub); code != 409 {
			t.Errorf("a second release: %d, want 409", code)
		}
	})

	t.Run("the walls: another instance, no context, a revoked grant, a lost reply capability", func(t *testing.T) {
		claim(holder, "A")
		// the conversation of instance B through the URL of A, and through B without any grant there
		if code, _ := api.do("POST", "/api/v1/tenants/"+w.tenant["A"].String()+"/inbox/conversations/"+w.conv["B"].String()+"/transfer", `{"target_user_id":"`+peer.String()+`"}`, holder, hub); code != 404 {
			t.Errorf("a conversation of another instance through this instance's URL: %d, want 404", code)
		}
		if code, _ := api.do("POST", api.conv("B", "B", "transfer"), `{"target_user_id":"`+peer.String()+`"}`, holder, hub); code != 404 {
			t.Errorf("an instance the person has no grant for: %d, want 404", code)
		}
		// a member's request (no acting header) reaches the ORIGINAL handlers untouched
		member := w.user("member2")
		w.directMember(member, "A")
		before := api.original.Load()
		for _, route := range []struct{ method, action string }{{"POST", "transfer"}, {"POST", "unassign"}} {
			if code, _ := api.do(route.method, api.conv("A", "A", route.action), `{"target_user_id":"`+peer.String()+`"}`, member, ""); code != 204 {
				t.Errorf("member %s: %d, want the original handler (204)", route.action, code)
			}
		}
		if api.original.Load() != before+2 {
			t.Error("a member's request did not reach the original handlers")
		}
		// the candidates route exists only for the delegated context
		if code, _ := api.do("GET", api.conv("A", "A", "transfer-candidates"), "", member, ""); code != 404 {
			t.Errorf("candidates for a member: %d, want 404", code)
		}
		// the data layer still refuses every direct write to the conversation
		w.attend(holder, "A", func(ctx context.Context, q platformdb.Querier) {
			tag, err := q.Exec(ctx, `UPDATE conversations SET assigned_to_user_id = NULL WHERE id = $1`, w.conv["A"])
			if err == nil && tag.RowsAffected() != 0 {
				t.Error("a delegate rewrote a conversation directly")
			}
		})
		holderIs("A", &holder)

		// the reply capability is a separate condition
		w.exec(`UPDATE effective_access_grants SET can_reply = false WHERE user_id = $1 AND tenant_id = $2`, holder, w.tenant["A"])
		if code, _ := api.do("POST", api.conv("A", "A", "transfer"), `{"target_user_id":"`+peer.String()+`"}`, holder, hub); code == 200 {
			t.Error("transferred without the reply capability")
		}
		w.exec(`UPDATE effective_access_grants SET can_reply = true WHERE user_id = $1 AND tenant_id = $2`, holder, w.tenant["A"])
		// a revoked grant stops it at once
		w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE user_id = $1 AND tenant_id = $2`, holder, w.tenant["A"])
		if code, _ := api.do("POST", api.conv("A", "A", "transfer"), `{"target_user_id":"`+peer.String()+`"}`, holder, hub); code != 404 {
			t.Errorf("transfer with a revoked grant: %d, want 404", code)
		}
		holderIs("A", &holder)
	})

	t.Run("a chosen person whose grant was revoked meanwhile cannot take it (proven again inside the transaction)", func(t *testing.T) {
		keen := w.colleague("keen", "A")
		leaving := w.colleague("leaving", "A")
		claim(keen, "A0")
		w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE user_id = $1 AND tenant_id = $2`, leaving, w.tenant["A"])
		if code, _ := api.do("POST", api.conv("A", "A0", "transfer"), `{"target_user_id":"`+leaving.String()+`"}`, keen, hub); code != 409 {
			t.Errorf("transfer to a person whose grant is revoked: %d, want 409", code)
		}
		holderIs("A0", &keen)
	})
}

func TestDelegatedNotesFollowTheKeyTheAuthorAndTheInstance(t *testing.T) {
	w := newWorld(t)
	api := w.transferAPI(t)
	hub := "hub:" + w.hub.String()
	w.ceiling("A", provisioning.ServingPresets["completo"]...)
	contactA, contactB := w.contactOf("A"), w.contactOf("B")
	notes := func(tenantKey string, contact uuid.UUID) string {
		return "/api/v1/tenants/" + w.tenant[tenantKey].String() + "/contacts/" + contact.String() + "/notes"
	}
	w.ceiling("B", provisioning.ServingPresets["completo"]...)
	writer := w.colleague("writer", "A")
	w.grantKeys(w.grantReply(writer, "B"), provisioning.ServingPresets["completo"]...) // the same person also serves B: only the pin keeps B's notes out while acting for A
	other := w.colleague("other", "A")
	classifier := w.colleague("classifier", "A", provisioning.ServingPresets["classificacao"]...)
	member := w.user("member")
	w.directMember(member, "A")
	// a note written by one of the instance's own people, and one in another instance
	var memberNote, noteB uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `INSERT INTO contact_notes (tenant_id, contact_id, body, created_by_user_id) VALUES ($1,$2,'nota interna da empresa',$3) RETURNING id`, w.tenant["A"], contactA, member).Scan(&memberNote))
	w.must(w.owner.QueryRow(w.ctx, `INSERT INTO contact_notes (tenant_id, contact_id, body, created_by_user_id) VALUES ($1,$2,'nota de outra empresa',$3) RETURNING id`, w.tenant["B"], contactB, member).Scan(&noteB))

	var mine string
	t.Run("with the key the agent adds, reads, edits and removes their own note, and reads the company's", func(t *testing.T) {
		code, body := api.do("POST", notes("A", contactA), `{"body":"cliente prefere contato à tarde"}`, writer, hub)
		if code != 201 || !strings.Contains(body, "cliente prefere") {
			t.Fatalf("add: %d %s", code, body)
		}
		mine = w.ownerOne(`SELECT id::text FROM contact_notes WHERE tenant_id = $1 AND created_by_user_id = $2`, w.tenant["A"], writer)
		code, body = api.do("GET", notes("A", contactA), "", writer, hub)
		if code != 200 || !strings.Contains(body, "cliente prefere") || !strings.Contains(body, "nota interna da empresa") {
			t.Errorf("list: %d %s", code, body)
		}
		if strings.Contains(body, "nota de outra empresa") {
			t.Errorf("another instance's note in the list: %s", body)
		}
		if code, body := api.do("PUT", notes("A", contactA)+"/"+mine, `{"body":"cliente prefere contato de manhã"}`, writer, hub); code != 200 || !strings.Contains(body, "de manhã") {
			t.Errorf("edit own: %d %s", code, body)
		}
		if n := w.count(`SELECT count(*) FROM audit_events WHERE actor_id = $1 AND tenant_id = $2 AND action = 'contact.note_changed'`, writer, w.tenant["A"]); n != 2 {
			t.Errorf("audit rows for add+edit = %d, want 2 (the text itself never goes to the trail)", n)
		}
		if n := w.count(`SELECT count(*) FROM audit_events WHERE tenant_id = $1 AND metadata::text LIKE '%manhã%'`, w.tenant["A"]); n != 0 {
			t.Error("the note text reached the audit trail")
		}
	})

	t.Run("only the author edits or removes: not another agent, not the company's own note", func(t *testing.T) {
		for name, user := range map[string]uuid.UUID{"another agent": other, "the same agent on the company's note": writer} {
			target := mine
			if user == writer {
				target = memberNote.String()
			}
			if code, _ := api.do("PUT", notes("A", contactA)+"/"+target, `{"body":"reescrita"}`, user, hub); code != 404 {
				t.Errorf("edit by %s: %d, want 404", name, code)
			}
			if code, _ := api.do("DELETE", notes("A", contactA)+"/"+target, "", user, hub); code != 404 {
				t.Errorf("delete by %s: %d, want 404", name, code)
			}
		}
		if got := w.ownerOne(`SELECT body FROM contact_notes WHERE id = $1`, memberNote); got != "nota interna da empresa" {
			t.Errorf("the company's note was changed: %q", got)
		}
		if got := w.ownerOne(`SELECT body FROM contact_notes WHERE id = $1`, mine); !strings.Contains(got, "de manhã") {
			t.Errorf("the agent's note was changed by someone else: %q", got)
		}
		if code, _ := api.do("DELETE", notes("A", contactA)+"/"+mine, "", writer, hub); code != 204 {
			t.Errorf("remove own: %d, want 204", code)
		}
	})

	t.Run("reading the card or classifying does not open the notes: the key itself is needed, in the routes AND in the database", func(t *testing.T) {
		for _, m := range []string{"GET", "POST"} {
			if code, _ := api.do(m, notes("A", contactA), `{"body":"x"}`, classifier, hub); code != 403 {
				t.Errorf("%s notes with contact.classify only: %d, want 403", m, code)
			}
		}
		w.attend(classifier, "A", func(ctx context.Context, q platformdb.Querier) {
			var n int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM contact_notes`).Scan(&n))
			if n != 0 {
				t.Errorf("a classifier reads %d notes straight from the table", n)
			}
			if err := w.try(ctx, q, `INSERT INTO contact_notes (tenant_id, contact_id, body, created_by_user_id) VALUES ($1,$2,'x',$3)`, w.tenant["A"], contactA, classifier); err == nil {
				t.Error("a classifier inserted a note")
			}
		})
		w.attend(writer, "A", func(ctx context.Context, q platformdb.Querier) {
			var n int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM contact_notes`).Scan(&n))
			if n != 1 {
				t.Errorf("the writer sees %d notes of the instance, want exactly the company's 1 (never another instance's)", n)
			}
		})
	})

	t.Run("a note-only agent holds no contact write at all: the key gives the notes and the sight of the contact, nothing else", func(t *testing.T) {
		noteOnly := w.colleague("noteonly", "A", "conversation.read", "contact.note")
		w.attend(noteOnly, "A", func(ctx context.Context, q platformdb.Querier) {
			if w.domainAccess(ctx, q, noteOnly, "A", "contact", "write") {
				t.Error("contact.note opened the contact WRITE domain")
			}
			tag, err := q.Exec(ctx, `UPDATE contacts SET alias = 'x' WHERE tenant_id = $1 AND id = $2`, w.tenant["A"], contactA)
			if err == nil && tag.RowsAffected() != 0 {
				t.Error("a note-only agent updated a contact")
			}
			var n int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM contact_notes`).Scan(&n))
			if n == 0 {
				t.Error("a note-only agent cannot read the notes")
			}
		})
		// the whole note cycle works with that key alone (no contact.read, no contact.classify)
		code, body := api.do("POST", notes("A", contactA), `{"body":"só a chave de anotações"}`, noteOnly, hub)
		if code != 201 {
			t.Fatalf("a note-only agent adds a note: %d %s", code, body)
		}
		own := w.ownerOne(`SELECT id::text FROM contact_notes WHERE tenant_id = $1 AND created_by_user_id = $2`, w.tenant["A"], noteOnly)
		if code, _ := api.do("GET", notes("A", contactA), "", noteOnly, hub); code != 200 {
			t.Errorf("a note-only agent lists the notes: %d", code)
		}
		if code, _ := api.do("PUT", notes("A", contactA)+"/"+own, `{"body":"editada"}`, noteOnly, hub); code != 200 {
			t.Errorf("a note-only agent edits their note: %d", code)
		}
		if code, _ := api.do("DELETE", notes("A", contactA)+"/"+own, "", noteOnly, hub); code != 204 {
			t.Errorf("a note-only agent removes their note: %d", code)
		}
	})

	t.Run("the database refuses what the handler never asks: a note in another author's name, in another instance, or on a contact out of scope", func(t *testing.T) {
		w.attend(writer, "A", func(ctx context.Context, q platformdb.Querier) {
			if err := w.try(ctx, q, `INSERT INTO contact_notes (tenant_id, contact_id, body, created_by_user_id) VALUES ($1,$2,'x',$3)`, w.tenant["A"], contactA, member); err == nil {
				t.Error("a note was written in another person's name")
			}
			if err := w.try(ctx, q, `INSERT INTO contact_notes (tenant_id, contact_id, body, created_by_user_id) VALUES ($1,$2,'x',$3)`, w.tenant["B"], contactB, writer); err == nil {
				t.Error("a note was written in another instance")
			}
			tag, err := q.Exec(ctx, `UPDATE contact_notes SET body = 'x' WHERE id = $1`, noteB)
			if err == nil && tag.RowsAffected() != 0 {
				t.Error("another instance's note was updated")
			}
			tag, err = q.Exec(ctx, `UPDATE contact_notes SET body = 'x' WHERE id = $1`, memberNote)
			if err == nil && tag.RowsAffected() != 0 {
				t.Error("the company's own note was updated")
			}
			tag, err = q.Exec(ctx, `DELETE FROM contact_notes WHERE id = $1`, memberNote)
			if err == nil && tag.RowsAffected() != 0 {
				t.Error("the company's own note was deleted")
			}
		})
		// a contact the contract's scope does not reach has no readable notes
		quiet := uuid.New()
		w.exec(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1,$2,'Fora do escopo','+5511900000001')`, quiet, w.tenant["A"])
		q1 := w.queue1["A"]
		w.conversation(w.tenant["A"], quiet, &q1)
		w.exec(`INSERT INTO contact_notes (tenant_id, contact_id, body, created_by_user_id) VALUES ($1,$2,'nota fora do escopo',$3)`, w.tenant["A"], quiet, member)
		w.queueScope("A", w.queue2["A"])
		w.attend(writer, "A", func(ctx context.Context, q platformdb.Querier) {
			var n int
			w.must(q.QueryRow(ctx, `SELECT count(*) FROM contact_notes WHERE contact_id = $1`, quiet).Scan(&n))
			if n != 0 {
				t.Errorf("the table shows %d notes of a contact outside the contract's scope", n)
			}
			if err := w.try(ctx, q, `INSERT INTO contact_notes (tenant_id, contact_id, body, created_by_user_id) VALUES ($1,$2,'x',$3)`, w.tenant["A"], quiet, writer); err == nil {
				t.Error("a note was inserted on a contact outside the contract's scope")
			}
		})
		if code, _ := api.do("GET", notes("A", quiet), "", writer, hub); code != 404 {
			t.Errorf("notes of a contact outside the contract's queue scope: %d, want 404", code)
		}
		if code, _ := api.do("POST", notes("A", quiet), `{"body":"x"}`, writer, hub); code != 404 {
			t.Errorf("adding a note to a contact outside the scope: %d, want 404", code)
		}
		if code, body := api.do("GET", notes("A", contactA), "", writer, hub); code != 200 || strings.Contains(body, "fora do escopo") {
			t.Errorf("the contact inside the scope: %d %s", code, body)
		}
	})

	t.Run("the other instance's contact through this instance's URL, and a member's request, behave as before", func(t *testing.T) {
		if code, _ := api.do("GET", notes("A", contactB), "", writer, hub); code != 404 {
			t.Errorf("another instance's contact under this URL: %d, want 404", code)
		}
		if code, _ := api.do("GET", notes("B", contactB), "", other, hub); code != 404 {
			t.Errorf("an instance without a grant: %d, want 404", code)
		}
		// no delegated context: the legacy member's permission (contact.classify via the role) decides, exactly as before
		if code, _ := api.do("GET", notes("A", contactA), "", member, ""); code == 404 {
			t.Errorf("a member's request must reach the notes handler, got 404")
		}
	})
}
