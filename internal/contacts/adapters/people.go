package adapters

import (
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Unified people directory (ADR-0018 Wave 9). It lists two DIFFERENT kinds of subject side by side without merging them:
//
//	subject_type "contact"        an external person (classification, companies, conversations)
//	subject_type "internal_user"  a staff member (Users + Memberships). It has NO classification and is managed in the
//	                              access screens, never classified as customer/other.
//
// Internal users are visible only to whoever holds membership.read (the Team permission); everyone else gets the contacts
// part of the same list, so a role never learns who the staff is through this endpoint.

const permMembershipRead = "membership.read"

// PersonContact is a contact row: every ContactItem field plus a summary of its companies.
type PersonContact struct {
	SubjectType string `json:"subject_type"`
	ContactItem
	AccountCount       int     `json:"account_count"`
	PrimaryAccountName *string `json:"primary_account_name,omitempty"`
}

// PersonInternalUser is a staff row. There is deliberately no kind/classification field.
type PersonInternalUser struct {
	SubjectType string    `json:"subject_type"`
	ID          uuid.UUID `json:"id"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"`
	// Status is the MEMBERSHIP status: active | inactive.
	Status   string `json:"status"`
	RoleKey  string `json:"role_key"`
	RoleName string `json:"role_name"`
	// ManagePath is where access is managed (the SPA route); the directory never edits staff itself.
	ManagePath string    `json:"manage_path"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type peopleView struct {
	contacts     bool
	internal     bool
	contactKinds []string // nil = every kind except spam
}

func parsePeopleView(v string) (peopleView, bool) {
	switch v {
	case "", "all":
		return peopleView{contacts: true, internal: true}, true
	case "customers":
		return peopleView{contacts: true, contactKinds: []string{"customer"}}, true
	case "others":
		return peopleView{contacts: true, contactKinds: []string{"other"}}, true
	case "unclassified":
		return peopleView{contacts: true, contactKinds: []string{"unclassified"}}, true
	case "spam":
		return peopleView{contacts: true, contactKinds: []string{"spam"}}, true
	case "internal":
		return peopleView{internal: true}, true
	}
	return peopleView{}, false
}

// ListPeople: GET /tenants/{tenant_id}/people?view=all|customers|others|unclassified|spam|internal&q=&status=&limit=&cursor=
func (h *ContactsAPIHandler) ListPeople(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	view, ok := parsePeopleView(r.URL.Query().Get("view"))
	if !ok {
		http.Error(w, "invalid view", http.StatusBadRequest)
		return
	}
	canSeeStaff, err := h.hasPermission(r, tc, permMembershipRead)
	if err != nil {
		http.Error(w, "failed to check permission", http.StatusInternalServerError)
		return
	}
	if view.internal && !canSeeStaff {
		if !view.contacts {
			http.Error(w, "forbidden", http.StatusForbidden) // the Internos view needs membership.read
			return
		}
		view.internal = false // "all" without it: the contacts part only
	}
	opts := pagination.ParsePageOptionsFromQuery(r)
	if opts.Sort != "" && opts.Sort != "updated_at:desc" {
		http.Error(w, "unsupported sort", http.StatusBadRequest)
		return
	}
	cursor, err := pagination.DecodeCursor(opts.Cursor)
	if err != nil {
		http.Error(w, "invalid pagination", http.StatusBadRequest)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 100 {
		http.Error(w, "invalid search", http.StatusBadRequest)
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "active" && status != "blocked" && status != "archived" && status != "inactive" {
		http.Error(w, "invalid status filter", http.StatusBadRequest)
		return
	}

	args := []any{tc.TenantID}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	var branches []string
	if view.contacts {
		where := "c.tenant_id = $1"
		if view.contactKinds == nil {
			where += " AND c.kind <> 'spam'"
		} else {
			where += " AND c.kind = ANY(" + arg(view.contactKinds) + ")"
		}
		if q != "" {
			like := arg("%" + escapeLike(q) + "%")
			clause := "c.display_name ILIKE " + like + " OR c.email ILIKE " + like
			if digits, ok := phoneDigits(q); ok {
				clause += " OR c.phone_e164 LIKE " + arg("%"+digits+"%")
			}
			where += " AND (" + clause + ")"
		}
		if status != "" {
			if status == "inactive" {
				where += " AND false" // staff-only status
			} else {
				where += " AND c.status = " + arg(status)
			}
		}
		branches = append(branches, `SELECT 'contact'::text AS subject_type, c.id AS id, c.updated_at AS activity FROM contacts c WHERE `+where)
	}
	if view.internal {
		where := "m.tenant_id = $1 AND m.status IN ('active','inactive')"
		if q != "" {
			like := arg("%" + escapeLike(q) + "%")
			where += " AND (COALESCE(u.display_name,'') ILIKE " + like + " OR COALESCE(u.email,'') ILIKE " + like + ")"
		}
		switch status {
		case "":
		case "active", "inactive":
			where += " AND m.status = " + arg(status)
		default: // blocked / archived are contact lifecycle states
			where += " AND false"
		}
		branches = append(branches, `SELECT 'internal_user'::text AS subject_type, m.user_id AS id, m.updated_at AS activity FROM memberships m JOIN users u ON u.id = m.user_id WHERE `+where)
	}
	page := "SELECT subject_type, id, activity FROM (" + strings.Join(branches, " UNION ALL ") + ") people"
	if cursor != nil {
		cid, perr := uuid.Parse(cursor.ID)
		if perr != nil {
			http.Error(w, "invalid pagination", http.StatusBadRequest)
			return
		}
		page += " WHERE (activity, id) < (" + arg(cursor.Timestamp) + ", " + arg(cid) + ")"
	}
	page += " ORDER BY activity DESC, id DESC LIMIT " + arg(opts.Limit+1)

	ctx := r.Context()
	dbq := platformdb.QuerierFromContext(ctx, h.pool)
	rows, err := dbq.Query(ctx, page, args...)
	if err != nil {
		log.Printf("contacts: list people: %v", err)
		http.Error(w, "failed to list people", http.StatusInternalServerError)
		return
	}
	type key struct {
		subject  string
		id       uuid.UUID
		activity time.Time
	}
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.subject, &k.id, &k.activity); err != nil {
			rows.Close()
			http.Error(w, "failed to read people", http.StatusInternalServerError)
			return
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read people", http.StatusInternalServerError)
		return
	}
	hasMore := len(keys) > opts.Limit
	if hasMore {
		keys = keys[:opts.Limit]
	}

	// hydrate the page: contacts with their derived facts and company summary, staff with their role
	var contactIDs, userIDs []uuid.UUID
	for _, k := range keys {
		if k.subject == "contact" {
			contactIDs = append(contactIDs, k.id)
		} else {
			userIDs = append(userIDs, k.id)
		}
	}
	contacts := map[uuid.UUID]PersonContact{}
	if len(contactIDs) > 0 {
		crows, err := dbq.Query(ctx, contactSelect+` WHERE c.tenant_id = $1 AND c.id = ANY($2)`, tc.TenantID, contactIDs)
		if err != nil {
			http.Error(w, "failed to read people", http.StatusInternalServerError)
			return
		}
		for crows.Next() {
			it, err := scanContactItem(crows)
			if err != nil {
				crows.Close()
				http.Error(w, "failed to read people", http.StatusInternalServerError)
				return
			}
			contacts[it.ID] = PersonContact{SubjectType: "contact", ContactItem: it}
		}
		crows.Close()
		arows, err := dbq.Query(ctx, `
			SELECT l.contact_id, count(*)::int, max(a.name) FILTER (WHERE l.is_primary)
			FROM contact_account_links l JOIN customer_accounts a ON a.tenant_id = l.tenant_id AND a.id = l.account_id
			WHERE l.tenant_id = $1 AND l.contact_id = ANY($2) AND l.status = 'active'
			GROUP BY l.contact_id`, tc.TenantID, contactIDs)
		if err != nil {
			http.Error(w, "failed to read people", http.StatusInternalServerError)
			return
		}
		for arows.Next() {
			var id uuid.UUID
			var n int
			var name *string
			if err := arows.Scan(&id, &n, &name); err != nil {
				arows.Close()
				http.Error(w, "failed to read people", http.StatusInternalServerError)
				return
			}
			p := contacts[id]
			p.AccountCount, p.PrimaryAccountName = n, name
			contacts[id] = p
		}
		arows.Close()
	}
	staff := map[uuid.UUID]PersonInternalUser{}
	if len(userIDs) > 0 {
		srows, err := dbq.Query(ctx, `
			SELECT u.id, COALESCE(NULLIF(u.display_name,''), u.email, ''), COALESCE(u.email,''), m.status, r.key, r.name, m.updated_at
			FROM memberships m JOIN users u ON u.id = m.user_id JOIN roles r ON r.id = m.role_id
			WHERE m.tenant_id = $1 AND m.user_id = ANY($2)`, tc.TenantID, userIDs)
		if err != nil {
			http.Error(w, "failed to read people", http.StatusInternalServerError)
			return
		}
		for srows.Next() {
			p := PersonInternalUser{SubjectType: "internal_user", ManagePath: "/settings/team"}
			if err := srows.Scan(&p.ID, &p.DisplayName, &p.Email, &p.Status, &p.RoleKey, &p.RoleName, &p.UpdatedAt); err != nil {
				srows.Close()
				http.Error(w, "failed to read people", http.StatusInternalServerError)
				return
			}
			staff[p.ID] = p
		}
		srows.Close()
	}

	result := &pagination.PageResult{Items: make([]interface{}, 0, len(keys)), HasMore: hasMore, Limit: opts.Limit}
	for _, k := range keys {
		if k.subject == "contact" {
			if p, ok := contacts[k.id]; ok {
				result.Items = append(result.Items, p)
			}
		} else if p, ok := staff[k.id]; ok {
			result.Items = append(result.Items, p)
		}
	}
	result.Count = len(result.Items)
	if hasMore && len(keys) > 0 {
		last := keys[len(keys)-1]
		result.NextCursor = (&pagination.Cursor{ID: last.id.String(), Timestamp: last.activity}).Encode()
	}
	pagination.WritePaginationHeaders(w, result)
	writeJSON(w, result)
}
