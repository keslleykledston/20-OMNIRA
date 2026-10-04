package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var now = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func TestNewTopicThreadValidatesAndStartsOpen(t *testing.T) {
	tenant := uuid.New()
	topic, err := NewTopicThread(tenant, "  Pedido 837 atrasado  ", SourceManual, now)
	if err != nil {
		t.Fatal(err)
	}
	if topic.Title != "Pedido 837 atrasado" || topic.Status != TopicOpen || topic.PrivacyPolicy != PrivacyPublic || topic.TenantID != tenant || topic.ID == uuid.Nil {
		t.Fatalf("unexpected topic: %+v", topic)
	}
	for name, c := range map[string]struct {
		tenant uuid.UUID
		title  string
		source TopicSource
	}{
		"nil tenant":     {uuid.Nil, "x", SourceManual},
		"empty title":    {tenant, "   ", SourceManual},
		"long title":     {tenant, strings.Repeat("a", MaxTitleRunes+1), SourceManual},
		"unknown source": {tenant, "x", TopicSource("magic")},
	} {
		if _, err := NewTopicThread(c.tenant, c.title, c.source, now); !errors.Is(err, ErrInvalidTopic) {
			t.Errorf("%s: err = %v, want ErrInvalidTopic", name, err)
		}
	}
	if _, err := NewTopicThread(tenant, strings.Repeat("ç", MaxTitleRunes), SourceAI, now); err != nil {
		t.Errorf("a title of exactly %d runes must be accepted: %v", MaxTitleRunes, err)
	}
}

func TestTopicStatusTransitions(t *testing.T) {
	topic, _ := NewTopicThread(uuid.New(), "x", SourceManual, now)
	if err := topic.Reopen(now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("reopening an open topic must fail, got %v", err)
	}
	if err := topic.Resolve(now.Add(time.Minute)); err != nil || topic.Status != TopicResolved || topic.ResolvedAt == nil {
		t.Fatalf("resolve: %v %+v", err, topic)
	}
	if err := topic.Resolve(now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("resolving twice must fail, got %v", err)
	}
	if err := topic.Reopen(now.Add(time.Hour)); err != nil || topic.Status != TopicOpen || topic.ResolvedAt != nil {
		t.Fatalf("reopen: %v %+v", err, topic)
	}
	if err := topic.Archive(now); err != nil || topic.Status != TopicArchived {
		t.Fatalf("archive: %v", err)
	}
	if err := topic.Reopen(now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("an archived topic is final, got %v", err)
	}
	if err := topic.Archive(now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("archiving twice must fail, got %v", err)
	}
}

func TestMessageTopicLinkValidation(t *testing.T) {
	tenant, msg, topic := uuid.New(), uuid.New(), uuid.New()
	ok := 0.9
	if _, err := NewMessageTopicLink(tenant, msg, topic, RelationPrimary, DecisionAgent, &ok); err != nil {
		t.Fatal(err)
	}
	bad := 1.5
	cases := map[string]func() error{
		"nil message": func() error {
			_, e := NewMessageTopicLink(tenant, uuid.Nil, topic, RelationPrimary, DecisionAgent, nil)
			return e
		},
		"bad relation": func() error { _, e := NewMessageTopicLink(tenant, msg, topic, "x", DecisionAgent, nil); return e },
		"bad source":   func() error { _, e := NewMessageTopicLink(tenant, msg, topic, RelationPrimary, "x", nil); return e },
		"confidence above": func() error {
			_, e := NewMessageTopicLink(tenant, msg, topic, RelationPrimary, DecisionAgent, &bad)
			return e
		},
	}
	for name, fn := range cases {
		if err := fn(); !errors.Is(err, ErrInvalidTopic) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestTopicTicketLinkValidation(t *testing.T) {
	tenant, topic, ticket := uuid.New(), uuid.New(), uuid.New()
	if _, err := NewTopicTicketLink(tenant, topic, ticket, TicketPrimary, TicketLinkAgent, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTopicTicketLink(tenant, topic, uuid.Nil, TicketPrimary, TicketLinkAgent, nil); !errors.Is(err, ErrInvalidTopic) {
		t.Fatalf("nil ticket: %v", err)
	}
	if _, err := NewTopicTicketLink(tenant, topic, ticket, "owner", TicketLinkAgent, nil); !errors.Is(err, ErrInvalidTopic) {
		t.Fatalf("bad relation: %v", err)
	}
	if _, err := NewTopicTicketLink(tenant, topic, ticket, TicketRelated, "robot", nil); !errors.Is(err, ErrInvalidTopic) {
		t.Fatalf("bad origin: %v", err)
	}
}

// The enumerations here must stay in sync with the CHECK constraints of migration 000063.
func TestEnumerationsMatchTheMigration(t *testing.T) {
	for _, s := range []TopicStatus{TopicOpen, TopicResolved, TopicArchived} {
		if !s.Valid() {
			t.Errorf("%s", s)
		}
	}
	if TopicStatus("closed").Valid() || PrivacyPolicy("secret").Valid() || SummaryStatus("final").Valid() {
		t.Error("unknown values must be invalid")
	}
	for _, s := range []SummaryStatus{SummaryAIInferred, SummaryCustomerConfirmed, SummaryAgentConfirmed, SummaryCorrected, SummarySuperseded} {
		if !s.Valid() {
			t.Errorf("%s", s)
		}
	}
	for _, r := range []ConversationRelation{ConversationOrigin, ConversationActive, ConversationRelated, ConversationHandoff} {
		if !r.Valid() {
			t.Errorf("%s", r)
		}
	}
}
