package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/platform/pagination"
)

// A qualitative performance check (not a load test): a conversation with 3,000 messages spread over 40 topics. The point is
// to catch a pathological query (a missing index, an accidental full scan), so each bound is generous. The measured
// durations are logged so they can be read from the test output.
func TestQueriesStayFastOnABusyConversation(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	const topics, perTopic = 40, 75
	// 3,000 inbound messages over the last 3 days
	e.exec(`INSERT INTO messages(id,tenant_id,conversation_id,direction,message_type,body,status,created_at)
	        SELECT uuid_generate_v4(), $1, $2, 'inbound', 'text', 'mensagem número ' || g || ' sobre o pedido ' || (1000 + g % 40), 'received', now() - (g || ' minutes')::interval
	        FROM generate_series(1, $3) g`, a.id, a.conversation, topics*perTopic)
	e.exec(`INSERT INTO topic_threads(id,tenant_id,primary_contact_id,origin_conversation_id,title,status,source,last_activity_at)
	        SELECT uuid_generate_v4(), $1, $2, $3, 'Assunto ' || g, 'open', 'rule', now() - (g || ' minutes')::interval FROM generate_series(1, $4) g`, a.id, a.contact, a.conversation, topics)
	e.exec(`WITH t AS (SELECT id, row_number() OVER (ORDER BY title) AS n FROM topic_threads WHERE tenant_id=$1),
	             m AS (SELECT id, row_number() OVER (ORDER BY created_at) AS n FROM messages WHERE tenant_id=$1)
	        INSERT INTO message_topic_links(tenant_id,message_id,topic_thread_id,relation,decision_source)
	        SELECT $1, m.id, t.id, 'primary', 'rule' FROM m JOIN t ON t.n = (m.n % $2) + 1`, a.id, topics)
	e.exec(`INSERT INTO topic_conversation_links(tenant_id,topic_thread_id,conversation_id,relation) SELECT tenant_id, id, $2, 'origin' FROM topic_threads WHERE tenant_id=$1`, a.id, a.conversation)
	e.exec(`INSERT INTO topic_entities(tenant_id,topic_thread_id,entity_type,canonical_key,source) SELECT $1, id, 'order', (1000 + row_number() OVER ())::text, 'rule' FROM topic_threads WHERE tenant_id=$1`, a.id)
	e.exec(`ANALYZE`)

	var first uuid.UUID
	_ = e.seed.QueryRow(e.ctx, `SELECT id FROM topic_threads WHERE tenant_id=$1 ORDER BY title LIMIT 1`, a.id).Scan(&first)
	topicSvc := application.NewTopicService(NewPostgresTopicRepository(e.app))

	measure := func(name string, limit time.Duration, fn func(ctx context.Context)) {
		var best time.Duration = time.Hour
		for i := 0; i < 3; i++ { // best of 3: ignore a cold cache
			start := time.Now()
			e.session(a.id, admin, fn)
			if d := time.Since(start); d < best {
				best = d
			}
		}
		t.Logf("perf: %-34s %v (limit %v)", name, best.Round(time.Millisecond), limit)
		if best > limit {
			t.Errorf("%s took %v, over %v", name, best, limit)
		}
	}
	measure("list the conversation's 40 topics", 400*time.Millisecond, func(ctx context.Context) {
		if items, err := topicSvc.ListConversationTopics(ctx, a.conversation); err != nil || len(items) != topics {
			t.Fatalf("%d %v", len(items), err)
		}
	})
	measure("topic messages (first page of 50)", 300*time.Millisecond, func(ctx context.Context) {
		if _, _, err := topicSvc.ListTopicMessages(ctx, first, (*pagination.Cursor)(nil), 50); err != nil {
			t.Fatal(err)
		}
	})
	measure("build the topic context (75 messages)", 500*time.Millisecond, func(ctx context.Context) {
		ctxRepo := NewPostgresContextRepository(e.app)
		b := application.NewContextBuilder(NewPostgresTopicRepository(e.app), ctxRepo, NewPostgresSummaryRepository(e.app))
		if _, err := b.Build(ctx, first, nil); err != nil {
			t.Fatal(err)
		}
	})
	msg := e.message(a.id, a.conversation, "ainda sem resposta sobre o pedido 1003")
	measure("route one message (40 open topics)", 400*time.Millisecond, func(ctx context.Context) {
		svc := application.NewRoutingService(NewPostgresRoutingRepository(e.app), NewPostgresTopicRepository(e.app), application.DefaultFlags(), domain.DefaultRoutingConfig(), nil) // dry run: nothing is written
		if _, err := svc.Route(ctx, cnv(msg), application.RouteOptions{}); err != nil {
			t.Fatal(err)
		}
	})
	measure("evaluation report (30 days)", 800*time.Millisecond, func(ctx context.Context) {
		if _, err := NewPostgresEvaluationRepository(e.app).Report(ctx, a.id, 30); err != nil {
			t.Fatal(err)
		}
	})
}
