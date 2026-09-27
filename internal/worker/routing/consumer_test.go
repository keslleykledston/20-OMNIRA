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
	consumer, err := startConsumer(ctx, js, handler, testStream, "test_"+runShort+"_"+uuid.NewString(), testSubject, testSubject)
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
