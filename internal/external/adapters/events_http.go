package adapters

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// EventsHandler serves the durable journal (ADR-0046 H4): the checkpoint a client bootstraps from and the facts after a sequence number. The journal is
// written by triggers in the transaction of each change and numbered by the sequencer in an order that never lets a fact appear under a number already
// read; this side only reads, and re-asks the visibility of every fact at the moment of reading (an installation limited to a queue never hears about
// another queue, and a conversation that turned internal or spam stops being reported).
type EventsHandler struct {
	pool          *pgxpool.Pool
	retentionDays int
}

func NewEventsHandler(pool *pgxpool.Pool, retentionDays int) *EventsHandler {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	return &EventsHandler{pool: pool, retentionDays: retentionDays}
}

type CheckpointDTO struct {
	SeqHead       int64     `json:"seq_head"`
	MinSeq        int64     `json:"min_seq"`
	RetentionDays int       `json:"retention_days"`
	At            time.Time `json:"at"`
}

type EventOrigin struct {
	Kind string `json:"kind"` // channel | workspace | hub | crm | automation | system
	Mine bool   `json:"mine,omitempty"`
}

type EventAggregate struct {
	Type string    `json:"type"`
	ID   uuid.UUID `json:"id"`
}

// EventDTO is the envelope. References and counters only; never a message body, a phone number or an attachment. tenant_id is informative.
type EventDTO struct {
	EventID          uuid.UUID      `json:"event_id"`
	Seq              int64          `json:"seq"`
	Type             string         `json:"type"`
	Version          int            `json:"version"`
	OccurredAt       time.Time      `json:"occurred_at"`
	TenantID         uuid.UUID      `json:"tenant_id"`
	Aggregate        EventAggregate `json:"aggregate"`
	ConversationID   *uuid.UUID     `json:"conversation_id,omitempty"`
	AggregateVersion *int64         `json:"aggregate_version,omitempty"`
	Origin           EventOrigin    `json:"origin"`
	Data             map[string]any `json:"data"`
}

type EventsPageDTO struct {
	Items      []EventDTO `json:"items"`
	NextCursor string     `json:"next_cursor"`
	HasMore    bool       `json:"has_more"`
	Head       int64      `json:"head"`
}

func resync(w http.ResponseWriter, r *http.Request, detail string) {
	externalMetric.Inc("not_found")
	WriteProblem(w, r, http.StatusGone, "resync_required", "Resynchronization required", detail)
}

func (h *EventsHandler) bounds(ctx context.Context, q platformdb.Querier, tenant uuid.UUID) (head, min int64, err error) {
	err = q.QueryRow(ctx, `SELECT last_seq, min_seq FROM domain_event_cursors WHERE tenant_id = $1`, tenant).Scan(&head, &min)
	if err == pgx.ErrNoRows {
		return 0, 1, nil
	}
	return head, min, err
}

// Checkpoint: GET /external/sync/checkpoint. The head sequence number to read FIRST, then the snapshot (conversations, messages), then the events after
// the head: a change committed before the checkpoint but numbered after it is both in the snapshot and replayed (harmless: events are references), a
// change after the snapshot is replayed, and none can fall between.
func (h *EventsHandler) Checkpoint(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	var head, min int64
	err := platformdb.WithSystemTenantSession(r.Context(), h.pool, p.TenantID, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, h.pool)
		if _, err := LoadAllowlists(ctx, q, p); err != nil { // the installation must still be usable
			return err
		}
		var err error
		head, min, err = h.bounds(ctx, q, p.TenantID)
		return err
	})
	if err != nil {
		failRead(w, r, err)
		return
	}
	externalMetric.Inc("served")
	WriteJSON(w, r, http.StatusOK, CheckpointDTO{SeqHead: head, MinSeq: min, RetentionDays: h.retentionDays, At: time.Now().UTC()})
}

// ListEvents: GET /external/events?after=<seq>&limit=. 410 resync_required when `after` is older than what is kept or newer than the head (a number
// that never belonged to this company's journal): the same answer, so a forged number tells nothing about any company.
func (h *EventsHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	p, _ := PrincipalFrom(r.Context())
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64) // an empty or malformed value is an error too
	if err != nil || after < 0 {
		problemBad(w, r, "after is required: the seq of the checkpoint (GET /external/sync/checkpoint) or the next_cursor of the previous page.")
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageSize {
			problemBad(w, r, "limit must be 1 to 200.")
			return
		}
		limit = n
	}
	var out EventsPageDTO
	var gone string
	err = scopedRun(h.pool, r, p, func(ctx context.Context, db platformdb.Querier, v Visibility) error {
		head, min, err := h.bounds(ctx, db, p.TenantID)
		if err != nil {
			return err
		}
		out.Head = head
		if after > head {
			gone = "The cursor is not one this journal issued."
			return nil
		}
		if after+1 < min {
			gone = "The cursor is older than the events kept; read the checkpoint and rebuild from the snapshot."
			return nil
		}
		var args Args
		t := args.Add(p.TenantID)
		visConv := v.Conversation("c", &args)
		visConv2 := v.Conversation("c", &args)
		rows, err := db.Query(ctx, `
			WITH w AS (
			  SELECT e.* FROM domain_events e
			   WHERE e.tenant_id = `+t+` AND e.seq IS NOT NULL AND e.seq > `+args.Add(after)+`
			   ORDER BY e.seq LIMIT `+args.Add(limit)+`)
			SELECT w.seq, w.id, w.event_type, w.event_version, w.aggregate_type, w.aggregate_id, w.conversation_id, w.aggregate_version,
			       w.origin_kind, w.origin_installation_id, w.occurred_at, w.payload,
			       CASE
			         WHEN w.conversation_id IS NOT NULL THEN EXISTS (SELECT 1 FROM conversations c WHERE c.id = w.conversation_id AND `+visConv+`)
			         WHEN w.aggregate_type = 'contact' THEN EXISTS (
			              SELECT 1 FROM contacts ct JOIN conversations c ON c.contact_id = ct.id AND c.tenant_id = ct.tenant_id
			               WHERE ct.tenant_id = `+t+` AND ct.id = w.aggregate_id AND ct.kind NOT IN ('internal', 'spam') AND `+visConv2+`)
			         ELSE false END AS visible
			  FROM w ORDER BY w.seq`, args.Values()...)
		if err != nil {
			return err
		}
		defer rows.Close()
		var last int64 = after
		for rows.Next() {
			var e EventDTO
			var inst *uuid.UUID
			var payload map[string]any
			var visible bool
			if err := rows.Scan(&e.Seq, &e.EventID, &e.Type, &e.Version, &e.Aggregate.Type, &e.Aggregate.ID, &e.ConversationID, &e.AggregateVersion,
				&e.Origin.Kind, &inst, &e.OccurredAt, &payload, &visible); err != nil {
				return err
			}
			last = e.Seq
			if !visible {
				continue
			}
			e.TenantID = p.TenantID
			e.Origin.Mine = inst != nil && *inst == p.InstallationID
			e.Data = payload
			out.Items = append(out.Items, e)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out.NextCursor = strconv.FormatInt(last, 10)
		out.HasMore = last < head
		return nil
	})
	if err != nil {
		failRead(w, r, err)
		return
	}
	if gone != "" {
		resync(w, r, gone)
		return
	}
	if out.Items == nil {
		out.Items = []EventDTO{}
	}
	externalMetric.Inc("served")
	WriteJSON(w, r, http.StatusOK, out)
}
