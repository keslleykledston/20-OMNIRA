package adapters

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	aiports "github.com/omnira/omnira/internal/ai/ports"
	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
)

// The final integrated scenarios of the master plan (A-J). They run the real services against real Postgres under RLS;
// only the AI provider is a fake. Where the plan's expectation depends on something that is deliberately NOT automatic
// (an AI proposal is never applied; a second concurrent ticket in one conversation is never opened by automation) the
// test asserts the safe behaviour and says so.

func autoRouting() application.Flags {
	f := application.DefaultFlags()
	f.TopicAutoRoutingEnabled = true
	return f
}

func (e *env) router(flags application.Flags) *application.RoutingService {
	return application.NewRoutingService(NewPostgresRoutingRepository(e.app), NewPostgresTopicRepository(e.app), flags, domain.DefaultRoutingConfig(), nil)
}

func (e *env) route(tenant, user uuid.UUID, svc *application.RoutingService, ref ports.MessageRef) *application.RoutingOutcome {
	var out *application.RoutingOutcome
	e.session(tenant, user, func(ctx context.Context) {
		var err error
		if out, err = svc.Route(ctx, ref, application.RouteOptions{}); err != nil {
			e.t.Fatalf("route: %v", err)
		}
	})
	return out
}

func (e *env) person(tenant uuid.UUID, g groupFixture, name string) uuid.UUID {
	id := uuid.New()
	e.exec(`INSERT INTO channel_participants(id,tenant_id,channel_connection_id,provider,external_participant_id,display_name) VALUES($1,$2,$3,'waha',$4,$5)`, id, tenant, g.conn, id.String(), name)
	return id
}

func (e *env) topicOfGroupMessage(tenant, msg uuid.UUID) (topic uuid.UUID) {
	if err := e.seed.QueryRow(e.ctx, `SELECT topic_thread_id FROM group_message_topic_links WHERE tenant_id=$1 AND group_message_id=$2 AND relation='primary'`, tenant, msg).Scan(&topic); err != nil {
		e.t.Fatalf("group message %s has no primary topic: %v", msg, err)
	}
	return topic
}

func (e *env) topicOfMessage(tenant, msg uuid.UUID) (topic uuid.UUID) {
	if err := e.seed.QueryRow(e.ctx, `SELECT topic_thread_id FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2 AND relation='primary'`, tenant, msg).Scan(&topic); err != nil {
		e.t.Fatalf("message %s has no primary topic: %v", msg, err)
	}
	return topic
}

func TestScenarioA_GroupMultiTopic(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	g := e.group(a.id)
	joao, maria, pedro := e.person(a.id, g, "João"), e.person(a.id, g, "Maria"), e.person(a.id, g, "Pedro")
	svc := e.router(autoRouting())
	m1 := e.groupMessage(a.id, g, joao, "pedido 837 não chegou", nil)
	m2 := e.groupMessage(a.id, g, maria, "a NF 992 está errada", nil)
	m3 := e.groupMessage(a.id, g, joao, "já passou quatro dias", nil)
	m4 := e.groupMessage(a.id, g, pedro, "meu acesso está bloqueado", nil)
	var last *application.RoutingOutcome
	for _, m := range []uuid.UUID{m1, m2, m3, m4} {
		last = e.route(a.id, admin, svc, grp(m))
	}
	// Pedro names nothing and is a different author than the topics' participants: the router does NOT guess, it asks a
	// person (the AI shadow may propose; it never applies). The person opens his topic.
	if last.Result.Status != domain.RoutingAmbiguous || last.AmbiguityID == nil {
		t.Fatalf("Pedro's message must wait for a person: %+v", last.Result)
	}
	e.session(a.id, admin, func(ctx context.Context) {
		if _, err := svc.ResolveAmbiguity(ctx, *last.AmbiguityID, nil, "Acesso bloqueado", domain.DecisionAgent); err != nil {
			t.Fatal(err)
		}
	})
	tA, tB, tC := e.topicOfGroupMessage(a.id, m1), e.topicOfGroupMessage(a.id, m2), e.topicOfGroupMessage(a.id, m4)
	if tA == tB || tA == tC || tB == tC {
		t.Fatalf("three subjects must be three topics: %v %v %v", tA, tB, tC)
	}
	if e.topicOfGroupMessage(a.id, m3) != tA {
		t.Fatal("João's follow-up (no entity) must stay in his topic")
	}
	has := func(topic uuid.UUID, typ, key string) bool {
		return e.count(`SELECT count(*) FROM topic_entities WHERE tenant_id=$1 AND topic_thread_id=$2 AND entity_type=$3 AND canonical_key=$4`, a.id, topic, typ, key) == 1
	}
	if !has(tA, "order", "837") || !has(tB, "invoice", "992") {
		t.Fatal("entities: order 837 in A, invoice 992 in B")
	}
	if e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1`, a.id) != 3 || e.count(`SELECT count(*) FROM ambiguity_cases WHERE tenant_id=$1 AND status='open'`, a.id) != 0 {
		t.Fatal("exactly 3 topics and no open ambiguity")
	}
	for _, topic := range []uuid.UUID{tA, tB, tC} {
		if e.count(`SELECT count(*) FROM topic_group_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND group_id=$3`, a.id, topic, g.group) != 1 {
			t.Error("every topic lives in the group")
		}
	}
}

func TestScenarioB_SameContactSwitchingTopics(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	damaged, _ := e.topicWith(a, admin, "Produto danificado", "o produto chegou quebrado", "a caixa estava amassada")
	refund, _ := e.topicWith(a, admin, "Estorno", "quero o estorno do valor", "cadê meu dinheiro de volta")
	mPhoto := e.message(a.id, a.conversation, "segue a foto")
	mRefund := e.message(a.id, a.conversation, "e o estorno?")
	mSerial := e.message(a.id, a.conversation, "o número de série é ABC123")
	expected := map[uuid.UUID]uuid.UUID{mPhoto: damaged.ID, mRefund: refund.ID, mSerial: damaged.ID}

	// the fake model reads the CONTENT and answers over aliases (it is told only titles and summaries)
	answer := map[string]string{"segue a foto": "Produto danificado", "e o estorno?": "Estorno", "o número de série é ABC123": "Produto danificado"}
	gen := genFunc(func(r aiports.GenerateRequest) string {
		var title string
		for k, v := range answer {
			if strings.Contains(r.Input, `[message customer] "`+k+`"`) {
				title = v
			}
		}
		for _, l := range strings.Split(r.Input, "\n") {
			if strings.HasPrefix(l, "[candidate T") && strings.Contains(l, title) && !strings.Contains(l, "entities") && !strings.Contains(l, "summary") {
				return `{"verdict":"existing","topic":"` + l[len("[candidate "):len("[candidate ")+2] + `","confidence":0.9,"reason":"semântico"}`
			}
		}
		return `{"verdict":"none","confidence":0.1}`
	})
	flags := autoRouting()
	flags.TopicAIRoutingEnabled = true
	svc, cls := e.router(flags), e.classifier(gen, flags, nil)
	for _, m := range []uuid.UUID{mPhoto, mRefund, mSerial} {
		out := e.route(a.id, admin, svc, cnv(m))
		// deterministic evidence alone cannot tell: it must hand the decision to a person, never guess
		if out.Result.Status == domain.RoutingAssigned && out.Applied {
			t.Fatalf("the deterministic router must not auto-assign a message with no hard evidence: %+v", out.Result)
		}
		e.session(a.id, admin, func(ctx context.Context) {
			if _, err := cls.ClassifyShadow(ctx, cnv(m)); err != nil {
				t.Fatal(err)
			}
		})
	}
	// the AI shadow proposed exactly what the scenario expects...
	for m, want := range expected {
		var sel *uuid.UUID
		var applied bool
		if err := e.seed.QueryRow(e.ctx, `SELECT selected_topic_thread_id, applied FROM routing_decisions WHERE tenant_id=$1 AND message_id=$2 AND decision_source='ai'`, a.id, m).Scan(&sel, &applied); err != nil {
			t.Fatalf("no AI proposal for %s: %v", m, err)
		}
		if sel == nil || *sel != want || applied {
			t.Errorf("AI proposal for %s = %v (applied=%v), want %s unapplied", m, sel, applied, want)
		}
		// ...but NOTHING was applied: the placement waits for a person
		if e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2`, a.id, m) != 0 {
			t.Errorf("message %s was linked by automation", m)
		}
	}
	// the person confirms what the AI suggested (ambiguity resolution is the human decision)
	for m, want := range expected {
		var amb uuid.UUID
		if err := e.seed.QueryRow(e.ctx, `SELECT id FROM ambiguity_cases WHERE tenant_id=$1 AND message_id=$2 AND status='open'`, a.id, m).Scan(&amb); err != nil {
			t.Fatalf("message %s should be waiting as an ambiguity: %v", m, err)
		}
		topic := want
		e.session(a.id, admin, func(ctx context.Context) {
			if _, err := svc.ResolveAmbiguity(ctx, amb, &topic, "", domain.DecisionAgent); err != nil {
				t.Fatal(err)
			}
		})
	}
	for m, want := range expected {
		if got := e.topicOfMessage(a.id, m); got != want {
			t.Errorf("final placement of %s = %s, want %s", m, got, want)
		}
	}
	// and the evaluation now measures the AI against the people: 3 of 3
	var ev *ports.Evaluation
	e.session(a.id, admin, func(ctx context.Context) {
		var err error
		if ev, err = NewPostgresEvaluationRepository(e.app).Report(ctx, a.id, 7); err != nil {
			t.Fatal(err)
		}
	})
	if ev.AIShadow.WithFinalPlacement != 3 || ev.AIShadow.Agreed != 3 {
		t.Fatalf("evaluation = %+v", ev.AIShadow)
	}
}

func TestScenarioC_DirectReplyInheritsTheTopic(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	topicA, idsA := e.topicWith(a, admin, "Pedido atrasado", "meu pedido 837 atrasou")
	_, _ = e.topicWith(a, admin, "Nota errada", "a nota veio errada")
	reply := e.message(a.id, a.conversation, "pode ser?")
	e.exec(`UPDATE messages SET reply_to_message_id=$3 WHERE tenant_id=$1 AND id=$2`, a.id, reply, idsA[0])
	out := e.route(a.id, admin, e.router(autoRouting()), cnv(reply))
	if !out.Applied || out.Result.Source != domain.DecisionReply || e.topicOfMessage(a.id, reply) != topicA.ID {
		t.Fatalf("a direct reply must inherit its target's topic by strong evidence: %+v", out.Result)
	}
}

func TestScenarioD_OneMessageTwoTopics(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	delivery, _ := e.topicWith(a, admin, "Entrega", "meu pedido 837 não chegou")
	billing, _ := e.topicWith(a, admin, "Cobrança", "a nota 992 veio com valor errado")
	e.exec(`INSERT INTO topic_entities(tenant_id,topic_thread_id,entity_type,canonical_key,source) VALUES($1,$2,'order','837','rule'),($1,$3,'invoice','992','rule')`, a.id, delivery.ID, billing.ID)
	m := e.message(a.id, a.conversation, "o pedido 837 atrasou e a nota 992 veio errada")
	out := e.route(a.id, admin, e.router(autoRouting()), cnv(m))
	if out.Result.Status != domain.RoutingMultiTopic || e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2 AND topic_thread_id IN ($3,$4)`, a.id, m, delivery.ID, billing.ID) != 2 {
		t.Fatalf("one message naming two subjects must have 2 topic links: %+v", out.Result)
	}
}

func TestScenarioE_HandoffFromGroupToPrivateChat(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	g := e.group(a.id)
	joao := e.person(a.id, g, "João")
	gm := e.groupMessage(a.id, g, joao, "o produto veio com defeito", nil)
	flags := autoRouting()
	flags.PrivateHandoffEnabled, flags.TopicSummariesEnabled = true, true
	// an entity-less first message opens no topic by itself: the agent opens it from the group message
	topic := e.groupTopicWith(a, admin, g, "Produto com defeito", gm)

	// agent invites the customer to the private chat
	hs := e.handoffSvc(flags, nil)
	var token string
	e.session(a.id, admin, func(ctx context.Context) {
		_, tok, err := hs.Create(ctx, topic.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		token = tok
	})
	// the customer pastes it in the private chat: the correct topic is restored through the real pipeline
	priv := e.message(a.id, a.conversation, token)
	pipeline := application.HandoffPipeline{Next: application.RoutingPipeline{Routing: e.router(flags)}, Handoffs: hs}
	store := NewPostgresJobStore(e.app)
	_, _ = store.EnsureFromEvent(e.ctx, cnv(priv), application.PipelineVersion)
	if n, err := application.NewJobRunner(store, pipeline, e.session2(a.id), fastConfig(), nil).ProcessOnce(e.ctx); err != nil || n != 1 {
		t.Fatalf("pipeline: %d %v", n, err)
	}
	if e.topicOfMessage(a.id, priv) != topic.ID || e.count(`SELECT count(*) FROM topic_conversation_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND conversation_id=$3`, a.id, topic.ID, a.conversation) != 1 {
		t.Fatal("the private conversation must be bound to the group's topic")
	}
	// the summary is shown...
	sum := summarySvc(e, &fakeGen{out: "Assunto\nProduto com defeito"}, flags)
	e.session(a.id, admin, func(ctx context.Context) {
		if s, created, err := sum.Generate(ctx, topic.ID); err != nil || !created || s.Version != 1 {
			t.Fatalf("summary: %v %v", created, err)
		}
		list, err := sum.List(ctx, topic.ID)
		if err != nil || len(list) != 1 || !strings.Contains(list[0].SummaryText, "defeito") {
			t.Fatalf("summaries: %+v %v", list, err)
		}
		// ...the customer confirms it...
		if s, err := sum.Confirm(ctx, topic.ID, domain.DecisionCustomer); err != nil || s.Status != domain.SummaryCustomerConfirmed {
			t.Fatalf("customer confirmation: %+v %v", s, err)
		}
	})
	// ...and the ticket is linked
	tk := e.ticket(a.id, a.conversation, "open")
	ts := e.ticketSvc(flags)
	e.session(a.id, admin, func(ctx context.Context) {
		res, err := ts.Apply(ctx, topic.ID, domain.TicketActionAdoptActive, domain.TicketLinkAgent)
		if err != nil || res.TicketID != tk {
			t.Fatalf("ticket link: %+v %v", res, err)
		}
	})
	if e.count(`SELECT count(*) FROM topic_ticket_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND ticket_id=$3 AND relation='primary'`, a.id, topic.ID, tk) != 1 {
		t.Fatal("ticket not linked")
	}
}

func TestScenarioF_SameContactTwoTicketsDoNotMix(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	conv2 := uuid.New()
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, conv2, a.id, a.contact) // the same contact, another conversation
	t813, t829 := e.ticket(a.id, a.conversation, "open"), e.ticket(a.id, conv2, "open")
	e.exec(`UPDATE tickets SET provider='k3g', external_ticket_id='813' WHERE id=$1`, t813)
	e.exec(`UPDATE tickets SET provider='k3g', external_ticket_id='829' WHERE id=$1`, t829)
	topicA, _ := e.topicWith(a, admin, "Assunto A", "primeiro assunto")
	conv2Topic := func() *domain.TopicThread {
		m := e.message(a.id, conv2, "segundo assunto")
		var tp *domain.TopicThread
		e.session(a.id, admin, func(ctx context.Context) {
			var err error
			tp, err = application.NewTopicService(NewPostgresTopicRepository(e.app)).CreateTopic(ctx, application.CreateTopicInput{ConversationID: conv2, ContactID: &a.contact, Title: "Assunto B", MessageIDs: []uuid.UUID{m}})
			if err != nil {
				t.Fatal(err)
			}
		})
		return tp
	}()
	ts := e.ticketSvc(application.DefaultFlags())
	e.session(a.id, admin, func(ctx context.Context) {
		if _, err := ts.Apply(ctx, topicA.ID, domain.TicketActionAdoptActive, domain.TicketLinkAgent); err != nil {
			t.Fatal(err)
		}
		if _, err := ts.Apply(ctx, conv2Topic.ID, domain.TicketActionAdoptActive, domain.TicketLinkAgent); err != nil {
			t.Fatal(err)
		}
	})
	svc := e.router(autoRouting())
	m813 := e.message(a.id, a.conversation, "alguma novidade do chamado 813?")
	m829 := e.message(a.id, conv2, "e o ticket 829, como está?")
	out1, out2 := e.route(a.id, admin, svc, cnv(m813)), e.route(a.id, admin, svc, cnv(m829))
	if !out1.Applied || e.topicOfMessage(a.id, m813) != topicA.ID || !out2.Applied || e.topicOfMessage(a.id, m829) != conv2Topic.ID {
		t.Fatalf("each message must follow ITS ticket: %+v / %+v", out1.Result, out2.Result)
	}
	// the invariant: a second concurrent ticket in ONE conversation is a person's decision, never automation's
	third, _ := e.topicWith(a, admin, "Assunto C", "terceiro assunto")
	e.attempt(a.id, admin, func(ctx context.Context) {
		adv, _ := ts.Advise(ctx, third.ID)
		if adv.Action != domain.TicketActionNeedsAgent {
			t.Errorf("a second ticket in the same conversation must need a person: %+v", adv)
		}
	})
	if e.activeTickets(a.id, a.conversation) != 1 {
		t.Fatal("only one active ticket per conversation")
	}
}

func TestScenarioG_OmnichannelEntityLinkingStaysWithinTheSameCustomer(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	topicA, _ := e.topicWith(a, admin, "Pedido 837", "meu pedido 837 não chegou")
	e.exec(`INSERT INTO topic_entities(tenant_id,topic_thread_id,entity_type,canonical_key,source) VALUES($1,$2,'order','837','rule')`, a.id, topicA.ID)
	e.exec(`UPDATE topic_threads SET primary_contact_id=$2 WHERE id=$1`, topicA.ID, a.contact)
	svc := e.router(autoRouting())

	// the SAME customer writes by e-mail (another conversation of the same contact) about the same order: linked
	emailConv := uuid.New()
	e.exec(`INSERT INTO conversations(id,tenant_id,contact_id,status) VALUES($1,$2,$3,'open')`, emailConv, a.id, a.contact)
	mail := e.message(a.id, emailConv, "Bom dia, sobre o pedido 837: ainda não recebi")
	out := e.route(a.id, admin, svc, cnv(mail))
	if !out.Applied || out.Result.Source != domain.DecisionEntity || e.topicOfMessage(a.id, mail) != topicA.ID {
		t.Fatalf("e-mail naming the same order by the same customer must link to the topic: %+v", out.Result)
	}
	if e.count(`SELECT count(*) FROM topic_conversation_links WHERE tenant_id=$1 AND topic_thread_id=$2`, a.id, topicA.ID) != 2 {
		t.Fatal("the topic must now span both channels")
	}

	// ANOTHER customer who happens to write "pedido 837" must NOT be attached to that topic (isolation inside the tenant)
	otherConv := e.conversationFor(a.id, nil)
	stranger := e.message(a.id, otherConv, "meu pedido 837 também atrasou")
	out2 := e.route(a.id, admin, svc, cnv(stranger))
	if linked := e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2 AND topic_thread_id=$3`, a.id, stranger, topicA.ID); linked != 0 {
		t.Fatalf("another customer's message was attached to this customer's topic: %+v", out2.Result)
	}
}

func TestScenarioH_AIUnavailableNothingElseBreaks(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	attendant := e.member(a.id, "tenant_agent")
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, attendant)
	tk := e.ticket(a.id, a.conversation, "open")
	flags := autoRouting()
	flags.TopicAIRoutingEnabled, flags.TopicSummariesEnabled, flags.CopilotEnabled, flags.AIToolGatewayEnabled = true, true, true, true
	down := errGen{context.DeadlineExceeded} // every provider call times out
	routing := e.router(flags)
	var pipeline application.Pipeline = application.RoutingPipeline{Routing: routing}
	pipeline = application.ShadowPipeline{Next: pipeline, Classifier: e.classifier(down, flags, nil)}
	sum := summarySvc(e, &fakeGen{err: context.DeadlineExceeded}, flags)
	sum.FirstSummaryAt = 1
	pipeline = application.SummaryPipeline{Next: pipeline, Summaries: sum}
	store := NewPostgresJobStore(e.app)
	runner := application.NewJobRunner(store, pipeline, e.session2(a.id), fastConfig(), nil)

	m := e.message(a.id, a.conversation, "meu pedido 837 não chegou")
	_, _ = store.EnsureFromEvent(e.ctx, cnv(m), application.PipelineVersion)
	if n, err := runner.ProcessOnce(e.ctx); err != nil || n != 1 {
		t.Fatalf("pipeline with the AI down: %d %v", n, err)
	}
	if e.count(`SELECT count(*) FROM intelligence_jobs WHERE tenant_id=$1 AND state='completed'`, a.id) != 1 || e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2`, a.id, m) != 1 {
		t.Fatal("messages must still enter and be routed deterministically")
	}
	// agents keep working: manual topic, manual ticket link, normal reply path untouched
	var manual *domain.TopicThread
	e.session(a.id, attendant, func(ctx context.Context) {
		var err error
		manual, err = application.NewTopicService(NewPostgresTopicRepository(e.app)).CreateTopic(ctx, application.CreateTopicInput{ConversationID: a.conversation, Title: "Criado à mão"})
		if err != nil {
			t.Fatal(err)
		}
	})
	e.session(a.id, attendant, func(ctx context.Context) {
		if err := application.NewTopicService(NewPostgresTopicRepository(e.app)).LinkTicket(ctx, manual.ID, tk, domain.TicketPrimary); err != nil {
			t.Fatalf("manual ticket link: %v", err)
		}
	})
	// the AI features say "unavailable" instead of failing the system
	e.attempt(a.id, attendant, func(ctx context.Context) {
		if _, _, err := sum.Generate(ctx, manual.ID); err == nil {
			t.Error("summary with a dead provider must report unavailable")
		}
	})
	h := e.handler().WithCopilot(e.copilot(down, flags))
	if rec := e.call(a.id, attendant, http.MethodPost, ``, p("topic_id", manual.ID.String()), h.SuggestReply); rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusServiceUnavailable {
		t.Errorf("copilot with the AI down = %d", rec.Code)
	}
	// read tools of the gateway need no AI at all
	th := e.toolHandler(flags)
	if code, out := invoke(e, th, a.id, attendant, manual.ID, "topic.list_tickets", "", "h-read-ticket-1", ""); code != 200 || out.Status != "executed" {
		t.Errorf("gateway read with the AI down = %d %+v", code, out)
	}
	_ = admin
}

func TestScenarioI_DuplicateEventsCreateNoDuplicates(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	g := e.group(a.id)
	joao := e.person(a.id, g, "João")
	flags := autoRouting()
	flags.PrivateHandoffEnabled, flags.TopicSummariesEnabled, flags.AutoTicketPolicyEnabled = true, true, true
	sum := summarySvc(e, &fakeGen{out: "resumo"}, flags)
	sum.FirstSummaryAt = 1
	tickets := e.ticketSvc(flags)
	hs := e.handoffSvc(flags, nil)
	var pipeline application.Pipeline = application.RoutingPipeline{Routing: e.router(flags)}
	pipeline = application.SummaryPipeline{Next: pipeline, Summaries: sum}
	pipeline = application.TicketPolicyPipeline{Next: pipeline, Tickets: tickets}
	pipeline = application.HandoffPipeline{Next: pipeline, Handoffs: hs}
	store := NewPostgresJobStore(e.app)

	// 1) a group message delivered 5 times, processed by 3 racing workers
	gm := e.groupMessage(a.id, g, joao, "o pedido 837 não chegou", nil)
	for i := 0; i < 5; i++ {
		_, _ = store.EnsureFromEvent(e.ctx, grp(gm), application.PipelineVersion)
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = application.NewJobRunner(store, pipeline, e.session2(a.id), fastConfig(), nil).ProcessOnce(e.ctx)
		}()
	}
	wg.Wait()
	// 2) the same private-chat handoff message delivered twice
	topic := e.topicOfGroupMessage(a.id, gm)
	var token string
	e.session(a.id, admin, func(ctx context.Context) {
		_, tok, err := hs.Create(ctx, topic, 0)
		if err != nil {
			t.Fatal(err)
		}
		token = tok
	})
	tk := e.ticket(a.id, a.conversation, "open")
	_ = tk
	priv := e.message(a.id, a.conversation, token)
	for i := 0; i < 3; i++ {
		_, _ = store.EnsureFromEvent(e.ctx, cnv(priv), application.PipelineVersion)
	}
	for i := 0; i < 3; i++ {
		_, _ = application.NewJobRunner(store, pipeline, e.session2(a.id), fastConfig(), nil).ProcessOnce(e.ctx)
	}
	// 3) replay of every step by hand (first settle the summary of the topic's current state, then replay)
	e.session(a.id, admin, func(ctx context.Context) { _, _, _ = sum.Generate(ctx, topic) })
	for i := 0; i < 3; i++ {
		e.session(a.id, admin, func(ctx context.Context) {
			_, _ = hs.TryRedeem(ctx, cnv(priv))
			_, _, _ = sum.Generate(ctx, topic)
			tickets.AutoForMessage(ctx, grp(gm))
			_, _ = e.router(flags).Route(ctx, grp(gm), application.RouteOptions{})
		})
	}
	for what, q := range map[string]string{
		"topics":               `SELECT count(*) FROM topic_threads WHERE tenant_id=$1`,
		"decisions":            `SELECT count(*) FROM routing_decisions WHERE tenant_id=$1 AND applied`,
		"jobs":                 `SELECT count(*) FROM intelligence_jobs WHERE tenant_id=$1`,
		"group message links":  `SELECT count(*) FROM group_message_topic_links WHERE tenant_id=$1`,
		"message links":        `SELECT count(*) FROM message_topic_links WHERE tenant_id=$1`,
		"redeemed handoffs":    `SELECT count(*) FROM topic_handoffs WHERE tenant_id=$1 AND status='redeemed'`,
		"summaries":            `SELECT count(*) FROM topic_summaries WHERE tenant_id=$1`,
		"primary ticket links": `SELECT count(*) FROM topic_ticket_links WHERE tenant_id=$1 AND relation='primary'`,
		"active tickets":       `SELECT count(*) FROM tickets WHERE tenant_id=$1 AND status IN ('open','in_progress','waiting')`,
	} {
		n := e.count(q, a.id)
		want := 1
		if what == "decisions" || what == "jobs" {
			want = 2 // one for the group message, one for the private handoff message
		}
		if what == "summaries" {
			want = 2 // one per STATE of the topic (before and after the handoff added a message), never per delivery
		}
		if what == "message links" || what == "group message links" {
			want = 1
		}
		if n != want {
			t.Errorf("%s = %d after duplicate delivery, want %d", what, n, want)
		}
	}
}

func TestScenarioJ_CrossTenantAttack(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	adminA, adminB := e.member(a.id, "tenant_admin"), e.member(b.id, "tenant_admin")
	flags := autoRouting()
	flags.PrivateHandoffEnabled, flags.AIToolGatewayEnabled, flags.CopilotEnabled = true, true, true
	// Tenant B's assets
	gB := e.group(b.id)
	partB := e.person(b.id, gB, "Participante B")
	topicB, msgsB := e.topicWith(b, adminB, "Assunto de B", "mensagem confidencial de B")
	ticketB := e.ticket(b.id, b.conversation, "open")
	gmB := e.groupMessage(b.id, gB, partB, "mensagem de grupo de B", nil)
	var tokenB string
	e.session(b.id, adminB, func(ctx context.Context) {
		_, tok, err := e.handoffSvc(flags, nil).Create(ctx, topicB.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		tokenB = tok
	})
	topicA, _ := e.topicWith(a, adminA, "Assunto de A", "mensagem de A")
	svc := e.router(flags)
	h := e.handler().WithSummaries(summarySvc(e, &fakeGen{out: "x"}, flags)).WithHandoffs(e.handoffSvc(flags, nil)).WithTickets(e.ticketSvc(flags)).
		WithRestructure(application.NewRestructureService(NewPostgresTopicRepository(e.app), NewPostgresRestructureRepository(e.app), NewPostgresRoutingRepository(e.app))).WithCopilot(e.copilot(&fakeGen{out: goodDraft}, flags))
	h = h.WithTools(application.NewToolGateway(NewPostgresToolCallRepository(e.app), NewPostgresTopicRepository(e.app), h.ToolAuthorizer(),
		application.NewToolExecutors(application.NewTopicService(NewPostgresTopicRepository(e.app)), summarySvc(e, &fakeGen{out: "x"}, flags), e.ticketSvc(flags)), flags))
	tb := p("topic_id", topicB.ID.String())

	// TOPIC: every topic endpoint answers 404 for B's id when called as A
	for name, fn := range map[string]http.HandlerFunc{
		"get": h.GetTopic, "summaries": h.ListSummaries, "handoffs": h.ListHandoffs, "ticket policy": h.GetTicketPolicy, "tools": h.ListAITools, "tool calls": h.ListAIToolCalls,
	} {
		if rec := e.call(a.id, adminA, http.MethodGet, "", tb, fn); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s of B's topic as A = %d, want 404", name, rec.Code)
		}
	}
	for name, fn := range map[string]http.HandlerFunc{"generate summary": h.GenerateSummary, "create handoff": h.CreateHandoff, "suggest reply": h.SuggestReply, "apply policy": h.ApplyTicketPolicy} {
		if rec := e.call(a.id, adminA, http.MethodPost, `{"action":"create"}`, tb, fn); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s on B's topic as A = %d, want 404", name, rec.Code)
		}
	}
	if rec := e.call(a.id, adminA, http.MethodPost, mergeBody(topicB.ID), p("topic_id", topicA.ID.String()), h.MergeTopic); rec.Code != http.StatusNotFound {
		t.Errorf("merge into B's topic = %d, want 404", rec.Code)
	}
	// MESSAGE: routing B's message under A's session finds nothing; linking it to A's topic is refused
	e.attempt(a.id, adminA, func(ctx context.Context) {
		if _, err := svc.Route(ctx, cnv(msgsB[0]), application.RouteOptions{}); err == nil {
			t.Error("A routed B's message")
		}
		if _, err := svc.Route(ctx, grp(gmB), application.RouteOptions{}); err == nil {
			t.Error("A routed B's group message")
		}
	})
	e.attempt(a.id, adminA, func(ctx context.Context) {
		if err := application.NewTopicService(NewPostgresTopicRepository(e.app)).LinkMessage(ctx, topicA.ID, msgsB[0], domain.RelationPrimary); err == nil {
			t.Error("A linked B's message to its own topic")
		}
	})
	// TICKET: B's ticket cannot be linked to A's topic
	e.attempt(a.id, adminA, func(ctx context.Context) {
		if err := application.NewTopicService(NewPostgresTopicRepository(e.app)).LinkTicket(ctx, topicA.ID, ticketB, domain.TicketRelated); err == nil {
			t.Error("A linked B's ticket")
		}
	})
	// PARTICIPANT: B's participant cannot be attached to A's conversation
	_, err := e.seed.Exec(e.ctx, `INSERT INTO conversation_channel_participants(tenant_id,conversation_id,channel_participant_id) VALUES($1,$2,$3)`, a.id, a.conversation, partB)
	if err == nil {
		t.Error("A's conversation accepted B's participant (the composite FK must refuse it)")
	}
	// HANDOFF: B's token typed in A's private chat redeems nothing and does not burn B's invitation
	msg := e.message(a.id, a.conversation, tokenB)
	e.session(a.id, adminA, func(ctx context.Context) {
		if ok, err := e.handoffSvc(flags, nil).TryRedeem(ctx, cnv(msg)); err != nil || ok {
			t.Errorf("B's token redeemed in A: %v %v", ok, err)
		}
	})
	var st string
	_ = e.seed.QueryRow(e.ctx, `SELECT status FROM topic_handoffs WHERE tenant_id=$1 AND topic_thread_id=$2`, b.id, topicB.ID).Scan(&st)
	if st != "pending" {
		t.Errorf("B's invitation = %s, must still be pending", st)
	}
	// nothing of B moved, and no response above carried B's text
	if e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND topic_thread_id=$2`, b.id, topicB.ID) != 1 {
		t.Error("B's data changed")
	}
	rec := e.call(a.id, adminA, http.MethodGet, "", p("topic_id", topicA.ID.String()), h.GetTopic)
	if strings.Contains(rec.Body.String(), "confidencial") {
		t.Error("B's content in A's response")
	}
}
