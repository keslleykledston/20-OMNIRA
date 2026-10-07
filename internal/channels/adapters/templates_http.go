package adapters

import (
	"context"
	"errors"
	"log"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/channels/application"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/meta"
	"github.com/omnira/omnira/internal/channels/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// TemplatesHandler syncs and lists the WhatsApp Cloud API message templates of a connection. Sync is management
// (channel.manage); listing is for anyone who attends (the composer needs it), and carries no secret.
type TemplatesHandler struct {
	pool  *pgxpool.Pool
	conns ports.ChannelConnectionRepository
	meta  *meta.Provider
	perms ports.PermissionChecker
}

func NewTemplatesHandler(pool *pgxpool.Pool, conns ports.ChannelConnectionRepository, provider *meta.Provider, perms ports.PermissionChecker) *TemplatesHandler {
	return &TemplatesHandler{pool: pool, conns: conns, meta: provider, perms: perms}
}

var (
	templateNamePattern = regexp.MustCompile(`^[a-z0-9_]{1,512}$`)
	templateLangPattern = regexp.MustCompile(`^[a-z]{2,3}(_[A-Za-z]{2,4})?$`)
)

type templateJSON struct {
	ID                uuid.UUID `json:"id"`
	Name              string    `json:"name"`
	Language          string    `json:"language"`
	Category          string    `json:"category"`
	Status            string    `json:"status"`
	Body              string    `json:"body"`
	VariableCount     int       `json:"variable_count"`
	Sendable          bool      `json:"sendable"`
	UnsupportedReason string    `json:"unsupported_reason,omitempty"`
}

func (h *TemplatesHandler) connection(w http.ResponseWriter, r *http.Request) (*tenancydomain.TenantContext, *domain.ChannelConnection, bool) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, nil, false
	}
	id, err := uuid.Parse(r.PathValue("connection_id"))
	if err != nil {
		http.Error(w, "invalid connection_id", http.StatusBadRequest)
		return nil, nil, false
	}
	conn, err := h.conns.FindByID(r.Context(), id)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return nil, nil, false
	}
	// RLS hides other tenants' rows; the explicit check is defense in depth. Unknown and foreign look the same.
	if conn == nil || conn.TenantID != tc.TenantID || conn.Provider != domain.ProviderMetaCloud {
		http.Error(w, "connection not found", http.StatusNotFound)
		return nil, nil, false
	}
	return tc, conn, true
}

// Sync answers POST /channels/connections/{connection_id}/templates/sync (channel.manage).
func (h *TemplatesHandler) Sync(w http.ResponseWriter, r *http.Request) {
	tc, conn, ok := h.connection(w, r)
	if !ok {
		return
	}
	allowed, err := h.perms.HasPermission(r.Context(), tc.ActorID, application.PermissionChannelManage)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	list, err := h.meta.ListTemplates(r.Context(), *conn)
	if err != nil {
		switch {
		case errors.Is(err, ports.ErrAuthentication):
			http.Error(w, "credential rejected by the provider: check the token and try again", http.StatusUnprocessableEntity)
		case errors.Is(err, ports.ErrNotConfigured):
			http.Error(w, "channel provider is not configured", http.StatusServiceUnavailable)
		default:
			log.Printf("templates sync: %v", err) // no credential material in provider errors
			http.Error(w, "channel provider unavailable", http.StatusBadGateway)
		}
		return
	}
	if err := h.replace(r.Context(), tc.TenantID, conn.ID, list); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	sendable := 0
	for _, t := range list {
		if t.Status == "APPROVED" && t.UnsupportedReason == "" {
			sendable++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"synced": len(list), "sendable": sendable})
}

// replace makes the stored set equal to what Meta reports: upsert everything, drop what Meta no longer has.
func (h *TemplatesHandler) replace(ctx context.Context, tenantID, connID uuid.UUID, list []meta.Template) error {
	q := platformdb.QuerierFromContext(ctx, h.pool)
	now := time.Now().UTC()
	for _, t := range list {
		// A template whose name/language does not fit our stricter format is skipped (a failed statement would abort
		// the whole transaction), not fatal.
		if !templateNamePattern.MatchString(t.Name) || !templateLangPattern.MatchString(t.Language) || t.VariableCount > 20 {
			log.Printf("templates sync: skipped a template with an unsupported name/language/variable count")
			continue
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO channel_message_templates
			  (tenant_id, connection_id, provider_template_id, name, language, category, status, body_text, variable_count, sendable, unsupported_reason, synced_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT (tenant_id, connection_id, name, language) DO UPDATE SET
			  provider_template_id = EXCLUDED.provider_template_id, category = EXCLUDED.category, status = EXCLUDED.status,
			  body_text = EXCLUDED.body_text, variable_count = EXCLUDED.variable_count, sendable = EXCLUDED.sendable,
			  unsupported_reason = EXCLUDED.unsupported_reason, synced_at = EXCLUDED.synced_at`,
			tenantID, connID, t.ID, t.Name, t.Language, t.Category, t.Status, t.Body, t.VariableCount, t.UnsupportedReason == "", t.UnsupportedReason, now); err != nil {
			return err
		}
	}
	_, err := q.Exec(ctx, `DELETE FROM channel_message_templates WHERE tenant_id=$1 AND connection_id=$2 AND synced_at < $3`, tenantID, connID, now)
	return err
}

// List answers GET /channels/lines/{connection_id}/templates: the templates an attendant may pick on that line.
// Approved ones first; the unsupported ones are listed with the reason so the operator is told why they are not offered.
func (h *TemplatesHandler) List(w http.ResponseWriter, r *http.Request) {
	tc, conn, ok := h.connection(w, r)
	if !ok {
		return
	}
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `
		SELECT id, name, language, category, status, body_text, variable_count, sendable, unsupported_reason
		FROM channel_message_templates
		WHERE tenant_id=$1 AND connection_id=$2 AND status='APPROVED'
		ORDER BY sendable DESC, name, language`, tc.TenantID, conn.ID)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []templateJSON{}
	for rows.Next() {
		var t templateJSON
		if err := rows.Scan(&t.ID, &t.Name, &t.Language, &t.Category, &t.Status, &t.Body, &t.VariableCount, &t.Sendable, &t.UnsupportedReason); err != nil {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		items = append(items, t)
	}
	if rows.Err() != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
