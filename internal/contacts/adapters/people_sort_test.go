package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The Contacts screen sorts by name and shows how many people match: contacts and staff together, case-insensitively, page after page without repeating
// or skipping anyone (even two people with the same name), and the count never reaches another company.
func TestPeopleSortByNameWalksEveryPageOnceAndCountsTheMatches(t *testing.T) {
	e := newClsEnv(t)
	a, b := seedTenant(t, e.seed, "people-sort-a"), seedTenant(t, e.seed, "people-sort-b")
	admin := seedMemberRole(t, e.seed, a, "tenant_admin", "active")
	adminB := seedMemberRole(t, e.seed, b, "tenant_admin", "active")
	if _, err := e.seed.Exec(context.Background(), `UPDATE users SET display_name='Marta Admin' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, n := range []string{"carlos", "Ana", "bruno", "Ana", "Zeca"} { // two Anas on purpose: the id breaks the tie
		seedContact(t, e.seed, a, n, "+55929566000"+string(rune('0'+i)), now.Add(-time.Duration(i)*time.Minute))
	}
	seedContact(t, e.seed, b, "Aaron do outro tenant", "+5592956700001", now)
	_ = adminB

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
			h := NewContactsAPIHandler(e.app)
			rec := call(t, e.app, a, admin, http.MethodGet, "/?"+q.Encode()+extra, "", nil, h.ListPeople)
			if rec.Code != 200 {
				t.Fatalf("sort %s page %d = %d %s", sort, i, rec.Code, rec.Body.String())
			}
			var page struct {
				Items      []peopleRow `json:"items"`
				HasMore    bool        `json:"has_more"`
				NextCursor string      `json:"next_cursor"`
				Total      *int        `json:"total"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			seen = append(seen, names(page.Items)...)
			if page.Total == nil {
				t.Fatalf("sort %s: no total on page %d", sort, i)
			}
			if total != -1 && total != *page.Total {
				t.Fatalf("sort %s: the total changed between pages", sort)
			}
			total = *page.Total
			if !page.HasMore {
				break
			}
			cursor = page.NextCursor
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

	// case-insensitive, staff among the contacts (Marta), both ways, for any page size
	wantAsc := []string{"Ana", "Ana", "bruno", "carlos", "Marta Admin", "Zeca"}
	for _, size := range []string{"1", "2", "4", "20"} {
		if got, total := walk("name:asc", size, ""); !same(got, wantAsc...) || total != 6 {
			t.Errorf("name:asc limit %s = %v (total %d)", size, got, total)
		}
	}
	wantDesc := []string{"Zeca", "Marta Admin", "carlos", "bruno", "Ana", "Ana"}
	for _, size := range []string{"1", "3"} {
		if got, _ := walk("name:desc", size, ""); !same(got, wantDesc...) {
			t.Errorf("name:desc limit %s = %v", size, got)
		}
	}
	// the count follows the filters and stays inside the company
	if got, total := walk("name:asc", "1", "&q=ana"); !same(got, "Ana", "Ana") || total != 2 {
		t.Errorf("filtered = %v (total %d)", got, total)
	}
	// the default order keeps its cursor and reports the total too
	h := NewContactsAPIHandler(e.app)
	rec := call(t, e.app, a, admin, http.MethodGet, "/?limit=2", "", nil, h.ListPeople)
	var def struct {
		Total *int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &def); err != nil || def.Total == nil || *def.Total != 6 {
		t.Errorf("default order total = %s", rec.Body.String())
	}

	// refused: an ordering we do not offer, and a cursor carried over from another ordering
	if rec := call(t, e.app, a, admin, http.MethodGet, "/?sort=phone:asc", "", nil, h.ListPeople); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown sort = %d, want 400", rec.Code)
	}
	var first struct {
		NextCursor string `json:"next_cursor"`
	}
	rec = call(t, e.app, a, admin, http.MethodGet, "/?limit=1&sort=name:asc", "", nil, h.ListPeople)
	_ = json.Unmarshal(rec.Body.Bytes(), &first)
	if first.NextCursor == "" {
		t.Fatal("no cursor to try")
	}
	for _, q := range []string{"limit=1&sort=name:desc&cursor=", "limit=1&cursor="} {
		if rec := call(t, e.app, a, admin, http.MethodGet, "/?"+q+url.QueryEscape(first.NextCursor), "", nil, h.ListPeople); rec.Code != http.StatusBadRequest {
			t.Errorf("%q with a cursor of another ordering = %d, want 400", q, rec.Code)
		}
	}
	_ = uuid.Nil
}
