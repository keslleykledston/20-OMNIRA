package realtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

type capturePublisher struct {
	subjects []string
	bodies   [][]byte
}

func (c *capturePublisher) Publish(subject string, data []byte) error {
	c.subjects = append(c.subjects, subject)
	c.bodies = append(c.bodies, data)
	return nil
}

// forward is pure (payload in, publish out): no Postgres or NATS needed.
func TestForwardAddsAnEventIDAndAPayloadVersionAndKeepsTheLegacyFields(t *testing.T) {
	tenant, conv := uuid.New(), uuid.New()
	pub := &capturePublisher{}
	b := NewBridge(nil, pub)
	payload := `{"tenant_id":"` + tenant.String() + `","conversation_id":"` + conv.String() + `","type":"message_received","data":{"message_id":"m1","direction":"inbound"}}`
	b.forward(context.Background(), payload)
	b.forward(context.Background(), payload)
	if len(pub.bodies) != 2 || pub.subjects[0] != Subject(tenant, conv) {
		t.Fatalf("published %d on %v", len(pub.bodies), pub.subjects)
	}
	var first, second map[string]any
	_ = json.Unmarshal(pub.bodies[0], &first)
	_ = json.Unmarshal(pub.bodies[1], &second)
	id1, _ := first["event_id"].(string)
	id2, _ := second["event_id"].(string)
	if _, err := uuid.Parse(id1); err != nil || id1 == id2 {
		t.Fatalf("event ids must be uuids and unique per event: %q %q", id1, id2)
	}
	if first["v"].(float64) != 1 || first["type"] != "message_received" || first["id"] != conv.String() || first["timestamp"] == nil {
		t.Fatalf("event = %v", first)
	}
	if d := first["data"].(map[string]any); d["message_id"] != "m1" || d["direction"] != "inbound" {
		t.Fatalf("data = %v", d)
	}
	// the tenant id never travels in the body (it is only in the subject, which the server scopes per authorized tenant)
	if _, leaked := first["tenant_id"]; leaked {
		t.Fatal("tenant_id must not be part of the event body")
	}
}

func TestForwardDropsMalformedNotifications(t *testing.T) {
	pub := &capturePublisher{}
	b := NewBridge(nil, pub)
	for _, bad := range []string{`not json`, `{}`, `{"tenant_id":"` + uuid.NewString() + `","type":"x"}`} {
		b.forward(context.Background(), bad)
	}
	if len(pub.bodies) != 0 {
		t.Fatalf("malformed notifications must not be published: %d", len(pub.bodies))
	}
}

// The pair (tenant, conversation) of a notification is verified, not trusted: a conversation that is not the tenant's never reaches that tenant's subject.
func TestForwardDropsAConversationThatIsNotTheTenants(t *testing.T) {
	tenantA, tenantB, convOfB := uuid.New(), uuid.New(), uuid.New()
	pub := &capturePublisher{}
	b := NewBridge(nil, pub)
	asked := 0
	b.verify = func(_ context.Context, tenant, conv uuid.UUID) (bool, error) {
		asked++
		return tenant == tenantB && conv == convOfB, nil
	}
	forged := `{"tenant_id":"` + tenantA.String() + `","conversation_id":"` + convOfB.String() + `","type":"message_status","data":{}}`
	genuine := `{"tenant_id":"` + tenantB.String() + `","conversation_id":"` + convOfB.String() + `","type":"message_status","data":{}}`
	b.forward(context.Background(), forged)
	if len(pub.subjects) != 0 {
		t.Fatalf("a forged pair was published on %v", pub.subjects)
	}
	b.forward(context.Background(), genuine)
	b.forward(context.Background(), genuine)
	if len(pub.subjects) != 2 || pub.subjects[0] != Subject(tenantB, convOfB) {
		t.Fatalf("the genuine pair was not published: %v", pub.subjects)
	}
	if asked != 2 { // the forged one and the first genuine one; the second genuine one is remembered (and the repeated forged one below too)
		t.Errorf("the database was asked %d times, want 2 (a verified pair is remembered)", asked)
	}
	b.forward(context.Background(), forged)
	if len(pub.subjects) != 2 {
		t.Error("a negative answer must not be remembered as a positive one")
	}
	if asked != 2 {
		t.Errorf("a refusal is remembered for a while: the database was asked %d times, want 2", asked)
	}
}

func TestForwardFailsClosedWhenTheVerificationFails(t *testing.T) {
	pub := &capturePublisher{}
	b := NewBridge(nil, pub)
	b.verify = func(context.Context, uuid.UUID, uuid.UUID) (bool, error) { return false, context.DeadlineExceeded }
	b.forward(context.Background(), `{"tenant_id":"`+uuid.NewString()+`","conversation_id":"`+uuid.NewString()+`","type":"x","data":{}}`)
	if len(pub.subjects) != 0 {
		t.Fatal("an unverifiable notification was published")
	}
}

// The verification is not a lever for whoever can run pg_notify: refusals are remembered, queries are bounded per second, and answers expire.
func TestForwardCostsAtMostABoundedNumberOfQueriesPerSecond(t *testing.T) {
	pub := &capturePublisher{}
	b := NewBridge(nil, pub)
	now := time.Unix(1_800_000_000, 0)
	b.now = func() time.Time { return now }
	asked := 0
	b.verify = func(context.Context, uuid.UUID, uuid.UUID) (bool, error) { asked++; return false, nil }
	for i := 0; i < 5000; i++ { // five thousand DISTINCT forged pairs in the same instant
		b.forward(context.Background(), `{"tenant_id":"`+uuid.NewString()+`","conversation_id":"`+uuid.NewString()+`","type":"x","data":{}}`)
	}
	if asked > verifyBurst {
		t.Fatalf("5000 forged pairs in one instant cost %d database queries; the burst bound is %d", asked, verifyBurst)
	}
	if len(pub.subjects) != 0 {
		t.Fatal("a forged pair was published")
	}
	now = now.Add(time.Second) // a second later the bucket has refilled a little, not a flood
	before := asked
	for i := 0; i < 5000; i++ {
		b.forward(context.Background(), `{"tenant_id":"`+uuid.NewString()+`","conversation_id":"`+uuid.NewString()+`","type":"x","data":{}}`)
	}
	if asked-before > verifiesPerSec+1 {
		t.Fatalf("one more second of forgeries cost %d queries, want at most %d", asked-before, verifiesPerSec)
	}
}

func TestForwardAnswersExpireAndAreAskedAgain(t *testing.T) {
	tenant, conv := uuid.New(), uuid.New()
	pub := &capturePublisher{}
	b := NewBridge(nil, pub)
	now := time.Unix(1_800_000_000, 0)
	b.now = func() time.Time { return now }
	truth := true
	asked := 0
	b.verify = func(context.Context, uuid.UUID, uuid.UUID) (bool, error) { asked++; return truth, nil }
	msg := `{"tenant_id":"` + tenant.String() + `","conversation_id":"` + conv.String() + `","type":"x","data":{}}`
	b.forward(context.Background(), msg)
	now = now.Add(positiveTTL - time.Second)
	b.forward(context.Background(), msg)
	if asked != 1 || len(pub.subjects) != 2 {
		t.Fatalf("before the lifetime ends the answer is reused: asked=%d published=%d", asked, len(pub.subjects))
	}
	truth = false // the relation changed (the row was removed or re-created elsewhere)
	now = now.Add(2 * time.Second)
	b.forward(context.Background(), msg)
	if asked != 2 || len(pub.subjects) != 2 {
		t.Fatalf("after the lifetime ends it is asked again and now refused: asked=%d published=%d", asked, len(pub.subjects))
	}
}
