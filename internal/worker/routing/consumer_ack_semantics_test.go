package routing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/omnira/omnira/internal/routing/application"
	"github.com/omnira/omnira/internal/testhelpers"
)

// --- PILOT.4D2 §9: pure classification tests (no NATS, instant) ----------

func TestAckAction_Success(t *testing.T) {
	if got := ackAction(nil); got != ackActionAck {
		t.Fatalf("got %v, want ack", got)
	}
}

// B: the core new behavior — no eligible agent is ACKed, never NAKed.
func TestAckAction_NoEligibleAgent(t *testing.T) {
	if got := ackAction(application.ErrNoEligibleAgent); got != ackActionAck {
		t.Fatalf("got %v, want ack", got)
	}
}

// C: a technical error (anything not a recognized sentinel) still NAKs.
func TestAckAction_TechnicalError(t *testing.T) {
	if got := ackAction(errors.New("boom: connection reset")); got != ackActionNak {
		t.Fatalf("got %v, want nak", got)
	}
}

// D: permanent job errors still Term.
func TestAckAction_Permanent(t *testing.T) {
	if got := ackAction(fmt.Errorf("%w: malformed envelope", ErrPermanent)); got != ackActionTerm {
		t.Fatalf("got %v, want term", got)
	}
}

// E: a FUTURE error wrapper must not silently turn the business outcome back
// into a technical retry — errors.Is must still reach the ACK branch through
// an arbitrary wrap depth.
func TestAckAction_WrappedNoEligibleAgent_StillAcks(t *testing.T) {
	wrapped := fmt.Errorf("routing worker: %w", fmt.Errorf("assign failed: %w", application.ErrNoEligibleAgent))
	if got := ackAction(wrapped); got != ackActionAck {
		t.Fatalf("got %v, want ack (wrapping must preserve errors.Is)", got)
	}
}

// --- fakes for the real-NATS tests below ----------------------------------

type fixedErrAssigner struct{ err error }

func (a *fixedErrAssigner) AssignRoundRobin(context.Context, uuid.UUID) (uuid.UUID, error) {
	return uuid.New(), a.err
}

type countingAssigner struct {
	err   error
	calls chan struct{}
}

func (a *countingAssigner) AssignRoundRobin(context.Context, uuid.UUID) (uuid.UUID, error) {
	a.calls <- struct{}{}
	return uuid.New(), a.err
}

func newDisposableConsumer(t *testing.T, js jetstream.JetStream, handler *Handler, runID string) (subject string, teardown func()) {
	t.Helper()
	ctx := context.Background()
	runShort := strings.ReplaceAll(runID, ":", "")
	testStream := "OMNIRA_TEST_" + runShort + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	testSubject := "test.routing." + runShort + "." + strings.ReplaceAll(uuid.NewString(), "-", "")
	ensureTestStream(t, ctx, js, testStream, testSubject)
	consumer, err := startConsumer(context.Background(), js, handler, testStream, "test_"+runShort+"_"+uuid.NewString(), testSubject)
	if err != nil {
		t.Fatal(err)
	}
	return testSubject, func() {
		consumer.Stop()
		_ = js.DeleteStream(ctx, testStream)
	}
}

func routingEnvelope(conversationID uuid.UUID) []byte {
	raw, _ := json.Marshal(map[string]string{"aggregate_id": conversationID.String()})
	return raw
}

// PILOT.4D2 §9 (end-to-end confirmation) + §13 (load-shaped regression): a
// disposable, uniquely-named stream/subject — never the real OMNIRA_JOBS
// pilot stream. Proves the no-agent outcome is delivered exactly once and
// never redelivered, for several jobs at once, deterministically.
func TestNoEligibleAgent_DeliveredOnceNeverRedelivered(t *testing.T) {
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

	const jobCount = 5
	calls := make(chan struct{}, jobCount*3) // generous: would overflow-detect extra redeliveries
	assigner := &countingAssigner{err: application.ErrNoEligibleAgent, calls: calls}
	handler, err := NewHandler(&fakeRunner{}, assigner)
	if err != nil {
		t.Fatal(err)
	}
	subject, teardown := newDisposableConsumer(t, js, handler, natsCfg.RunID)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < jobCount; i++ {
		if _, err := js.Publish(ctx, subject, routingEnvelope(uuid.New()), jetstream.WithMsgID(uuid.NewString())); err != nil {
			t.Fatal(err)
		}
	}

	seen := 0
	for seen < jobCount {
		select {
		case <-calls:
			seen++
		case <-ctx.Done():
			t.Fatalf("only %d/%d no-agent jobs were evaluated", seen, jobCount)
		}
	}

	// Wait comfortably past the OLD 5s NakWithDelay window that would have
	// applied before PILOT.4D2. Any redelivery arriving here would prove the
	// ACK didn't take effect.
	select {
	case <-calls:
		t.Fatal("a no-agent job was redelivered — ACK did not take effect")
	case <-time.After(7 * time.Second):
	}
}

// PILOT.4D2 §9C: a genuine technical error still NAKs and gets redelivered —
// unchanged behavior, proven on the same disposable harness.
func TestTechnicalError_IsRedelivered(t *testing.T) {
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

	calls := make(chan struct{}, 4)
	assigner := &countingAssigner{err: errors.New("boom: db unavailable"), calls: calls}
	handler, err := NewHandler(&fakeRunner{}, assigner)
	if err != nil {
		t.Fatal(err)
	}
	subject, teardown := newDisposableConsumer(t, js, handler, natsCfg.RunID)
	defer teardown()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if _, err := js.Publish(ctx, subject, routingEnvelope(uuid.New()), jetstream.WithMsgID(uuid.NewString())); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-calls:
		case <-ctx.Done():
			t.Fatalf("technical error was not redelivered (only %d attempts observed)", i)
		}
	}
}

// PILOT.4D2 §12: the central real-JetStream property — a no-agent outcome
// must not leave the consumer's ack floor stuck the way a NAK-forever/
// MaxDeliver-exhaustion cycle would. Uses the local, loopback-only NATS
// monitoring API (PILOT.4D) against this test's own disposable stream —
// never the shared OMNIRA_JOBS pilot stream.
func TestNoEligibleAgent_AckFloorAdvances(t *testing.T) {
	natsCfg := testhelpers.RequireIntegrationNATS(t)
	if natsCfg.MonitorURL == "" {
		t.Skip("OMNIRA_NATS_MONITOR_URL required")
	}
	monitorURL := natsCfg.MonitorURL
	nc, err := nats.Connect(natsCfg.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}

	calls := make(chan struct{}, 1)
	assigner := &countingAssigner{err: application.ErrNoEligibleAgent, calls: calls}
	handler, err := NewHandler(&fakeRunner{}, assigner)
	if err != nil {
		t.Fatal(err)
	}

	runShort := strings.ReplaceAll(natsCfg.RunID, ":", "")
	testStream := "OMNIRA_TEST_" + runShort + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	testDurable := "test_" + runShort + "_" + uuid.NewString()
	testSubject := "test.routing." + runShort + "." + strings.ReplaceAll(uuid.NewString(), "-", "")
	ensureTestStream(t, context.Background(), js, testStream, testSubject)
	consumer, err := startConsumer(context.Background(), js, handler, testStream, testDurable, testSubject)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		consumer.Stop()
		_ = js.DeleteStream(context.Background(), testStream)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := js.Publish(ctx, testSubject, routingEnvelope(uuid.New()), jetstream.WithMsgID(uuid.NewString())); err != nil {
		t.Fatal(err)
	}
	select {
	case <-calls:
	case <-ctx.Done():
		t.Fatal("no-agent job was not evaluated")
	}

	// Give the Ack a moment to reach the server and the monitoring endpoint
	// a moment to reflect it.
	time.Sleep(500 * time.Millisecond)

	info := fetchConsumerInfo(t, monitorURL, testStream, testDurable)
	if info.NumRedelivered != 0 {
		t.Fatalf("num_redelivered = %d, want 0", info.NumRedelivered)
	}
	if info.NumAckPending != 0 {
		t.Fatalf("num_ack_pending = %d, want 0", info.NumAckPending)
	}
	if info.AckFloorStreamSeq < 1 {
		t.Fatalf("ack_floor.stream_seq = %d, want >= 1 (advanced past the no-agent message)", info.AckFloorStreamSeq)
	}

	// A later, successfully-assigned message must continue advancing the
	// floor normally — the no-agent path does not leave the consumer in a
	// different state than any other successful Ack would.
	assigner.err = nil
	if _, err := js.Publish(ctx, testSubject, routingEnvelope(uuid.New()), jetstream.WithMsgID(uuid.NewString())); err != nil {
		t.Fatal(err)
	}
	select {
	case <-calls:
	case <-ctx.Done():
		t.Fatal("second (assigned) job was not evaluated")
	}
	time.Sleep(500 * time.Millisecond)
	info2 := fetchConsumerInfo(t, monitorURL, testStream, testDurable)
	if info2.AckFloorStreamSeq <= info.AckFloorStreamSeq {
		t.Fatalf("ack floor did not continue advancing: before=%d after=%d", info.AckFloorStreamSeq, info2.AckFloorStreamSeq)
	}
	if info2.NumRedelivered != 0 {
		t.Fatalf("num_redelivered = %d after second message, want 0", info2.NumRedelivered)
	}
}

type consumerInfo struct {
	NumRedelivered    int
	NumAckPending     int
	AckFloorStreamSeq int
}

// fetchConsumerInfo reads the read-only NATS monitoring HTTP API
// (127.0.0.1-only per PILOT.4D) for one specific stream/consumer — never
// mutates NATS state.
func fetchConsumerInfo(t *testing.T, monitorURL, stream, consumer string) consumerInfo {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Get(monitorURL + "/jsz?streams=1&consumers=1")
	if err != nil {
		t.Fatalf("monitoring request failed: %v", err)
	}
	defer res.Body.Close()
	var data struct {
		AccountDetails []struct {
			StreamDetail []struct {
				Name           string `json:"name"`
				ConsumerDetail []struct {
					Name           string `json:"name"`
					NumRedelivered int    `json:"num_redelivered"`
					NumAckPending  int    `json:"num_ack_pending"`
					AckFloor       struct {
						StreamSeq int `json:"stream_seq"`
					} `json:"ack_floor"`
				} `json:"consumer_detail"`
			} `json:"stream_detail"`
		} `json:"account_details"`
	}
	if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
		t.Fatalf("decode monitoring response: %v", err)
	}
	for _, acc := range data.AccountDetails {
		for _, s := range acc.StreamDetail {
			if s.Name != stream {
				continue
			}
			for _, c := range s.ConsumerDetail {
				if c.Name != consumer {
					continue
				}
				return consumerInfo{NumRedelivered: c.NumRedelivered, NumAckPending: c.NumAckPending, AckFloorStreamSeq: c.AckFloor.StreamSeq}
			}
		}
	}
	t.Fatalf("consumer %s/%s not found in monitoring output", stream, consumer)
	return consumerInfo{}
}
