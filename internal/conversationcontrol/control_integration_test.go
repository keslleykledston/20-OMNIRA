package conversationcontrol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	attendanceadapters "github.com/omnira/omnira/internal/attendance/adapters"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	cc "github.com/omnira/omnira/internal/conversationcontrol"
	flowsadapters "github.com/omnira/omnira/internal/flows/adapters"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesports "github.com/omnira/omnira/internal/messages/ports"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
	routingapp "github.com/omnira/omnira/internal/routing/application"
	routingengine "github.com/omnira/omnira/internal/routing/engine"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

// ADR-0046 H2: ONE typed control of the conversation. Real PostgreSQL as omnira_app (FORCE RLS, signed session context).

type env struct {
	t    *testing.T
	ctx  context.Context
	seed *pgxpool.Pool
	app  *pgxpool.Pool
	n    int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(seed.Close)
	cfg, err := pgxpool.ParseConfig(appURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 64 // the 100-competitor race needs the connections
	app, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return &env{t: t, ctx: ctx, seed: seed, app: app}
}

func (e *env) exec(q string, a ...any) {
	e.t.Helper()
	if _, err := e.seed.Exec(e.ctx, q, a...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) tenant() uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO tenants(id,legal_name,status) VALUES($1,$2,'active')`, id, "T-"+id.String())
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, id) })
	return id
}

func (e *env) user(tenant uuid.UUID, roleKey string) uuid.UUID {
	u := uuid.New()
	e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u.String(), u.String()+"@invalid")
	var role uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL`, roleKey).Scan(&role); err != nil {
		e.t.Fatal(err)
	}
	var membership uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `INSERT INTO memberships(tenant_id,user_id,role_id,status) VALUES($1,$2,$3,'active') RETURNING id`, tenant, u, role).Scan(&membership); err != nil {
		e.t.Fatal(err)
	}
	e.exec(`INSERT INTO agent_profiles(tenant_id,membership_id,status) VALUES($1,$2,'active')`, tenant, membership)
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	return u
}

func (e *env) queue(tenant uuid.UUID, mode string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO queues(id,tenant_id,name,mode) VALUES($1,$2,$3,$4)`, id, tenant, "q-"+id.String()[:8], mode)
	return id
}

func (e *env) member(tenant, queue, user uuid.UUID) {
	e.exec(`INSERT INTO queue_members(tenant_id,queue_id,user_id,active,available,capacity) VALUES($1,$2,$3,true,true,5)`, tenant, queue, user)
}

func (e *env) installation(tenant uuid.UUID) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO external_app_installations(id,tenant_id,name,scopes) VALUES($1,$2,$3,ARRAY['conversation.claim'])`, id, tenant, "crm-"+id.String()[:8])
	return id
}

func (e *env) connection(tenant uuid.UUID) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES($1,$2,'whatsapp','waha','unofficial',$3,'active','["text"]'::jsonb)`,
		id, tenant, "n-"+id.String())
	return id
}

// conversation seeds a contact and an open conversation (optionally in a queue and on a line).
func (e *env) conversation(tenant uuid.UUID, queue, conn *uuid.UUID) uuid.UUID {
	e.n++
	contact, conv := uuid.New(), uuid.New()
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'C',$3)`, contact, tenant, fmt.Sprintf("+5592955%05d", e.n))
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,queue_id,channel_connection_id,status) VALUES($1,$2,$3,$4,$5,'open')`, conv, tenant, contact, queue, conn)
	return conv
}

type ctrl struct {
	Kind     *string
	Holder   *uuid.UUID
	Version  int64
	Origin   *string
	Assigned *uuid.UUID
	Status   string
}

func (e *env) control(conv uuid.UUID) ctrl {
	e.t.Helper()
	var c ctrl
	if err := e.seed.QueryRow(e.ctx, `SELECT control_kind, control_holder_id, control_version, control_origin, assigned_to_user_id, status FROM conversations WHERE id=$1`, conv).
		Scan(&c.Kind, &c.Holder, &c.Version, &c.Origin, &c.Assigned, &c.Status); err != nil {
		e.t.Fatal(err)
	}
	return c
}

func (e *env) sys(tenant uuid.UUID, fn func(ctx context.Context, q platformdb.Querier) error) error {
	return platformdb.WithSystemTenantSession(e.ctx, e.app, tenant, func(ctx context.Context) error {
		return fn(ctx, platformdb.QuerierFromContext(ctx, e.app))
	})
}

func (e *env) human(tenant, user uuid.UUID, fn func(ctx context.Context) error) error {
	return platformdb.WithTenantSession(e.ctx, e.app, user, false, func(ctx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, user, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		return fn(tenancydomain.WithTenantContext(ctx, tc))
	})
}

// sysCap is a system session that has asked for the capability of the control service, like the service itself does before a change of hold.
func (e *env) sysCap(tenant uuid.UUID, fn func(ctx context.Context, q platformdb.Querier) error) error {
	return e.sys(tenant, func(ctx context.Context, q platformdb.Querier) error {
		if _, err := q.Exec(ctx, `SELECT conversation_control_begin($1)`, tenant); err != nil {
			return err
		}
		return fn(ctx, q)
	})
}

func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

var svc = cc.Service{}

func actor(inst uuid.UUID) cc.Actor { return cc.Actor{Kind: cc.ActorCRMApplication, ID: inst} }

func (e *env) claim(tenant, conv, inst uuid.UUID, ifMatch *int64) (cc.State, bool, error) {
	var st cc.State
	var changed bool
	err := e.sys(tenant, func(ctx context.Context, q platformdb.Querier) error {
		var err error
		st, changed, err = svc.ClaimByApplication(ctx, q, tenant, conv, inst, ifMatch, actor(inst))
		return err
	})
	return st, changed, err
}

// ---------------------------------------------------------------------------------------------------------------------------------

// The guard derives the control for the legacy writers, validates the canonical ones and FAILS LOUD when a legacy writer meets an application's hold.
func TestGuardDerivesForLegacyWritersAndRaisesOverAnExternalHold(t *testing.T) {
	e := newEnv(t)
	tn := e.tenant()
	u1, u2 := e.user(tn, "tenant_agent"), e.user(tn, "tenant_agent")
	inst := e.installation(tn)
	conv := e.conversation(tn, nil, nil)

	legacy := func(to *uuid.UUID, extra string) error {
		return e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
			_, err := q.Exec(ctx, `UPDATE conversations SET assigned_to_user_id=$3, assigned_at=now(), updated_at=now()`+extra+` WHERE tenant_id=$1 AND id=$2`, tn, conv, to)
			return err
		})
	}
	if c := e.control(conv); c.Kind != nil || c.Version != 0 {
		t.Fatalf("a new conversation is free at version 0: %+v", c)
	}
	// the legacy human writer: control derived, version advances
	if err := legacy(&u1, ""); err != nil {
		t.Fatal(err)
	}
	if c := e.control(conv); c.Kind == nil || *c.Kind != "tenant_user" || *c.Holder != u1 || c.Version != 1 || *c.Origin != "workspace" {
		t.Fatalf("after a legacy assign: %+v", c)
	}
	if err := legacy(&u2, ""); err != nil {
		t.Fatal(err)
	}
	if c := e.control(conv); *c.Holder != u2 || c.Version != 2 || *c.Assigned != u2 {
		t.Fatalf("after a legacy reassign: %+v", c)
	}
	if err := legacy(nil, ""); err != nil {
		t.Fatal(err)
	}
	if c := e.control(conv); c.Kind != nil || c.Holder != nil || c.Version != 3 || c.Assigned != nil {
		t.Fatalf("after a legacy unassign: %+v", c)
	}
	// an unrelated update does not move the version
	e.exec(`UPDATE conversations SET title='x' WHERE id=$1`, conv)
	if c := e.control(conv); c.Version != 3 {
		t.Fatalf("an unrelated update advanced the version: %+v", c)
	}
	// the application claims (version 4)
	st, changed, err := e.claim(tn, conv, inst, nil)
	if err != nil || !changed || st.Version != 4 || !st.HeldBy(cc.KindCRMApplication, inst) || st.AssignedUserID != nil {
		t.Fatalf("claim = %+v %v %v", st, changed, err)
	}
	// a legacy writer over the hold FAILS LOUD (OM001) and loses the whole statement, including the other columns
	q := e.queue(tn, "manual")
	err = legacy(&u1, fmt.Sprintf(", queue_id='%s'", q))
	if pgCode(err) != "OM001" {
		t.Fatalf("legacy writer over an external hold = %v, want OM001", err)
	}
	if c := e.control(conv); c.Version != 4 || c.Assigned != nil || *c.Kind != "crm_application" {
		t.Fatalf("the hold changed: %+v", c)
	}
	var queue *uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT queue_id FROM conversations WHERE id=$1`, conv).Scan(&queue); err != nil || queue != nil {
		t.Fatalf("the rejected statement left a trace: queue=%v %v", queue, err)
	}
	// neither a Hub agent nor ANOTHER application can overwrite the hold through a canonical write either
	other2 := e.installation(tn)
	for _, w := range []struct {
		name string
		sql  string
		args []any
	}{
		{"a Hub agent", `UPDATE conversations SET assigned_to_user_id=$3, control_kind='hub_user', control_holder_id=$3, control_origin='hub', control_version=control_version+1, control_since=now() WHERE tenant_id=$1 AND id=$2`, []any{tn, conv, u1}},
		{"another application", `UPDATE conversations SET control_kind='crm_application', control_holder_id=$3, control_origin='crm', control_version=control_version+1, control_since=now() WHERE tenant_id=$1 AND id=$2`, []any{tn, conv, other2}},
	} {
		err = e.sysCap(tn, func(ctx context.Context, q platformdb.Querier) error { // even WITH the capability
			_, err := q.Exec(ctx, w.sql, w.args...)
			return err
		})
		if pgCode(err) != "OM001" {
			t.Fatalf("%s overwrote an application's hold: %v, want OM001", w.name, err)
		}
	}
	if c := e.control(conv); c.Version != 4 || *c.Holder != inst {
		t.Fatalf("the hold changed: %+v", c)
	}
	// a canonical write must advance the version by exactly one
	err = e.sysCap(tn, func(ctx context.Context, q platformdb.Querier) error {
		_, err := q.Exec(ctx, `UPDATE conversations SET control_kind=NULL, control_holder_id=NULL, control_since=NULL, control_origin=NULL WHERE tenant_id=$1 AND id=$2`, tn, conv)
		return err
	})
	if pgCode(err) != "OM002" {
		t.Fatalf("a change of holder without a version bump = %v, want OM002", err)
	}
	// the holder must exist in this company
	conv2 := e.conversation(tn, nil, nil)
	err = e.sysCap(tn, func(ctx context.Context, q platformdb.Querier) error {
		_, err := q.Exec(ctx, `UPDATE conversations SET control_kind='crm_application', control_holder_id=$3, control_version=control_version+1, control_since=now(), control_origin='crm' WHERE tenant_id=$1 AND id=$2`, tn, conv2, uuid.New())
		return err
	})
	if pgCode(err) != "OM003" {
		t.Fatalf("an application that does not exist = %v, want OM003", err)
	}
	// an installation of ANOTHER company cannot hold this company's conversation
	other := e.tenant()
	foreign := e.installation(other)
	err = e.sysCap(tn, func(ctx context.Context, q platformdb.Querier) error {
		_, err := q.Exec(ctx, `UPDATE conversations SET control_kind='crm_application', control_holder_id=$3, control_version=control_version+1, control_since=now(), control_origin='crm' WHERE tenant_id=$1 AND id=$2`, tn, conv2, foreign)
		return err
	})
	if pgCode(err) != "OM003" {
		t.Fatalf("another company's application = %v, want OM003", err)
	}
	// the shape: an application holder never coexists with a human assignee
	err = e.sysCap(tn, func(ctx context.Context, q platformdb.Querier) error {
		_, err := q.Exec(ctx, `UPDATE conversations SET assigned_to_user_id=$3, control_kind='crm_application', control_holder_id=$4, control_version=control_version+1, control_since=now(), control_origin='crm' WHERE tenant_id=$1 AND id=$2`, tn, conv2, u1, inst)
		return err
	})
	if pgCode(err) != "23514" {
		t.Fatalf("an application holder with a human assignee = %v, want a CHECK violation", err)
	}
	// insert with an assignee derives the control too
	conv3 := uuid.New()
	contact := uuid.New()
	e.exec(`INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES($1,$2,'C','+559295599999')`, contact, tn)
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status,assigned_to_user_id) VALUES($1,$2,$3,'open',$4)`, conv3, tn, contact, u1)
	if c := e.control(conv3); c.Kind == nil || *c.Holder != u1 || c.Version != 1 {
		t.Fatalf("insert with an assignee: %+v", c)
	}
}

// Idempotence, fencing and errors of the canonical operations.
func TestClaimReleaseIdempotencyFencingAndHistory(t *testing.T) {
	e := newEnv(t)
	tn := e.tenant()
	inst, inst2 := e.installation(tn), e.installation(tn)
	human := e.user(tn, "tenant_agent")
	rr, manual := e.queue(tn, "round_robin"), e.queue(tn, "manual")
	convRR, convManual := e.conversation(tn, &rr, nil), e.conversation(tn, &manual, nil)
	events := func(conv uuid.UUID) int {
		var n int
		_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM conversation_control_events WHERE conversation_id=$1`, conv).Scan(&n)
		return n
	}
	jobs := func(conv uuid.UUID) int {
		var n int
		_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM outbox_events WHERE event_type='job.routing.assign.v1' AND aggregate_id=$1`, conv.String()).Scan(&n)
		return n
	}

	st, changed, err := e.claim(tn, convRR, inst, nil)
	if err != nil || !changed || st.Version != 1 {
		t.Fatalf("claim = %+v %v %v", st, changed, err)
	}
	// idempotent for the same application: same version, no new history
	st, changed, err = e.claim(tn, convRR, inst, nil)
	if err != nil || changed || st.Version != 1 || events(convRR) != 1 {
		t.Fatalf("repeat = %+v %v %v events=%d", st, changed, err, events(convRR))
	}
	one, stale := int64(1), int64(7)
	if _, _, err := e.claim(tn, convRR, inst, &one); err != nil {
		t.Fatalf("repeat with the current version = %v", err)
	}
	if _, _, err := e.claim(tn, convRR, inst, &stale); !errors.Is(err, cc.ErrStaleVersion) {
		t.Fatalf("repeat with a stale version = %v, want ErrStaleVersion", err)
	}
	// another application, a person: ErrHeldByOther, nothing written
	if _, _, err := e.claim(tn, convRR, inst2, nil); !errors.Is(err, cc.ErrHeldByOther) {
		t.Fatalf("second application = %v", err)
	}
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, convManual, human)
	if _, _, err := e.claim(tn, convManual, inst, nil); !errors.Is(err, cc.ErrHeldByOther) {
		t.Fatalf("claim over a person = %v", err)
	}
	e.exec(`UPDATE conversations SET assigned_to_user_id=NULL WHERE id=$1`, convManual)
	// a stale ifMatch on a FREE conversation is refused and writes nothing
	if _, _, err := e.claim(tn, convManual, inst, &stale); !errors.Is(err, cc.ErrStaleVersion) {
		t.Fatalf("claim of a free conversation with a stale version = %v", err)
	}
	if c := e.control(convManual); c.Kind != nil {
		t.Fatalf("a refused claim held the conversation: %+v", c)
	}
	// a closed conversation cannot be claimed
	closed := e.conversation(tn, nil, nil)
	e.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, closed)
	if _, _, err := e.claim(tn, closed, inst, nil); !errors.Is(err, cc.ErrClosed) {
		t.Fatalf("closed = %v", err)
	}
	// unknown / other tenant's conversation is "not found"
	if _, _, err := e.claim(tn, uuid.New(), inst, nil); !errors.Is(err, cc.ErrNotFound) {
		t.Fatalf("unknown = %v", err)
	}
	foreign := e.tenant()
	fconv := e.conversation(foreign, nil, nil)
	if _, _, err := e.claim(tn, fconv, inst, nil); !errors.Is(err, cc.ErrNotFound) {
		t.Fatalf("another company's conversation = %v, want ErrNotFound", err)
	}

	rel := func(tenant, conv, who uuid.UUID, version int64) (cc.State, error) {
		var out cc.State
		err := e.sys(tenant, func(ctx context.Context, q platformdb.Querier) error {
			var err error
			out, err = svc.ReleaseByApplication(ctx, q, tenant, conv, who, version, actor(who))
			return err
		})
		return out, err
	}
	if _, err := rel(tn, convRR, inst, 99); !errors.Is(err, cc.ErrStaleVersion) {
		t.Fatalf("release with a stale version = %v", err)
	}
	if _, err := rel(tn, convRR, inst2, 1); !errors.Is(err, cc.ErrNotHolder) {
		t.Fatalf("release by who does not hold it = %v", err)
	}
	jobsBefore := jobs(convRR)
	out, err := rel(tn, convRR, inst, 1)
	if err != nil || !out.Free() || out.Version != 2 {
		t.Fatalf("release = %+v %v", out, err)
	}
	if events(convRR) != 2 {
		t.Fatalf("history rows = %d, want 2 (claim, release)", events(convRR))
	}
	if jobs(convRR) != jobsBefore+1 {
		t.Fatalf("a round-robin conversation must be offered to the router again: jobs %d -> %d", jobsBefore, jobs(convRR))
	}
	if _, err := rel(tn, convRR, inst, 2); !errors.Is(err, cc.ErrNotHolder) {
		t.Fatalf("second release = %v, want ErrNotHolder", err)
	}
	// a manual queue keeps the conversation visible for a person: no routing job
	if _, _, err := e.claim(tn, convManual, inst, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := rel(tn, convManual, inst, e.control(convManual).Version); err != nil {
		t.Fatal(err)
	}
	if jobs(convManual) != 0 {
		t.Fatalf("a manual queue got %d routing jobs", jobs(convManual))
	}
	// the history is typed: the actor is an application, never a user, and the version is unique per conversation
	var kind string
	var actorID uuid.UUID
	if err := e.seed.QueryRow(e.ctx, `SELECT actor_kind, actor_id FROM conversation_control_events WHERE conversation_id=$1 ORDER BY control_version LIMIT 1`, convRR).Scan(&kind, &actorID); err != nil || kind != "crm_application" || actorID != inst {
		t.Fatalf("history actor = %s %s (%v)", kind, actorID, err)
	}
	if _, err := e.seed.Exec(e.ctx, `UPDATE conversation_control_events SET reason='x' WHERE conversation_id=$1`, convRR); err != nil {
		t.Fatal("the owner may change it, the application role must not:", err)
	}
	err = e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
		_, err := q.Exec(ctx, `DELETE FROM conversation_control_events WHERE conversation_id=$1`, convRR)
		return err
	})
	if pgCode(err) != "42501" {
		t.Fatalf("the application role deleted history: %v", err)
	}
}

// Exactly one holder at the end of any race.
func TestExactlyOneHolderUnderCompetition(t *testing.T) {
	for _, n := range []int{2, 10, 100} {
		n := n
		t.Run(fmt.Sprintf("%d competitors", n), func(t *testing.T) {
			e := newEnv(t)
			tn := e.tenant()
			conv := e.conversation(tn, nil, nil)
			apps := make([]uuid.UUID, n)
			for i := range apps {
				apps[i] = e.installation(tn)
			}
			var humans []uuid.UUID
			if n >= 10 {
				for i := 0; i < n/2; i++ {
					humans = append(humans, e.user(tn, "tenant_agent"))
				}
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			var appWins, humanWins, appLost, humanLost atomic.Int32
			for _, a := range apps {
				a := a
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, changed, err := e.claim(tn, conv, a, nil)
					switch {
					case err == nil && changed:
						appWins.Add(1)
					case errors.Is(err, cc.ErrHeldByOther):
						appLost.Add(1)
					case err == nil:
						t.Errorf("a claim succeeded without changing anything")
					default:
						t.Errorf("claim: %v", err)
					}
				}()
			}
			for _, h := range humans { // the legacy writers: compare-and-set on "free", exactly like the engine and the round robin
				h := h
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					err := e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
						tag, err := q.Exec(ctx, `UPDATE conversations SET assigned_to_user_id=$3, assigned_at=now(), updated_at=now()
						                         WHERE tenant_id=$1 AND id=$2 AND assigned_to_user_id IS NULL AND control_kind IS NULL AND status='open'`, tn, conv, h)
						if err == nil && tag.RowsAffected() == 1 {
							humanWins.Add(1)
						} else if err == nil {
							humanLost.Add(1)
						}
						return err
					})
					if err != nil {
						t.Errorf("legacy writer: %v", err)
					}
				}()
			}
			close(start)
			wg.Wait()
			if wins := appWins.Load() + humanWins.Load(); wins != 1 {
				t.Fatalf("%d winners (apps %d, people %d), want exactly one", wins, appWins.Load(), humanWins.Load())
			}
			c := e.control(conv)
			if c.Version != 1 || c.Kind == nil {
				t.Fatalf("final control = %+v, want a single holder at version 1", c)
			}
			if appWins.Load() == 1 && (*c.Kind != "crm_application" || c.Assigned != nil) {
				t.Fatalf("an application won but the final state is %+v", c)
			}
			if humanWins.Load() == 1 && (*c.Kind != "tenant_user" || c.Assigned == nil || *c.Assigned != *c.Holder) {
				t.Fatalf("a person won but the final state is %+v", c)
			}
			if got := appLost.Load() + appWins.Load(); got != int32(n) {
				t.Fatalf("applications accounted for %d of %d", got, n)
			}
		})
	}
}

// No legacy writer takes, speaks over or routes a conversation an application holds; and each one works when it is free (the controls).
func TestNoLegacyPathStealsOrSpeaksOverAnApplicationHold(t *testing.T) {
	e := newEnv(t)
	tn := e.tenant()
	agent := e.user(tn, "tenant_agent")
	inst := e.installation(tn)
	rr := e.queue(tn, "round_robin")
	e.member(tn, rr, agent)
	conn := e.connection(tn)
	convFree := e.conversation(tn, &rr, &conn)
	convHeld := e.conversation(tn, &rr, &conn)
	if _, _, err := e.claim(tn, convHeld, inst, nil); err != nil {
		t.Fatal(err)
	}
	held0 := e.control(convHeld)
	assignmentEvents := func(conv uuid.UUID) int {
		var n int
		_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM assignment_events WHERE conversation_id=$1`, conv).Scan(&n)
		return n
	}

	// 1. the legacy round robin
	rrRepo := routingadapters.NewPostgresAssignmentRepository(e.app, nil)
	roundRobin := func(conv uuid.UUID) (uuid.UUID, bool, error) {
		var u uuid.UUID
		var ok bool
		err := e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
			var err error
			u, ok, err = rrRepo.AssignRoundRobin(ctx, conv, "round_robin")
			return err
		})
		return u, ok, err
	}
	if u, ok, err := roundRobin(convHeld); err != nil || ok || u != uuid.Nil {
		t.Fatalf("round robin over an application's hold = %v %v %v", u, ok, err)
	}
	if assignmentEvents(convHeld) != 0 {
		t.Fatal("round robin left an assignment event for a conversation it did not assign")
	}
	if u, ok, err := roundRobin(convFree); err != nil || !ok || u != agent {
		t.Fatalf("control: round robin on a free conversation = %v %v %v", u, ok, err)
	}

	// 2. the manual self-claim of the assignment repository
	claimOwn := func(conv uuid.UUID) (bool, error) {
		var ok bool
		err := e.human(tn, agent, func(ctx context.Context) error {
			var err error
			ok, err = rrRepo.ClaimUnassigned(ctx, conv, agent, "manual_claim")
			return err
		})
		return ok, err
	}
	if ok, err := claimOwn(convHeld); err != nil || ok {
		t.Fatalf("manual claim over an application's hold = %v %v", ok, err)
	}

	// 3. the manual assign / unassign of the Workspace routes, and the participant transfer
	auditRepo := auditadapters.NewPostgresAuditEventRepository(e.app)
	assigner := routingapp.NewAssigner(routingadapters.NewPostgresConversationAssigner(e.app), routingadapters.NewAuditRecorder(auditRepo))
	err := e.human(tn, agent, func(ctx context.Context) error {
		_, err := assigner.Assign(ctx, convHeld, uuid.Nil)
		return err
	})
	if !errors.Is(err, routingapp.ErrHeldByExternal) {
		t.Fatalf("Assign over an application's hold = %v, want ErrHeldByExternal", err)
	}
	err = e.human(tn, agent, func(ctx context.Context) error {
		_, err := assigner.Unassign(ctx, convHeld)
		return err
	})
	if !errors.Is(err, routingapp.ErrHeldByExternal) {
		t.Fatalf("Unassign over an application's hold = %v, want ErrHeldByExternal", err)
	}
	participants := routingapp.NewParticipantService(routingadapters.NewPostgresParticipantRepository(e.app), routingadapters.NewPostgresConversationAssigner(e.app), routingadapters.NewAuditRecorder(auditRepo))
	sup := e.user(tn, "tenant_supervisor")
	err = e.human(tn, sup, func(ctx context.Context) error {
		_, err := participants.Transfer(ctx, convHeld, agent)
		return err
	})
	if !errors.Is(err, routingapp.ErrHeldByExternal) {
		t.Fatalf("Transfer over an application's hold = %v, want ErrHeldByExternal", err)
	}

	// 4. the central routing engine
	eng := routingengine.New(e.app, nil)
	decide := func(conv uuid.UUID) (routingengine.Decision, error) {
		var d routingengine.Decision
		err := e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
			var err error
			d, err = eng.Decide(ctx, routingengine.Request{ConversationID: conv, Trigger: routingengine.TriggerRoute})
			return err
		})
		return d, err
	}
	d, err := decide(convHeld)
	if err != nil || d.Kind != routingengine.NoAction || d.Reason != routingengine.ReasonHeldExternally {
		t.Fatalf("engine over an application's hold = %+v %v", d, err)
	}

	// 5. the bot (system sender) and a person's reply
	store := messagesadapters.NewPostgresOutboundStore(e.app)
	sc := messagesports.SendContext{ConversationID: convHeld, ConnectionID: &conn}
	err = e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
		_, _, err := store.InsertQueuedSystem(ctx, sc, "olá do bot", "bot-key-0001", "h")
		return err
	})
	if !errors.Is(err, messagesports.ErrConversationChanged) {
		t.Fatalf("the bot over an application's hold = %v, want ErrConversationChanged", err)
	}
	scFree := messagesports.SendContext{ConversationID: convFree, ConnectionID: &conn}
	if err := e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
		_, _, err := store.InsertQueuedSystem(ctx, scFree, "olá do bot", "bot-key-0002", "h")
		return err
	}); err == nil {
		t.Fatal("control: the bot must not speak over the person the round robin just assigned either")
	}
	convBot := e.conversation(tn, &rr, &conn)
	if err := e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
		_, _, err := store.InsertQueuedSystem(ctx, messagesports.SendContext{ConversationID: convBot, ConnectionID: &conn}, "olá do bot", "bot-key-0003", "h")
		return err
	}); err != nil {
		t.Fatalf("control: the bot on a free conversation = %v", err)
	}
	err = e.human(tn, sup, func(ctx context.Context) error {
		_, _, err := store.InsertQueued(ctx, sup, sc, "resposta", "human-key-001", "h", false)
		return err
	})
	if !errors.Is(err, messagesports.ErrConversationChanged) {
		t.Fatalf("a person's reply into an application's hold = %v, want ErrConversationChanged", err)
	}
	var loaded *messagesports.SendContext
	err = e.human(tn, sup, func(ctx context.Context) error {
		var err error
		loaded, err = store.LoadSendContext(ctx, convHeld)
		return err
	})
	if err != nil || loaded == nil || !loaded.HeldExternally {
		t.Fatalf("LoadSendContext does not report the hold: %+v %v", loaded, err)
	}
	var msgs int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM messages WHERE conversation_id=$1`, convHeld).Scan(&msgs)
	if msgs != 0 {
		t.Fatalf("%d messages were written into a conversation an application holds", msgs)
	}

	// 6. nothing above changed the hold
	if c := e.control(convHeld); c.Version != held0.Version || *c.Kind != "crm_application" || c.Assigned != nil {
		t.Fatalf("the hold changed: before %+v after %+v", held0, c)
	}
}

// Suspending or revoking the installation frees what it holds in the same transaction and offers the conversations to the router.
func TestDrainFreesWhatTheInstallationHolds(t *testing.T) {
	e := newEnv(t)
	tn := e.tenant()
	inst, other := e.installation(tn), e.installation(tn)
	rr, manual := e.queue(tn, "round_robin"), e.queue(tn, "manual")
	a, b, c, mine := e.conversation(tn, &rr, nil), e.conversation(tn, &manual, nil), e.conversation(tn, &rr, nil), e.conversation(tn, &rr, nil)
	for _, conv := range []uuid.UUID{a, b, c} {
		if _, _, err := e.claim(tn, conv, inst, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := e.claim(tn, mine, other, nil); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, c) // a closed one, still held by the application
	var freed int
	err := e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
		var err error
		freed, err = svc.DrainInstallation(ctx, q, tn, inst, "revoke")
		return err
	})
	if err != nil || freed != 3 {
		t.Fatalf("drain = %d %v, want 3", freed, err)
	}
	for _, conv := range []uuid.UUID{a, b, c} {
		if ctl := e.control(conv); ctl.Kind != nil || ctl.Version != 2 {
			t.Fatalf("after the drain %s: %+v", conv, ctl)
		}
	}
	if ctl := e.control(mine); ctl.Kind == nil || *ctl.Holder != other {
		t.Fatalf("the drain touched another installation's conversation: %+v", ctl)
	}
	jobs := func(conv uuid.UUID) int {
		var n int
		_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM outbox_events WHERE event_type='job.routing.assign.v1' AND aggregate_id=$1`, conv.String()).Scan(&n)
		return n
	}
	if jobs(a) != 1 || jobs(b) != 0 || jobs(c) != 0 {
		t.Fatalf("routing jobs after the drain: open round-robin=%d manual=%d closed=%d, want 1/0/0", jobs(a), jobs(b), jobs(c))
	}
	var n int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM conversation_control_events WHERE conversation_id=$1 AND actor_kind='system' AND reason='installation revoke'`, a).Scan(&n)
	if n != 1 {
		t.Fatalf("drain history rows = %d", n)
	}
	// a second drain finds nothing
	err = e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
		var err error
		freed, err = svc.DrainInstallation(ctx, q, tn, inst, "revoke")
		return err
	})
	if err != nil || freed != 0 {
		t.Fatalf("second drain = %d %v", freed, err)
	}
	// another company cannot drain this one's installation
	foreign := e.tenant()
	err = e.sys(foreign, func(ctx context.Context, q platformdb.Querier) error {
		var err error
		freed, err = svc.DrainInstallation(ctx, q, foreign, other, "revoke")
		return err
	})
	if err != nil || freed != 0 {
		t.Fatalf("a drain scoped to another company freed %d (%v)", freed, err)
	}
	if ctl := e.control(mine); ctl.Kind == nil {
		t.Fatal("a drain through another company's tenant id freed this company's conversation")
	}
}

// Finalizing releases an application's hold (a closed conversation held by an application would be an orphan).
func TestFinalizingReleasesTheApplicationHold(t *testing.T) {
	e := newEnv(t)
	tn := e.tenant()
	inst := e.installation(tn)
	conv := e.conversation(tn, nil, nil)
	if _, _, err := e.claim(tn, conv, inst, nil); err != nil {
		t.Fatal(err)
	}
	repo := attendanceadapters.NewPostgresRepository(e.app)
	err := e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
		return repo.MarkClosed(ctx, conv)
	})
	if err != nil {
		t.Fatal(err)
	}
	if c := e.control(conv); c.Kind != nil || c.Status != "closed" || c.Version != 2 {
		t.Fatalf("after closing: %+v", c)
	}
	var n int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM conversation_control_events WHERE conversation_id=$1 AND reason='conversation_closed'`, conv).Scan(&n)
	if n != 1 {
		t.Fatalf("history rows for the close = %d", n)
	}
	// a PERSON's hold is not touched by a close (only an application's is released)
	person := e.user(tn, "tenant_agent")
	mine := e.conversation(tn, nil, nil)
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, mine, person)
	if err := e.sys(tn, func(ctx context.Context, q platformdb.Querier) error { return repo.MarkClosed(ctx, mine) }); err != nil {
		t.Fatal(err)
	}
	if c := e.control(mine); c.Kind == nil || *c.Kind != "tenant_user" || *c.Holder != person || c.Version != 1 || c.Status != "closed" {
		t.Fatalf("closing changed a person's hold: %+v", c)
	}
}

// The typed history is written ONLY by conversation_control_record, which needs the capability of the control service and records what the conversation is now.
func TestTheControlHistoryCannotBeForged(t *testing.T) {
	e := newEnv(t)
	tn := e.tenant()
	agent, sup := e.user(tn, "tenant_agent"), e.user(tn, "tenant_supervisor")
	inst := e.installation(tn)
	conv := e.conversation(tn, nil, nil)
	insert := `INSERT INTO conversation_control_events(tenant_id,conversation_id,control_version,actor_kind,reason) VALUES($1,$2,1,'tenant_user','forged')`
	// nobody inserts directly: not an agent, not a supervisor, not even a system session
	for name, run := range map[string]func() error{
		"an agent": func() error {
			return e.human(tn, agent, func(ctx context.Context) error {
				_, err := platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, insert, tn, conv)
				return err
			})
		},
		"a supervisor": func() error {
			return e.human(tn, sup, func(ctx context.Context) error {
				_, err := platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, insert, tn, conv)
				return err
			})
		},
		"a system session": func() error {
			return e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
				_, err := q.Exec(ctx, insert, tn, conv)
				return err
			})
		},
	} {
		if err := run(); pgCode(err) != "42501" {
			t.Errorf("%s inserted into the history: %v, want permission denied", name, err)
		}
	}
	record := `SELECT conversation_control_record($1,$2,NULL,NULL,'tenant_user',NULL,'forged',NULL,NULL)`
	// the function refuses a writer without the capability
	for name, run := range map[string]func() error{
		"an agent": func() error {
			return e.human(tn, agent, func(ctx context.Context) error {
				_, err := platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, record, tn, conv)
				return err
			})
		},
		"a supervisor": func() error {
			return e.human(tn, sup, func(ctx context.Context) error {
				_, err := platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, record, tn, conv)
				return err
			})
		},
		"a system session without begin": func() error {
			return e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
				_, err := q.Exec(ctx, record, tn, conv)
				return err
			})
		},
	} {
		// (a system session is not a superuser and has not asked: refused)
		if err := run(); pgCode(err) != "OM004" {
			t.Errorf("%s wrote the history without the capability: %v, want OM004", name, err)
		}
	}
	// with the capability (the claim asks for it) it writes what the conversation IS: the version and the holder come from the row, not from the caller
	if _, _, err := e.claim(tn, conv, inst, nil); err != nil {
		t.Fatal(err)
	}
	var version int64
	var toKind, actorKind string
	if err := e.seed.QueryRow(e.ctx, `SELECT control_version, to_kind, actor_kind FROM conversation_control_events WHERE conversation_id=$1 AND reason='crm_claim'`, conv).Scan(&version, &toKind, &actorKind); err != nil || version != 1 || toKind != "crm_application" || actorKind != "crm_application" {
		t.Fatalf("the recorded state is not the real one: v%d %s %s (%v)", version, toKind, actorKind, err)
	}
	// a second row for the same version is refused (one description per real change)
	err := e.sysCap(tn, func(ctx context.Context, q platformdb.Querier) error {
		_, err := q.Exec(ctx, `SELECT conversation_control_record($1,$2,NULL,NULL,'crm_application',$3,'a note',NULL,NULL)`, tn, conv, inst)
		return err
	})
	if pgCode(err) != "23505" {
		t.Fatalf("a second history row for one version = %v, want a unique violation", err)
	}
}

// The guard is a boundary: only a transaction that obtained the capability (a system session, or a person holding conversation.takeover_external) changes,
// gives or ends the hold of an external application. An agent with arbitrary SQL cannot release, steal or forge one.
func TestOnlyTheControlServiceChangesAnExternalHold(t *testing.T) {
	e := newEnv(t)
	tn, other := e.tenant(), e.tenant()
	agent, sup := e.user(tn, "tenant_agent"), e.user(tn, "tenant_supervisor")
	inst := e.installation(tn)
	held, free := e.conversation(tn, nil, nil), e.conversation(tn, nil, nil)
	if _, _, err := e.claim(tn, held, inst, nil); err != nil {
		t.Fatal(err)
	}
	want := e.control(held)

	type attempt struct {
		sql  string
		args []any
	}
	attempts := map[string]attempt{
		"release it":                 {`UPDATE conversations SET control_kind=NULL, control_holder_id=NULL, control_since=NULL, control_origin=NULL, control_version=control_version+1 WHERE id=$1`, []any{held}},
		"hand it to a person":        {`UPDATE conversations SET assigned_to_user_id=$2, control_kind='tenant_user', control_holder_id=$2, control_origin='workspace', control_version=control_version+1 WHERE id=$1`, []any{held, agent}},
		"only bump the version":      {`UPDATE conversations SET control_version=control_version+9 WHERE id=$1`, []any{held}},
		"rewrite where it came from": {`UPDATE conversations SET control_origin='system' WHERE id=$1`, []any{held}},
		"assign it the old way":      {`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, []any{held, agent}},
	}
	forge := attempt{`UPDATE conversations SET control_kind='crm_application', control_holder_id=$2, control_origin='crm', control_since=now(), control_version=control_version+1 WHERE id=$1`, []any{free, inst}}
	for who, run := range map[string]func(a attempt) error{
		"an agent": func(a attempt) error {
			return e.human(tn, agent, func(ctx context.Context) error {
				_, err := platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, a.sql, a.args...)
				return err
			})
		},
		"a supervisor (raw SQL, without the capability)": func(a attempt) error {
			return e.human(tn, sup, func(ctx context.Context) error {
				_, err := platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, a.sql, a.args...)
				return err
			})
		},
		"a system session (without the capability)": func(a attempt) error {
			return e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
				_, err := q.Exec(ctx, a.sql, a.args...)
				return err
			})
		},
	} {
		for what, a := range attempts {
			if err := run(a); pgCode(err) != "OM001" {
				t.Errorf("%s: %s = %v, want OM001", who, what, err)
			}
		}
		if err := run(forge); pgCode(err) != "OM001" { // forging a hold on a FREE conversation
			t.Errorf("%s: forging a hold = %v, want OM001", who, err)
		}
	}
	if got := e.control(held); got.Version != want.Version || *got.Kind != "crm_application" || *got.Holder != inst {
		t.Fatalf("the hold changed: before %+v after %+v", want, got)
	}
	if got := e.control(free); got.Kind != nil {
		t.Fatalf("a forged hold stuck: %+v", got)
	}
	// forging at INSERT
	err := e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
		_, err := q.Exec(ctx, `INSERT INTO conversations(id,tenant_id,contact_id,status,control_kind,control_holder_id,control_since,control_origin,control_version)
		                       SELECT $1,$2,contact_id,'open','crm_application',$3,now(),'crm',1 FROM conversations WHERE id=$4`, uuid.New(), tn, inst, free)
		return err
	})
	if pgCode(err) != "OM001" {
		t.Errorf("inserting a conversation already held by an application = %v, want OM001", err)
	}

	// who can get the capability
	if err := e.human(tn, agent, func(ctx context.Context) error {
		_, err := platformdb.QuerierFromContext(ctx, e.app).Exec(ctx, `SELECT conversation_control_begin($1)`, tn)
		return err
	}); pgCode(err) != "OM004" {
		t.Errorf("an agent obtained the capability: %v, want OM004", err)
	}
	// a supervisor who asks (that is what the takeover does) may; the SAME change then goes through
	if err := e.human(tn, sup, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, e.app)
		if _, err := q.Exec(ctx, `SELECT conversation_control_begin($1)`, tn); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `UPDATE conversations SET control_kind=NULL, control_holder_id=NULL, control_since=NULL, control_origin=NULL, control_version=control_version+1 WHERE id=$1`, held)
		return err
	}); err != nil {
		t.Errorf("a supervisor with the capability could not release: %v", err)
	}
	if got := e.control(held); got.Kind != nil || got.Version != want.Version+1 {
		t.Fatalf("after the supervisor's capability-backed release: %+v", got)
	}

	// the capability ends with the transaction (the same backend, the next transaction has none) and belongs to ONE company
	cfg, err := pgxpool.ParseConfig(os.Getenv("OMNIRA_APP_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	one, err := pgxpool.NewWithConfig(e.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	if _, _, err := e.claim(tn, held, inst, nil); err != nil {
		t.Fatal(err)
	}
	if err := platformdb.WithSystemTenantSession(e.ctx, one, tn, func(ctx context.Context) error {
		_, err := platformdb.QuerierFromContext(ctx, one).Exec(ctx, `SELECT conversation_control_begin($1)`, tn)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err = platformdb.WithSystemTenantSession(e.ctx, one, tn, func(ctx context.Context) error { // same backend, next transaction
		_, err := platformdb.QuerierFromContext(ctx, one).Exec(ctx, `UPDATE conversations SET control_kind=NULL, control_holder_id=NULL, control_since=NULL, control_origin=NULL, control_version=control_version+1 WHERE id=$1`, held)
		return err
	})
	if pgCode(err) != "OM001" {
		t.Errorf("the capability of a finished transaction still worked: %v", err)
	}
	otherInst := e.installation(other)
	otherConv := e.conversation(other, nil, nil)
	if _, _, err := e.claim(other, otherConv, otherInst, nil); err != nil {
		t.Fatal(err)
	}
	err = e.sys(tn, func(ctx context.Context, q platformdb.Querier) error { // a capability for tenant tn does not open tenant other
		if _, err := q.Exec(ctx, `SELECT conversation_control_begin($1)`, tn); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `UPDATE conversations SET control_kind=NULL, control_holder_id=NULL, control_since=NULL, control_origin=NULL, control_version=control_version+1 WHERE id=$1`, otherConv)
		return err
	})
	if pgCode(err) != "OM001" {
		t.Errorf("the capability of one company released another company's hold: %v", err)
	}
}

// The flow's queue / hand-off effects do not touch a conversation an application holds, and keep working for a free one.
func TestFlowEffectsLeaveAnExternalHoldAlone(t *testing.T) {
	e := newEnv(t)
	tn := e.tenant()
	inst := e.installation(tn)
	q1 := e.queue(tn, "manual")
	held, free := e.conversation(tn, nil, nil), e.conversation(tn, nil, nil)
	if _, _, err := e.claim(tn, held, inst, nil); err != nil {
		t.Fatal(err)
	}
	fx := flowsadapters.NewPostgresEffects(e.app, nil)
	state := func(conv uuid.UUID) (queue *uuid.UUID, mode string) {
		if err := e.seed.QueryRow(e.ctx, `SELECT queue_id, automation_mode FROM conversations WHERE id=$1`, conv).Scan(&queue, &mode); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := e.sys(tn, func(ctx context.Context, q platformdb.Querier) error { return fx.Handoff(ctx, held, &q1) }); err != nil {
		t.Fatalf("handoff over a hold: %v", err)
	}
	if queue, mode := state(held); queue != nil || mode == "waiting_human" {
		t.Fatalf("a flow moved a conversation an application holds: queue=%v mode=%s", queue, mode)
	}
	if err := e.sys(tn, func(ctx context.Context, q platformdb.Querier) error { return fx.Handoff(ctx, free, &q1) }); err != nil {
		t.Fatalf("control: handoff of a free conversation: %v", err)
	}
	if queue, mode := state(free); queue == nil || *queue != q1 || mode != "waiting_human" {
		t.Fatalf("control: a free conversation must be queued and handed off: queue=%v mode=%s", queue, mode)
	}
	// a queue that does not exist is still an error (not hidden by the hold)
	if err := e.sys(tn, func(ctx context.Context, q platformdb.Querier) error {
		missing := uuid.New()
		return fx.AssignQueue(ctx, held, &missing)
	}); err == nil {
		t.Fatal("a missing queue must still be reported")
	}
}

// The supervisor's takeover: permissioned, explicit, with a reason; never silent.
func TestSupervisorTakeover(t *testing.T) {
	e := newEnv(t)
	tn := e.tenant()
	inst := e.installation(tn)
	sup, agent, admin := e.user(tn, "tenant_supervisor"), e.user(tn, "tenant_agent"), e.user(tn, "tenant_admin")
	conv, free, closed := e.conversation(tn, nil, nil), e.conversation(tn, nil, nil), e.conversation(tn, nil, nil)
	if _, _, err := e.claim(tn, conv, inst, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.claim(tn, closed, inst, nil); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, closed)

	authz := tenancyapplication.NewAuthorizationService(tenancyadapters.NewPostgresMembershipRepository(e.app), tenancyadapters.NewPostgresTenantRepository(e.app))
	h := cc.NewTakeoverHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app))
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/takeover", tenancyadapters.AuthorizationMiddleware(e.app, authz)(http.HandlerFunc(h.Takeover)))
	call := func(user uuid.UUID, conv uuid.UUID, body string) (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/"+tn.String()+"/inbox/conversations/"+conv.String()+"/takeover", bytes.NewReader([]byte(body)))
		req.Header.Set("X-Request-ID", uuid.NewString())
		req = req.WithContext(authn.WithPrincipal(req.Context(), &authn.Principal{UserID: user, Subject: user.String()}))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	good := `{"reason":"cliente pediu atendimento humano","confirm":true}`
	if code, body := call(agent, conv, good); code != http.StatusForbidden {
		t.Errorf("an agent took it over: %d %s", code, body)
	}
	for name, body := range map[string]string{"no reason": `{"reason":"  ","confirm":true}`, "no confirmation": `{"reason":"x","confirm":false}`, "unknown field": `{"reason":"x","confirm":true,"tenant_id":"` + uuid.NewString() + `"}`, "empty": ``} {
		if code, _ := call(sup, conv, body); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
	if code, _ := call(sup, free, good); code != http.StatusConflict {
		t.Errorf("takeover of a free conversation = %d, want 409", code)
	}
	if code, _ := call(sup, closed, good); code != http.StatusConflict {
		t.Errorf("takeover of a finalized conversation = %d, want 409", code)
	}
	if code, _ := call(sup, uuid.New(), good); code != http.StatusNotFound {
		t.Errorf("takeover of an unknown conversation = %d, want 404", code)
	}
	if c := e.control(conv); c.Kind == nil || *c.Kind != "crm_application" {
		t.Fatalf("a refused takeover changed the hold: %+v", c)
	}
	code, body := call(sup, conv, good)
	if code != http.StatusOK {
		t.Fatalf("takeover = %d %s", code, body)
	}
	if c := e.control(conv); c.Kind == nil || *c.Kind != "tenant_user" || *c.Holder != sup || c.Assigned == nil || *c.Assigned != sup || c.Version != 2 || *c.Origin != "workspace" {
		t.Fatalf("after the takeover: %+v", c)
	}
	var reason, ak string
	if err := e.seed.QueryRow(e.ctx, `SELECT reason, actor_kind FROM conversation_control_events WHERE conversation_id=$1 ORDER BY control_version DESC LIMIT 1`, conv).Scan(&reason, &ak); err != nil || !strings.HasPrefix(reason, "takeover: ") || ak != "tenant_user" {
		t.Fatalf("history = %q %q (%v)", reason, ak, err)
	}
	var ae int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM assignment_events WHERE conversation_id=$1 AND reason='takeover_external' AND changed_by=$2`, conv, sup).Scan(&ae)
	if ae != 1 {
		t.Fatalf("assignment history rows = %d", ae)
	}
	var audits int
	_ = e.seed.QueryRow(e.ctx, `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action='conversation.takeover_external' AND actor_id=$2`, tn, sup).Scan(&audits)
	if audits != 1 {
		t.Fatalf("audit rows = %d", audits)
	}
	// the second call: it is a person's now
	if code, _ := call(admin, conv, good); code != http.StatusConflict {
		t.Errorf("taking over what a person already holds = %d, want 409", code)
	}
}

// A change of control reaches the realtime channel (000026 fired only on assigned_to_user_id / status).
func TestControlChangesAreAnnouncedOnTheRealtimeChannel(t *testing.T) {
	e := newEnv(t)
	tn := e.tenant()
	inst := e.installation(tn)
	conv := e.conversation(tn, nil, nil)
	listener, err := e.seed.Acquire(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Release()
	if _, err := listener.Exec(e.ctx, `LISTEN omnira_inbox_events`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.claim(tn, conv, inst, nil); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	for {
		n, err := listener.Conn().WaitForNotification(wctx)
		if err != nil {
			t.Fatalf("no realtime event for the claim: %v", err)
		}
		var p struct {
			ConversationID uuid.UUID `json:"conversation_id"`
			Data           struct {
				ControlKind    string `json:"control_kind"`
				ControlVersion int64  `json:"control_version"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(n.Payload), &p) != nil || p.ConversationID != conv {
			continue
		}
		if p.Data.ControlKind != "crm_application" || p.Data.ControlVersion != 1 {
			t.Fatalf("payload = %s", n.Payload)
		}
		break
	}
}

// The text of the backfill in migration 136 classifies existing holders: an active member is a tenant user, anyone else a Hub agent.
func TestMigrationBackfillClassifiesTheExistingHolders(t *testing.T) {
	e := newEnv(t)
	tn := e.tenant()
	member := e.user(tn, "tenant_agent")
	hub := uuid.New() // a person with no membership in this company (a Hub agent)
	e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, hub, hub.String(), hub.String()+"@invalid")
	t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, hub) })
	a, b, free := e.conversation(tn, nil, nil), e.conversation(tn, nil, nil), e.conversation(tn, nil, nil)
	// rows as they were BEFORE the migration: assigned, with no control (the guard is switched off just for this setup)
	e.exec(`ALTER TABLE conversations DISABLE TRIGGER conversations_control_guard_trg`)
	t.Cleanup(func() {
		_, _ = e.seed.Exec(context.Background(), `ALTER TABLE conversations ENABLE TRIGGER conversations_control_guard_trg`)
	})
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2, assigned_at=now() WHERE id=$1`, a, member)
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2, assigned_at=now() WHERE id=$1`, b, hub)
	dir := os.Getenv("OMNIRA_TEST_MIGRATIONS_DIR") // the mutation script points this at its mutated copy
	if dir == "" {
		dir = "../../migrations"
	}
	raw, err := os.ReadFile(filepath.Join(dir, "000136_conversation_control.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	start := strings.Index(sql, "UPDATE conversations c SET")
	end := strings.Index(sql, "ALTER TABLE conversations\n  ADD CONSTRAINT conversations_control_kind_check")
	if start < 0 || end < start {
		t.Fatal("the backfill statements were not found in the migration")
	}
	// the backfill touches every assigned conversation of the database, which is what the migration does; restrict the test to its own rows
	stmts := strings.ReplaceAll(sql[start:end], "WHERE c.assigned_to_user_id IS NOT NULL;", "WHERE c.assigned_to_user_id IS NOT NULL AND c.tenant_id = '"+tn.String()+"';")
	if strings.Count(stmts, "UPDATE ") != 1 {
		t.Fatalf("the backfill is expected to be ONE statement (the shape CHECK is added right after it), found %d", strings.Count(stmts, "UPDATE "))
	}
	e.exec(stmts)
	ca, cb, cf := e.control(a), e.control(b), e.control(free)
	if *ca.Kind != "tenant_user" || *ca.Origin != "workspace" || ca.Version != 1 || *ca.Holder != member {
		t.Errorf("a member's conversation: %+v", ca)
	}
	if *cb.Kind != "hub_user" || *cb.Origin != "hub" || cb.Version != 1 || *cb.Holder != hub {
		t.Errorf("a Hub agent's conversation: %+v", cb)
	}
	if cf.Kind != nil || cf.Version != 0 {
		t.Errorf("a free conversation was touched: %+v", cf)
	}
}

// Static guard: every SQL that decides "is this conversation free?" by assigned_to_user_id IS NULL also asks about the control. A new writer or
// reader that forgets it fails here instead of stealing a conversation from an application in production.
func TestEveryFreeCheckAsksAboutTheExternalControl(t *testing.T) {
	re := regexp.MustCompile(`assigned_to_user_id IS NULL`)
	var offenders []string
	for _, root := range []string{"../../internal", "../../apps"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			src := string(raw)
			for _, m := range re.FindAllStringIndex(src, -1) {
				lineStart := strings.LastIndex(src[:m[0]], "\n") + 1
				if strings.Contains(src[lineStart:m[0]], "//") || strings.Contains(src[lineStart:m[1]], "control:ok") {
					continue // prose in a comment, or an audited exception
				}
				open := strings.LastIndex(src[:m[0]], "`")
				close := strings.Index(src[m[1]:], "`")
				if open < 0 || close < 0 || !strings.Contains(src[open:m[1]+close], "control_kind") {
					line := strings.Count(src[:m[0]], "\n") + 1
					offenders = append(offenders, fmt.Sprintf("%s:%d", strings.TrimPrefix(path, "../../"), line))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("these statements treat assigned_to_user_id IS NULL as \"free\" without asking about control_kind (ADR-0046): %v", offenders)
	}
}

// pgx import is used by the notification test helper types; keep the compiler honest.
var _ = pgx.ErrNoRows
