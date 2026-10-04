package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

var t0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

func ent(typ EntityType, key string) string { return Entity{Type: typ, Key: key}.String() }

func TestExtractEntitiesFindsExplicitSubjectsOnly(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"pedido 837 não chegou", []string{"order:837"}},
		{"Meu Pedido nº 00837 atrasou", []string{"order:00837"}},
		{"a NF 992 está errada", []string{"invoice:992"}},
		{"nota fiscal #4410 veio errada", []string{"invoice:4410"}},
		{"o pedido 837 atrasou e a nota 992 veio errada", []string{"order:837", "invoice:992"}},
		{"abri o chamado 813 ontem", []string{"ticket:813"}},
		{"protocolo 20261004", []string{"ticket:20261004"}},
		{"o número de série é abc-123", []string{"device:ABC-123"}},
		{"contrato 5521 renovação", []string{"contract:5521"}},
		{"boleto 123456 venceu", []string{"payment:123456"}},
		{"PEDIDO 837 e pedido 837 de novo", []string{"order:837"}}, // no duplicates
		// things that must NOT become subjects
		{"dou nota 10 para vocês", nil},
		{"tenho 3 pedidos em aberto", nil},
		{"o pedido chegou hoje", nil},
		{"meu telefone é 11 99999", nil},
		{"", nil},
	}
	for _, c := range cases {
		var got []string
		for _, e := range ExtractEntities(c.text) {
			got = append(got, e.String())
		}
		if len(got) != len(c.want) {
			t.Errorf("%q: got %v, want %v", c.text, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q: got %v, want %v", c.text, got, c.want)
			}
		}
	}
}

func input(text string) RoutingInput {
	return RoutingInput{Now: t0, Text: text, EntityTopics: map[string][]uuid.UUID{}}
}

func TestHardEvidenceBeatsEverything(t *testing.T) {
	cfg := DefaultRoutingConfig()
	handoff, other := uuid.New(), uuid.New()
	in := input("qualquer coisa")
	in.HandoffTopic = &handoff
	in.OpenTopics = []TopicBrief{{ID: other, Title: "qualquer coisa", LastActivityAt: t0}}
	in.Focus = []FocusHint{{TopicID: other, Confidence: 1}}
	r := Route(cfg, in)
	if r.Status != RoutingAssigned || *r.Primary != handoff || r.Source != DecisionHandoff || r.Confidence != 1 {
		t.Fatalf("handoff must win: %+v", r)
	}
	explicit := uuid.New()
	in2 := input("x")
	in2.ExplicitTopic = &explicit
	in2.ReplyTopics = []uuid.UUID{other}
	if r := Route(cfg, in2); *r.Primary != explicit || r.Source != DecisionExplicit {
		t.Fatalf("an explicit choice beats a reply: %+v", r)
	}
}

func TestDirectReplyAndQuoteInheritTheRepliedTopic(t *testing.T) {
	cfg := DefaultRoutingConfig()
	a, b := uuid.New(), uuid.New()
	in := input("sim, esse mesmo") // ambiguous text on its own
	in.ReplyTopics = []uuid.UUID{a}
	in.OpenTopics = []TopicBrief{{ID: b, Title: "outro assunto", LastActivityAt: t0.Add(-time.Minute)}, {ID: a, Title: "pedido", LastActivityAt: t0.Add(-time.Hour)}}
	r := Route(cfg, in)
	if r.Status != RoutingAssigned || *r.Primary != a || r.Source != DecisionReply || r.Confidence < 0.97 {
		t.Fatalf("a direct reply is near-deterministic evidence: %+v", r)
	}
}

func TestExactEntityMatchAssignsAndTicketMatchIsStrongest(t *testing.T) {
	cfg := DefaultRoutingConfig()
	delivery, billing := uuid.New(), uuid.New()
	in := input("sobre o pedido 837, ainda nada")
	in.EntityTopics[ent(EntityOrder, "837")] = []uuid.UUID{delivery}
	in.OpenTopics = []TopicBrief{{ID: billing, Title: "cobrança", LastActivityAt: t0}, {ID: delivery, Title: "entrega", LastActivityAt: t0.Add(-3 * time.Hour)}}
	r := Route(cfg, in)
	if r.Status != RoutingAssigned || *r.Primary != delivery || r.Source != DecisionEntity {
		t.Fatalf("entity match must beat recency: %+v", r)
	}
	tk := uuid.New()
	in2 := input("chamado 813 de novo")
	in2.EntityTopics[ent(EntityTicket, "813")] = []uuid.UUID{tk}
	r2 := Route(cfg, in2)
	if *r2.Primary != tk || r2.Confidence != 0.95 {
		t.Fatalf("ticket match: %+v", r2)
	}
}

func TestSameIntentDifferentEntityGoesToDifferentTopics(t *testing.T) {
	cfg := DefaultRoutingConfig()
	a, b := uuid.New(), uuid.New()
	mk := func(text string) RoutingInput {
		in := input(text)
		in.EntityTopics[ent(EntityOrder, "100")] = []uuid.UUID{a}
		in.EntityTopics[ent(EntityOrder, "200")] = []uuid.UUID{b}
		return in
	}
	if r := Route(cfg, mk("o pedido 100 atrasou")); *r.Primary != a {
		t.Fatalf("pedido 100: %+v", r)
	}
	if r := Route(cfg, mk("o pedido 200 atrasou")); *r.Primary != b {
		t.Fatalf("pedido 200: %+v", r)
	}
}

func TestOneMessageNamingTwoSubjectsBecomesMultiTopic(t *testing.T) {
	cfg := DefaultRoutingConfig()
	delivery, invoice := uuid.New(), uuid.New()
	in := input("o pedido 837 atrasou e a nota 992 veio errada")
	in.EntityTopics[ent(EntityOrder, "837")] = []uuid.UUID{delivery}
	in.EntityTopics[ent(EntityInvoice, "992")] = []uuid.UUID{invoice}
	r := Route(cfg, in)
	if r.Status != RoutingMultiTopic || r.Primary == nil || *r.Primary != delivery || len(r.Additional) != 1 || r.Additional[0] != invoice || r.Source != DecisionEntity {
		t.Fatalf("multi-topic: %+v", r)
	}
}

func TestAuthorKeepsContinuingTheirOwnTopicInAGroup(t *testing.T) {
	cfg := DefaultRoutingConfig()
	joao, maria := uuid.New(), uuid.New()
	// João wrote in topic "joao" 2 minutes ago; Maria's topic is the most recent in the group.
	in := input("já passou quatro dias")
	in.ParticipantTopics = []ParticipantTopic{{TopicID: joao, At: t0.Add(-2 * time.Minute)}}
	in.OpenTopics = []TopicBrief{{ID: maria, Title: "nota fiscal 992", LastActivityAt: t0.Add(-time.Minute)}, {ID: joao, Title: "pedido 837", LastActivityAt: t0.Add(-2 * time.Minute)}}
	r := Route(cfg, in)
	if r.Status != RoutingAssigned || *r.Primary != joao {
		t.Fatalf("an author continues their own topic, not the group's latest: %+v", r)
	}
	// An hour later the same inference is only a hint: it needs a person.
	stale := in
	stale.ParticipantTopics = []ParticipantTopic{{TopicID: joao, At: t0.Add(-90 * time.Minute)}}
	stale.OpenTopics = []TopicBrief{{ID: joao, Title: "pedido 837", LastActivityAt: t0.Add(-90 * time.Minute)}}
	if r := Route(cfg, stale); r.Status == RoutingAssigned {
		t.Fatalf("a 90-minute-old author relation must not auto-assign: %+v", r)
	}
}

func TestFocusIsAHintNotAuthority(t *testing.T) {
	cfg := DefaultRoutingConfig()
	topic := uuid.New()
	in := input("e então?")
	in.Focus = []FocusHint{{TopicID: topic, Confidence: 1, ParticipantScoped: true}}
	r := Route(cfg, in)
	if r.Status != RoutingAmbiguous || r.Candidates[0].Score >= cfg.AutoAssign {
		t.Fatalf("a focus hint alone must ask a person (0.82 < 0.85): %+v", r)
	}
	// expired focus is ignored
	past := t0.Add(-time.Minute)
	in.Focus[0].ExpiresAt = &past
	if r := Route(cfg, in); r.Status != RoutingUnassigned {
		t.Fatalf("expired focus: %+v", r)
	}
	// focus + another agreeing signal crosses the line
	in.Focus[0].ExpiresAt = nil
	in.OpenTopics = []TopicBrief{{ID: topic, Title: "pedido", LastActivityAt: t0.Add(-time.Minute)}}
	if r := Route(cfg, in); r.Status != RoutingAssigned || *r.Primary != topic {
		t.Fatalf("focus + recency agree: %+v", r)
	}
}

func TestTwoOpenTopicsAndNoEvidenceIsAmbiguousNeverAGuess(t *testing.T) {
	cfg := DefaultRoutingConfig()
	a, b := uuid.New(), uuid.New()
	in := input("e agora, como fica?")
	in.OpenTopics = []TopicBrief{{ID: a, Title: "entrega", LastActivityAt: t0.Add(-5 * time.Minute)}, {ID: b, Title: "cobrança", LastActivityAt: t0.Add(-6 * time.Minute)}}
	r := Route(cfg, in)
	// only the most recent open topic gets the recency hint (0.62): above "ambiguous", far below "auto"
	if r.Status != RoutingAmbiguous || r.Primary != nil {
		t.Fatalf("expected ambiguous without a primary: %+v", r)
	}
}

func TestNoCandidateProposesANewTopicOnlyWhenTheMessageNamesASubject(t *testing.T) {
	cfg := DefaultRoutingConfig()
	r := Route(cfg, input("pedido 837 não chegou"))
	if r.Status != RoutingNewTopic || r.NewTopic == nil || r.NewTopic.Title != "Pedido 837" || len(r.NewTopic.Entities) != 1 {
		t.Fatalf("new topic: %+v", r)
	}
	if r := Route(cfg, input("oi, tudo bem?")); r.Status != RoutingUnassigned || r.NewTopic != nil {
		t.Fatalf("nothing to go on: %+v", r)
	}
	if r := Route(cfg, input("")); r.Status != RoutingUnassigned {
		t.Fatalf("empty (media only): %+v", r)
	}
}

func TestLexicalMatchIsWeakAndNeverAutoAssigns(t *testing.T) {
	cfg := DefaultRoutingConfig()
	topic := uuid.New()
	in := input("quero o reembolso do estorno")
	in.OpenTopics = []TopicBrief{{ID: topic, Title: "Estorno e reembolso", LastActivityAt: t0.Add(-5 * time.Hour)}}
	r := Route(cfg, in)
	if r.Status == RoutingAssigned {
		t.Fatalf("words alone must not auto-assign: %+v", r)
	}
	if r.Status != RoutingAmbiguous || r.Candidates[0].Signals[0].Kind != SignalLexical {
		t.Fatalf("a strong word overlap should surface as a candidate: %+v", r)
	}
}

func TestTiesBetweenStrongNonHardSignalsAreAmbiguous(t *testing.T) {
	cfg := DefaultRoutingConfig()
	a, b := uuid.New(), uuid.New()
	in := input("e então?")
	in.ParticipantTopics = []ParticipantTopic{{TopicID: a, At: t0.Add(-time.Minute)}, {TopicID: b, At: t0.Add(-2 * time.Minute)}}
	r := Route(cfg, in)
	if r.Status != RoutingAmbiguous || len(r.Candidates) != 2 {
		t.Fatalf("two equally plausible topics must not be guessed: %+v", r)
	}
}

func TestRoutingIsDeterministicAndThresholdsAreCentral(t *testing.T) {
	cfg := DefaultRoutingConfig()
	a := uuid.New()
	in := input("pedido 837")
	in.EntityTopics[ent(EntityOrder, "837")] = []uuid.UUID{a}
	first := Route(cfg, in)
	for i := 0; i < 20; i++ {
		if again := Route(cfg, in); again.Status != first.Status || *again.Primary != *first.Primary || again.Confidence != first.Confidence {
			t.Fatal("the router must be a pure function of its input")
		}
	}
	// raising the bar changes the outcome without touching code
	strict := cfg
	strict.AutoAssign = 0.99
	if r := Route(strict, in); r.Status == RoutingAssigned {
		t.Fatalf("thresholds must come from the config: %+v", r)
	}
}

func TestANewlyNamedSubjectWeakensContextualHints(t *testing.T) {
	cfg := DefaultRoutingConfig()
	orderTopic := uuid.New()
	base := input("a NF 992 está errada") // names an invoice no topic holds
	base.OpenTopics = []TopicBrief{{ID: orderTopic, Title: "Pedido 837", LastActivityAt: t0.Add(-time.Minute)}}
	// the only evidence for the old topic is that it is the latest one: not enough to hijack a new subject
	r := Route(cfg, base)
	if r.Status != RoutingNewTopic || r.NewTopic == nil || r.NewTopic.Title != "Nota fiscal 992" {
		t.Fatalf("a named new subject with only a recency hint opens a topic: %+v", r)
	}
	// the same author's continuity (0.88) is also demoted to a question for a person, never auto-assigned
	cont := base
	cont.ParticipantTopics = []ParticipantTopic{{TopicID: orderTopic, At: t0.Add(-time.Minute)}}
	if r := Route(cfg, cont); r.Status != RoutingAmbiguous {
		t.Fatalf("a new subject named by the author who was just on another one must ask: %+v", r)
	}
	// but hard evidence is untouched: a direct reply still wins
	reply := base
	reply.ReplyTopics = []uuid.UUID{orderTopic}
	if r := Route(cfg, reply); r.Status != RoutingAssigned || *r.Primary != orderTopic {
		t.Fatalf("a reply is not weakened by a newly named subject: %+v", r)
	}
	// and when the named subject IS held, nothing is weakened
	known := base
	known.EntityTopics = map[string][]uuid.UUID{ent(EntityInvoice, "992"): {orderTopic}}
	if r := Route(cfg, known); r.Status != RoutingAssigned || *r.Primary != orderTopic {
		t.Fatalf("a held subject matches: %+v", r)
	}
}
