package routing

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/omnira/omnira/internal/testhelpers"
)

type notifyingAssigner struct{ called chan uuid.UUID }

func (a *notifyingAssigner) AssignRoundRobin(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	a.called <- id
	return uuid.New(), nil
}

func TestJetStreamConsumerDispatchesRoutingJob(t *testing.T) {
	natsCfg := testhelpers.RequireIntegrationNATS(t)
	nc, err := nats.Connect(natsCfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}

	conversationID := uuid.New()
	assigner := &notifyingAssigner{called: make(chan uuid.UUID, 1)}
	handler, err := NewHandler(&fakeRunner{}, assigner)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runShort := strings.ReplaceAll(natsCfg.RunID, ":", "")
	testStream := "OMNIRA_TEST_" + runShort + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	testSubject := "test.routing." + runShort + "." + strings.ReplaceAll(uuid.NewString(), "-", "")
	ensureTestStream(t, ctx, js, testStream, testSubject)
	consumer, err := startConsumer(ctx, js, handler, testStream, "test_"+runShort+"_"+uuid.NewString(), testSubject)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Stop()
	defer func() { _ = js.DeleteStream(ctx, testStream) }()

	raw := []byte(`{"tenant_id":"` + uuid.NewString() + `","aggregate_id":"` + conversationID.String() + `"}`)
	if _, err := js.Publish(ctx, testSubject, raw, jetstream.WithMsgID(uuid.NewString())); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-assigner.called:
		if got != conversationID {
			t.Fatalf("conversation=%s", got)
		}
	case <-ctx.Done():
		t.Fatal("routing job was not consumed")
	}
}

// ensureTestStream provisions a disposable, uniquely-named stream for a
// test's own use. PILOT.4D3-C1: startConsumer no longer creates OMNIRA_JOBS
// itself (that is jobsstream.Ensure's sole responsibility, called once at
// worker startup) — these tests use arbitrary per-run stream names, never
// the canonical OMNIRA_JOBS name/subjects jobsstream.Config() governs, so
// they provision their own equivalent-shape stream directly instead of
// calling jobsstream.Ensure.
func ensureTestStream(t *testing.T, ctx context.Context, js jetstream.JetStream, name, subject string) {
	t.Helper()
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     name,
		Subjects: []string{subject},
		Storage:  jetstream.FileStorage,
	}); err != nil {
		t.Fatalf("provision test stream %s: %v", name, err)
	}
}
