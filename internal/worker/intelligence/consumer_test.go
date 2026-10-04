package intelligence

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

	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	"github.com/omnira/omnira/internal/testhelpers"
)

type fakeJobs struct {
	calls []ports.MessageRef
	err   error
	seen  map[uuid.UUID]bool
}

func (f *fakeJobs) EnsureFromEvent(_ context.Context, ref ports.MessageRef, _ string) (bool, error) {
	f.calls = append(f.calls, ref)
	if f.err != nil {
		return false, f.err
	}
	if f.seen == nil {
		f.seen = map[uuid.UUID]bool{}
	}
	created := !f.seen[ref.ID]
	f.seen[ref.ID] = true
	return created, nil
}
func (f *fakeJobs) Claim(context.Context, int, time.Duration) ([]ports.Job, error) { return nil, nil }
func (f *fakeJobs) Complete(context.Context, ports.Job) (bool, error)              { return false, nil }
func (f *fakeJobs) Retry(context.Context, ports.Job, string, time.Duration) (bool, error) {
	return false, nil
}
func (f *fakeJobs) Dead(context.Context, ports.Job, string) (bool, error) { return false, nil }

func event(id uuid.UUID, kind string) []byte {
	return []byte(`{"id":"e","tenant_id":"` + uuid.NewString() + `","aggregate_type":"message","aggregate_id":"` + id.String() + `","payload":{"kind":"` + kind + `"}}`)
}

func TestHandlerCreatesTheJobFromReferencesOnlyAndIsIdempotent(t *testing.T) {
	jobs := &fakeJobs{}
	h, _ := NewHandler(jobs)
	id := uuid.New()
	for i := 0; i < 3; i++ { // the same event delivered three times
		if err := h.Handle(context.Background(), event(id, "conversation")); err != nil {
			t.Fatal(err)
		}
	}
	if len(jobs.calls) != 3 || jobs.calls[0].Kind != ports.KindConversation || jobs.calls[0].ID != id {
		t.Fatalf("calls = %+v", jobs.calls)
	}
	if err := h.Handle(context.Background(), event(uuid.New(), "group")); err != nil || jobs.calls[3].Kind != ports.KindGroup {
		t.Fatalf("group event: %v %+v", err, jobs.calls)
	}
}

func TestHandlerClassifiesFailures(t *testing.T) {
	h, _ := NewHandler(&fakeJobs{})
	for name, raw := range map[string][]byte{
		"malformed json": []byte(`{`), "no id": []byte(`{"aggregate_id":""}`), "nil uuid": []byte(`{"aggregate_id":"` + uuid.Nil.String() + `"}`),
		"unknown kind": event(uuid.New(), "carrier-pigeon"),
	} {
		if err := h.Handle(context.Background(), raw); !errors.Is(err, ErrPermanent) || classify(err) != actTerm {
			t.Errorf("%s: %v, want a permanent error (terminate, never retry)", name, err)
		}
	}
	gone, _ := NewHandler(&fakeJobs{err: domain.ErrReferenceNotFound})
	if err := gone.Handle(context.Background(), event(uuid.New(), "conversation")); classify(err) != actTerm {
		t.Errorf("a message deleted before we saw it must be terminated: %v", err)
	}
	flaky, _ := NewHandler(&fakeJobs{err: errors.New("db down")})
	if err := flaky.Handle(context.Background(), event(uuid.New(), "conversation")); classify(err) != actNak {
		t.Errorf("a transient failure must be retried (nak): %v", err)
	}
	if classify(nil) != actAck {
		t.Error("success must ack")
	}
	if _, err := NewHandler(nil); err == nil {
		t.Error("a handler without a store must be refused")
	}
}

// A real JetStream (disposable) delivering the same event twice, a malformed one and a transient failure.
type flakyJobs struct {
	mu      sync.Mutex
	calls   int
	created map[uuid.UUID]bool
	failOn  uuid.UUID
	failed  bool
}

func (f *flakyJobs) EnsureFromEvent(_ context.Context, ref ports.MessageRef, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if ref.ID == f.failOn && !f.failed {
		f.failed = true
		return false, errors.New("transient database error")
	}
	if f.created == nil {
		f.created = map[uuid.UUID]bool{}
	}
	created := !f.created[ref.ID]
	f.created[ref.ID] = true
	return created, nil
}
func (f *flakyJobs) Claim(context.Context, int, time.Duration) ([]ports.Job, error) { return nil, nil }
func (f *flakyJobs) Complete(context.Context, ports.Job) (bool, error)              { return false, nil }
func (f *flakyJobs) Retry(context.Context, ports.Job, string, time.Duration) (bool, error) {
	return false, nil
}
func (f *flakyJobs) Dead(context.Context, ports.Job, string) (bool, error) { return false, nil }

func TestJetStreamDeliversTheSameEventTwiceAndTheJobIsCreatedOnce(t *testing.T) {
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
	subject := "test.intelligence." + run + "." + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{Name: stream, Subjects: []string{subject}, Storage: jetstream.FileStorage}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = js.DeleteStream(ctx, stream) }()

	dup, flaky := uuid.New(), uuid.New()
	store := &flakyJobs{failOn: flaky}
	h, _ := NewHandler(store)
	cons, err := startConsumer(ctx, js, h, stream, "test_"+run+"_"+uuid.NewString(), subject)
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
	publish(event(flaky, "group"))

	deadline := time.After(10 * time.Second)
	for {
		store.mu.Lock()
		done := store.failed && store.created[flaky] && store.created[dup]
		calls := store.calls
		store.mu.Unlock()
		if done && calls >= 4 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("pipeline did not settle: calls=%d created=%v", calls, store.created)
		case <-time.After(50 * time.Millisecond):
		}
	}
	time.Sleep(600 * time.Millisecond) // a redelivered poison or duplicate would show up now
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.created) != 2 {
		t.Fatalf("distinct jobs created = %d, want 2 (the duplicate event created nothing new)", len(store.created))
	}
	if store.calls != 4 && store.calls != 5 {
		t.Fatalf("calls = %d: duplicate x2, transient fail+retry x2, malformed never reaches the store", store.calls)
	}
}
