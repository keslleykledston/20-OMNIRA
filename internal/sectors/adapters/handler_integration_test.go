package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/testhelpers"
)

// Real Postgres, runtime role under RLS, a real tenant session per call. Tenant A is the caller; tenant B owns data that must stay untouched.

type env struct {
	seed, app *pgxpool.Pool
	h         *Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	open := func(url string) *pgxpool.Pool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		if err := p.Ping(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	owner, app := testhelpers.RequireIntegrationDatabase(t)
	e := &env{seed: open(owner), app: open(app)}
	e.h = NewHandler(e.app, auditadapters.NewPostgresAuditEventRepository(e.app))
	return e
}

func (e *env) tenant(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO tenants (id, legal_name, isolation_profile, status) VALUES ($1,$2,'shared_strong_isolation','active')`, id, "sec-"+id.String()[:8]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM tenants WHERE id=$1`, id) })
	return id
}

func (e *env) member(t *testing.T, tenant uuid.UUID, role string) uuid.UUID {
	t.Helper()
	u := uuid.New()
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO users (id, external_subject, email, display_name, status) VALUES ($1,$2,$3,'m','active')`, u, u.String(), u.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	var roleID uuid.UUID
	if err := e.seed.QueryRow(context.Background(), `SELECT id FROM roles WHERE key=$1 AND tenant_id IS NULL LIMIT 1`, role).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO memberships (id, tenant_id, user_id, role_id, status) VALUES ($1,$2,$3,$4,'active')`, uuid.New(), tenant, u, roleID); err != nil {
		t.Fatal(err)
	}
	return u
}

func (e *env) queue(t *testing.T, tenant uuid.UUID, name string, def bool) uuid.UUID {
	t.Helper()
	id := uuid.New()
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO queues (id, tenant_id, name, mode, is_default) VALUES ($1,$2,$3,'manual',$4)`, id, tenant, name, def); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *env) call(t *testing.T, tenant, actor uuid.UUID, method, target string, fn http.HandlerFunc, path map[string]string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if s, ok := body.(string); ok {
		raw = []byte(s)
	} else if body != nil {
		raw, _ = json.Marshal(body)
	}
	var rec *httptest.ResponseRecorder
	err := platformdb.WithTenantSession(context.Background(), e.app, actor, false, func(sessionCtx context.Context) error {
		tc, err := tenancydomain.NewTenantContext(tenant, actor, tenancydomain.AccessSourceDirect)
		if err != nil {
			return err
		}
		req := httptest.NewRequestWithContext(tenancydomain.WithTenantContext(sessionCtx, tc), method, target, bytes.NewReader(raw))
		for k, v := range path {
			req.SetPathValue(k, v)
		}
		rec = httptest.NewRecorder()
		fn(rec, req)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	return v
}

func businessBody(name, tz string) map[string]any {
	days := []map[string]any{}
	for _, d := range []string{"mon", "tue", "wed", "thu", "fri"} {
		days = append(days, map[string]any{"day": d, "intervals": []map[string]string{{"start": "09:00", "end": "18:00"}}})
	}
	return map[string]any{"name": name, "definition": map[string]any{"kind": "business", "timezone": tz, "weekly": days}}
}

func at(t *testing.T, tz, local string) string {
	t.Helper()
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatal(err)
	}
	v, err := time.ParseInLocation("2006-01-02 15:04", local, loc)
	if err != nil {
		t.Fatal(err)
	}
	return v.UTC().Format(time.RFC3339)
}

func auditCount(t *testing.T, e *env, tenant uuid.UUID, action string) int {
	t.Helper()
	var n int
	if err := e.seed.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE tenant_id=$1 AND action=$2`, tenant, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSectorsCalendarsAndInheritedAvailability(t *testing.T) {
	e := newEnv(t)
	a := e.tenant(t)
	admin := e.member(t, a, "tenant_admin")
	const tz = "America/Manaus"
	q := e.queue(t, a, "Financeiro N1", false)
	other := e.queue(t, a, "Triagem", true)

	// no calendar anywhere: open, exactly the behaviour before sectors and calendars existed
	avail := func(queue uuid.UUID, when string) Availability {
		rec := e.call(t, a, admin, "GET", "/?at="+when, e.h.QueueAvailability, map[string]string{"queue_id": queue.String()}, nil)
		if rec.Code != 200 {
			t.Fatalf("availability = %d %s", rec.Code, rec.Body.String())
		}
		return decode[Availability](t, rec)
	}
	sat := at(t, tz, "2026-10-10 11:00")   // Saturday
	mon10 := at(t, tz, "2026-10-12 10:00") // Monday
	if v := avail(q, sat); !v.Open || v.CalendarSource != "none" {
		t.Fatalf("no calendar: %+v", v)
	}

	// create a sector and a business calendar; link calendar -> sector -> queue
	rec := e.call(t, a, admin, "POST", "/", e.h.CreateSector, nil, map[string]string{"name": "Financeiro"})
	if rec.Code != 201 {
		t.Fatalf("create sector = %d %s", rec.Code, rec.Body.String())
	}
	sector := decode[sectorItem](t, rec).ID
	if got := e.call(t, a, admin, "POST", "/", e.h.CreateSector, nil, map[string]string{"name": "Financeiro"}).Code; got != 409 {
		t.Fatalf("duplicate sector name = %d, want 409", got)
	}
	rec = e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, businessBody("Comercial", tz))
	if rec.Code != 201 {
		t.Fatalf("create calendar = %d %s", rec.Code, rec.Body.String())
	}
	cal := decode[Calendar](t, rec)
	if cal.Revision != 1 || len(cal.Spec.Weekly) != 5 {
		t.Fatalf("stored calendar: %+v", cal)
	}
	if got := e.call(t, a, admin, "PUT", "/", e.h.SetQueueSector, map[string]string{"queue_id": q.String()}, map[string]any{"sector_id": sector}).Code; got != 200 {
		t.Fatalf("link queue to sector = %d", got)
	}
	if got := e.call(t, a, admin, "PATCH", "/", e.h.UpdateSector, map[string]string{"sector_id": sector.String()}, map[string]any{"calendar_id": cal.ID}).Code; got != 200 {
		t.Fatalf("link sector to calendar = %d", got)
	}
	if v := avail(q, sat); v.Open || v.CalendarSource != "sector" || v.NextOpen == nil || !v.NextOpen.Equal(mustTime(t, at(t, tz, "2026-10-12 09:00"))) {
		t.Fatalf("Saturday: %+v", v) // Financeiro fora do expediente: closed, next open Monday 09:00
	}
	if v := avail(q, mon10); !v.Open || v.NextClose == nil || !v.NextClose.Equal(mustTime(t, at(t, tz, "2026-10-12 18:00"))) {
		t.Fatalf("Monday: %+v", v)
	}
	// a queue of another sector (here none) is unaffected; the tenant default applies when set, a queue override wins over the sector
	if v := avail(other, sat); !v.Open || v.CalendarSource != "none" {
		t.Fatalf("queue without sector must stay open: %+v", v)
	}
	rec = e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, map[string]any{"name": "24x7", "definition": map[string]any{"kind": "24x7", "timezone": tz}})
	always := decode[Calendar](t, rec).ID
	e.call(t, a, admin, "PUT", "/", e.h.SetDefaultCalendar, nil, map[string]any{"calendar_id": cal.ID})
	if v := avail(other, sat); v.Open || v.CalendarSource != "tenant" {
		t.Fatalf("tenant default: %+v", v)
	}
	e.call(t, a, admin, "PUT", "/", e.h.SetQueueCalendar, map[string]string{"queue_id": q.String()}, map[string]any{"calendar_id": always})
	if v := avail(q, sat); !v.Open || v.CalendarSource != "queue" {
		t.Fatalf("queue override must win: %+v", v)
	}
	e.call(t, a, admin, "PUT", "/", e.h.SetQueueCalendar, map[string]string{"queue_id": q.String()}, map[string]any{"calendar_id": nil})
	if v := avail(q, sat); v.Open || v.CalendarSource != "sector" {
		t.Fatalf("clearing the override returns to the sector: %+v", v)
	}

	// lists
	lst := decode[struct {
		Items []sectorItem `json:"items"`
	}](t, e.call(t, a, admin, "GET", "/", e.h.ListSectors, nil, nil))
	if len(lst.Items) != 1 || len(lst.Items[0].Queues) != 1 || lst.Items[0].Queues[0].ID != q || lst.Items[0].CalendarID == nil {
		t.Fatalf("sector list: %+v", lst.Items)
	}
	for _, act := range []string{"sector.created", "calendar.created", "queue.sector_changed", "queue.calendar_changed", "tenant.default_calendar_changed"} {
		if auditCount(t, e, a, act) == 0 {
			t.Errorf("no audit event %s", act)
		}
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCalendarEditsHolidaysSuspensionsAndRevision(t *testing.T) {
	e := newEnv(t)
	a := e.tenant(t)
	admin := e.member(t, a, "tenant_admin")
	const tz = "America/Sao_Paulo"
	q := e.queue(t, a, "Suporte N1", false)
	cal := decode[Calendar](t, e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, businessBody("Horário", tz)))
	e.call(t, a, admin, "PUT", "/", e.h.SetQueueCalendar, map[string]string{"queue_id": q.String()}, map[string]any{"calendar_id": cal.ID})
	open := func(when string) bool {
		rec := e.call(t, a, admin, "GET", "/?at="+at(t, tz, when), e.h.QueueAvailability, map[string]string{"queue_id": q.String()}, nil)
		return decode[Availability](t, rec).Open
	}
	if !open("2026-09-07 10:00") {
		t.Fatal("Monday 7 September is a normal day before the holiday exists")
	}
	// replace the definition: holiday + exception; the revision moves
	body := businessBody("Horário", tz)
	def := body["definition"].(map[string]any)
	def["holidays"] = []map[string]any{{"name": "Independência", "date": "2026-09-07"}}
	def["exceptions"] = []map[string]any{{"name": "Mutirão", "from": "2026-10-10", "to": "2026-10-10", "closed": false, "intervals": []map[string]string{{"start": "09:00", "end": "13:00"}}}}
	rec := e.call(t, a, admin, "PUT", "/", e.h.UpdateCalendar, map[string]string{"calendar_id": cal.ID.String()}, body)
	if rec.Code != 200 {
		t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
	}
	upd := decode[Calendar](t, rec)
	if upd.Revision != 2 || len(upd.Spec.Holidays) != 1 || len(upd.Spec.Exceptions) != 1 {
		t.Fatalf("after update: %+v", upd)
	}
	if open("2026-09-07 10:00") || !open("2026-10-10 10:00") || open("2026-10-10 14:00") {
		t.Fatal("holiday closes Monday; the exception opens Saturday morning only")
	}
	// emergency suspension: closes a normally open hour, bumps the revision, and removing it reopens
	sus := e.call(t, a, admin, "POST", "/", e.h.AddSuspension, map[string]string{"calendar_id": cal.ID.String()},
		map[string]any{"from": at(t, tz, "2026-10-13 10:00"), "to": at(t, tz, "2026-10-13 12:00"), "reason": "queda de energia"})
	if sus.Code != 201 {
		t.Fatalf("suspension = %d %s", sus.Code, sus.Body.String())
	}
	if open("2026-10-13 11:00") || !open("2026-10-13 12:00") || !open("2026-10-13 09:59") {
		t.Fatal("the suspension closes exactly its span")
	}
	if got := decode[Calendar](t, e.call(t, a, admin, "GET", "/", e.h.GetCalendar, map[string]string{"calendar_id": cal.ID.String()}, nil)).Revision; got != 3 {
		t.Fatalf("revision after suspension = %d, want 3", got)
	}
	sid := decode[Suspension](t, sus).ID
	if got := e.call(t, a, admin, "DELETE", "/", e.h.RemoveSuspension, map[string]string{"calendar_id": cal.ID.String(), "suspension_id": sid.String()}, nil).Code; got != 204 {
		t.Fatalf("remove suspension = %d", got)
	}
	if !open("2026-10-13 11:00") {
		t.Fatal("removing the suspension reopens")
	}
	if got := e.call(t, a, admin, "POST", "/", e.h.AddSuspension, map[string]string{"calendar_id": cal.ID.String()},
		map[string]any{"from": at(t, tz, "2026-10-13 12:00"), "to": at(t, tz, "2026-10-13 10:00")}).Code; got != 400 {
		t.Fatalf("reversed suspension = %d, want 400", got)
	}
	// in use -> 409; after unlinking -> 204
	if got := e.call(t, a, admin, "DELETE", "/", e.h.DeleteCalendar, map[string]string{"calendar_id": cal.ID.String()}, nil).Code; got != 409 {
		t.Fatalf("delete of a calendar in use = %d, want 409", got)
	}
	e.call(t, a, admin, "PUT", "/", e.h.SetQueueCalendar, map[string]string{"queue_id": q.String()}, map[string]any{"calendar_id": nil})
	if got := e.call(t, a, admin, "DELETE", "/", e.h.DeleteCalendar, map[string]string{"calendar_id": cal.ID.String()}, nil).Code; got != 204 {
		t.Fatalf("delete = %d, want 204", got)
	}
}

func TestSectorsAndCalendarsValidationPermissionsAndTenantIsolation(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(t), e.tenant(t)
	admin := e.member(t, a, "tenant_admin")
	supervisor := e.member(t, a, "tenant_supervisor")
	agent := e.member(t, a, "tenant_agent")
	adminB := e.member(t, b, "tenant_admin")
	qa := e.queue(t, a, "Fila A", false)
	calB := decode[Calendar](t, e.call(t, b, adminB, "POST", "/", e.h.CreateCalendar, nil, businessBody("Do B", "America/Manaus")))
	sectorB := decode[sectorItem](t, e.call(t, b, adminB, "POST", "/", e.h.CreateSector, nil, map[string]string{"name": "Setor do B"})).ID
	calA := decode[Calendar](t, e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, businessBody("Do A", "America/Manaus")))

	// permissions: a supervisor manages (agent.manage), an agent does not even read
	if got := e.call(t, a, supervisor, "POST", "/", e.h.CreateSector, nil, map[string]string{"name": "Do supervisor"}).Code; got != 201 {
		t.Fatalf("supervisor create = %d", got)
	}
	for name, c := range map[string]*httptest.ResponseRecorder{
		"create sector":  e.call(t, a, agent, "POST", "/", e.h.CreateSector, nil, map[string]string{"name": "X"}),
		"list sectors":   e.call(t, a, agent, "GET", "/", e.h.ListSectors, nil, nil),
		"create cal":     e.call(t, a, agent, "POST", "/", e.h.CreateCalendar, nil, businessBody("X", "America/Manaus")),
		"availability":   e.call(t, a, agent, "GET", "/", e.h.QueueAvailability, map[string]string{"queue_id": qa.String()}, nil),
		"set queue link": e.call(t, a, agent, "PUT", "/", e.h.SetQueueSector, map[string]string{"queue_id": qa.String()}, map[string]any{"sector_id": nil}),
	} {
		if c.Code != 403 {
			t.Errorf("%s as an agent = %d, want 403", name, c.Code)
		}
	}
	// isolation: another tenant's calendar / sector / queue is a plain 404 and nothing leaks
	for name, c := range map[string]*httptest.ResponseRecorder{
		"get B's calendar":      e.call(t, a, admin, "GET", "/", e.h.GetCalendar, map[string]string{"calendar_id": calB.ID.String()}, nil),
		"update B's calendar":   e.call(t, a, admin, "PUT", "/", e.h.UpdateCalendar, map[string]string{"calendar_id": calB.ID.String()}, businessBody("hack", "America/Manaus")),
		"delete B's calendar":   e.call(t, a, admin, "DELETE", "/", e.h.DeleteCalendar, map[string]string{"calendar_id": calB.ID.String()}, nil),
		"suspend B's calendar":  e.call(t, a, admin, "POST", "/", e.h.AddSuspension, map[string]string{"calendar_id": calB.ID.String()}, map[string]any{"from": time.Now(), "to": time.Now().Add(time.Hour)}),
		"B's sector on A queue": e.call(t, a, admin, "PUT", "/", e.h.SetQueueSector, map[string]string{"queue_id": qa.String()}, map[string]any{"sector_id": sectorB}),
		"B's calendar on queue": e.call(t, a, admin, "PUT", "/", e.h.SetQueueCalendar, map[string]string{"queue_id": qa.String()}, map[string]any{"calendar_id": calB.ID}),
		"B's calendar default":  e.call(t, a, admin, "PUT", "/", e.h.SetDefaultCalendar, nil, map[string]any{"calendar_id": calB.ID}),
		"update B's sector":     e.call(t, a, admin, "PATCH", "/", e.h.UpdateSector, map[string]string{"sector_id": sectorB.String()}, map[string]any{"name": "hack"}),
		"availability of B":     e.call(t, a, admin, "GET", "/", e.h.QueueAvailability, map[string]string{"queue_id": e.queue(t, b, "Fila B", false).String()}, nil),
	} {
		if c.Code != 404 {
			t.Errorf("%s = %d, want 404 (%s)", name, c.Code, c.Body.String())
		}
		if strings.Contains(c.Body.String(), "Do B") || strings.Contains(c.Body.String(), "Setor do B") {
			t.Errorf("%s leaks the other tenant's data", name)
		}
	}
	var name string
	if err := e.seed.QueryRow(context.Background(), `SELECT name FROM calendars WHERE id=$1`, calB.ID).Scan(&name); err != nil || name != "Do B" {
		t.Fatalf("B's calendar was touched: %q %v", name, err)
	}
	// the database itself refuses a cross-tenant reference (composite foreign key), even for the owner role
	if _, err := e.seed.Exec(context.Background(), `UPDATE queues SET calendar_id=$2 WHERE id=$1`, qa, calB.ID); err == nil {
		t.Error("the database must refuse a queue pointing at another tenant's calendar")
	}
	if _, err := e.seed.Exec(context.Background(), `UPDATE sectors SET calendar_id=$2 WHERE tenant_id=$1 AND name='Do supervisor'`, a, calB.ID); err == nil {
		t.Error("the database must refuse a sector pointing at another tenant's calendar")
	}
	// the same sector name can exist in another tenant
	if got := e.call(t, b, adminB, "POST", "/", e.h.CreateSector, nil, map[string]string{"name": "Do supervisor"}).Code; got != 201 {
		t.Fatalf("the same name in another tenant = %d, want 201", got)
	}
	// validation: every one of these must be refused with 400 and leave nothing behind
	bad := map[string]map[string]any{
		"unknown tz":    businessBody("v1", "Mars/Olympus"),
		"empty name":    businessBody(" ", "America/Manaus"),
		"overlap":       {"name": "v3", "definition": map[string]any{"kind": "custom", "timezone": "America/Manaus", "weekly": []map[string]any{{"day": "mon", "intervals": []map[string]string{{"start": "09:00", "end": "12:00"}, {"start": "11:00", "end": "13:00"}}}}}},
		"bad day":       {"name": "v4", "definition": map[string]any{"kind": "custom", "timezone": "America/Manaus", "weekly": []map[string]any{{"day": "funday", "intervals": []map[string]string{{"start": "09:00", "end": "12:00"}}}}}},
		"bad clock":     {"name": "v5", "definition": map[string]any{"kind": "custom", "timezone": "America/Manaus", "weekly": []map[string]any{{"day": "mon", "intervals": []map[string]string{{"start": "9h", "end": "12:00"}}}}}},
		"24x7 + weekly": {"name": "v6", "definition": map[string]any{"kind": "24x7", "timezone": "America/Manaus", "weekly": []map[string]any{{"day": "mon", "intervals": []map[string]string{{"start": "09:00", "end": "12:00"}}}}}},
		"no weekly":     {"name": "v7", "definition": map[string]any{"kind": "business", "timezone": "America/Manaus"}},
	}
	for name, body := range bad {
		if got := e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, body).Code; got != 400 {
			t.Errorf("%s = %d, want 400", name, got)
		}
	}
	if got := e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, businessBody("Do A", "America/Manaus")).Code; got != 409 {
		t.Errorf("duplicate calendar name = %d, want 409", got)
	}
	var n int
	if err := e.seed.QueryRow(context.Background(), `SELECT count(*) FROM calendars WHERE tenant_id=$1`, a).Scan(&n); err != nil || n != 1 {
		t.Fatalf("refused requests must leave nothing behind: %d calendars %v", n, err)
	}
	_ = calA
	// sectors are deactivated, never deleted: the stable id survives and the queue keeps its link
	sec := decode[sectorItem](t, e.call(t, a, admin, "POST", "/", e.h.CreateSector, nil, map[string]string{"name": "Comercial"})).ID
	e.call(t, a, admin, "PUT", "/", e.h.SetQueueSector, map[string]string{"queue_id": qa.String()}, map[string]any{"sector_id": sec})
	rec := e.call(t, a, admin, "PATCH", "/", e.h.UpdateSector, map[string]string{"sector_id": sec.String()}, map[string]any{"active": false, "name": "Vendas"})
	if rec.Code != 200 {
		t.Fatalf("deactivate+rename = %d %s", rec.Code, rec.Body.String())
	}
	var active bool
	var linked *uuid.UUID
	if err := e.seed.QueryRow(context.Background(), `SELECT s.active, q.sector_id FROM sectors s, queues q WHERE s.id=$1 AND q.id=$2`, sec, qa).Scan(&active, &linked); err != nil || active || linked == nil || *linked != sec {
		t.Fatalf("a deactivated sector keeps its id and its queue link: active=%v linked=%v err=%v", active, linked, err)
	}
}

// Migration 000124's data step: the standard groups get a same-named sector and are linked; Default is left alone; idempotent.
func TestMigrationBackfillCreatesSectorsForTheStandardGroups(t *testing.T) {
	e := newEnv(t)
	a := e.tenant(t)
	def := e.queue(t, a, "Default", true)
	sup := e.queue(t, a, "Suporte", false)
	custom := e.queue(t, a, "Servidores", false)
	raw, err := os.ReadFile("../../../migrations/000124_sectors_and_calendars.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	i := strings.Index(sql, "-- data:")
	if i < 0 {
		t.Fatal("the migration lost its data step")
	}
	ctx := context.Background()
	tx, err := e.seed.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for run := 0; run < 2; run++ {
		if _, err := tx.Exec(ctx, sql[i:]); err != nil {
			t.Fatalf("data step run %d: %v", run+1, err)
		}
	}
	var supSector, defSector, customSector *uuid.UUID
	var sectors int
	if err := tx.QueryRow(ctx, `SELECT (SELECT sector_id FROM queues WHERE id=$1), (SELECT sector_id FROM queues WHERE id=$2), (SELECT sector_id FROM queues WHERE id=$3),
		(SELECT count(*) FROM sectors WHERE tenant_id=$4)`, sup, def, custom, a).Scan(&supSector, &defSector, &customSector, &sectors); err != nil {
		t.Fatal(err)
	}
	if supSector == nil || defSector != nil || customSector != nil || sectors != 1 {
		t.Fatalf("Suporte must be linked to its sector, Default and custom groups untouched, exactly one sector: sup=%v def=%v custom=%v sectors=%d", supSector, defSector, customSector, sectors)
	}
}

// Codex review of phase 3: bounded bodies, honest answers when the row policy filters a write, optimistic concurrency, a refused
// request that changes nothing, and unique races that answer 409 instead of 500.
func TestHardeningBoundedBodiesStaleEditsHonestRefusalsAndRaces(t *testing.T) {
	e := newEnv(t)
	a := e.tenant(t)
	admin := e.member(t, a, "tenant_admin")
	supervisor := e.member(t, a, "tenant_supervisor")
	const tz = "America/Manaus"
	cal := decode[Calendar](t, e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, businessBody("Base", tz)))

	// a body over the limit is refused before it is materialized
	huge := `{"name":"x","definition":{"kind":"business","timezone":"` + tz + `","weekly":[],"holidays":[` + strings.Repeat(`{"date":"2026-01-01"},`, 20000) + `{"date":"2026-01-02"}]}}`
	if got := e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, huge).Code; got != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d, want 413", got)
	}
	// names longer than the columns are a 400, not a database error
	long := businessBody("Longo", tz)
	long["definition"].(map[string]any)["holidays"] = []map[string]any{{"name": strings.Repeat("x", 81), "date": "2026-09-07"}}
	if got := e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, long).Code; got != 400 {
		t.Errorf("holiday name of 81 characters = %d, want 400", got)
	}
	// the default calendar lives on the tenants row (admin only): a supervisor is refused, nothing is audited as done
	if got := e.call(t, a, supervisor, "PUT", "/", e.h.SetDefaultCalendar, nil, map[string]any{"calendar_id": cal.ID}).Code; got != 403 {
		t.Errorf("supervisor default calendar = %d, want 403", got)
	}
	if auditCount(t, e, a, "tenant.default_calendar_changed") != 0 {
		t.Error("a refused change must not be audited as done")
	}
	if got := e.call(t, a, admin, "PUT", "/", e.h.SetDefaultCalendar, nil, map[string]any{"calendar_id": cal.ID}).Code; got != 200 {
		t.Errorf("admin default calendar = %d, want 200", got)
	}
	// optimistic concurrency: the second editor who read revision 1 gets 409 and nothing is overwritten
	first := businessBody("Base", tz)
	first["expected_revision"] = 1
	if got := e.call(t, a, admin, "PUT", "/", e.h.UpdateCalendar, map[string]string{"calendar_id": cal.ID.String()}, first).Code; got != 200 {
		t.Fatalf("first edit = %d", got)
	}
	second := businessBody("Outro nome", tz)
	second["expected_revision"] = 1
	if got := e.call(t, a, admin, "PUT", "/", e.h.UpdateCalendar, map[string]string{"calendar_id": cal.ID.String()}, second).Code; got != 409 {
		t.Errorf("stale edit = %d, want 409", got)
	}
	if got := decode[Calendar](t, e.call(t, a, admin, "GET", "/", e.h.GetCalendar, map[string]string{"calendar_id": cal.ID.String()}, nil)); got.Name != "Base" || got.Revision != 2 {
		t.Errorf("the stale edit must change nothing: %+v", got)
	}
	// the suspension limit refuses without bumping the revision
	for i := 0; i < 200; i++ {
		if _, err := e.seed.Exec(context.Background(), `INSERT INTO calendar_suspensions (tenant_id, calendar_id, starts_at, ends_at) VALUES ($1,$2,now()+make_interval(hours => $3), now()+make_interval(hours => $3)+interval '30 minutes')`, a, cal.ID, i); err != nil {
			t.Fatal(err)
		}
	}
	if got := e.call(t, a, admin, "POST", "/", e.h.AddSuspension, map[string]string{"calendar_id": cal.ID.String()},
		map[string]any{"from": time.Now().Add(1000 * time.Hour), "to": time.Now().Add(1001 * time.Hour)}).Code; got != 409 {
		t.Errorf("201st suspension = %d, want 409", got)
	}
	if got := decode[Calendar](t, e.call(t, a, admin, "GET", "/", e.h.GetCalendar, map[string]string{"calendar_id": cal.ID.String()}, nil)).Revision; got != 2 {
		t.Errorf("a refused suspension moved the revision to %d", got)
	}
	// two requests racing for the same new name: one wins, the other gets 409 (never a 500)
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			codes <- e.call(t, a, admin, "POST", "/", e.h.CreateSector, nil, map[string]string{"name": "Corrida"}).Code
		}()
	}
	c1, c2 := <-codes, <-codes
	if !((c1 == 201 && c2 == 409) || (c1 == 409 && c2 == 201)) {
		t.Errorf("racing creates answered %d and %d, want one 201 and one 409", c1, c2)
	}
}

// A change that moves an opening re-plans the waiting conversations in the SAME request: lifting a suspension wakes them now, and a
// longer one pushes the timer out.
func TestCalendarChangesReplanTheTimersOfWaitingConversations(t *testing.T) {
	e := newEnv(t)
	a := e.tenant(t)
	admin := e.member(t, a, "tenant_admin")
	const tz = "America/Manaus"
	q := e.queue(t, a, "Financeiro N1", false)
	cal := decode[Calendar](t, e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, map[string]any{"name": "24x7", "definition": map[string]any{"kind": "24x7", "timezone": tz}})).ID
	e.call(t, a, admin, "PUT", "/", e.h.SetQueueCalendar, map[string]string{"queue_id": q.String()}, map[string]any{"calendar_id": cal})
	sus := decode[Suspension](t, e.call(t, a, admin, "POST", "/", e.h.AddSuspension, map[string]string{"calendar_id": cal.String()},
		map[string]any{"from": time.Now().Add(-time.Hour), "to": time.Now().Add(3 * time.Hour), "reason": "queda"}))
	contact, conv := uuid.New(), uuid.New()
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO contacts (id, tenant_id, display_name, phone_e164, status) VALUES ($1,$2,'C','+5511900007001','active')`, contact, a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO conversations (id, tenant_id, contact_id, queue_id, status) VALUES ($1,$2,$3,$4,'open')`, conv, a, contact, q); err != nil {
		t.Fatal(err)
	}
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO routing_engine_state (tenant_id, conversation_id, state, queue_id) VALUES ($1,$2,'waiting_open',$3)`, a, conv, q); err != nil {
		t.Fatal(err)
	}
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO routing_engine_timers (tenant_id, conversation_id, episode, kind, due_at) VALUES ($1,$2,1,'open_wakeup',now()+interval '3 hours')`, a, conv); err != nil {
		t.Fatal(err)
	}
	timer := func() (due time.Time, gen int, state string) {
		_ = e.seed.QueryRow(context.Background(), `SELECT due_at, generation, state FROM routing_engine_timers WHERE conversation_id=$1`, conv).Scan(&due, &gen, &state)
		return
	}
	// lifting the suspension: the sector is open now, so the timer is due now
	if got := e.call(t, a, admin, "DELETE", "/", e.h.RemoveSuspension, map[string]string{"calendar_id": cal.String(), "suspension_id": sus.ID.String()}, nil).Code; got != 204 {
		t.Fatalf("remove suspension = %d", got)
	}
	due, gen, state := timer()
	if time.Until(due) > 5*time.Second || gen != 2 || state != "pending" {
		t.Fatalf("lifted: due in %v gen=%d state=%s", time.Until(due), gen, state)
	}
	// a new, longer suspension pushes the timer to its end
	e.call(t, a, admin, "POST", "/", e.h.AddSuspension, map[string]string{"calendar_id": cal.String()},
		map[string]any{"from": time.Now().Add(-time.Hour), "to": time.Now().Add(8 * time.Hour)})
	due, gen, _ = timer()
	if time.Until(due) < 7*time.Hour || gen != 3 {
		t.Fatalf("extended: due in %v gen=%d", time.Until(due), gen)
	}
	// a calendar that never opens hands the conversation to a person and cancels the timer
	never := decode[Calendar](t, e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, map[string]any{"name": "Nunca", "definition": map[string]any{"kind": "custom", "timezone": tz, "weekly": []map[string]any{{"day": "mon", "intervals": []map[string]string{}}}}})).ID
	e.call(t, a, admin, "PUT", "/", e.h.SetQueueCalendar, map[string]string{"queue_id": q.String()}, map[string]any{"calendar_id": never})
	_, _, state = timer()
	var st string
	_ = e.seed.QueryRow(context.Background(), `SELECT state FROM routing_engine_state WHERE conversation_id=$1`, conv).Scan(&st)
	if state != "cancelled" || st != "manual_required" {
		t.Fatalf("never opens: timer=%s state=%s", state, st)
	}
}

func policyBody(steps ...map[string]any) map[string]any {
	return map[string]any{"active": true, "max_steps": 4, "on_exhausted": "contingency", "steps": steps}
}

func qstep(pos int, dest uuid.UUID, wait int, triggers ...string) map[string]any {
	return map[string]any{"position": pos, "triggers": triggers, "wait_seconds": wait, "destination_kind": "queue", "destination_id": dest}
}

func TestOverflowPolicyAPIValidationPermissionsRevisionAndIsolation(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(t), e.tenant(t)
	admin := e.member(t, a, "tenant_admin")
	supervisor := e.member(t, a, "tenant_supervisor")
	agent := e.member(t, a, "tenant_agent")
	adminB := e.member(t, b, "tenant_admin")
	n1, n2 := e.queue(t, a, "N1", false), e.queue(t, a, "N2", false)
	foreign := e.queue(t, b, "Do B", false)
	rosterA := decode[rosterItem](t, e.call(t, a, admin, "POST", "/", e.h.CreateRoster, nil, map[string]any{"name": "Plantão", "kind": "on_call"})).ID
	rosterB := decode[rosterItem](t, e.call(t, b, adminB, "POST", "/", e.h.CreateRoster, nil, map[string]any{"name": "Plantão B", "kind": "on_call"})).ID
	path := map[string]string{"queue_id": n1.String()}
	put := func(actor uuid.UUID, body map[string]any) *httptest.ResponseRecorder {
		return e.call(t, a, actor, "PUT", "/", e.h.PutOverflowPolicy, path, body)
	}
	good := policyBody(qstep(1, n2, 300, "no_eligible_agent", "capacity_exhausted"),
		map[string]any{"position": 2, "triggers": []string{"max_wait"}, "wait_seconds": 900, "destination_kind": "oncall", "destination_id": rosterA})

	// permissions
	if got := put(agent, good).Code; got != 403 {
		t.Fatalf("agent put = %d, want 403", got)
	}
	if got := e.call(t, a, agent, "GET", "/", e.h.GetOverflowPolicy, path, nil).Code; got != 403 {
		t.Fatalf("agent get = %d, want 403", got)
	}
	rec := put(supervisor, good)
	if rec.Code != 200 {
		t.Fatalf("supervisor put = %d %s", rec.Code, rec.Body.String())
	}
	got := decode[struct {
		Policy struct {
			Revision int `json:"revision"`
			Steps    []struct {
				Position int `json:"position"`
			} `json:"steps"`
		} `json:"policy"`
	}](t, rec)
	if got.Policy.Revision != 1 || len(got.Policy.Steps) != 2 {
		t.Fatalf("saved: %+v", got)
	}
	// revision: a stale editor gets 409 and nothing is lost
	again := policyBody(qstep(1, n2, 60, "no_eligible_agent"))
	again["expected_revision"] = 1
	if got := put(admin, again).Code; got != 200 {
		t.Fatalf("expected revision 1 = %d", got)
	}
	stale := policyBody(qstep(1, n2, 999, "no_eligible_agent"))
	stale["expected_revision"] = 1
	if got := put(admin, stale).Code; got != 409 {
		t.Fatalf("stale revision = %d, want 409", got)
	}
	var wait int
	if err := e.seed.QueryRow(context.Background(), `SELECT s.wait_seconds FROM overflow_steps s JOIN overflow_policies p ON p.id=s.policy_id WHERE p.queue_id=$1`, n1).Scan(&wait); err != nil || wait != 60 {
		t.Fatalf("the stale write changed nothing: wait=%d %v", wait, err)
	}
	// validation: each of these is a 400 and leaves the saved policy alone
	bad := map[string]map[string]any{
		"back to the origin":     policyBody(qstep(1, n1, 0, "no_eligible_agent")),
		"another company queue":  policyBody(qstep(1, foreign, 0, "no_eligible_agent")),
		"unknown queue":          policyBody(qstep(1, uuid.New(), 0, "no_eligible_agent")),
		"another company roster": policyBody(map[string]any{"position": 1, "triggers": []string{"max_wait"}, "wait_seconds": 60, "destination_kind": "oncall", "destination_id": rosterB}),
		"unknown trigger":        policyBody(qstep(1, n2, 0, "whenever")),
		"max_wait without wait":  policyBody(qstep(1, n2, 0, "max_wait")),
		"no steps":               policyBody(),
		"too many steps":         policyBody(qstep(1, n2, 0, "no_eligible_agent"), qstep(2, n2, 0, "no_eligible_agent")),
		"bad exhausted":          {"active": true, "max_steps": 4, "on_exhausted": "explode", "steps": []map[string]any{qstep(1, n2, 0, "no_eligible_agent")}},
	}
	for name, body := range bad {
		if got := put(admin, body).Code; got != 400 {
			t.Errorf("%s = %d, want 400", name, got)
		}
	}
	if e.call(t, a, admin, "PUT", "/", e.h.PutOverflowPolicy, map[string]string{"queue_id": uuid.NewString()}, good).Code != 404 {
		t.Error("a queue that does not exist is 404")
	}
	// a deactivated roster is not a valid destination
	e.call(t, a, admin, "PATCH", "/", e.h.UpdateRoster, map[string]string{"roster_id": rosterA.String()}, map[string]any{"active": false})
	if got := put(admin, good).Code; got != 400 {
		t.Errorf("an inactive roster as destination = %d, want 400", got)
	}
	// isolation: B cannot see or touch A's policy
	pathA := map[string]string{"queue_id": n1.String()}
	for name, c := range map[string]*httptest.ResponseRecorder{
		"B reads":   e.call(t, b, adminB, "GET", "/", e.h.GetOverflowPolicy, pathA, nil),
		"B writes":  e.call(t, b, adminB, "PUT", "/", e.h.PutOverflowPolicy, pathA, policyBody(qstep(1, foreign, 0, "no_eligible_agent"))),
		"B deletes": e.call(t, b, adminB, "DELETE", "/", e.h.DeleteOverflowPolicy, pathA, nil),
	} {
		if c.Code != 404 {
			t.Errorf("%s = %d, want 404", name, c.Code)
		}
	}
	// read, delete, read again
	read := e.call(t, a, admin, "GET", "/", e.h.GetOverflowPolicy, path, nil)
	if read.Code != 200 || !strings.Contains(read.Body.String(), `"revision":2`) {
		t.Fatalf("get = %d %s", read.Code, read.Body.String())
	}
	if got := e.call(t, a, admin, "DELETE", "/", e.h.DeleteOverflowPolicy, path, nil).Code; got != 204 {
		t.Fatalf("delete = %d", got)
	}
	if read := e.call(t, a, admin, "GET", "/", e.h.GetOverflowPolicy, path, nil); !strings.Contains(read.Body.String(), `"policy":null`) {
		t.Fatalf("after delete: %s", read.Body.String())
	}
	if auditCount(t, e, a, "overflow.policy_saved") != 2 || auditCount(t, e, a, "overflow.policy_removed") != 1 {
		t.Errorf("audit: saved=%d removed=%d", auditCount(t, e, a, "overflow.policy_saved"), auditCount(t, e, a, "overflow.policy_removed"))
	}
}

// Codex: allow_closed / allow_off_shift bypass the calendar and the shift - only a holder of routing.override may set them; saving a
// policy re-evaluates the conversations already waiting under the old one.
func TestOverflowBypassFlagsNeedRoutingOverrideAndSavingKicksWaitingConversations(t *testing.T) {
	e := newEnv(t)
	a := e.tenant(t)
	admin := e.member(t, a, "tenant_admin")
	supervisor := e.member(t, a, "tenant_supervisor")
	n1, n2 := e.queue(t, a, "N1", false), e.queue(t, a, "N2", false)
	path := map[string]string{"queue_id": n1.String()}
	plain := policyBody(qstep(1, n2, 0, "no_eligible_agent"))
	flagged := policyBody(map[string]any{"position": 1, "triggers": []string{"after_hours"}, "destination_kind": "queue", "destination_id": n2, "allow_closed": true})
	offShift := policyBody(map[string]any{"position": 1, "triggers": []string{"no_eligible_agent"}, "destination_kind": "queue", "destination_id": n2, "allow_off_shift": true})
	if got := e.call(t, a, supervisor, "PUT", "/", e.h.PutOverflowPolicy, path, plain).Code; got != 200 {
		t.Fatalf("a supervisor edits a plain chain: %d", got)
	}
	for name, body := range map[string]map[string]any{"allow_closed": flagged, "allow_off_shift": offShift} {
		if got := e.call(t, a, supervisor, "PUT", "/", e.h.PutOverflowPolicy, path, body).Code; got != 403 {
			t.Errorf("a supervisor setting %s = %d, want 403", name, got)
		}
	}
	if got := e.call(t, a, admin, "PUT", "/", e.h.PutOverflowPolicy, path, flagged).Code; got != 200 {
		t.Fatalf("an administrator holds routing.override: %d", got)
	}
	var closed bool
	if err := e.seed.QueryRow(context.Background(), `SELECT s.allow_closed FROM overflow_steps s JOIN overflow_policies p ON p.id=s.policy_id WHERE p.queue_id=$1`, n1).Scan(&closed); err != nil || !closed {
		t.Fatalf("the administrator's flag was saved: %v %v", closed, err)
	}
	// saving kicks the open unassigned conversations of the queue: a 'retry' timer due now
	conv, contact := uuid.New(), uuid.New()
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO contacts (id, tenant_id, display_name, phone_e164, status) VALUES ($1,$2,'C','+5511900006001','active')`, contact, a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO conversations (id, tenant_id, contact_id, queue_id, status) VALUES ($1,$2,$3,$4,'open')`, conv, a, contact, n1); err != nil {
		t.Fatal(err)
	}
	if got := e.call(t, a, admin, "PUT", "/", e.h.PutOverflowPolicy, path, plain).Code; got != 200 {
		t.Fatalf("re-save: %d", got)
	}
	var n int
	if err := e.seed.QueryRow(context.Background(), `SELECT count(*) FROM routing_engine_timers WHERE conversation_id=$1 AND kind='retry' AND state='pending' AND due_at <= now() + interval '5 seconds'`, conv).Scan(&n); err != nil || n != 1 {
		t.Fatalf("saving a policy re-evaluates the waiting conversation: timers=%d %v", n, err)
	}
	if err := e.seed.QueryRow(context.Background(), `SELECT count(*) FROM conversations WHERE id=$1 AND assigned_to_user_id IS NOT NULL`, conv).Scan(&n); err != nil || n != 0 {
		t.Fatal("the kick only schedules; it assigns nothing itself")
	}
}
