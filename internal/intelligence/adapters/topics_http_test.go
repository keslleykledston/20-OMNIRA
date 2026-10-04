package adapters

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
}

func p(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func TestTopicHTTPReadAndCreateByTheAttendant(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	h := e.handler()
	admin := e.member(a.id, "tenant_admin")
	agent := e.member(a.id, "tenant_agent")
	conv := e.conversationFor(a.id, &agent) // the agent attends this conversation
	msg := e.message(a.id, conv, "pedido 837 não chegou")

	body := `{"title":"Pedido 837","intent":"delivery_delay","message_ids":["` + msg.String() + `"]}`
	rec := e.call(a.id, agent, http.MethodPost, body, p("conversation_id", conv.String()), h.CreateConversationTopic)
	if rec.Code != http.StatusCreated {
		t.Fatalf("attendant create = %d %s", rec.Code, rec.Body.String())
	}
	var created topicDTO
	decodeBody(t, rec, &created)
	if created.Title != "Pedido 837" || created.Status != "open" || created.Source != "manual" || created.OriginConversationID == nil {
		t.Fatalf("created = %+v", created)
	}

	// every member with topic.read sees it, with counts
	rec = e.call(a.id, admin, http.MethodGet, "", p("conversation_id", conv.String()), h.ListConversationTopics)
	var list struct{ Items []topicDTO }
	decodeBody(t, rec, &list)
	if rec.Code != 200 || len(list.Items) != 1 || list.Items[0].MessageCount == nil || *list.Items[0].MessageCount != 1 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	rec = e.call(a.id, admin, http.MethodGet, "", p("topic_id", created.ID.String()), h.GetTopic)
	if rec.Code != 200 {
		t.Fatalf("get = %d", rec.Code)
	}
	rec = e.call(a.id, admin, http.MethodGet, "", p("topic_id", created.ID.String()), h.ListTopicMessages)
	var page struct {
		Items   []topicMessageDTO `json:"items"`
		HasMore bool              `json:"has_more"`
	}
	decodeBody(t, rec, &page)
	if rec.Code != 200 || len(page.Items) != 1 || page.Items[0].ID != msg || page.Items[0].DecisionSource != "agent" {
		t.Fatalf("timeline = %d %s", rec.Code, rec.Body.String())
	}
	rec = e.call(a.id, admin, http.MethodGet, "", p("contact_id", created.PrimaryContactID.String()), h.ListContactTopics)
	decodeBody(t, rec, &list)
	if rec.Code != 200 || len(list.Items) != 1 {
		t.Fatalf("contact topics = %d %s", rec.Code, rec.Body.String())
	}
	if rec = e.call(a.id, admin, http.MethodGet, "", p("contact_id", created.PrimaryContactID.String()), func(w http.ResponseWriter, r *http.Request) {
		r.URL.RawQuery = "status=weird"
		h.ListContactTopics(w, r)
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad status filter = %d, want 400", rec.Code)
	}
}

func TestTopicHTTPAuthorization(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	h := e.handler()
	supervisor := e.member(a.id, "tenant_supervisor") // holds conversation.manage
	attendant := e.member(a.id, "tenant_agent")
	stranger := e.member(a.id, "tenant_agent") // agent of the same tenant, not the attendant
	viewer := e.readOnlyMember(a.id)           // no topic permissions at all
	conv := e.conversationFor(a.id, &attendant)
	cp := p("conversation_id", conv.String())

	// no topic permissions: neither read nor write
	if rec := e.call(a.id, viewer, http.MethodGet, "", cp, h.ListConversationTopics); rec.Code != http.StatusForbidden {
		t.Errorf("viewer read = %d, want 403", rec.Code)
	}
	if rec := e.call(a.id, viewer, http.MethodPost, `{"title":"x"}`, cp, h.CreateConversationTopic); rec.Code != http.StatusForbidden {
		t.Errorf("viewer create = %d, want 403", rec.Code)
	}
	// an agent who does not attend the conversation cannot organise its topics...
	if rec := e.call(a.id, stranger, http.MethodPost, `{"title":"x"}`, cp, h.CreateConversationTopic); rec.Code != http.StatusForbidden {
		t.Errorf("non-attendant create = %d, want 403", rec.Code)
	}
	if e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1`, a.id) != 0 {
		t.Fatal("a refused request must not create anything")
	}
	// ...but a supervisor (conversation.manage) can, and so can the attendant
	rec := e.call(a.id, supervisor, http.MethodPost, `{"title":"do supervisor"}`, cp, h.CreateConversationTopic)
	if rec.Code != http.StatusCreated {
		t.Fatalf("supervisor create = %d %s", rec.Code, rec.Body.String())
	}
	var topic topicDTO
	decodeBody(t, rec, &topic)
	tp := p("topic_id", topic.ID.String())
	if rec := e.call(a.id, attendant, http.MethodPatch, `{"title":"renomeado"}`, tp, h.PatchTopic); rec.Code != 200 {
		t.Errorf("attendant patch = %d, want 200", rec.Code)
	}
	if rec := e.call(a.id, stranger, http.MethodPatch, `{"title":"hack"}`, tp, h.PatchTopic); rec.Code != http.StatusForbidden {
		t.Errorf("non-attendant patch = %d, want 403", rec.Code)
	}
	if rec := e.call(a.id, stranger, http.MethodPost, `{"message_id":"`+uuid.NewString()+`"}`, tp, h.LinkMessage); rec.Code != http.StatusForbidden {
		t.Errorf("non-attendant link message = %d, want 403", rec.Code)
	}
	if rec := e.call(a.id, stranger, http.MethodPost, `{"ticket_id":"`+uuid.NewString()+`"}`, tp, h.LinkTicket); rec.Code != http.StatusForbidden {
		t.Errorf("non-attendant link ticket = %d, want 403", rec.Code)
	}
	if got := e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1 AND title='renomeado'`, a.id); got != 1 {
		t.Errorf("title = renomeado count %d", got)
	}
}

func TestTopicHTTPValidationAndErrors(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	h := e.handler()
	admin := e.member(a.id, "tenant_admin")
	cp := p("conversation_id", a.conversation.String())

	cases := []struct {
		name string
		body string
		want int
	}{
		{"missing title", `{}`, 422},
		{"blank title", `{"title":"   "}`, 422},
		{"title too long", `{"title":"` + strings.Repeat("x", 201) + `"}`, 422},
		{"bad privacy", `{"title":"x","privacy_policy":"secret"}`, 422},
		{"unknown field", `{"title":"x","tenant_id":"` + uuid.NewString() + `"}`, 400},
		{"malformed json", `{"title":`, 400},
		{"bad message id", `{"title":"x","message_ids":["nope"]}`, 400},
		{"message of nowhere", `{"title":"x","message_ids":["` + uuid.NewString() + `"]}`, 404},
	}
	for _, c := range cases {
		if rec := e.call(a.id, admin, http.MethodPost, c.body, cp, h.CreateConversationTopic); rec.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, rec.Code, strings.TrimSpace(rec.Body.String()), c.want)
		}
	}
	if rec := e.call(a.id, admin, http.MethodPost, `{"title":"x"}`, p("conversation_id", "not-a-uuid"), h.CreateConversationTopic); rec.Code != 400 {
		t.Errorf("invalid conversation id = %d, want 400", rec.Code)
	}
	if rec := e.call(a.id, admin, http.MethodPost, `{"title":"x"}`, p("conversation_id", uuid.NewString()), h.CreateConversationTopic); rec.Code != 404 {
		t.Errorf("unknown conversation = %d, want 404", rec.Code)
	}
	if rec := e.call(a.id, admin, http.MethodGet, "", p("topic_id", uuid.NewString()), h.GetTopic); rec.Code != 404 {
		t.Errorf("unknown topic = %d, want 404", rec.Code)
	}
	if rec := e.call(a.id, admin, http.MethodGet, "", p("topic_id", "zzz"), h.GetTopic); rec.Code != 400 {
		t.Errorf("invalid topic id = %d, want 400", rec.Code)
	}
	if e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1`, a.id) != 0 {
		t.Fatal("no refused request may leave a topic behind")
	}
}

func TestTopicHTTPLifecycleAndTickets(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	h := e.handler()
	admin := e.member(a.id, "tenant_admin")
	cp := p("conversation_id", a.conversation.String())
	rec := e.call(a.id, admin, http.MethodPost, `{"title":"Cobrança"}`, cp, h.CreateConversationTopic)
	var topic topicDTO
	decodeBody(t, rec, &topic)
	tp := p("topic_id", topic.ID.String())

	if rec := e.call(a.id, admin, http.MethodPatch, `{"status":"resolved"}`, tp, h.PatchTopic); rec.Code != 200 {
		t.Fatalf("resolve = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.call(a.id, admin, http.MethodPatch, `{"status":"resolved"}`, tp, h.PatchTopic); rec.Code != 200 {
		t.Errorf("setting the same status is a no-op, got %d", rec.Code)
	}
	if rec := e.call(a.id, admin, http.MethodPatch, `{"status":"open"}`, tp, h.PatchTopic); rec.Code != 200 {
		t.Errorf("reopen = %d", rec.Code)
	}
	if rec := e.call(a.id, admin, http.MethodPatch, `{"status":"archived"}`, tp, h.PatchTopic); rec.Code != http.StatusConflict {
		t.Errorf("archive via patch = %d, want 409", rec.Code)
	}
	if rec := e.call(a.id, admin, http.MethodPatch, `{"privacy_policy":"private_required"}`, tp, h.PatchTopic); rec.Code != 200 {
		t.Errorf("privacy patch = %d", rec.Code)
	}

	t1 := e.ticket(a.id, a.conversation, "open")
	other := e.conversationFor(a.id, nil)
	t2 := e.ticket(a.id, other, "open")
	if rec := e.call(a.id, admin, http.MethodPost, `{"ticket_id":"`+t1.String()+`","relation":"primary"}`, tp, h.LinkTicket); rec.Code != http.StatusNoContent {
		t.Fatalf("link ticket = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.call(a.id, admin, http.MethodPost, `{"ticket_id":"`+t2.String()+`","relation":"primary"}`, tp, h.LinkTicket); rec.Code != http.StatusConflict {
		t.Errorf("second primary = %d, want 409", rec.Code)
	}
	if rec := e.call(a.id, admin, http.MethodPost, `{"ticket_id":"`+t2.String()+`","relation":"owner"}`, tp, h.LinkTicket); rec.Code != 422 {
		t.Errorf("bad relation = %d, want 422", rec.Code)
	}
	if rec := e.call(a.id, admin, http.MethodPost, `{"ticket_id":"`+uuid.NewString()+`"}`, tp, h.LinkTicket); rec.Code != 404 {
		t.Errorf("unknown ticket = %d, want 404", rec.Code)
	}
	rec = e.call(a.id, admin, http.MethodGet, "", tp, h.ListTopicTickets)
	var tickets struct{ Items []topicTicketDTO }
	decodeBody(t, rec, &tickets)
	if rec.Code != 200 || len(tickets.Items) != 1 || tickets.Items[0].ID != t1 || tickets.Items[0].Relation != "primary" {
		t.Fatalf("tickets = %d %s", rec.Code, rec.Body.String())
	}

	// messages: add, list, remove
	msg := e.message(a.id, a.conversation, "oi")
	if rec := e.call(a.id, admin, http.MethodPost, `{"message_id":"`+msg.String()+`"}`, tp, h.LinkMessage); rec.Code != 204 {
		t.Fatalf("link message = %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.call(a.id, admin, http.MethodPost, `{"message_id":"`+msg.String()+`","relation":"weird"}`, tp, h.LinkMessage); rec.Code != 422 {
		t.Errorf("bad relation = %d, want 422", rec.Code)
	}
	if rec := e.call(a.id, admin, http.MethodDelete, "", p("topic_id", topic.ID.String(), "message_id", msg.String()), h.UnlinkMessage); rec.Code != 204 {
		t.Errorf("unlink = %d", rec.Code)
	}
	if rec := e.call(a.id, admin, http.MethodDelete, "", p("topic_id", topic.ID.String(), "message_id", msg.String()), h.UnlinkMessage); rec.Code != 404 {
		t.Errorf("unlink twice = %d, want 404", rec.Code)
	}
	if e.count(`SELECT count(*) FROM messages WHERE id=$1`, msg) != 1 {
		t.Fatal("unlinking must never delete the message")
	}
}

func TestTopicHTTPCrossTenantAttackAnswersNotFound(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	h := e.handler()
	adminA, adminB := e.member(a.id, "tenant_admin"), e.member(b.id, "tenant_admin")
	msgB := e.message(b.id, b.conversation, "de B")
	ticketB := e.ticket(b.id, b.conversation, "open")
	rec := e.call(b.id, adminB, http.MethodPost, `{"title":"de B","message_ids":["`+msgB.String()+`"]}`, p("conversation_id", b.conversation.String()), h.CreateConversationTopic)
	var topicB topicDTO
	decodeBody(t, rec, &topicB)
	rec = e.call(a.id, adminA, http.MethodPost, `{"title":"de A"}`, p("conversation_id", a.conversation.String()), h.CreateConversationTopic)
	var topicA topicDTO
	decodeBody(t, rec, &topicA)

	tpB, tpA := p("topic_id", topicB.ID.String()), p("topic_id", topicA.ID.String())
	for name, fn := range map[string]func() *httptest.ResponseRecorder{
		"get B's topic": func() *httptest.ResponseRecorder { return e.call(a.id, adminA, http.MethodGet, "", tpB, h.GetTopic) },
		"patch B's topic": func() *httptest.ResponseRecorder {
			return e.call(a.id, adminA, http.MethodPatch, `{"title":"x"}`, tpB, h.PatchTopic)
		},
		"B's timeline": func() *httptest.ResponseRecorder {
			return e.call(a.id, adminA, http.MethodGet, "", tpB, h.ListTopicMessages)
		},
		"B's tickets": func() *httptest.ResponseRecorder {
			return e.call(a.id, adminA, http.MethodGet, "", tpB, h.ListTopicTickets)
		},
		"B's conversation": func() *httptest.ResponseRecorder {
			return e.call(a.id, adminA, http.MethodGet, "", p("conversation_id", b.conversation.String()), h.ListConversationTopics)
		},
		"create in B's conv": func() *httptest.ResponseRecorder {
			return e.call(a.id, adminA, http.MethodPost, `{"title":"x"}`, p("conversation_id", b.conversation.String()), h.CreateConversationTopic)
		},
		"link B's message": func() *httptest.ResponseRecorder {
			return e.call(a.id, adminA, http.MethodPost, `{"message_id":"`+msgB.String()+`"}`, tpA, h.LinkMessage)
		},
		"link B's ticket": func() *httptest.ResponseRecorder {
			return e.call(a.id, adminA, http.MethodPost, `{"ticket_id":"`+ticketB.String()+`"}`, tpA, h.LinkTicket)
		},
		"B's contact topics": func() *httptest.ResponseRecorder {
			return e.call(a.id, adminA, http.MethodGet, "", p("contact_id", b.contact.String()), h.ListContactTopics)
		},
	} {
		got := fn()
		switch name {
		case "B's contact topics":
			var l struct{ Items []topicDTO }
			decodeBody(t, got, &l)
			if got.Code != 200 || len(l.Items) != 0 {
				t.Errorf("%s: %d with %d items; it must show nothing", name, got.Code, len(l.Items))
			}
		default:
			if got.Code != http.StatusNotFound {
				t.Errorf("%s: %d %s, want 404 (never 403: that would confirm the id exists)", name, got.Code, strings.TrimSpace(got.Body.String()))
			}
		}
		if strings.Contains(got.Body.String(), "de B") {
			t.Errorf("%s leaked tenant B's content", name)
		}
	}
	if e.count(`SELECT count(*) FROM message_topic_links WHERE tenant_id=$1`, a.id) != 0 || e.count(`SELECT count(*) FROM topic_ticket_links WHERE tenant_id=$1`, a.id) != 0 {
		t.Fatal("the attack must not have linked anything into A")
	}
	if e.count(`SELECT count(*) FROM topic_threads WHERE tenant_id=$1 AND title='x'`, b.id) != 0 {
		t.Fatal("the attack must not have changed B")
	}
}
