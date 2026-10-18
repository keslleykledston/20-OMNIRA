package crmsim

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Receiver is the webhook endpoint of the reference CRM. It does what every integration must: verify the signature over the RAW body, refuse a timestamp outside the
// window, accept two signatures during a rotation, acknowledge only what it has recorded, and treat a repeated event_id as already done (at-least-once delivery).
type Receiver struct {
	mu       sync.Mutex
	secrets  [][]byte
	maxSkew  time.Duration
	now      func() time.Time
	seen     map[string]bool
	received []Event
	// Offline makes the endpoint answer 503, as a CRM that is down does.
	offline bool
	// Calls counts every request that reached the handler (even repeated ones), Rejected those refused for a bad signature or timestamp.
	Calls, Rejected, Duplicates int
	// DeliveryIDs keeps the delivery id of every accepted call, to show that retries keep the same id.
	DeliveryIDs map[string]string
}

func NewReceiver(secrets ...[]byte) *Receiver {
	return &Receiver{secrets: secrets, maxSkew: 5 * time.Minute, now: time.Now, seen: map[string]bool{}, DeliveryIDs: map[string]string{}}
}

// SetClock is for tests.
func (r *Receiver) SetClock(now func() time.Time) { r.now = now }

// SetSecrets replaces the secrets that are accepted (a rotation: the new one, and the old one for a while).
func (r *Receiver) SetSecrets(secrets ...[]byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.secrets = secrets
}

func (r *Receiver) SetOffline(off bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.offline = off
}

// Events returns what was accepted, in arrival order.
func (r *Receiver) Events() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.received...)
}

func sign(secret []byte, ts string, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(ts))
	m.Write([]byte{'.'})
	m.Write(body)
	return "v1=" + hex.EncodeToString(m.Sum(nil))
}

func (r *Receiver) verify(header, ts string, body []byte) bool {
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	d := r.now().Sub(time.Unix(sec, 0))
	if d > r.maxSkew || d < -r.maxSkew {
		return false
	}
	for _, secret := range r.secrets {
		want := sign(secret, ts, body)
		for _, part := range strings.Split(header, ",") {
			if hmac.Equal([]byte(strings.TrimSpace(part)), []byte(want)) {
				return true
			}
		}
	}
	return false
}

func (r *Receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls++
	if r.offline {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if err != nil || req.Method != http.MethodPost {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if !r.verify(req.Header.Get("X-Omnira-Signature"), req.Header.Get("X-Omnira-Timestamp"), body) {
		r.Rejected++
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil || ev.EventID == "" || ev.EventID != req.Header.Get("X-Omnira-Event-ID") {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if r.seen[ev.EventID] { // delivered twice (a retry after a lost answer): already done, acknowledge again
		r.Duplicates++
		w.WriteHeader(http.StatusNoContent)
		return
	}
	r.seen[ev.EventID] = true
	r.DeliveryIDs[ev.EventID] = req.Header.Get("X-Omnira-Delivery-ID")
	r.received = append(r.received, ev)
	w.WriteHeader(http.StatusNoContent)
}

// ---- state kept by the CRM ----

// Syncer keeps the CRM's own copy of "who holds what", rebuilt the way the API documents: checkpoint, snapshot, then the events after the checkpoint. It treats the
// journal as the source of truth and webhooks as a doorbell: a webhook only makes it read the journal sooner.
type Syncer struct {
	C       *Client
	Cursor  int64
	Control map[string]Control // conversation id -> last control state known
	Status  map[string]string  // conversation id -> open | closed
	Seen    map[string]bool    // event ids already applied
	Applied int
	// Resyncs counts how many times a 410 forced a rebuild from the snapshot.
	Resyncs int
	// OnFact, when set, is called once for every fact applied for the first time (a repeated event_id is not called again).
	OnFact func(Event)
}

func NewSyncer(c *Client) *Syncer {
	return &Syncer{C: c, Control: map[string]Control{}, Status: map[string]string{}, Seen: map[string]bool{}}
}

// Bootstrap: checkpoint FIRST, then the snapshot, then the events after the checkpoint. Nothing that changes while the snapshot is paged can be lost.
func (s *Syncer) Bootstrap(ctx context.Context) error {
	cp, err := s.C.Checkpoint(ctx)
	if err != nil {
		return err
	}
	convs, err := s.C.Conversations(ctx, "all")
	if err != nil {
		return err
	}
	s.Control, s.Status = map[string]Control{}, map[string]string{}
	for _, c := range convs {
		s.Control[c.ID], s.Status[c.ID] = c.Control, c.Status
	}
	s.Cursor = cp.SeqHead
	return s.CatchUp(ctx)
}

// CatchUp reads the journal from the cursor to its end and applies every fact once. A 410 means the cursor is older than what is kept: rebuild.
func (s *Syncer) CatchUp(ctx context.Context) error {
	for {
		events, next, more, err := s.C.Events(ctx, s.Cursor)
		if IsGone(err) {
			s.Resyncs++
			return s.Bootstrap(ctx)
		}
		if err != nil {
			return err
		}
		for _, ev := range events {
			s.Apply(ev)
		}
		s.Cursor = next
		if !more {
			return nil
		}
	}
}

// Apply records one fact. It is idempotent (by event_id) and keeps the highest control version per conversation: an older fact never moves a conversation back.
func (s *Syncer) Apply(ev Event) {
	if s.Seen[ev.EventID] {
		return
	}
	s.Seen[ev.EventID] = true
	s.Applied++
	if s.OnFact != nil {
		s.OnFact(ev)
	}
	if ev.ConversationID == "" {
		return
	}
	switch ev.Type {
	case "conversation.created.v1":
		if _, known := s.Status[ev.ConversationID]; !known {
			s.Status[ev.ConversationID] = "open"
			s.Control[ev.ConversationID] = Control{State: "unassigned"}
		}
	case "conversation.control_changed.v1":
		if ev.AggregateVer != nil && *ev.AggregateVer >= s.Control[ev.ConversationID].Version {
			cur := s.Control[ev.ConversationID]
			cur.Version = *ev.AggregateVer
			if c, ok := ev.Data["control"].(map[string]any); ok {
				if st, ok := c["state"].(string); ok {
					cur.State = st
				}
				if k, ok := c["kind"].(string); ok {
					cur.Holder = &Holder{Kind: k, Mine: ev.Origin.Mine && k == "crm_application"}
				} else {
					cur.Holder = nil
				}
			}
			s.Control[ev.ConversationID] = cur
		}
	case "conversation.closed.v1":
		s.Status[ev.ConversationID] = "closed"
	}
}

// Reread asks the API for the truth about one conversation: what a CRM does after a 409/412.
func (s *Syncer) Reread(ctx context.Context, id string) (Conversation, error) {
	c, err := s.C.Conversation(ctx, id)
	if err == nil {
		s.Control[id], s.Status[id] = c.Control, c.Status
	}
	return c, err
}
