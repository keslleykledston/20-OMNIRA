package adapters

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// The Tickets screen sorts by column and shows how many tickets match: the order is the database's (keyset, so a page boundary never repeats or skips a row,
// even among equal values), the count covers every page, and it never counts another company's tickets.
func TestListTicketsSortsByColumnWalksEveryPageOnceAndCountsTheMatches(t *testing.T) {
	seed, app := seedPool(t), appPool(t)
	tenantID, other := seedTenant(t, seed, "sorting"), seedTenant(t, seed, "sorting-other")
	userID := seedMember(t, seed, tenantID, "tenant_admin", "active")

	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	// the same priority and the same moment on purpose: the id has to break the tie
	seedTicket(t, seed, tenantID, "beta", "open", "medium", base.Add(3*time.Minute))
	seedTicket(t, seed, tenantID, "Alpha", "open", "low", base.Add(1*time.Minute))
	seedTicket(t, seed, tenantID, "gamma", "open", "critical", base.Add(2*time.Minute))
	seedTicket(t, seed, tenantID, "delta", "open", "medium", base.Add(3*time.Minute))
	seedTicket(t, seed, tenantID, "epsilon", "open", "high", base.Add(4*time.Minute))
	seedTicket(t, seed, other, "of another company", "open", "critical", base)

	h := NewHandler(app)
	walk := func(sort string, limit string, extra string) ([]string, int) {
		t.Helper()
		var seen []string
		total := -1
		cursor := ""
		for i := 0; i < 20; i++ {
			q := url.Values{"limit": {limit}, "sort": {sort}}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets?"+q.Encode()+extra, h.List)
			if rec.Code != http.StatusOK {
				t.Fatalf("sort %s page %d = %d %q", sort, i, rec.Code, rec.Body.String())
			}
			p := decodeTotalPage(t, rec)
			for _, it := range p.Items {
				seen = append(seen, it["subject"].(string))
			}
			if p.Total == nil {
				t.Fatalf("sort %s: no total on page %d", sort, i)
			}
			if total != -1 && total != *p.Total {
				t.Fatalf("sort %s: the total changed between pages: %d then %d", sort, total, *p.Total)
			}
			total = *p.Total
			if !p.HasMore {
				break
			}
			cursor = p.NextCursor
		}
		return seen, total
	}
	same := func(got []string, want ...string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	// subject: case-insensitive, both ways
	if got, total := walk("subject:asc", "2", ""); !same(got, "Alpha", "beta", "delta", "epsilon", "gamma") || total != 5 {
		t.Errorf("subject:asc = %v (total %d)", got, total)
	}
	if got, _ := walk("subject:desc", "2", ""); !same(got, "gamma", "epsilon", "delta", "beta", "Alpha") {
		t.Errorf("subject:desc = %v", got)
	}
	// priority: most urgent first, or last; ties (beta/delta share medium) keep a stable, gap-free order across page boundaries
	prio, _ := walk("priority:desc", "2", "")
	if len(prio) != 5 || prio[0] != "gamma" || prio[1] != "epsilon" || prio[4] != "Alpha" {
		t.Errorf("priority:desc = %v", prio)
	}
	if got, _ := walk("priority:asc", "1", ""); len(got) != 5 || got[0] != "Alpha" || got[4] != "gamma" {
		t.Errorf("priority:asc (page size 1) = %v", got)
	}
	// the page size does not change the order, and no row repeats or is skipped
	for _, size := range []string{"1", "2", "3", "5"} {
		got, _ := walk("priority:desc", size, "")
		if !same(got, prio...) {
			t.Errorf("priority:desc with limit %s = %v, want %v", size, got, prio)
		}
	}
	// oldest activity first, equal moments broken by id (beta and delta together, in a fixed order)
	if got, _ := walk("updated_at:asc", "2", ""); len(got) != 5 || got[0] != "Alpha" || got[1] != "gamma" || got[4] != "epsilon" {
		t.Errorf("updated_at:asc = %v", got)
	}
	// the total follows the filters and stays inside the company
	if got, total := walk("subject:asc", "2", "&priority=medium"); !same(got, "beta", "delta") || total != 2 {
		t.Errorf("filtered = %v (total %d)", got, total)
	}
	// the default order keeps its cursor and also reports the total
	rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets?limit=2", h.List)
	if p := decodeTotalPage(t, rec); p.Total == nil || *p.Total != 5 || len(p.Items) != 2 || p.Items[0]["subject"] != "epsilon" {
		t.Errorf("default order = %+v", p)
	}

	// refused: an ordering we do not offer, and a cursor carried over from another ordering
	if rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets?sort=assigned_to:asc", h.List); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown sort = %d, want 400", rec.Code)
	}
	first := decodeTotalPage(t, callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets?limit=1&sort=subject:asc", h.List))
	if first.NextCursor == "" {
		t.Fatal("no cursor to try")
	}
	if rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets?limit=1&sort=subject:desc&cursor="+first.NextCursor, h.List); rec.Code != http.StatusBadRequest {
		t.Errorf("a cursor of another ordering = %d, want 400", rec.Code)
	}
	if rec := callAsTenant(t, app, tenantID, userID, "/api/v1/tenants/"+tenantID.String()+"/tickets?limit=1&cursor="+first.NextCursor, h.List); rec.Code != http.StatusBadRequest {
		t.Errorf("a key cursor on the default order = %d, want 400", rec.Code)
	}
}

type totalPage struct {
	page
	Total *int `json:"total"`
}

func decodeTotalPage(t *testing.T, rec *httptest.ResponseRecorder) totalPage {
	t.Helper()
	var body totalPage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode page: %v (body=%s)", err, rec.Body.String())
	}
	return body
}
