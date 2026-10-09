package adapters_test

// Audit view of a hub (ADR-0038 §6) on a real PostgreSQL, through the real handler: only an admin of the hub; only the configuration events of
// THIS hub's instances; nothing of the per-message/conversation traffic; only whitelisted facts; filter and paging.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/hub/adapters"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
)

type auditAPI struct {
	w   *world
	srv *httptest.Server
}

func newAuditAPI(t *testing.T, w *world) *auditAPI {
	t.Helper()
	h := adapters.NewAuditHandler(w.app)
	shim := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if raw := r.Header.Get("X-Test-User"); raw != "" {
				id, _ := uuid.Parse(raw)
				r = r.WithContext(contextWithPrincipal(r, id))
			}
			next.ServeHTTP(rw, r)
		})
	}
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/hubs/{hub_id}/audit", shim(tenancyadapters.UserSessionMiddleware(w.app)(http.HandlerFunc(h.List))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &auditAPI{w: w, srv: srv}
}

func (a *auditAPI) get(user uuid.UUID, hub uuid.UUID, query string) (int, string) {
	a.w.t.Helper()
	req, err := http.NewRequest("GET", a.srv.URL+"/api/v1/hubs/"+hub.String()+"/audit"+query, bytes.NewReader(nil))
	a.w.must(err)
	if user != uuid.Nil {
		req.Header.Set("X-Test-User", user.String())
	}
	resp, err := http.DefaultClient.Do(req)
	a.w.must(err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (w *world) auditEvent(tenant *uuid.UUID, actor *uuid.UUID, action string, meta string, at time.Time) {
	w.exec(`INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata, created_at)
	        VALUES ($1, $2, $3, $4, 'x', $5, 'success', $6, $7::jsonb, $8)`, uuid.New(), tenant, actor, action, uuid.NewString(), uuid.New(), meta, at)
}

type auditPage struct {
	Items []struct {
		Action     string         `json:"action"`
		TenantName string         `json:"tenant_name"`
		ActorEmail string         `json:"actor_email"`
		Via        string         `json:"via"`
		Facts      map[string]any `json:"facts"`
	} `json:"items"`
	Next string `json:"next"`
}

func TestAudit_OnlyAnAdminOfTheHubSeesItsInstancesConfigurationChanges(t *testing.T) {
	w := newWorld(t)
	api := newAuditAPI(t, w)
	admin := w.hubAdmin("admin")
	w.email(admin, "adm-"+w.hub.String()[:8]+"@k3g.com")
	agent := w.hubAgent("agent")
	otherHub := uuid.New()
	w.exec(`INSERT INTO service_hubs (id, name) VALUES ($1, 'Other')`, otherHub)
	otherAdmin := w.user("otheradmin")
	w.exec(`INSERT INTO hub_memberships (hub_id, user_id, role_id) VALUES ($1, $2, $3)`, otherHub, otherAdmin, w.roleHubAdmin)
	foreign := uuid.New()
	w.exec(`INSERT INTO tenants (id, legal_name, status) VALUES ($1, 'Foreign Co', 'active')`, foreign)

	now := time.Now().UTC()
	tA, tB := w.tenant["A"], w.tenant["B"]
	w.auditEvent(&tA, &admin, "hub.grant.granted", `{"hub_id":"`+w.hub.String()+`","mode":"reply","secret":"nao-sai","token":"nao-sai"}`, now.Add(-1*time.Minute))
	w.auditEvent(&tA, &admin, "channel.connection_created", `{"via":"hub","hub_id":"`+w.hub.String()+`","provider":"k3g_crm","host":"crm.example.com"}`, now.Add(-2*time.Minute))
	w.auditEvent(&tB, &admin, "platform.company.status_changed", `{"from":"active","to":"suspended"}`, now.Add(-3*time.Minute))
	w.auditEvent(nil, &admin, "hub.pool.created", `{"hub_id":"`+w.hub.String()+`","name":"Suporte"}`, now.Add(-4*time.Minute))
	// what must NOT appear: per-message and per-conversation traffic, other hubs' instances, unrelated actions
	w.auditEvent(&tA, &agent, "hub.message.sent", `{"hub_id":"`+w.hub.String()+`"}`, now)
	w.auditEvent(&tA, &agent, "hub.conversation.claimed", `{"hub_id":"`+w.hub.String()+`"}`, now)
	w.auditEvent(&foreign, &admin, "platform.company.status_changed", `{"from":"active","to":"suspended"}`, now)
	w.auditEvent(&tA, &admin, "tickets.created", `{}`, now)
	w.auditEvent(nil, &admin, "hub.pool.created", `{"hub_id":"`+otherHub.String()+`","name":"De outro Hub"}`, now)

	for name, who := range map[string]uuid.UUID{"agent": agent, "admin of another hub": otherAdmin, "anonymous": uuid.Nil} {
		want := http.StatusNotFound
		if who == uuid.Nil {
			want = http.StatusUnauthorized
		}
		if code, _ := api.get(who, w.hub, ""); code != want {
			t.Errorf("%s: %d, want %d", name, code, want)
		}
	}
	if code, _ := api.get(admin, otherHub, ""); code != http.StatusNotFound {
		t.Errorf("an admin must not read another hub's audit: %d", code)
	}

	code, body := api.get(admin, w.hub, "")
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var page auditPage
	w.must(json.Unmarshal([]byte(body), &page))
	var actions []string
	for _, it := range page.Items {
		actions = append(actions, it.Action)
	}
	want := []string{"hub.grant.granted", "channel.connection_created", "platform.company.status_changed", "hub.pool.created"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want exactly %v (newest first, this hub's instances only, no message/conversation traffic)", actions, want)
	}
	if page.Items[1].Via != "hub" || page.Items[1].Facts["provider"] != "k3g_crm" || page.Items[1].TenantName == "" || page.Items[1].ActorEmail == "" {
		t.Errorf("the connection event must say it came through the hub, with its facts, instance and actor: %+v", page.Items[1])
	}
	if strings.Contains(body, "nao-sai") || strings.Contains(body, `"secret"`) || strings.Contains(body, `"token"`) {
		t.Fatalf("metadata that is not whitelisted leaked: %s", body)
	}
	if page.Items[0].Facts["mode"] != "reply" {
		t.Errorf("whitelisted facts must pass: %+v", page.Items[0].Facts)
	}

	// filter by instance (an instance of ANOTHER hub gives nothing, not its events)
	_, body = api.get(admin, w.hub, "?tenant="+tB.String())
	w.must(json.Unmarshal([]byte(body), &page))
	if len(page.Items) != 1 || page.Items[0].Action != "platform.company.status_changed" {
		t.Errorf("filter by instance B: %s", body)
	}
	_, body = api.get(admin, w.hub, "?tenant="+foreign.String())
	w.must(json.Unmarshal([]byte(body), &page))
	if len(page.Items) != 0 {
		t.Errorf("another hub's instance must show nothing: %s", body)
	}
	if code, _ := api.get(admin, w.hub, "?tenant=not-a-uuid"); code != http.StatusBadRequest {
		t.Errorf("a malformed filter: %d", code)
	}

	// paging: limit 2 -> next cursor -> the rest, no overlap
	_, body = api.get(admin, w.hub, "?limit=2")
	w.must(json.Unmarshal([]byte(body), &page))
	if len(page.Items) != 2 || page.Next == "" {
		t.Fatalf("page 1: %s", body)
	}
	first := page.Items[0].Action
	_, body = api.get(admin, w.hub, "?limit=2&before="+page.Next)
	page = auditPage{} // Unmarshal leaves a field the JSON omits untouched: start from nothing
	w.must(json.Unmarshal([]byte(body), &page))
	if len(page.Items) != 2 || page.Items[0].Action == first || page.Next != "" {
		t.Fatalf("page 2: %s", body)
	}
}
