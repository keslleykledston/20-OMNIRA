package adapters

import (
	"context"
	"encoding/json"
	"errors"
	mediadomain "github.com/omnira/omnira/internal/media/domain"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	messagesapp "github.com/omnira/omnira/internal/messages/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// PermissionChecker resolves a role permission of a user in the TenantContext tenant.
type PermissionChecker interface {
	HasPermission(ctx context.Context, userID uuid.UUID, permission string) (bool, error)
}

// ChannelLinesHandler is the Inbox's channel selector backend: which line a conversation is on (and whether its
// provider currently accepts free text), and opening a person's conversation on a chosen line.
type ChannelLinesHandler struct {
	pool  *pgxpool.Pool
	perms PermissionChecker
	// mediaSend: outbound media (ADR-0024) is enabled on this server.
	mediaSend bool
}

// WithOutboundMedia reports to clients whether operators may attach files (the feature flag); the provider still decides per conversation.
func (h *ChannelLinesHandler) WithOutboundMedia(enabled bool) *ChannelLinesHandler {
	h.mediaSend = enabled
	return h
}

func NewChannelLinesHandler(pool *pgxpool.Pool, perms PermissionChecker) *ChannelLinesHandler {
	return &ChannelLinesHandler{pool: pool, perms: perms}
}

type conversationChannel struct {
	ChannelConnectionID *uuid.UUID `json:"channel_connection_id,omitempty"`
	Provider            string     `json:"provider,omitempty"`
	CanSendText         bool       `json:"can_send_text"`
	// CanSendMedia: the server has outbound media on AND this conversation's provider can deliver files (ADR-0024).
	CanSendMedia bool `json:"can_send_media"`
	// WindowRequired: the provider only accepts free text within 24 h of the customer's last message.
	WindowRequired bool `json:"window_required"`
	// WindowOpen is true whenever free text may be sent right now (always true when no window is required).
	WindowOpen      bool    `json:"window_open"`
	LastInboundAt   *string `json:"last_inbound_at,omitempty"`
	WindowExpiresAt *string `json:"window_expires_at,omitempty"`
}

// Channel answers GET /inbox/conversations/{id}/channel. Unknown and cross-tenant ids are both 404.
func (h *ChannelLinesHandler) Channel(w http.ResponseWriter, r *http.Request) {
	tenantID, err := requestTenant(r)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}
	id, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		http.Error(w, "invalid conversation_id", http.StatusBadRequest)
		return
	}
	var (
		out      conversationChannel
		provider *string
		ready    *bool
		last     *time.Time
	)
	err = platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT c.channel_connection_id, cc.provider, (cc.status = 'active' AND cc.capabilities ? 'text'),
		       (SELECT max(m.created_at) FROM messages m WHERE m.tenant_id = c.tenant_id AND m.conversation_id = c.id AND m.direction = 'inbound')
		FROM conversations c
		LEFT JOIN channel_connections cc ON cc.tenant_id = c.tenant_id AND cc.id = c.channel_connection_id
		WHERE c.tenant_id = $1 AND c.id = $2`, tenantID, id).Scan(&out.ChannelConnectionID, &provider, &ready, &last)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read conversation channel", http.StatusInternalServerError)
		return
	}
	if provider != nil {
		out.Provider = *provider
	}
	out.CanSendText = ready != nil && *ready
	out.CanSendMedia = h.mediaSend && out.CanSendText && mediadomain.OutboundMediaSupported(out.Provider)
	out.WindowRequired, out.WindowOpen = messagesapp.SessionWindow(out.Provider, last, time.Now())
	if last != nil {
		s := last.UTC().Format(time.RFC3339)
		out.LastInboundAt = &s
		if out.WindowRequired && out.WindowOpen {
			e := last.Add(messagesapp.SessionWindowDuration).UTC().Format(time.RFC3339)
			out.WindowExpiresAt = &e
		}
	}
	writeJSON(w, out)
}

var phonePattern = regexp.MustCompile(`^\+[0-9]{8,15}$`)

// Open answers POST /inbox/conversations/open {contact_id, channel_connection_id}: the contact's open conversation on
// that line, created empty (and unassigned: taking it is the usual claim) when there is none. It never sends anything.
// Needs conversation.claim, like replying. The tenant comes only from the TenantContext.
func (h *ChannelLinesHandler) Open(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var req struct {
		ContactID           uuid.UUID `json:"contact_id"`
		ChannelConnectionID uuid.UUID `json:"channel_connection_id"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
	if err != nil || json.Unmarshal(body, &req) != nil || req.ContactID == uuid.Nil || req.ChannelConnectionID == uuid.Nil {
		http.Error(w, "contact_id and channel_connection_id are required", http.StatusBadRequest)
		return
	}
	ok, err := h.perms.HasPermission(r.Context(), tc.ActorID, messagesapp.PermissionClaim)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var phone string
	err = q.QueryRow(r.Context(), `SELECT phone_e164 FROM contacts WHERE tenant_id=$1 AND id=$2`, tc.TenantID, req.ContactID).Scan(&phone)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "contact not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !phonePattern.MatchString(phone) {
		http.Error(w, "contact has no usable phone number", http.StatusUnprocessableEntity)
		return
	}
	var usable bool
	if err := q.QueryRow(r.Context(), `
		SELECT EXISTS (SELECT 1 FROM channel_connections WHERE tenant_id=$1 AND id=$2 AND channel='whatsapp'
		               AND status='active' AND capabilities ? 'text')`, tc.TenantID, req.ChannelConnectionID).Scan(&usable); err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if !usable {
		http.Error(w, "channel not found or not active", http.StatusUnprocessableEntity)
		return
	}
	// Serialize concurrent opens of the same (contact, line): the partial unique index allows one OPEN conversation.
	var id uuid.UUID
	created := false
	err = q.QueryRow(r.Context(), `
		SELECT id FROM conversations
		WHERE tenant_id=$1 AND contact_id=$2 AND channel_connection_id=$3 AND status='open'
		ORDER BY updated_at DESC, id DESC LIMIT 1`, tc.TenantID, req.ContactID, req.ChannelConnectionID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		id = uuid.New()
		created = true
		_, err = q.Exec(r.Context(), `
			INSERT INTO conversations (id, tenant_id, contact_id, conversation_kind, has_unclassified_participants, channel_connection_id, status, title, created_at, updated_at)
			SELECT $1,$2,$3, k.kind, (k.kind = 'unclassified'), $4, 'open', '', now(), now()
			FROM (SELECT contact_kind_to_conversation_kind(COALESCE((SELECT ct.kind FROM contacts ct WHERE ct.tenant_id=$2 AND ct.id=$3),'unclassified')) AS kind) k`,
			id, tc.TenantID, req.ContactID, req.ChannelConnectionID)
	}
	if err != nil {
		http.Error(w, "failed to open conversation", http.StatusInternalServerError)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"conversation_id": id, "created": created})
}
