package adapters

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// ContactItem is the read model served to the browser. It deliberately omits
// tenant_id: the tenant is already established by the session, and echoing it
// back only widens what a compromised client can learn.
type ContactItem struct {
	ID          uuid.UUID `json:"id"`
	DisplayName string    `json:"display_name"`
	PhoneE164   string    `json:"phone_e164"`
	Email       string    `json:"email"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ContactsAPIHandler struct {
	pool *pgxpool.Pool
}

func NewContactsAPIHandler(pool *pgxpool.Pool) *ContactsAPIHandler {
	return &ContactsAPIHandler{pool: pool}
}

const contactColumns = `id, display_name, phone_e164, email, status, created_at, updated_at`

// ListContacts returns one page of the TenantContext tenant's contacts, newest
// activity first. Ordering matches idx_contacts_tenant_updated so the cursor
// walks the index.
func (h *ContactsAPIHandler) ListContacts(w http.ResponseWriter, r *http.Request) {
	tenantID, err := requestTenant(r)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
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

	query := `SELECT ` + contactColumns + ` FROM contacts WHERE tenant_id = $1
		ORDER BY updated_at DESC, id DESC LIMIT $2`
	args := []any{tenantID, opts.Limit + 1}
	if cursor != nil {
		cursorID, parseErr := uuid.Parse(cursor.ID)
		if parseErr != nil {
			http.Error(w, "invalid pagination", http.StatusBadRequest)
			return
		}
		query = `SELECT ` + contactColumns + ` FROM contacts WHERE tenant_id = $1
			AND (updated_at, id) < ($2, $3)
			ORDER BY updated_at DESC, id DESC LIMIT $4`
		args = []any{tenantID, cursor.Timestamp, cursorID, opts.Limit + 1}
	}

	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "failed to list contacts", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]ContactItem, 0, opts.Limit)
	for rows.Next() {
		item, scanErr := scanContactItem(rows)
		if scanErr != nil {
			http.Error(w, "failed to read contacts", http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read contacts", http.StatusInternalServerError)
		return
	}

	hasMore := len(items) > opts.Limit
	if hasMore {
		items = items[:opts.Limit]
	}

	result := &pagination.PageResult{
		Items:   make([]interface{}, len(items)),
		HasMore: hasMore,
		Count:   len(items),
		Limit:   opts.Limit,
	}
	for i, item := range items {
		result.Items[i] = item
	}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		result.NextCursor = (&pagination.Cursor{ID: last.ID.String(), Timestamp: last.UpdatedAt}).Encode()
	}

	pagination.WritePaginationHeaders(w, result)
	writeJSON(w, result)
}

// GetContact returns one contact of the TenantContext tenant. A contact of
// another tenant and an unknown id are indistinguishable (404), so a caller
// cannot probe for ids that exist elsewhere.
func (h *ContactsAPIHandler) GetContact(w http.ResponseWriter, r *http.Request) {
	tenantID, err := requestTenant(r)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	contactID, err := uuid.Parse(r.PathValue("contact_id"))
	if err != nil {
		http.Error(w, "invalid contact_id", http.StatusBadRequest)
		return
	}

	item, err := scanContactItem(platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(),
		`SELECT `+contactColumns+` FROM contacts WHERE tenant_id = $1 AND id = $2`, tenantID, contactID))
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "contact not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read contact", http.StatusInternalServerError)
		return
	}
	writeJSON(w, item)
}

type contactRowScanner interface {
	Scan(dest ...any) error
}

func scanContactItem(row contactRowScanner) (ContactItem, error) {
	var item ContactItem
	err := row.Scan(&item.ID, &item.DisplayName, &item.PhoneE164, &item.Email, &item.Status, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

// The tenant comes from the session-established TenantContext, never from the
// path — the path segment is routing sugar only.
func requestTenant(r *http.Request) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, errors.New("contacts: tenant context not found")
	}
	return tc.TenantID, nil
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
