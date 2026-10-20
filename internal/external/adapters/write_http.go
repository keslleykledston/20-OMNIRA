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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	"github.com/omnira/omnira/internal/conversationcontrol"
	"github.com/omnira/omnira/internal/entitlements"
	"github.com/omnira/omnira/internal/external/domain"
	mediadomain "github.com/omnira/omnira/internal/media/domain"
	messagesapp "github.com/omnira/omnira/internal/messages/application"
	messagesports "github.com/omnira/omnira/internal/messages/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	sectorsadapters "github.com/omnira/omnira/internal/sectors/adapters"
)

// The coordinated write side of the external API (ADR-0048 H5). Five commands on a conversation, all with the same shape:
//
//	authenticate (outside) -> ONE transaction: re-check the installation (installation lock first) -> the conversation must be visible (else the uniform 404)
//	-> lock the conversation -> look for a receipt of this (installation, operation, key) -> prove the control (holder, version) -> execute the SAME application
//	the Workspace and the Hub use -> write the receipt -> commit.
//
// The receipt is written in the transaction that did the work, so a retry after a timeout finds either everything or nothing. A failure leaves no receipt
// (its transaction rolls back). 202 means QUEUED: a message handed to the delivery job, never that WhatsApp delivered it.

// MessageSender queues a message for an application (messages/application.ExternalSender).
type MessageSender interface {
	Send(ctx context.Context, m messagesapp.ExternalMessage) (*messagesports.QueuedMessage, error)
}

// NoteWriter records an internal note for an application (ADR-0053; messages/application.ExternalNoteWriter). Deliberately a different
// interface from MessageSender: nothing that satisfies it may also queue a delivery job.
type NoteWriter interface {
	Note(ctx context.Context, n messagesapp.ExternalNote) (*messagesports.QueuedMessage, error)
}

// FinalizeOutcome is what a finalize reports.
type FinalizeOutcome struct {
	ClosureID uuid.UUID
	Reason    string
}

// Finalizer ends the attendance (attendance/application.FinalizeByExternalApp), in the caller's system session with the conversation locked.
type Finalizer func(ctx context.Context, installationID, conversationID uuid.UUID, reason, note string, controlVersion int64, notifyProtocol *bool) (FinalizeOutcome, error)

// MediaSender is the file side of an application's messages (messagesapp.Attachments): upload one file, then queue the message that carries it.
type MediaSender interface {
	UploadExternal(ctx context.Context, installation, conversationID uuid.UUID, declaredName, declaredMime string, data []byte) (*messagesports.OutboundAttachment, error)
	SendMediaExternal(ctx context.Context, m messagesapp.ExternalMediaMessage) (*messagesports.QueuedMessage, error)
}

// QueueOpener says whether a queue is inside its calendar now (sectors/adapters.QueueAvailability).
type QueueOpener func(ctx context.Context, q platformdb.Querier, tenant, queue uuid.UUID) (bool, error)

// WriteDeps are the pieces of the platform the write side reuses; none of them is reimplemented here.
type WriteDeps struct {
	Sender    MessageSender
	Note      NoteWriter  // ADR-0053: records internal notes, never a delivery
	Media     MediaSender // nil: this deployment does not deliver files (outbound media off)
	Finalize  Finalizer
	Eligible  conversationcontrol.Eligible
	QueueOpen QueueOpener
	Starter   ConversationStarter // ADR-0050: create-or-reuse a conversation by phone
}

// SectorsEligible is the rule the router applies to a person of a queue (membership, active, shift, capacity). Presence is not asked: it lives in Valkey,
// which is not transactional, and a transfer to a named person is the application's explicit choice.
func SectorsEligible(ctx context.Context, q platformdb.Querier, tenant, queue, user uuid.UUID) (bool, error) {
	// The same protocol the router follows before it hands a conversation to a person (engine.tryAssignLocked): the member row, then the person's advisory lock,
	// and only THEN the fresh snapshot. Both stay held until the transaction ends, so the router (or another transfer) cannot use up the capacity between the
	// look and the update.
	var member int
	if err := q.QueryRow(ctx, `SELECT 1 FROM queue_members WHERE tenant_id=$1 AND queue_id=$2 AND user_id=$3 FOR UPDATE`, tenant, queue, user).Scan(&member); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('routing:agent:' || $1::text || ':' || $2::text, 0))`, tenant, user); err != nil {
		return false, err
	}
	snap, err := sectorsadapters.SnapshotWith(ctx, q, tenant, queue, time.Now(), nil, sectorsadapters.SnapshotOptions{Only: &user, SkipPresence: true})
	if err != nil {
		return false, err
	}
	for _, a := range snap.Agents {
		if a.UserID == user {
			return a.Eligible, nil
		}
	}
	return false, nil
}

// SectorsQueueOpen says whether the queue's calendar is open now (no calendar anywhere: open).
func SectorsQueueOpen(ctx context.Context, q platformdb.Querier, tenant, queue uuid.UUID) (bool, error) {
	a, err := sectorsadapters.QueueAvailability(ctx, q, tenant, queue, time.Now())
	return a.Open, err
}

type WriteHandler struct {
	pool *pgxpool.Pool
	d    WriteDeps
	ctl  conversationcontrol.Service
	now  func() time.Time
}

func NewWriteHandler(pool *pgxpool.Pool, d WriteDeps) *WriteHandler {
	return &WriteHandler{pool: pool, d: d, now: time.Now}
}

const maxCommandBody = 64 << 10

var (
	idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
	ifMatchPattern        = regexp.MustCompile(`^"control-([0-9]{1,18})"$`)
	errReceiptRace        = errors.New("another request recorded this command first")
)

// cmdError is a refusal with a stable code; etag, when set, is the control version the conversation has NOW (so the client can see why it was refused).
type cmdError struct {
	status       int
	code, title  string
	detail       string
	currentValue *int64
}

func (e *cmdError) Error() string { return e.code }

func refuse(status int, code, title, detail string, st *conversationcontrol.State) error {
	e := &cmdError{status: status, code: code, title: title, detail: detail}
	if st != nil && st.ConversationID != uuid.Nil {
		v := st.Version
		e.currentValue = &v
	}
	return e
}

type cmdInput struct {
	p         *domain.Principal
	conv      uuid.UUID
	ifMatch   *int64
	payload   any
	al        Allowlists
	vis       Visibility
	queueID   *uuid.UUID
	requestID string
}

type cmdOutput struct {
	status    int
	body      any
	messageID *uuid.UUID
	version   int64
	note      map[string]any // extra, non-sensitive audit metadata
}

type ifMatchPolicy int

const (
	ifMatchOptional ifMatchPolicy = iota
	ifMatchRequired
)

type cmdSpec struct {
	op      string
	ifMatch ifMatchPolicy
	decode  func(body []byte) (payload any, canonical []byte, err error)
	run     func(h *WriteHandler, ctx context.Context, q platformdb.Querier, in *cmdInput) (*cmdOutput, error)
}

// ---- payloads ----

// ExternalAgentID is a free label the application may attach to say which of ITS operators acted. It is recorded in the history and the audit as UNVERIFIED
// metadata (never an identity, never a user of OMNIRA, never used to decide anything).
const maxExternalAgentID = 80

var externalAgentPattern = regexp.MustCompile(`^[A-Za-z0-9._:@/+ -]{1,80}$`)

func checkAgentRef(ref string) error {
	if ref != "" && !externalAgentPattern.MatchString(ref) {
		return errors.New("external_agent_id must be 1 to 80 characters of letters, digits and . _ : @ / + - or space")
	}
	return nil
}

type emptyPayload struct {
	ExternalAgentID string `json:"external_agent_id,omitempty"`
}

func decodeEmpty(body []byte) (any, []byte, error) {
	var v emptyPayload
	if len(bytes.TrimSpace(body)) > 0 {
		if err := strictJSON(body, &v); err != nil {
			return nil, nil, err
		}
	}
	if err := checkAgentRef(v.ExternalAgentID); err != nil {
		return nil, nil, err
	}
	c, _ := json.Marshal(v)
	return v, c, nil
}

// agentRefOf reads the label out of whichever payload the command carried.
func agentRefOf(p any) string {
	switch v := p.(type) {
	case emptyPayload:
		return v.ExternalAgentID
	case transferPayload:
		return v.ExternalAgentID
	case messagePayload:
		return v.ExternalAgentID
	case finalizePayload:
		return v.ExternalAgentID
	case notePayload:
		return v.ExternalAgentID
	case startPayload:
		return v.ExternalAgentID
	}
	return ""
}

func strictJSON(body []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return errors.New("the body must be a JSON object with the documented fields only")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) { // anything after the object (a second document, even whitespace-separated) is refused
		return errors.New("the body must be one JSON object")
	}
	return nil
}

type transferPayload struct {
	QueueID         uuid.UUID  `json:"queue_id"`
	UserID          *uuid.UUID `json:"user_id,omitempty"`
	ExternalAgentID string     `json:"external_agent_id,omitempty"`
}

func decodeTransfer(body []byte) (any, []byte, error) {
	var v transferPayload
	if err := strictJSON(body, &v); err != nil {
		return nil, nil, err
	}
	if v.QueueID == uuid.Nil {
		return nil, nil, errors.New("queue_id is required")
	}
	if err := checkAgentRef(v.ExternalAgentID); err != nil {
		return nil, nil, err
	}
	c, _ := json.Marshal(v)
	return v, c, nil
}

type templatePayload struct {
	ID     uuid.UUID `json:"id"`
	Params []string  `json:"params,omitempty"`
}

type messagePayload struct {
	// Text is the message, or the caption of the file when AttachmentID is set (plain text, up to 1024 characters).
	Text            string           `json:"text,omitempty"`
	Template        *templatePayload `json:"template,omitempty"`
	AttachmentID    *uuid.UUID       `json:"attachment_id,omitempty"` // a file this installation uploaded to this conversation
	ExternalAgentID string           `json:"external_agent_id,omitempty"`
}

func decodeMessage(body []byte) (any, []byte, error) {
	var v messagePayload
	if err := strictJSON(body, &v); err != nil {
		return nil, nil, err
	}
	if v.AttachmentID != nil {
		if v.Template != nil || *v.AttachmentID == uuid.Nil {
			return nil, nil, errors.New("attachment_id goes alone, with an optional text as the caption")
		}
	} else if (v.Template == nil) == (strings.TrimSpace(v.Text) == "") {
		return nil, nil, errors.New("send exactly one of text and template, or an attachment_id with an optional caption")
	}
	if v.Template != nil && v.Template.ID == uuid.Nil {
		return nil, nil, errors.New("template.id is required")
	}
	if err := checkAgentRef(v.ExternalAgentID); err != nil {
		return nil, nil, err
	}
	c, _ := json.Marshal(v)
	return v, c, nil
}

// notePayload (ADR-0053): plain text only — no template, no attachment. A note is never sent to the contact, so none of the delivery
// concerns of messagePayload apply here.
type notePayload struct {
	Text            string `json:"text"`
	ExternalAgentID string `json:"external_agent_id,omitempty"`
}

func decodeNote(body []byte) (any, []byte, error) {
	var v notePayload
	if err := strictJSON(body, &v); err != nil {
		return nil, nil, err
	}
	// emptiness and length are judged by ExternalNoteWriter (messagesapp.ErrInvalidNote), not here, so both ways of sending "no real text" answer
	// with the same code instead of two different ones depending on which layer happened to notice first.
	if err := checkAgentRef(v.ExternalAgentID); err != nil {
		return nil, nil, err
	}
	c, _ := json.Marshal(v)
	return v, c, nil
}

type finalizePayload struct {
	Reason          string `json:"reason,omitempty"`
	Note            string `json:"note,omitempty"`
	ExternalAgentID string `json:"external_agent_id,omitempty"`
	// NotifyProtocol: tell the customer the protocol number as the attendance ends. Absent = yes (the same default as the Workspace).
	NotifyProtocol *bool `json:"notify_protocol,omitempty"`
}

func decodeFinalize(body []byte) (any, []byte, error) {
	var v finalizePayload
	if len(bytes.TrimSpace(body)) > 0 {
		if err := strictJSON(body, &v); err != nil {
			return nil, nil, err
		}
	}
	if v.Reason == "" {
		v.Reason = string(attendancedomain.ReasonResolved)
	}
	if !attendancedomain.CloseReason(v.Reason).Valid() {
		return nil, nil, errors.New("reason must be one of resolved, no_response, duplicate, spam, transferred, other")
	}
	if err := checkAgentRef(v.ExternalAgentID); err != nil {
		return nil, nil, err
	}
	c, _ := json.Marshal(v)
	return v, c, nil
}

// ---- documents ----

type controlDoc struct {
	ConversationID uuid.UUID  `json:"conversation_id"`
	Status         string     `json:"status"`
	Control        ControlDTO `json:"control"`
}

func controlOf(st conversationcontrol.State, installation uuid.UUID) ControlDTO {
	c := ControlDTO{State: "unassigned", Version: st.Version}
	if st.Kind != nil {
		c.State = "held"
		c.Holder = &HolderDTO{Kind: string(*st.Kind), Mine: st.HeldBy(conversationcontrol.KindCRMApplication, installation)}
		if st.Origin != nil {
			o := string(*st.Origin)
			c.Origin = &o
		}
		c.Since = st.Since
	}
	return c
}

type commandResult struct {
	controlDoc
	Changed   *bool      `json:"changed,omitempty"`
	MessageID *uuid.UUID `json:"message_id,omitempty"`
	Delivery  string     `json:"delivery,omitempty"` // "queued": handed to the delivery job, nothing more
	ClosureID *uuid.UUID `json:"closure_id,omitempty"`
	Reason    string     `json:"reason,omitempty"`
}

func result(st conversationcontrol.State, installation uuid.UUID) commandResult {
	return commandResult{controlDoc: controlDoc{ConversationID: st.ConversationID, Status: st.Status, Control: controlOf(st, installation)}}
}

// ---- the five commands ----

func (h *WriteHandler) claimSpec() cmdSpec {
	return cmdSpec{op: "claim", ifMatch: ifMatchOptional, decode: decodeEmpty, run: func(h *WriteHandler, ctx context.Context, q platformdb.Querier, in *cmdInput) (*cmdOutput, error) {
		cur, err := h.ctl.Lock(ctx, q, in.p.TenantID, in.conv)
		if err != nil {
			return nil, h.fromControl(err, &cur)
		}
		// Outside the sector's calendar an application may not take a conversation unless the administrator allowed it for this installation.
		// Repeating a claim it already holds changes nothing, so the calendar is not asked again.
		if !cur.HeldBy(conversationcontrol.KindCRMApplication, in.p.InstallationID) && !in.p.AllowOutsideHours && h.d.QueueOpen != nil {
			var queue *uuid.UUID // read now, with the conversation locked: the queue it is in is the one whose calendar decides
			if err := q.QueryRow(ctx, `SELECT queue_id FROM conversations WHERE tenant_id = $1 AND id = $2`, in.p.TenantID, in.conv).Scan(&queue); err != nil {
				return nil, err
			}
			open := true
			if queue != nil {
				var err error
				if open, err = h.d.QueueOpen(ctx, q, in.p.TenantID, *queue); err != nil {
					return nil, err
				}
			}
			if !open {
				return nil, refuse(http.StatusConflict, "outside_hours", "Outside the sector's hours", "The conversation's queue is closed now and this installation is not allowed to claim outside hours.", &cur)
			}
		}
		st, changed, err := h.ctl.ClaimByApplication(ctx, q, in.p.TenantID, in.conv, in.p.InstallationID, in.ifMatch, h.actor(in))
		if err != nil {
			return nil, h.fromControl(err, &st)
		}
		r := result(st, in.p.InstallationID)
		r.Changed = &changed
		return &cmdOutput{status: http.StatusOK, body: r, version: st.Version}, nil
	}}
}

func (h *WriteHandler) releaseSpec() cmdSpec {
	return cmdSpec{op: "release", ifMatch: ifMatchRequired, decode: decodeEmpty, run: func(h *WriteHandler, ctx context.Context, q platformdb.Querier, in *cmdInput) (*cmdOutput, error) {
		st, err := h.ctl.ReleaseByApplication(ctx, q, in.p.TenantID, in.conv, in.p.InstallationID, *in.ifMatch, h.actor(in))
		if err != nil {
			return nil, h.fromControl(err, &st)
		}
		changed := true
		r := result(st, in.p.InstallationID)
		r.Changed = &changed
		return &cmdOutput{status: http.StatusOK, body: r, version: st.Version}, nil
	}}
}

// heartbeat: "I am still here". It changes nothing about the conversation; it proves the holder and the version and (like every command of the holder) renews the lease.
func (h *WriteHandler) heartbeatSpec() cmdSpec {
	return cmdSpec{op: "heartbeat", ifMatch: ifMatchRequired, decode: decodeEmpty, run: func(h *WriteHandler, ctx context.Context, q platformdb.Querier, in *cmdInput) (*cmdOutput, error) {
		cur, err := h.ctl.Lock(ctx, q, in.p.TenantID, in.conv)
		if err != nil {
			return nil, h.fromControl(err, &cur)
		}
		if err := h.mustHold(cur, in); err != nil {
			return nil, err
		}
		changed := false
		r := result(cur, in.p.InstallationID)
		r.Changed = &changed
		return &cmdOutput{status: http.StatusOK, body: r, version: cur.Version}, nil
	}}
}

func (h *WriteHandler) transferSpec() cmdSpec {
	return cmdSpec{op: "transfer", ifMatch: ifMatchRequired, decode: decodeTransfer, run: func(h *WriteHandler, ctx context.Context, q platformdb.Querier, in *cmdInput) (*cmdOutput, error) {
		pl := in.payload.(transferPayload)
		// the target must be inside what the installation may see (a queue it could not read would make the conversation vanish from its view)
		if len(in.al.QueueIDs) > 0 && !containsUUID(in.al.QueueIDs, pl.QueueID) {
			return nil, refuse(http.StatusForbidden, "queue_not_allowed", "Queue not allowed", "The queue is not one this installation may use.", nil)
		}
		st, err := h.ctl.TransferByApplication(ctx, q, in.p.TenantID, in.conv, in.p.InstallationID, *in.ifMatch,
			conversationcontrol.Target{QueueID: pl.QueueID, UserID: pl.UserID}, h.actor(in), h.d.Eligible)
		if err != nil {
			return nil, h.fromControl(err, &st)
		}
		changed := true
		r := result(st, in.p.InstallationID)
		r.Changed = &changed
		return &cmdOutput{status: http.StatusOK, body: r, version: st.Version}, nil
	}}
}

func (h *WriteHandler) messageSpec() cmdSpec {
	return cmdSpec{op: "message", ifMatch: ifMatchRequired, decode: decodeMessage, run: func(h *WriteHandler, ctx context.Context, q platformdb.Querier, in *cmdInput) (*cmdOutput, error) {
		pl := in.payload.(messagePayload)
		cur, err := h.ctl.Lock(ctx, q, in.p.TenantID, in.conv)
		if err != nil {
			return nil, h.fromControl(err, &cur)
		}
		if err := h.mustHold(cur, in); err != nil {
			return nil, err
		}
		if pl.AttachmentID != nil {
			if h.d.Media == nil {
				return nil, refuse(http.StatusUnprocessableEntity, "media_unavailable", "Files are not available", "This deployment does not deliver files.", &cur)
			}
			msg, err := h.d.Media.SendMediaExternal(ctx, messagesapp.ExternalMediaMessage{InstallationID: in.p.InstallationID, ConversationID: in.conv, ControlVersion: cur.Version, AttachmentID: *pl.AttachmentID, Caption: pl.Text})
			if err != nil {
				return nil, h.fromMessage(err, &cur)
			}
			r := result(cur, in.p.InstallationID)
			r.MessageID, r.Delivery = &msg.ID, "queued"
			return &cmdOutput{status: http.StatusAccepted, body: r, messageID: &msg.ID, version: cur.Version}, nil
		}
		m := messagesapp.ExternalMessage{InstallationID: in.p.InstallationID, ConversationID: in.conv, ControlVersion: cur.Version, Text: pl.Text, Now: h.now()}
		if pl.Template != nil {
			id := pl.Template.ID
			m.TemplateID, m.TemplateParams = &id, pl.Template.Params
		}
		msg, err := h.d.Sender.Send(ctx, m)
		if err != nil {
			return nil, h.fromMessage(err, &cur)
		}
		r := result(cur, in.p.InstallationID)
		r.MessageID, r.Delivery = &msg.ID, "queued"
		return &cmdOutput{status: http.StatusAccepted, body: r, messageID: &msg.ID, version: cur.Version}, nil
	}}
}

// noteSpec (ADR-0053): an internal note, never sent to the contact. Same hold/If-Match discipline as a real message — an installation records a
// note only on a conversation it currently holds — but 201, never 202/"queued": nothing is pending delivery.
func (h *WriteHandler) noteSpec() cmdSpec {
	return cmdSpec{op: "note", ifMatch: ifMatchRequired, decode: decodeNote, run: func(h *WriteHandler, ctx context.Context, q platformdb.Querier, in *cmdInput) (*cmdOutput, error) {
		pl := in.payload.(notePayload)
		cur, err := h.ctl.Lock(ctx, q, in.p.TenantID, in.conv)
		if err != nil {
			return nil, h.fromControl(err, &cur)
		}
		if err := h.mustHold(cur, in); err != nil {
			return nil, err
		}
		msg, err := h.d.Note.Note(ctx, messagesapp.ExternalNote{InstallationID: in.p.InstallationID, ConversationID: in.conv, ControlVersion: cur.Version, Text: pl.Text})
		if err != nil {
			return nil, h.fromNote(err, &cur)
		}
		r := result(cur, in.p.InstallationID)
		r.MessageID = &msg.ID
		return &cmdOutput{status: http.StatusCreated, body: r, messageID: &msg.ID, version: cur.Version}, nil
	}}
}

func (h *WriteHandler) finalizeSpec() cmdSpec {
	return cmdSpec{op: "finalize", ifMatch: ifMatchRequired, decode: decodeFinalize, run: func(h *WriteHandler, ctx context.Context, q platformdb.Querier, in *cmdInput) (*cmdOutput, error) {
		pl := in.payload.(finalizePayload)
		cur, err := h.ctl.Lock(ctx, q, in.p.TenantID, in.conv)
		if err != nil {
			return nil, h.fromControl(err, &cur)
		}
		if err := h.mustHold(cur, in); err != nil {
			return nil, err
		}
		out, err := h.d.Finalize(ctx, in.p.InstallationID, in.conv, pl.Reason, pl.Note, cur.Version, pl.NotifyProtocol)
		if err != nil {
			return nil, h.fromAttendance(err, &cur)
		}
		st, err := h.ctl.Get(ctx, q, in.p.TenantID, in.conv) // closing released the hold (the version moved)
		if err != nil {
			return nil, err
		}
		r := result(st, in.p.InstallationID)
		r.ClosureID, r.Reason = &out.ClosureID, out.Reason
		return &cmdOutput{status: http.StatusOK, body: r, version: st.Version}, nil
	}}
}

// mustHold: sending and finalizing need THIS installation to hold the conversation at exactly the version it presented.
func (h *WriteHandler) mustHold(cur conversationcontrol.State, in *cmdInput) error {
	if cur.Status != "open" {
		return refuse(http.StatusConflict, "conversation_closed", "Conversation closed", "The attendance was finalized.", &cur)
	}
	if !cur.HeldBy(conversationcontrol.KindCRMApplication, in.p.InstallationID) {
		if cur.Free() {
			return refuse(http.StatusConflict, "not_holder", "Not the holder", "This installation does not hold the conversation: claim it first.", &cur)
		}
		return refuse(http.StatusConflict, "held_by_other", "Held by someone else", "Another holder has the conversation.", &cur)
	}
	if in.ifMatch == nil || *in.ifMatch != cur.Version {
		return refuse(http.StatusPreconditionFailed, "stale_control_version", "The conversation changed", "The control version is not the current one; read the conversation again.", &cur)
	}
	return nil
}

func (h *WriteHandler) actor(in *cmdInput) conversationcontrol.Actor {
	return conversationcontrol.Actor{Kind: conversationcontrol.ActorCRMApplication, ID: in.p.InstallationID, RequestID: in.requestID, ClaimedAgentRef: agentRefOf(in.payload)}
}

func containsUUID(list []uuid.UUID, id uuid.UUID) bool {
	for _, x := range list {
		if x == id {
			return true
		}
	}
	return false
}

// ---- error mapping ----

func (h *WriteHandler) fromControl(err error, st *conversationcontrol.State) error {
	switch {
	case errors.Is(err, conversationcontrol.ErrNotFound):
		return errNotVisible
	case errors.Is(err, conversationcontrol.ErrStaleVersion):
		return refuse(http.StatusPreconditionFailed, "stale_control_version", "The conversation changed", "The control version is not the current one; read the conversation again.", st)
	case errors.Is(err, conversationcontrol.ErrHeldByOther):
		return refuse(http.StatusConflict, "held_by_other", "Held by someone else", "Another holder has the conversation.", st)
	case errors.Is(err, conversationcontrol.ErrNotHolder):
		return refuse(http.StatusConflict, "not_holder", "Not the holder", "This installation does not hold the conversation.", st)
	case errors.Is(err, conversationcontrol.ErrClosed):
		return refuse(http.StatusConflict, "conversation_closed", "Conversation closed", "The attendance was finalized.", st)
	case errors.Is(err, conversationcontrol.ErrInvalidTarget):
		return refuse(http.StatusUnprocessableEntity, "invalid_target", "Invalid target", "The queue does not exist or the person cannot receive the conversation now.", st)
	case errors.Is(err, conversationcontrol.ErrNotAllowed):
		return refuse(http.StatusForbidden, "forbidden", "Forbidden", "This operation is not allowed.", st)
	}
	return err
}

func (h *WriteHandler) fromMessage(err error, st *conversationcontrol.State) error {
	switch {
	case errors.Is(err, messagesapp.ErrNotFound):
		return errNotVisible
	case errors.Is(err, messagesapp.ErrConversationClosed):
		return refuse(http.StatusConflict, "conversation_closed", "Conversation closed", "The attendance was finalized.", st)
	case errors.Is(err, messagesapp.ErrChannelUnavailable):
		return refuse(http.StatusConflict, "channel_unavailable", "Channel unavailable", "The conversation has no active text channel.", st)
	case errors.Is(err, messagesapp.ErrConversationChanged):
		return refuse(http.StatusConflict, "conversation_changed", "The conversation changed", "The holder or the channel changed while the message was being queued; read the conversation again.", st)
	case errors.Is(err, messagesapp.ErrWindowClosed):
		return refuse(http.StatusUnprocessableEntity, "window_closed", "Customer-service window closed", "More than 24 hours passed since the contact wrote: send an approved template.", st)
	case errors.Is(err, messagesapp.ErrInvalidText):
		return refuse(http.StatusUnprocessableEntity, "invalid_message", "Invalid message", "Send exactly one of text (1 to 4096 characters) and template.", st)
	case errors.Is(err, messagesapp.ErrTemplateUnsupported), errors.Is(err, messagesapp.ErrTemplateNotAllowed), errors.Is(err, messagesapp.ErrTemplateParams):
		return refuse(http.StatusUnprocessableEntity, "invalid_template", "Invalid template", "The template is not approved for this conversation's line or its variables are wrong.", st)
	case errors.Is(err, messagesapp.ErrForbidden):
		return refuse(http.StatusForbidden, "forbidden", "Forbidden", "This operation is not allowed.", st)
	}
	return fromMedia(err, st)
}

// fromNote maps what recording an internal note can refuse (ADR-0053) — a small subset of fromMessage's: no window, no channel, no template.
func (h *WriteHandler) fromNote(err error, st *conversationcontrol.State) error {
	switch {
	case errors.Is(err, messagesapp.ErrNotFound):
		return errNotVisible
	case errors.Is(err, messagesapp.ErrConversationChanged):
		return refuse(http.StatusConflict, "conversation_changed", "The conversation changed", "The holder changed while the note was being recorded; read the conversation again.", st)
	case errors.Is(err, messagesapp.ErrInvalidNote):
		return refuse(http.StatusUnprocessableEntity, "invalid_note", "Invalid note", "Send text of 1 to 4000 characters.", st)
	case errors.Is(err, messagesapp.ErrForbidden):
		return refuse(http.StatusForbidden, "forbidden", "Forbidden", "This operation is not allowed.", st)
	}
	return err
}

// fromMedia maps what a file operation can refuse; anything else is left to the caller (an unexpected error stays a 500 with no detail).
func fromMedia(err error, st *conversationcontrol.State) error {
	var rejected *messagesapp.AttachmentRejected
	switch {
	case errors.As(err, &rejected):
		if rejected.Reason == mediadomain.ReasonTooLarge {
			return refuse(http.StatusRequestEntityTooLarge, "file_too_large", "File too large", "The limit is 16 MiB.", st)
		}
		return refuse(http.StatusUnprocessableEntity, "file_rejected", "File not accepted", "The file was not accepted: "+rejected.Reason+".", st)
	case errors.Is(err, messagesapp.ErrMediaUnsupported):
		return refuse(http.StatusUnprocessableEntity, "media_unsupported", "Files are not supported here", "This line or this deployment cannot send files.", st)
	case errors.Is(err, messagesapp.ErrInvalidCaption):
		return refuse(http.StatusUnprocessableEntity, "invalid_caption", "Invalid caption", "The caption is plain text of up to 1024 characters.", st)
	case errors.Is(err, messagesapp.ErrAttachmentUnavailable):
		return refuse(http.StatusUnprocessableEntity, "attachment_unavailable", "Attachment not available", "The upload does not exist for this installation in this conversation, was already sent or has expired.", st)
	case errors.Is(err, messagesapp.ErrTooManyAttachments):
		return refuse(http.StatusConflict, "too_many_attachments", "Too many unsent files", "Send or let expire the pending files of this conversation first.", st)
	case errors.Is(err, messagesapp.ErrAttachmentQuota):
		return refuse(http.StatusConflict, "attachment_quota", "Storage quota reached", "Send or let expire the pending files first.", st)
	case errors.Is(err, messagesapp.ErrAttachmentRate):
		return refuse(http.StatusTooManyRequests, "upload_rate", "Too many uploads", "Wait a moment and try again.", st)
	case errors.Is(err, messagesapp.ErrAttachmentInfected):
		return refuse(http.StatusUnprocessableEntity, "file_blocked", "File blocked", "The file was blocked by the antivirus.", st)
	case errors.Is(err, messagesapp.ErrScannerUnavailable):
		return refuse(http.StatusServiceUnavailable, "scanner_unavailable", "Antivirus unavailable", "The file was not accepted: try again later.", st)
	case errors.Is(err, entitlements.ErrDisabled):
		return refuse(http.StatusForbidden, "capability_disabled", "Capability disabled", "Sending files is not enabled for this company.", st)
	}
	return err
}

func (h *WriteHandler) fromAttendance(err error, st *conversationcontrol.State) error {
	switch {
	case errors.Is(err, attendancedomain.ErrNotAContact):
		return refuse(http.StatusUnprocessableEntity, "not_a_customer_conversation", "Not a customer conversation", "Only a conversation with a customer can be finalized.", st)
	case errors.Is(err, attendancedomain.ErrInvalid):
		return refuse(http.StatusUnprocessableEntity, "invalid_request", "Invalid request", "The reason or the note is not valid.", st)
	case errors.Is(err, attendancedomain.ErrForbidden):
		return refuse(http.StatusForbidden, "forbidden", "Forbidden", "This operation is not allowed.", st)
	case errors.Is(err, conversationcontrol.ErrNotFound):
		return errNotVisible
	}
	return err
}

// ---- upload of a file ----

// UploadedFile is what the upload buffer read before the database was touched: the declared name and type, and the bytes (already size-limited).
type UploadedFile func(ctx context.Context) (name, mime string, data []byte, ok bool)

type uploadedResponse struct {
	ID             uuid.UUID `json:"attachment_id"`
	ConversationID uuid.UUID `json:"conversation_id"`
	Kind           string    `json:"kind"`
	Mime           string    `json:"mime"`
	SizeBytes      int64     `json:"size_bytes"`
	FileName       string    `json:"file_name"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// UploadAttachment: POST /external/conversations/{id}/attachments (multipart, one "file" part), scope message.send, for a body the upload buffer already read.
// It is not a coordinated command: it changes nothing about the conversation and needs no receipt (a retry is another pending file, bounded by the quotas and
// the 24 h expiry). It does need the conversation to be one the installation may see AND hold right now, so an application cannot park files on conversations
// it does not serve; the message that carries the file proves the hold again at the control version.
func (h *WriteHandler) UploadAttachment(file UploadedFile) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required", "Send Authorization: Bearer <integration token>.")
			return
		}
		conv, ok := uuidParam(r, "id")
		if !ok {
			problemNotFound(w, r)
			return
		}
		name, mimeType, data, ok := file(r.Context())
		if !ok {
			problemBad(w, r, "A multipart \"file\" part is required.")
			return
		}
		if h.d.Media == nil {
			WriteProblem(w, r, http.StatusUnprocessableEntity, "media_unavailable", "Files are not available", "This deployment does not deliver files.")
			return
		}
		var att *messagesports.OutboundAttachment
		err := platformdb.WithSystemTenantSession(r.Context(), h.pool, p.TenantID, func(ctx context.Context) error {
			q := platformdb.QuerierFromContext(ctx, h.pool)
			if err := RecheckPrincipal(ctx, q, p); err != nil {
				return err
			}
			al, err := LoadAllowlists(ctx, q, p)
			if err != nil {
				return err
			}
			in := &cmdInput{p: p, conv: conv, al: al, vis: NewVisibility(p, al)}
			visible, _, err := h.visibleConversation(ctx, q, in)
			if err != nil {
				return err
			}
			if !visible {
				return errNotVisible
			}
			cur, err := h.ctl.Get(ctx, q, p.TenantID, conv)
			if err != nil {
				return h.fromControl(err, nil)
			}
			if cur.Status != "open" {
				return refuse(http.StatusConflict, "conversation_closed", "Conversation closed", "The attendance was finalized.", &cur)
			}
			if !cur.HeldBy(conversationcontrol.KindCRMApplication, p.InstallationID) {
				if cur.Free() {
					return refuse(http.StatusConflict, "not_holder", "Not the holder", "This installation does not hold the conversation: claim it first.", &cur)
				}
				return refuse(http.StatusConflict, "held_by_other", "Held by someone else", "Another holder has the conversation.", &cur)
			}
			att, err = h.d.Media.UploadExternal(ctx, p.InstallationID, conv, name, mimeType, data)
			if err != nil {
				return h.fromMessage(err, &cur)
			}
			return nil
		})
		if err != nil {
			h.fail(w, r, err)
			return
		}
		externalMetric.Inc("served")
		hd := w.Header()
		hd.Set("Content-Type", "application/json")
		hd.Set("Cache-Control", "no-store")
		hd.Set("X-Content-Type-Options", "nosniff")
		if id := RequestIDFrom(r.Context()); id != "" {
			hd.Set("X-Request-ID", id)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(uploadedResponse{ID: att.ID, ConversationID: conv, Kind: att.Kind, Mime: att.Mime, SizeBytes: att.SizeBytes, FileName: att.FileName, ExpiresAt: att.ExpiresAt.UTC()})
	})
}

// ---- the engine ----

func (h *WriteHandler) Claim() http.Handler     { return h.handle(h.claimSpec()) }
func (h *WriteHandler) Release() http.Handler   { return h.handle(h.releaseSpec()) }
func (h *WriteHandler) Transfer() http.Handler  { return h.handle(h.transferSpec()) }
func (h *WriteHandler) Message() http.Handler   { return h.handle(h.messageSpec()) }
func (h *WriteHandler) Note() http.Handler      { return h.handle(h.noteSpec()) }
func (h *WriteHandler) Finalize() http.Handler  { return h.handle(h.finalizeSpec()) }
func (h *WriteHandler) Heartbeat() http.Handler { return h.handle(h.heartbeatSpec()) }

func (h *WriteHandler) handle(spec cmdSpec) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			WriteProblem(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required", "Send Authorization: Bearer <integration token>.")
			return
		}
		conv, ok := uuidParam(r, "id")
		if !ok {
			problemNotFound(w, r)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if !idempotencyKeyPattern.MatchString(key) {
			problemBad(w, r, "Idempotency-Key is required: 8 to 128 characters of A-Z a-z 0-9 . _ : -")
			return
		}
		var ifMatch *int64
		if raw := r.Header.Get("If-Match"); raw != "" {
			m := ifMatchPattern.FindStringSubmatch(raw)
			if m == nil {
				problemBad(w, r, `If-Match must be the control version in quotes, for example "control-7".`)
				return
			}
			n, _ := strconv.ParseInt(m[1], 10, 64)
			ifMatch = &n
		} else if spec.ifMatch == ifMatchRequired {
			externalMetric.Inc("precondition_failed")
			WriteProblem(w, r, http.StatusPreconditionRequired, "if_match_required", "If-Match is required", `Send If-Match: "control-N" with the control version you read.`)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCommandBody))
		if err != nil {
			problemBad(w, r, "The body is too large or could not be read.")
			return
		}
		if len(bytes.TrimSpace(raw)) > 0 {
			if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
				externalMetric.Inc("bad_request")
				WriteProblem(w, r, http.StatusUnsupportedMediaType, "unsupported_media_type", "Unsupported media type", "Send Content-Type: application/json.")
				return
			}
		}
		payload, canonical, err := spec.decode(raw)
		if err != nil {
			externalMetric.Inc("unprocessable")
			WriteProblem(w, r, http.StatusUnprocessableEntity, "invalid_request", "Invalid request", err.Error())
			return
		}
		sum := sha256.Sum256([]byte(conv.String() + "\n" + string(canonical))) // the operation is part of the receipt's key, not of the hash
		hash := hex.EncodeToString(sum[:])
		in := &cmdInput{p: p, conv: conv, ifMatch: ifMatch, payload: payload, requestID: RequestIDFrom(r.Context())}

		var out *cmdOutput
		var stored []byte
		var replay bool
		for attempt := 0; attempt < 3; attempt++ {
			out, stored, replay, err = h.transact(r.Context(), spec, in, key, hash)
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
		if id := RequestIDFrom(r.Context()); id != "" {
			hd.Set("X-Request-ID", id)
		}
		if replay {
			hd.Set("Idempotent-Replay", "true")
		}
		w.WriteHeader(out.status)
		_, _ = w.Write(stored)
	})
}

// transact is the whole command in one transaction. It returns the answer's bytes: for a replay they are the ones stored by the first execution.
func (h *WriteHandler) transact(ctx context.Context, spec cmdSpec, in *cmdInput, key, hash string) (*cmdOutput, []byte, bool, error) {
	var out *cmdOutput
	var stored []byte
	var replay bool
	err := platformdb.WithSystemTenantSession(ctx, h.pool, in.p.TenantID, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, h.pool)
		out, stored, replay = nil, nil, false
		// 1. the installation, first (the lock every revocation also takes)
		if err := RecheckPrincipal(ctx, q, in.p); err != nil {
			return err
		}
		al, err := LoadAllowlists(ctx, q, in.p)
		if err != nil {
			return err
		}
		in.al, in.vis = al, NewVisibility(in.p, al)
		// 2. is the conversation one this installation may see? (read without a lock: a conversation that is NOT visible is never locked on behalf of an application)
		visible, queue, err := h.visibleConversation(ctx, q, in)
		if err != nil {
			return err
		}
		in.queueID = queue
		if !visible {
			// a command that was already done keeps its answer even if the conversation has since left the installation's view (same key, same payload =
			// same receipt); anything else is the uniform 404
			if rec, found, err := h.receipt(ctx, q, in, spec.op, key, hash); err != nil {
				return err
			} else if found {
				out, stored, replay = rec.out, rec.body, true
				return h.renew(ctx, q, in)
			}
			return errNotVisible
		}
		// 3. lock the conversation: concurrent commands on it (and retries of one command) are serialized here, so a retry always sees the first one's receipt
		if _, err := h.ctl.Lock(ctx, q, in.p.TenantID, in.conv); err != nil {
			return h.fromControl(err, nil)
		}
		if _, err := q.Exec(ctx, `SELECT set_config('omnira.event_origin', 'crm', true)`); err != nil {
			return err
		}
		// 4. a receipt of this key? the same command gets the same answer, a different one is refused
		if rec, found, err := h.receipt(ctx, q, in, spec.op, key, hash); err != nil {
			return err
		} else if found {
			out, stored, replay = rec.out, rec.body, true
			return h.renew(ctx, q, in)
		}
		// the permission to act is judged AGAIN with the conversation locked: a queue, a line or a contact classification that changed after the first look
		// (Workspace, Routing, a reclassification) cannot be raced, because the row is ours now
		if visible, queue, err = h.visibleConversation(ctx, q, in); err != nil {
			return err
		} else if !visible {
			return errNotVisible
		}
		in.queueID = queue
		// 5. do it, with the same application the Workspace and the Hub use
		o, err := spec.run(h, ctx, q, in)
		if err != nil {
			return err
		}
		body, err := json.Marshal(o.body)
		if err != nil {
			return err
		}
		// 6. the receipt, in the same transaction
		tag, err := q.Exec(ctx, `
			INSERT INTO external_command_receipts(tenant_id, installation_id, operation, idempotency_key, request_hash, conversation_id, message_id, control_version, status_code, response)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb) ON CONFLICT DO NOTHING`,
			in.p.TenantID, in.p.InstallationID, spec.op, key, hash, in.conv, o.messageID, o.version, o.status, string(body))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errReceiptRace // the other writer wins; this transaction (and its effects) is rolled back and the command is looked up again
		}
		// 7. the audit row names the APPLICATION; no person is impersonated
		meta, _ := json.Marshal(map[string]any{"operation": spec.op, "conversation_id": in.conv.String(), "installation_id": in.p.InstallationID.String(),
			"actor_kind": "crm_application", "control_version": o.version, "status": o.status,
			"external_agent_id": agentRefOf(in.payload), "external_agent_verified": false})
		if _, err := q.Exec(ctx, `
			INSERT INTO audit_events(tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata)
			VALUES ($1, NULL, 'external_app.command', 'conversation', $2, 'success', $3, $4::jsonb)`,
			in.p.TenantID, in.conv.String(), in.requestID, string(meta)); err != nil {
			return err
		}
		out, stored = o, body
		return h.renew(ctx, q, in)
	})
	return out, stored, replay, err
}

type foundReceipt struct {
	out  *cmdOutput
	body []byte
}

// visibleConversation: is the conversation one this installation may see (same tenant, not staff, contact neither internal nor spam, inside the allow-lists)?
func (h *WriteHandler) visibleConversation(ctx context.Context, q platformdb.Querier, in *cmdInput) (bool, *uuid.UUID, error) {
	var args Args
	pred := in.vis.Conversation("c", &args)
	var queue *uuid.UUID
	if err := q.QueryRow(ctx, `SELECT c.queue_id FROM conversations c WHERE `+pred+` AND c.id = `+args.Add(in.conv), args.Values()...).Scan(&queue); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil, nil
		}
		return false, nil, err
	}
	return true, queue, nil
}

// receipt looks for the stored answer of (installation, operation, key). The same payload gets the same answer; another payload under the same key is refused.
func (h *WriteHandler) receipt(ctx context.Context, q platformdb.Querier, in *cmdInput, op, key, hash string) (foundReceipt, bool, error) {
	var rHash string
	var rStatus int
	var rBody []byte
	var rVersion *int64
	err := q.QueryRow(ctx, `
		SELECT request_hash, status_code, response::text, control_version FROM external_command_receipts
		 WHERE tenant_id = $1 AND installation_id = $2 AND operation = $3 AND idempotency_key = $4`,
		in.p.TenantID, in.p.InstallationID, op, key).Scan(&rHash, &rStatus, &rBody, &rVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return foundReceipt{}, false, nil
	}
	if err != nil {
		return foundReceipt{}, false, err
	}
	if rHash != hash {
		return foundReceipt{}, false, refuse(http.StatusUnprocessableEntity, "idempotency_key_reused", "Idempotency-Key reused", "The key was already used with a different request.", nil)
	}
	v := int64(0)
	if rVersion != nil {
		v = *rVersion
	}
	return foundReceipt{out: &cmdOutput{status: rStatus, version: v}, body: rBody}, true, nil
}

// renew pushes the lease of a conversation this installation STILL holds forward (any command, a replay or a heartbeat counts as life). With no lease configured
// for the installation, or when the hold ended with this command, nothing is written.
func (h *WriteHandler) renew(ctx context.Context, q platformdb.Querier, in *cmdInput) error {
	if in.p.LeaseSeconds == nil {
		return nil
	}
	_, err := q.Exec(ctx, `
		INSERT INTO external_conversation_leases(tenant_id, conversation_id, installation_id, lease_until, renewed_at)
		SELECT c.tenant_id, c.id, $3, now() + make_interval(secs => $4), now()
		  FROM conversations c
		 WHERE c.tenant_id = $1 AND c.id = $2 AND c.status = 'open' AND c.control_kind = 'crm_application' AND c.control_holder_id = $3
		ON CONFLICT (tenant_id, conversation_id) DO UPDATE SET installation_id = EXCLUDED.installation_id, lease_until = EXCLUDED.lease_until, renewed_at = now()`,
		in.p.TenantID, in.conv, in.p.InstallationID, float64(*in.p.LeaseSeconds))
	return err
}

func (h *WriteHandler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var ce *cmdError
	if errors.As(err, &ce) {
		switch ce.status {
		case http.StatusConflict:
			externalMetric.Inc("conflict")
		case http.StatusPreconditionFailed:
			externalMetric.Inc("precondition_failed")
		case http.StatusUnprocessableEntity:
			externalMetric.Inc("unprocessable")
		case http.StatusForbidden:
			externalMetric.Inc("forbidden_scope")
		}
		if ce.currentValue != nil {
			w.Header().Set("ETag", `"control-`+strconv.FormatInt(*ce.currentValue, 10)+`"`)
		}
		WriteProblem(w, r, ce.status, ce.code, ce.title, ce.detail)
		return
	}
	if errors.Is(err, errReceiptRace) { // three rounds and still racing: let the client retry
		w.Header().Set("Retry-After", "1")
		WriteProblem(w, r, http.StatusConflict, "request_in_progress", "Request in progress", "The same command is being processed; retry with the same Idempotency-Key.")
		return
	}
	failRead(w, r, err)
}
