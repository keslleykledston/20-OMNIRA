package adapters_test

// ADR-0059 Gate 2 through the real HTTP handler, against a real PostgreSQL: both sides of a link/
// relationship write, idempotency replay/mismatch, optimistic concurrency, and uniform 404/401 for
// everyone else.
//
// Run with: scripts/test-integration-one.sh ./internal/hub/adapters TestGroupOrganizerAPI

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/adapters"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
)

type groupOrganizerAPI struct {
	w   *world
	srv *httptest.Server
}

func newGroupOrganizerAPI(t *testing.T, w *world) *groupOrganizerAPI {
	t.Helper()
	h := adapters.NewGroupOrganizerHandler(w.app)
	shim := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if raw := r.Header.Get("X-Test-User"); raw != "" {
				id, _ := uuid.Parse(raw)
				r = r.WithContext(contextWithPrincipal(r, id))
			}
			next.ServeHTTP(rw, r)
		})
	}
	session := tenancyadapters.UserSessionMiddleware(w.app)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/hubs/{hub_id}/groups/catalog", shim(session(http.HandlerFunc(h.ListCatalog))))
	mux.Handle("POST /api/v1/hubs/{hub_id}/groups/catalog", shim(session(http.HandlerFunc(h.UpsertCatalogEntry))))
	mux.Handle("GET /api/v1/hubs/{hub_id}/groups/catalog/{group_id}/links", shim(session(http.HandlerFunc(h.ListLinks))))
	mux.Handle("POST /api/v1/hubs/{hub_id}/groups/catalog/{group_id}/links", shim(session(http.HandlerFunc(h.CreateLink))))
	mux.Handle("POST /api/v1/hubs/{hub_id}/groups/catalog/{group_id}/links/{tenant_id}/revoke", shim(session(http.HandlerFunc(h.RevokeLink))))
	mux.Handle("POST /api/v1/hubs/{hub_id}/groups/catalog/{group_id}/links/{tenant_id}/transfer", shim(session(http.HandlerFunc(h.TransferLink))))
	mux.Handle("GET /api/v1/hubs/{hub_id}/groups/relationships", shim(session(http.HandlerFunc(h.ListRelationships))))
	mux.Handle("POST /api/v1/hubs/{hub_id}/groups/relationships", shim(session(http.HandlerFunc(h.CreateRelationship))))
	mux.Handle("POST /api/v1/hubs/{hub_id}/groups/relationships/{relationship_id}/revoke", shim(session(http.HandlerFunc(h.RevokeRelationship))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &groupOrganizerAPI{w: w, srv: srv}
}

func (a *groupOrganizerAPI) call(method, path string, user uuid.UUID, idempotencyKey string, body any) (int, map[string]any) {
	a.w.t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		a.w.must(err)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.srv.URL+"/api/v1/hubs/"+a.w.hub.String()+path, rd)
	a.w.must(err)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if user != uuid.Nil {
		req.Header.Set("X-Test-User", user.String())
	}
	resp, err := http.DefaultClient.Do(req)
	a.w.must(err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed) // best effort; some responses (204, text/plain 404) are not JSON
	return resp.StatusCode, parsed
}

func TestGroupOrganizerAPI_UnauthenticatedIsUniform401(t *testing.T) {
	w := newWorld(t)
	api := newGroupOrganizerAPI(t, w)
	if code, _ := api.call("GET", "/groups/catalog", uuid.Nil, "", nil); code != 401 {
		t.Fatalf("anonymous: got %d, want 401", code)
	}
}

func TestGroupOrganizerAPI_UpsertCatalogIsIdempotentByNaturalKeyAndRequiresOriginScope(t *testing.T) {
	w := newWorld(t)
	api := newGroupOrganizerAPI(t, w)
	conn := w.channelConnection("A")

	alice := w.hubAgent("alice")
	w.canManage(w.grant(alice, "A"))
	w.groupsScope("A")

	bob := w.hubAgent("bob") // can_manage on A's OTHER scopes only in this test's setup — never gets 'groups'
	w.canManage(w.grant(bob, "B"))

	body := map[string]any{
		"source_tenant_id":             w.tenant["A"],
		"source_channel_connection_id": conn,
		"provider_group_id":            "551199990000001@g.us",
		"display_name":                 "Alpha Suporte",
	}
	if code, _ := api.call("POST", "/groups/catalog", bob, "", body); code != 404 {
		t.Fatalf("bob (no 'groups' on A): got %d, want 404", code)
	}
	code, resp := api.call("POST", "/groups/catalog", alice, "", body)
	if code != 200 {
		t.Fatalf("alice first upsert: got %d, want 200", code)
	}
	groupID := resp["id"].(string)

	body["display_name"] = "Alpha Suporte (renamed)"
	code, resp2 := api.call("POST", "/groups/catalog", alice, "", body)
	if code != 200 {
		t.Fatalf("alice second upsert (same identity): got %d, want 200", code)
	}
	if resp2["id"].(string) != groupID {
		t.Fatalf("re-registering the same natural key must update the SAME row: got id %v, want %v", resp2["id"], groupID)
	}
	if n := w.count(`SELECT count(*) FROM hub_group_catalog WHERE hub_id = $1 AND source_tenant_id = $2`, w.hub, w.tenant["A"]); n != 1 {
		t.Fatalf("two upserts of the same identity created %d row(s), want 1 (no duplicate)", n)
	}
}

func TestGroupOrganizerAPI_CreateLinkRequiresIdempotencyKeyAndBothSides(t *testing.T) {
	w := newWorld(t)
	api := newGroupOrganizerAPI(t, w)
	conn := w.channelConnection("A")
	group := w.catalogEntry("", "A", conn, "551199990000002@g.us", "Alpha x Beta")
	w.groupsScope("A")
	w.groupsScope("B")

	owner := w.hubAgent("owner")
	w.canManage(w.grant(owner, "A"))
	w.canManage(w.grant(owner, "B"))

	onlyA := w.hubAgent("onlyA")
	w.canManage(w.grant(onlyA, "A"))

	linkBody := map[string]any{"tenant_id": w.tenant["B"], "role": "supplier"}

	if code, _ := api.call("POST", "/groups/catalog/"+group.String()+"/links", owner, "", linkBody); code != 400 {
		t.Fatalf("missing Idempotency-Key: got %d, want 400", code)
	}
	if code, _ := api.call("POST", "/groups/catalog/"+group.String()+"/links", onlyA, "k-onlya-001", linkBody); code != 404 {
		t.Fatalf("onlyA (no 'groups' on destination B): got %d, want 404", code)
	}

	code, resp := api.call("POST", "/groups/catalog/"+group.String()+"/links", owner, "k-owner-001", linkBody)
	if code != 201 {
		t.Fatalf("owner creating the link (both sides authorized): got %d, want 201 (%v)", code, resp)
	}
	linkID := resp["id"].(string)

	// exact replay: same key, same body -> 200, same resource, no duplicate row
	code, resp2 := api.call("POST", "/groups/catalog/"+group.String()+"/links", owner, "k-owner-001", linkBody)
	if code != 200 {
		t.Fatalf("replay with same Idempotency-Key: got %d, want 200", code)
	}
	if resp2["id"].(string) != linkID {
		t.Fatalf("replay returned a different resource: got %v, want %v", resp2["id"], linkID)
	}
	if n := w.count(`SELECT count(*) FROM hub_group_tenant_links WHERE id = $1`, linkID); n != 1 {
		t.Fatalf("replay must not create a second row: found %d", n)
	}

	// same key, DIFFERENT body -> 422 (key reused for another request)
	mismatch := map[string]any{"tenant_id": w.tenant["B"], "role": "consumer"}
	if code, _ := api.call("POST", "/groups/catalog/"+group.String()+"/links", owner, "k-owner-001", mismatch); code != 422 {
		t.Fatalf("same key, different body: got %d, want 422", code)
	}

	// B already has an active link (from the replay above) -> a NEW key with the same target is a conflict
	if code, _ := api.call("POST", "/groups/catalog/"+group.String()+"/links", owner, "k-owner-002", linkBody); code != 409 {
		t.Fatalf("second active link to the same tenant: got %d, want 409", code)
	}
}

func TestGroupOrganizerAPI_RevokeLinkIsOptimisticallyConcurrent(t *testing.T) {
	w := newWorld(t)
	api := newGroupOrganizerAPI(t, w)
	conn := w.channelConnection("A")
	group := w.catalogEntry("", "A", conn, "551199990000003@g.us", "Alpha Financeiro")
	w.groupsScope("A")
	w.groupsScope("B")

	owner := w.hubAgent("owner")
	w.canManage(w.grant(owner, "A"))
	w.canManage(w.grant(owner, "B"))

	linkBody := map[string]any{"tenant_id": w.tenant["B"], "role": "observer"}
	code, resp := api.call("POST", "/groups/catalog/"+group.String()+"/links", owner, "k-revoke-001", linkBody)
	if code != 201 {
		t.Fatalf("create: got %d, want 201", code)
	}
	version := int64(resp["version"].(float64))

	if code, _ := api.call("POST", "/groups/catalog/"+group.String()+"/links/"+w.tenant["B"].String()+"/revoke",
		owner, "", map[string]any{"expected_version": version + 1}); code != 409 {
		t.Fatalf("revoke with a stale expected_version: got %d, want 409", code)
	}
	if code, _ := api.call("POST", "/groups/catalog/"+group.String()+"/links/"+w.tenant["B"].String()+"/revoke",
		owner, "", map[string]any{"expected_version": version}); code != 204 {
		t.Fatalf("revoke with the correct expected_version: got %d, want 204", code)
	}
	if code, _ := api.call("POST", "/groups/catalog/"+group.String()+"/links/"+w.tenant["B"].String()+"/revoke",
		owner, "", map[string]any{"expected_version": version}); code != 409 {
		t.Fatalf("revoking an already-revoked link: got %d, want 409", code)
	}
}

func TestGroupOrganizerAPI_TransferLinkIsAtomic(t *testing.T) {
	w := newWorld(t)
	api := newGroupOrganizerAPI(t, w)
	conn := w.channelConnection("A")
	group := w.catalogEntry("", "A", conn, "551199990000004@g.us", "Alpha Comercial")
	w.groupsScope("A")
	w.groupsScope("B")
	w.groupsScope("C")

	owner := w.hubAgent("owner")
	w.canManage(w.grant(owner, "A"))
	w.canManage(w.grant(owner, "B"))
	w.canManage(w.grant(owner, "C"))

	code, resp := api.call("POST", "/groups/catalog/"+group.String()+"/links", owner, "k-transfer-seed",
		map[string]any{"tenant_id": w.tenant["B"], "role": "consumer"})
	if code != 201 {
		t.Fatalf("seed link to B: got %d, want 201", code)
	}
	version := int64(resp["version"].(float64))

	transferBody := map[string]any{"to_tenant_id": w.tenant["C"], "role": "consumer", "expected_version": version}
	code, resp2 := api.call("POST", "/groups/catalog/"+group.String()+"/links/"+w.tenant["B"].String()+"/transfer", owner, "k-transfer-001", transferBody)
	if code != 201 {
		t.Fatalf("transfer B->C: got %d, want 201 (%v)", code, resp2)
	}
	if resp2["tenant_id"].(string) != w.tenant["C"].String() {
		t.Fatalf("transferred link's tenant: got %v, want C", resp2["tenant_id"])
	}

	if n := w.count(`SELECT count(*) FROM hub_group_tenant_links WHERE hub_group_id = $1 AND tenant_id = $2 AND status = 'active'`, group, w.tenant["B"]); n != 0 {
		t.Fatalf("B must have no active link after transfer: got %d", n)
	}
	if n := w.count(`SELECT count(*) FROM hub_group_tenant_links WHERE hub_group_id = $1 AND tenant_id = $2 AND status = 'active'`, group, w.tenant["C"]); n != 1 {
		t.Fatalf("C must have exactly one active link after transfer: got %d", n)
	}

	// replay with the same key is safe (no second transfer attempted against the now-revoked B link)
	code, resp3 := api.call("POST", "/groups/catalog/"+group.String()+"/links/"+w.tenant["B"].String()+"/transfer", owner, "k-transfer-001", transferBody)
	if code != 200 {
		t.Fatalf("replay transfer: got %d, want 200", code)
	}
	if resp3["id"].(string) != resp2["id"].(string) {
		t.Fatalf("replay returned a different resource")
	}
}

func TestGroupOrganizerAPI_RelationshipRequiresBothSidesAndRevokeIsFinal(t *testing.T) {
	w := newWorld(t)
	api := newGroupOrganizerAPI(t, w)
	w.groupsScope("A")
	w.groupsScope("B")

	owner := w.hubAgent("owner")
	w.canManage(w.grant(owner, "A"))
	w.canManage(w.grant(owner, "B"))

	onlyA := w.hubAgent("onlyA")
	w.canManage(w.grant(onlyA, "A"))

	relBody := map[string]any{"consumer_tenant_id": w.tenant["A"], "supplier_tenant_id": w.tenant["B"], "service_kind": "transito_ip"}

	if code, _ := api.call("POST", "/groups/relationships", onlyA, "k-rel-onlya", relBody); code != 404 {
		t.Fatalf("onlyA (no 'groups' on supplier B): got %d, want 404", code)
	}
	code, resp := api.call("POST", "/groups/relationships", owner, "k-rel-001", relBody)
	if code != 201 {
		t.Fatalf("owner creating A->B relationship: got %d, want 201 (%v)", code, resp)
	}
	relID := resp["id"].(string)

	code, items := api.call("GET", "/groups/relationships", onlyA, "", nil)
	if code != 200 {
		t.Fatalf("onlyA listing: got %d, want 200", code)
	}
	if its, ok := items["items"].([]any); !ok || len(its) != 0 {
		t.Fatalf("onlyA (only one side authorized) must not see the relationship (SEC-002): %v", items)
	}

	if code, _ := api.call("POST", "/groups/relationships/"+relID+"/revoke", owner, "", nil); code != 204 {
		t.Fatalf("revoke: got %d, want 204", code)
	}
	if code, _ := api.call("POST", "/groups/relationships/"+relID+"/revoke", owner, "", nil); code != 409 {
		t.Fatalf("revoking an already-revoked relationship: got %d, want 409", code)
	}
}
