package adapters_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	"github.com/omnira/omnira/internal/platform/authn"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

// ADR-0043 (S3-lite): ?queue= is a VIEW filter by group. "mine" = groups the caller belongs to (active) plus what is assigned to the
// caller; "none" = no group; a uuid = that group (another tenant's id finds nothing). It never widens what the caller may read.
func TestListConversationsQueueFilterIsAViewByGroup(t *testing.T) {
	e := newKindEnv(t)
	tenantA, tenantB := e.tenant(), e.tenant()
	user := e.member(tenantA, "tenant_agent")
	qMine, qOther, qInactive, qForeign := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode,is_default) VALUES($1,$2,'Meu','manual',true)`, qMine, tenantA)
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$2,'Outro','manual')`, qOther, tenantA)
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$2,'Saí','manual')`, qInactive, tenantA)
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode,is_default) VALUES($1,$2,'Do B','manual',true)`, qForeign, tenantB)
	e.exec(`INSERT INTO queue_members(tenant_id,queue_id,user_id,active,available,capacity) VALUES($1,$2,$3,true,true,5)`, tenantA, qMine, user)
	e.exec(`INSERT INTO queue_members(tenant_id,queue_id,user_id,active,available,capacity) VALUES($1,$2,$3,false,true,5)`, tenantA, qInactive, user)

	inMine := e.conversation(tenantA, "No meu grupo", "other", "+5592966660001")
	inOther := e.conversation(tenantA, "Em outro grupo", "other", "+5592966660002")
	assignedElsewhere := e.conversation(tenantA, "Atribuída a mim", "other", "+5592966660003")
	noGroup := e.conversation(tenantA, "Sem grupo", "other", "+5592966660004")
	inInactive := e.conversation(tenantA, "Grupo que deixei", "other", "+5592966660005")
	e.exec(`UPDATE conversations SET queue_id=$2 WHERE id=$1`, inMine, qMine)
	e.exec(`UPDATE conversations SET queue_id=$2 WHERE id=$1`, inOther, qOther)
	e.exec(`UPDATE conversations SET queue_id=$2, assigned_to_user_id=$3 WHERE id=$1`, assignedElsewhere, qOther, user)
	e.exec(`UPDATE conversations SET queue_id=$2 WHERE id=$1`, inInactive, qInactive)

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(e.app), tenancyadapters.NewPostgresTenantRepository(e.app))
	h := inboxadapters.NewInboxAPIHandler(e.app)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations", tenancyadapters.AuthorizationMiddleware(e.app, authz)(http.HandlerFunc(h.ListConversations)))
	list := func(query string) (int, map[string]bool) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+tenantA.String()+"/inbox/conversations"+query, nil)
		req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		ids := map[string]bool{}
		if rec.Code == 200 {
			var page struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			for _, it := range page.Items {
				ids[it.ID] = true
			}
		}
		return rec.Code, ids
	}
	want := func(name, query string, code int, ids ...uuid.UUID) {
		t.Helper()
		gotCode, got := list(query)
		if gotCode != code || len(got) != len(ids) {
			t.Fatalf("%s: %d %v, want %d with %d items", name, gotCode, got, code, len(ids))
		}
		for _, id := range ids {
			if !got[id.String()] {
				t.Errorf("%s: missing %s", name, id)
			}
		}
	}

	want("no filter shows everything the caller may read", "", 200, inMine, inOther, assignedElsewhere, noGroup, inInactive)
	want("mine = my active groups + assigned to me", "?queue=mine", 200, inMine, assignedElsewhere)
	want("none", "?queue=none", 200, noGroup)
	want("one group", "?queue="+qOther.String(), 200, inOther, assignedElsewhere)
	want("another tenant's group finds nothing", "?queue="+qForeign.String(), 200)
	want("malformed", "?queue=zzz", 400)
}
