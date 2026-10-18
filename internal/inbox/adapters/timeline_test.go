package adapters_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// The timeline of an attendance: what happened TO it, from the facts the writers already keep, read with the same guard as its messages.
func TestTimelineTellsWhatHappenedToTheAttendanceAndNothingMore(t *testing.T) {
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
	tenantA, tenantB, ana, beto, userB := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, u := range []struct {
		id   uuid.UUID
		name string
	}{{ana, "Ana Atendente"}, {beto, "Beto Atendente"}, {userB, "Bia da Outra"}} {
		exec(`INSERT INTO users(id,external_subject,email,display_name,status) VALUES($1,$2,$3,$4,'active')`, u.id, u.id, u.id.String()+"@invalid", u.name)
	}
	for _, tn := range []uuid.UUID{tenantA, tenantB} {
		exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, tn, tn.String())
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = seed.Exec(bg, `DELETE FROM tenants WHERE id IN ($1,$2)`, tenantA, tenantB)
		_, _ = seed.Exec(bg, `DELETE FROM users WHERE id IN ($1,$2,$3)`, ana, beto, userB)
	})
	var role uuid.UUID
	if err := seed.QueryRow(ctx, `SELECT id FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantA, ana, role)
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantA, beto, role)
	exec(`INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active')`, tenantB, userB, role)

	newConv := func(tn uuid.UUID, phone string) (contact, conv uuid.UUID) {
		contact, conv = uuid.New(), uuid.New()
		exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'Cliente',$3)`, contact, tn, phone)
		exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, conv, tn, contact)
		return
	}
	contact, conv := newConv(tenantA, "+5592933330001")
	_, foreign := newConv(tenantB, "+5592933330002")
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	at := func(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }

	// the platform gives it to Ana, Ana sends it to a queue, Beto takes it, the supervisor-ish step: Ana releases nothing; Beto hands it to Ana
	exec(`INSERT INTO assignment_events(tenant_id,conversation_id,from_user_id,to_user_id,changed_by,reason,actor_source,created_at) VALUES($1,$2,NULL,$3,NULL,'round_robin','system',$4)`, tenantA, conv, ana, at(1))
	exec(`INSERT INTO assignment_events(tenant_id,conversation_id,from_user_id,to_user_id,changed_by,reason,created_at) VALUES($1,$2,$3,NULL,$3,'queue_transfer',$4)`, tenantA, conv, ana, at(5))
	exec(`INSERT INTO assignment_events(tenant_id,conversation_id,from_user_id,to_user_id,changed_by,reason,created_at) VALUES($1,$2,NULL,$3,$3,'manual_claim',$4)`, tenantA, conv, beto, at(8))
	// an external application takes it and gives it back (the control history); a control change between people must not appear
	exec(`INSERT INTO conversation_control_events(tenant_id,conversation_id,control_version,from_kind,to_kind,to_holder_id,actor_kind,reason) VALUES($1,$2,1,NULL,'crm_application',$3,'crm_application','claim')`, tenantA, conv, uuid.New())
	exec(`UPDATE conversation_control_events SET created_at=$1 WHERE conversation_id=$2 AND control_version=1`, at(10), conv)
	exec(`INSERT INTO conversation_control_events(tenant_id,conversation_id,control_version,from_kind,to_kind,actor_kind,reason,claimed_agent_ref) VALUES($1,$2,2,'crm_application',NULL,'crm_application','release','agente-secreto-do-crm')`, tenantA, conv)
	exec(`UPDATE conversation_control_events SET created_at=$1 WHERE conversation_id=$2 AND control_version=2`, at(12), conv)
	exec(`INSERT INTO conversation_control_events(tenant_id,conversation_id,control_version,from_kind,to_kind,actor_kind,reason) VALUES($1,$2,3,'tenant_user','tenant_user','tenant_user','between people')`, tenantA, conv)
	exec(`UPDATE conversation_control_events SET created_at=$1 WHERE conversation_id=$2 AND control_version=3`, at(13), conv)
	exec(`INSERT INTO conversation_control_events(tenant_id,conversation_id,control_version,from_kind,to_kind,actor_kind,reason) VALUES($1,$2,4,'crm_application','crm_application','crm_application','renewed')`, tenantA, conv)
	exec(`UPDATE conversation_control_events SET created_at=$1 WHERE conversation_id=$2 AND control_version=4`, at(14), conv)
	// (the source 'finalize' is the one the session guard lets a seed insert; the timeline does not look at it)
	// a protocol opened and closed on its own (not by a finalization) ...
	exec(`INSERT INTO service_sessions(tenant_id,protocol,protocol_sequence,conversation_id,contact_id,status,opened_source,opened_by_user_id,opened_at,resolution_code,resolution_canonical,closed_source,closed_at)
	      VALUES($1,'KTG-1',1,$2,$3,'closed','finalize',$4,$5,'other','other','operator',$6)`, tenantA, conv, contact, beto, at(8), at(20))
	// ... and a second one closed by the finalization, which says so itself
	exec(`INSERT INTO service_sessions(tenant_id,protocol,protocol_sequence,conversation_id,contact_id,status,opened_source,opened_at,resolution_code,resolution_canonical,closed_source,closed_at)
	      VALUES($1,'KTG-2',2,$2,$3,'closed','finalize',$4,'resolved','resolved_first_contact','finalize',$5)`, tenantA, conv, contact, at(25), at(40))
	exec(`INSERT INTO conversation_closures(tenant_id,conversation_id,contact_id,closed_by_user_id,source,reason,note,summary,created_at) VALUES($1,$2,$3,$4,'agent','resolved','NOTA-PRIVADA','RESUMO-PRIVADO',$5)`, tenantA, conv, contact, beto, at(40))

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(app), tenancyadapters.NewPostgresTenantRepository(app))
	h := inboxadapters.NewInboxAPIHandler(app)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/timeline", tenancyadapters.AuthorizationMiddleware(app, authz)(http.HandlerFunc(h.ListTimeline)))
	get := func(user, tenant, conversation uuid.UUID) (int, string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/"+tenant.String()+"/inbox/conversations/"+conversation.String()+"/timeline", nil)
		req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	code, body := get(ana, tenantA, conv)
	if code != 200 {
		t.Fatalf("status %d: %s", code, body)
	}
	var out struct {
		Items []inboxadapters.TimelineEvent `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	type line struct{ kind, actor, actorID, target, protocol, detail string }
	var got []line
	for _, e := range out.Items {
		got = append(got, line{e.Kind, e.Actor, e.ActorID, e.Target, e.Protocol, e.Detail})
	}
	// Ana (an agent) reads: she may read her own name but not a colleague's (membership.read), so Beto is told by id alone and the screen picks the words
	want := []line{
		{"auto_assigned", "", "", "Ana Atendente", "", ""},
		{"sent_to_queue", "Ana Atendente", ana.String(), "", "", ""},
		{"claimed", "", beto.String(), "", "", ""},
		{"protocol_opened", "", beto.String(), "", "KTG-1", ""},
		{"external_claimed", "", "", "", "", ""},
		{"external_released", "", "", "", "", ""},
		{"protocol_closed", "", "", "", "KTG-1", ""},
		{"protocol_opened", "", "", "", "KTG-2", ""},
		{"finalized", "", beto.String(), "", "KTG-2", "resolved"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	// oldest first, the platform's own work marked as such, and the finalization's protocol closing not told twice
	for i := 1; i < len(out.Items); i++ {
		if out.Items[i].At.Before(out.Items[i-1].At) {
			t.Errorf("event %d is older than the one before it", i)
		}
	}
	if !out.Items[0].System {
		t.Error("the distribution by the platform should be marked system")
	}
	// nothing private leaves: no note, no summary, no unverified agent the application claims to speak for, no email
	for _, secret := range []string{"NOTA-PRIVADA", "RESUMO-PRIVADO", "agente-secreto-do-crm", "@invalid", "between people"} {
		if strings.Contains(body, secret) {
			t.Errorf("the timeline leaked %q: %s", secret, body)
		}
	}

	// another company: its member is refused on this company's address, and this company's address does not find its conversation
	if code, _ := get(userB, tenantA, conv); code != http.StatusNotFound {
		t.Errorf("a member of another company read this timeline: %d, want 404", code)
	}
	if code, _ := get(ana, tenantA, foreign); code != http.StatusNotFound {
		t.Errorf("another company's conversation under this company: %d, want 404", code)
	}
	if code, _ := get(ana, tenantA, uuid.New()); code != http.StatusNotFound {
		t.Errorf("an unknown conversation: %d, want 404", code)
	}
}
