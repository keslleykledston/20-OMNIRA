package adapters

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	"github.com/omnira/omnira/internal/platform/pagination"
)

func svc(e *env) (*application.TopicService, *PostgresTopicRepository) {
	repo := NewPostgresTopicRepository(e.app)
	return application.NewTopicService(repo), repo
}

func TestTopicCreateReadAndConversationLink(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	s, repo := svc(e)
	m1 := e.message(a.id, a.conversation, "pedido 837 atrasou")
	var topic *domain.TopicThread
	e.session(a.id, admin, func(ctx context.Context) {
		var err error
		topic, err = s.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, ContactID: &a.contact, Title: "Pedido 837", MessageIDs: []uuid.UUID{m1}})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.GetTopic(ctx, topic.ID)
		if err != nil || got.Title != "Pedido 837" || got.Status != domain.TopicOpen || got.OriginConversationID == nil || *got.OriginConversationID != a.conversation {
			t.Fatalf("get = %+v %v", got, err)
		}
		items, err := s.ListConversationTopics(ctx, a.conversation)
		if err != nil || len(items) != 1 || items[0].MessageCount != 1 || items[0].TicketCount != 0 || items[0].LastMessageAt == nil {
			t.Fatalf("list = %+v %v", items, err)
		}
		byContact, err := s.ListContactTopics(ctx, a.contact, nil, 0)
		if err != nil || len(byContact) != 1 {
			t.Fatalf("by contact = %+v %v", byContact, err)
		}
	})
	if e.count(`SELECT count(*) FROM topic_conversation_links WHERE topic_thread_id=$1 AND conversation_id=$2 AND relation='origin'`, topic.ID, a.conversation) != 1 {
		t.Fatal("the origin conversation link must exist")
	}
	_ = repo
}

func TestOneMessageInManyTopicsAndOneTopicAcrossConversations(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	other := e.conversationFor(a.id, nil)
	s, _ := svc(e)
	msg := e.message(a.id, a.conversation, "o pedido atrasou e a nota veio errada")
	msgOther := e.message(a.id, other, "segue o comprovante do pedido")
	e.session(a.id, admin, func(ctx context.Context) {
		delivery, err := s.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, Title: "Entrega", MessageIDs: []uuid.UUID{msg}})
		if err != nil {
			t.Fatal(err)
		}
		invoice, err := s.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, Title: "Nota fiscal"})
		if err != nil {
			t.Fatal(err)
		}
		// the same physical message belongs to both topics (no duplication of the message)
		if err := s.LinkMessage(ctx, invoice.ID, msg, domain.RelationSecondary); err != nil {
			t.Fatal(err)
		}
		// the delivery topic continues in another conversation
		if err := s.LinkMessage(ctx, delivery.ID, msgOther, domain.RelationSupporting); err != nil {
			t.Fatal(err)
		}
		d, _ := s.ListConversationTopics(ctx, a.conversation)
		o, _ := s.ListConversationTopics(ctx, other)
		if len(d) != 2 {
			t.Fatalf("conversation A topics = %d, want 2", len(d))
		}
		if len(o) != 1 || o[0].Topic.ID != delivery.ID {
			t.Fatalf("conversation B must show only the delivery topic, got %+v", o)
		}
		msgs, _, err := s.ListTopicMessages(ctx, delivery.ID, nil, 50)
		if err != nil || len(msgs) != 2 {
			t.Fatalf("delivery timeline = %d %v", len(msgs), err)
		}
		inv, _, _ := s.ListTopicMessages(ctx, invoice.ID, nil, 50)
		if len(inv) != 1 || inv[0].ID != msg || inv[0].Relation != domain.RelationSecondary {
			t.Fatalf("invoice timeline = %+v", inv)
		}
	})
	if e.count(`SELECT count(*) FROM messages WHERE id=$1`, msg) != 1 {
		t.Fatal("linking must never duplicate the message")
	}
	if e.count(`SELECT count(*) FROM message_topic_links WHERE message_id=$1`, msg) != 2 {
		t.Fatal("the message must have two topic links")
	}
}

func TestMessageLinkAuthorityIsNeverDowngradedAndReplayIsIdempotent(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	s, repo := svc(e)
	msg := e.message(a.id, a.conversation, "x")
	conf := 0.7
	e.session(a.id, admin, func(ctx context.Context) {
		topic, err := s.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, Title: "T"})
		if err != nil {
			t.Fatal(err)
		}
		link := func(rel domain.MessageRelation, src domain.DecisionSource) {
			l, _ := domain.NewMessageTopicLink(a.id, msg, topic.ID, rel, src, &conf)
			if err := repo.LinkMessage(ctx, l); err != nil {
				t.Fatal(err)
			}
		}
		current := func() (string, string) {
			var rel, src string
			_ = repo.q(ctx).QueryRow(ctx, `SELECT relation, decision_source FROM message_topic_links WHERE message_id=$1`, msg).Scan(&rel, &src)
			return rel, src
		}
		link(domain.RelationPrimary, domain.DecisionAI)
		link(domain.RelationPrimary, domain.DecisionAI) // replay of the same event
		var n int
		_ = repo.q(ctx).QueryRow(ctx, `SELECT count(*) FROM message_topic_links WHERE message_id=$1`, msg).Scan(&n)
		if n != 1 {
			t.Fatalf("replay created %d links", n)
		}
		link(domain.RelationSecondary, domain.DecisionAgent) // a human decides
		if rel, src := current(); rel != "secondary" || src != "agent" {
			t.Fatalf("agent must override AI, got %s/%s", rel, src)
		}
		link(domain.RelationPrimary, domain.DecisionAI) // a later AI decision must not undo the human one
		if rel, src := current(); rel != "secondary" || src != "agent" {
			t.Fatalf("AI overwrote an agent decision: %s/%s", rel, src)
		}
		link(domain.RelationAmbiguous, domain.DecisionRule)
		if rel, src := current(); src != "agent" {
			t.Fatalf("a rule overwrote an agent decision: %s/%s", rel, src)
		}
	})
}

func TestTicketLinksAreManyToManyWithASinglePrimary(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	s, _ := svc(e)
	c2, c3 := e.conversationFor(a.id, nil), e.conversationFor(a.id, nil)
	t1, t2, shared := e.ticket(a.id, a.conversation, "open"), e.ticket(a.id, c2, "open"), e.ticket(a.id, c3, "open")
	var topicA, topicB *domain.TopicThread
	e.session(a.id, admin, func(ctx context.Context) {
		topicA, _ = s.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, Title: "A"})
		topicB, _ = s.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, Title: "B"})
		if err := s.LinkTicket(ctx, topicA.ID, t1, domain.TicketPrimary); err != nil {
			t.Fatal(err)
		}
		if err := s.LinkTicket(ctx, topicA.ID, t1, domain.TicketPrimary); err != nil {
			t.Fatalf("linking the same pair twice must be idempotent: %v", err)
		}
	})
	// a second primary is refused (its own session: the violation aborts that request)
	e.attempt(a.id, admin, func(ctx context.Context) {
		if err := s.LinkTicket(ctx, topicA.ID, t2, domain.TicketPrimary); !errors.Is(err, domain.ErrPrimaryTicketTaken) {
			t.Errorf("a second primary must be refused, got %v", err)
		}
	})
	// related tickets are unlimited, and one ticket can serve two topics
	e.session(a.id, admin, func(ctx context.Context) {
		for _, c := range []struct {
			topic  uuid.UUID
			ticket uuid.UUID
			rel    domain.TicketRelation
		}{{topicA.ID, t2, domain.TicketRelated}, {topicA.ID, shared, domain.TicketChild}, {topicB.ID, shared, domain.TicketPrimary}} {
			if err := s.LinkTicket(ctx, c.topic, c.ticket, c.rel); err != nil {
				t.Fatal(err)
			}
		}
		tickets, err := s.ListTopicTickets(ctx, topicA.ID)
		if err != nil || len(tickets) != 3 || tickets[0].ID != t1 || tickets[0].Relation != domain.TicketPrimary {
			t.Fatalf("topic A tickets = %+v %v", tickets, err)
		}
		list, _ := s.ListConversationTopics(ctx, a.conversation)
		counts := map[string]int{}
		for _, it := range list {
			counts[it.Topic.Title] = it.TicketCount
		}
		if counts["A"] != 3 || counts["B"] != 1 {
			t.Fatalf("ticket counts = %v", counts)
		}
	})
}

func TestResolveReopenAndTimestamps(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	s, _ := svc(e)
	e.session(a.id, admin, func(ctx context.Context) {
		topic, _ := s.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, Title: "T"})
		resolved := domain.TopicResolved
		got, err := s.UpdateTopic(ctx, topic.ID, application.UpdateTopicInput{Status: &resolved})
		if err != nil || got.Status != domain.TopicResolved || got.ResolvedAt == nil {
			t.Fatalf("resolve: %v %+v", err, got)
		}
		if _, err := s.UpdateTopic(ctx, topic.ID, application.UpdateTopicInput{Status: &resolved}); err != nil {
			t.Fatalf("setting the same status again is a no-op: %v", err)
		}
		open := domain.TopicOpen
		got, err = s.UpdateTopic(ctx, topic.ID, application.UpdateTopicInput{Status: &open})
		if err != nil || got.Status != domain.TopicOpen || got.ResolvedAt != nil {
			t.Fatalf("reopen: %v %+v", err, got)
		}
		archived := domain.TopicArchived
		if _, err := s.UpdateTopic(ctx, topic.ID, application.UpdateTopicInput{Status: &archived}); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Fatalf("archiving through update must be refused: %v", err)
		}
		title := "  "
		if _, err := s.UpdateTopic(ctx, topic.ID, application.UpdateTopicInput{Title: &title}); !errors.Is(err, domain.ErrInvalidTopic) {
			t.Fatalf("blank title: %v", err)
		}
	})
	// the database refuses an inconsistent resolved state on its own
	if _, err := e.seed.Exec(e.ctx, `UPDATE topic_threads SET status='resolved', resolved_at=NULL WHERE tenant_id=$1`, a.id); err == nil {
		t.Fatal("CHECK must refuse resolved without resolved_at")
	}
}

func TestCrossTenantIdsAreNotFoundAndInvisible(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	adminA, adminB := e.member(a.id, "tenant_admin"), e.member(b.id, "tenant_admin")
	s, repo := svc(e)
	msgB := e.message(b.id, b.conversation, "segredo de B")
	ticketB := e.ticket(b.id, b.conversation, "open")
	var topicB *domain.TopicThread
	e.session(b.id, adminB, func(ctx context.Context) {
		var err error
		topicB, err = s.CreateTopic(ctx, application.CreateTopicInput{ConversationID: b.conversation, Title: "Tópico de B", MessageIDs: []uuid.UUID{msgB}})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.LinkTicket(ctx, topicB.ID, ticketB, domain.TicketPrimary); err != nil {
			t.Fatal(err)
		}
	})
	var topicA *domain.TopicThread
	e.session(a.id, adminA, func(ctx context.Context) {
		topicA, _ = s.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, Title: "Tópico de A"})
	})
	// A cannot read, change or traverse B's topic even knowing every id (reads do not abort the session)
	e.session(a.id, adminA, func(ctx context.Context) {
		if _, err := s.GetTopic(ctx, topicB.ID); !errors.Is(err, domain.ErrTopicNotFound) {
			t.Errorf("get B's topic from A: %v", err)
		}
		title := "hijack"
		if _, err := s.UpdateTopic(ctx, topicB.ID, application.UpdateTopicInput{Title: &title}); !errors.Is(err, domain.ErrTopicNotFound) {
			t.Errorf("update B's topic from A: %v", err)
		}
		if _, _, err := s.ListTopicMessages(ctx, topicB.ID, nil, 10); !errors.Is(err, domain.ErrTopicNotFound) {
			t.Errorf("B's timeline from A: %v", err)
		}
	})
	// A cannot attach B's message or ticket to A's own topic: the composite FK refuses another tenant's ids.
	// Each refusal aborts its request, so each runs in its own session.
	e.attempt(a.id, adminA, func(ctx context.Context) {
		if err := s.LinkMessage(ctx, topicA.ID, msgB, domain.RelationPrimary); !errors.Is(err, domain.ErrReferenceNotFound) {
			t.Errorf("link B's message into A's topic: %v", err)
		}
	})
	e.attempt(a.id, adminA, func(ctx context.Context) {
		if err := s.LinkTicket(ctx, topicA.ID, ticketB, domain.TicketRelated); !errors.Is(err, domain.ErrReferenceNotFound) {
			t.Errorf("link B's ticket into A's topic: %v", err)
		}
	})
	e.attempt(a.id, adminA, func(ctx context.Context) {
		if err := s.LinkConversation(ctx, topicA.ID, b.conversation, domain.ConversationRelated); !errors.Is(err, domain.ErrReferenceNotFound) {
			t.Errorf("link B's conversation into A's topic: %v", err)
		}
	})
	e.attempt(a.id, adminA, func(ctx context.Context) {
		// a forged tenant in a raw write is refused by RLS
		if _, err := repo.q(ctx).Exec(ctx, `INSERT INTO topic_threads(tenant_id,title,source) VALUES($1,'forged','manual')`, b.id); err == nil {
			t.Error("RLS must refuse writing into another tenant")
		}
	})
	e.session(a.id, adminA, func(ctx context.Context) {
		var visible int
		_ = repo.q(ctx).QueryRow(ctx, `SELECT count(*) FROM topic_threads`).Scan(&visible)
		if visible != 1 {
			t.Errorf("A's session sees %d topics, want only its own", visible)
		}
		for _, table := range []string{"message_topic_links", "topic_conversation_links", "topic_ticket_links", "topic_summaries"} {
			var n int
			_ = repo.q(ctx).QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE topic_thread_id=$1`, topicB.ID).Scan(&n)
			if n != 0 {
				t.Errorf("%s: A sees %d rows of B's topic", table, n)
			}
		}
	})
	if e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1 AND title='hijack'`, b.id) != 0 {
		t.Fatal("B's topic was changed by A")
	}
}

func TestTenantContextIsRequired(t *testing.T) {
	e := newEnv(t)
	s, _ := svc(e)
	if _, err := s.GetTopic(context.Background(), uuid.New()); err == nil {
		t.Fatal("a call without a tenant context must fail")
	}
	if _, err := s.CreateTopic(context.Background(), application.CreateTopicInput{Title: "x"}); err == nil {
		t.Fatal("create without a tenant context must fail")
	}
}

func TestPaginationOfTheTopicTimeline(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	s, _ := svc(e)
	var ids []uuid.UUID
	for i := 0; i < 5; i++ {
		ids = append(ids, e.message(a.id, a.conversation, "m"))
		time.Sleep(5 * time.Millisecond)
	}
	e.session(a.id, admin, func(ctx context.Context) {
		topic, _ := s.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, Title: "T", MessageIDs: ids})
		first, more, err := s.ListTopicMessages(ctx, topic.ID, nil, 2)
		if err != nil || !more || len(first) != 2 {
			t.Fatalf("page 1: %d more=%v %v", len(first), more, err)
		}
		last := first[len(first)-1]
		page2, more2, err := s.ListTopicMessages(ctx, topic.ID, &pagination.Cursor{ID: last.ID.String(), Timestamp: last.CreatedAt}, 2)
		if err != nil || !more2 || len(page2) != 2 || page2[0].ID == first[0].ID || page2[0].ID == first[1].ID {
			t.Fatalf("page 2: %d more=%v %v", len(page2), more2, err)
		}
		_ = ports.TopicMessage{}
	})
}
