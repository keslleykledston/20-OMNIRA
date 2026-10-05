package adapters

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"

	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/domain"
)

func (e *env) ticketSvc(flags application.Flags) *application.TopicTicketService {
	topics := NewPostgresTopicRepository(e.app)
	return application.NewTopicTicketService(topics, NewPostgresTicketPolicyRepository(e.app), NewPostgresRoutingRepository(e.app),
		inboxadapters.TicketStore{PostgresInboundStore: inboxadapters.NewPostgresInboundStore(e.app)}, application.NewTopicService(topics), flags)
}

func (e *env) activeTickets(tenant, conv uuid.UUID) int {
	return e.count(`SELECT count(*) FROM tickets WHERE tenant_id=$1 AND conversation_id=$2 AND status IN ('open','in_progress','waiting')`, tenant, conv)
}

func TestTicketPolicyNeverOpensASecondTicketOnItsOwnButAPersonMayChooseTo(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	placeholder := e.ticket(a.id, a.conversation, "open")
	first, _ := e.topicWith(a, admin, "Pedido atrasado", "pedido 837 atrasado")
	second, _ := e.topicWith(a, admin, "Nota errada", "nota 992 errada")
	svc := e.ticketSvc(application.DefaultFlags())

	e.session(a.id, admin, func(ctx context.Context) {
		adv, err := svc.Advise(ctx, first.ID)
		if err != nil || adv.Action != domain.TicketActionAdoptActive || adv.TicketID == nil || *adv.TicketID != placeholder {
			t.Fatalf("advice: %+v %v", adv, err)
		}
		res, err := svc.Apply(ctx, first.ID, domain.TicketActionAdoptActive, domain.TicketLinkAgent)
		if err != nil || res.TicketID != placeholder || res.Relation != domain.TicketPrimary || res.Created {
			t.Fatalf("adopt: %+v %v", res, err)
		}
	})
	// the second subject is NOT advised a ticket automatically, and cannot steal the first's, but a person may choose
	// to relate it or to open its own
	e.attempt(a.id, admin, func(ctx context.Context) {
		adv, _ := svc.Advise(ctx, second.ID)
		if adv.Action != domain.TicketActionNeedsAgent || !adv.CanApply(domain.TicketActionShareActive) || !adv.CanApply(domain.TicketActionCreate) || adv.CanApply(domain.TicketActionAdoptActive) {
			t.Errorf("second topic advice: %+v", adv)
		}
		if _, err := svc.Apply(ctx, second.ID, domain.TicketActionAdoptActive, domain.TicketLinkAgent); err == nil {
			t.Error("adopting a ticket that belongs to another subject must be refused")
		}
	})
	if e.activeTickets(a.id, a.conversation) != 1 {
		t.Fatal("nothing may have opened a ticket yet")
	}
	e.session(a.id, admin, func(ctx context.Context) {
		res, err := svc.Apply(ctx, second.ID, domain.TicketActionShareActive, domain.TicketLinkAgent)
		if err != nil || res.Relation != domain.TicketRelated || res.TicketID != placeholder {
			t.Fatalf("share: %+v %v", res, err)
		}
	})
	if e.count(`SELECT count(*) FROM topic_ticket_links WHERE tenant_id=$1 AND ticket_id=$2 AND relation='primary'`, a.id, placeholder) != 1 ||
		e.count(`SELECT count(*) FROM topic_ticket_links WHERE tenant_id=$1 AND ticket_id=$2 AND relation='related'`, a.id, placeholder) != 1 {
		t.Fatal("the ticket must keep ONE primary topic and share with the other as related")
	}
	// replay: a finished decision is not applied twice
	e.attempt(a.id, admin, func(ctx context.Context) {
		if _, err := svc.Apply(ctx, first.ID, domain.TicketActionAdoptActive, domain.TicketLinkAgent); err == nil {
			t.Error("applying again must be refused (the topic already has its primary ticket)")
		}
	})
}

func TestTicketPolicyOpensOneTicketPerSubjectEvenWhenAttemptsRace(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	e.ticket(a.id, a.conversation, "resolved") // history only: nothing active
	one, _ := e.topicWith(a, admin, "Troca de plano", "quero trocar de plano")
	two, _ := e.topicWith(a, admin, "Mudança de endereço", "mudei de endereço")
	svc := e.ticketSvc(application.DefaultFlags())
	var wg sync.WaitGroup
	for _, id := range []uuid.UUID{one.ID, two.ID, one.ID, two.ID, one.ID, two.ID} {
		wg.Add(1)
		go func(id uuid.UUID) {
			defer wg.Done()
			e.attempt(a.id, admin, func(ctx context.Context) {
				_, _ = svc.Apply(ctx, id, domain.TicketActionCreate, domain.TicketLinkAgent)
			})
		}(id)
	}
	wg.Wait()
	// each subject ends with exactly ONE ticket of its own: the first created is the conversation's ticket, the other is
	// scoped to its subject; six racing attempts never produce a third or a second conversation ticket
	if n := e.activeTickets(a.id, a.conversation); n != 2 {
		t.Fatalf("concurrent policy actions left %d active tickets, want exactly 2 (one per subject)", n)
	}
	if e.count(`SELECT count(*) FROM tickets WHERE tenant_id=$1 AND status='open' AND NOT topic_scoped`, a.id) != 1 || e.count(`SELECT count(*) FROM tickets WHERE tenant_id=$1 AND status='open' AND topic_scoped`, a.id) != 1 {
		t.Fatal("exactly one conversation ticket and one subject ticket")
	}
	if e.count(`SELECT count(*) FROM topic_ticket_links WHERE tenant_id=$1 AND relation='primary'`, a.id) != 2 ||
		e.count(`SELECT count(DISTINCT topic_thread_id) FROM topic_ticket_links WHERE tenant_id=$1 AND relation='primary'`, a.id) != 2 {
		t.Fatal("each topic owns exactly one primary ticket")
	}
	rows, _ := e.seed.Query(e.ctx, `SELECT t.subject FROM tickets t WHERE t.tenant_id=$1 AND t.status='open'`, a.id)
	titles := map[string]bool{}
	for rows.Next() {
		var sub string
		_ = rows.Scan(&sub)
		titles[sub] = true
	}
	rows.Close()
	if !titles["Troca de plano"] || !titles["Mudança de endereço"] {
		t.Fatalf("each ticket subject should be its topic's title, got %v", titles)
	}
}

func TestLegacyBackfillIsOnDemandIdempotentAndNeverClassifiesHistory(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	tk := e.ticket(a.id, a.conversation, "open")
	_ = e.message(a.id, a.conversation, "mensagem antiga 1")
	_ = e.message(a.id, a.conversation, "mensagem antiga 2")
	svc := e.ticketSvc(application.DefaultFlags())
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.attempt(a.id, admin, func(ctx context.Context) { _, _, _ = svc.BackfillLegacy(ctx, a.conversation, &a.contact) })
		}()
	}
	wg.Wait()
	if n := e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1 AND source='legacy_backfill'`, a.id); n != 1 {
		t.Fatalf("backfill created %d topics, want 1", n)
	}
	var origin, relation string
	if err := e.seed.QueryRow(e.ctx, `SELECT created_by, relation FROM topic_ticket_links WHERE tenant_id=$1 AND ticket_id=$2`, a.id, tk).Scan(&origin, &relation); err != nil || origin != "legacy_backfill" || relation != "primary" {
		t.Fatalf("legacy link = %s %s %v", origin, relation, err)
	}
	if e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1`, a.id) != 0 {
		t.Fatal("the backfill must not classify historical messages")
	}
	e.session(a.id, admin, func(ctx context.Context) {
		if _, created, err := svc.BackfillLegacy(ctx, a.conversation, &a.contact); err != nil || created {
			t.Fatalf("second run: created=%v err=%v", created, err)
		}
	})
	// a conversation with no active ticket has nothing to backfill
	other := e.conversationFor(a.id, nil)
	e.session(a.id, admin, func(ctx context.Context) {
		if _, created, err := svc.BackfillLegacy(ctx, other, nil); err != nil || created {
			t.Fatalf("no ticket: created=%v err=%v", created, err)
		}
	})
}

func TestAutomaticTicketPolicyIsOffByDefaultAndOnlyDoesTheUnambiguousThings(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	placeholder := e.ticket(a.id, a.conversation, "open")
	one, ids1 := e.topicWith(a, admin, "Primeiro", "primeiro assunto")
	two, _ := e.topicWith(a, admin, "Segundo", "segundo assunto")
	off := e.ticketSvc(application.DefaultFlags())
	e.session(a.id, admin, func(ctx context.Context) { off.AutoForMessage(ctx, cnv(ids1[0])) })
	if e.count(`SELECT count(*) FROM topic_ticket_links WHERE tenant_id=$1`, a.id) != 0 {
		t.Fatal("the automation acted with the flag off")
	}
	on := application.DefaultFlags()
	on.AutoTicketPolicyEnabled = true
	svc := e.ticketSvc(on)
	e.session(a.id, admin, func(ctx context.Context) {
		svc.AutoForMessage(ctx, cnv(ids1[0])) // adopts the unowned placeholder
		svc.AutoForMessage(ctx, cnv(ids1[0])) // replay: nothing more
	})
	var primaryTopic uuid.UUID
	var origin string
	_ = e.seed.QueryRow(e.ctx, `SELECT topic_thread_id, created_by FROM topic_ticket_links WHERE tenant_id=$1 AND ticket_id=$2 AND relation='primary'`, a.id, placeholder).Scan(&primaryTopic, &origin)
	if primaryTopic != one.ID || origin != "rule" {
		t.Fatalf("auto adoption: %v by %s", primaryTopic, origin)
	}
	// the second subject needs a person: the automation neither creates a ticket nor shares
	m2 := e.message(a.id, a.conversation, "ainda o segundo assunto")
	e.session(a.id, admin, func(ctx context.Context) {
		if err := application.NewTopicService(NewPostgresTopicRepository(e.app)).LinkMessage(ctx, two.ID, m2, domain.RelationPrimary); err != nil {
			t.Fatal(err)
		}
		svc.AutoForMessage(ctx, cnv(m2))
	})
	if e.activeTickets(a.id, a.conversation) != 1 || e.count(`SELECT count(*) FROM topic_ticket_links WHERE tenant_id=$1`, a.id) != 1 {
		t.Fatal("the automation must leave the ambiguous second topic to a person")
	}
}

func TestTicketPolicyForAGroupOnlyTopicNeedsAPerson(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	g := e.group(a.id)
	author := uuid.New()
	e.exec(`INSERT INTO channel_participants(id,tenant_id,channel_connection_id,provider,external_participant_id,display_name) VALUES($1,$2,$3,'waha','p1','P')`, author, a.id, g.conn)
	m := e.groupMessage(a.id, g, author, "o link caiu", nil)
	svc := e.ticketSvc(application.DefaultFlags())
	e.session(a.id, admin, func(ctx context.Context) {
		topic, err := application.NewTopicService(NewPostgresTopicRepository(e.app)).CreateTopic(ctx, application.CreateTopicInput{Title: "Link fora"})
		if err != nil {
			t.Fatal(err)
		}
		rr := NewPostgresRoutingRepository(e.app)
		if err := rr.LinkMessage(ctx, a.id, grp(m), topic.ID, domain.RelationPrimary, domain.DecisionAgent, nil, nil); err != nil {
			t.Fatal(err)
		}
		if err := rr.LinkContainer(ctx, a.id, grp(m), g.group, topic.ID); err != nil {
			t.Fatal(err)
		}
		if adv, err := svc.Advise(ctx, topic.ID); err != nil || adv.Action != domain.TicketActionNeedsAgent {
			t.Fatalf("group-only advice: %+v %v", adv, err)
		}
	})
}

func TestTicketPolicyAPIAuthorization(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	attendant := e.member(a.id, "tenant_agent")
	stranger := e.member(a.id, "tenant_agent")
	viewer := e.readOnlyMember(a.id)
	adminB := e.member(b.id, "tenant_admin")
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, attendant)
	e.ticket(a.id, a.conversation, "open")
	h := e.handler().WithTickets(e.ticketSvc(application.DefaultFlags()))
	topic, _ := e.topicWith(a, attendant, "Pedido", "um")
	tp := p("topic_id", topic.ID.String())
	cp := p("conversation_id", a.conversation.String())

	if rec := e.call(a.id, viewer, http.MethodGet, "", tp, h.GetTicketPolicy); rec.Code != http.StatusForbidden {
		t.Errorf("viewer advice = %d", rec.Code)
	}
	if rec := e.call(b.id, adminB, http.MethodGet, "", tp, h.GetTicketPolicy); rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant advice = %d, want 404", rec.Code)
	}
	body := `{"action":"adopt_active"}`
	if rec := e.call(a.id, stranger, http.MethodPost, body, tp, h.ApplyTicketPolicy); rec.Code != http.StatusForbidden {
		t.Errorf("non-attendant apply = %d, want 403", rec.Code)
	}
	if rec := e.call(b.id, adminB, http.MethodPost, body, tp, h.ApplyTicketPolicy); rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant apply = %d, want 404", rec.Code)
	}
	if rec := e.call(a.id, stranger, http.MethodPost, ``, cp, h.BackfillLegacyTopic); rec.Code != http.StatusForbidden {
		t.Errorf("non-attendant backfill = %d, want 403", rec.Code)
	}
	for b, want := range map[string]int{`{"action":"delete_ticket"}`: 422, `{"action":"none"}`: 422, `{"action":"create","x":1}`: 400, `{`: 400} {
		if rec := e.call(a.id, attendant, http.MethodPost, b, tp, h.ApplyTicketPolicy); rec.Code != want {
			t.Errorf("apply %s = %d, want %d", b, rec.Code, want)
		}
	}
	if rec := e.call(a.id, attendant, http.MethodPost, `{"action":"create"}`, tp, h.ApplyTicketPolicy); rec.Code != http.StatusConflict {
		t.Errorf("create while the conversation has an active ticket = %d, want 409", rec.Code)
	}
	if rec := e.call(a.id, attendant, http.MethodGet, "", tp, h.GetTicketPolicy); rec.Code != 200 {
		t.Fatalf("advice = %d", rec.Code)
	}
	if rec := e.call(a.id, attendant, http.MethodPost, body, tp, h.ApplyTicketPolicy); rec.Code != http.StatusOK {
		t.Fatalf("adopt = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.call(a.id, attendant, http.MethodPost, body, tp, h.ApplyTicketPolicy); rec.Code != http.StatusConflict {
		t.Errorf("adopt again = %d, want 409", rec.Code)
	}
	if rec := e.call(a.id, attendant, http.MethodPost, ``, cp, h.BackfillLegacyTopic); rec.Code != http.StatusOK {
		t.Errorf("backfill with nothing to do = %d, want 200", rec.Code)
	}
	// without the service wired the endpoints do not exist
	bare := e.handler()
	if rec := e.call(a.id, attendant, http.MethodGet, "", tp, bare.GetTicketPolicy); rec.Code != http.StatusNotFound {
		t.Errorf("unwired = %d, want 404", rec.Code)
	}
}

func TestANewSubjectCanOpenItsOwnTicketAndTheConversationTicketGuaranteeStays(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	placeholder := e.ticket(a.id, a.conversation, "open")
	first, _ := e.topicWith(a, admin, "Pedido atrasado", "pedido 837 atrasado")
	second, _ := e.topicWith(a, admin, "Nota errada", "nota 992 errada")
	svc := e.ticketSvc(application.DefaultFlags())
	e.session(a.id, admin, func(ctx context.Context) {
		if _, err := svc.Apply(ctx, first.ID, domain.TicketActionAdoptActive, domain.TicketLinkAgent); err != nil {
			t.Fatal(err)
		}
		res, err := svc.Apply(ctx, second.ID, domain.TicketActionCreate, domain.TicketLinkAgent)
		if err != nil || !res.Created || res.TicketID == placeholder || res.Relation != domain.TicketPrimary {
			t.Fatalf("subject ticket: %+v %v", res, err)
		}
	})
	if e.activeTickets(a.id, a.conversation) != 2 || e.count(`SELECT count(*) FROM tickets WHERE tenant_id=$1 AND topic_scoped AND conversation_id=$2`, a.id, a.conversation) != 1 {
		t.Fatal("expected the conversation ticket plus one subject ticket")
	}
	// the conversation still reports ITS ticket (not the subject's) wherever one ticket per conversation is read
	var picked uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT id FROM tickets WHERE tenant_id=$1 AND conversation_id=$2 AND status IN ('open','in_progress','waiting') ORDER BY topic_scoped ASC, updated_at DESC LIMIT 1`, a.id, a.conversation).Scan(&picked)
	if picked != placeholder {
		t.Fatal("the conversation ticket must stay the one conversation-level reads pick")
	}
	// ...and the race protection of inbound is intact: a SECOND conversation ticket is still impossible
	if _, err := e.seed.Exec(e.ctx, `INSERT INTO tickets(id,tenant_id,conversation_id,status,subject) VALUES(uuid_generate_v4(),$1,$2,'open','duplicado')`, a.id, a.conversation); err == nil {
		t.Fatal("two conversation tickets in one conversation must remain impossible")
	}
}
