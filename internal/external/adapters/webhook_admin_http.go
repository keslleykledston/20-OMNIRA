package adapters

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	"github.com/omnira/omnira/internal/external/domain"
	"github.com/omnira/omnira/internal/external/netpolicy"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Sealer encrypts a signing secret for storage (AES-256-GCM with the platform key, ADR-0009).
type Sealer interface {
	Encrypt(plaintext []byte) ([]byte, error)
}

// WebhookHandler is the administration of the webhook subscriptions of an installation. Same rules as AdminHandler (a person with integration.read /
// integration.manage; never delegable to the Hub). The signing secret appears once, in the response that created or rotated it, and is not readable afterwards
// (the database grants no SELECT on the ciphertext).
type WebhookHandler struct {
	*AdminHandler
	sealer Sealer
	secret func() (secret, keyID string, raw []byte, err error)
}

func NewWebhookHandler(admin *AdminHandler, sealer Sealer, newSecret func() (string, string, []byte, error)) *WebhookHandler {
	return &WebhookHandler{AdminHandler: admin, sealer: sealer, secret: newSecret}
}

const maxSubscriptionsPerInstallation = 5

var allEventTypes = []string{"conversation.created.v1", "conversation.control_changed.v1", "conversation.closed.v1", "message.created.v1", "message.status_changed.v1", "contact.updated.v1", "ticket.linked.v1"}

func validEventTypes(in []string) ([]string, bool) {
	if len(in) == 0 {
		return nil, false
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range in {
		ok := false
		for _, a := range allEventTypes {
			if a == t {
				ok = true
			}
		}
		if !ok {
			return nil, false
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, true
}

type subscriptionView struct {
	ID                  uuid.UUID  `json:"id"`
	URL                 string     `json:"url"`
	EventTypes          []string   `json:"event_types"`
	Status              string     `json:"status"`
	KeyID               string     `json:"key_id"`
	PreviousKeyID       *string    `json:"previous_key_id"`
	PreviousValidUntil  *time.Time `json:"previous_valid_until"`
	EnqueuedThroughSeq  int64      `json:"enqueued_through_seq"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	LastDeliveryAt      *time.Time `json:"last_delivery_at"`
	LastHTTPStatus      *int       `json:"last_http_status"`
	LastErrorClass      *string    `json:"last_error_class"`
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

const subscriptionColumns = `id, url, event_types, status, key_id, previous_key_id, previous_valid_until, enqueued_through_seq, consecutive_failures,
	last_delivery_at, last_http_status, last_error_class, created_at, updated_at`

func scanSubscription(row pgx.Row) (subscriptionView, error) {
	var v subscriptionView
	err := row.Scan(&v.ID, &v.URL, &v.EventTypes, &v.Status, &v.KeyID, &v.PreviousKeyID, &v.PreviousValidUntil, &v.EnqueuedThroughSeq, &v.ConsecutiveFailures,
		&v.LastDeliveryAt, &v.LastHTTPStatus, &v.LastErrorClass, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func hookID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("hook_id"))
	if err != nil {
		http.Error(w, "invalid hook_id", http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

// installationOf checks that the installation exists in the tenant and is not revoked.
func (h *WebhookHandler) installationOf(w http.ResponseWriter, r *http.Request, q platformdb.Querier, tenant uuid.UUID) (domain.Installation, bool) {
	id, ok := installationID(w, r)
	if !ok {
		return domain.Installation{}, false
	}
	in, err := h.store.Get(r.Context(), q, tenant, id)
	if err != nil {
		mapStoreError(w, err)
		return in, false
	}
	return in, true
}

// List: GET /tenants/{t}/integrations/apps/{app_id}/webhooks
func (h *WebhookHandler) List(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permRead)
	if !ok {
		return
	}
	in, ok := h.installationOf(w, r, q, tc.TenantID)
	if !ok {
		return
	}
	rows, err := q.Query(r.Context(), `SELECT `+subscriptionColumns+` FROM webhook_subscriptions WHERE tenant_id = $1 AND installation_id = $2 ORDER BY created_at, id`, tc.TenantID, in.ID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []subscriptionView{}
	for rows.Next() {
		v, err := scanSubscription(rows)
		if err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "count": len(out)})
}

type createHookRequest struct {
	URL        string   `json:"url"`
	EventTypes []string `json:"event_types"`
}

// Create: POST .../webhooks — returns the subscription and its signing secret (once).
func (h *WebhookHandler) Create(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permManage)
	if !ok {
		return
	}
	in, ok := h.installationOf(w, r, q, tc.TenantID)
	if !ok {
		return
	}
	if in.Status == domain.StatusRevoked {
		mapStoreError(w, domain.ErrInstallationRevoked)
		return
	}
	var req createHookRequest
	if !decodeBody(w, r, &req) {
		return
	}
	types, ok := validEventTypes(req.EventTypes)
	if !ok {
		http.Error(w, "event_types must list at least one known event type", http.StatusBadRequest)
		return
	}
	if err := netpolicy.ValidateURL(req.URL); err != nil {
		http.Error(w, "the webhook URL must be https, on a public address, with no credentials, on port 443 or 8443", http.StatusBadRequest)
		return
	}
	var count int
	if err := q.QueryRow(r.Context(), `SELECT count(*) FROM webhook_subscriptions WHERE tenant_id = $1 AND installation_id = $2`, tc.TenantID, in.ID).Scan(&count); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if count >= maxSubscriptionsPerInstallation {
		http.Error(w, "an installation holds at most 5 webhook subscriptions", http.StatusConflict)
		return
	}
	secret, keyID, raw, err := h.secret()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	sealed, err := h.sealer.Encrypt(raw)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var head *int64
	if err := q.QueryRow(r.Context(), `SELECT webhook_journal_head($1)`, tc.TenantID).Scan(&head); err != nil || head == nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	id := uuid.New()
	if _, err := q.Exec(r.Context(), `
		INSERT INTO webhook_subscriptions(id, tenant_id, installation_id, url, event_types, key_id, secret_ciphertext, enqueued_through_seq, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, tc.TenantID, in.ID, req.URL, types, keyID, sealed, *head, tc.ActorID); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	v, err := scanSubscription(q.QueryRow(r.Context(), `SELECT `+subscriptionColumns+` FROM webhook_subscriptions WHERE tenant_id = $1 AND id = $2`, tc.TenantID, id))
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.record(r, in.ID, auditdomain.ActionExternalWebhookCreated, map[string]any{"webhook_id": id.String(), "key_id": keyID, "event_types": types})
	writeJSON(w, http.StatusCreated, map[string]any{"subscription": v, "secret": secret})
}

type updateHookRequest struct {
	URL        *string   `json:"url"`
	EventTypes *[]string `json:"event_types"`
	Status     *string   `json:"status"`
}

// Update: PATCH .../webhooks/{hook_id}
func (h *WebhookHandler) Update(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permManage)
	if !ok {
		return
	}
	in, ok := h.installationOf(w, r, q, tc.TenantID)
	if !ok {
		return
	}
	hid, ok := hookID(w, r)
	if !ok {
		return
	}
	var req updateHookRequest
	if !decodeBody(w, r, &req) {
		return
	}
	var url *string
	if req.URL != nil {
		if err := netpolicy.ValidateURL(*req.URL); err != nil {
			http.Error(w, "the webhook URL must be https, on a public address, with no credentials, on port 443 or 8443", http.StatusBadRequest)
			return
		}
		url = req.URL
	}
	var types []string
	if req.EventTypes != nil {
		var ok bool
		if types, ok = validEventTypes(*req.EventTypes); !ok {
			http.Error(w, "event_types must list at least one known event type", http.StatusBadRequest)
			return
		}
	}
	if req.Status != nil && *req.Status != "active" && *req.Status != "paused" {
		http.Error(w, "status must be active or paused", http.StatusBadRequest)
		return
	}
	tag, err := q.Exec(r.Context(), `
		UPDATE webhook_subscriptions SET url = COALESCE($4, url), event_types = COALESCE($5, event_types), status = COALESCE($6, status), updated_at = now()
		 WHERE tenant_id = $1 AND installation_id = $2 AND id = $3`, tc.TenantID, in.ID, hid, url, types, req.Status)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "webhook not found", http.StatusNotFound)
		return
	}
	v, err := scanSubscription(q.QueryRow(r.Context(), `SELECT `+subscriptionColumns+` FROM webhook_subscriptions WHERE tenant_id = $1 AND id = $2`, tc.TenantID, hid))
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.record(r, in.ID, auditdomain.ActionExternalWebhookUpdated, map[string]any{"webhook_id": hid.String()})
	writeJSON(w, http.StatusOK, v)
}

// Delete: DELETE .../webhooks/{hook_id}
func (h *WebhookHandler) Delete(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permManage)
	if !ok {
		return
	}
	in, ok := h.installationOf(w, r, q, tc.TenantID)
	if !ok {
		return
	}
	hid, ok := hookID(w, r)
	if !ok {
		return
	}
	tag, err := q.Exec(r.Context(), `DELETE FROM webhook_subscriptions WHERE tenant_id = $1 AND installation_id = $2 AND id = $3`, tc.TenantID, in.ID, hid)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "webhook not found", http.StatusNotFound)
		return
	}
	h.record(r, in.ID, auditdomain.ActionExternalWebhookDeleted, map[string]any{"webhook_id": hid.String()})
	w.WriteHeader(http.StatusNoContent)
}

// Rotate: POST .../webhooks/{hook_id}/rotate-secret — a new secret, shown once; the old one keeps verifying for `keep_hours` (default 24, 1 to 168).
func (h *WebhookHandler) Rotate(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permManage)
	if !ok {
		return
	}
	in, ok := h.installationOf(w, r, q, tc.TenantID)
	if !ok {
		return
	}
	hid, ok := hookID(w, r)
	if !ok {
		return
	}
	var req struct {
		KeepHours *int `json:"keep_hours"`
	}
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	keep := 24
	if req.KeepHours != nil {
		keep = *req.KeepHours
	}
	if keep < 1 || keep > 168 {
		http.Error(w, "keep_hours must be between 1 and 168", http.StatusBadRequest)
		return
	}
	var exists bool
	if err := q.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM webhook_subscriptions WHERE tenant_id = $1 AND installation_id = $2 AND id = $3)`, tc.TenantID, in.ID, hid).Scan(&exists); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "webhook not found", http.StatusNotFound)
		return
	}
	secret, keyID, raw, err := h.secret()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	sealed, err := h.sealer.Encrypt(raw)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var done bool
	if err := q.QueryRow(r.Context(), `SELECT webhook_rotate_secret($1, $2, $3, $4, make_interval(hours => $5))`, tc.TenantID, hid, keyID, sealed, keep).Scan(&done); err != nil || !done {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h.record(r, in.ID, auditdomain.ActionExternalWebhookSecretRotated, map[string]any{"webhook_id": hid.String(), "key_id": keyID})
	writeJSON(w, http.StatusOK, map[string]any{"key_id": keyID, "secret": secret, "previous_valid_hours": keep})
}

type deliveryView struct {
	ID            uuid.UUID  `json:"id"`
	EventID       uuid.UUID  `json:"event_id"`
	EventSeq      int64      `json:"event_seq"`
	Status        string     `json:"status"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt time.Time  `json:"next_attempt_at"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	DeliveredAt   *time.Time `json:"delivered_at"`
	LastHTTP      *int       `json:"last_http_status"`
	LastError     *string    `json:"last_error_class"`
	CreatedAt     time.Time  `json:"created_at"`
}

// Deliveries: GET .../webhooks/{hook_id}/deliveries?status=dead|pending|delivered|cancelled&limit= — the history and the dead-letter list.
func (h *WebhookHandler) Deliveries(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permRead)
	if !ok {
		return
	}
	in, ok := h.installationOf(w, r, q, tc.TenantID)
	if !ok {
		return
	}
	hid, ok := hookID(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "dead" && status != "pending" && status != "delivered" && status != "cancelled" {
		http.Error(w, "status must be dead, pending, delivered or cancelled", http.StatusBadRequest)
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			http.Error(w, "limit must be 1 to 200", http.StatusBadRequest)
			return
		}
		limit = n
	}
	rows, err := q.Query(r.Context(), `
		SELECT d.id, d.event_id, d.event_seq, d.status, d.attempts, d.next_attempt_at, d.last_attempt_at, d.delivered_at, d.last_http_status, d.last_error_class, d.created_at
		  FROM webhook_deliveries d JOIN webhook_subscriptions s ON s.tenant_id = d.tenant_id AND s.id = d.subscription_id
		 WHERE d.tenant_id = $1 AND s.installation_id = $2 AND d.subscription_id = $3 AND ($4 = '' OR d.status = $4)
		 ORDER BY d.event_seq DESC LIMIT $5`, tc.TenantID, in.ID, hid, status, limit)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []deliveryView{}
	for rows.Next() {
		var d deliveryView
		if err := rows.Scan(&d.ID, &d.EventID, &d.EventSeq, &d.Status, &d.Attempts, &d.NextAttemptAt, &d.LastAttemptAt, &d.DeliveredAt, &d.LastHTTP, &d.LastError, &d.CreatedAt); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "count": len(out)})
}

// Retry: POST .../webhooks/{hook_id}/deliveries/{delivery_id}/retry — puts a dead (or cancelled) delivery back in the queue, with a fresh retry window.
func (h *WebhookHandler) Retry(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, permManage)
	if !ok {
		return
	}
	in, ok := h.installationOf(w, r, q, tc.TenantID)
	if !ok {
		return
	}
	hid, ok := hookID(w, r)
	if !ok {
		return
	}
	did, err := uuid.Parse(r.PathValue("delivery_id"))
	if err != nil {
		http.Error(w, "invalid delivery_id", http.StatusBadRequest)
		return
	}
	if in.Status != domain.StatusActive {
		http.Error(w, "the installation is not active", http.StatusConflict)
		return
	}
	tag, err := q.Exec(r.Context(), `
		UPDATE webhook_deliveries d SET status = 'pending', next_attempt_at = now(), attempts = 0, first_attempt_at = NULL
		  FROM webhook_subscriptions s
		 WHERE d.tenant_id = $1 AND d.id = $2 AND d.subscription_id = $3 AND d.status IN ('dead', 'cancelled')
		   AND s.tenant_id = d.tenant_id AND s.id = d.subscription_id AND s.installation_id = $4`, tc.TenantID, did, hid, in.ID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "delivery not found, or it is not dead", http.StatusNotFound)
		return
	}
	h.record(r, in.ID, auditdomain.ActionExternalWebhookRetried, map[string]any{"webhook_id": hid.String(), "delivery_id": did.String()})
	w.WriteHeader(http.StatusAccepted)
}

var _ = errors.Is
