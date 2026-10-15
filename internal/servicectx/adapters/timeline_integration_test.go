package adapters_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/adapters"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

func (s *stack) timeline(user uuid.UUID, id uuid.UUID, ascending bool, limit int) (all []ports.TimelineItem, pages int) {
	s.t.Helper()
	cursor := ""
	s.as(user, s.env.TenantA, func(ctx context.Context) {
		for pages = 0; pages < 50; {
			items, next, err := s.svc.SessionTimeline(ctx, id, cursor, limit, ascending)
			if err != nil {
				s.t.Fatal(err)
			}
			pages++
			all = append(all, items...)
			if next == "" {
				return
			}
			cursor = next
		}
	})
	return all, pages
}

func types(items []ports.TimelineItem) (out []string) {
	for _, it := range items {
		out = append(out, it.EventType)
	}
	return out
}

func TestTheLifecycleWritesOrderedStructuredEventsWithoutText(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	s.message(conv, "inbound", time.Now().UTC(), nil)
	sess := s.mustOpen(s.agent, conv)
	cat := uuid.New()
	s.exec(`INSERT INTO service_categories(id, tenant_id, code, label) VALUES($1,$2,'fin','Financeiro')`, cat, s.env.TenantA)
	var cur *domain.Session
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) {
		cur, e = s.svc.Patch(ctx, sess.ID, domain.PatchInput{CategoryID: &cat}, sess.Version)
		return
	}))
	must(s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) {
		cur, e = s.svc.SetStatus(ctx, sess.ID, domain.SessionWaiting, cur.Version)
		return
	}))
	closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther, Note: "texto do operador que nunca vai para o evento"}, cur.Version)
	must(err)
	_, err = s.reopen(s.svc, s.agent, sess.ID, closed.Version)
	must(err)

	items, _ := s.timeline(s.agent, sess.ID, true, 50)
	want := []string{"service_session.opened", "service_session.classified", "service_session.status_changed", "service_session.closed", "service_session.reopened"}
	if fmt.Sprint(types(items)) != fmt.Sprint(want) {
		t.Fatalf("events = %v, want %v", types(items), want)
	}
	for _, it := range items {
		if it.Derived || it.ActorType != "user" || it.ActorID == nil || *it.ActorID != s.agent || it.SchemaVersion != 1 || it.SessionID == nil || *it.SessionID != sess.ID {
			t.Errorf("event %s: %+v", it.EventType, it)
		}
		if it.Payload["protocol"] != sess.Protocol {
			t.Errorf("payload carries the protocol: %v", it.Payload)
		}
		if strings.Contains(fmt.Sprint(it.Payload), "operador") {
			t.Errorf("a payload never carries typed text: %v", it.Payload)
		}
	}
	if items[3].Payload["result"] != domain.ResOther || items[4].Payload["previous_result"] != domain.ResOther || items[4].Note != "o cliente voltou a falar" {
		t.Fatalf("closed/reopened payloads: %v / %v note=%q", items[3].Payload, items[4].Payload, items[4].Note)
	}
	var bodies int
	_ = s.env.Seed.QueryRow(context.Background(), `SELECT count(*) FROM context_events WHERE payload::text LIKE '%oi%' AND event_type NOT LIKE '%opened%'`).Scan(&bodies)
	if n := s.count(`SELECT count(*) FROM messages WHERE conversation_id=$1`, conv); n != 1 {
		t.Fatalf("recording history never touches messages: %d", n)
	}
}

func TestHistoryIsImmutableForEveryone(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	for _, stmt := range []string{
		`UPDATE context_events SET note='x' WHERE session_id=$1`,
		`UPDATE context_events SET tenant_id=` + "'" + uuid.NewString() + "'" + ` WHERE session_id=$1`,
	} {
		if _, err := s.env.Seed.Exec(context.Background(), stmt, sess.ID); err == nil {
			t.Errorf("not even the owner role may change history: %s", stmt)
		}
	}
	err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error {
		return execInSession(ctx, `DELETE FROM context_events WHERE session_id=$1`, sess.ID)
	})
	if err == nil {
		t.Error("the application role must not delete history")
	}
	if n := s.count(`SELECT count(*) FROM context_events WHERE session_id=$1`, sess.ID); n != 1 {
		t.Fatalf("the opening event is still there: %d", n)
	}
}

func TestAnEventCannotBeSignedAsSomeoneElseNorWrittenWithoutAKey(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	insert := func(actor uuid.UUID) string {
		return fmt.Sprintf(`INSERT INTO context_events(id, tenant_id, event_type, entity_type, entity_id, session_id, actor_type, actor_id, occurred_at)
			VALUES('%s','%s','service_session.status_changed','service_session','%s','%s','user','%s', now())`, uuid.New(), s.env.TenantA, sess.ID, sess.ID, actor)
	}
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, insert(s.admin)) }); err == nil {
		t.Error("a person cannot write history as somebody else")
	}
	if err := s.try(s.observer, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, insert(s.observer)) }); err == nil {
		t.Error("a member with no service key cannot write history")
	}
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, insert(s.agent)) }); err != nil {
		t.Errorf("a person with a key signs as themselves: %v", err)
	}
	// the other actor kinds exist only in a system session: nobody forges a 'system', 'flow' or 'ai' fact with plain SQL, not even with their own id
	for _, kind := range []string{"system", "flow", "ai", "integration"} {
		forged := fmt.Sprintf(`INSERT INTO context_events(id, tenant_id, event_type, entity_type, entity_id, session_id, actor_type, actor_id, occurred_at)
			VALUES('%s','%s','service_session.closed','service_session','%s','%s','%s','%s', now())`, uuid.New(), s.env.TenantA, sess.ID, sess.ID, kind, s.agent)
		if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error { return execInSession(ctx, forged) }); err == nil {
			t.Errorf("a person cannot write an event as actor_type=%s", kind)
		}
	}
	s.env.AsSystem(t, s.env.TenantA, func(ctx context.Context) {
		ok := fmt.Sprintf(`INSERT INTO context_events(id, tenant_id, event_type, entity_type, entity_id, session_id, actor_type, occurred_at)
			VALUES('%s','%s','service_session.status_changed','service_session','%s','%s','flow', now())`, uuid.New(), s.env.TenantA, sess.ID, sess.ID)
		if err := execInSession(ctx, ok); err != nil {
			t.Errorf("a system session writes flow facts: %v", err)
		}
	})
}

func TestRecordingTheSameFactTwiceWritesItOnce(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	repo := adapters.NewPostgresRepository(s.env.App)
	ev := func() domain.ContextEvent {
		return domain.ContextEvent{ID: uuid.New(), TenantID: s.env.TenantA, EventType: "service_session.classified", EntityType: "service_session", EntityID: sess.ID,
			SessionID: &sess.ID, ActorType: "user", ActorID: &s.agent, OccurredAt: time.Now().UTC(), RecordedAt: time.Now().UTC(), SchemaVersion: 1,
			Payload: map[string]any{"k": "v"}, DedupKey: "retry-safe-key-1"}
	}
	for i := 0; i < 3; i++ {
		if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error { return repo.Record(ctx, ev()) }); err != nil {
			t.Fatal(err)
		}
	}
	if n := s.count(`SELECT count(*) FROM context_events WHERE dedup_key='retry-safe-key-1'`); n != 1 {
		t.Fatalf("a retry writes nothing new: %d", n)
	}
	bad := ev()
	bad.DedupKey = ""
	bad.Payload = map[string]any{"text": strings.Repeat("x", 100)}
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) error { return repo.Record(ctx, bad) }); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a payload with a long text is refused before it reaches the database: %v", err)
	}
}

func TestTimelinePaginatesInBothDirectionsAndBreaksTiesById(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	repo := adapters.NewPostgresRepository(s.env.App)
	base := time.Now().UTC().Add(time.Second)
	for i := 0; i < 7; i++ {
		at := base.Add(time.Duration(i/2) * time.Millisecond) // pairs share the same instant: the id breaks the tie
		e := domain.ContextEvent{ID: uuid.New(), TenantID: s.env.TenantA, EventType: "service_session.classified", EntityType: "service_session", EntityID: sess.ID,
			SessionID: &sess.ID, ActorType: "user", ActorID: &s.admin, OccurredAt: at, RecordedAt: at, SchemaVersion: 1, Payload: map[string]any{"i": i}}
		if err := s.try(s.admin, s.env.TenantA, func(ctx context.Context) error { return repo.Record(ctx, e) }); err != nil {
			t.Fatal(err)
		}
	}
	check := func(asc bool) {
		items, pages := s.timeline(s.agent, sess.ID, asc, 3)
		if len(items) != 8 || pages < 3 { // 7 + the opening
			t.Fatalf("asc=%v: %d items in %d pages", asc, len(items), pages)
		}
		seen := map[uuid.UUID]bool{}
		for i, it := range items {
			if seen[it.ID] {
				t.Fatalf("an event twice across pages: %v", it.ID)
			}
			seen[it.ID] = true
			if i == 0 {
				continue
			}
			prev := items[i-1]
			before := prev.OccurredAt.Before(it.OccurredAt) || (prev.OccurredAt.Equal(it.OccurredAt) && prev.ID.String() < it.ID.String())
			if asc != before {
				t.Fatalf("asc=%v out of order at %d: %v/%v then %v/%v", asc, i, prev.OccurredAt, prev.ID, it.OccurredAt, it.ID)
			}
		}
	}
	check(true)
	check(false)
}

func TestTheSessionTimelineShowsWhoHeldTheConversationOnlyWhileItWasInCourse(t *testing.T) {
	s := newStack(t)
	conv, _ := s.conversation()
	assign := func(at time.Time, from, to *uuid.UUID) uuid.UUID {
		id := uuid.New()
		s.exec(`INSERT INTO assignment_events(id, tenant_id, conversation_id, from_user_id, to_user_id, changed_by, reason, created_at) VALUES($1,$2,$3,$4,$5,$6,'manual',$7)`,
			id, s.env.TenantA, conv, from, to, s.admin, at)
		return id
	}
	beforeOpen := assign(time.Now().UTC().Add(-time.Minute), nil, &s.agent)
	sess := s.mustOpen(s.agent, conv)
	time.Sleep(10 * time.Millisecond)
	during := assign(time.Now().UTC(), &s.agent, &s.admin)
	time.Sleep(10 * time.Millisecond)
	closed, err := s.closeSession(s.agent, sess.ID, domain.CloseInput{ResolutionCode: domain.ResOther}, sess.Version)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	whileClosed := assign(time.Now().UTC(), &s.admin, &s.agent)
	time.Sleep(10 * time.Millisecond)
	reopened, err := s.reopen(s.svc, s.agent, sess.ID, closed.Version)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	afterReopen := assign(time.Now().UTC(), &s.agent, &s.admin)
	_ = reopened

	items, _ := s.timeline(s.agent, sess.ID, true, 50)
	derived := map[uuid.UUID]bool{}
	for _, it := range items {
		if it.Derived {
			if it.EventType != "conversation.assignment_changed" || it.SourceSystem != "omnira.routing" {
				t.Errorf("derived entry: %+v", it)
			}
			derived[it.ID] = true
		}
	}
	if derived[beforeOpen] || derived[whileClosed] || !derived[during] || !derived[afterReopen] {
		t.Fatalf("assignments in the session's episodes only: before=%v during=%v whileClosed=%v afterReopen=%v", derived[beforeOpen], derived[during], derived[whileClosed], derived[afterReopen])
	}
	if n := s.count(`SELECT count(*) FROM context_events WHERE event_type='conversation.assignment_changed'`); n != 0 {
		t.Fatal("the routing history is read, never copied into the timeline table")
	}
}

func TestTimelinesNeverCrossCompanies(t *testing.T) {
	s := newStack(t)
	conv, contact := s.conversation()
	sess := s.mustOpen(s.agent, conv)
	if err := s.try(s.env.UserB, s.env.TenantB, func(ctx context.Context) error {
		_, _, e := s.svc.SessionTimeline(ctx, sess.ID, "", 10, false)
		return e
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("session timeline from B: %v", err)
	}
	if err := s.try(s.env.UserB, s.env.TenantB, func(ctx context.Context) error {
		_, _, e := s.svc.ContactTimeline(ctx, contact, "", 10, false)
		return e
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("contact timeline from B: %v", err)
	}
	s.env.AsSystem(t, s.env.TenantB, func(ctx context.Context) {
		if _, _, err := s.svc.SessionTimeline(ctx, sess.ID, "", 10, false); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("a system session of B reads A's timeline: %v", err)
		}
		if _, err := s.svc.ContactContext(ctx, contact, nil); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("a system session of B reads A's contact context: %v", err)
		}
	})
}

// A contact may serve several companies: the context never guesses, and never shows one company's data under another.
func TestContactContextAsksForTheCustomerWhenThereIsMoreThanOne(t *testing.T) {
	s := newStack(t)
	_, contact := s.conversation()
	c1, c2, other := uuid.New(), uuid.New(), uuid.New()
	for _, c := range []struct {
		id   uuid.UUID
		name string
	}{{c1, "Empresa Um"}, {c2, "Empresa Dois"}, {other, "Sem vínculo"}} {
		s.exec(`INSERT INTO customer_accounts(id, tenant_id, name) VALUES($1,$2,$3)`, c.id, s.env.TenantA, c.name)
	}
	for _, c := range []uuid.UUID{c1, c2} {
		s.exec(`INSERT INTO contact_account_links(tenant_id, contact_id, account_id, source) VALUES($1,$2,$3,'manual')`, s.env.TenantA, contact, c)
	}
	open := func(customer *uuid.UUID) *domain.Session {
		t.Helper()
		res, err := s.open(s.agent, domain.OpenInput{ContactID: &contact, CustomerID: customer, OriginChannel: "phone"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.closeSession(s.agent, res.Session.ID, domain.CloseInput{ResolutionCode: domain.ResInformationProvided}, 1); err != nil {
			t.Fatal(err)
		}
		return res.Session
	}
	s1, s2, s0 := open(&c1), open(&c2), open(nil)

	get := func(customer *uuid.UUID) (cc *application.ContactContext, err error) {
		err = s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) {
			cc, e = s.svc.ContactContext(ctx, contact, customer)
			return
		})
		return
	}
	cc, err := get(nil)
	if err != nil || !cc.NeedsSelection || len(cc.Customers) != 2 || cc.SelectedCustomerID != nil {
		t.Fatalf("two links and no choice: %+v %v", cc, err)
	}
	ids := func(c *application.ContactContext) (out []uuid.UUID) {
		for _, x := range c.RecentSessions {
			out = append(out, x.ID)
		}
		return
	}
	if got := ids(cc); len(got) != 1 || got[0] != s0.ID {
		t.Fatalf("without a choice only what is tied to no customer: %v", got)
	}
	for _, ev := range cc.RecentEvents {
		if ev.CustomerID != nil {
			t.Fatalf("no customer's event rides along before the choice: %+v", ev)
		}
	}
	cc, err = get(&c1)
	if err != nil || cc.NeedsSelection || cc.SelectedCustomerID == nil || *cc.SelectedCustomerID != c1 {
		t.Fatalf("chosen: %+v %v", cc, err)
	}
	got := map[uuid.UUID]bool{}
	for _, id := range ids(cc) {
		got[id] = true
	}
	if !got[s1.ID] || !got[s0.ID] || got[s2.ID] {
		t.Fatalf("customer 1 plus what has no customer, never customer 2: %v", got)
	}
	for _, ev := range cc.RecentEvents {
		if ev.CustomerID != nil && *ev.CustomerID != c1 {
			t.Fatalf("another company's event: %+v", ev)
		}
	}
	if _, err := get(&other); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a customer that is not linked to the contact: %v", err)
	}
	// a contact with a single link needs no choice
	_, solo := s.conversation()
	s.exec(`INSERT INTO contact_account_links(tenant_id, contact_id, account_id, source) VALUES($1,$2,$3,'manual')`, s.env.TenantA, solo, c1)
	var one *application.ContactContext
	if err := s.try(s.agent, s.env.TenantA, func(ctx context.Context) (e error) { one, e = s.svc.ContactContext(ctx, solo, nil); return }); err != nil {
		t.Fatal(err)
	}
	if one.NeedsSelection || one.SelectedCustomerID == nil || *one.SelectedCustomerID != c1 {
		t.Fatalf("a single link is the customer: %+v", one)
	}
}

func TestHTTPTimelineAndContext(t *testing.T) {
	s := newStack(t)
	mux := s.mux()
	conv, contact := s.conversation()
	r := s.call(mux, s.agent, "POST", "/service-sessions", fmt.Sprintf(`{"conversation_id":"%s"}`, conv))
	id := r.body["id"].(string)
	if r := s.call(mux, s.agent, "GET", "/service-sessions/"+id+"/timeline?order=sideways", ""); r.code != http.StatusBadRequest {
		t.Fatalf("bad order: %d", r.code)
	}
	r = s.call(mux, s.agent, "GET", "/service-sessions/"+id+"/timeline?order=asc&limit=5", "")
	items, _ := r.body["items"].([]any)
	if r.code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["event_type"] != "service_session.opened" {
		t.Fatalf("timeline: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.observer, "GET", "/service-sessions/"+id+"/timeline", ""); r.code != http.StatusForbidden {
		t.Fatalf("no service.read: %d", r.code)
	}
	r = s.call(mux, s.agent, "GET", "/contacts/"+contact.String()+"/service-context", "")
	if r.code != http.StatusOK || r.body["needs_customer_selection"] != false || r.body["active_session"] == nil {
		t.Fatalf("context: %d %v", r.code, r.body)
	}
	if r := s.call(mux, s.agent, "GET", "/contacts/"+contact.String()+"/service-context?customer_id="+uuid.NewString(), ""); r.code != http.StatusBadRequest {
		t.Fatalf("an unlinked customer: %d", r.code)
	}
	if r := s.call(mux, s.agent, "GET", "/contacts/"+uuid.NewString()+"/service-timeline", ""); r.code != http.StatusNotFound {
		t.Fatalf("unknown contact: %d", r.code)
	}
}
