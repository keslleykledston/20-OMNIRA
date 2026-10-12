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

	w.exec(`UPDATE hub_inbox_items SET customer_name = 'Ana Pendente', assigned_user_id = NULL, status = 'open' WHERE conversation_id = $1`, waiting)
	w.exec(`UPDATE hub_inbox_items SET customer_name = 'Bruno Meu', assigned_user_id = $2, status = 'open' WHERE conversation_id = $1`, mine, agent)
	w.exec(`UPDATE hub_inbox_items SET customer_name = 'Carla do Colega', assigned_user_id = $2, status = 'open' WHERE conversation_id = $1`, theirs, colleague)
	w.exec(`UPDATE hub_inbox_items SET customer_name = 'Davi Encerrado', assigned_user_id = NULL, status = 'closed' WHERE conversation_id = $1`, closed)
	w.exec(`UPDATE hub_inbox_items SET customer_name = '100% Sob_Medida', assigned_user_id = $2, status = 'closed' WHERE conversation_id = $1`, closedMine, agent)

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
