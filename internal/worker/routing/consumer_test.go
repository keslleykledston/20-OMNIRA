package routing

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type notifyingAssigner struct{ called chan uuid.UUID }

func (a *notifyingAssigner) AssignRoundRobin(_ context.Context, id uuid.UUID) (uuid.UUID, error) {
	a.called <- id
	return uuid.New(), nil
}

func TestJetStreamConsumerDispatchesRoutingJob(t *testing.T) {
	natsURL := os.Getenv("OMNIRA_NATS_URL")
	if natsURL == "" {
		t.Skip("OMNIRA_NATS_URL required")
	}
	nc, err := nats.Connect(natsURL)
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
	testStream := "OMNIRA_ROUTING_TEST_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	testSubject := "contract.routing.test." + strings.ReplaceAll(uuid.NewString(), "-", "")
	consumer, err := startConsumer(ctx, js, handler, testStream, "routing-test-"+uuid.NewString(), testSubject, testSubject)
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
