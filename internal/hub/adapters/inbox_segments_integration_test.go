package adapters_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"testing"

	"github.com/google/uuid"
)

type segItem struct {
	ID             uuid.UUID `json:"id"`
	TenantID       uuid.UUID `json:"tenant_id"`
	ConversationID uuid.UUID `json:"conversation_id"`
	CustomerName   string    `json:"customer_name"`
	Status         string    `json:"status"`
	Assignment     string    `json:"assignment"`
}

type segPage struct {
	Items  []segItem `json:"items"`
	Counts *struct {
		Open    int `json:"open"`
		Waiting int `json:"waiting"`
		Mine    int `json:"mine"`
		Closed  int `json:"closed"`
	} `json:"counts"`
	NextCursor string `json:"next_cursor"`
}

func (a *hubAPI) segment(user uuid.UUID, query string) (int, segPage) {
	a.w.t.Helper()
	code, body, _ := a.do("GET", fmt.Sprintf("/api/v1/hubs/%s/inbox%s", a.w.hub, query), user, nil)
	var p segPage
	if code == 200 {
		a.w.must(json.Unmarshal([]byte(body), &p))
	}
	return code, p
}

// setItem writes what the projector would (name, holder, state) AND what the conversation itself says: the list reads who holds it and whether it is
// finalized from the conversation (the projection lags by up to a minute), so a fixture must agree with both.
func (w *world) setItem(conv uuid.UUID, name string, assigned any, status string) {
	w.exec(`UPDATE hub_inbox_items SET customer_name = $2, assigned_user_id = $3, status = $4 WHERE conversation_id = $1`, conv, name, assigned, status)
	w.exec(`UPDATE conversations SET assigned_to_user_id = $2, status = $3 WHERE id = $1`, conv, assigned, status)
}

func convsOf(p segPage) map[uuid.UUID]segItem {
	m := map[uuid.UUID]segItem{}
	for _, it := range p.Items {
		m[it.ConversationID] = it
	}
	return m
}

// The Hub inbox keeps the queue of pending attendances apart from the history (segments, ADR-0040 UX parity): "waiting" is open and held by
// nobody, "mine" is open and held by the caller, "closed" is the history, "open" is everything not finalized. A segment, like the search and
// the company filter, only NARROWS what RLS shows the caller; it can never widen it.
func TestHubInbox_SegmentsSeparateThePendingQueueFromTheHistory(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	agent := w.hubAgent("seg")
	colleague := w.hubAgent("seg-colleague")
	w.grant(agent, "A")
	w.grant(agent, "B")

	waiting, mine, theirs, closed, closedMine := w.conv["A"], w.conv["A2"], w.conv["A0"], w.conv["B"], uuid.New()
	// one more finalized conversation of B that the agent had held
	bContact := uuid.New()
	w.exec(`INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES ($1, $2, 'Antiga B', '+5511900000001')`, bContact, w.tenant["B"])
	w.conv["B-old"] = closedMine
	w.exec(`INSERT INTO conversations (id, tenant_id, contact_id) VALUES ($1, $2, $3)`, closedMine, w.tenant["B"], bContact)
	w.exec(`INSERT INTO hub_inbox_items (hub_id, tenant_id, conversation_id) VALUES ($1, $2, $3)`, w.hub, w.tenant["B"], closedMine)

	w.setItem(waiting, "Ana Pendente", nil, "open")
	w.setItem(mine, "Bruno Meu", agent, "open")
	w.setItem(theirs, "Carla do Colega", colleague, "open")
	w.setItem(closed, "Davi Encerrado", nil, "closed")
	w.setItem(closedMine, "100% Sob_Medida", agent, "closed")

	want := func(q string, ids ...uuid.UUID) {
		t.Helper()
		code, p := api.segment(agent, q)
		if code != 200 {
			t.Fatalf("%s: %d", q, code)
		}
		got := convsOf(p)
		if len(got) != len(ids) {
			t.Fatalf("%s: got %d items %v, want %d", q, len(got), got, len(ids))
		}
		for _, id := range ids {
			if _, ok := got[id]; !ok {
				t.Fatalf("%s: missing %s in %v", q, id, got)
			}
		}
	}
	want("", waiting, mine, theirs, closed, closedMine)          // no segment: the API before segments existed
	want("?segment=open", waiting, mine, theirs)                 // not finalized
	want("?segment=waiting", waiting)                            // the queue of pending attendances
	want("?segment=mine", mine)                                  // held by the caller (the colleague's is not mine)
	want("?segment=closed", closed, closedMine)                  // the history
	want("?segment=waiting&companies=" + w.tenant["B"].String()) // narrowed by company: B has nothing pending
	want("?segment=closed&companies="+w.tenant["B"].String(), closed, closedMine)

	// the assignment is relative to the caller and names nobody
	_, all := api.segment(agent, "")
	g := convsOf(all)
	for id, wantAssign := range map[uuid.UUID]string{waiting: "none", mine: "me", theirs: "other", closed: "none", closedMine: "me"} {
		if g[id].Assignment != wantAssign {
			t.Errorf("assignment of %s = %q, want %q", id, g[id].Assignment, wantAssign)
		}
	}

	// an unknown segment is a 400, never a silent "everything"
	for _, bad := range []string{"?segment=nope", "?segment=OPEN", "?segment=waiting;drop", "?segment=%27"} {
		if code, _ := api.segment(agent, bad); code != 400 {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}

	// the search is on the customer name and the text is DATA: wildcards are literal
	want("?q="+url.QueryEscape("ana"), waiting)
	want("?q="+url.QueryEscape("ANA")+"&segment=open", waiting)
	want("?q="+url.QueryEscape("%"), closedMine) // a literal percent sign, not "everything"
	want("?q="+url.QueryEscape("_"), closedMine) // a literal underscore, not "any character"
	want("?q="+url.QueryEscape("sob_medida"), closedMine)
	want("?q=" + url.QueryEscape("zzz-no-one"))
	want("?q=" + url.QueryEscape("'; DROP TABLE hub_inbox_items;--"))
	if code, _ := api.segment(agent, "?q="+url.QueryEscape(string(make([]rune, 81)))); code != 400 {
		t.Errorf("an 81-character search was accepted: %d", code)
	}
	// ... and a search can never reach a company the caller holds no grant on
	w.exec(`UPDATE hub_inbox_items SET customer_name = 'Ana Secreta' WHERE conversation_id = $1`, w.conv["C"])
	want("?q="+url.QueryEscape("ana"), waiting)
	want("?q=" + url.QueryEscape("secreta"))

	// counters: first page only, narrowed by the company filter, not by the segment or the search, never counting what RLS hides
	code, p := api.segment(agent, "?segment=closed&q=zzz")
	if code != 200 || p.Counts == nil {
		t.Fatalf("counts missing on the first page: %d %+v", code, p.Counts)
	}
	if c := *p.Counts; c.Open != 3 || c.Waiting != 1 || c.Mine != 1 || c.Closed != 2 {
		t.Fatalf("counts = %+v, want open 3, waiting 1, mine 1, closed 2 (the segment and the search do not change them)", c)
	}
	_, onlyB := api.segment(agent, "?companies="+w.tenant["B"].String())
	if c := *onlyB.Counts; c.Open != 0 || c.Waiting != 0 || c.Mine != 0 || c.Closed != 2 {
		t.Fatalf("counts narrowed to B = %+v", c)
	}
	// a second page carries no counts (they are not recomputed per page)
	_, first := api.segment(agent, "?limit=1")
	if first.NextCursor == "" {
		t.Fatal("expected a second page")
	}
	_, second := api.segment(agent, "?limit=1&cursor="+url.QueryEscape(first.NextCursor))
	if second.Counts != nil {
		t.Errorf("counts repeated on page 2: %+v", second.Counts)
	}
	// paging keeps the segment
	_, one := api.segment(agent, "?segment=closed&limit=1")
	if len(one.Items) != 1 || one.NextCursor == "" || one.Items[0].Status != "closed" {
		t.Fatalf("closed page 1: %+v", one)
	}
	_, two := api.segment(agent, "?segment=closed&limit=1&cursor="+url.QueryEscape(one.NextCursor))
	if len(two.Items) != 1 || two.Items[0].Status != "closed" || two.Items[0].ID == one.Items[0].ID {
		t.Fatalf("closed page 2: %+v", two)
	}

	// revoking the grant of B takes its pending/closed items out of every segment and out of the counters at once
	w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE user_id = $1 AND tenant_id = $2`, agent, w.tenant["B"])
	want("?segment=closed")
	_, afterRevoke := api.segment(agent, "")
	if c := *afterRevoke.Counts; c.Closed != 0 || c.Open != 3 || c.Waiting != 1 || c.Mine != 1 {
		t.Fatalf("counts after revoking B = %+v", c)
	}

	// somebody who is not of the hub gets the same 404 as ever, with or without a segment
	stranger := w.user("stranger")
	for _, q := range []string{"", "?segment=waiting", "?q=ana"} {
		if code, _ := api.segment(stranger, q); code != 404 {
			t.Errorf("stranger %q: %d, want 404", q, code)
		}
	}
}

// The tab of each company carries two numbers (ADR-0040 UX parity, the tenant tabs of the approved design): how many attendances are open and how
// many of those have customer messages nobody answered. Counted under the caller's own session: a company they hold no grant on is not even
// listed, and what they cannot read is not counted.
func TestHubInbox_CompaniesCarryTheCountersOfTheirTabs(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	agent := w.hubAgent("tabs")
	w.grant(agent, "A")
	w.grant(agent, "B")

	// A: three open (two unanswered) + one closed unanswered; B: one open, answered; C (no grant): unanswered, must never show
	w.exec(`UPDATE hub_inbox_items SET status = 'open', unread_count = 2 WHERE conversation_id = $1`, w.conv["A"])
	w.exec(`UPDATE hub_inbox_items SET status = 'open', unread_count = 1 WHERE conversation_id = $1`, w.conv["A2"])
	w.exec(`UPDATE hub_inbox_items SET status = 'open', unread_count = 0 WHERE conversation_id = $1`, w.conv["A0"])
	w.exec(`UPDATE hub_inbox_items SET status = 'open', unread_count = 0 WHERE conversation_id = $1`, w.conv["B"])
	w.exec(`UPDATE hub_inbox_items SET status = 'open', unread_count = 9 WHERE conversation_id = $1`, w.conv["C"])
	closedA := uuid.New()
	w.exec(`INSERT INTO conversations (id, tenant_id, contact_id, status) SELECT $1, tenant_id, contact_id, 'closed' FROM conversations WHERE id = $2`, closedA, w.conv["A"])
	w.exec(`INSERT INTO hub_inbox_items (hub_id, tenant_id, conversation_id, status, unread_count) VALUES ($1, $2, $3, 'closed', 5)`, w.hub, w.tenant["A"], closedA)

	type co struct {
		ID         uuid.UUID `json:"id"`
		Open       int       `json:"open"`
		Unanswered int       `json:"unanswered"`
	}
	read := func(q string) map[uuid.UUID]co {
		t.Helper()
		code, body, _ := api.do("GET", fmt.Sprintf("/api/v1/hubs/%s/inbox%s", w.hub, q), agent, nil)
		if code != 200 {
			t.Fatalf("%s: %d", q, code)
		}
		var b struct {
			Companies []co `json:"companies"`
		}
		w.must(json.Unmarshal([]byte(body), &b))
		m := map[uuid.UUID]co{}
		for _, c := range b.Companies {
			m[c.ID] = c
		}
		return m
	}
	m := read("")
	if len(m) != 2 {
		t.Fatalf("companies offered: %v", m)
	}
	if a := m[w.tenant["A"]]; a.Open != 3 || a.Unanswered != 2 {
		t.Errorf("A = %+v, want open 3, unanswered 2 (the closed one does not count)", a)
	}
	if b := m[w.tenant["B"]]; b.Open != 1 || b.Unanswered != 0 {
		t.Errorf("B = %+v, want open 1, unanswered 0", b)
	}
	if _, leaked := m[w.tenant["C"]]; leaked {
		t.Fatal("a company without a grant was listed with its counters")
	}
	// the counters of the tabs do not depend on the page's filter, segment or search (they describe the companies, not the page)
	m = read("?segment=closed&q=zzz&companies=" + w.tenant["B"].String())
	if m[w.tenant["A"]].Open != 3 || m[w.tenant["B"]].Open != 1 {
		t.Errorf("counters changed with the page filter: %+v", m)
	}
	// a revoked grant takes the company and its counters away at once
	w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE user_id = $1 AND tenant_id = $2`, agent, w.tenant["A"])
	m = read("")
	if _, still := m[w.tenant["A"]]; still || m[w.tenant["B"]].Open != 1 {
		t.Errorf("after revoking A: %+v", m)
	}
}

// The list answers from what the CONVERSATION says about who holds it and whether it is finalized, not from the projection, which the projector
// reconciles once a minute: after "Assumir" the attendance has to be in "Minhas" at once (and out of the pending queue), and after "Finalizar" in the history.
func TestHubInbox_SegmentsFollowTheConversationNotTheLaggingProjection(t *testing.T) {
	w := newWorld(t)
	api := newHubAPI(t, w)
	agent := w.hubAgent("live")
	w.grant(agent, "A")
	conv := w.conv["A"]
	// the projection still says: open and held by nobody
	w.exec(`UPDATE hub_inbox_items SET status = 'open', assigned_user_id = NULL, customer_name = 'Eva Ao Vivo' WHERE conversation_id = $1`, conv)

	in := func(q string) bool {
		t.Helper()
		code, p := api.segment(agent, q)
		if code != 200 {
			t.Fatalf("%s: %d", q, code)
		}
		_, ok := convsOf(p)[conv]
		return ok
	}
	if !in("?segment=waiting") || in("?segment=mine") {
		t.Fatal("setup: the pending conversation must be in the queue and not in mine")
	}
	// the agent takes it: the CONVERSATION changes, the projection has not run yet
	w.exec(`UPDATE conversations SET assigned_to_user_id = $2 WHERE id = $1`, conv, agent)
	if in("?segment=waiting") || !in("?segment=mine") {
		t.Fatal("after taking it the attendance is still in the pending queue / not in mine (the list followed the lagging projection)")
	}
	_, p := api.segment(agent, "?segment=mine")
	if it := convsOf(p)[conv]; it.Assignment != "me" {
		t.Errorf("assignment of the taken attendance = %q, want me", it.Assignment)
	}
	if c := *p.Counts; c.Mine != 1 || c.Waiting != 2 { // A2 and A0 still wait; the one taken left the queue
		t.Errorf("counters did not follow the conversation: %+v", c)
	}
	// finalized: the conversation closes, the projection has not run yet
	w.exec(`UPDATE conversations SET status = 'closed' WHERE id = $1`, conv)
	if in("?segment=open") || in("?segment=mine") || !in("?segment=closed") {
		t.Fatal("after finalizing, the attendance must be in the history and out of the queue")
	}
	// the tab of the company counts it as no longer open
	_, raw, _ := api.do("GET", "/api/v1/hubs/"+w.hub.String()+"/inbox?segment=closed", agent, nil)
	var b struct {
		Companies []struct {
			ID   uuid.UUID `json:"id"`
			Open int       `json:"open"`
		} `json:"companies"`
	}
	w.must(json.Unmarshal([]byte(raw), &b))
	for _, c := range b.Companies {
		if c.ID == w.tenant["A"] && c.Open != 2 { // A holds three conversations; one is finalized now
			t.Errorf("tab of A counts %d open, want 2", c.Open)
		}
	}
	// reading the conversation grants nothing the policy did not: a grant without the queue of the conversation does not see it, live or not
	w.exec(`UPDATE effective_access_grants SET status = 'revoked' WHERE user_id = $1 AND tenant_id = $2`, agent, w.tenant["A"])
	if in("?segment=closed") || in("") {
		t.Fatal("a revoked grant still sees the conversation through the live join")
	}
}
