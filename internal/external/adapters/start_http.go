package adapters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnira/omnira/internal/conversationcontrol"
	"github.com/omnira/omnira/internal/external/domain"
	messagesapp "github.com/omnira/omnira/internal/messages/application"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// ADR-0050: POST /api/v1/external/conversations — create-or-reuse a conversation by phone (the Gateway alert use case). This is the one
// command of the external write side that has no existing {id}: it does not fit cmdSpec/handle/transact (built entirely around locking a
// row that already exists), so it is its own small transaction, following the SAME seven-step recipe as write_http.go's transact —
// re-check the installation, look for a receipt, do the work, write the receipt and the audit row, all before commit — minus the steps
// that need an id this command does not have yet.

// ConversationStarter is the store behind this command (internal/external/adapters.PostgresStartStore). Kept separate from MessageSender
// and NoteWriter so a bug in one can never reach the others.
type ConversationStarter interface {
	// FindOpenConversation is the "reuse" half: the newest OPEN conversation of the contact with this phone, narrowed to channelIDs
	// when non-empty. found is false when there is none.
	FindOpenConversation(ctx context.Context, q platformdb.Querier, tenantID uuid.UUID, phoneE164 string, channelIDs []uuid.UUID) (conversationID, contactID uuid.UUID, found bool, err error)
	// ResolveChannel picks the connection a NEW conversation will use.
	ResolveChannel(ctx context.Context, q platformdb.Querier, tenantID uuid.UUID, requested *uuid.UUID, allow []uuid.UUID) (uuid.UUID, error)
	// CreateConversation is the "create" half: upserts the contact by phone and inserts the conversation, already held.
	CreateConversation(ctx context.Context, q platformdb.Querier, tenantID, conversationID, installation, connectionID uuid.UUID, phoneE164 string, queueID *uuid.UUID) (contactID uuid.UUID, err error)
}

type startMessagePayload struct {
	Text     string           `json:"text,omitempty"`
	Template *templatePayload `json:"template,omitempty"`
}

type startPayload struct {
	PhoneE164       string               `json:"phone_e164"`
	QueueID         *uuid.UUID           `json:"queue_id,omitempty"`
	ChannelID       *uuid.UUID           `json:"channel_id,omitempty"`
	ExternalAgentID string               `json:"external_agent_id,omitempty"`
	Message         *startMessagePayload `json:"message,omitempty"`
}

func decodeStart(body []byte) (startPayload, []byte, error) {
	var v startPayload
	if err := strictJSON(body, &v); err != nil {
		return v, nil, err
	}
	v.PhoneE164 = strings.TrimSpace(v.PhoneE164)
	if !e164Pattern.MatchString(v.PhoneE164) {
		return v, nil, errors.New("phone_e164 must be a valid E.164 number, e.g. +5592999999999")
	}
	if v.Message != nil {
		if (v.Message.Template == nil) == (strings.TrimSpace(v.Message.Text) == "") {
			return v, nil, errors.New("message must have exactly one of text and template")
		}
		if v.Message.Template != nil && v.Message.Template.ID == uuid.Nil {
			return v, nil, errors.New("message.template.id is required")
		}
	}
	if err := checkAgentRef(v.ExternalAgentID); err != nil {
		return v, nil, err
	}
	c, _ := json.Marshal(v)
	return v, c, nil
}

type startResult struct {
	controlDoc
	ContactID uuid.UUID  `json:"contact_id"`
	Created   bool       `json:"created"`
	MessageID *uuid.UUID `json:"message_id,omitempty"`
	Delivery  string     `json:"delivery,omitempty"`
}

// StartConversation: POST /api/v1/external/conversations. Needs contact.write AND conversation.open (RequireScope chained twice by the
// caller, server.go). Idempotency-Key required; no If-Match (there is no prior version — nothing existed before this call, or what
// already existed is reused without changing its control).
func (h *WriteHandler) StartConversation() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required", "Send Authorization: Bearer <integration token>.")
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if !idempotencyKeyPattern.MatchString(key) {
			problemBad(w, r, "Idempotency-Key is required: 8 to 128 characters of A-Z a-z 0-9 . _ : -")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCommandBody))
		if err != nil {
			problemBad(w, r, "The body is too large or could not be read.")
			return
		}
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			externalMetric.Inc("bad_request")
			WriteProblem(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type", "Send Content-Type: application/json.")
			return
		}
		pl, canonical, err := decodeStart(bytes.TrimSpace(raw))
		if err != nil {
			externalMetric.Inc("unprocessable")
			WriteProblem(w, r, http.StatusUnprocessableEntity, "invalid_phone", "Invalid request", err.Error())
			return
		}
		sum := sha256.Sum256(canonical)
		hash := hex.EncodeToString(sum[:])
		requestID := RequestIDFrom(r.Context())

		var out *cmdOutput
		var stored []byte
		var replay bool
		for attempt := 0; attempt < 3; attempt++ {
			out, stored, replay, err = h.startTransact(r.Context(), p, pl, key, hash, requestID)
			if !errors.Is(err, errReceiptRace) {
				break
			}
		}
		if err != nil {
			h.fail(w, r, err)
			return
		}
		externalMetric.Inc("served")
		hd := w.Header()
		hd.Set("Content-Type", "application/json")
		hd.Set("Cache-Control", "no-store")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("ETag", `"control-`+strconv.FormatInt(out.version, 10)+`"`)
		if requestID != "" {
			hd.Set("X-Request-ID", requestID)
		}
		if replay {
			hd.Set("Idempotent-Replay", "true")
		}
		w.WriteHeader(out.status)
		_, _ = w.Write(stored)
	})
}

func (h *WriteHandler) startTransact(ctx context.Context, p *domain.Principal, pl startPayload, key, hash, requestID string) (*cmdOutput, []byte, bool, error) {
	var out *cmdOutput
	var stored []byte
	var replay bool
	in := &cmdInput{p: p, payload: pl, requestID: requestID} // h.receipt and h.actor only read in.p / in.payload / in.requestID
	err := platformdb.WithSystemTenantSession(ctx, h.pool, p.TenantID, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, h.pool)
		out, stored, replay = nil, nil, false
		if err := RecheckPrincipal(ctx, q, p); err != nil {
			return err
		}
		al, err := LoadAllowlists(ctx, q, p)
		if err != nil {
			return err
		}
		if rec, found, err := h.receipt(ctx, q, in, "start_conversation", key, hash); err != nil {
			return err
		} else if found {
			out, stored, replay = rec.out, rec.body, true
			return nil
		}
		if _, err := q.Exec(ctx, `SELECT set_config('omnira.event_origin', 'crm', true)`); err != nil {
			return err
		}
		if pl.ChannelID != nil && len(al.ChannelIDs) > 0 && !containsUUID(al.ChannelIDs, *pl.ChannelID) {
			return refuse(http.StatusForbidden, "channel_not_allowed", "Channel not allowed", "The line is not one this installation may use.", nil)
		}
		if pl.QueueID != nil {
			if len(al.QueueIDs) > 0 && !containsUUID(al.QueueIDs, *pl.QueueID) {
				return refuse(http.StatusForbidden, "queue_not_allowed", "Queue not allowed", "The queue is not one this installation may use.", nil)
			}
			var one int
			if err := q.QueryRow(ctx, `SELECT 1 FROM queues WHERE tenant_id = $1 AND id = $2`, p.TenantID, *pl.QueueID).Scan(&one); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return refuse(http.StatusUnprocessableEntity, "invalid_target", "Invalid queue", "The queue does not exist.", nil)
				}
				return err
			}
		}

		findChannels := al.ChannelIDs
		if pl.ChannelID != nil {
			findChannels = []uuid.UUID{*pl.ChannelID}
		}
		convID, contactID, found, err := h.d.Starter.FindOpenConversation(ctx, q, p.TenantID, pl.PhoneE164, findChannels)
		if err != nil {
			return err
		}
		var cur conversationcontrol.State
		var created bool
		if found {
			cur, err = h.ctl.Lock(ctx, q, p.TenantID, convID)
			if err != nil {
				return h.fromControl(err, &cur)
			}
			switch {
			case cur.HeldBy(conversationcontrol.KindCRMApplication, p.InstallationID):
				// already mine: proceed, no control change
			case cur.Free():
				if pl.Message != nil {
					return refuse(http.StatusConflict, "not_holder", "Not the holder", "This installation does not hold the conversation: claim it first.", &cur)
				}
			default:
				return refuse(http.StatusConflict, "held_by_other", "Held by someone else", "Another holder has the conversation.", &cur)
			}
		} else {
			connID, err := h.d.Starter.ResolveChannel(ctx, q, p.TenantID, pl.ChannelID, al.ChannelIDs)
			if err != nil {
				return h.fromStart(err)
			}
			newID := uuid.New()
			var cerr error
			if err := h.ctl.CreateHeldByApplication(ctx, q, p.TenantID, newID, func(ctx context.Context) error {
				contactID, cerr = h.d.Starter.CreateConversation(ctx, q, p.TenantID, newID, p.InstallationID, connID, pl.PhoneE164, pl.QueueID)
				return cerr
			}, h.actor(in)); err != nil {
				return err
			}
			convID, created = newID, true
			cur, err = h.ctl.Get(ctx, q, p.TenantID, convID)
			if err != nil {
				return err
			}
		}

		var messageID *uuid.UUID
		var delivery string
		if pl.Message != nil {
			m := messagesapp.ExternalMessage{InstallationID: p.InstallationID, ConversationID: convID, ControlVersion: cur.Version, Text: pl.Message.Text, Now: h.now()}
			if pl.Message.Template != nil {
				id := pl.Message.Template.ID
				m.TemplateID, m.TemplateParams = &id, pl.Message.Template.Params
			}
			msg, err := h.d.Sender.Send(ctx, m)
			if err != nil {
				return h.fromMessage(err, &cur)
			}
			messageID, delivery = &msg.ID, "queued"
		}

		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		body, err := json.Marshal(startResult{
			controlDoc: controlDoc{ConversationID: convID, Status: cur.Status, Control: controlOf(cur, p.InstallationID)},
			ContactID:  contactID, Created: created, MessageID: messageID, Delivery: delivery,
		})
		if err != nil {
			return err
		}
		tag, err := q.Exec(ctx, `
			INSERT INTO external_command_receipts(tenant_id, installation_id, operation, idempotency_key, request_hash, conversation_id, message_id, control_version, status_code, response)
			VALUES ($1,$2,'start_conversation',$3,$4,$5,$6,$7,$8,$9::jsonb) ON CONFLICT DO NOTHING`,
			p.TenantID, p.InstallationID, key, hash, convID, messageID, cur.Version, status, string(body))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errReceiptRace
		}
		meta, _ := json.Marshal(map[string]any{"operation": "start_conversation", "conversation_id": convID.String(), "installation_id": p.InstallationID.String(),
			"actor_kind": "crm_application", "control_version": cur.Version, "status": status, "created": created,
			"external_agent_id": agentRefOf(in.payload), "external_agent_verified": false})
		if _, err := q.Exec(ctx, `
			INSERT INTO audit_events(tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata)
			VALUES ($1, NULL, 'external_app.command', 'conversation', $2, 'success', $3, $4::jsonb)`,
			p.TenantID, convID.String(), requestID, string(meta)); err != nil {
			return err
		}
		out, stored = &cmdOutput{status: status, version: cur.Version}, body
		return nil
	})
	return out, stored, replay, err
}

// fromStart maps what resolving a channel for a NEW conversation can refuse.
func (h *WriteHandler) fromStart(err error) error {
	switch {
	case errors.Is(err, ErrNoChannel):
		return refuse(http.StatusConflict, "channel_unavailable", "Channel unavailable", "No active text channel matches; name channel_id.", nil)
	case errors.Is(err, ErrChannelAmbiguous):
		return refuse(http.StatusUnprocessableEntity, "channel_ambiguous", "Channel ambiguous", "More than one active text channel matches: name channel_id.", nil)
	}
	return err
}
