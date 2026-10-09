package adapters

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/application"
	"github.com/omnira/omnira/internal/hub/replying"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// DelegatedWrites — the WRITES of the pilot journey (claim a conversation, answer it) for a Hub agent attending an instance in the delegated
// context (ADR-0040 phase 03). The instance routes keep their URLs; when the request is delegated (`hub_serve`) these wrappers hand it to the
// Hub's own write path (ADR-0037: authorize in the caller's RLS session, execute in a tenant-scoped system session that re-checks the delegation
// inside the transaction, audit), which is already reviewed and proven; for everybody else the original handler runs untouched. No policy opens
// the tables for writing: the data layer keeps refusing every direct write by a Hub agent.
//
// Two independent conditions must both hold: the new permission key (checked by Delegable before this runs: conversation.claim /
// conversation.reply) and the legacy reply-capable grant (can_reply, checked by replying.Authorize with RequireReply). Provisioning sets both together.
type DelegatedWrites struct {
	pool  *pgxpool.Pool
	reply *replying.Service
}

func NewDelegatedWrites(pool *pgxpool.Pool) *DelegatedWrites {
	repo := NewPostgresHubRepository(pool)
	authz := application.NewHubAuthorizationService(repo)
	return &DelegatedWrites{pool: pool, reply: replying.New(pool, authz, repo, messagesadapters.NewPostgresOutboundStore(pool))}
}

// delegatedTarget resolves, for a delegated request, the persisted hub inbox item of the conversation in the path and authorizes the write on it.
// ok=false means the response was already written.
func (d *DelegatedWrites) target(w http.ResponseWriter, r *http.Request, tc *tenancydomain.TenantContext) (*replying.Target, bool) {
	conversationID, err := uuid.Parse(r.PathValue("conversation_id"))
	if err != nil {
		httpError(w, "invalid request", http.StatusBadRequest)
		return nil, false
	}
	var itemID uuid.UUID
	err = platformdb.QuerierFromContext(r.Context(), d.pool).QueryRow(r.Context(),
		`SELECT id FROM hub_inbox_items WHERE hub_id = $1 AND tenant_id = $2 AND conversation_id = $3`, *tc.HubID, tc.TenantID, conversationID).Scan(&itemID)
	if errors.Is(err, pgx.ErrNoRows) {
		httpError(w, "not found", http.StatusNotFound)
		return nil, false
	}
	if err != nil {
		writeReplyError(w, err)
		return nil, false
	}
	// the company on screen is the company of the context the server proved (the URL tenant already resolved to it), never a client field
	t, err := d.reply.Authorize(r.Context(), tc.ActorID, *tc.HubID, itemID, tc.TenantID, correlationOf(r))
	if err != nil {
		writeReplyError(w, err)
		return nil, false
	}
	return t, true
}

func delegatedContext(r *http.Request) (*tenancydomain.TenantContext, bool) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc == nil || tc.Source != tenancydomain.AccessSourceHubServe || tc.HubID == nil {
		return nil, false
	}
	return tc, true
}

// Claim wraps POST /tenants/{tenant_id}/inbox/conversations/{conversation_id}/assign.
func (d *DelegatedWrites) Claim(original http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc, delegated := delegatedContext(r)
		if !delegated {
			original.ServeHTTP(w, r)
			return
		}
		t, ok := d.target(w, r, tc)
		if !ok {
			return
		}
		changed, err := d.reply.Claim(r.Context(), tc.ActorID, t)
		if err != nil {
			writeReplyError(w, err)
			return
		}
		writeJSON(w, map[string]any{"conversation_id": t.Item.ConversationID, "assigned_to_user_id": tc.ActorID, "changed": changed})
	})
}

type delegatedReplyRequest struct {
	Text         string     `json:"text"`
	AttachmentID *uuid.UUID `json:"attachment_id,omitempty"`
}

// Reply wraps POST /tenants/{tenant_id}/inbox/conversations/{conversation_id}/messages. Text only: an attachment is refused here (a later phase).
func (d *DelegatedWrites) Reply(original http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc, delegated := delegatedContext(r)
		if !delegated {
			original.ServeHTTP(w, r)
			return
		}
		var req delegatedReplyRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
			httpError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.AttachmentID != nil {
			httpError(w, "attachments are not available when attending through a Hub yet", http.StatusUnprocessableEntity)
			return
		}
		t, ok := d.target(w, r, tc)
		if !ok {
			return
		}
		res, err := d.reply.Send(r.Context(), tc.ActorID, t, req.Text, r.Header.Get("Idempotency-Key"))
		if err != nil {
			writeReplyError(w, err)
			return
		}
		status := http.StatusAccepted
		if res.Replayed {
			status = http.StatusOK
			w.Header().Set("Idempotent-Replayed", "true")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": res.Message.ID, "conversation_id": res.Message.ConversationID, "direction": "outbound", "body": res.Message.Body,
			"status": res.Message.Status, "created_at": res.Message.CreatedAt.UTC().Format(time.RFC3339),
		})
	})
}
