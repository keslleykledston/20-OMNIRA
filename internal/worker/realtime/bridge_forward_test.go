package realtime

import (
	"context"
	"encoding/json"
	"testing"

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
	if asked != 2 { // the forged one and the first genuine one; the second genuine one is remembered
		t.Errorf("the database was asked %d times, want 2 (a verified pair is remembered)", asked)
	}
	b.forward(context.Background(), forged)
	if len(pub.subjects) != 2 {
		t.Error("a negative answer must not be remembered as a positive one")
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
