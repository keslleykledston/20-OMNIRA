package application_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/adapters"
	. "github.com/omnira/omnira/internal/flows/application"
	"github.com/omnira/omnira/internal/flows/flowstest"
	"github.com/omnira/omnira/internal/flows/ports"
)

// onlyQueues is a ResourceChecker that cannot tell about sectors (an unusual wiring): publishing must fail closed.
type onlyQueues struct{ ports.ResourceChecker }

func TestPublishChecksSectorReferencesAgainstTheTenantAndFailsClosed(t *testing.T) {
	env := flowstest.New(t)
	repo := adapters.NewPostgresFlowRepository(env.App)
	mine, foreign := uuid.New(), uuid.New()
	for sector, tenant := range map[uuid.UUID]uuid.UUID{mine: env.TenantA, foreign: env.TenantB} {
		if _, err := env.Seed.Exec(context.Background(), `INSERT INTO sectors (id, tenant_id, name) VALUES ($1,$2,$3)`, sector, tenant, "S-"+sector.String()[:8]); err != nil {
			t.Fatal(err)
		}
	}
	flow := func(sector uuid.UUID) string {
		return wf(fmt.Sprintf(`{"id":"start","type":"trigger"},{"id":"c","type":"check_schedule","config":{"sector":%q}},{"id":"a","type":"end"},{"id":"b","type":"end"}`, sector),
			strings.Join([]string{edge("1", "start", "next", "c"), edge("2", "c", "open", "a"), edge("3", "c", "closed", "b")}, ","), "")
	}
	has := func(issues []string, code string) bool {
		for _, i := range issues {
			if i == code {
				return true
			}
		}
		return false
	}
	check := func(cp *ControlPlane, sector uuid.UUID) (codes []string) {
		env.AsUser(t, env.TenantA, env.UserA, func(ctx context.Context) {
			issues, err := cp.ValidateRaw(ctx, []byte(flow(sector)))
			if err != nil {
				t.Fatal(err)
			}
			for _, i := range issues {
				if i.Severity == "error" {
					codes = append(codes, i.Code)
				}
			}
		})
		return
	}
	cp := NewControlPlane(repo, repo, nil)
	if got := check(cp, mine); len(got) != 0 {
		t.Fatalf("the tenant's own sector must publish: %v", got)
	}
	if got := check(cp, foreign); !has(got, "resource_not_found") {
		t.Fatalf("another company's sector must be refused: %v", got)
	}
	if got := check(cp, uuid.New()); !has(got, "resource_not_found") {
		t.Fatalf("an unknown sector must be refused: %v", got)
	}
	if got := check(NewControlPlane(repo, onlyQueues{repo}, nil), mine); !has(got, "resource_unverifiable") {
		t.Fatalf("a checker that cannot verify sectors must fail closed: %v", got)
	}
}

// ---- StartRouted: the routing engine's "flow" destination (ADR-0044) ----

func (x *rt) startRouted(conv, flow uuid.UUID, key string) (out string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = platformdbSystem(ctx, x.env, x.tenant, func(sc context.Context) error {
		out, err = x.eng.StartRouted(sc, conv, flow, key)
		return err
	})
	return out, err
}

func (x *rt) runCount(conv uuid.UUID) (n int) {
	_ = x.env.Seed.QueryRow(context.Background(), `SELECT count(*) FROM flow_runs WHERE conversation_id=$1`, conv).Scan(&n)
	return
}

func TestStartRoutedStartsOneFlowIdempotentlyAndNeverOverAnotherRun(t *testing.T) {
	x := newRT(t)
	flow := x.publish("fora-do-horario", askNameFlow)
	if out, err := x.startRouted(x.conv, flow, "routing-flow:k1"); err != nil || out != RoutedStarted {
		t.Fatalf("start: %q %v", out, err)
	}
	if st, mode, _ := x.runStatus(); st != "waiting_input" || mode != "bot" {
		t.Fatalf("the flow asked its question and waits for the customer: %s %s", st, mode)
	}
	if got := strings.Join(x.fx.SentTexts(), "|"); got != "Qual o seu nome?" {
		t.Fatalf("the flow spoke first, with no customer message: %q", got)
	}
	var trig string
	_ = x.env.Seed.QueryRow(context.Background(), `SELECT trigger_event_id FROM flow_runs WHERE conversation_id=$1`, x.conv).Scan(&trig)
	if trig != "routing-flow:k1" {
		t.Fatalf("the run is keyed by the routing request: %q", trig)
	}
	// the same request again is a no-op (the routing worker retried)
	if out, err := x.startRouted(x.conv, flow, "routing-flow:k1"); err != nil || out != RoutedStarted || x.runCount(x.conv) != 1 {
		t.Fatalf("same key: %q %v runs=%d", out, err, x.runCount(x.conv))
	}
	// another request while the flow runs: busy (a flow is never started over another one), and nothing is created
	if out, err := x.startRouted(x.conv, flow, "routing-flow:k2"); err != nil || out != RoutedBusy || x.runCount(x.conv) != 1 {
		t.Fatalf("busy: %q %v runs=%d", out, err, x.runCount(x.conv))
	}
	// the customer answers: the same run resumes through the ordinary inbound path
	if out, err := x.deliver(x.say("Carlos"), false); err != nil || out != OutcomeResumed {
		t.Fatalf("the customer's answer resumes the routed flow: %v %v", out, err)
	}
	if st, _, _ := x.runStatus(); st != "completed" {
		t.Fatalf("completed: %s", st)
	}
}

func TestStartRoutedRefusesWhatTheAutomationMayNotTake(t *testing.T) {
	x := newRT(t)
	flow := x.publish("fora-do-horario", askNameFlow)
	// a conversation somebody already owns
	owned, _ := x.env.SeedConversation(t, x.tenant, "none")
	if _, err := x.env.Seed.Exec(context.Background(), `UPDATE conversations SET assigned_to_user_id=$2 WHERE id=$1`, owned, x.env.UserA); err != nil {
		t.Fatal(err)
	}
	// a closed one
	closed, _ := x.env.SeedConversation(t, x.tenant, "none")
	if _, err := x.env.Seed.Exec(context.Background(), `UPDATE conversations SET status='closed', closed_at=now() WHERE id=$1`, closed); err != nil {
		t.Fatal(err)
	}
	// a conversation that is not customer service (partner, supplier...)
	internal, _ := x.env.SeedConversation(t, x.tenant, "none")
	if _, err := x.env.Seed.Exec(context.Background(), `UPDATE conversations SET conversation_kind='external_other' WHERE id=$1`, internal); err != nil {
		t.Fatal(err)
	}
	for name, conv := range map[string]uuid.UUID{"owned": owned, "closed": closed, "not customer service": internal, "unknown": uuid.New()} {
		if out, err := x.startRouted(conv, flow, "routing-flow:"+name); err != nil || out != RoutedUnavailable {
			t.Errorf("%s: %q %v, want unavailable", name, out, err)
		}
		if x.runCount(conv) != 0 {
			t.Errorf("%s: a run was created", name)
		}
	}
	// the flow side: unknown id, not published, another type, another company's flow, a channel filter that does not match
	fresh := func() uuid.UUID { c, _ := x.env.SeedConversation(t, x.tenant, "none"); return c }
	draft := uuid.New()
	x.env.AsUser(t, x.tenant, x.env.UserA, func(ctx context.Context) {
		f, err := x.cp.Create(ctx, CreateInput{Slug: "rascunho", Name: "rascunho"})
		if err != nil {
			t.Fatal(err)
		}
		draft = f.ID
	})
	other := x.publish("outro-tipo", askNameFlow)
	if _, err := x.env.Seed.Exec(context.Background(), `UPDATE flows SET flow_type='AI_WORKFLOW' WHERE id=$1`, other); err != nil {
		t.Fatal(err)
	}
	filtered := x.publish("so-meta", askNameFlow)
	if _, err := x.env.Seed.Exec(context.Background(), `UPDATE flows SET trigger_filter='{"providers":["meta_cloud"]}'::jsonb WHERE id=$1`, filtered); err != nil {
		t.Fatal(err)
	}
	foreign := uuid.New()
	if _, err := x.env.Seed.Exec(context.Background(), `INSERT INTO flows (id, tenant_id, slug, name) VALUES ($1,$2,'estrangeiro','x')`, foreign, x.env.TenantB); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]uuid.UUID{"unknown flow": uuid.New(), "draft": draft, "another type": other, "channel filter": filtered, "another company's": foreign} {
		c := fresh()
		if out, err := x.startRouted(c, id, "routing-flow:"+name); err != nil || out != RoutedUnavailable || x.runCount(c) != 0 {
			t.Errorf("%s: %q %v runs=%d, want unavailable and no run", name, out, err, x.runCount(c))
		}
	}
	// an empty key is refused (no idempotency, no start)
	if out, _ := x.startRouted(fresh(), flow, ""); out != RoutedUnavailable {
		t.Errorf("empty key: %q", out)
	}
}
