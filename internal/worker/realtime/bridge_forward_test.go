package realtime

import (
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
	b.forward(payload)
	b.forward(payload)
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
		b.forward(bad)
	}
	if len(pub.bodies) != 0 {
		t.Fatalf("malformed notifications must not be published: %d", len(pub.bodies))
	}
}
