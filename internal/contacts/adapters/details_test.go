package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
)

func TestEditingAContactNameAndEmail(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	h := NewContactsAPIHandler(app).WithAudit(auditadapters.NewPostgresAuditEventRepository(app))
	a, b := seedTenant(t, seed, "det-a"), seedTenant(t, seed, "det-b")
	agent := seedMemberRole(t, seed, a, "tenant_agent", "active")
	adminB := seedMemberRole(t, seed, b, "tenant_admin", "active")
	c := seedContact(t, seed, a, "Fulano", "+5592966660001", time.Now())
	cB := seedContact(t, seed, b, "De B", "+5592966660002", time.Now())
	put := func(tenant, user, contact uuid.UUID, body string) int {
		return call(t, app, tenant, user, http.MethodPut, "/", body, map[string]string{"contact_id": contact.String()}, h.UpdateDetails).Code
	}
	row := func() (n, e, p string) {
		_ = seed.QueryRow(context.Background(), `SELECT display_name,email,phone_e164 FROM contacts WHERE id=$1`, c).Scan(&n, &e, &p)
		return
	}
	wa := func() (w string, alias *string) {
		_ = seed.QueryRow(context.Background(), `SELECT whatsapp_name, alias FROM contacts WHERE id=$1`, c).Scan(&w, &alias)
		return
	}
	if _, err := seed.Exec(context.Background(), `UPDATE contacts SET whatsapp_name='Fulano (WhatsApp)' WHERE id=$1`, c); err != nil {
		t.Fatal(err)
	}
	if code := put(a, agent, c, `{"alias":"  Fulano da Silva ","email":"fulano@exemplo.com"}`); code != 200 {
		t.Fatalf("edit = %d", code)
	}
	if n, e, p := row(); n != "Fulano da Silva" || e != "fulano@exemplo.com" || p != "+5592966660001" {
		t.Fatalf("saved %q %q %q (the phone is the identity and never changes)", n, e, p)
	}
	// the alias is the principal name; the WhatsApp name is kept untouched beside it
	if w, al := wa(); w != "Fulano (WhatsApp)" || al == nil || *al != "Fulano da Silva" {
		t.Fatalf("whatsapp=%q alias=%v", w, al)
	}
	// only the e-mail: the name stays; an empty e-mail clears it
	if code := put(a, agent, c, `{"email":""}`); code != 200 {
		t.Fatal(code)
	}
	if n, e, _ := row(); n != "Fulano da Silva" || e != "" {
		t.Fatalf("partial edit: %q %q", n, e)
	}
	// clearing the alias falls back to the WhatsApp name on its own
	if code := put(a, agent, c, `{"alias":"   "}`); code != 200 {
		t.Fatal(code)
	}
	if n, _, _ := row(); n != "Fulano (WhatsApp)" {
		t.Fatalf("after clearing the alias the name must be the WhatsApp one, got %q", n)
	}
	if _, al := wa(); al != nil {
		t.Fatalf("a blank alias must be NULL, got %q", *al)
	}
	if code := put(a, agent, c, `{"alias":"Fulano da Silva"}`); code != 200 {
		t.Fatal(code)
	}
	for name, body := range map[string]string{
		"too long alias": `{"alias":"` + repeat("a", 201) + `"}`, "name field": `{"display_name":"x"}`, "bad email": `{"email":"nao-e-email"}`, "email with name": `{"email":"Fulano <f@x.com>"}`,
		"phone": `{"phone_e164":"+5511999999999"}`, "tenant": `{"alias":"x","tenant_id":"` + b.String() + `"}`, "empty": `{}`,
	} {
		if code := put(a, agent, c, body); code < 400 {
			t.Errorf("%s = %d", name, code)
		}
	}
	if n, _, p := row(); n != "Fulano da Silva" || p != "+5592966660001" {
		t.Fatalf("a refused edit changed data: %q", n)
	}
	if code := put(b, adminB, c, `{"alias":"sequestrado"}`); code != http.StatusNotFound {
		t.Errorf("another tenant's contact = %d, want 404", code)
	}
	if n, _, _ := row(); n == "sequestrado" {
		t.Fatal("cross-tenant edit")
	}
	_ = cB
	var n int
	_ = seed.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='contact.updated' AND resource_id=$2`, a, c.String()).Scan(&n)
	if n != 4 {
		t.Fatalf("audits = %d, want 4", n)
	}
}

func TestContactNotesAreWrittenByPeopleAndOnlyTheirAuthorChangesThem(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	h := NewContactsAPIHandler(app).WithAudit(auditadapters.NewPostgresAuditEventRepository(app))
	a, b := seedTenant(t, seed, "note-a"), seedTenant(t, seed, "note-b")
	ana, beto := seedMemberRole(t, seed, a, "tenant_agent", "active"), seedMemberRole(t, seed, a, "tenant_supervisor", "active")
	adminB := seedMemberRole(t, seed, b, "tenant_admin", "active")
	viewer, role := uuid.New(), uuid.New()
	for _, q := range []struct {
		s string
		a []any
	}{
		{`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, []any{viewer, viewer.String(), viewer.String() + "@invalid"}},
		{`INSERT INTO roles(id,tenant_id,key,name) VALUES($1,$2,'viewer_notes','V')`, []any{role, a}},
		{`INSERT INTO role_permissions(role_id,permission_key) VALUES($1,'tenant.read')`, []any{role}},
		{`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, []any{a, viewer, role}},
	} {
		if _, err := seed.Exec(context.Background(), q.s, q.a...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { _, _ = seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, viewer) })
	c := seedContact(t, seed, a, "Fulano", "+5592966660003", time.Now())
	p := map[string]string{"contact_id": c.String()}
	type note struct {
		ID   uuid.UUID `json:"id"`
		Body string    `json:"body"`
		Mine bool      `json:"mine"`
	}
	add := func(user uuid.UUID, body string) (int, note) {
		rec := call(t, app, a, user, http.MethodPost, "/", `{"body":`+quote(body)+`}`, p, h.AddNote)
		var n note
		_ = json.Unmarshal(rec.Body.Bytes(), &n)
		return rec.Code, n
	}
	code, n1 := add(ana, "  Cliente prefere contato por WhatsApp pela manhã.  ")
	if code != http.StatusCreated || n1.Body != "Cliente prefere contato por WhatsApp pela manhã." || !n1.Mine {
		t.Fatalf("add = %d %+v", code, n1)
	}
	_, n2 := add(beto, "Contrato renovado em outubro.")
	list := func(user uuid.UUID) []note {
		rec := call(t, app, a, user, http.MethodGet, "/", "", p, h.ListNotes)
		var out struct{ Items []note }
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("list = %d", rec.Code)
		}
		return out.Items
	}
	items := list(ana)
	if len(items) != 2 || items[0].ID != n2.ID || items[0].Mine || !items[1].Mine {
		t.Fatalf("newest first, 'mine' relative to the caller: %+v", items)
	}
	np := map[string]string{"contact_id": c.String(), "note_id": n1.ID.String()}
	// only the author edits or deletes
	if rec := call(t, app, a, beto, http.MethodPut, "/", `{"body":"adulterada"}`, np, h.EditNote); rec.Code != http.StatusNotFound {
		t.Errorf("edit by another person = %d", rec.Code)
	}
	if rec := call(t, app, a, beto, http.MethodDelete, "/", "", np, h.DeleteNote); rec.Code != http.StatusNotFound {
		t.Errorf("delete by another person = %d", rec.Code)
	}
	if rec := call(t, app, a, ana, http.MethodPut, "/", `{"body":"Prefere WhatsApp, à tarde."}`, np, h.EditNote); rec.Code != 200 {
		t.Errorf("author edit = %d", rec.Code)
	}
	var body string
	_ = seed.QueryRow(context.Background(), `SELECT body FROM contact_notes WHERE id=$1`, n1.ID).Scan(&body)
	if body != "Prefere WhatsApp, à tarde." {
		t.Fatalf("body = %q", body)
	}
	for name, bd := range map[string]string{"empty": `{"body":"  "}`, "unknown": `{"body":"x","tenant_id":"` + b.String() + `"}`, "huge": `{"body":"` + string(make([]byte, 0)) + repeat("a", 4001) + `"}`} {
		if rec := call(t, app, a, ana, http.MethodPost, "/", bd, p, h.AddNote); rec.Code < 400 {
			t.Errorf("%s = %d", name, rec.Code)
		}
	}
	// a role without contact.classify neither reads nor writes notes
	if rec := call(t, app, a, viewer, http.MethodGet, "/", "", p, h.ListNotes); rec.Code != http.StatusForbidden {
		t.Errorf("viewer list = %d", rec.Code)
	}
	if rec := call(t, app, a, viewer, http.MethodPost, "/", `{"body":"x"}`, p, h.AddNote); rec.Code != http.StatusForbidden {
		t.Errorf("viewer add = %d", rec.Code)
	}
	// another tenant sees and changes nothing (same 404 as an unknown contact)
	if rec := call(t, app, b, adminB, http.MethodGet, "/", "", p, h.ListNotes); rec.Code != http.StatusNotFound {
		t.Errorf("foreign list = %d", rec.Code)
	}
	if rec := call(t, app, b, adminB, http.MethodPost, "/", `{"body":"x"}`, p, h.AddNote); rec.Code != http.StatusNotFound {
		t.Errorf("foreign add = %d", rec.Code)
	}
	if rec := call(t, app, b, adminB, http.MethodDelete, "/", "", np, h.DeleteNote); rec.Code != http.StatusNotFound {
		t.Errorf("foreign delete = %d", rec.Code)
	}
	if rec := call(t, app, a, ana, http.MethodDelete, "/", "", np, h.DeleteNote); rec.Code != http.StatusNoContent {
		t.Errorf("author delete = %d", rec.Code)
	}
	// the audit trail records that a note changed, never its text
	var leaked int
	_ = seed.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='contact.note_changed' AND metadata::text ILIKE '%WhatsApp%'`, a).Scan(&leaked)
	var ops int
	_ = seed.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='contact.note_changed'`, a).Scan(&ops)
	if leaked != 0 || ops != 4 {
		t.Fatalf("audit: leaked=%d ops=%d (want 0 and 4: add, add, edit, delete)", leaked, ops)
	}
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func repeat(s string, n int) string {
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
