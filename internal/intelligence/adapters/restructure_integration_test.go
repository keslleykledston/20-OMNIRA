package adapters

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/domain"
)

func (e *env) restructureHandler() *TopicHandler {
	topics := NewPostgresTopicRepository(e.app)
	return e.handler().WithRestructure(application.NewRestructureService(topics, NewPostgresRestructureRepository(e.app), NewPostgresRoutingRepository(e.app)))
}

type contextT = context.Context

// groupTopicWith: a topic holding the given group messages of an existing group.
func (e *env) groupTopicWith(a tenantFixture, admin uuid.UUID, g groupFixture, title string, msgs ...uuid.UUID) *domain.TopicThread {
	var topic *domain.TopicThread
	e.session(a.id, admin, func(ctx context.Context) {
		var err error
		topic, err = application.NewTopicService(NewPostgresTopicRepository(e.app)).CreateTopic(ctx, application.CreateTopicInput{Title: title})
		if err != nil {
			e.t.Fatal(err)
		}
		rr := NewPostgresRoutingRepository(e.app)
		for _, m := range msgs {
			if err := rr.LinkMessage(ctx, a.id, grp(m), topic.ID, domain.RelationPrimary, domain.DecisionAgent, nil, nil); err != nil {
				e.t.Fatal(err)
			}
		}
		if err := rr.LinkContainer(ctx, a.id, grp(msgs[0]), g.group, topic.ID); err != nil {
			e.t.Fatal(err)
		}
	})
	return topic
}

func mergeBody(into uuid.UUID) string { return `{"into_topic_id":"` + into.String() + `"}` }

func TestMergeFoldsTheSourceIntoTheTargetWithoutLosingAnyHistory(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	attendant := e.member(a.id, "tenant_agent")
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, attendant)
	h := e.restructureHandler()
	src, srcMsgs := e.topicWith(a, attendant, "Pedido 837", "meu pedido 837 não chegou", "já faz quatro dias")
	dst, dstMsgs := e.topicWith(a, attendant, "Entrega atrasada", "a entrega está atrasada")
	e.exec(`INSERT INTO topic_entities(tenant_id,topic_thread_id,entity_type,canonical_key,source) VALUES($1,$2,'order','837','rule')`, a.id, src.ID)
	t1, t2 := e.ticket(a.id, a.conversation, "resolved"), e.ticket(a.id, a.conversation, "open")
	e.exec(`INSERT INTO topic_ticket_links(tenant_id,topic_thread_id,ticket_id,relation,created_by) VALUES($1,$2,$3,'primary','agent'),($1,$4,$5,'primary','agent')`, a.id, src.ID, t1, dst.ID, t2)
	e.exec(`INSERT INTO conversation_topic_focus(tenant_id,conversation_id,topic_thread_id,source) VALUES($1,$2,$3,'router')`, a.id, a.conversation, src.ID)
	hand := uuid.New()
	e.exec(`INSERT INTO topic_handoffs(id,tenant_id,topic_thread_id,token_hash,expires_at) VALUES($1,$2,$3,$4,now()+interval '1 hour')`, hand, a.id, src.ID, "0123456789012345678901234567890123456789012345678901234567890123")

	rec := e.call(a.id, attendant, http.MethodPost, mergeBody(dst.ID), p("topic_id", src.ID.String()), h.MergeTopic)
	var out restructureDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || out.Messages != 2 || out.Entities != 1 || out.Tickets != 1 || out.SourceStatus != "archived" {
		t.Fatalf("merge = %d %s", rec.Code, rec.Body.String())
	}
	// the target now holds everything
	for _, m := range append(append([]uuid.UUID{}, srcMsgs...), dstMsgs...) {
		if e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2 AND topic_thread_id=$3`, a.id, m, dst.ID) != 1 {
			t.Errorf("message %s missing from the target", m)
		}
	}
	if e.count(`SELECT count(*) FROM topic_entities WHERE tenant_id=$1 AND topic_thread_id=$2 AND canonical_key='837'`, a.id, dst.ID) != 1 {
		t.Error("entity not carried to the target")
	}
	// HISTORY: the source keeps its own links, is archived and points at the target
	var status string
	var into *uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT status, merged_into_topic_id FROM topic_threads WHERE id=$1`, src.ID).Scan(&status, &into)
	if status != "archived" || into == nil || *into != dst.ID {
		t.Fatalf("source = %s -> %v", status, into)
	}
	if e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND topic_thread_id=$2`, a.id, src.ID) != 2 {
		t.Fatal("the archived source must keep its own message links (history)")
	}
	// tickets: the source's primary becomes 'merged' on the target; the target keeps exactly ONE primary
	var rel string
	_ = e.seed.QueryRow(e.ctx, `SELECT relation FROM topic_ticket_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND ticket_id=$3`, a.id, dst.ID, t1).Scan(&rel)
	if rel != "merged" || e.count(`SELECT count(*) FROM topic_ticket_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND relation='primary'`, a.id, dst.ID) != 1 {
		t.Fatalf("ticket relation on target = %q", rel)
	}
	// hints and pending handoffs of the archived source are gone
	if e.count(`SELECT count(*) FROM conversation_topic_focus WHERE tenant_id=$1 AND topic_thread_id=$2`, a.id, src.ID) != 0 {
		t.Error("focus hint on the archived source must be dropped")
	}
	var hs string
	_ = e.seed.QueryRow(e.ctx, `SELECT status FROM topic_handoffs WHERE id=$1`, hand).Scan(&hs)
	if hs != "revoked" {
		t.Errorf("pending handoff of the merged topic = %s, want revoked", hs)
	}
	// the archived source no longer clutters the conversation's list, and GET shows where it went
	rec = e.call(a.id, attendant, http.MethodGet, "", p("conversation_id", a.conversation.String()), h.ListConversationTopics)
	var list struct{ Items []topicDTO }
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	for _, it := range list.Items {
		if it.ID == src.ID {
			t.Error("a merged topic must not be listed")
		}
	}
	rec = e.call(a.id, attendant, http.MethodGet, "", p("topic_id", src.ID.String()), h.GetTopic)
	var got topicDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.MergedIntoTopicID == nil || *got.MergedIntoTopicID != dst.ID || got.Status != "archived" {
		t.Fatalf("GET merged topic = %s", rec.Body.String())
	}
	// a second merge of the same source is refused, as is merging into a closed topic or itself
	if rec := e.call(a.id, attendant, http.MethodPost, mergeBody(dst.ID), p("topic_id", src.ID.String()), h.MergeTopic); rec.Code != http.StatusConflict {
		t.Errorf("merging an archived topic = %d, want 409", rec.Code)
	}
	if rec := e.call(a.id, attendant, http.MethodPost, mergeBody(dst.ID), p("topic_id", dst.ID.String()), h.MergeTopic); rec.Code != http.StatusConflict {
		t.Errorf("merging into itself = %d, want 409", rec.Code)
	}
}

func TestMergeAuthorizationTenancyAndOppositeMergesNeverDeadlock(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	attendant := e.member(a.id, "tenant_agent")
	stranger := e.member(a.id, "tenant_agent")
	viewer := e.readOnlyMember(a.id)
	adminB := e.member(b.id, "tenant_admin")
	other := e.conversationFor(a.id, &stranger) // a conversation the attendant does NOT operate
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, attendant)
	h := e.restructureHandler()
	x, _ := e.topicWith(a, attendant, "X", "x1", "x2")
	y, _ := e.topicWith(a, attendant, "Y", "y1")
	var foreign *uuid.UUID
	e.session(a.id, stranger, func(ctx contextT) {
		m := e.message(a.id, other, "mensagem de outra conversa")
		tp, err := application.NewTopicService(NewPostgresTopicRepository(e.app)).CreateTopic(ctx, application.CreateTopicInput{ConversationID: other, Title: "Alheio", MessageIDs: []uuid.UUID{m}})
		if err != nil {
			t.Fatal(err)
		}
		foreign = &tp.ID
	})
	bt, _ := e.topicWith(b, adminB, "De B", "b1")

	if rec := e.call(a.id, viewer, http.MethodPost, mergeBody(y.ID), p("topic_id", x.ID.String()), h.MergeTopic); rec.Code != http.StatusForbidden {
		t.Errorf("viewer = %d", rec.Code)
	}
	// operating only ONE of the two topics is not enough
	if rec := e.call(a.id, attendant, http.MethodPost, mergeBody(*foreign), p("topic_id", x.ID.String()), h.MergeTopic); rec.Code != http.StatusForbidden {
		t.Errorf("target not operated = %d, want 403", rec.Code)
	}
	if rec := e.call(a.id, attendant, http.MethodPost, mergeBody(x.ID), p("topic_id", foreign.String()), h.MergeTopic); rec.Code != http.StatusForbidden {
		t.Errorf("source not operated = %d, want 403", rec.Code)
	}
	// a topic of another tenant is invisible, in either role
	if rec := e.call(a.id, attendant, http.MethodPost, mergeBody(bt.ID), p("topic_id", x.ID.String()), h.MergeTopic); rec.Code != http.StatusNotFound {
		t.Errorf("foreign target = %d, want 404", rec.Code)
	}
	if rec := e.call(a.id, attendant, http.MethodPost, mergeBody(x.ID), p("topic_id", bt.ID.String()), h.MergeTopic); rec.Code != http.StatusNotFound {
		t.Errorf("foreign source = %d, want 404", rec.Code)
	}
	if rec := e.call(a.id, attendant, http.MethodPost, `{"into_topic_id":"nope"}`, p("topic_id", x.ID.String()), h.MergeTopic); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id = %d", rec.Code)
	}
	if e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1 AND status='archived'`, a.id) != 0 || e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1 AND status='archived'`, b.id) != 0 {
		t.Fatal("a refused merge changed something")
	}
	// opposite merges at the same time: one wins, the other is a conflict, nobody deadlocks
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, pair := range [][2]uuid.UUID{{x.ID, y.ID}, {y.ID, x.ID}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- e.call(a.id, attendant, http.MethodPost, mergeBody(pair[1]), p("topic_id", pair[0].String()), h.MergeTopic).Code
		}()
	}
	wg.Wait()
	close(codes)
	ok, conflict := 0, 0
	for c := range codes {
		switch c {
		case 200:
			ok++
		case http.StatusConflict:
			conflict++
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("opposite merges: %d ok, %d conflict", ok, conflict)
	}
	if e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1 AND status='archived'`, a.id) != 1 {
		t.Fatal("exactly one topic may end up archived")
	}
	// unwired: the endpoints do not exist
	if rec := e.call(a.id, attendant, http.MethodPost, mergeBody(y.ID), p("topic_id", x.ID.String()), e.handler().MergeTopic); rec.Code != http.StatusNotFound {
		t.Errorf("unwired = %d", rec.Code)
	}
}

func TestSplitMovesOnlyTheChosenMessagesAndRemembersItsOrigin(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	attendant := e.member(a.id, "tenant_agent")
	stranger := e.member(a.id, "tenant_agent")
	adminB := e.member(b.id, "tenant_admin")
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, attendant)
	h := e.restructureHandler()
	src, ids := e.topicWith(a, attendant, "Misturado", "meu pedido 837 não chegou", "outra coisa: a nota 992 veio errada", "obrigado", "ainda sem resposta")
	// a routing decision applied to the message being moved
	e.exec(`INSERT INTO routing_decisions(tenant_id,message_id,status,selected_topic_thread_id,decision_source,applied) VALUES($1,$2,'assigned',$3,'rule',true)`, a.id, ids[1], src.ID)
	bt, bids := e.topicWith(b, adminB, "De B", "b1", "b2")
	tp := p("topic_id", src.ID.String())

	body := func(title string, msgs ...uuid.UUID) string {
		parts := ""
		for i, m := range msgs {
			if i > 0 {
				parts += ","
			}
			parts += `"` + m.String() + `"`
		}
		return `{"title":"` + title + `","message_ids":[` + parts + `]}`
	}
	// refusals first: nothing may change
	for name, c := range map[string]struct {
		as   uuid.UUID
		body string
		want int
	}{
		"non-operator":    {stranger, body("Nota", ids[1]), http.StatusForbidden},
		"empty list":      {attendant, `{"title":"x","message_ids":[]}`, http.StatusUnprocessableEntity},
		"no title":        {attendant, body("   ", ids[1]), http.StatusUnprocessableEntity},
		"all messages":    {attendant, body("Tudo", ids...), http.StatusConflict},
		"duplicate":       {attendant, body("Dup", ids[1], ids[1]), http.StatusUnprocessableEntity},
		"foreign message": {attendant, body("Alheio", bids[0]), http.StatusNotFound},
		"unknown field":   {attendant, `{"title":"x","message_ids":["` + ids[1].String() + `"],"tenant_id":"x"}`, http.StatusBadRequest},
	} {
		if rec := e.call(a.id, c.as, http.MethodPost, c.body, tp, h.SplitTopic); rec.Code != c.want {
			t.Errorf("%s = %d (%s), want %d", name, rec.Code, rec.Body.String(), c.want)
		}
	}
	if rec := e.call(b.id, adminB, http.MethodPost, body("x", ids[1]), tp, h.SplitTopic); rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant source = %d, want 404", rec.Code)
	}
	_ = bt
	if e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1`, a.id) != 1 {
		t.Fatal("a refused split created a topic")
	}

	rec := e.call(a.id, attendant, http.MethodPost, body("Nota fiscal 992", ids[1], ids[2]), tp, h.SplitTopic)
	var out restructureDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusCreated || out.NewTopic == nil || out.Messages != 2 || out.NewTopic.SplitFromTopicID == nil || *out.NewTopic.SplitFromTopicID != src.ID {
		t.Fatalf("split = %d %s", rec.Code, rec.Body.String())
	}
	nt := out.NewTopic.ID
	for _, m := range ids[1:3] {
		if e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2 AND topic_thread_id=$3 AND relation='primary' AND decision_source='agent'`, a.id, m, nt) != 1 {
			t.Errorf("message %s not moved to the new topic", m)
		}
		if e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2 AND topic_thread_id=$3`, a.id, m, src.ID) != 0 {
			t.Errorf("message %s still in the source", m)
		}
	}
	if e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1 AND topic_thread_id=$2`, a.id, src.ID) != 2 {
		t.Fatal("the source must keep the messages that were not moved")
	}
	if e.count(`SELECT count(*) FROM routing_decisions WHERE tenant_id=$1 AND message_id=$2 AND overridden_at IS NOT NULL`, a.id, ids[1]) != 1 {
		t.Error("the router's earlier decision must be marked overridden")
	}
	if e.count(`SELECT count(*) FROM topic_conversation_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND conversation_id=$3`, a.id, nt, a.conversation) != 1 {
		t.Error("the new topic must live in the conversation of its messages")
	}
	if e.count(`SELECT count(*) FROM topic_entities WHERE tenant_id=$1 AND topic_thread_id=$2 AND canonical_key='992'`, a.id, nt) != 1 {
		t.Error("the new topic must get the entities its own messages name")
	}
	var st string
	_ = e.seed.QueryRow(e.ctx, `SELECT status FROM topic_threads WHERE id=$1`, src.ID).Scan(&st)
	if st != "open" {
		t.Errorf("the source stays open, got %s", st)
	}
	// moving the same message again is refused: it no longer belongs to the source
	if rec := e.call(a.id, attendant, http.MethodPost, body("Outra vez", ids[1]), tp, h.SplitTopic); rec.Code != http.StatusNotFound {
		t.Errorf("splitting a moved message = %d, want 404", rec.Code)
	}
}

func TestSplitSupportsGroupMessages(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	h := e.restructureHandler()
	g := e.group(a.id)
	author := uuid.New()
	e.exec(`INSERT INTO channel_participants(id,tenant_id,channel_connection_id,provider,external_participant_id,display_name) VALUES($1,$2,$3,'waha','p','P')`, author, a.id, g.conn)
	m1 := e.groupMessage(a.id, g, author, "o pedido 700 atrasou", nil)
	m2 := e.groupMessage(a.id, g, author, "e a fatura 5512 está errada", nil)
	var src *struct{ id uuid.UUID }
	topic := e.groupTopicWith(a, admin, g, "Grupo misturado", m1, m2)
	src = &struct{ id uuid.UUID }{topic.ID}
	rec := e.call(a.id, admin, http.MethodPost, `{"title":"Fatura 5512","message_ids":["`+m2.String()+`"]}`, p("topic_id", src.id.String()), h.SplitTopic)
	var out restructureDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != http.StatusCreated || out.GroupMessages != 1 || out.NewTopic == nil {
		t.Fatalf("group split = %d %s", rec.Code, rec.Body.String())
	}
	if e.count(`SELECT count(*) FROM group_message_topic_links WHERE tenant_id=$1 AND group_message_id=$2 AND topic_thread_id=$3`, a.id, m2, out.NewTopic.ID) != 1 ||
		e.count(`SELECT count(*) FROM topic_group_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND group_id=$3`, a.id, out.NewTopic.ID, g.group) != 1 {
		t.Fatal("the group message and its group must follow the new topic")
	}
	if e.count(`SELECT count(*) FROM topic_entities WHERE tenant_id=$1 AND topic_thread_id=$2 AND canonical_key='5512'`, a.id, out.NewTopic.ID) != 1 {
		t.Error("entities of a moved group message")
	}
}
