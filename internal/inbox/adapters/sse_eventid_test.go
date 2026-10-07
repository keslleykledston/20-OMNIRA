package adapters_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// End to end through real NATS: an event the bridge stamped with an id reaches the client with that id as the SSE `id:` field
// and inside the JSON, and an event from an older bridge (no id) still arrives exactly as before.
func TestSSEForwardsTheEventIDAndStillServesLegacyEvents(t *testing.T) {
	f := newSSE(t, time.Hour)
	_, r, _ := f.open(t, "/t/"+f.tenant.String()+"/events")
	time.Sleep(100 * time.Millisecond)

	id := uuid.NewString()
	body, _ := json.Marshal(map[string]any{"event_id": id, "v": 1, "type": "message_received", "id": f.conv.String(),
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "data": map[string]any{"message_id": "m1"}})
	if err := f.nc.Publish("inbox.events."+f.tenant.String()+"."+f.conv.String(), body); err != nil {
		t.Fatal(err)
	}
	_ = f.nc.Flush()
	var sawID bool
	var data string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "id: "+id {
			sawID = true
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
			break
		}
	}
	var ev map[string]any
	if json.Unmarshal([]byte(data), &ev) != nil || !sawID || ev["event_id"] != id || ev["v"].(float64) != 1 || ev["type"] != "message_received" {
		t.Fatalf("sawID=%v event=%s", sawID, data)
	}

	publish(t, f.nc, f.tenant, f.conv, "conversation_updated") // legacy shape: no event_id
	legacy, err := nextData(r)
	if err != nil || strings.Contains(legacy, "event_id") || !strings.Contains(legacy, "conversation_updated") {
		t.Fatalf("legacy event changed: %q (%v)", legacy, err)
	}
}
