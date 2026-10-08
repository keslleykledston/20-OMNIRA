package adapters_test

// HTTP-level proof of the read-only Hub slice on a real PostgreSQL. The real handler, the real
// tenancyadapters.UserSessionMiddleware (the caller's own RLS session) and the real repository run; only
// authentication is replaced by a test shim that injects the Principal (the production handler never reads
// the X-Test-User header, and ignores X-Tenant-ID on purpose).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
)

type hubAPI struct {
	w   *world
	srv *httptest.Server
}

func newHubAPI(t *testing.T, w *world) *hubAPI {
	t.Helper()
	h := adapters.NewHTTPHandler(w.app)
	shim := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if raw := r.Header.Get("X-Test-User"); raw != "" {
				id, err := uuid.Parse(raw)
				if err != nil {
					t.Errorf("bad test user header %q", raw)
				}
				r = r.WithContext(contextWithPrincipal(r, id))
			}
			next.ServeHTTP(rw, r)
		})
	}
	session := tenancyadapters.UserSessionMiddleware(w.app)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/hubs", shim(session(http.HandlerFunc(h.ListMyHubs))))
	mux.Handle("GET /api/v1/hubs/{hub_id}/inbox", shim(session(http.HandlerFunc(h.ListInbox))))
	mux.Handle("GET /api/v1/hubs/{hub_id}/inbox/{item_id}", shim(session(http.HandlerFunc(h.OpenInboxItem))))
	mux.Handle("POST /api/v1/hubs/{hub_id}/inbox/{item_id}/claim", shim(session(http.HandlerFunc(h.ClaimItem))))
	mux.Handle("POST /api/v1/hubs/{hub_id}/inbox/{item_id}/messages", shim(session(http.HandlerFunc(h.ReplyItem))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &hubAPI{w: w, srv: srv}
}

func (a *hubAPI) do(method, path string, user uuid.UUID, hdr map[string]string) (int, string, http.Header) {
	a.w.t.Helper()
	req, err := http.NewRequest(method, a.srv.URL+path, nil)
	a.w.must(err)
	if user != uuid.Nil {
		req.Header.Set("X-Test-User", user.String())
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	a.w.must(err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), resp.Header
}

type listBody struct {
	Items []struct {
		ID             uuid.UUID  `json:"id"`
		TenantID       uuid.UUID  `json:"tenant_id"`
		TenantName     string     `json:"tenant_name"`
		ConversationID uuid.UUID  `json:"conversation_id"`
		QueueID        *uuid.UUID `json:"queue_id"`
	} `json:"items"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
	Count      int    `json:"count"`
}

func (a *hubAPI) list(user uuid.UUID, query string) (int, listBody) {
	a.w.t.Helper()
	code, body, _ := a.do("GET", fmt.Sprintf("/api/v1/hubs/%s/inbox%s", a.w.hub, query), user, nil)
	var lb listBody
	if code == 200 {
		a.w.must(json.Unmarshal([]byte(body), &lb))
	}
	return code, lb
}

func (a *hubAPI) open(user uuid.UUID, hub, item uuid.UUID) (int, string) {
	code, body, _ := a.do("GET", fmt.Sprintf("/api/v1/hubs/%s/inbox/%s", hub, item), user, nil)
	return code, body
}

func (w *world) itemID(convKey string) uuid.UUID {
	var id uuid.UUID
	w.must(w.owner.QueryRow(w.ctx, `SELECT id FROM hub_inbox_items WHERE conversation_id = $1`, w.conv[convKey]).Scan(&id))
	return id
}

func tenantsOf(lb listBody) map[uuid.UUID]int {
	m := map[uuid.UUID]int{}
	for _, it := range lb.Items {
		m[it.TenantID]++
	}
	return m
}

func TestHubAPI_ListAndOpen_AuthorizationMatrix(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	alice := w.hubAgent("alice")
	w.grant(alice, "A")
	w.grant(alice, "B")
	bob := w.hubAgent("bob")
	w.grant(bob, "B")
	noGrant := w.hubAgent("nogrant")
	carol := w.user("carol")
	w.directMember(carol, "A")
	stranger := w.user("stranger")

	t.Run("Alice lists A and B (4 items) and never C", func(t *testing.T) {
		code, lb := api.list(alice, "")
		if code != 200 {
			t.Fatalf("status %d", code)
		}
		got := tenantsOf(lb)
		if got[w.tenant["A"]] != 3 || got[w.tenant["B"]] != 1 || len(got) != 2 || lb.Count != 4 {
			t.Fatalf("unexpected view: %v count=%d", got, lb.Count)
		}
	})
	t.Run("Bob lists only B", func(t *testing.T) {
		_, lb := api.list(bob, "")
		if got := tenantsOf(lb); got[w.tenant["B"]] != 1 || len(got) != 1 {
			t.Fatalf("bob sees %v", got)
		}
	})
	t.Run("an agent without grants gets an empty list, not an error and not data", func(t *testing.T) {
		code, lb := api.list(noGrant, "")
		if code != 200 || len(lb.Items) != 0 {
			t.Fatalf("status %d items %d", code, len(lb.Items))
		}
	})
	t.Run("a user outside the hub, and a direct tenant member, get 404", func(t *testing.T) {
		for name, u := range map[string]uuid.UUID{"stranger": stranger, "carol (direct member of A)": carol} {
			if code, _ := api.list(u, ""); code != 404 {
				t.Errorf("%s: list status %d, want 404", name, code)
			}
			if code, _ := api.open(u, w.hub, w.itemID("A")); code != 404 {
				t.Errorf("%s: open status %d, want 404", name, code)
			}
		}
	})
	t.Run("a forged hub id is 404", func(t *testing.T) {
		code, body, _ := api.do("GET", fmt.Sprintf("/api/v1/hubs/%s/inbox", uuid.New()), alice, nil)
		if code != 404 {
			t.Fatalf("status %d body %q", code, body)
		}
		if code, _ := api.open(alice, uuid.New(), w.itemID("A")); code != 404 {
			t.Fatalf("open with a forged hub: %d", code)
		}
	})
	t.Run("a malformed id is 400", func(t *testing.T) {
		if code, _, _ := api.do("GET", "/api/v1/hubs/not-a-uuid/inbox", alice, nil); code != 400 {
			t.Fatalf("status %d", code)
		}
		if code, _, _ := api.do("GET", fmt.Sprintf("/api/v1/hubs/%s/inbox/nope", w.hub), alice, nil); code != 400 {
			t.Fatalf("status %d", code)
		}
	})
	t.Run("no authentication is 401", func(t *testing.T) {
		if code, _ := api.list(uuid.Nil, ""); code != 401 {
			t.Fatalf("status %d", code)
		}
	})
	t.Run("a forged tenant selector is refused or ignored, never honoured", func(t *testing.T) {
		if code, _ := api.list(alice, "?tenant_id="+w.tenant["C"].String()); code != 400 {
			t.Fatalf("?tenant_id=C returned %d, want 400", code)
		}
		code, body, _ := api.do("GET", fmt.Sprintf("/api/v1/hubs/%s/inbox", w.hub), alice, map[string]string{"X-Tenant-ID": w.tenant["C"].String(), "X-Hub-ID": uuid.NewString()})
		if code != 200 {
			t.Fatalf("status %d", code)
		}
		if strings.Contains(body, w.tenant["C"].String()) {
			t.Fatal("a tenant header leaked tenant C into the response")
		}
	})
	t.Run("Alice opens items of A and B; the answer carries only that tenant's data", func(t *testing.T) {
		for _, k := range []string{"A", "B"} {
			code, body := api.open(alice, w.hub, w.itemID(k))
			if code != 200 {
				t.Fatalf("open %s: status %d body %s", k, code, body)
			}
			var d struct {
				Tenant       struct{ ID uuid.UUID }   `json:"tenant"`
				Access       struct{ Source string }  `json:"access"`
				Conversation struct{ ID uuid.UUID }   `json:"conversation"`
				Messages     []struct{ ID uuid.UUID } `json:"messages"`
			}
			w.must(json.Unmarshal([]byte(body), &d))
			if d.Tenant.ID != w.tenant[k] || d.Conversation.ID != w.conv[k] || d.Access.Source != "hub" || len(d.Messages) != 1 {
				t.Fatalf("open %s returned %+v", k, d)
			}
			for _, other := range []string{"A", "B", "C"} {
				if other != k && strings.Contains(body, w.tenant[other].String()) {
					t.Fatalf("response for %s mentions tenant %s", k, other)
				}
			}
		}
	})
	t.Run("IDOR: opening an item of an ungranted tenant, by its real id, is 404", func(t *testing.T) {
		if code, body := api.open(alice, w.hub, w.itemID("C")); code != 404 || strings.Contains(body, w.tenant["C"].String()) {
			t.Fatalf("alice opening C: %d %q", code, body)
		}
		if code, _ := api.open(bob, w.hub, w.itemID("A")); code != 404 {
			t.Fatalf("bob opening A: %d", code)
		}
		if code, _ := api.open(noGrant, w.hub, w.itemID("A")); code != 404 {
			t.Fatalf("nogrant opening A: %d", code)
		}
	})
	t.Run("a conversation or tenant uuid used as an item id finds nothing", func(t *testing.T) {
		for _, id := range []uuid.UUID{w.conv["A"], w.tenant["A"], w.contract["A"], uuid.New()} {
			if code, _ := api.open(alice, w.hub, id); code != 404 {
				t.Errorf("foreign uuid %s opened an item: %d", id, code)
			}
		}
	})
	t.Run("denials are indistinguishable: ungranted tenant, missing item and forged hub give the same body", func(t *testing.T) {
		_, b1 := api.open(alice, w.hub, w.itemID("C"))
		_, b2 := api.open(alice, w.hub, uuid.New())
		_, b3 := api.open(alice, uuid.New(), w.itemID("A"))
		if b1 != b2 || b2 != b3 {
			t.Fatalf("distinguishable denials: %q / %q / %q", b1, b2, b3)
		}
	})
	t.Run("the slice is read-only: every non-GET method is rejected", func(t *testing.T) {
		for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			for _, p := range []string{fmt.Sprintf("/api/v1/hubs/%s/inbox", w.hub), fmt.Sprintf("/api/v1/hubs/%s/inbox/%s", w.hub, w.itemID("A"))} {
				if code, _, _ := api.do(m, p, alice, nil); code != 405 {
					t.Errorf("%s %s -> %d, want 405", m, p, code)
				}
			}
		}
	})
}

func TestHubAPI_RevocationIsImmediateOverHTTP(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	steps := []struct {
		name   string
		break_ func(u uuid.UUID)
		fix    func(u uuid.UUID)
	}{
		{"grant revoked", func(u uuid.UUID) { w.exec(`UPDATE effective_access_grants SET status='revoked' WHERE user_id=$1`, u) },
			func(u uuid.UUID) { w.exec(`UPDATE effective_access_grants SET status='active' WHERE user_id=$1`, u) }},
		{"grant expired", func(u uuid.UUID) {
			w.exec(`UPDATE effective_access_grants SET valid_from=now()-interval '2h', valid_until=now()-interval '1h' WHERE user_id=$1`, u)
		}, func(u uuid.UUID) { w.exec(`UPDATE effective_access_grants SET valid_until=NULL WHERE user_id=$1`, u) }},
		{"contract revoked", func(u uuid.UUID) {
			w.exec(`UPDATE hub_tenant_service_contracts SET status='revoked' WHERE id=$1`, w.contract["A"])
		},
			func(u uuid.UUID) {
				w.exec(`UPDATE hub_tenant_service_contracts SET status='active' WHERE id=$1`, w.contract["A"])
			}},
		{"hub suspended", func(u uuid.UUID) { w.exec(`UPDATE service_hubs SET status='suspended' WHERE id=$1`, w.hub) },
			func(u uuid.UUID) { w.exec(`UPDATE service_hubs SET status='active' WHERE id=$1`, w.hub) }},
		{"left the hub", func(u uuid.UUID) { w.exec(`DELETE FROM hub_memberships WHERE user_id=$1`, u) }, nil},
	}
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			u := w.hubAgent("agent-" + strings.ReplaceAll(s.name, " ", "-"))
			w.grant(u, "A")
			item := w.itemID("A")
			if code, _ := api.open(u, w.hub, item); code != 200 {
				t.Fatalf("before: open %d", code)
			}
			s.break_(u)
			if code, _ := api.open(u, w.hub, item); code != 404 {
				t.Errorf("open after %q: %d, want 404", s.name, code)
			}
			if code, lb := api.list(u, ""); code == 200 && len(lb.Items) != 0 {
				t.Errorf("list after %q still returns %d item(s)", s.name, len(lb.Items))
			}
			if s.fix != nil {
				s.fix(u)
				if code, _ := api.open(u, w.hub, item); code != 200 {
					t.Errorf("open after restoring: %d", code)
				}
			}
		})
	}
}

func TestHubAPI_QueueScopeOverHTTP(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	u := w.hubAgent("scoped")
	w.grant(u, "A")
	w.exec(`UPDATE hub_tenant_service_contracts SET service_scope = $2::jsonb WHERE id = $1`, w.contract["A"], fmt.Sprintf(`{"queue_ids":["%s"]}`, w.queue1["A"]))

	_, lb := api.list(u, "")
	if len(lb.Items) != 1 || lb.Items[0].ConversationID != w.conv["A"] {
		t.Fatalf("scoped list should hold only the q1 conversation, got %d item(s)", len(lb.Items))
	}
	if code, _ := api.open(u, w.hub, w.itemID("A")); code != 200 {
		t.Fatalf("q1 item: %d", code)
	}
	for _, k := range []string{"A2", "A0"} {
		if code, _ := api.open(u, w.hub, w.itemID(k)); code != 404 {
			t.Errorf("out-of-scope item %s opened: %d", k, code)
		}
	}
}

func TestHubAPI_Pagination(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	u := w.hubAgent("pager")
	w.grant(u, "A")
	w.grant(u, "B")

	seen := map[uuid.UUID]bool{}
	cursor, pages := "", 0
	for {
		q := "?limit=3"
		if cursor != "" {
			q += "&cursor=" + cursor
		}
		code, body, hdr := api.do("GET", fmt.Sprintf("/api/v1/hubs/%s/inbox%s", w.hub, q), u, nil)
		if code != 200 {
			t.Fatalf("page %d: %d %s", pages, code, body)
		}
		var lb listBody
		w.must(json.Unmarshal([]byte(body), &lb))
		if hdr.Get("X-Pagination-Limit") != "3" || hdr.Get("X-Pagination-Count") == "" {
			t.Fatalf("pagination headers missing: %v", hdr)
		}
		for _, it := range lb.Items {
			if seen[it.ID] {
				t.Fatalf("item %s returned twice", it.ID)
			}
			seen[it.ID] = true
		}
		pages++
		if !lb.HasMore {
			if lb.NextCursor != "" || hdr.Get("X-Pagination-HasMore") != "false" {
				t.Fatal("last page still advertises a cursor")
			}
			break
		}
		cursor = lb.NextCursor
		if pages > 5 {
			t.Fatal("pagination does not terminate")
		}
	}
	if len(seen) != 4 || pages != 2 {
		t.Fatalf("expected 4 items over 2 pages, got %d over %d", len(seen), pages)
	}
	for _, bad := range []string{"garbage", "AAAA", "bm90fGEgY3Vyc29y"} {
		if code, _ := api.list(u, "?cursor="+bad); code != 400 {
			t.Errorf("cursor %q -> %d, want 400", bad, code)
		}
	}
	if code, _ := api.list(u, "?limit=0"); code != 400 {
		t.Errorf("limit=0 -> %d, want 400", code)
	}
}

func contextWithPrincipal(r *http.Request, id uuid.UUID) context.Context {
	return context.WithValue(r.Context(), authn.PrincipalKey, &authn.Principal{UserID: id})
}

type myHub struct {
	ID                 uuid.UUID `json:"id"`
	Name               string    `json:"name"`
	Role               string    `json:"role"`
	CanManageCompanies bool      `json:"can_manage_companies"`
}

func (a *hubAPI) myHubs(user uuid.UUID) (int, []myHub, string) {
	a.w.t.Helper()
	code, body, _ := a.do("GET", "/api/v1/hubs", user, nil)
	var out struct {
		Items []myHub `json:"items"`
	}
	if code == 200 {
		a.w.must(json.Unmarshal([]byte(body), &out))
	}
	return code, out.Items, body
}

func TestHubAPI_ListMyHubs(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	agent := w.hubAgent("agent")
	admin := w.user("admin")
	w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, w.hub, admin, w.roleHubAdmin)
	stranger := w.user("stranger")

	t.Run("a member sees the hub and their role, nothing about others", func(t *testing.T) {
		code, items, _ := api.myHubs(agent)
		if code != 200 || len(items) != 1 || items[0].ID != w.hub || items[0].Role != "hub_agent" {
			t.Fatalf("agent: %d %+v", code, items)
		}
		if _, items, _ := api.myHubs(admin); len(items) != 1 || items[0].Role != "hub_admin" {
			t.Fatalf("admin: %+v", items)
		}
	})
	t.Run("someone with no hub gets an empty list, not an error", func(t *testing.T) {
		code, items, body := api.myHubs(stranger)
		if code != 200 || len(items) != 0 || !strings.Contains(body, `"items":[]`) {
			t.Fatalf("stranger: %d %q", code, body)
		}
	})
	t.Run("a hub the caller is not in never appears", func(t *testing.T) {
		other := uuid.New()
		w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'Somebody else hub')`, other)
		if _, items, _ := api.myHubs(agent); len(items) != 1 || items[0].ID == other {
			t.Fatalf("leaked another hub: %+v", items)
		}
	})
	t.Run("a member of two hubs gets both, ordered by name", func(t *testing.T) {
		second := uuid.New()
		w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'AAA first by name')`, second)
		u := w.hubAgent("two")
		w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, second, u, w.roleHubAgent)
		_, items, _ := api.myHubs(u)
		if len(items) != 2 || items[0].ID != second {
			t.Fatalf("expected 2 hubs ordered by name, got %+v", items)
		}
	})
	t.Run("a suspended hub is not offered, and leaving a hub removes it", func(t *testing.T) {
		u := w.hubAgent("temp")
		w.exec(`UPDATE service_hubs SET status = 'suspended' WHERE id = $1`, w.hub)
		if _, items, _ := api.myHubs(u); len(items) != 0 {
			t.Errorf("suspended hub still listed: %+v", items)
		}
		w.exec(`UPDATE service_hubs SET status = 'active' WHERE id = $1`, w.hub)
		if _, items, _ := api.myHubs(u); len(items) != 1 {
			t.Errorf("reactivated hub not listed: %+v", items)
		}
		w.exec(`DELETE FROM hub_memberships WHERE hub_id = $1 AND user_id = $2`, w.hub, u)
		if _, items, _ := api.myHubs(u); len(items) != 0 {
			t.Errorf("a removed member still sees the hub: %+v", items)
		}
	})
	t.Run("unauthenticated is 401 and the endpoint is read-only", func(t *testing.T) {
		if code, _, _ := api.myHubs(uuid.Nil); code != 401 {
			t.Errorf("status %d", code)
		}
		for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
			if code, _, _ := api.do(m, "/api/v1/hubs", agent, nil); code != 405 {
				t.Errorf("%s -> %d, want 405", m, code)
			}
		}
	})
}

func TestHubAPI_TenantNamesComeOnlyFromGrantedTenants(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	w.exec(`UPDATE tenants SET trade_name = 'Nome Fantasia B' WHERE id = $1`, w.tenant["B"])
	alice := w.hubAgent("alice")
	w.grant(alice, "A")
	w.grant(alice, "B")

	_, lb := api.list(alice, "")
	if len(lb.Items) != 4 {
		t.Fatalf("expected 4 items, got %d", len(lb.Items))
	}
	var nameC string
	w.must(w.owner.QueryRow(w.ctx, `SELECT legal_name FROM tenants WHERE id = $1`, w.tenant["C"]).Scan(&nameC))
	_, raw, _ := api.do("GET", fmt.Sprintf("/api/v1/hubs/%s/inbox", w.hub), alice, nil)
	if strings.Contains(raw, nameC) || strings.Contains(raw, w.tenant["C"].String()) {
		t.Fatal("the response mentions the ungranted tenant C")
	}
	for _, it := range lb.Items {
		switch it.TenantID {
		case w.tenant["A"]:
			if !strings.HasPrefix(it.TenantName, "Tenant A ") {
				t.Errorf("tenant A name: %q", it.TenantName)
			}
		case w.tenant["B"]:
			if it.TenantName != "Nome Fantasia B" {
				t.Errorf("trade name must win over legal name, got %q", it.TenantName)
			}
		default:
			t.Errorf("item of an unexpected tenant %s", it.TenantID)
		}
	}
	// the same name travels with the opened item
	code, body := api.open(alice, w.hub, w.itemID("B"))
	if code != 200 || !strings.Contains(body, `"tenant_name":"Nome Fantasia B"`) {
		t.Fatalf("open: %d %s", code, body)
	}
	// a grant that is no longer live gives no names (the rows are hidden too)
	w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE user_id = $1 AND tenant_id = $2`, alice, w.tenant["B"])
	_, raw, _ = api.do("GET", fmt.Sprintf("/api/v1/hubs/%s/inbox", w.hub), alice, nil)
	if strings.Contains(raw, "Nome Fantasia B") {
		t.Error("a revoked grant still reveals the tenant name")
	}
}
