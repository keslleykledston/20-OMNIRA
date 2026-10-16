package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	attendanceadapters "github.com/omnira/omnira/internal/attendance/adapters"
	attendanceapplication "github.com/omnira/omnira/internal/attendance/application"
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	"github.com/omnira/omnira/internal/hub/application"
	"github.com/omnira/omnira/internal/hub/replying"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/platform/claimhook"
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
	onClaim    *claimhook.Hook // an optional module learns that the agent assumed the conversation (nil: nobody)
	pool       *pgxpool.Pool
	reply      *replying.Service
	attendance *attendanceapplication.Service // the attendance rules a delegated finalize runs (nil: the route does not exist for a delegate)
}

// WithAttendance gives the Hub's write path the attendance service (ADR-0040 phase 05b: finalizing the attendance).
func (d *DelegatedWrites) WithAttendance(svc *attendanceapplication.Service) *DelegatedWrites {
	d.attendance = svc
	return d
}

// WithClaimHook installs the hook that runs after a Hub agent claimed a conversation (ADR-0049: the protocol opens without a click).
func (d *DelegatedWrites) WithClaimHook(h *claimhook.Hook) *DelegatedWrites {
	d.onClaim = h
	return d
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
		if changed {
			d.afterClaim(r.Context(), tc, t)
		}
		writeJSON(w, map[string]any{"conversation_id": t.Item.ConversationID, "assigned_to_user_id": tc.ActorID, "changed": changed})
	})
}

// afterClaim lets the optional module open the attendance for the agent who just assumed the conversation. The key service.create is proven HERE, in the
// delegated session (the write below runs in a system session that cannot see delegated keys); the hook runs in the Hub's write path with the delegation
// re-proven under the conversation lock, and what it did is audited naming the hub, grant and contract. Nothing here can fail the claim, which already stands.
func (d *DelegatedWrites) afterClaim(ctx context.Context, tc *tenancydomain.TenantContext, t *replying.Target) {
	if !d.onClaim.Installed() {
		return
	}
	var may bool
	if err := platformdb.QuerierFromContext(ctx, d.pool).QueryRow(ctx, `SELECT actor_has_permission($1, $2, 'service.create')`, tc.TenantID, tc.ActorID).Scan(&may); err != nil || !may {
		if err != nil {
			log.Printf("hub: claim hook permission check of conversation %s failed: %v", t.Item.ConversationID, err)
		} else {
			log.Printf("hub: claim of conversation %s opened no protocol: the delegated grant of the agent lacks service.create", t.Item.ConversationID)
		}
		return
	}
	openProtocolOnClaim(ctx, d.reply, d.onClaim, t, tc.ActorID)
}

// openProtocolOnClaim runs the claim hook in the Hub's write path, with the delegation re-proven under the conversation lock, and audits what it did naming
// the hub, grant and contract. Both doors a Hub agent takes to assume a conversation (the instance's assign route and the Hub's own claim route) end here.
// Nothing here can fail the claim, which already stands. The caller has proven the key service.create.
func openProtocolOnClaim(ctx context.Context, reply *replying.Service, hook *claimhook.Hook, t *replying.Target, actor uuid.UUID) {
	err := reply.Act(ctx, actor, t, "hub.service.session_opened_on_claim", func(c context.Context, _ *uuid.UUID, closed bool) (map[string]any, error) {
		if closed {
			return nil, nil
		}
		created, err := hook.Run(c, t.Item.ConversationID, actor)
		if err != nil || !created {
			if err == nil {
				log.Printf("hub: claim of conversation %s opened no protocol (the module declined: no key, not a customer chat, or already ended)", t.Item.ConversationID)
			}
			return nil, err
		}
		return map[string]any{"conversation_id": t.Item.ConversationID.String()}, nil
	})
	if err != nil {
		log.Printf("hub: the claim hook of conversation %s failed: %v", t.Item.ConversationID, err)
	}
}

// Release wraps POST /tenants/{tenant_id}/inbox/conversations/{conversation_id}/unassign: the holder gives the conversation back to the queue. Only the
// current holder may (the Hub path locks the row and proves it), and it needs nothing beyond what holding it took (conversation.claim + the reply grant).
func (d *DelegatedWrites) Release(original http.Handler) http.Handler {
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
		if err := d.reply.Transfer(r.Context(), tc.ActorID, t, nil); err != nil {
			writeReplyError(w, err)
			return
		}
		writeJSON(w, map[string]any{"conversation_id": t.Item.ConversationID, "assigned_to_user_id": nil, "changed": true})
	})
}

type delegatedTransferRequest struct {
	TargetUserID uuid.UUID `json:"target_user_id"`
}

// Transfer wraps POST /tenants/{tenant_id}/inbox/conversations/{conversation_id}/transfer: the holder hands the conversation to another person of the
// hub. The member's route needs conversation.manage and may move any conversation; this one needs the key conversation.transfer AND the holding of the
// conversation, and the person chosen must be able to take it (a reply-capable grant for the instance and the queue, proven again inside the
// transaction), so the directory of the instance's own members is not reachable from here.
func (d *DelegatedWrites) Transfer(original http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc, delegated := delegatedContext(r)
		if !delegated {
			original.ServeHTTP(w, r)
			return
		}
		var req delegatedTransferRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.TargetUserID == uuid.Nil {
			httpError(w, "target_user_id required", http.StatusBadRequest)
			return
		}
		t, ok := d.target(w, r, tc)
		if !ok {
			return
		}
		if err := d.reply.Transfer(r.Context(), tc.ActorID, t, &req.TargetUserID); err != nil {
			writeReplyError(w, err)
			return
		}
		writeJSON(w, map[string]any{"previous_assignee": tc.ActorID, "new_assignee": req.TargetUserID, "changed": true})
	})
}

// TransferCandidates serves GET /tenants/{tenant_id}/inbox/conversations/{conversation_id}/transfer-candidates: who the holder may hand THIS conversation to
// right now (the people of the hub with a live, reply-capable grant for the instance and the conversation's queue). It exists only for the delegated
// context; a member's request is a 404.
func (d *DelegatedWrites) TransferCandidates() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc, delegated := delegatedContext(r)
		if !delegated {
			httpError(w, "not found", http.StatusNotFound)
			return
		}
		t, ok := d.target(w, r, tc)
		if !ok {
			return
		}
		items, err := d.reply.Candidates(r.Context(), tc.ActorID, t)
		if err != nil {
			writeReplyError(w, err)
			return
		}
		writeJSON(w, map[string]any{"items": items})
	})
}

// Finalize wraps POST /tenants/{tenant_id}/inbox/conversations/{conversation_id}/finalize: the holder ends the attendance (conversation closed, local tickets
// closed, closure and follow-ups recorded). Needs the key conversation.finalize (checked by Delegable before this runs) AND the reply-capable grant. The
// attendance rules are the member's (attendance.Service.FinalizeAsHubAgent), run inside the Hub's write path with the delegation re-proven under the row lock.
func (d *DelegatedWrites) Finalize(original http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc, delegated := delegatedContext(r)
		if !delegated {
			original.ServeHTTP(w, r)
			return
		}
		if d.attendance == nil {
			httpError(w, "not found", http.StatusNotFound)
			return
		}
		var in attendancedomain.FinalizeInput
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			httpError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		t, ok := d.target(w, r, tc)
		if !ok {
			return
		}
		in.ConversationID = t.Item.ConversationID // the conversation of the persisted item, never a body field
		// the closing notice for the customer is a reply: it needs the key conversation.reply, proven HERE, in the delegated session (the write below runs in a system
		// session that cannot see delegated keys). Without it the finalize still goes on and answers "not_permitted".
		mayNotify := false
		if in.Farewell != "" {
			if err := platformdb.QuerierFromContext(r.Context(), d.pool).QueryRow(r.Context(), `SELECT actor_has_permission($1, $2, 'conversation.reply')`, tc.TenantID, tc.ActorID).Scan(&mayNotify); err != nil {
				writeReplyError(w, err)
				return
			}
		}
		var res attendanceapplication.FinalizeResult
		err := d.reply.Act(r.Context(), tc.ActorID, t, "hub.conversation.finalized", func(c context.Context, _ *uuid.UUID, _ bool) (map[string]any, error) {
			out, err := d.attendance.FinalizeAsHubAgent(c, tc.ActorID, in, mayNotify)
			if err != nil {
				return nil, err
			}
			res = out
			if !out.Changed {
				return nil, nil
			}
			return map[string]any{"reason": string(in.Reason), "source": string(attendancedomain.SourceAgent),
				"local_tickets_closed": out.Closure.LocalTicketsClosed, "tickets_kept": out.Closure.TicketsKept, "follow_ups": len(out.FollowUps), "farewell": string(out.Farewell)}, nil
		})
		switch {
		case err == nil:
			attendanceadapters.WriteOK(w, attendanceadapters.FinalizeBody(res))
		case errors.Is(err, attendancedomain.ErrForbidden), errors.Is(err, attendancedomain.ErrNotFound), errors.Is(err, attendancedomain.ErrNotAContact), errors.Is(err, attendancedomain.ErrInvalid):
			attendanceadapters.WriteError(w, err)
		default:
			writeReplyError(w, err)
		}
	})
}

type delegatedReplyRequest struct {
	Text         string     `json:"text"`
	AttachmentID *uuid.UUID `json:"attachment_id,omitempty"`
}

// Reply wraps POST /tenants/{tenant_id}/inbox/conversations/{conversation_id}/messages: text, or a file previously uploaded through Upload (text is then its caption).
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
			t, ok := d.target(w, r, tc)
			if !ok {
				return
			}
			res, err := d.reply.SendMedia(r.Context(), tc.ActorID, t, *req.AttachmentID, req.Text, r.Header.Get("Idempotency-Key"))
			if err != nil {
				writeMediaError(w, err)
				return
			}
			writeMediaMessage(w, res, nil)
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

// TicketOpenedNotice returns the "your ticket was opened" notice for a Hub agent (ADR-0040 phase 04b): the same text, key and once-per-ticket guarantee as
// the member's notice, sent through the Hub's own write path (authorize in the caller's session, execute in a tenant-scoped system session that re-checks the
// delegation, audit). It needs BOTH the conversation.reply key and the legacy reply-capable grant, like any reply; without them it is skipped and the
// caller (which treats the notice as best-effort) only logs.
func (d *DelegatedWrites) TicketOpenedNotice(store messagesapplication.NoticeStore) *DelegatedTicketNotice {
	return &DelegatedTicketNotice{writes: d, store: store}
}

type DelegatedTicketNotice struct {
	writes *DelegatedWrites
	store  messagesapplication.NoticeStore
}

func (n *DelegatedTicketNotice) NotifyTicketOpened(ctx context.Context, conversationID, localTicketID uuid.UUID, ticketNumber string) error {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc == nil || tc.Source != tenancydomain.AccessSourceHubServe || tc.HubID == nil {
		return messagesapplication.ErrNoticeSkipped
	}
	if localTicketID == uuid.Nil || strings.TrimSpace(ticketNumber) == "" {
		return messagesapplication.ErrNoticeSkipped
	}
	q := platformdb.QuerierFromContext(ctx, n.writes.pool)
	var mayReply bool
	if err := q.QueryRow(ctx, `SELECT actor_has_permission($1, $2, 'conversation.reply')`, tc.TenantID, tc.ActorID).Scan(&mayReply); err != nil {
		return err
	}
	if !mayReply {
		return messagesapplication.ErrNoticeSkipped
	}
	var itemID uuid.UUID
	err = q.QueryRow(ctx, `SELECT id FROM hub_inbox_items WHERE hub_id = $1 AND tenant_id = $2 AND conversation_id = $3`, *tc.HubID, tc.TenantID, conversationID).Scan(&itemID)
	if errors.Is(err, pgx.ErrNoRows) {
		return messagesapplication.ErrNoticeSkipped
	}
	if err != nil {
		return err
	}
	key := "ticket-opened-" + localTicketID.String()
	if sent, err := n.store.NoticeAlreadySent(ctx, conversationID, key); err != nil || sent {
		return err
	}
	name, err := n.store.ContactName(ctx, conversationID)
	if err != nil {
		return err
	}
	t, err := n.writes.reply.Authorize(ctx, tc.ActorID, *tc.HubID, itemID, tc.TenantID, tc.CorrelationID)
	if err != nil {
		return err
	}
	_, err = n.writes.reply.Send(ctx, tc.ActorID, t, messagesapplication.RenderTicketOpened(name, ticketNumber), key)
	return err
}
