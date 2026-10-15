package domain_test

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
)

func TestPublicNamesAreVersionedAndMapTheOutsideVocabulary(t *testing.T) {
	cases := []struct {
		ev   domain.ContextEvent
		want string
	}{
		{domain.ContextEvent{EventType: domain.EvSessionOpened}, "service_session.opened.v1"},
		{domain.ContextEvent{EventType: domain.EvSessionClosed}, "service_session.closed.v1"},
		{domain.ContextEvent{EventType: domain.EvSessionClassified}, "service_session.classified.v1"},
		{domain.ContextEvent{EventType: domain.EvCaseOpened}, "service_case.created.v1"},
		{domain.ContextEvent{EventType: domain.EvCaseUpdated}, "service_case.updated.v1"},
		{domain.ContextEvent{EventType: domain.EvCaseStatus, Payload: map[string]any{"to": "resolved"}}, "service_case.resolved.v1"},
		{domain.ContextEvent{EventType: domain.EvCaseStatus, Payload: map[string]any{"to": "waiting"}}, "service_case.updated.v1"},
		{domain.ContextEvent{EventType: domain.EvTaskCreated}, "service_task.created.v1"},
		{domain.ContextEvent{EventType: domain.EvTaskStatus, Payload: map[string]any{"to": "done"}}, "service_task.completed.v1"},
		{domain.ContextEvent{EventType: domain.EvTaskStatus, Payload: map[string]any{"to": "cancelled"}}, "service_task.status_changed.v1"},
		{domain.ContextEvent{EventType: domain.EvExternalLinked}, "external_record.linked.v1"},
		{domain.ContextEvent{EventType: domain.EvExternalStatusChanged}, "external_record.status_changed.v1"},
	}
	for _, c := range cases {
		if got := domain.PublicEvent(c.ev); got != c.want {
			t.Errorf("%s/%v = %s, want %s", c.ev.EventType, c.ev.Payload, got, c.want)
		}
	}
}

func TestTheEnvelopeCarriesIdsAndNeverTheNote(t *testing.T) {
	session, contact, actor, corr := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	ev := domain.ContextEvent{ID: uuid.New(), TenantID: uuid.New(), EventType: domain.EvSessionReopened, EntityType: "service_session", EntityID: session, SessionID: &session,
		ContactID: &contact, ActorType: "user", ActorID: &actor, SourceSystem: "omnira", OccurredAt: time.Now(), RecordedAt: time.Now(), SchemaVersion: 1,
		Payload: map[string]any{"previous_result": "other"}, Note: "texto que o operador digitou e que não sai daqui", CorrelationID: &corr}
	env := domain.Envelope(ev)
	raw, _ := json.Marshal(env)
	if strings.Contains(string(raw), "operador") || env["note"] != nil {
		t.Fatalf("the note never leaves: %s", raw)
	}
	for _, k := range []string{"event_id", "event_type", "tenant_id", "schema_version", "occurred_at", "recorded_at", "entity", "links", "actor", "payload", "correlation_id"} {
		if _, ok := env[k]; !ok {
			t.Errorf("envelope lacks %s", k)
		}
	}
	if env["event_type"] != "service_session.reopened.v1" {
		t.Fatalf("event_type: %v", env["event_type"])
	}
}

// What the code can announce is exactly what the AsyncAPI contract documents: nothing undocumented goes out, nothing documented is impossible.
func TestTheAsyncContractMatchesWhatCanBeAnnounced(t *testing.T) {
	raw, err := os.ReadFile("../../../contracts/asyncapi/service-context-events-v1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^  ([a-z_]+\.[a-z_]+)\.v1:\s*$`)
	documented := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		documented[m[1]+".v1"] = true
	}
	variants := []map[string]any{nil, {"to": "resolved"}, {"to": "done"}, {"to": "waiting"}}
	announced := map[string]bool{}
	for _, typ := range domain.AllEventTypes() {
		for _, v := range variants {
			announced[domain.PublicEvent(domain.ContextEvent{EventType: typ, Payload: v})] = true
		}
	}
	for name := range announced {
		if !documented[name] {
			t.Errorf("%s can be announced but is not in the AsyncAPI contract", name)
		}
	}
	for name := range documented {
		if !announced[name] {
			t.Errorf("the AsyncAPI contract documents %s but nothing can announce it", name)
		}
	}
	if !strings.HasSuffix(domain.PublicEvent(domain.ContextEvent{EventType: domain.EvSessionOpened}), ".v1") {
		t.Fatal("every public name is versioned")
	}
}
