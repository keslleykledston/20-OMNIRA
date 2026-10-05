package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ADR-0018 Wave 9: the unified people directory, on real Postgres under FORCE RLS.

type peopleRow map[string]any

func peopleCall(t *testing.T, env *clsEnv, tenant, user uuid.UUID, query string) (int, []peopleRow, http.Header) {
	t.Helper()
	h := NewContactsAPIHandler(env.app)
	rec := call(t, env.app, tenant, user, http.MethodGet, "/?"+query, "", nil, h.ListPeople)
	var page struct {
		Items []peopleRow `json:"items"`
	}
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
	}
	return rec.Code, page.Items, rec.Header()
}

func names(rows []peopleRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r["display_name"].(string))
	}
	return out
}

func sameSet(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	m := map[string]int{}
	for _, g := range got {
		m[g]++
	}
	for _, w := range want {
		m[w]--
	}
	for _, v := range m {
		if v != 0 {
			return false
		}
	}
	return true
}

func TestPeopleDirectoryKeepsContactsAndStaffApartAndStaffBehindMembershipRead(t *testing.T) {
	e := newClsEnv(t)
	a, b := seedTenant(t, e.seed, "people-a"), seedTenant(t, e.seed, "people-b")
	admin := seedMemberRole(t, e.seed, a, "tenant_admin", "active")
	agent := seedMemberRole(t, e.seed, a, "tenant_agent", "active")
	adminB := seedMemberRole(t, e.seed, b, "tenant_admin", "active")
	for u, n := range map[uuid.UUID]string{admin: "Ana Admin", agent: "Beto Atendente"} {
		if _, err := e.seed.Exec(context.Background(), `UPDATE users SET display_name=$2 WHERE id=$1`, u, n); err != nil {
			t.Fatal(err)
		}
	}
	inactive := seedMemberRole(t, e.seed, a, "tenant_agent", "inactive")
	revoked := seedMemberRole(t, e.seed, a, "tenant_agent", "revoked")
	_, _ = e.seed.Exec(context.Background(), `UPDATE users SET display_name='Carla Inativa' WHERE id=$1`, inactive)
	_, _ = e.seed.Exec(context.Background(), `UPDATE users SET display_name='Davi Removido' WHERE id=$1`, revoked)

	now := time.Now()
	un := seedContact(t, e.seed, a, "Cliente Novo", "+5592955550001", now.Add(-1*time.Hour))
	cu := seedContact(t, e.seed, a, "Cliente Ouro", "+5592955550002", now.Add(-2*time.Hour))
	ot := seedContact(t, e.seed, a, "Fornecedor", "+5592955550003", now.Add(-3*time.Hour))
	sp := seedContact(t, e.seed, a, "Promo Chata", "+5592955550004", now.Add(-4*time.Hour))
	bl := seedContact(t, e.seed, a, "Bloqueado", "+5592955550005", now.Add(-5*time.Hour))
	seedContact(t, e.seed, b, "De Outro Tenant", "+5592955550006", now)
	_ = un
	acme, xpto := e.account(a, admin, "ACME"), e.account(a, admin, "XPTO")
	e.in(a, admin, func(ctx context.Context) {
		if _, err := e.repo.Classify(ctx, a, admin, cu, "customer", "manual", nil, false); err == nil {
			t.Fatal("setup guard: a customer needs a company")
		}
	})
	e.exec2(`UPDATE contacts SET kind='other' WHERE id=$1`, ot)
	e.exec2(`UPDATE contacts SET kind='spam' WHERE id=$1`, sp)
	e.exec2(`UPDATE contacts SET status='blocked' WHERE id=$1`, bl)
	for _, acc := range []struct {
		id      uuid.UUID
		primary bool
	}{{acme, true}, {xpto, false}} {
		e.exec2(`INSERT INTO contact_account_links(tenant_id,contact_id,account_id,source,is_primary) VALUES($1,$2,$3,'manual',$4)`, a, cu, acc.id, acc.primary)
	}
	e.exec2(`UPDATE contacts SET kind='customer' WHERE id=$1`, cu)

	// Todos (admin): external contacts (no spam) AND active/inactive staff (no revoked), discriminated
	code, rows, _ := peopleCall(t, e, a, admin, "")
	if code != 200 || !sameSet(names(rows), "Cliente Novo", "Cliente Ouro", "Fornecedor", "Bloqueado", "Ana Admin", "Beto Atendente", "Carla Inativa") {
		t.Fatalf("all = %d %v", code, names(rows))
	}
	for _, r := range rows {
		switch r["subject_type"] {
		case "contact":
			if _, has := r["kind"]; !has {
				t.Errorf("a contact row carries its classification: %v", r)
			}
		case "internal_user":
			if _, has := r["kind"]; has {
				t.Errorf("staff are never classified: %v", r)
			}
			if r["manage_path"] != "/settings/team" || r["role_key"] == "" {
				t.Errorf("staff row: %v", r)
			}
		default:
			t.Errorf("unknown subject_type: %v", r)
		}
	}
	// the customer row carries its companies summary
	for _, r := range rows {
		if r["display_name"] == "Cliente Ouro" && (r["account_count"] != float64(2) || r["primary_account_name"] != "ACME" || r["kind"] != "customer") {
			t.Errorf("company summary: %v", r)
		}
	}
	// views
	for view, want := range map[string][]string{
		"customers":    {"Cliente Ouro"},
		"others":       {"Fornecedor"},
		"unclassified": {"Cliente Novo", "Bloqueado"},
		"spam":         {"Promo Chata"},
		"internal":     {"Ana Admin", "Beto Atendente", "Carla Inativa"},
	} {
		if code, rows, _ := peopleCall(t, e, a, admin, "view="+view); code != 200 || !sameSet(names(rows), want...) {
			t.Errorf("view=%s = %d %v, want %v", view, code, names(rows), want)
		}
	}
	// search and status
	if _, rows, _ := peopleCall(t, e, a, admin, "q="+url.QueryEscape("ana")); !sameSet(names(rows), "Ana Admin") {
		t.Errorf("q=ana: %v", names(rows))
	}
	if _, rows, _ := peopleCall(t, e, a, admin, "q="+url.QueryEscape("(92) 95555-0002")); !sameSet(names(rows), "Cliente Ouro") {
		t.Errorf("phone search: %v", names(rows))
	}
	if _, rows, _ := peopleCall(t, e, a, admin, "status=inactive"); !sameSet(names(rows), "Carla Inativa") {
		t.Errorf("status=inactive is staff-only: %v", names(rows))
	}
	if _, rows, _ := peopleCall(t, e, a, admin, "status=blocked"); !sameSet(names(rows), "Bloqueado") {
		t.Errorf("status=blocked is contact-only: %v", names(rows))
	}
	if code, _, _ := peopleCall(t, e, a, admin, "view=mixed"); code != http.StatusBadRequest {
		t.Errorf("unknown view = %d", code)
	}
	if code, _, _ := peopleCall(t, e, a, admin, "status=banana"); code != http.StatusBadRequest {
		t.Errorf("unknown status = %d", code)
	}

	// an agent WITHOUT membership.read: the contacts part only, and the Internos view is refused
	if code, rows, _ := peopleCall(t, e, a, agent, ""); code != 200 || !sameSet(names(rows), "Cliente Novo", "Cliente Ouro", "Fornecedor", "Bloqueado") {
		t.Errorf("agent all = %d %v: a role without membership.read must not learn who the staff is", code, names(rows))
	}
	if code, _, _ := peopleCall(t, e, a, agent, "view=internal"); code != http.StatusForbidden {
		t.Errorf("agent internal = %d, want 403", code)
	}
	if _, rows, _ := peopleCall(t, e, a, agent, "q=ana"); len(rows) != 0 {
		t.Errorf("a staff name must not leak through search: %v", names(rows))
	}
	// another tenant sees only its own people
	if _, rows, _ := peopleCall(t, e, b, adminB, ""); !sameSet(names(rows), "De Outro Tenant", "") && len(rows) != 2 {
		t.Errorf("tenant B: %v", names(rows))
	}
	for _, n := range func() []string { _, r, _ := peopleCall(t, e, b, adminB, ""); return names(r) }() {
		if n == "Cliente Ouro" || n == "Ana Admin" {
			t.Errorf("tenant B saw tenant A's person %q", n)
		}
	}
}

func TestPeopleDirectoryPagesAcrossBothSubjectTypesWithoutRepeatsOrGaps(t *testing.T) {
	e := newClsEnv(t)
	a := seedTenant(t, e.seed, "people-pages")
	admin := seedMemberRole(t, e.seed, a, "tenant_admin", "active")
	now := time.Now().UTC().Truncate(time.Second)
	want := map[string]bool{}
	for i := 0; i < 5; i++ {
		n := "Contato " + string(rune('A'+i))
		seedContact(t, e.seed, a, n, "+559295556000"+string(rune('0'+i)), now.Add(-time.Duration(i)*time.Minute))
		want[n] = true
	}
	for i := 0; i < 3; i++ {
		u := seedMemberRole(t, e.seed, a, "tenant_agent", "active")
		n := "Staff " + string(rune('A'+i))
		_, _ = e.seed.Exec(context.Background(), `UPDATE users SET display_name=$2 WHERE id=$1`, u, n)
		_, _ = e.seed.Exec(context.Background(), `UPDATE memberships SET updated_at=$3 WHERE tenant_id=$1 AND user_id=$2`, a, u, now.Add(-time.Duration(i)*90*time.Second))
		want[n] = true
	}
	want["display"] = false
	delete(want, "display")
	seen := map[string]int{}
	cursor := ""
	pages := 0
	for {
		q := "limit=3"
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		h := NewContactsAPIHandler(e.app)
		rec := call(t, e.app, a, admin, http.MethodGet, "/?"+q, "", nil, h.ListPeople)
		var page struct {
			Items      []peopleRow `json:"items"`
			HasMore    bool        `json:"has_more"`
			NextCursor string      `json:"next_cursor"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
			t.Fatalf("page = %d %s", rec.Code, rec.Body.String())
		}
		for _, n := range names(page.Items) {
			seen[n]++
		}
		pages++
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
		if pages > 10 {
			t.Fatal("pagination does not terminate")
		}
	}
	// the tenant also holds the admin staff row
	delete(seen, "")
	for n := range want {
		if seen[n] != 1 {
			t.Errorf("%q seen %d times", n, seen[n])
		}
	}
	if pages < 3 {
		t.Fatalf("expected several pages, got %d", pages)
	}
}

// Search finds a contact by the team's alias AND by the name it declared on WhatsApp; the principal name is the alias.
func TestPeopleSearchFindsBothNamesAndTheAliasIsThePrincipalName(t *testing.T) {
	e := newClsEnv(t)
	a := seedTenant(t, e.seed, "alias-search")
	admin := seedMemberRole(t, e.seed, a, "tenant_admin", "active")
	c := seedContact(t, e.seed, a, "x", "+5592977770001", time.Now())
	e.exec2(`UPDATE contacts SET whatsapp_name='Zé Boladão', alias='José Carlos (ACME)' WHERE id=$1`, c)
	for _, q := range []string{"Bolad", "ACME", "josé"} {
		code, rows, _ := peopleCall(t, e, a, admin, "q="+url.QueryEscape(q))
		if code != 200 || len(rows) != 1 {
			t.Fatalf("q=%q = %d %v", q, code, names(rows))
		}
		if rows[0]["display_name"] != "José Carlos (ACME)" || rows[0]["whatsapp_name"] != "Zé Boladão" || rows[0]["alias"] != "José Carlos (ACME)" {
			t.Fatalf("principal=alias, whatsapp beside it: %v", rows[0])
		}
	}
}
