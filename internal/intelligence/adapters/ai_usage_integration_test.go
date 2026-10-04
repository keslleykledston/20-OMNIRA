package adapters

import (
	"context"
	"github.com/google/uuid"
	"testing"

	aiports "github.com/omnira/omnira/internal/ai/ports"
	aiusageadapters "github.com/omnira/omnira/internal/aiusage/adapters"
	"github.com/omnira/omnira/internal/intelligence/application"
)

type tokenGen struct {
	out    string
	in     int
	outTok int
	err    error
}

func (g tokenGen) Generate(context.Context, aiports.GenerateRequest) (aiports.GenerateResponse, error) {
	if g.err != nil {
		return aiports.GenerateResponse{}, g.err
	}
	return aiports.GenerateResponse{OutputText: g.out, InputTokens: g.in, OutputTokens: g.outTok}, nil
}

func TestEveryModelCallOfTheIntelligenceFeaturesLeavesALedgerRow(t *testing.T) {
	e := newEnv(t)
	a := e.tenant()
	admin := e.member(a.id, "tenant_admin")
	ledger := aiusageadapters.NewPostgresLedger(e.app)
	topic, _ := e.topicWith(a, admin, "Pedido atrasado", "pedido 837 atrasado", "ainda sem resposta", "quatro dias")

	// the classifier: a valid answer, an invalid one and a provider failure — each one is a row
	msgOK := e.message(a.id, a.conversation, "chegou?")
	msgBad := e.message(a.id, a.conversation, "e agora?")
	msgFail := e.message(a.id, a.conversation, "alguém?")
	run := func(msg uuidT, gen aiports.TextGenerator) {
		cl := e.classifier(gen, shadowFlags(), nil).WithLedger(ledger)
		e.session(a.id, admin, func(ctx context.Context) { _, _ = cl.ClassifyShadow(ctx, cnv(msg)) })
	}
	run(msgOK, tokenGen{out: `{"verdict":"new","confidence":0.7}`, in: 420, outTok: 15})
	run(msgBad, tokenGen{out: `não sei`, in: 400, outTok: 5})
	run(msgFail, tokenGen{err: context.DeadlineExceeded})

	type row struct {
		ok      bool
		in, out int
		reason  string
		cost    *float64
		model   string
	}
	rows := map[string]row{}
	rs, err := e.seed.Query(e.ctx, `SELECT reason, success, input_tokens, output_tokens, cost_usd::float8, model FROM ai_usage WHERE tenant_id=$1 AND task='topic_classify'`, a.id)
	if err != nil {
		t.Fatal(err)
	}
	for rs.Next() {
		var r row
		var reason string
		if err := rs.Scan(&reason, &r.ok, &r.in, &r.out, &r.cost, &r.model); err != nil {
			t.Fatal(err)
		}
		rows[reason] = r
	}
	rs.Close()
	if len(rows) != 3 || !rows[""].ok || rows[""].in != 420 || rows[""].out != 15 || rows[""].cost != nil || rows[""].model != "mini" ||
		rows["invalid_output"].ok || rows["invalid_output"].in != 400 || rows["provider_error"].ok {
		t.Fatalf("classifier ledger = %+v", rows)
	}
	// the tokens are also kept on the routing decision itself
	var tin, tout *int
	_ = e.seed.QueryRow(e.ctx, `SELECT input_tokens, output_tokens FROM routing_decisions WHERE tenant_id=$1 AND message_id=$2 AND decision_source='ai'`, a.id, msgOK).Scan(&tin, &tout)
	if tin == nil || *tin != 420 || tout == nil || *tout != 15 {
		t.Fatalf("decision tokens = %v %v", tin, tout)
	}

	// the summary: one row per call, tied to the summary it produced; a failure is a row too
	sum := summarySvc(e, &fakeGen{out: "Assunto\nPedido atrasado"}, application.DefaultFlags()).WithLedger(ledger)
	e.session(a.id, admin, func(ctx context.Context) {
		s, created, err := sum.Generate(ctx, topic.ID)
		if err != nil || !created {
			t.Fatalf("summary: %v %v", created, err)
		}
		if n := e.count(`SELECT count(*) FROM ai_usage WHERE tenant_id=$1 AND task='topic_summary' AND success AND ref_id=$2`, a.id, s.ID); n != 1 {
			t.Errorf("summary ledger rows = %d", n)
		}
	})
	failing := summarySvc(e, &fakeGen{err: context.DeadlineExceeded}, application.DefaultFlags()).WithLedger(ledger)
	topic2, _ := e.topicWith(a, admin, "Outro", "x", "y")
	e.attempt(a.id, admin, func(ctx context.Context) { _, _, _ = failing.Generate(ctx, topic2.ID) })
	if e.count(`SELECT count(*) FROM ai_usage WHERE tenant_id=$1 AND task='topic_summary' AND NOT success AND reason='provider_error' AND provider='fake'`, a.id) != 1 {
		t.Fatal("a failed summary call must be accounted with its provider")
	}
	// a replay that does not call the provider is not accounted: no call, no row
	before := e.count(`SELECT count(*) FROM ai_usage WHERE tenant_id=$1`, a.id)
	e.session(a.id, admin, func(ctx context.Context) { _, _, _ = sum.Generate(ctx, topic.ID) })
	if e.count(`SELECT count(*) FROM ai_usage WHERE tenant_id=$1`, a.id) != before {
		t.Fatal("an idempotent replay made no call and must leave no row")
	}
}

type uuidT = uuid.UUID
