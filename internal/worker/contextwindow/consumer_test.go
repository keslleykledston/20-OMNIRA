package contextwindow

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/omnira/omnira/internal/intelligence/contextwindow"
	"github.com/omnira/omnira/internal/intelligence/ports"
	"github.com/omnira/omnira/internal/testhelpers"
)

type fakeStore struct {
	resolved map[uuid.UUID]ports.ResolvedMessage // messages that exist and are inbound conversation messages
	touches  []ports.ResolvedMessage
	err      error // ResolveMessage transient failure
}

func (f *fakeStore) ResolveMessage(_ context.Context, id uuid.UUID) (ports.ResolvedMessage, bool, error) {
	if f.err != nil {
		return ports.ResolvedMessage{}, false, f.err
	}
	msg, ok := f.resolved[id]
	return msg, ok, nil
}
func (f *fakeStore) Touch(_ context.Context, _ contextwindow.Policy, tenantID, conversationID, _ uuid.UUID, _ time.Time) error {
	f.touches = append(f.touches, ports.ResolvedMessage{TenantID: tenantID, ConversationID: conversationID})
	return nil
}
func (f *fakeStore) Force(context.Context, uuid.UUID, uuid.UUID, contextwindow.Trigger) (bool, error) {
	return false, nil
}
func (f *fakeStore) Sweep(context.Context, time.Time, int) (int, error) { return 0, nil }
func (f *fakeStore) Claim(context.Context, int, time.Duration) ([]ports.ContextWindow, error) {
	return nil, nil
}
func (f *fakeStore) MessageIDs(context.Context, ports.ContextWindow) ([]uuid.UUID, error) {
	return nil, nil
}
func (f *fakeStore) Complete(context.Context, ports.ContextWindow) (bool, error) { return false, nil }
func (f *fakeStore) Retry(context.Context, ports.ContextWindow, string, time.Duration) (bool, error) {
	return false, nil
}
func (f *fakeStore) Dead(context.Context, ports.ContextWindow, string) (bool, error) {
	return false, nil
}

func event(id uuid.UUID, kind string) []byte {
	return []byte(`{"id":"e","tenant_id":"` + uuid.NewString() + `","aggregate_type":"message","aggregate_id":"` + id.String() + `","payload":{"kind":"` + kind + `"}}`)
}

func TestHandlerTouchesTheResolvedWindowAndIsIdempotentByConstruction(t *testing.T) {
	id := uuid.New()
	msg := ports.ResolvedMessage{TenantID: uuid.New(), ConversationID: uuid.New(), CreatedAt: time.Now().UTC()}
	store := &fakeStore{resolved: map[uuid.UUID]ports.ResolvedMessage{id: msg}}
	h, err := NewHandler(store, contextwindow.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // the same event delivered three times — Touch itself is idempotent (tested in the adapter)
		if err := h.Handle(context.Background(), event(id, "conversation")); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.touches) != 3 || store.touches[0].TenantID != msg.TenantID || store.touches[0].ConversationID != msg.ConversationID {
		t.Fatalf("touches = %+v", store.touches)
	}
}

func TestHandlerIgnoresGroupMessagesTheyAreOutOfScope(t *testing.T) {
	store := &fakeStore{}
	h, _ := NewHandler(store, contextwindow.DefaultPolicy())
	if err := h.Handle(context.Background(), event(uuid.New(), "group")); err != nil {
		t.Fatalf("group event must be a no-op, not an error: %v", err)
	}
	if len(store.touches) != 0 {
		t.Fatalf("a group message must never touch a window: %+v", store.touches)
	}
}

func TestHandlerClassifiesFailures(t *testing.T) {
	store := &fakeStore{}
	h, _ := NewHandler(store, contextwindow.DefaultPolicy())
	for name, raw := range map[string][]byte{
		"malformed json": []byte(`{`), "no id": []byte(`{"aggregate_id":""}`), "nil uuid": []byte(`{"aggregate_id":"` + uuid.Nil.String() + `"}`),
	} {
		if err := h.Handle(context.Background(), raw); !errors.Is(err, ErrPermanent) || classify(err) != actTerm {
			t.Errorf("%s: %v, want a permanent error (terminate, never retry)", name, err)
		}
	}
	// Not found (deleted before we saw it, or not an inbound conversation message) is a no-op, not an error.
	gone := &fakeStore{}
	goneH, _ := NewHandler(gone, contextwindow.DefaultPolicy())
	if err := goneH.Handle(context.Background(), event(uuid.New(), "conversation")); err != nil {
		t.Errorf("a message not resolvable must be a silent no-op, not an error: %v", err)
	}
	flaky := &fakeStore{err: errors.New("db down")}
	flakyH, _ := NewHandler(flaky, contextwindow.DefaultPolicy())
	if err := flakyH.Handle(context.Background(), event(uuid.New(), "conversation")); classify(err) != actNak {
		t.Errorf("a transient failure must be retried (nak): %v", err)
	}
	if classify(nil) != actAck {
		t.Error("success must ack")
	}
	if _, err := NewHandler(nil, contextwindow.DefaultPolicy()); err == nil {
		t.Error("a handler without a store must be refused")
	}
	if _, err := NewHandler(store, contextwindow.Policy{}); err == nil {
		t.Error("a handler with an invalid policy must be refused")
	}
}

// A real JetStream (disposable) delivering the same event twice, a malformed one and a transient failure.
type flakyStore struct {
	mu      sync.Mutex
	calls   int
	touched map[uuid.UUID]bool
	failOn  uuid.UUID
	failed  bool
	msg     ports.ResolvedMessage
}

func (f *flakyStore) ResolveMessage(_ context.Context, id uuid.UUID) (ports.ResolvedMessage, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if id == f.failOn && !f.failed {
		f.failed = true
		return ports.ResolvedMessage{}, false, errors.New("transient database error")
	}
	return f.msg, true, nil
}
func (f *flakyStore) Touch(_ context.Context, _ contextwindow.Policy, _, _, id uuid.UUID, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.touched == nil {
		f.touched = map[uuid.UUID]bool{}
	}
	f.touched[id] = true
	return nil
}
func (f *flakyStore) Force(context.Context, uuid.UUID, uuid.UUID, contextwindow.Trigger) (bool, error) {
	return false, nil
}
func (f *flakyStore) Sweep(context.Context, time.Time, int) (int, error) { return 0, nil }
func (f *flakyStore) Claim(context.Context, int, time.Duration) ([]ports.ContextWindow, error) {
	return nil, nil
}
func (f *flakyStore) MessageIDs(context.Context, ports.ContextWindow) ([]uuid.UUID, error) {
	return nil, nil
}
func (f *flakyStore) Complete(context.Context, ports.ContextWindow) (bool, error) { return false, nil }
func (f *flakyStore) Retry(context.Context, ports.ContextWindow, string, time.Duration) (bool, error) {
	return false, nil
}
func (f *flakyStore) Dead(context.Context, ports.ContextWindow, string) (bool, error) {
	return false, nil
}

func TestJetStreamDeliversTheSameEventTwiceAndTheWindowIsTouchedIdempotently(t *testing.T) {
	natsCfg := testhelpers.RequireIntegrationNATS(t)
	nc, err := nats.Connect(natsCfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, _ := jetstream.New(nc)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	run := strings.ReplaceAll(natsCfg.RunID, ":", "")
	stream := "OMNIRA_TEST_" + run + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	subject := "test.contextwindow." + run + "." + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: stream, Subjects: []string{subject}, Storage: jetstream.FileStorage}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = js.DeleteStream(ctx, stream) }()

	dup, flaky := uuid.New(), uuid.New()
	store := &flakyStore{failOn: flaky, msg: ports.ResolvedMessage{TenantID: uuid.New(), ConversationID: uuid.New(), CreatedAt: time.Now().UTC()}}
	h, _ := NewHandler(store, contextwindow.DefaultPolicy())
	cons, err := startConsumerOn(ctx, js, h, stream, "test_"+run+"_"+uuid.NewString(), subject)
	if err != nil {
		t.Fatal(err)
	}
	defer cons.Stop()

	publish := func(raw []byte) {
		if _, err := js.Publish(ctx, subject, raw, jetstream.WithMsgID(uuid.NewString())); err != nil { // distinct msg ids: at-least-once duplicates
			t.Fatal(err)
		}
	}
	publish(event(dup, "conversation"))
	publish(event(dup, "conversation"))
	publish([]byte(`not json at all`))
	publish(event(flaky, "conversation"))

	deadline := time.After(10 * time.Second)
	for {
		store.mu.Lock()
		done := store.failed && store.touched[flaky] && store.touched[dup]
		calls := store.calls
		store.mu.Unlock()
		if done && calls >= 4 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("pipeline did not settle: calls=%d touched=%v", calls, store.touched)
		case <-time.After(50 * time.Millisecond):
		}
	}
	time.Sleep(600 * time.Millisecond) // a redelivered poison or duplicate would show up now
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.touched) != 2 {
		t.Fatalf("distinct messages touched = %d, want 2 (dup and flaky — the duplicate delivery of dup is idempotent by construction)", len(store.touched))
	}
}
