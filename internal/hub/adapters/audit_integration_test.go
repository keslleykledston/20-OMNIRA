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
	// a company that ALSO has a contract with the other hub: what is attributed to the other hub stays out; what carries no attribution is the company's own
	w.exec(`INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, valid_from) VALUES ($1, $2, $3, now() - interval '1 day')`, uuid.New(), otherHub, tA)
	w.auditEvent(&tA, &otherAdmin, "hub.grant.granted", `{"hub_id":"`+otherHub.String()+`","mode":"reply","name":"SO-DO-OUTRO-HUB"}`, now.Add(-30*time.Second))
	w.auditEvent(&tA, &admin, "channel.connection_created", `{"provider":"waha","name":"proprio-da-empresa"}`, now.Add(-90*time.Second))
	// a foreign company's unattributed event is not ours; nor is an event about a company WITHOUT a contract with this hub, even if its metadata
	// carries this hub's id (the metadata is a claim, the contract is the fact)
	w.auditEvent(&foreign, &admin, "channel.connection_created", `{"provider":"waha","name":"empresa-alheia"}`, now.Add(-45*time.Second))
	w.auditEvent(&foreign, &admin, "hub.grant.granted", `{"hub_id":"`+w.hub.String()+`","name":"empresa-alheia-com-id-deste-hub"}`, now.Add(-46*time.Second))

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
	want := []string{"hub.grant.granted", "channel.connection_created", "channel.connection_created", "platform.company.status_changed", "hub.pool.created"} // newest first
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want exactly %v (newest first, this hub's instances only, no message/conversation traffic)", actions, want)
	}
	var viaHub = -1
	for i, it := range page.Items {
		if it.Facts["provider"] == "k3g_crm" {
			viaHub = i
		}
	}
	if viaHub < 0 || page.Items[viaHub].Via != "hub" || page.Items[viaHub].TenantName == "" || page.Items[viaHub].ActorEmail == "" {
		t.Errorf("the connection event must say it came through the hub, with its facts, instance and actor: %+v", page.Items)
	} else if page.Items[viaHub].Facts["host"] != "crm.example.com" {
		t.Errorf("the host fact is missing: %+v", page.Items[viaHub])
	}
	if strings.Contains(body, "SO-DO-OUTRO-HUB") || strings.Contains(body, "empresa-alheia") {
		t.Fatalf("an event attributed to ANOTHER hub, or a foreign company's own event, reached this hub's administrator: %s", body)
	}
	if !strings.Contains(body, "proprio-da-empresa") {
		t.Fatalf("the company's own unattributed event must be visible to a hub that serves it: %s", body)
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
	var seen []string
	next := ""
	for i := 0; i < 10; i++ {
		_, body = api.get(admin, w.hub, "?limit=2&before="+next)
		page = auditPage{} // Unmarshal leaves a field the JSON omits untouched: start from nothing
		w.must(json.Unmarshal([]byte(body), &page))
		for _, it := range page.Items {
			seen = append(seen, it.Action+"@"+it.TenantName)
		}
		if page.Next == "" {
			break
		}
		next = page.Next
	}
	if len(seen) != 5 {
		t.Fatalf("paging 2 at a time must give each event exactly once (%d of 5): %v", len(seen), seen)
	}
}

// Events that share a timestamp must be neither repeated nor skipped across pages (the cursor is (time, id)).
func TestAudit_PagingIsStableForEventsSharingATimestamp(t *testing.T) {
	w := newWorld(t)
	api := newAuditAPI(t, w)
	admin := w.hubAdmin("admin")
	tA := w.tenant["A"]
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	for i := 0; i < 5; i++ {
		w.auditEvent(&tA, &admin, "hub.grant.granted", `{"hub_id":"`+w.hub.String()+`","name":"n`+string(rune('a'+i))+`"}`, at)
	}
	seen := map[string]bool{}
	next := ""
	for i := 0; i < 10; i++ {
		_, body := api.get(admin, w.hub, "?limit=2&before="+next)
		var page auditPage
		w.must(json.Unmarshal([]byte(body), &page))
		for _, it := range page.Items {
			n, _ := it.Facts["name"].(string)
			if seen[n] {
				t.Fatalf("event %s repeated across pages", n)
			}
			seen[n] = true
		}
		if page.Next == "" {
			break
		}
		next = page.Next
	}
	if len(seen) != 5 {
		t.Fatalf("an event sharing a timestamp was skipped: saw %d of 5", len(seen))
	}
	if code, _ := api.get(admin, w.hub, "?before=garbage"); code != http.StatusBadRequest {
		t.Errorf("a malformed cursor: %d", code)
	}
}
