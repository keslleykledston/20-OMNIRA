package jobsstream

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/omnira/omnira/internal/testhelpers"
)

// testStreamName/testSubject build a disposable, per-test-run stream/subject
// pair — never the real "OMNIRA_JOBS"/"job.>" jobsstream.Config() governs, so
// these tests never risk colliding with a real OMNIRA_JOBS instance even if
// pointed at a shared NATS server by mistake (the integration guard already
// refuses that, but this is an independent second layer).
func testStreamName(t *testing.T, natsCfg testhelpers.IntegrationNATSConfig) string {
	t.Helper()
	runShort := strings.ReplaceAll(natsCfg.RunID, ":", "")
	return "JOBSSTREAM_TEST_" + runShort + "_" + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func connectJS(t *testing.T, natsCfg testhelpers.IntegrationNATSConfig) (*nats.Conn, jetstream.JetStream) {
	t.Helper()
	nc, err := nats.Connect(natsCfg.URL)
	if err != nil {
		t.Fatalf("connect NATS: %v", err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream.New: %v", err)
	}
	return nc, js
}

// legacyStreamConfig replicates exactly what internal/worker/routing/consumer.go
// and internal/worker/delivery/consumer.go used to construct before
// PILOT.4D3-C1 — Name/Subjects/Storage only, everything else a Go
// zero-value — so these tests exercise a REAL pre-C1 stream, not a synthetic
// stand-in.
func legacyStreamConfig(name, subject string) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:     name,
		Subjects: []string{subject},
		Storage:  jetstream.FileStorage,
	}
}

// TestEnsure_ExistingStreamUpdate_AppliesCanonicalPolicyAndPreservesIdentity
// proves the real, non-mocked round trip: a stream created with the OLD
// unbounded config (today's live OMNIRA_JOBS shape) is handed to
// ensureWithConfig with a canonical-shaped test policy, and the update:
//   - succeeds and verifies cleanly (the server actually accepted every
//     governed field);
//   - does not recreate the stream (messages/first_seq unchanged);
//   - does not disturb its existing durable consumer (Created unchanged).
func TestEnsure_ExistingStreamUpdate_AppliesCanonicalPolicyAndPreservesIdentity(t *testing.T) {
	natsCfg := testhelpers.RequireIntegrationNATS(t)
	_, js := connectJS(t, natsCfg)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	name := testStreamName(t, natsCfg)
	subject := "test.jobsstream." + name
	if _, err := js.CreateStream(ctx, legacyStreamConfig(name, subject)); err != nil {
		t.Fatalf("seed legacy stream: %v", err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), name) })

	consumer, err := js.CreateOrUpdateConsumer(ctx, name, jetstream.ConsumerConfig{
		Durable:       "worker_like_consumer",
		AckPolicy:     jetstream.AckExplicitPolicy,
		FilterSubject: subject,
	})
	if err != nil {
		t.Fatalf("seed consumer: %v", err)
	}
	consumerInfoBefore, err := consumer.Info(ctx)
	if err != nil {
		t.Fatalf("consumer.Info before update: %v", err)
	}

	for i := 0; i < 3; i++ {
		if _, err := js.Publish(ctx, subject, []byte(fmt.Sprintf("pre-update-%d", i))); err != nil {
			t.Fatalf("seed publish: %v", err)
		}
	}
	before, err := js.Stream(ctx, name)
	if err != nil {
		t.Fatalf("Stream before update: %v", err)
	}
	infoBefore, err := before.Info(ctx)
	if err != nil {
		t.Fatalf("Info before update: %v", err)
	}

	testCfg := Config()
	testCfg.Name = name
	testCfg.Subjects = []string{subject}
	testCfg.MaxAge = 1 * time.Hour // long enough that nothing expires mid-test
	testCfg.Duplicates = 1 * time.Second

	stream, err := ensureWithConfig(ctx, js, testCfg)
	if err != nil {
		t.Fatalf("ensureWithConfig: %v", err)
	}
	infoAfter, err := stream.Info(ctx)
	if err != nil {
		t.Fatalf("Info after update: %v", err)
	}

	if infoAfter.State.Msgs != infoBefore.State.Msgs {
		t.Fatalf("message count changed: before=%d after=%d — update must not recreate the stream", infoBefore.State.Msgs, infoAfter.State.Msgs)
	}
	if infoAfter.State.FirstSeq != infoBefore.State.FirstSeq {
		t.Fatalf("first_seq changed: before=%d after=%d — update must not recreate the stream", infoBefore.State.FirstSeq, infoAfter.State.FirstSeq)
	}
	if err := verifyConfig(testCfg, infoAfter.Config); err != nil {
		t.Fatalf("canonical policy not actually applied: %v", err)
	}

	consumerInfoAfter, err := consumer.Info(ctx)
	if err != nil {
		t.Fatalf("consumer.Info after update: %v", err)
	}
	if !consumerInfoAfter.Created.Equal(consumerInfoBefore.Created) {
		t.Fatalf("consumer Created changed: before=%v after=%v — the existing durable consumer must survive the stream policy update", consumerInfoBefore.Created, consumerInfoAfter.Created)
	}
}

// TestEnsure_ConsumerPreservation_BothConsumerStyles seeds TWO durable
// consumers on a legacy-configured stream — one named like routing's
// "worker-routing", one like delivery's "worker-channel-send" — and proves
// ensureWithConfig's stream-policy update leaves BOTH untouched (identical
// Created timestamps, both still able to receive/ack a message afterward).
func TestEnsure_ConsumerPreservation_BothConsumerStyles(t *testing.T) {
	natsCfg := testhelpers.RequireIntegrationNATS(t)
	_, js := connectJS(t, natsCfg)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	name := testStreamName(t, natsCfg)
	subject := "test.jobsstream." + name
	if _, err := js.CreateStream(ctx, legacyStreamConfig(name, subject)); err != nil {
		t.Fatalf("seed legacy stream: %v", err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), name) })

	routingLike, err := js.CreateOrUpdateConsumer(ctx, name, jetstream.ConsumerConfig{
		Durable: "worker-routing", AckPolicy: jetstream.AckExplicitPolicy, FilterSubject: subject,
	})
	if err != nil {
		t.Fatalf("seed worker-routing-style consumer: %v", err)
	}
	deliveryLike, err := js.CreateOrUpdateConsumer(ctx, name, jetstream.ConsumerConfig{
		Durable: "worker-channel-send", AckPolicy: jetstream.AckExplicitPolicy, FilterSubject: subject,
	})
	if err != nil {
		t.Fatalf("seed worker-channel-send-style consumer: %v", err)
	}
	routingBefore, _ := routingLike.Info(ctx)
	deliveryBefore, _ := deliveryLike.Info(ctx)

	testCfg := Config()
	testCfg.Name = name
	testCfg.Subjects = []string{subject}
	testCfg.MaxAge = 1 * time.Hour
	testCfg.Duplicates = 1 * time.Second
	if _, err := ensureWithConfig(ctx, js, testCfg); err != nil {
		t.Fatalf("ensureWithConfig: %v", err)
	}

	routingAfter, err := routingLike.Info(ctx)
	if err != nil {
		t.Fatalf("worker-routing-style Info after update: %v", err)
	}
	deliveryAfter, err := deliveryLike.Info(ctx)
	if err != nil {
		t.Fatalf("worker-channel-send-style Info after update: %v", err)
	}
	if !routingAfter.Created.Equal(routingBefore.Created) {
		t.Fatalf("worker-routing-style consumer Created changed: before=%v after=%v", routingBefore.Created, routingAfter.Created)
	}
	if !deliveryAfter.Created.Equal(deliveryBefore.Created) {
		t.Fatalf("worker-channel-send-style consumer Created changed: before=%v after=%v", deliveryBefore.Created, deliveryAfter.Created)
	}

	if _, err := js.Publish(ctx, subject, []byte("still-usable")); err != nil {
		t.Fatalf("publish after update: %v", err)
	}
	for _, c := range []jetstream.Consumer{routingLike, deliveryLike} {
		msgs, err := c.Fetch(1, jetstream.FetchMaxWait(3*time.Second))
		if err != nil {
			t.Fatalf("Fetch after update: %v", err)
		}
		got := 0
		for m := range msgs.Messages() {
			got++
			_ = m.Ack()
		}
		if got != 1 {
			t.Fatalf("consumer delivered %d message(s) after update, want 1 — still usable", got)
		}
	}
}

// TestEnsure_AgeExpiry_ShortTestPolicy proves MaxAge is really enforced by
// the canonical Ensure/Config path — using a seconds-scale test policy
// (Duplicates kept <= MaxAge, a real NATS constraint discovered this
// session), never the production 7-day value, so the proof completes in a
// normal test run instead of requiring a 7-day wait.
func TestEnsure_AgeExpiry_ShortTestPolicy(t *testing.T) {
	natsCfg := testhelpers.RequireIntegrationNATS(t)
	_, js := connectJS(t, natsCfg)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	name := testStreamName(t, natsCfg)
	subject := "test.jobsstream." + name
	testCfg := Config()
	testCfg.Name = name
	testCfg.Subjects = []string{subject}
	testCfg.MaxAge = 2 * time.Second
	testCfg.Duplicates = 1 * time.Second
	testCfg.MaxBytes = -1

	if _, err := ensureWithConfig(ctx, js, testCfg); err != nil {
		t.Fatalf("ensureWithConfig: %v", err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), name) })

	if _, err := js.Publish(ctx, subject, []byte("old")); err != nil {
		t.Fatalf("publish old message: %v", err)
	}
	stream, err := js.Stream(ctx, name)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	infoBefore, err := stream.Info(ctx)
	if err != nil {
		t.Fatalf("Info before expiry: %v", err)
	}
	if infoBefore.State.Msgs != 1 {
		t.Fatalf("messages=%d before expiry, want 1", infoBefore.State.Msgs)
	}

	time.Sleep(3 * time.Second) // past MaxAge(2s)

	infoAfter, err := stream.Info(ctx)
	if err != nil {
		t.Fatalf("Info after expiry: %v", err)
	}
	if infoAfter.State.Msgs != 0 {
		t.Fatalf("messages=%d after MaxAge window, want 0 (expired)", infoAfter.State.Msgs)
	}

	if _, err := js.Publish(ctx, subject, []byte("young")); err != nil {
		t.Fatalf("publish young message: %v", err)
	}
	infoYoung, err := stream.Info(ctx)
	if err != nil {
		t.Fatalf("Info after young publish: %v", err)
	}
	if infoYoung.State.Msgs != 1 {
		t.Fatalf("messages=%d immediately after a fresh publish, want 1 (not yet expired)", infoYoung.State.Msgs)
	}
}

// TestEnsure_MaxBytesDiscardNew_RejectsThenRecoversAfterExpiry proves the
// canonical Ensure/Config implementation — not a standalone scratch program
// — reproduces the same fail-closed capacity behavior proven earlier this
// session: DiscardNew rejects a publish once MaxBytes is exceeded (visibly,
// without evicting older messages), and MaxAge freeing bytes lets the
// identical publish succeed afterward.
func TestEnsure_MaxBytesDiscardNew_RejectsThenRecoversAfterExpiry(t *testing.T) {
	natsCfg := testhelpers.RequireIntegrationNATS(t)
	_, js := connectJS(t, natsCfg)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// A short subject deliberately: JetStream's real per-message storage
	// footprint (subject + payload + framing/index overhead) is larger than
	// State.Bytes' own accounting once a small MaxBytes is actually
	// enforced — proven this session: an exact State.Bytes-sized cap still
	// rejected a same-size republish after expiry. A short, fixed-size
	// subject plus a comfortable fixed MaxBytes (matching the margin already
	// proven to work earlier this session: 3 tiny messages fit in ~138
	// bytes, 40 bytes was too tight, 100 was not) sidesteps needing to
	// reverse-engineer that overhead exactly.
	name := testStreamName(t, natsCfg)
	subject := "t." + strings.ReplaceAll(uuid.NewString(), "-", "")[:8]
	testCfg := Config()
	testCfg.Name = name
	testCfg.Subjects = []string{subject}
	testCfg.MaxAge = 3 * time.Second
	testCfg.Duplicates = 1 * time.Second
	testCfg.MaxBytes = 100 // fits exactly one "fill" message, not two.

	if _, err := ensureWithConfig(ctx, js, testCfg); err != nil {
		t.Fatalf("ensureWithConfig: %v", err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), name) })

	if _, err := js.Publish(ctx, subject, []byte("fill")); err != nil {
		t.Fatalf("publish to fill capacity: %v", err)
	}
	stream, err := js.Stream(ctx, name)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if _, err := js.Publish(ctx, subject, []byte("rejected-attempt")); err == nil {
		t.Fatal("publish beyond MaxBytes with Discard=DiscardNew succeeded, want a visible rejection")
	}

	infoAfterReject, err := stream.Info(ctx)
	if err != nil {
		t.Fatalf("Info after rejected publish: %v", err)
	}
	if infoAfterReject.State.Msgs != 1 {
		t.Fatalf("messages=%d after a rejected publish, want 1 (the original message must not have been evicted to make room)", infoAfterReject.State.Msgs)
	}

	time.Sleep(4 * time.Second) // past MaxAge(3s): frees the capacity DiscardNew was refusing

	// The identical publish that was rejected above must now succeed —
	// proving the freed capacity is real spare room, not a fluke.
	if _, err := js.Publish(ctx, subject, []byte("rejected-attempt")); err != nil {
		t.Fatalf("publish after MaxAge freed capacity: %v, want success", err)
	}
}
