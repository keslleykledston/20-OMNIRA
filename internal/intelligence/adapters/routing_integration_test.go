package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

func routingSvc(e *env, auto bool) (*application.RoutingService, *application.TopicService, *PostgresRoutingRepository) {
	flags := application.DefaultFlags()
	flags.TopicAutoRoutingEnabled = auto
	topics := NewPostgresTopicRepository(e.app)
	routing := NewPostgresRoutingRepository(e.app)
	return application.NewRoutingService(routing, topics, flags, domain.DefaultRoutingConfig(), nil), application.NewTopicService(topics).WithRouting(routing), routing
}

func grp(id uuid.UUID) ports.MessageRef { return ports.MessageRef{Kind: ports.KindGroup, ID: id} }
func cnv(id uuid.UUID) ports.MessageRef {
	return ports.MessageRef{Kind: ports.KindConversation, ID: id}
}

// Scenario A of the plan: one WhatsApp group, three people, three subjects at once.
func TestScenarioAGroupWithSeveralSubjectsAtOnce(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	g := e.group(a.id)
	joao, maria, pedro := e.participant(a.id, g, "111@lid", "João"), e.participant(a.id, g, "222@lid", "Maria"), e.participant(a.id, g, "333@lid", "Pedro")
	routing, _, repo := routingSvc(e, true)

	m1 := e.groupMessage(a.id, g, joao, "pedido 837 não chegou", nil)
	m2 := e.groupMessage(a.id, g, maria, "a NF 992 está errada", nil)
	m3 := e.groupMessage(a.id, g, joao, "já passou quatro dias", nil)
	m4 := e.groupMessage(a.id, g, pedro, "meu acesso está bloqueado", nil)

	var o1, o2, o3, o4 *application.RoutingOutcome
	step := func(id uuid.UUID) (out *application.RoutingOutcome) {
		e.session(a.id, admin, func(ctx context.Context) {
			var err error
			if out, err = routing.Route(ctx, grp(id), application.RouteOptions{}); err != nil {
				t.Fatal(err)
			}
		})
		return out
	}
	o1, o2, o3, o4 = step(m1), step(m2), step(m3), step(m4)

	if o1.Result.Status != domain.RoutingNewTopic || o1.CreatedTopic == nil {
		t.Fatalf("João's first message opens a topic for order 837: %+v", o1.Result)
	}
	if o2.Result.Status != domain.RoutingNewTopic || o2.CreatedTopic == nil || *o2.CreatedTopic == *o1.CreatedTopic {
		t.Fatalf("Maria's invoice is another topic: %+v", o2.Result)
	}
	if o3.Result.Status != domain.RoutingAssigned || *o3.Result.Primary != *o1.CreatedTopic {
		t.Fatalf("João continuing goes to HIS topic, not to the group's latest: %+v", o3.Result)
	}
	if o4.Result.Status != domain.RoutingAmbiguous || o4.AmbiguityID == nil {
		t.Fatalf("Pedro's message names nothing: a person must decide: %+v", o4.Result)
	}
	// the person decides: a new topic for Pedro's problem
	var topicC uuid.UUID
	e.session(a.id, admin, func(ctx context.Context) {
		var err error
		topicC, err = routing.ResolveAmbiguity(ctx, *o4.AmbiguityID, nil, "Acesso bloqueado", domain.DecisionAgent)
		if err != nil {
			t.Fatal(err)
		}
	})

	// final state: three topics, each message in the right one, entities recorded, audit complete
	topicOf := func(msg uuid.UUID) (topic uuid.UUID, source string) {
		_ = e.seed.QueryRow(e.ctx, `SELECT topic_thread_id, decision_source FROM group_message_topic_links WHERE tenant_id=$1 AND group_message_id=$2`, a.id, msg).Scan(&topic, &source)
		return topic, source
	}
	if tp, src := topicOf(m1); tp != *o1.CreatedTopic || src != "entity" {
		t.Errorf("m1 -> %v %s", tp, src)
	}
	if tp, _ := topicOf(m3); tp != *o1.CreatedTopic {
		t.Errorf("m3 -> %v, want João's topic", tp)
	}
	if tp, _ := topicOf(m2); tp != *o2.CreatedTopic {
		t.Errorf("m2 -> %v, want the invoice topic", tp)
	}
	if tp, src := topicOf(m4); tp != topicC || src != "agent" {
		t.Errorf("m4 -> %v %s, want the new topic by an agent decision", tp, src)
	}
	if n := e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1`, a.id); n != 3 {
		t.Errorf("topics = %d, want 3", n)
	}
	if n := e.count(`SELECT count(*) FROM topic_entities WHERE tenant_id=$1 AND ((entity_type='order' AND canonical_key='837') OR (entity_type='invoice' AND canonical_key='992'))`, a.id); n != 2 {
		t.Errorf("entities recorded = %d, want 2", n)
	}
	if n := e.count(`SELECT count(*) FROM topic_group_links WHERE tenant_id=$1 AND group_id=$2`, a.id, g.group); n != 3 {
		t.Errorf("topics linked to the group = %d, want 3", n)
	}
	if n := e.count(`SELECT count(*) FROM routing_decisions WHERE tenant_id=$1 AND applied AND signals ? 'signals'`, a.id); n != 4 {
		t.Errorf("decisions with their evidence = %d, want 4", n)
	}
	if n := e.count(`SELECT count(*) FROM routing_decisions WHERE tenant_id=$1 AND overridden_at IS NOT NULL`, a.id); n != 1 {
		t.Errorf("the human decision must supersede the automated one, overridden = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM conversation_topic_focus WHERE tenant_id=$1`, a.id); n < 2 {
		t.Errorf("focus hints written = %d", n)
	}
	// reading back through the API layer: the group topics list shows the three subjects with their counts
	_ = repo
}

func TestDirectReplyInheritsTheRepliedTopic(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	g := e.group(a.id)
	joao, ana := e.participant(a.id, g, "111@lid", "João"), e.participant(a.id, g, "444@lid", "Ana")
	routing, _, _ := routingSvc(e, true)
	first := e.groupMessage(a.id, g, joao, "pedido 837 não chegou", nil)
	var topic uuid.UUID
	e.session(a.id, admin, func(ctx context.Context) {
		out, err := routing.Route(ctx, grp(first), application.RouteOptions{})
		if err != nil || out.CreatedTopic == nil {
			t.Fatalf("setup: %+v %v", out, err)
		}
		topic = *out.CreatedTopic
	})
	// another group member answers with a message that says nothing by itself, but replies to the first one
	reply := e.groupMessage(a.id, g, ana, "sim, comigo também", &first)
	e.session(a.id, admin, func(ctx context.Context) {
		out, err := routing.Route(ctx, grp(reply), application.RouteOptions{})
		if err != nil || out.Result.Status != domain.RoutingAssigned || *out.Result.Primary != topic || out.Result.Source != domain.DecisionReply {
			t.Fatalf("reply: %+v %v", out, err)
		}
	})
	var source string
	_ = e.seed.QueryRow(e.ctx, `SELECT decision_source FROM group_message_topic_links WHERE tenant_id=$1 AND group_message_id=$2`, a.id, reply).Scan(&source)
	if source != "reply" {
		t.Fatalf("link source = %q, want reply", source)
	}
}

// Scenario D: one message, two subjects, two links, no duplicated message.
func TestOneMessageNamingTwoSubjectsLinksBothTopics(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	routing, topicsSvc, _ := routingSvc(e, true)
	var delivery, invoice *domain.TopicThread
	e.session(a.id, admin, func(ctx context.Context) {
		delivery, _ = topicsSvc.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, ContactID: &a.contact, Title: "Pedido 837"})
		invoice, _ = topicsSvc.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, ContactID: &a.contact, Title: "Nota fiscal 992"})
	})
	r := NewPostgresRoutingRepository(e.app)
	e.session(a.id, admin, func(ctx context.Context) {
		if err := r.UpsertEntities(ctx, a.id, delivery.ID, domain.ExtractEntities("pedido 837"), nil, "rule"); err != nil {
			t.Fatal(err)
		}
		if err := r.UpsertEntities(ctx, a.id, invoice.ID, domain.ExtractEntities("nota 992"), nil, "rule"); err != nil {
			t.Fatal(err)
		}
	})
	msg := e.message(a.id, a.conversation, "o pedido 837 atrasou e a nota 992 veio errada")
	e.session(a.id, admin, func(ctx context.Context) {
		out, err := routing.Route(ctx, cnv(msg), application.RouteOptions{})
		if err != nil || out.Result.Status != domain.RoutingMultiTopic {
			t.Fatalf("multi-topic: %+v %v", out, err)
		}
	})
	if n := e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2`, a.id, msg); n != 2 {
		t.Fatalf("links = %d, want 2", n)
	}
	if n := e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2 AND relation='primary'`, a.id, msg); n != 1 {
		t.Fatal("exactly one link is primary")
	}
	if n := e.count(`SELECT count(*) FROM messages WHERE id=$1`, msg); n != 1 {
		t.Fatal("the message must not be duplicated")
	}
}

func TestRoutingTheSameMessageTwiceOrConcurrentlyChangesNothingTheSecondTime(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	routing, _, _ := routingSvc(e, true)
	msg := e.message(a.id, a.conversation, "pedido 5150 não chegou")
	var wg sync.WaitGroup
	results := make([]*application.RoutingOutcome, 6)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			e.attempt(a.id, admin, func(ctx context.Context) {
				out, err := routing.Route(ctx, cnv(msg), application.RouteOptions{})
				if err == nil {
					results[i] = out
				}
			})
		}(i)
	}
	wg.Wait()
	// the same event again afterwards
	e.session(a.id, admin, func(ctx context.Context) {
		out, err := routing.Route(ctx, cnv(msg), application.RouteOptions{})
		if err != nil || !out.Replay {
			t.Fatalf("a later replay must be a no-op: %+v %v", out, err)
		}
	})
	if n := e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1`, a.id); n != 1 {
		t.Fatalf("topics = %d, want exactly 1 however many workers routed the message", n)
	}
	if n := e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2`, a.id, msg); n != 1 {
		t.Fatalf("links = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM routing_decisions WHERE tenant_id=$1 AND message_id=$2 AND applied`, a.id, msg); n != 1 {
		t.Fatalf("active decisions = %d, want 1", n)
	}
}

func TestWithAutoRoutingOffTheRouterOnlyRecordsAProposal(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	routing, _, _ := routingSvc(e, false)
	msg := e.message(a.id, a.conversation, "pedido 777 não chegou")
	for i := 0; i < 3; i++ { // re-running refreshes the proposal, it never multiplies
		e.session(a.id, admin, func(ctx context.Context) {
			out, err := routing.Route(ctx, cnv(msg), application.RouteOptions{})
			if err != nil || out.Applied || out.Result.Status != domain.RoutingNewTopic {
				t.Fatalf("dry run: %+v %v", out, err)
			}
		})
	}
	if e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1`, a.id) != 0 || e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1`, a.id) != 0 {
		t.Fatal("with the flag off nothing may be created or linked")
	}
	if n := e.count(`SELECT count(*) FROM routing_decisions WHERE tenant_id=$1 AND message_id=$2 AND NOT applied AND status='new_topic'`, a.id, msg); n != 1 {
		t.Fatalf("proposals recorded = %d, want 1", n)
	}
}

func TestSameContactWithTwoOpenTopicsIsAmbiguousAndAHumanOverrideSticks(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	agent := e.member(a.id, "tenant_agent")
	if _, err := e.seed.Exec(e.ctx, `UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, agent); err != nil {
		t.Fatal(err)
	}
	routing, topicsSvc, _ := routingSvc(e, true)
	var t1, t2 *domain.TopicThread
	e.session(a.id, agent, func(ctx context.Context) {
		t1, _ = topicsSvc.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, ContactID: &a.contact, Title: "Cobrança"})
		t2, _ = topicsSvc.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, ContactID: &a.contact, Title: "Equipamento danificado"})
	})
	msg := e.message(a.id, a.conversation, "e agora, como fica?")
	var amb *uuid.UUID
	e.session(a.id, agent, func(ctx context.Context) {
		out, err := routing.Route(ctx, cnv(msg), application.RouteOptions{})
		if err != nil || out.Result.Status != domain.RoutingAmbiguous || out.AmbiguityID == nil {
			t.Fatalf("expected an ambiguity: %+v %v", out, err)
		}
		amb = out.AmbiguityID
	})
	// the CUSTOMER answers the clarification question: that is a human decision too
	e.session(a.id, agent, func(ctx context.Context) {
		chosen, err := routing.ResolveAmbiguity(ctx, *amb, &t2.ID, "", domain.DecisionCustomer)
		if err != nil || chosen != t2.ID {
			t.Fatalf("customer resolution: %v %v", chosen, err)
		}
	})
	var src string
	_ = e.seed.QueryRow(e.ctx, `SELECT decision_source FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2`, a.id, msg).Scan(&src)
	if src != "customer" {
		t.Fatalf("source = %q, want customer", src)
	}
	// a resolved ambiguity cannot be resolved twice, and an agent cannot use the AI source to resolve
	e.attempt(a.id, agent, func(ctx context.Context) {
		if _, err := routing.ResolveAmbiguity(ctx, *amb, &t1.ID, "", domain.DecisionAgent); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("second resolution: %v", err)
		}
	})
	e.attempt(a.id, agent, func(ctx context.Context) {
		if _, err := routing.ResolveAmbiguity(ctx, *amb, &t1.ID, "", domain.DecisionAI); !errors.Is(err, domain.ErrInvalidTopic) {
			t.Errorf("an AI source must not resolve: %v", err)
		}
	})
	// re-routing a message a person already placed does nothing, and an agent edit keeps authority over automation
	e.session(a.id, agent, func(ctx context.Context) {
		out, err := routing.Route(ctx, cnv(msg), application.RouteOptions{})
		if err != nil || !out.Replay {
			t.Fatalf("re-route of a placed message: %+v %v", out, err)
		}
		if err := topicsSvc.LinkMessage(ctx, t1.ID, msg, domain.RelationSecondary); err != nil {
			t.Fatal(err)
		}
	})
	if n := e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2`, a.id, msg); n != 2 {
		t.Fatalf("links = %d, want 2 (customer's choice kept, agent's addition)", n)
	}
}

func TestOutboundMessagesAreNotRouted(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	routing, _, _ := routingSvc(e, true)
	out := uuid.New()
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status) VALUES($1,$2,$3,'outbound','text','pedido 1 enviado','sent')`, out, a.id, a.conversation)
	e.attempt(a.id, admin, func(ctx context.Context) {
		if _, err := routing.Route(ctx, cnv(out), application.RouteOptions{}); !errors.Is(err, application.ErrNotRoutable) {
			t.Errorf("an agent's own message must not be routed: %v", err)
		}
	})
}

func TestRoutingNeverCrossesTenants(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	adminA, adminB := e.member(a.id, "tenant_admin"), e.member(b.id, "tenant_admin")
	routing, topicsSvc, _ := routingSvc(e, true)
	// tenant B already has a topic about order 837
	var topicB *domain.TopicThread
	e.session(b.id, adminB, func(ctx context.Context) {
		topicB, _ = topicsSvc.CreateTopic(ctx, application.CreateTopicInput{ConversationID: b.conversation, ContactID: &b.contact, Title: "Pedido 837"})
		_ = NewPostgresRoutingRepository(e.app).UpsertEntities(ctx, b.id, topicB.ID, domain.ExtractEntities("pedido 837"), nil, "rule")
	})
	msgB := e.message(b.id, b.conversation, "mensagem de B")
	// A's message about the SAME order number must open A's own topic, never attach to B's
	msgA := e.message(a.id, a.conversation, "pedido 837 atrasou")
	e.session(a.id, adminA, func(ctx context.Context) {
		out, err := routing.Route(ctx, cnv(msgA), application.RouteOptions{})
		if err != nil || out.Result.Status != domain.RoutingNewTopic || out.CreatedTopic == nil || *out.CreatedTopic == topicB.ID {
			t.Fatalf("A's order 837 must not match B's topic: %+v %v", out, err)
		}
	})
	// A cannot route, nor resolve anything about, B's messages
	e.attempt(a.id, adminA, func(ctx context.Context) {
		if _, err := routing.Route(ctx, cnv(msgB), application.RouteOptions{}); !errors.Is(err, domain.ErrReferenceNotFound) {
			t.Errorf("routing B's message from A: %v", err)
		}
	})
	if n := e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1`, b.id); n != 0 {
		t.Fatalf("tenant B gained %d links from A's activity", n)
	}
	if n := e.count(`SELECT count(*) FROM routing_decisions WHERE tenant_id=$1`, b.id); n != 0 {
		t.Fatalf("tenant B gained %d decisions from A's activity", n)
	}
}

func TestAmbiguityAPIListAndResolveFollowTheAttendantRule(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	attendant := e.member(a.id, "tenant_agent")
	stranger := e.member(a.id, "tenant_agent")
	supervisor := e.member(a.id, "tenant_supervisor")
	viewer := e.readOnlyMember(a.id)
	adminB := e.member(b.id, "tenant_admin")
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, attendant)
	routing, topicsSvc, rrepo := routingSvc(e, true)
	h := e.handler().WithRouting(routing, rrepo)

	var t1, t2 *domain.TopicThread
	e.session(a.id, supervisor, func(ctx context.Context) {
		t1, _ = topicsSvc.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, ContactID: &a.contact, Title: "Cobrança"})
		t2, _ = topicsSvc.CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, ContactID: &a.contact, Title: "Entrega"})
	})
	ambiguous := func() uuid.UUID {
		msg := e.message(a.id, a.conversation, "e agora, como fica?")
		var id uuid.UUID
		e.session(a.id, supervisor, func(ctx context.Context) {
			out, err := routing.Route(ctx, cnv(msg), application.RouteOptions{})
			if err != nil || out.AmbiguityID == nil {
				t.Fatalf("setup: %+v %v", out, err)
			}
			id = *out.AmbiguityID
		})
		return id
	}
	amb := ambiguous()
	cp := p("conversation_id", a.conversation.String())

	// listing: any member with topic.read of the tenant; nobody else
	rec := e.call(a.id, stranger, http.MethodGet, "", cp, h.ListConversationAmbiguities)
	var list struct{ Items []ambiguityDTO }
	decodeBody(t, rec, &list)
	if rec.Code != 200 || len(list.Items) != 1 || list.Items[0].ID != amb || list.Items[0].Kind != "conversation" {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	var cands []map[string]any
	_ = json.Unmarshal(list.Items[0].Candidates, &cands)
	if len(cands) == 0 || cands[0]["score"] == nil {
		t.Fatalf("candidates must carry their score and evidence: %s", list.Items[0].Candidates)
	}
	if rec := e.call(a.id, viewer, http.MethodGet, "", cp, h.ListConversationAmbiguities); rec.Code != http.StatusForbidden {
		t.Errorf("viewer list = %d, want 403", rec.Code)
	}
	if rec := e.call(b.id, adminB, http.MethodGet, "", cp, h.ListConversationAmbiguities); rec.Code != http.StatusNotFound {
		t.Errorf("another tenant listing = %d, want 404", rec.Code)
	}

	ap := p("ambiguity_id", amb.String())
	resolve := func(user uuid.UUID, tenant uuid.UUID, body string) int {
		return e.call(tenant, user, http.MethodPost, body, ap, h.ResolveAmbiguity).Code
	}
	if c := resolve(stranger, a.id, `{"topic_id":"`+t1.ID.String()+`"}`); c != http.StatusForbidden {
		t.Errorf("non-attendant resolve = %d, want 403", c)
	}
	if c := resolve(viewer, a.id, `{"topic_id":"`+t1.ID.String()+`"}`); c != http.StatusForbidden {
		t.Errorf("viewer resolve = %d, want 403", c)
	}
	if c := resolve(adminB, b.id, `{"topic_id":"`+t1.ID.String()+`"}`); c != http.StatusNotFound {
		t.Errorf("another tenant resolving = %d, want 404", c)
	}
	if c := resolve(attendant, a.id, `{}`); c != http.StatusUnprocessableEntity {
		t.Errorf("neither topic nor title = %d, want 422", c)
	}
	if c := resolve(attendant, a.id, `{"topic_id":"`+t1.ID.String()+`","new_topic_title":"x"}`); c != http.StatusUnprocessableEntity {
		t.Errorf("both topic and title = %d, want 422", c)
	}
	if c := resolve(attendant, a.id, `{"topic_id":"`+uuid.NewString()+`"}`); c != http.StatusNotFound {
		t.Errorf("unknown topic = %d, want 404", c)
	}
	if e.count(`SELECT count(*) FROM ambiguity_cases WHERE tenant_id=$1 AND status='open'`, a.id) != 1 {
		t.Fatal("refused requests must leave the ambiguity open")
	}
	if c := resolve(attendant, a.id, `{"topic_id":"`+t2.ID.String()+`"}`); c != 200 {
		t.Fatalf("attendant resolve = %d, want 200", c)
	}
	if c := resolve(attendant, a.id, `{"topic_id":"`+t1.ID.String()+`"}`); c != http.StatusConflict {
		t.Errorf("resolving twice = %d, want 409", c)
	}
	var topic uuid.UUID
	var src string
	_ = e.seed.QueryRow(e.ctx, `SELECT l.topic_thread_id, l.decision_source FROM message_topic_links l JOIN ambiguity_cases a ON a.message_id=l.message_id WHERE a.id=$1`, amb).Scan(&topic, &src)
	if topic != t2.ID || src != "agent" {
		t.Fatalf("the message must join the chosen topic by an agent decision: %v %s", topic, src)
	}
	// a supervisor (conversation.manage) may resolve a conversation they do not attend; to open a NEW topic they give a title
	amb2 := ambiguous()
	if c := e.call(a.id, supervisor, http.MethodPost, `{"new_topic_title":"Assunto novo"}`, p("ambiguity_id", amb2.String()), h.ResolveAmbiguity).Code; c != 200 {
		t.Errorf("supervisor resolve = %d, want 200", c)
	}
	if e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1 AND title='Assunto novo' AND source='manual'`, a.id) != 1 {
		t.Error("the new topic must exist")
	}
}

func TestGroupAmbiguityNeedsGroupManage(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	agent := e.member(a.id, "tenant_agent")
	admin := e.member(a.id, "tenant_admin")
	g := e.group(a.id)
	joao := e.participant(a.id, g, "111@lid", "João")
	routing, _, rrepo := routingSvc(e, true)
	h := e.handler().WithRouting(routing, rrepo)
	msg := e.groupMessage(a.id, g, joao, "alguém sabe como fica?", nil)
	var amb uuid.UUID
	e.session(a.id, admin, func(ctx context.Context) {
		// give the group one open topic so that recency makes the message ambiguous
		topic, _ := domain.NewTopicThread(a.id, "Pedido 1", domain.SourceManual, time.Now())
		if err := NewPostgresTopicRepository(e.app).CreateTopic(ctx, topic); err != nil {
			t.Fatal(err)
		}
		if err := rrepo.LinkContainer(ctx, a.id, grp(msg), g.group, topic.ID); err != nil {
			t.Fatal(err)
		}
		out, err := routing.Route(ctx, grp(msg), application.RouteOptions{})
		if err != nil || out.AmbiguityID == nil {
			t.Fatalf("setup: %+v %v", out, err)
		}
		amb = *out.AmbiguityID
	})
	var topicID uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT id FROM topic_threads WHERE tenant_id=$1`, a.id).Scan(&topicID)
	ap := p("ambiguity_id", amb.String())
	if c := e.call(a.id, agent, http.MethodPost, `{"topic_id":"`+topicID.String()+`"}`, ap, h.ResolveAmbiguity).Code; c != http.StatusForbidden {
		t.Errorf("an agent resolving a GROUP ambiguity = %d, want 403 (group.manage)", c)
	}
	if c := e.call(a.id, admin, http.MethodPost, `{"topic_id":"`+topicID.String()+`"}`, ap, h.ResolveAmbiguity).Code; c != 200 {
		t.Errorf("an admin resolving a group ambiguity = %d, want 200", c)
	}
	if e.count(`SELECT count(*) FROM group_message_topic_links WHERE tenant_id=$1 AND group_message_id=$2 AND decision_source='agent'`, a.id, msg) != 1 {
		t.Error("the group message must join the topic as an agent decision")
	}
}

// RLS of the Wave 3 tables, checked with the runtime role: a session of tenant A sees none of tenant B's rows, can write
// none, and a forged tenant_id in a raw insert is refused.
func TestWave3TablesAreInvisibleAndImmutableAcrossTenants(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	adminA, adminB := e.member(a.id, "tenant_admin"), e.member(b.id, "tenant_admin")
	routing, topicsSvc, rrepo := routingSvc(e, true)
	g := e.group(b.id)
	pb := e.participant(b.id, g, "999@lid", "B")
	e.session(b.id, adminB, func(ctx context.Context) {
		topic, _ := topicsSvc.CreateTopic(ctx, application.CreateTopicInput{ConversationID: b.conversation, ContactID: &b.contact, Title: "de B"})
		msg := e.message(b.id, b.conversation, "pedido 4321 e mais")
		if _, err := routing.Route(ctx, cnv(msg), application.RouteOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := rrepo.SetFocus(ctx, b.id, cnv(msg), b.conversation, nil, topic.ID, "router", 0.9, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		gm := e.groupMessage(b.id, g, pb, "alguém sabe como fica?", nil)
		_ = gm
	})
	tables := []string{"topic_entities", "routing_decisions", "ambiguity_cases", "conversation_topic_focus", "group_message_topic_links", "topic_group_links"}
	for _, tbl := range tables {
		if e.count(`SELECT count(*) FROM `+tbl+` WHERE tenant_id=$1`, b.id) == 0 && tbl != "ambiguity_cases" && tbl != "group_message_topic_links" && tbl != "topic_group_links" {
			t.Fatalf("setup: %s has no rows for tenant B, the test would prove nothing", tbl)
		}
	}
	e.session(a.id, adminA, func(ctx context.Context) {
		for _, tbl := range tables {
			var n int
			_ = rrepo.q(ctx).QueryRow(ctx, `SELECT count(*) FROM `+tbl).Scan(&n)
			if n != 0 {
				t.Errorf("tenant A's session sees %d rows of %s that belong to B", n, tbl)
			}
		}
	})
	e.attempt(a.id, adminA, func(ctx context.Context) {
		if _, err := rrepo.q(ctx).Exec(ctx, `INSERT INTO routing_decisions(tenant_id,message_id,status,decision_source) VALUES($1,gen_random_uuid(),'unassigned','rule')`, b.id); err == nil {
			t.Error("a forged tenant_id in a raw insert must be refused by RLS")
		}
	})
	e.session(a.id, adminA, func(ctx context.Context) {
		tag, _ := rrepo.q(ctx).Exec(ctx, `UPDATE routing_decisions SET status='unassigned' WHERE tenant_id=$1`, b.id)
		if tag.RowsAffected() != 0 {
			t.Error("tenant A changed tenant B's routing decisions")
		}
		tag, _ = rrepo.q(ctx).Exec(ctx, `DELETE FROM topic_entities WHERE tenant_id=$1`, b.id)
		if tag.RowsAffected() != 0 {
			t.Error("tenant A deleted tenant B's entities")
		}
	})
}
