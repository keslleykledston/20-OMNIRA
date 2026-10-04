package adapters

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/domain"
)

type handoffCounter struct {
	mu sync.Mutex
	n  map[string]int
}

func (c *handoffCounter) Handoff(o string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == nil {
		c.n = map[string]int{}
	}
	c.n[o]++
}

func handoffFlags() application.Flags {
	f := application.DefaultFlags()
	f.PrivateHandoffEnabled = true
	return f
}

func (e *env) handoffSvc(flags application.Flags, m application.HandoffMetrics) *application.HandoffService {
	return application.NewHandoffService(NewPostgresHandoffRepository(e.app), NewPostgresRoutingRepository(e.app), flags, m)
}

// groupTopic: a topic that started in a WhatsApp group (no conversation yet).
func (e *env) groupTopic(a tenantFixture, admin uuid.UUID, title string) *domain.TopicThread {
	g := e.group(a.id)
	author := uuid.New()
	e.exec(`INSERT INTO channel_participants(id,tenant_id,channel_connection_id,provider,external_participant_id,display_name) VALUES($1,$2,$3,'waha',$4,'P')`, author, a.id, g.conn, author.String())
	m := e.groupMessage(a.id, g, author, "o pedido 837 não chegou", nil)
	var topic *domain.TopicThread
	e.session(a.id, admin, func(ctx context.Context) {
		var err error
		topic, err = application.NewTopicService(NewPostgresTopicRepository(e.app)).CreateTopic(ctx, application.CreateTopicInput{Title: title})
		if err != nil {
			e.t.Fatal(err)
		}
		rr := NewPostgresRoutingRepository(e.app)
		if err := rr.LinkMessage(ctx, a.id, grp(m), topic.ID, domain.RelationPrimary, domain.DecisionAgent, nil, nil); err != nil {
			e.t.Fatal(err)
		}
		if err := rr.LinkContainer(ctx, a.id, grp(m), g.group, topic.ID); err != nil {
			e.t.Fatal(err)
		}
	})
	return topic
}

func TestPrivateHandoffBindsThePrivateConversationToTheGroupTopic(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	topic := e.groupTopic(a, admin, "Pedido 837")
	counters := &handoffCounter{}
	svc := e.handoffSvc(handoffFlags(), counters)
	var token string
	var hid uuid.UUID
	e.session(a.id, admin, func(ctx context.Context) {
		h, tok, err := svc.Create(ctx, topic.ID, 0)
		if err != nil || h.SourceGroupID == nil || !strings.HasPrefix(tok, domain.HandoffTokenPrefix) {
			t.Fatalf("create: %+v %q %v", h, tok, err)
		}
		token, hid = tok, h.ID
	})
	// only the hash is stored: the token appears nowhere in the row
	var rowText string
	_ = e.seed.QueryRow(e.ctx, `SELECT t::text FROM topic_handoffs t WHERE id=$1`, hid).Scan(&rowText)
	if strings.Contains(rowText, token) || !strings.Contains(rowText, domain.HashHandoffToken(token)) {
		t.Fatalf("the token must not be stored in clear: %s", rowText)
	}

	// the customer pastes it in the private chat, with words around it, through the real pipeline
	msg := e.message(a.id, a.conversation, "oi, o atendente pediu isto: "+token+" obrigado")
	flags := handoffFlags()
	routing := application.NewRoutingService(NewPostgresRoutingRepository(e.app), NewPostgresTopicRepository(e.app), flags, domain.DefaultRoutingConfig(), nil)
	pipeline := application.HandoffPipeline{Next: application.RoutingPipeline{Routing: routing}, Handoffs: e.handoffSvc(flags, counters)}
	store := NewPostgresJobStore(e.app)
	_, _ = store.EnsureFromEvent(e.ctx, cnv(msg), application.PipelineVersion)
	runner := application.NewJobRunner(store, pipeline, e.session2(a.id), fastConfig(), nil)
	if n, err := runner.ProcessOnce(e.ctx); err != nil || n != 1 {
		t.Fatalf("process: %d %v", n, err)
	}
	var source string
	var rel string
	if err := e.seed.QueryRow(e.ctx, `SELECT decision_source, relation FROM message_topic_links WHERE tenant_id=$1 AND message_id=$2 AND topic_thread_id=$3`, a.id, msg, topic.ID).Scan(&source, &rel); err != nil || source != "handoff" || rel != "primary" {
		t.Fatalf("message link: %s %s %v", source, rel, err)
	}
	if e.count(`SELECT count(*) FROM topic_conversation_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND conversation_id=$3`, a.id, topic.ID, a.conversation) != 1 {
		t.Fatal("the private conversation must be linked to the topic")
	}
	if e.count(`SELECT count(*) FROM routing_decisions WHERE tenant_id=$1 AND message_id=$2 AND applied AND decision_source='handoff'`, a.id, msg) != 1 {
		t.Fatal("expected one applied handoff decision (the strongest evidence), even with auto-routing off")
	}
	var status string
	var redeemedMsg, redeemedConv uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT status, redeemed_message_id, redeemed_conversation_id FROM topic_handoffs WHERE id=$1`, hid).Scan(&status, &redeemedMsg, &redeemedConv)
	if status != "redeemed" || redeemedMsg != msg || redeemedConv != a.conversation || counters.n["redeemed"] != 1 {
		t.Fatalf("handoff row: %s %v %v metrics %v", status, redeemedMsg, redeemedConv, counters.n)
	}
	// replaying the same job changes nothing
	e.session(a.id, admin, func(ctx context.Context) {
		if ok, err := e.handoffSvc(flags, counters).TryRedeem(ctx, cnv(msg)); err != nil || ok {
			t.Fatalf("replay: %v %v", ok, err)
		}
	})
	if e.count(`SELECT count(*) FROM routing_decisions WHERE tenant_id=$1 AND message_id=$2`, a.id, msg) != 1 {
		t.Fatal("a replay must not create another decision")
	}
}

func TestHandoffTokenIsSingleUseExpiringRevocableAndTenantBound(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	admin, adminB := e.member(a.id, "tenant_admin"), e.member(b.id, "tenant_admin")
	topic := e.groupTopic(a, admin, "Assunto")
	counters := &handoffCounter{}
	svc := e.handoffSvc(handoffFlags(), counters)
	create := func() (string, uuid.UUID) {
		var tok string
		var id uuid.UUID
		e.session(a.id, admin, func(ctx context.Context) {
			h, t2, err := svc.Create(ctx, topic.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			tok, id = t2, h.ID
		})
		return tok, id
	}
	tryIn := func(tenant uuid.UUID, user uuid.UUID, conv uuid.UUID, text string) bool {
		msg := e.message(tenant, conv, text)
		var ok bool
		e.session(tenant, user, func(ctx context.Context) {
			var err error
			if ok, err = svc.TryRedeem(ctx, cnv(msg)); err != nil {
				t.Fatal(err)
			}
		})
		return ok
	}

	// SINGLE USE under concurrency: several conversations race for one token; exactly one wins
	token, _ := create()
	convs := []uuid.UUID{a.conversation, e.conversationFor(a.id, nil), e.conversationFor(a.id, nil), e.conversationFor(a.id, nil)}
	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range convs {
		msg := e.message(a.id, c, token)
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.attempt(a.id, admin, func(ctx context.Context) {
				if ok, _ := svc.TryRedeem(ctx, cnv(msg)); ok {
					mu.Lock()
					wins++
					mu.Unlock()
				}
			})
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d redemptions won, want exactly 1", wins)
	}

	// EXPIRED
	expired, expiredID := create()
	e.exec(`UPDATE topic_handoffs SET created_at = now() - interval '2 days', expires_at = now() - interval '1 day' WHERE id=$1`, expiredID)
	if tryIn(a.id, admin, a.conversation, expired) {
		t.Error("an expired token must not redeem")
	}
	// REVOKED
	revoked, revokedID := create()
	e.session(a.id, admin, func(ctx context.Context) {
		if err := svc.Revoke(ctx, topic.ID, revokedID); err != nil {
			t.Fatal(err)
		}
		if err := svc.Revoke(ctx, topic.ID, revokedID); err == nil {
			t.Error("revoking twice must be refused")
		}
	})
	if tryIn(a.id, admin, a.conversation, revoked) {
		t.Error("a revoked token must not redeem")
	}
	// CLOSED topic
	closedTok, _ := create()
	e.exec(`UPDATE topic_threads SET status='resolved', resolved_at=now() WHERE id=$1`, topic.ID)
	if tryIn(a.id, admin, a.conversation, closedTok) {
		t.Error("a token for a closed topic must not redeem")
	}
	e.exec(`UPDATE topic_threads SET status='open', resolved_at=NULL WHERE id=$1`, topic.ID)
	// TENANT-BOUND: the same token typed in another tenant's chat does nothing and does not burn it
	other, _ := create()
	convB := b.conversation
	if tryIn(b.id, adminB, convB, other) {
		t.Fatal("a token must never redeem in another tenant")
	}
	if !tryIn(a.id, admin, a.conversation, other) {
		t.Fatal("the token must still be valid in its own tenant after a foreign attempt")
	}
	// made-up tokens and plain text: nothing happens, and nothing distinguishes them
	fake, _, _ := domain.NewHandoffToken()
	if tryIn(a.id, admin, a.conversation, fake) || tryIn(a.id, admin, a.conversation, "meu pedido 837") {
		t.Error("unknown tokens must not redeem")
	}
	if counters.n["rejected"] < 4 {
		t.Errorf("rejected metric = %v", counters.n)
	}
	// a token pasted in a GROUP is ignored on purpose
	g := e.group(a.id)
	author := uuid.New()
	e.exec(`INSERT INTO channel_participants(id,tenant_id,channel_connection_id,provider,external_participant_id,display_name) VALUES($1,$2,$3,'waha','x','X')`, author, a.id, g.conn)
	gtok, gid := create()
	gm := e.groupMessage(a.id, g, author, gtok, nil)
	e.session(a.id, admin, func(ctx context.Context) {
		if ok, err := svc.TryRedeem(ctx, grp(gm)); err != nil || ok {
			t.Errorf("group token: %v %v", ok, err)
		}
	})
	var st string
	_ = e.seed.QueryRow(e.ctx, `SELECT status FROM topic_handoffs WHERE id=$1`, gid).Scan(&st)
	if st != "pending" {
		t.Errorf("a token seen in a group must stay pending, got %s", st)
	}
}

func TestHandoffIsOffByDefaultAndCappedPerTopic(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	topic := e.groupTopic(a, admin, "T")
	off := e.handoffSvc(application.DefaultFlags(), nil)
	e.attempt(a.id, admin, func(ctx context.Context) {
		if _, _, err := off.Create(ctx, topic.ID, 0); err != application.ErrHandoffDisabled {
			t.Errorf("flag off: %v", err)
		}
	})
	tok, _, _ := domain.NewHandoffToken()
	msg := e.message(a.id, a.conversation, tok)
	e.session(a.id, admin, func(ctx context.Context) {
		if ok, err := off.TryRedeem(ctx, cnv(msg)); err != nil || ok {
			t.Errorf("flag off redeem: %v %v", ok, err)
		}
	})
	on := e.handoffSvc(handoffFlags(), nil)
	for i := 0; i < domain.MaxPendingHandoffs; i++ {
		e.session(a.id, admin, func(ctx context.Context) {
			if _, _, err := on.Create(ctx, topic.ID, 0); err != nil {
				t.Fatalf("create %d: %v", i, err)
			}
		})
	}
	e.attempt(a.id, admin, func(ctx context.Context) {
		if _, _, err := on.Create(ctx, topic.ID, 0); err != domain.ErrTooManyHandoffs {
			t.Errorf("past the cap: %v", err)
		}
	})
	// the ttl is clamped
	e.session(a.id, admin, func(ctx context.Context) {
		list, _ := on.List(ctx, topic.ID)
		for _, h := range list {
			if h.ExpiresAt.Sub(h.CreatedAt) > domain.MaxHandoffTTL {
				t.Errorf("ttl %v above the ceiling", h.ExpiresAt.Sub(h.CreatedAt))
			}
		}
	})
}

func TestHandoffAPIAuthorizationAndNoTokenLeak(t *testing.T) {
	e := newEnv(t)
	a, b := e.tenant(), e.tenant()
	attendant := e.member(a.id, "tenant_agent")
	stranger := e.member(a.id, "tenant_agent")
	viewer := e.readOnlyMember(a.id)
	adminB := e.member(b.id, "tenant_admin")
	e.exec(`UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, a.conversation, attendant)
	topic, _ := e.topicWith(a, attendant, "Pedido", "um")
	svc := e.handoffSvc(handoffFlags(), nil)
	h := e.handler().WithHandoffs(svc)
	tp := p("topic_id", topic.ID.String())

	if rec := e.call(a.id, viewer, http.MethodPost, ``, tp, h.CreateHandoff); rec.Code != http.StatusForbidden {
		t.Errorf("viewer create = %d", rec.Code)
	}
	if rec := e.call(a.id, stranger, http.MethodPost, ``, tp, h.CreateHandoff); rec.Code != http.StatusForbidden {
		t.Errorf("non-attendant create = %d", rec.Code)
	}
	if rec := e.call(b.id, adminB, http.MethodPost, ``, tp, h.CreateHandoff); rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant create = %d, want 404", rec.Code)
	}
	if rec := e.call(a.id, attendant, http.MethodPost, `{"expires_in_minutes":-5}`, tp, h.CreateHandoff); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("negative ttl = %d", rec.Code)
	}
	rec := e.call(a.id, attendant, http.MethodPost, `{"expires_in_minutes":60}`, tp, h.CreateHandoff)
	if rec.Code != http.StatusCreated || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create = %d %v", rec.Code, rec.Header())
	}
	var created struct {
		Handoff handoffDTO `json:"handoff"`
		Token   string     `json:"token"`
	}
	decodeBody(t, rec, &created)
	if !strings.HasPrefix(created.Token, domain.HandoffTokenPrefix) {
		t.Fatalf("token missing: %s", rec.Body.String())
	}
	// the token is returned exactly once: listing never includes it, for anyone
	for _, who := range []uuid.UUID{attendant, stranger} {
		lr := e.call(a.id, who, http.MethodGet, "", tp, h.ListHandoffs)
		if lr.Code != 200 || strings.Contains(lr.Body.String(), created.Token) || strings.Contains(lr.Body.String(), "token") {
			t.Fatalf("list leaked or failed: %d %s", lr.Code, lr.Body.String())
		}
	}
	if rec := e.call(a.id, viewer, http.MethodGet, "", tp, h.ListHandoffs); rec.Code != http.StatusForbidden {
		t.Errorf("viewer list = %d", rec.Code)
	}
	if rec := e.call(b.id, adminB, http.MethodGet, "", tp, h.ListHandoffs); rec.Code != http.StatusNotFound {
		t.Errorf("cross-tenant list = %d", rec.Code)
	}
	rv := p("topic_id", topic.ID.String())
	rv["handoff_id"] = created.Handoff.ID.String()
	if rec := e.call(a.id, stranger, http.MethodPost, ``, rv, h.RevokeHandoff); rec.Code != http.StatusForbidden {
		t.Errorf("non-attendant revoke = %d", rec.Code)
	}
	if rec := e.call(a.id, attendant, http.MethodPost, ``, rv, h.RevokeHandoff); rec.Code != http.StatusNoContent {
		t.Errorf("revoke = %d", rec.Code)
	}
	if rec := e.call(a.id, attendant, http.MethodPost, ``, rv, h.RevokeHandoff); rec.Code != http.StatusConflict {
		t.Errorf("revoke again = %d, want 409", rec.Code)
	}
	// flag off: the endpoints do not exist
	hOff := e.handler().WithHandoffs(e.handoffSvc(application.DefaultFlags(), nil))
	if rec := e.call(a.id, attendant, http.MethodPost, ``, tp, hOff.CreateHandoff); rec.Code != http.StatusNotFound {
		t.Errorf("flag off create = %d, want 404", rec.Code)
	}
}
