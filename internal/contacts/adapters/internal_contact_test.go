package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/contacts/domain"
)

// ADR-0018 addendum: "Interno" is declared by a person, needs its role, and only switches customer automation off.
func TestInternalContactNeedsItsRoleAndAPerson(t *testing.T) {
	e := newClsEnv(t)
	a := seedTenant(t, e.seed, "intc")
	u := seedMemberRole(t, e.seed, a, "tenant_agent", "active")
	c := seedContact(t, e.seed, a, "Fornecedor X", "+5592933330001", time.Now())

	refused := func(name string, kind domain.ContactKind, src domain.ClassificationSource, role domain.InternalRole, want error) {
		t.Helper()
		_ = e.in(a, u, func(ctx context.Context) {
			if _, err := e.repo.ClassifyWithRole(ctx, a, u, c, kind, src, nil, false, role); !errors.Is(err, want) {
				t.Errorf("%s: got %v, want %v", name, err, want)
			}
		})
	}
	refused("internal without a role", domain.KindInternal, domain.SourceManual, "", domain.ErrInternalNeedsRole)
	refused("internal with an unknown role", domain.KindInternal, domain.SourceManual, "boss", domain.ErrInternalNeedsRole)
	// no automation, import or AI suggestion marks anyone internal: a customer mislabeled that way would be ignored silently
	for _, src := range []domain.ClassificationSource{domain.SourceRule, domain.SourceImport, domain.SourceTrustedCRM, domain.SourceAISuggestionConfirmed, domain.SourceTicketFlow} {
		refused("internal by "+string(src), domain.KindInternal, src, domain.RoleSupplier, domain.ErrInvalidInput)
	}
	refused("a role on a customer-less other", domain.KindOther, domain.SourceManual, domain.RoleTeam, domain.ErrInvalidInput)
	if k, _, _ := e.state(c); k != "unclassified" {
		t.Fatalf("a refused change must leave the contact alone: %s", k)
	}
	// the database says the same, whoever writes
	if _, err := e.seed.Exec(context.Background(), `UPDATE contacts SET kind='internal' WHERE id=$1`, c); err == nil {
		t.Fatal("kind internal without a role must be refused by the database")
	}
	if _, err := e.seed.Exec(context.Background(), `UPDATE contacts SET internal_role='team' WHERE id=$1`, c); err == nil {
		t.Fatal("a role on a contact that is not internal must be refused by the database")
	}
}

func TestInternalContactKeepsTheConversationExternalAndTheRoleIsEditable(t *testing.T) {
	e := newClsEnv(t)
	a := seedTenant(t, e.seed, "intc2")
	u := seedMemberRole(t, e.seed, a, "tenant_agent", "active")
	c := seedContact(t, e.seed, a, "Parceiro Y", "+5592933330002", time.Now())
	conv := seedConversation(t, e.seed, a, c, nil, "open", time.Now())
	convKind := func() (k string) {
		_ = e.seed.QueryRow(context.Background(), `SELECT conversation_kind FROM conversations WHERE id=$1`, conv).Scan(&k)
		return
	}
	role := func() (r *string) {
		_ = e.seed.QueryRow(context.Background(), `SELECT internal_role FROM contacts WHERE id=$1`, c).Scan(&r)
		return
	}
	classify := func(kind domain.ContactKind, r domain.InternalRole) (ch Change) {
		t.Helper()
		if err := e.inErr(a, u, func(ctx context.Context) (err error) {
			ch, err = e.repo.ClassifyWithRole(ctx, a, u, c, kind, domain.SourceManual, nil, false, r)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return ch
	}

	ch := classify(domain.KindInternal, domain.RolePartner)
	if !ch.Changed || ch.Role != domain.RolePartner || ch.PreviousKind != domain.KindUnclassified {
		t.Fatalf("classify internal: %+v", ch)
	}
	if k, src, by := e.state(c); k != "internal" || src != "manual" || by == nil || *by != u {
		t.Fatalf("state = %s %s %v", k, src, by)
	}
	// not staff (no internal_user_id) and not a customer: customer automation stays off, the conversation stays assignable
	if convKind() != "external_other" {
		t.Fatalf("a declared-internal contact must not become a verified staff conversation: %s", convKind())
	}
	// same kind, new role: a change (and the viewers are told), the conversation kind is not touched
	ch = classify(domain.KindInternal, domain.RoleSupplier)
	if !ch.Changed || ch.PreviousRole != domain.RolePartner || ch.Role != domain.RoleSupplier || role() == nil || *role() != "supplier" {
		t.Fatalf("role change: %+v role=%v", ch, role())
	}
	// replay changes nothing
	if ch = classify(domain.KindInternal, domain.RoleSupplier); ch.Changed {
		t.Fatalf("replay changed: %+v", ch)
	}
	// leaving internal drops the role
	ch = classify(domain.KindOther, "")
	if !ch.Changed || role() != nil || convKind() != "external_other" {
		t.Fatalf("back to other: %+v role=%v conv=%s", ch, role(), convKind())
	}
}

func TestPutClassificationInternalOverHTTP(t *testing.T) {
	e := newHTTPEnv(t)
	c := e.contact("+5592933330003")
	path := map[string]string{"contact_id": c.String()}

	rec := e.do(e.agent, e.tenant, http.MethodPut, `{"kind":"internal"}`, path, e.h.PutClassification)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "role") {
		t.Fatalf("internal without a role = %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(e.agent, e.tenant, http.MethodPut, `{"kind":"customer","internal_role":"team","accounts":[{"directory_company_id":"42"}]}`, path, e.h.PutClassification)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a role on a customer = %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(e.agent, e.tenant, http.MethodPut, `{"kind":"internal","internal_role":"supplier"}`, path, e.h.PutClassification)
	var v struct {
		Kind         string  `json:"kind"`
		InternalRole *string `json:"internal_role"`
		Source       *string `json:"classification_source"`
	}
	decodeInto(t, rec, &v)
	if rec.Code != 200 || v.Kind != "internal" || v.InternalRole == nil || *v.InternalRole != "supplier" || v.Source == nil || *v.Source != "manual" {
		t.Fatalf("put internal = %d %s", rec.Code, rec.Body.String())
	}
	if e.n(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='contact.classified' AND resource_id=$2 AND metadata->>'internal_role_to'='supplier' AND metadata->>'kind_to'='internal'`, e.tenant, c.String()) != 1 {
		t.Fatal("marking a contact internal must be audited with the role")
	}
	// only the role changes: still audited, the old role is kept in the entry
	rec = e.do(e.agent, e.tenant, http.MethodPut, `{"kind":"internal","internal_role":"team"}`, path, e.h.PutClassification)
	if rec.Code != 200 || e.n(`SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND resource_id=$2 AND metadata->>'internal_role_from'='supplier' AND metadata->>'internal_role_to'='team'`, e.tenant, c.String()) != 1 {
		t.Fatalf("role change = %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(e.agent, e.tenant, http.MethodGet, "", path, e.h.GetClassification)
	decodeInto(t, rec, &v)
	if v.Kind != "internal" || v.InternalRole == nil || *v.InternalRole != "team" {
		t.Fatalf("get = %s", rec.Body.String())
	}
	// spam from internal is still allowed and drops the role
	rec = e.do(e.agent, e.tenant, http.MethodPut, `{"kind":"spam"}`, path, e.h.PutClassification)
	decodeInto(t, rec, &v)
	if rec.Code != 200 || v.Kind != "spam" || v.InternalRole != nil {
		t.Fatalf("spam = %d %s", rec.Code, rec.Body.String())
	}
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
}
