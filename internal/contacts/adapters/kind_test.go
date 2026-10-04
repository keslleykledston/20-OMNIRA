package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func seedMemberRole(t *testing.T, pool *pgxpool.Pool, tenantID uuid.UUID, roleKey, status string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	userID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO users (id, external_subject, email, status) VALUES ($1,$2,$3,'active')`,
		userID, userID.String(), userID.String()+"@example.com"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var roleID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL LIMIT 1`, roleKey).Scan(&roleID); err != nil {
		t.Fatalf("seed role %s: %v", roleKey, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,$5)`,
		uuid.New(), tenantID, userID, roleID, status); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID) })
	return userID
}

// call drives a handler in a real RLS session with a TenantContext, with an optional body and path values.
func call(t *testing.T, pool *pgxpool.Pool, tenantID, userID uuid.UUID, method, target, body string, path map[string]string, fn http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	err := platformdb.WithTenantSession(context.Background(), pool, userID, false, func(sessionCtx context.Context) error {
		tc, tcErr := tenancydomain.NewTenantContext(tenantID, userID, tenancydomain.AccessSourceDirect)
		if tcErr != nil {
			return tcErr
		}
		req := httptest.NewRequest(method, target, bytes.NewReader([]byte(body))).WithContext(tenancydomain.WithTenantContext(sessionCtx, tc))
		for k, v := range path {
			req.SetPathValue(k, v)
		}
		fn(rec, req)
		return nil
	})
	if err != nil {
		t.Fatalf("tenant session: %v", err)
	}
	return rec
}

func kindOf(t *testing.T, seed *pgxpool.Pool, contact uuid.UUID) string {
	t.Helper()
	var k string
	if err := seed.QueryRow(context.Background(), `SELECT kind FROM contacts WHERE id=$1`, contact).Scan(&k); err != nil {
		t.Fatal(err)
	}
	return k
}

func kindAudits(t *testing.T, seed *pgxpool.Pool, tenant uuid.UUID) int {
	t.Helper()
	var n int
	if err := seed.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='contact.kind_changed'`, tenant).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSetKindIsPermissionedAuditedIdempotentAndTenantScoped(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	h := NewContactsAPIHandler(app).WithAudit(auditadapters.NewPostgresAuditEventRepository(app))
	a, b := seedTenant(t, seed, "kind-a"), seedTenant(t, seed, "kind-b")
	admin := seedMemberRole(t, seed, a, "tenant_admin", "active")
	supervisor := seedMemberRole(t, seed, a, "tenant_supervisor", "active")
	agent := seedMemberRole(t, seed, a, "tenant_agent", "active")
	revoked := seedMemberRole(t, seed, a, "tenant_admin", "revoked")
	adminB := seedMemberRole(t, seed, b, "tenant_admin", "active")
	cA := seedContact(t, seed, a, "Contato A", "+5592900000001", time.Now())
	cB := seedContact(t, seed, b, "Contato B", "+5592900000002", time.Now())
	patch := func(tenant, user, contact uuid.UUID, body string) *httptest.ResponseRecorder {
		return call(t, app, tenant, user, http.MethodPatch, "/", body, map[string]string{"contact_id": contact.String()}, h.SetKind)
	}

	// New contacts start as 'other'.
	if k := kindOf(t, seed, cA); k != "other" {
		t.Fatalf("default kind = %q, want other", k)
	}
	// Refused without touching anything: a revoked membership, and someone who belongs to another tenant only.
	if rec := patch(a, revoked, cA, `{"kind":"spam"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("revoked = %d, want 403", rec.Code)
	}
	if rec := patch(a, adminB, cA, `{"kind":"spam"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider = %d, want 403", rec.Code)
	}
	if k := kindOf(t, seed, cA); k != "other" || kindAudits(t, seed, a) != 0 {
		t.Fatalf("a refused request changed state: kind=%s audits=%d", k, kindAudits(t, seed, a))
	}

	// Two open conversations of the contact: one waiting unassigned in a queue, one already held by a supervisor.
	queue := uuid.New()
	if _, err := seed.Exec(context.Background(), `INSERT INTO queues(id,tenant_id,name,mode,is_default) VALUES($1,$2,'Default','round_robin',true)`, queue, a); err != nil {
		t.Fatal(err)
	}
	waiting, held := uuid.New(), uuid.New()
	for id, owner := range map[uuid.UUID]*uuid.UUID{waiting: nil, held: &supervisor} {
		if _, err := seed.Exec(context.Background(),
			`INSERT INTO conversations(id,tenant_id,contact_id,status,queue_id,assigned_to_user_id,routing_retry_at) VALUES($1,$2,$3,'open',$4,$5,now())`, id, a, cA, queue, owner); err != nil {
			t.Fatal(err)
		}
	}
	queueOf := func(c uuid.UUID) (q *uuid.UUID, retry bool) {
		if err := seed.QueryRow(context.Background(), `SELECT queue_id, routing_retry_at IS NOT NULL FROM conversations WHERE id=$1`, c).Scan(&q, &retry); err != nil {
			t.Fatal(err)
		}
		return
	}

	// A plain agent (conversation.claim, no conversation.manage) flags the scam. Response and row agree.
	rec := patch(a, agent, cA, `{"kind":"spam"}`)
	var got ContactItem
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Kind != "spam" || got.ID != cA {
		t.Fatalf("agent = %d %s", rec.Code, rec.Body.String())
	}
	if kindOf(t, seed, cA) != "spam" || kindAudits(t, seed, a) != 1 {
		t.Fatalf("kind=%s audits=%d, want spam / 1", kindOf(t, seed, cA), kindAudits(t, seed, a))
	}
	// The scam leaves the queue and its pending routing retry; a conversation somebody holds is left alone.
	if q, retry := queueOf(waiting); q != nil || retry {
		t.Fatalf("the unassigned conversation must leave the queue and drop its retry, got queue=%v retry=%v", q, retry)
	}
	if q, retry := queueOf(held); q == nil || !retry {
		t.Fatalf("a conversation already held must be untouched, got queue=%v retry=%v", q, retry)
	}
	var from, to string
	var dequeued int
	if err := seed.QueryRow(context.Background(), `SELECT metadata->>'kind_from', metadata->>'kind_to', (metadata->>'conversations_dequeued')::int FROM audit_events WHERE tenant_id=$1 AND action='contact.kind_changed'`, a).Scan(&from, &to, &dequeued); err != nil || from != "other" || to != "spam" || dequeued != 1 {
		t.Fatalf("audit = %s/%s dequeued=%d (%v), want other/spam/1", from, to, dequeued, err)
	}
	// False positive: the same agent restores it. The conversation is NOT put back in the queue.
	if rec := patch(a, agent, cA, `{"kind":"other"}`); rec.Code != http.StatusOK || kindOf(t, seed, cA) != "other" || kindAudits(t, seed, a) != 2 {
		t.Fatalf("restore = %d kind=%s audits=%d", rec.Code, kindOf(t, seed, cA), kindAudits(t, seed, a))
	}
	if q, _ := queueOf(waiting); q != nil {
		t.Fatalf("restoring must not silently re-queue the conversation, got %v", q)
	}
	// Flag again for the idempotency check below.
	if rec := patch(a, supervisor, cA, `{"kind":"spam"}`); rec.Code != http.StatusOK || kindAudits(t, seed, a) != 3 {
		t.Fatalf("supervisor spam = %d audits=%d", rec.Code, kindAudits(t, seed, a))
	}

	// Same kind again: 200, no new audit.
	if rec := patch(a, admin, cA, `{"kind":"spam"}`); rec.Code != http.StatusOK || kindAudits(t, seed, a) != 3 {
		t.Fatalf("idempotent = %d audits=%d", rec.Code, kindAudits(t, seed, a))
	}
	// Reclassify back: second audit.
	if rec := patch(a, admin, cA, `{"kind":"customer"}`); rec.Code != http.StatusOK || kindOf(t, seed, cA) != "customer" || kindAudits(t, seed, a) != 4 {
		t.Fatalf("customer = %d kind=%s audits=%d", rec.Code, kindOf(t, seed, cA), kindAudits(t, seed, a))
	}

	// Invalid input is refused without touching anything.
	for name, body := range map[string]string{
		"unknown kind": `{"kind":"vip"}`, "missing": `{}`, "null": `{"kind":null}`, "not json": `nope`,
		"unknown field": `{"kind":"spam","tenant_id":"` + b.String() + `"}`, "wrong type": `{"kind":1}`,
	} {
		if rec := patch(a, admin, cA, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	if rec := call(t, app, a, admin, http.MethodPatch, "/", `{"kind":"spam"}`, map[string]string{"contact_id": "not-a-uuid"}, h.SetKind); rec.Code != http.StatusBadRequest {
		t.Errorf("bad uuid: %d, want 400", rec.Code)
	}

	// Tenant isolation: A's admin cannot touch B's contact (same 404 as an unknown id), and B is untouched.
	rb := patch(a, admin, cB, `{"kind":"spam"}`)
	ru := patch(a, admin, uuid.New(), `{"kind":"spam"}`)
	if rb.Code != http.StatusNotFound || ru.Code != http.StatusNotFound || rb.Body.String() != ru.Body.String() {
		t.Fatalf("enumeration oracle: %d %q vs %d %q", rb.Code, rb.Body.String(), ru.Code, ru.Body.String())
	}
	if kindOf(t, seed, cB) != "other" {
		t.Fatalf("tenant B contact changed by tenant A's admin: %s", kindOf(t, seed, cB))
	}
	if rec := patch(b, adminB, cB, `{"kind":"customer"}`); rec.Code != http.StatusOK || kindOf(t, seed, cA) != "customer" || kindOf(t, seed, cB) != "customer" {
		t.Fatalf("B's own admin: %d", rec.Code)
	}
}

func TestListContactsSearchAndFiltersAreServerSideAndTenantScoped(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	h := NewContactsAPIHandler(app)
	a, b := seedTenant(t, seed, "filter-a"), seedTenant(t, seed, "filter-b")
	user := seedMemberRole(t, seed, a, "tenant_agent", "active")
	now := time.Now().UTC().Truncate(time.Second)
	mk := func(tn uuid.UUID, name, phone, email, status, kind string, ageMin int) uuid.UUID {
		id := seedContact(t, seed, tn, name, phone, now.Add(-time.Duration(ageMin)*time.Minute))
		if _, err := seed.Exec(context.Background(), `UPDATE contacts SET email=$2, status=$3, kind=$4 WHERE id=$1`, id, email, status, kind); err != nil {
			t.Fatal(err)
		}
		return id
	}
	maria := mk(a, "Maria Souza", "+5592911110001", "maria@exemplo.com", "active", "customer", 1)
	literal := mk(a, "100% Real", "+5592911110002", "", "active", "other", 2)
	spam := mk(a, "Promo Chata", "+5592911110003", "promo@spam.test", "active", "spam", 3)
	blocked := mk(a, "Bloqueado Silva", "+5592911110004", "", "blocked", "other", 4)
	foreign := mk(b, "Maria Souza", "+5592911110005", "", "active", "customer", 0)

	list := func(query string) (int, []string) {
		rec := call(t, app, a, user, http.MethodGet, "/?"+query, "", nil, h.ListContacts)
		if rec.Code != http.StatusOK {
			return rec.Code, nil
		}
		var ids []string
		for _, it := range decodePage(t, rec) {
			ids = append(ids, it["id"].(string))
		}
		return rec.Code, ids
	}
	eq := func(got []string, want ...uuid.UUID) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i].String() {
				return false
			}
		}
		return true
	}

	if _, ids := list(""); !eq(ids, maria, literal, spam, blocked) {
		t.Fatalf("all of tenant A, newest first, none from B: %v", ids)
	}
	if _, ids := list("q=maria"); !eq(ids, maria) {
		t.Fatalf("name search is case-insensitive and tenant-scoped (B's Maria must not appear): %v", ids)
	}
	if _, ids := list("q=" + url.QueryEscape("PROMO@SPAM")); !eq(ids, spam) {
		t.Fatalf("email search: %v", ids)
	}
	if _, ids := list("q=" + url.QueryEscape("(92) 91111-0004")); !eq(ids, blocked) {
		t.Fatalf("phone search by digits ignoring formatting: %v", ids)
	}
	if _, ids := list("q=" + url.QueryEscape("100%")); !eq(ids, literal) {
		t.Fatalf("a literal %% must not be a wildcard: %v", ids)
	}
	if _, ids := list("q=_"); len(ids) != 0 {
		t.Fatalf("a lone _ must match nothing: %v", ids)
	}
	if _, ids := list("status=blocked"); !eq(ids, blocked) {
		t.Fatalf("status filter: %v", ids)
	}
	for kind, want := range map[string]uuid.UUID{"customer": maria, "spam": spam} {
		if _, ids := list("kind=" + kind); !eq(ids, want) {
			t.Fatalf("kind=%s: %v", kind, ids)
		}
	}
	if _, ids := list("kind=other&status=active"); !eq(ids, literal) {
		t.Fatalf("filters combine: %v", ids)
	}
	for _, bad := range []string{"kind=vip", "status=gone", "q=" + fmt.Sprintf("%0101d", 0)} {
		if code, _ := list(bad); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
	// Paging keeps the filter and never repeats or skips.
	var walked []string
	cursor := ""
	for i := 0; i < 6; i++ {
		q := "limit=1&kind=other"
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		rec := call(t, app, a, user, http.MethodGet, "/?"+q, "", nil, h.ListContacts)
		var page struct {
			Items      []map[string]any `json:"items"`
			HasMore    bool             `json:"has_more"`
			NextCursor string           `json:"next_cursor"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
			t.Fatalf("page %d: %d %s", i, rec.Code, rec.Body.String())
		}
		for _, it := range page.Items {
			walked = append(walked, it["id"].(string))
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if !eq(walked, literal, blocked) {
		t.Fatalf("filtered paging: %v", walked)
	}
	_ = foreign
}
