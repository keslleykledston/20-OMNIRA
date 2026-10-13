package adapters

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/routing/eligibility"
)

type fakePresence struct {
	online map[uuid.UUID]bool
	fail   bool
	calls  int
}

func (f *fakePresence) OnlineMembers(_ context.Context, _ uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]bool, error) {
	f.calls++
	if f.fail {
		return nil, context.DeadlineExceeded
	}
	out := map[uuid.UUID]bool{}
	for _, id := range ids {
		out[id] = f.online[id]
	}
	return out, nil
}

// attendant: an active member with an active agent profile, optionally a member of the queue.
func (e *env) attendant(t *testing.T, tenant uuid.UUID, queue *uuid.UUID, capacity int, available bool) (user, profile uuid.UUID) {
	t.Helper()
	user = e.member(t, tenant, "tenant_agent")
	var membership uuid.UUID
	if err := e.seed.QueryRow(context.Background(), `SELECT id FROM memberships WHERE tenant_id=$1 AND user_id=$2`, tenant, user).Scan(&membership); err != nil {
		t.Fatal(err)
	}
	profile = uuid.New()
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO agent_profiles (id, tenant_id, membership_id, status) VALUES ($1,$2,$3,'active')`, profile, tenant, membership); err != nil {
		t.Fatalf("agent profile: %v", err)
	}
	if queue != nil {
		if _, err := e.seed.Exec(context.Background(), `INSERT INTO queue_members (tenant_id, queue_id, user_id, active, available, capacity) VALUES ($1,$2,$3,true,$4,$5)`, tenant, *queue, user, available, capacity); err != nil {
			t.Fatal(err)
		}
	}
	return user, profile
}

func (e *env) snapshot(t *testing.T, tenant, actor, queue uuid.UUID, when string, h *Handler) QueueSnapshot {
	t.Helper()
	target := "/"
	if when != "" {
		target = "/?at=" + when
	}
	rec := e.call(t, tenant, actor, "GET", target, h.QueueEligibility, map[string]string{"queue_id": queue.String()}, nil)
	if rec.Code != 200 {
		t.Fatalf("eligibility = %d %s", rec.Code, rec.Body.String())
	}
	return decode[QueueSnapshot](t, rec)
}

func verdictOf(s QueueSnapshot, user uuid.UUID) AgentVerdict {
	for _, a := range s.Agents {
		if a.UserID == user {
			return a
		}
	}
	return AgentVerdict{}
}

func has(v AgentVerdict, r eligibility.Reason) bool {
	for _, x := range v.Reasons {
		if x == r {
			return true
		}
	}
	return false
}

// "Suporte 24x7 sem agentes online": the sector is open all day, and nobody is eligible because nobody is online.
func TestSupport24x7WithNobodyOnlineIsOpenButHasNoEligibleAgent(t *testing.T) {
	e := newEnv(t)
	a := e.tenant(t)
	admin := e.member(t, a, "tenant_admin")
	const tz = "America/Manaus"
	q := e.queue(t, a, "Suporte N1", false)
	always := decode[Calendar](t, e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, map[string]any{"name": "24x7", "definition": map[string]any{"kind": "24x7", "timezone": tz}})).ID
	e.call(t, a, admin, "PUT", "/", e.h.SetQueueCalendar, map[string]string{"queue_id": q.String()}, map[string]any{"calendar_id": always})
	u1, p1 := e.attendant(t, a, &q, 2, true)
	u2, _ := e.attendant(t, a, &q, 2, true)
	if _, err := e.seed.Exec(context.Background(), `UPDATE tenants SET routing_require_presence=true WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	pres := &fakePresence{online: map[uuid.UUID]bool{}}
	h := NewHandler(e.app, e.h.audit).WithPresence(pres)
	night := "" // presence and capacity exist only in real time: ask about NOW (the sector is 24x7, so the hour does not matter)
	s := e.snapshot(t, a, admin, q, night, h)
	if s.CalendarOnly || s.WiredToRouter || !s.Open || s.EligibleCnt != 0 || !has(verdictOf(s, u1), eligibility.Offline) || !has(verdictOf(s, u2), eligibility.Offline) {
		t.Fatalf("24x7 open, nobody online: %+v", s)
	}
	pres.online[p1] = true // one connects: open sector + online + capacity => eligible
	s = e.snapshot(t, a, admin, q, night, h)
	if s.EligibleCnt != 1 || !verdictOf(s, u1).Eligible || verdictOf(s, u2).Eligible {
		t.Fatalf("one online: %+v", s)
	}
	if pres.calls < 2 || pres.calls > 4 {
		t.Errorf("presence is read in one batch per snapshot, got %d calls for 2 snapshots", pres.calls)
	}
	// presence backend down: fail closed, never assume online
	pres.fail = true
	s = e.snapshot(t, a, admin, q, night, h)
	if s.EligibleCnt != 0 || !has(verdictOf(s, u1), eligibility.PresenceUnavailable) {
		t.Fatalf("presence down must fail closed: %+v", s)
	}
	// and without any checker wired at all, the same
	s = e.snapshot(t, a, admin, q, night, NewHandler(e.app, e.h.audit))
	if s.EligibleCnt != 0 || !has(verdictOf(s, u1), eligibility.PresenceUnavailable) {
		t.Fatalf("no presence backend must fail closed: %+v", s)
	}
}

// Agenda do setor, escala do agente, presença e capacidade são independentes.
func TestSectorCalendarAgentShiftPresenceAndCapacityAreIndependent(t *testing.T) {
	e := newEnv(t)
	a := e.tenant(t)
	admin := e.member(t, a, "tenant_admin")
	const tz = "America/Manaus"
	q := e.queue(t, a, "Financeiro N1", false)
	sector := decode[sectorItem](t, e.call(t, a, admin, "POST", "/", e.h.CreateSector, nil, map[string]string{"name": "Financeiro"})).ID
	e.call(t, a, admin, "PUT", "/", e.h.SetQueueSector, map[string]string{"queue_id": q.String()}, map[string]any{"sector_id": sector})
	office := decode[Calendar](t, e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, businessBody("Expediente", tz))).ID
	e.call(t, a, admin, "PATCH", "/", e.h.UpdateSector, map[string]string{"sector_id": sector.String()}, map[string]any{"calendar_id": office})
	// two shift calendars: mornings only (08-12) and nights only (22:00-06:00, crosses midnight)
	morning := decode[Calendar](t, e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, map[string]any{"name": "Manhã", "definition": map[string]any{
		"kind": "custom", "timezone": tz, "weekly": []map[string]any{{"day": "mon", "intervals": []map[string]string{{"start": "08:00", "end": "12:00"}}}}}})).ID
	night := decode[Calendar](t, e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, map[string]any{"name": "Madrugada", "definition": map[string]any{
		"kind": "custom", "timezone": tz, "weekly": []map[string]any{{"day": "mon", "intervals": []map[string]string{{"start": "22:00", "end": "06:00"}}}}}})).ID
	am, _ := e.attendant(t, a, &q, 3, true)
	nt, _ := e.attendant(t, a, &q, 3, true)
	free, _ := e.attendant(t, a, &q, 3, true)  // no shift: unrestricted
	away, _ := e.attendant(t, a, &q, 3, false) // marked unavailable in the queue
	full, _ := e.attendant(t, a, &q, 1, true)  // capacity 1, already holds one
	contact := uuid.New()
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO contacts (id, tenant_id, display_name, phone_e164, status) VALUES ($1,$2,'C','+5511900008001','active')`, contact, a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO conversations (id, tenant_id, contact_id, queue_id, assigned_to_user_id, status) VALUES ($1,$2,$3,$4,$5,'open')`, uuid.New(), a, contact, q, full); err != nil {
		t.Fatal(err)
	}
	set := func(user, cal uuid.UUID, queue *uuid.UUID) {
		body := map[string]any{"user_id": user, "calendar_id": cal}
		if queue != nil {
			body["queue_id"] = *queue
		}
		if got := e.call(t, a, admin, "PUT", "/", e.h.SetShift, nil, body).Code; got != 200 {
			t.Fatalf("set shift = %d", got)
		}
	}
	set(am, morning, nil)
	set(nt, night, &q) // queue scope
	// Monday 10:00: sector open, morning agent in shift, night agent off shift
	s := e.snapshot(t, a, admin, q, at(t, tz, "2026-10-12 10:00"), e.h)
	if !s.CalendarOnly {
		t.Fatal("an instant that is not now is answered by calendar, shift and membership only")
	}
	for name, c := range map[string]struct {
		user   uuid.UUID
		wantOK bool
		reason eligibility.Reason
	}{
		"morning agent in shift": {am, true, ""}, "night agent off shift": {nt, false, eligibility.OffShift}, "no shift = unrestricted": {free, true, ""},
		"unavailable": {away, false, eligibility.Unavailable},
	} {
		v := verdictOf(s, c.user)
		if v.Eligible != c.wantOK || (c.reason != "" && !has(v, c.reason)) {
			t.Errorf("%s: %+v", name, v)
		}
	}
	if verdictOf(s, am).ShiftSource != "all" || verdictOf(s, nt).ShiftSource != "queue" || verdictOf(s, free).ShiftSource != "none" {
		t.Errorf("shift sources: %q %q %q", verdictOf(s, am).ShiftSource, verdictOf(s, nt).ShiftSource, verdictOf(s, free).ShiftSource)
	}
	// Monday 23:00: the sector (09-18) is CLOSED for everyone, whatever the shifts say; the night agent is in shift but not eligible
	s = e.snapshot(t, a, admin, q, at(t, tz, "2026-10-12 23:00"), e.h)
	if s.Open || s.EligibleCnt != 0 || !has(verdictOf(s, nt), eligibility.QueueClosed) || has(verdictOf(s, nt), eligibility.OffShift) {
		t.Fatalf("closed sector, night agent in shift: %+v", s)
	}
	// Tuesday 02:00: still Monday's night shift (crosses midnight); the sector is closed anyway
	s = e.snapshot(t, a, admin, q, at(t, tz, "2026-10-13 02:00"), e.h)
	if has(verdictOf(s, nt), eligibility.OffShift) || !has(verdictOf(s, nt), eligibility.QueueClosed) {
		t.Fatalf("the night shift runs past midnight: %+v", verdictOf(s, nt))
	}
	// clearing a shift removes the restriction
	if got := e.call(t, a, admin, "PUT", "/", e.h.SetShift, nil, map[string]any{"user_id": nt, "calendar_id": nil, "queue_id": q}).Code; got != 200 {
		t.Fatalf("clear shift = %d", got)
	}
	s = e.snapshot(t, a, admin, q, at(t, tz, "2026-10-12 10:00"), e.h)
	if !verdictOf(s, nt).Eligible || verdictOf(s, nt).ShiftSource != "none" {
		t.Fatalf("after clearing: %+v", verdictOf(s, nt))
	}
}

// Plantão: quem está de plantão é elegível sem ser membro da fila; fora do período, não; roster inativo tira todos do plantão.
func TestOnCallRostersPeriodsOverlapsAndAuthority(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(t), e.tenant(t)
	admin := e.member(t, a, "tenant_admin")
	adminB := e.member(t, b, "tenant_admin")
	const tz = "America/Manaus"
	q := e.queue(t, a, "Suporte N2", false)
	sector := decode[sectorItem](t, e.call(t, a, admin, "POST", "/", e.h.CreateSector, nil, map[string]string{"name": "Suporte"})).ID
	e.call(t, a, admin, "PUT", "/", e.h.SetQueueSector, map[string]string{"queue_id": q.String()}, map[string]any{"sector_id": sector})
	always := decode[Calendar](t, e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, map[string]any{"name": "24x7", "definition": map[string]any{"kind": "24x7", "timezone": tz}})).ID
	e.call(t, a, admin, "PUT", "/", e.h.SetQueueCalendar, map[string]string{"queue_id": q.String()}, map[string]any{"calendar_id": always})
	onCallUser, _ := e.attendant(t, a, nil, 0, true) // NOT a member of the queue
	outsider := e.member(t, a, "tenant_agent")       // a member of the company without an agent profile
	foreignUser, _ := e.attendant(t, b, nil, 0, true)

	rec := e.call(t, a, admin, "POST", "/", e.h.CreateRoster, nil, map[string]any{"name": "Plantão noturno", "kind": "on_call", "sector_id": sector})
	if rec.Code != 201 {
		t.Fatalf("create roster = %d %s", rec.Code, rec.Body.String())
	}
	roster := decode[rosterItem](t, rec).ID
	path := map[string]string{"roster_id": roster.String()}
	from, to := time.Date(2026, 10, 12, 22, 0, 0, 0, time.UTC), time.Date(2026, 10, 13, 6, 0, 0, 0, time.UTC)
	add := func(user uuid.UUID, f, tt time.Time, capacity int) int {
		return e.call(t, a, admin, "POST", "/", e.h.AddPeriod, path, map[string]any{"user_id": user, "from": f, "to": tt, "capacity": capacity}).Code
	}
	if got := add(onCallUser, from, to, 2); got != 201 {
		t.Fatalf("add period = %d", got)
	}
	if got := add(onCallUser, from.Add(time.Hour), to.Add(time.Hour), 2); got != 409 {
		t.Fatalf("overlapping period for the same person = %d, want 409", got)
	}
	if got := add(onCallUser, to, to.Add(8*time.Hour), 2); got != 201 {
		t.Fatalf("a period that starts when the other ends is not an overlap: %d", got)
	}
	if got := add(outsider, from, to, 1); got != 404 {
		t.Fatalf("a member without an agent profile cannot be put on call: %d", got)
	}
	if got := add(foreignUser, from, to, 1); got != 404 {
		t.Fatalf("another company's attendant cannot be put on call: %d", got)
	}
	if got := add(onCallUser, from, from.Add(40*24*time.Hour), 1); got != 400 {
		t.Fatalf("a period over 31 days = %d, want 400", got)
	}
	inside, outside := from.Add(2*time.Hour).Format(time.RFC3339), from.Add(-2*time.Hour).Format(time.RFC3339)
	s := e.snapshot(t, a, admin, q, inside, e.h)
	v := verdictOf(s, onCallUser)
	if !v.Eligible || !v.OnCall || v.Capacity != 2 {
		t.Fatalf("on call, not a queue member: %+v", v)
	}
	if s := e.snapshot(t, a, admin, q, outside, e.h); verdictOf(s, onCallUser).UserID != uuid.Nil {
		t.Fatalf("outside the period the person is not even a candidate: %+v", verdictOf(s, onCallUser))
	}
	// a roster of ANOTHER sector does not cover this queue
	other := decode[sectorItem](t, e.call(t, a, admin, "POST", "/", e.h.CreateSector, nil, map[string]string{"name": "Financeiro"})).ID
	r2 := decode[rosterItem](t, e.call(t, a, admin, "POST", "/", e.h.CreateRoster, nil, map[string]any{"name": "Plantão financeiro", "kind": "on_call", "sector_id": other})).ID
	otherUser, _ := e.attendant(t, a, nil, 0, true)
	if got := e.call(t, a, admin, "POST", "/", e.h.AddPeriod, map[string]string{"roster_id": r2.String()}, map[string]any{"user_id": otherUser, "from": from, "to": to}).Code; got != 201 {
		t.Fatalf("add period to the other roster = %d", got)
	}
	if verdictOf(e.snapshot(t, a, admin, q, inside, e.h), otherUser).UserID != uuid.Nil {
		t.Fatal("a roster of another sector must not cover this queue")
	}
	// a tenant-wide contingency roster (no sector) covers every queue
	r3 := decode[rosterItem](t, e.call(t, a, admin, "POST", "/", e.h.CreateRoster, nil, map[string]any{"name": "Contingência", "kind": "contingency"})).ID
	cUser, _ := e.attendant(t, a, nil, 0, true)
	e.call(t, a, admin, "POST", "/", e.h.AddPeriod, map[string]string{"roster_id": r3.String()}, map[string]any{"user_id": cUser, "from": from, "to": to})
	if !verdictOf(e.snapshot(t, a, admin, q, inside, e.h), cUser).Eligible {
		t.Fatal("the contingency team covers every queue of the company")
	}
	// deactivating a roster takes everyone off call at once, history stays
	if got := e.call(t, a, admin, "PATCH", "/", e.h.UpdateRoster, path, map[string]any{"active": false}).Code; got != 200 {
		t.Fatalf("deactivate = %d", got)
	}
	if verdictOf(e.snapshot(t, a, admin, q, inside, e.h), onCallUser).UserID != uuid.Nil {
		t.Fatal("an inactive roster puts nobody on call")
	}
	var kept int
	if err := e.seed.QueryRow(context.Background(), `SELECT count(*) FROM on_call_periods WHERE roster_id=$1`, roster).Scan(&kept); err != nil || kept != 2 {
		t.Fatalf("deactivation keeps the periods: %d %v", kept, err)
	}
	// isolation: tenant B cannot see or change A's roster, and A's data never shows in B's lists
	for name, c := range map[string]int{
		"B adds to A's roster": e.call(t, b, adminB, "POST", "/", e.h.AddPeriod, path, map[string]any{"user_id": foreignUser, "from": from, "to": to}).Code,
		"B patches A's roster": e.call(t, b, adminB, "PATCH", "/", e.h.UpdateRoster, path, map[string]any{"active": true}).Code,
		"B lists A's periods":  e.call(t, b, adminB, "GET", "/", e.h.ListPeriods, path, nil).Code,
		"B reads A's queue":    e.call(t, b, adminB, "GET", "/", e.h.QueueEligibility, map[string]string{"queue_id": q.String()}, nil).Code,
	} {
		if c != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", name, c)
		}
	}
	if got := e.call(t, a, admin, "POST", "/", e.h.CreateRoster, nil, map[string]any{"name": "Plantão noturno", "kind": "on_call"}).Code; got != 409 {
		t.Errorf("duplicate roster name = %d, want 409", got)
	}
	// a shift for a stranger is refused, and the database refuses a cross-tenant calendar even for the owner role
	calB := decode[Calendar](t, e.call(t, b, adminB, "POST", "/", e.h.CreateCalendar, nil, businessBody("Do B", tz))).ID
	if got := e.call(t, a, admin, "PUT", "/", e.h.SetShift, nil, map[string]any{"user_id": foreignUser, "calendar_id": always}).Code; got != 404 {
		t.Errorf("shift for another company's attendant = %d, want 404", got)
	}
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO agent_shifts (tenant_id, user_id, calendar_id) VALUES ($1,$2,$3)`, a, onCallUser, calB); err == nil {
		t.Error("the database must refuse a shift pointing at another tenant's calendar")
	}
}

// A pessoa pode ter escala geral, do setor e da fila: vale a mais específica (fila > setor > geral).
func TestAgentShiftPrecedenceQueueOverSectorOverAll(t *testing.T) {
	e := newEnv(t)
	a := e.tenant(t)
	admin := e.member(t, a, "tenant_admin")
	const tz = "America/Manaus"
	q := e.queue(t, a, "Comercial N1", false)
	sector := decode[sectorItem](t, e.call(t, a, admin, "POST", "/", e.h.CreateSector, nil, map[string]string{"name": "Comercial"})).ID
	e.call(t, a, admin, "PUT", "/", e.h.SetQueueSector, map[string]string{"queue_id": q.String()}, map[string]any{"sector_id": sector})
	mk := func(name, kind string) uuid.UUID {
		def := map[string]any{"kind": kind, "timezone": tz}
		if kind == "custom" { // closed on Mondays: an explicit weekday with no intervals
			def["weekly"] = []map[string]any{{"day": "mon", "intervals": []map[string]string{}}}
		}
		return decode[Calendar](t, e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, map[string]any{"name": name, "definition": def})).ID
	}
	openCal, closedCal := mk("Aberta", "24x7"), mk("Fechada", "custom")
	user, _ := e.attendant(t, a, &q, 3, true)
	set := func(cal uuid.UUID, scope map[string]any) {
		body := map[string]any{"user_id": user, "calendar_id": cal}
		for k, v := range scope {
			body[k] = v
		}
		if got := e.call(t, a, admin, "PUT", "/", e.h.SetShift, nil, body).Code; got != 200 {
			t.Fatalf("set shift = %d", got)
		}
	}
	when := at(t, tz, "2026-10-12 10:00")
	check := func(want bool, source string) {
		t.Helper()
		v := verdictOf(e.snapshot(t, a, admin, q, when, e.h), user)
		if v.Eligible != want || v.ShiftSource != source {
			t.Fatalf("eligible=%v source=%q, want %v %q (%+v)", v.Eligible, v.ShiftSource, want, source, v)
		}
	}
	set(closedCal, nil) // general shift: closed
	check(false, "all")
	set(openCal, map[string]any{"sector_id": sector}) // the sector's shift is open and beats the general one
	check(true, "sector")
	set(closedCal, map[string]any{"queue_id": q}) // the queue's shift is closed and beats the sector's
	check(false, "queue")
	if got := e.call(t, a, admin, "PUT", "/", e.h.SetShift, nil, map[string]any{"user_id": user, "calendar_id": openCal, "queue_id": q, "sector_id": sector}).Code; got != 400 {
		t.Fatalf("a shift with both scopes = %d, want 400", got)
	}
	// re-setting the same scope replaces it (one shift per person and scope)
	set(openCal, map[string]any{"queue_id": q})
	check(true, "queue")
	var n int
	if err := e.seed.QueryRow(context.Background(), `SELECT count(*) FROM agent_shifts WHERE tenant_id=$1 AND user_id=$2`, a, user).Scan(&n); err != nil || n != 3 {
		t.Fatalf("one shift per person and scope: %d %v", n, err)
	}
}

// Capacidade e presença só existem em tempo real; para outro instante o instantâneo diz que é "só calendário" e não consulta a presença.
func TestCapacityIsEvaluatedInRealTimeAndOtherInstantsAreCalendarOnly(t *testing.T) {
	e := newEnv(t)
	a := e.tenant(t)
	admin := e.member(t, a, "tenant_admin")
	const tz = "America/Manaus"
	q := e.queue(t, a, "Suporte N1", false)
	always := decode[Calendar](t, e.call(t, a, admin, "POST", "/", e.h.CreateCalendar, nil, map[string]any{"name": "24x7", "definition": map[string]any{"kind": "24x7", "timezone": tz}})).ID
	e.call(t, a, admin, "PUT", "/", e.h.SetQueueCalendar, map[string]string{"queue_id": q.String()}, map[string]any{"calendar_id": always})
	free, _ := e.attendant(t, a, &q, 2, true)
	full, _ := e.attendant(t, a, &q, 1, true)
	contact := uuid.New()
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO contacts (id, tenant_id, display_name, phone_e164, status) VALUES ($1,$2,'C','+5511900008002','active')`, contact, a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO conversations (id, tenant_id, contact_id, queue_id, assigned_to_user_id, status) VALUES ($1,$2,$3,$4,$5,'open')`, uuid.New(), a, contact, q, full); err != nil {
		t.Fatal(err)
	}
	s := e.snapshot(t, a, admin, q, "", e.h)
	if s.CalendarOnly || !verdictOf(s, free).Eligible || verdictOf(s, full).Eligible || !has(verdictOf(s, full), eligibility.NoCapacity) || s.EligibleCnt != 1 {
		t.Fatalf("real time: %+v", s)
	}
	if v := verdictOf(s, full); v.Workload != 1 || v.Capacity != 1 {
		t.Fatalf("workload/capacity: %+v", v)
	}
	// a future instant: capacity and presence are not evaluated, the answer says so
	if _, err := e.seed.Exec(context.Background(), `UPDATE tenants SET routing_require_presence=true WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	pres := &fakePresence{online: map[uuid.UUID]bool{}}
	h := NewHandler(e.app, e.h.audit).WithPresence(pres)
	s = e.snapshot(t, a, admin, q, time.Now().Add(48*time.Hour).UTC().Format(time.RFC3339), h)
	if !s.CalendarOnly || s.PresenceReq || pres.calls != 0 || !verdictOf(s, full).Eligible {
		t.Fatalf("a future instant must be calendar-only and never consult presence: %+v calls=%d", s, pres.calls)
	}
	// the database refuses to schedule somebody who is not a member of the tenant, whoever writes (owner role included)
	stranger := uuid.New()
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO users (id, external_subject, email, display_name, status) VALUES ($1,$2,$3,'s','active')`, stranger, stranger.String(), stranger.String()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, stranger) })
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO agent_shifts (tenant_id, user_id, calendar_id) VALUES ($1,$2,$3)`, a, stranger, always); err == nil {
		t.Error("the database must refuse a shift for a user without membership in the tenant")
	}
	roster := uuid.New()
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO on_call_rosters (id, tenant_id, name, kind) VALUES ($1,$2,'R','on_call')`, roster, a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.seed.Exec(context.Background(), `INSERT INTO on_call_periods (tenant_id, roster_id, user_id, starts_at, ends_at) VALUES ($1,$2,$3,now(),now()+interval '1 hour')`, a, roster, stranger); err == nil {
		t.Error("the database must refuse an on-call period for a user without membership in the tenant")
	}
}
