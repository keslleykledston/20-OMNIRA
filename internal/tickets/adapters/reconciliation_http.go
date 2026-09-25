package adapters

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// PRODUCT.7A1: read-only tenant ticket reconciliation/audit surface over
// the two durable attempt tables. Gated on ticket.reconcile — deliberately
// NOT ticket.read or ticket.update (see migration 000051's own doc
// comment): this exposes every actor's identity and redacted idempotency/
// reconciliation state across the tenant, a materially more sensitive
// capability than either existing permission implies. No provider runtime
// is ever resolved here — provider calls are zero by construction, not by
// convention.

var validAttemptState = map[string]bool{
	"in_flight": true, "confirmed_success": true, "confirmed_failure": true, "outcome_unknown": true,
}

// authorizeTicketReconcile mirrors authorizeTicketRead exactly, gated on
// the distinct ticket.reconcile permission.
func (h *Handler) authorizeTicketReconcile(r *http.Request, tc *tenancydomain.TenantContext) error {
	var ok bool
	err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key='ticket.reconcile')`,
		tc.TenantID, tc.ActorID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return errPermissionDenied
	}
	return nil
}

// redactIdempotencyKey never returns the full key over this read-only
// surface (section 11): this slice has no retry/replay trigger that would
// operationally need it. Every idempotency_key is at least 8 chars
// (ticket_external_*_attempts_idempotency_key_check).
func redactIdempotencyKey(key string) string {
	const visible = 8
	if len(key) <= visible {
		return key + "…"
	}
	return key[:visible] + "…"
}

// reconciliationFilters is shared by both list endpoints — state/provider/
// external_ticket_id/date-range are the same shape on both tables.
type reconciliationFilters struct {
	state            string
	provider         string
	externalTicketID string
	createdFrom      *time.Time
	createdTo        *time.Time
}

func parseReconciliationFilters(r *http.Request) (reconciliationFilters, error) {
	var f reconciliationFilters
	f.state = r.URL.Query().Get("state")
	if f.state != "" && !validAttemptState[f.state] {
		return f, errors.New("invalid state")
	}
	f.provider = r.URL.Query().Get("provider")
	f.externalTicketID = r.URL.Query().Get("external_ticket_id")
	if v := r.URL.Query().Get("created_from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, errors.New("invalid created_from")
		}
		f.createdFrom = &t
	}
	if v := r.URL.Query().Get("created_to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			return f, errors.New("invalid created_to")
		}
		f.createdTo = &t
	}
	return f, nil
}

// reconciliationFilterWhere builds the shared tenant+filters WHERE clause
// against alias "a" (the attempt table); callers append their own
// cursor/limit predicates and args afterwards. Never touches RLS — the
// tenant_id predicate here is defense in depth on top of forced RLS, the
// same pattern ticketFilterWhere already uses for /tickets.
func reconciliationFilterWhere(tenantID uuid.UUID, f reconciliationFilters) (string, []any) {
	where := `WHERE a.tenant_id = $1`
	args := []any{tenantID}
	if f.state != "" {
		args = append(args, f.state)
		where += ` AND a.state = $` + strconv.Itoa(len(args))
	}
	if f.provider != "" {
		args = append(args, f.provider)
		where += ` AND a.provider = $` + strconv.Itoa(len(args))
	}
	if f.externalTicketID != "" {
		args = append(args, f.externalTicketID)
		where += ` AND a.external_ticket_id = $` + strconv.Itoa(len(args))
	}
	if f.createdFrom != nil {
		args = append(args, *f.createdFrom)
		where += ` AND a.created_at >= $` + strconv.Itoa(len(args))
	}
	if f.createdTo != nil {
		args = append(args, *f.createdTo)
		where += ` AND a.created_at <= $` + strconv.Itoa(len(args))
	}
	return where, args
}

// ReconciliationActor is the canonical safe user projection (id +
// display_name + email), the exact same shape already used by
// internal/tenancy/adapters (agent_profiles_http.go, team_http.go) — no
// internal auth/security field ever included.
type ReconciliationActor struct {
	ID          uuid.UUID `json:"id"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"`
}

// CreateAttemptItem is the read model for ticket_external_create_attempts.
// local_ticket_id/local_ticket_subject are nullable: a create attempt has
// no local ticket until the provider confirms success (section 10) —
// that row must remain visible, never dropped.
type CreateAttemptItem struct {
	ID                     uuid.UUID  `json:"id"`
	State                  string     `json:"state"`
	ConversationID         uuid.UUID  `json:"conversation_id"`
	LocalTicketID          *uuid.UUID `json:"local_ticket_id"`
	LocalTicketSubject     *string    `json:"local_ticket_subject"`
	Provider               *string    `json:"provider"`
	ExternalTicketID       *string    `json:"external_ticket_id"`
	Actor                  ReconciliationActor `json:"actor"`
	CreatedAt              time.Time  `json:"created_at"`
	UpdatedAt              time.Time  `json:"updated_at"`
	ProjectionSyncedAt     *time.Time `json:"projection_synced_at"`
	IdempotencyKeyRedacted string     `json:"idempotency_key_redacted"`
}

const reconciliationCreateColumns = `a.id, a.state, a.conversation_id, a.local_ticket_id, a.provider, a.external_ticket_id,
	a.actor_user_id, COALESCE(u.display_name,''), COALESCE(u.email,''),
	a.created_at, a.updated_at, a.projection_synced_at, a.idempotency_key, t.subject`

func scanCreateAttemptItem(row interface{ Scan(dest ...any) error }) (CreateAttemptItem, error) {
	var item CreateAttemptItem
	var rawKey string
	err := row.Scan(&item.ID, &item.State, &item.ConversationID, &item.LocalTicketID, &item.Provider, &item.ExternalTicketID,
		&item.Actor.ID, &item.Actor.DisplayName, &item.Actor.Email,
		&item.CreatedAt, &item.UpdatedAt, &item.ProjectionSyncedAt, &rawKey, &item.LocalTicketSubject)
	if err != nil {
		return item, err
	}
	item.IdempotencyKeyRedacted = redactIdempotencyKey(rawKey)
	return item, nil
}

// ListCreateAttempts — GET /tickets-reconciliation/create. Read-only,
// GET-only (no POST/PATCH/DELETE registered for this path — see
// server.go), zero provider calls: no TicketingRuntimeResolver/
// TicketingConnector is ever referenced by this handler.
func (h *Handler) ListCreateAttempts(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	if err := h.authorizeTicketReconcile(r, tc); err != nil {
		if errors.Is(err, errPermissionDenied) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
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

	filters, filterErr := parseReconciliationFilters(r)
	if filterErr != nil {
		http.Error(w, filterErr.Error(), http.StatusBadRequest)
		return
	}

	where, args := reconciliationFilterWhere(tc.TenantID, filters)
	if cursor != nil {
		cursorID, parseErr := uuid.Parse(cursor.ID)
		if parseErr != nil {
			http.Error(w, "invalid pagination", http.StatusBadRequest)
			return
		}
		args = append(args, cursor.Timestamp, cursorID)
		where += ` AND (a.updated_at, a.id) < ($` + strconv.Itoa(len(args)-1) + `, $` + strconv.Itoa(len(args)) + `)`
	}
	args = append(args, opts.Limit+1)
	query := `SELECT ` + reconciliationCreateColumns + `
		FROM ticket_external_create_attempts a
		JOIN users u ON u.id = a.actor_user_id
		LEFT JOIN tickets t ON t.tenant_id = a.tenant_id AND t.id = a.local_ticket_id
		` + where + ` ORDER BY a.updated_at DESC, a.id DESC LIMIT $` + strconv.Itoa(len(args))

	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "failed to list create attempts", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]CreateAttemptItem, 0, opts.Limit)
	for rows.Next() {
		item, scanErr := scanCreateAttemptItem(rows)
		if scanErr != nil {
			http.Error(w, "failed to read create attempts", http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read create attempts", http.StatusInternalServerError)
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

// StatusAttemptItem is the read model for ticket_external_status_attempts.
// Unlike CreateAttemptItem, local_ticket_id/provider/external_ticket_id
// are always present (a status mutation only ever targets an
// already-linked ticket) — kept as non-pointer fields to make that
// invariant visible in the type itself, per section 8 (no artificial
// shared model with the create-attempt DTO).
type StatusAttemptItem struct {
	ID                           uuid.UUID  `json:"id"`
	State                        string     `json:"state"`
	ConversationID               uuid.UUID  `json:"conversation_id"`
	LocalTicketID                uuid.UUID  `json:"local_ticket_id"`
	LocalTicketSubject           *string    `json:"local_ticket_subject"`
	Provider                     string     `json:"provider"`
	ExternalTicketID             string     `json:"external_ticket_id"`
	TargetStatus                 string     `json:"target_status"`
	ConfirmedExternalStatus      *string    `json:"confirmed_external_status"`
	ConfirmedExternalStatusLabel *string    `json:"confirmed_external_status_label"`
	Actor                        ReconciliationActor `json:"actor"`
	CreatedAt                    time.Time  `json:"created_at"`
	UpdatedAt                    time.Time  `json:"updated_at"`
	ProjectionSyncedAt           *time.Time `json:"projection_synced_at"`
	IdempotencyKeyRedacted       string     `json:"idempotency_key_redacted"`
}

const reconciliationStatusColumns = `a.id, a.state, a.conversation_id, a.local_ticket_id, a.provider, a.external_ticket_id,
	a.target_status, a.confirmed_external_status, a.confirmed_external_status_label,
	a.actor_user_id, COALESCE(u.display_name,''), COALESCE(u.email,''),
	a.created_at, a.updated_at, a.projection_synced_at, a.idempotency_key, t.subject`

func scanStatusAttemptItem(row interface{ Scan(dest ...any) error }) (StatusAttemptItem, error) {
	var item StatusAttemptItem
	var rawKey string
	err := row.Scan(&item.ID, &item.State, &item.ConversationID, &item.LocalTicketID, &item.Provider, &item.ExternalTicketID,
		&item.TargetStatus, &item.ConfirmedExternalStatus, &item.ConfirmedExternalStatusLabel,
		&item.Actor.ID, &item.Actor.DisplayName, &item.Actor.Email,
		&item.CreatedAt, &item.UpdatedAt, &item.ProjectionSyncedAt, &rawKey, &item.LocalTicketSubject)
	if err != nil {
		return item, err
	}
	item.IdempotencyKeyRedacted = redactIdempotencyKey(rawKey)
	return item, nil
}

// ListStatusAttempts — GET /tickets-reconciliation/status. Same
// read-only/GET-only/zero-provider-call guarantees as ListCreateAttempts.
func (h *Handler) ListStatusAttempts(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	if err := h.authorizeTicketReconcile(r, tc); err != nil {
		if errors.Is(err, errPermissionDenied) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "internal server error", http.StatusInternalServerError)
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

	filters, filterErr := parseReconciliationFilters(r)
	if filterErr != nil {
		http.Error(w, filterErr.Error(), http.StatusBadRequest)
		return
	}

	where, args := reconciliationFilterWhere(tc.TenantID, filters)
	if cursor != nil {
		cursorID, parseErr := uuid.Parse(cursor.ID)
		if parseErr != nil {
			http.Error(w, "invalid pagination", http.StatusBadRequest)
			return
		}
		args = append(args, cursor.Timestamp, cursorID)
		where += ` AND (a.updated_at, a.id) < ($` + strconv.Itoa(len(args)-1) + `, $` + strconv.Itoa(len(args)) + `)`
	}
	args = append(args, opts.Limit+1)
	query := `SELECT ` + reconciliationStatusColumns + `
		FROM ticket_external_status_attempts a
		JOIN users u ON u.id = a.actor_user_id
		LEFT JOIN tickets t ON t.tenant_id = a.tenant_id AND t.id = a.local_ticket_id
		` + where + ` ORDER BY a.updated_at DESC, a.id DESC LIMIT $` + strconv.Itoa(len(args))

	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "failed to list status attempts", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]StatusAttemptItem, 0, opts.Limit)
	for rows.Next() {
		item, scanErr := scanStatusAttemptItem(rows)
		if scanErr != nil {
			http.Error(w, "failed to read status attempts", http.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read status attempts", http.StatusInternalServerError)
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
