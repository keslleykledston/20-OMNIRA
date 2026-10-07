package adapters

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// No NATS needed: the frame is a pure function, so these run in every environment.
func TestSSEFrameCarriesEventIDAndKeepsTheLegacyDataShape(t *testing.T) {
	conv := uuid.New()
	e := RealtimeEvent{EventID: "6f1c2d3e-0000-4000-8000-00000000abcd", Version: 1, Type: "message_received", ID: conv,
		Timestamp: time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC), Data: map[string]any{"message_id": "m1"}}
	frame := string(sseFrame(e))
	lines := strings.Split(strings.TrimSuffix(frame, "\n\n"), "\n")
	if len(lines) != 2 || lines[0] != "id: 6f1c2d3e-0000-4000-8000-00000000abcd" || !strings.HasPrefix(lines[1], "data: ") {
		t.Fatalf("frame = %q", frame)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &got); err != nil {
		t.Fatal(err)
	}
	// what the web already reads is untouched; the additions are additive
	if got["type"] != "message_received" || got["id"] != conv.String() || got["timestamp"] == nil || got["data"] == nil || got["event_id"] != e.EventID || got["v"].(float64) != 1 {
		t.Fatalf("data = %v", got)
	}
}

func TestSSEFrameOfALegacyEventHasNoIDLineAndNoNewFields(t *testing.T) {
	frame := string(sseFrame(RealtimeEvent{Type: "conversation_updated", ID: uuid.New(), Timestamp: time.Now().UTC()}))
	if strings.HasPrefix(frame, "id: ") {
		t.Fatalf("a legacy event must not get an id line: %q", frame)
	}
	if strings.Contains(frame, "event_id") || strings.Contains(frame, `"v"`) || !strings.HasPrefix(frame, "data: ") {
		t.Fatalf("legacy frame changed: %q", frame)
	}
}

func TestSSEFrameRefusesAnEventIDThatCouldInjectAField(t *testing.T) {
	frame := string(sseFrame(RealtimeEvent{EventID: "x\nevent: hijack\ndata: {}", Type: "t", ID: uuid.New(), Timestamp: time.Now().UTC()}))
	if strings.Contains(frame, "\nevent: hijack") && strings.HasPrefix(frame, "id: ") {
		t.Fatalf("SSE field injection through the event id: %q", frame)
	}
	if strings.Count(frame, "\n\n") != 1 {
		t.Fatalf("a frame must end exactly once: %q", frame)
	}
}
