package adapters_test

import (
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
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	"github.com/omnira/omnira/internal/testhelpers"
)

type listPage struct {
	Items []struct {
		ID                   string  `json:"id"`
		ContactName          string  `json:"contact_name"`
		LastMessageAt        *string `json:"last_message_at"`
		LastMessageDirection string  `json:"last_message_direction"`
		LastMessageType      string  `json:"last_message_type"`
		LastMessagePreview   string  `json:"last_message_preview"`
		WaitingSince         *string `json:"waiting_since"`
	} `json:"items"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

// The Inbox used to list by creation date capped at 50: an old customer who wrote today
// vanished from the list. The list is now by last activity, pageable without a cap.
func TestListConversationsByActivityWithFiltersAndPaging(t *testing.T) {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := seed.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	tenantA, tenantB, userA, userB, other := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{userA, userB, other} {
		exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
	}
	for _, tn := range []uuid.UUID{tenantA, tenantB} {
		exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String())
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id IN ($1,$2)`, tenantA, tenantB)
		_, _ = seed.Exec(bg, `DELETE FROM users WHERE id IN ($1,$2,$3)`, userA, userB, other)
	})
	var role uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantA, userA, role)
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantB, userB, role)

	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	at := func(h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }
	phoneSeq := 0
	conv := func(tn uuid.UUID, name string, createdH int, owner *uuid.UUID) uuid.UUID {
		phoneSeq++
		contact, c := uuid.New(), uuid.New()
		exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,$3,$4)`, contact, tn, name, fmt.Sprintf("+55929%08d", phoneSeq*1111))
		exec(`INSERT INTO conversations(id,tenant_id,contact_id,status,assigned_to_user_id,created_at,updated_at) VALUES($1,$2,$3,'open',$4,$5,$5)`, c, tn, contact, owner, at(createdH))
		return c
	}
	msg := func(tn, c uuid.UUID, dir, status, body string, h int) {
		exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status,created_at) VALUES($1,$2,$3,$4,'text',$5,$6,$7)`, uuid.New(), tn, c, dir, body, status, at(h))
	}

	// old: created first, but wrote most recently. fresh: created last, quiet since.
	// empty: no message at all, so its activity is its creation.
	old := conv(tenantA, "Antigo Ativo", 0, nil)
	mine := conv(tenantA, "Atendida por mim", 1, &userA)
	fresh := conv(tenantA, "Recente Quieto", 5, nil)
	empty := conv(tenantA, "Sem Mensagem", 6, nil)
	lit := conv(tenantA, "100% literal", 2, nil)
	foreign := conv(tenantB, "Antigo Ativo", 0, nil)

	msg(tenantA, old, "inbound", "received", "oi", 40)
	msg(tenantA, old, "outbound", "sent", "olá", 41)
	msg(tenantA, old, "inbound", "received", "primeira espera", 42)
	msg(tenantA, old, "outbound", "failed", "não saiu", 43) // a failed reply does not answer the customer
	msg(tenantA, old, "inbound", "received", "segunda espera", 44)
	msg(tenantA, mine, "inbound", "received", "pergunta", 30)
	msg(tenantA, mine, "outbound", "sent", "resposta minha", 31)
	msg(tenantA, fresh, "inbound", "received", "um", 10)
	msg(tenantA, lit, "inbound", "received", "x", 11)
	msg(tenantB, foreign, "inbound", "received", "outro tenant", 45)

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(app), tenancyadapters.NewPostgresTenantRepository(app))
	h := inboxadapters.NewInboxAPIHandler(app)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations", tenancyadapters.AuthorizationMiddleware(app, authz)(http.HandlerFunc(h.ListConversations)))
	get := func(user, tenant uuid.UUID, query string) (int, listPage) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+tenant.String()+"/inbox/conversations?"+query, nil)
		req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var page listPage
		if rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
				t.Fatalf("decode: %v: %s", err, rec.Body.String())
			}
		}
		return rec.Code, page
	}
	ids := func(p listPage) []string {
		out := make([]string, len(p.Items))
		for i, it := range p.Items {
			out[i] = it.ID
		}
		return out
	}
	equal := func(got []string, want ...uuid.UUID) bool {
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

	// 1. Order: by last message, NOT by creation; a conversation without messages sorts by creation.
	code, page := get(userA, tenantA, "")
	wantOrder := []uuid.UUID{old, mine, lit, fresh, empty}
	// activity: old=h44, mine=h31, lit=h11, fresh=h10, empty=creation h6
	if code != 200 || !equal(ids(page), wantOrder...) {
		t.Fatalf("order by last activity: %d %v want %v", code, ids(page), wantOrder)
	}
	first := page.Items[0]
	if first.LastMessagePreview != "segunda espera" || first.LastMessageDirection != "inbound" || first.LastMessageType != "text" || first.LastMessageAt == nil {
		t.Fatalf("preview of the last message: %+v", first)
	}
	// 2. Waiting since = EARLIEST inbound after the last non-failed outbound (h41): h42, not h44.
	if first.WaitingSince == nil || *first.WaitingSince != at(42).Format(time.RFC3339Nano) {
		t.Fatalf("waiting_since = %v, want %s (a failed outbound must not reset the wait)", first.WaitingSince, at(42).Format(time.RFC3339Nano))
	}
	if page.Items[1].WaitingSince != nil || page.Items[1].LastMessageDirection != "outbound" {
		t.Fatalf("answered conversation must not be waiting: %+v", page.Items[1])
	}
	if page.Items[4].LastMessageAt != nil || page.Items[4].LastMessagePreview != "" {
		t.Fatalf("conversation without messages must carry no last message: %+v", page.Items[4])
	}
	// Tenant isolation: tenant B's conversation (same name) is never in A's list.
	for _, id := range ids(page) {
		if id == foreign.String() {
			t.Fatal("tenant B conversation leaked into tenant A list")
		}
	}

	// 3. Paging without a cap: walk with limit=2 and get every conversation once, in order.
	var walked []string
	cursor := ""
	for i := 0; i < 10; i++ {
		q := url.Values{"limit": {"2"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		c, p := get(userA, tenantA, q.Encode())
		if c != 200 {
			t.Fatalf("page %d: %d", i, c)
		}
		walked = append(walked, ids(p)...)
		if !p.HasMore {
			break
		}
		cursor = p.NextCursor
		if cursor == "" {
			t.Fatal("has_more without next_cursor")
		}
	}
	if !equal(walked, wantOrder...) {
		t.Fatalf("paged walk %v want %v (no repeats, no gaps)", walked, wantOrder)
	}

	// 4. Search: case-insensitive name, digits of the phone, LIKE wildcards are literal.
	if _, p := get(userA, tenantA, "q=ANTIGO"); !equal(ids(p), old) {
		t.Fatalf("search by name: %v", ids(p))
	}
	if _, p := get(userA, tenantA, "q="+url.QueryEscape("100%")); !equal(ids(p), lit) {
		t.Fatalf("a literal %% must not act as a wildcard: %v", ids(p))
	}
	// Text that is not phone-shaped searches names only: "100%" must not also match a phone that
	// merely contains the digits 100. Plain digits do search phones.
	var emptyContact uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT contact_id FROM conversations WHERE id=$1`, empty).Scan(&emptyContact); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE contacts SET phone_e164='+5592910000100' WHERE id=$1`, emptyContact)
	if _, p := get(userA, tenantA, "q="+url.QueryEscape("100%")); !equal(ids(p), lit) {
		t.Fatalf("a name-like query with digits must not search phones: %v", ids(p))
	}
	if _, p := get(userA, tenantA, "q=100"); !equal(ids(p), lit, empty) {
		t.Fatalf("plain digits search names and phones: %v", ids(p))
	}
	// "%" is a literal: only the contact whose name really contains one matches, not everything.
	if _, p := get(userA, tenantA, "q=%25"); !equal(ids(p), lit) {
		t.Fatalf("a lone %% must match only the literal %%, got %v", ids(p))
	}
	// "_" would match every name if it were a wildcard; no name here contains one.
	if _, p := get(userA, tenantA, "q=_"); len(p.Items) != 0 {
		t.Fatalf("a lone _ must be literal and match nothing, got %v", ids(p))
	}
	var phone string
	if err := seed.QueryRow(ctx, `SELECT co.phone_e164 FROM conversations c JOIN contacts co ON co.id=c.contact_id WHERE c.id=$1`, fresh).Scan(&phone); err != nil {
		t.Fatal(err)
	}
	if _, p := get(userA, tenantA, "q="+url.QueryEscape(phone[len(phone)-6:])); !equal(ids(p), fresh) {
		t.Fatalf("search by phone digits: %v", ids(p))
	}

	// 5. Filters: assigned=me and waiting=true; unknown values are rejected.
	if _, p := get(userA, tenantA, "assigned=me"); !equal(ids(p), mine) {
		t.Fatalf("assigned=me: %v", ids(p))
	}
	if _, p := get(userA, tenantA, "waiting=true"); !equal(ids(p), old, lit, fresh) {
		t.Fatalf("waiting=true (last message from the customer): %v", ids(p))
	}
	if c, _ := get(userA, tenantA, "assigned=someone"); c != 400 {
		t.Fatalf("unknown assigned filter: %d", c)
	}
	if c, _ := get(userA, tenantA, "sort=created_at:desc"); c != 400 {
		t.Fatalf("a foreign sort would corrupt the cursor and must be rejected: %d", c)
	}
	// 6. Paging with a filter keeps the filter.
	c1, p1 := get(userA, tenantA, "waiting=true&limit=2")
	c2, p2 := get(userA, tenantA, "waiting=true&limit=2&cursor="+url.QueryEscape(p1.NextCursor))
	if c1 != 200 || c2 != 200 || !equal(append(ids(p1), ids(p2)...), old, lit, fresh) || p2.HasMore {
		t.Fatalf("filtered paging: %v %v", ids(p1), ids(p2))
	}
	// 7. Tenant B sees only its own; user A cannot read tenant B at all.
	if _, p := get(userB, tenantB, ""); !equal(ids(p), foreign) {
		t.Fatalf("tenant B list: %v", ids(p))
	}
	if c, _ := get(userA, tenantB, ""); c != 404 {
		t.Fatalf("cross-tenant list without membership: %d", c)
	}
}

// has_more used to be false on every page (the lookahead row was consumed by the loop condition),
// so nobody could ever load older messages. Walk a thread with a small limit.
func TestListMessagesPagesNewestFirstWithoutGaps(t *testing.T) {
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := seed.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	tenantA, userA := uuid.New(), uuid.New()
	exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, userA, userA, userA.String()+"@invalid")
	exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tenantA, tenantA.String())
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id=$1`, tenantA)
		_, _ = seed.Exec(bg, `DELETE FROM users WHERE id=$1`, userA)
	})
	var role uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantA, userA, role)
	contact, conv := uuid.New(), uuid.New()
	exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'Thread','+5592911112222')`, contact, tenantA)
	exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, conv, tenantA, contact)
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	want := make([]string, 0, 5) // newest first
	for i := 0; i < 5; i++ {
		id := uuid.New()
		exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status,created_at) VALUES($1,$2,$3,'inbound','text',$4,'received',$5)`,
			id, tenantA, conv, fmt.Sprintf("m%d", i), base.Add(time.Duration(i)*time.Minute))
		want = append([]string{id.String()}, want...)
	}

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(app), tenancyadapters.NewPostgresTenantRepository(app))
	h := inboxadapters.NewInboxAPIHandler(app)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/messages", tenancyadapters.AuthorizationMiddleware(app, authz)(http.HandlerFunc(h.ListMessages)))
	var walked []string
	cursor := ""
	for i := 0; i < 10; i++ {
		q := url.Values{"limit": {"2"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+tenantA.String()+"/inbox/conversations/"+conv.String()+"/messages?"+q.Encode(), nil)
		req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: userA, Subject: userA.String()}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var page listPage
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
			t.Fatalf("page %d: %d %s", i, rec.Code, rec.Body.String())
		}
		walked = append(walked, ids2(page)...)
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if fmt.Sprint(walked) != fmt.Sprint(want) {
		t.Fatalf("message walk %v want %v", walked, want)
	}
}

func ids2(p listPage) []string {
	out := make([]string, len(p.Items))
	for i, it := range p.Items {
		out[i] = it.ID
	}
	return out
}
