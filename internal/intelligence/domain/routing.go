package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// RoutingStatus mirrors routing_decisions.status.
type RoutingStatus string

const (
	RoutingAssigned   RoutingStatus = "assigned"
	RoutingAmbiguous  RoutingStatus = "ambiguous"
	RoutingMultiTopic RoutingStatus = "multi_topic"
	RoutingNewTopic   RoutingStatus = "new_topic"
	RoutingUnassigned RoutingStatus = "unassigned"
)

// SignalKind is one kind of evidence, ordered by authority (the first three are hard evidence).
type SignalKind string

const (
	SignalHandoff     SignalKind = "handoff"
	SignalExplicit    SignalKind = "explicit"
	SignalReply       SignalKind = "reply"
	SignalTicket      SignalKind = "ticket_entity"
	SignalEntity      SignalKind = "entity"
	SignalParticipant SignalKind = "participant"
	SignalFocus       SignalKind = "focus"
	SignalRecent      SignalKind = "recent"
	SignalLexical     SignalKind = "lexical"
)

func (k SignalKind) hard() bool {
	return k == SignalHandoff || k == SignalExplicit || k == SignalReply
}

func (k SignalKind) entityBased() bool { return k == SignalTicket || k == SignalEntity }

// DecisionSource maps the strongest signal to the decision source stored on links.
func (k SignalKind) DecisionSource() DecisionSource {
	switch k {
	case SignalHandoff:
		return DecisionHandoff
	case SignalExplicit:
		return DecisionExplicit
	case SignalReply:
		return DecisionReply
	case SignalTicket, SignalEntity:
		return DecisionEntity
	}
	return DecisionRule
}

// RoutingConfig holds every threshold and weight in one place; nothing is hard-coded in the scorer.
type RoutingConfig struct {
	AutoAssign float64 // >= : attach automatically
	Ambiguous  float64 // between Ambiguous and AutoAssign: a person decides; below: new topic / unassigned
	// A close second place below the winner by less than this margin is a tie (ambiguous) unless the winner is hard evidence.
	TieMargin float64

	WeightHandoff, WeightExplicit, WeightReply, WeightTicket, WeightEntity float64
	WeightParticipantContinues, WeightParticipantRecent                    float64
	WeightFocusParticipant, WeightFocusConversation, WeightRecent          float64
	MaxLexical                                                             float64
	AgreementBonus                                                         float64 // per additional distinct signal on the same topic
	// NewSubjectPenalty scales every NON-hard contextual signal when the message names a subject (an entity) that no open
	// topic holds: an explicit new identifier is better evidence of a new subject than a vague hint about the old one.
	NewSubjectPenalty float64

	ParticipantContinuesWithin time.Duration
	ParticipantRecentWithin    time.Duration
	RecentWithin               time.Duration
	MaxCandidates              int
}

func DefaultRoutingConfig() RoutingConfig {
	return RoutingConfig{
		AutoAssign: 0.85, Ambiguous: 0.60, TieMargin: 0.08,
		WeightHandoff: 1, WeightExplicit: 1, WeightReply: 0.97, WeightTicket: 0.95, WeightEntity: 0.92,
		WeightParticipantContinues: 0.88, WeightParticipantRecent: 0.70,
		WeightFocusParticipant: 0.82, WeightFocusConversation: 0.72, WeightRecent: 0.62,
		MaxLexical: 0.60, AgreementBonus: 0.03, NewSubjectPenalty: 0.8,
		ParticipantContinuesWithin: 15 * time.Minute, ParticipantRecentWithin: 2 * time.Hour, RecentWithin: 30 * time.Minute,
		MaxCandidates: 4,
	}
}

// TopicBrief is what the router needs to know about a candidate topic.
type TopicBrief struct {
	ID             uuid.UUID
	Title          string
	Intent         string
	LastActivityAt time.Time
}

// ParticipantTopic: a topic where this same participant last wrote, and when.
type ParticipantTopic struct {
	TopicID uuid.UUID
	At      time.Time
}

// FocusHint is a stored hint about what a participant (or the whole conversation) is currently talking about.
type FocusHint struct {
	TopicID           uuid.UUID
	Confidence        float64
	ParticipantScoped bool
	ExpiresAt         *time.Time
}

// RoutingInput is everything known about one message, assembled by the application layer. The router itself touches
// no storage: the same input always gives the same result.
type RoutingInput struct {
	Now  time.Time
	Text string
	// Hard evidence, when present.
	HandoffTopic  *uuid.UUID
	ExplicitTopic *uuid.UUID
	ReplyTopics   []uuid.UUID // topics of the message this one replies to / quotes
	// EntityTopics: for each entity named in the text, the open topics already holding it.
	EntityTopics map[string][]uuid.UUID // key = Entity.String()
	// Contextual evidence.
	ParticipantTopics []ParticipantTopic
	Focus             []FocusHint
	OpenTopics        []TopicBrief // open topics of the same conversation/group, for the recency and lexical heuristics
}

// Signal is one piece of evidence for one topic.
type Signal struct {
	Kind   SignalKind `json:"kind"`
	Topic  uuid.UUID  `json:"topic_id"`
	Score  float64    `json:"score"`
	Detail string     `json:"detail,omitempty"`
}

// TopicCandidate is a topic with its combined score and the evidence behind it.
type TopicCandidate struct {
	TopicID uuid.UUID
	Score   float64
	Signals []Signal
}

func (c TopicCandidate) hard() bool {
	for _, s := range c.Signals {
		if s.Kind.hard() {
			return true
		}
	}
	return false
}

func (c TopicCandidate) strongest() SignalKind {
	best := c.Signals[0]
	for _, s := range c.Signals[1:] {
		if s.Score > best.Score {
			best = s
		}
	}
	return best.Kind
}

// NewTopicProposal describes a topic the router would create; creating it is the caller's decision (feature flag).
type NewTopicProposal struct {
	Title    string
	Entities []Entity
}

// RoutingResult is the structured outcome of routing one message.
type RoutingResult struct {
	Status     RoutingStatus
	Primary    *uuid.UUID
	Additional []uuid.UUID // multi_topic: the other topics
	Candidates []TopicCandidate
	Confidence float64
	Source     DecisionSource
	Signals    []Signal
	NewTopic   *NewTopicProposal
	Entities   []Entity
}

// Route decides where one message belongs. Evidence, in order of authority: handoff token, explicit choice, direct
// reply, exact ticket/entity match, then what the same participant just said, the stored focus, the most recent topic
// and a lexical match. It never calls a model.
func Route(cfg RoutingConfig, in RoutingInput) RoutingResult {
	entities := ExtractEntities(in.Text)
	signals := map[uuid.UUID][]Signal{}
	add := func(k SignalKind, topic uuid.UUID, score float64, detail string) {
		if topic == uuid.Nil || score <= 0 {
			return
		}
		signals[topic] = append(signals[topic], Signal{Kind: k, Topic: topic, Score: round3(score), Detail: detail})
	}

	if in.HandoffTopic != nil {
		add(SignalHandoff, *in.HandoffTopic, cfg.WeightHandoff, "private handoff token")
	}
	if in.ExplicitTopic != nil {
		add(SignalExplicit, *in.ExplicitTopic, cfg.WeightExplicit, "explicit selection")
	}
	for _, t := range in.ReplyTopics {
		add(SignalReply, t, cfg.WeightReply, "direct reply or quote")
	}
	// entity evidence keeps mention order for the multi-topic case
	var entityOrder []uuid.UUID
	for _, e := range entities {
		kind, weight := SignalEntity, cfg.WeightEntity
		if e.Type == EntityTicket {
			kind, weight = SignalTicket, cfg.WeightTicket
		}
		for _, t := range in.EntityTopics[e.String()] {
			add(kind, t, weight, e.String())
			entityOrder = append(entityOrder, t)
		}
	}
	// Contextual hints lose weight when the message introduces a subject nobody holds yet.
	ctxFactor := 1.0
	if len(entities) > 0 && len(entityOrder) == 0 && cfg.NewSubjectPenalty > 0 {
		ctxFactor = cfg.NewSubjectPenalty
	}
	addCtx := func(k SignalKind, topic uuid.UUID, score float64, detail string) {
		add(k, topic, score*ctxFactor, detail)
	}
	for _, p := range in.ParticipantTopics {
		age := in.Now.Sub(p.At)
		switch {
		case age >= 0 && age <= cfg.ParticipantContinuesWithin:
			addCtx(SignalParticipant, p.TopicID, cfg.WeightParticipantContinues, "same author, last topic within "+cfg.ParticipantContinuesWithin.String())
		case age >= 0 && age <= cfg.ParticipantRecentWithin:
			addCtx(SignalParticipant, p.TopicID, cfg.WeightParticipantRecent, "same author, recent topic")
		}
	}
	for _, f := range in.Focus {
		if f.ExpiresAt != nil && !f.ExpiresAt.After(in.Now) {
			continue
		}
		w := cfg.WeightFocusConversation
		if f.ParticipantScoped {
			w = cfg.WeightFocusParticipant
		}
		if f.Confidence > 0 && f.Confidence < 1 {
			w = minf(w, 0.5+0.5*f.Confidence)
		}
		addCtx(SignalFocus, f.TopicID, w, "focus hint")
	}
	// recency: only the most recently active open topic, and only inside the window
	var latest *TopicBrief
	for i := range in.OpenTopics {
		if latest == nil || in.OpenTopics[i].LastActivityAt.After(latest.LastActivityAt) {
			latest = &in.OpenTopics[i]
		}
	}
	if latest != nil && in.Now.Sub(latest.LastActivityAt) <= cfg.RecentWithin {
		addCtx(SignalRecent, latest.ID, cfg.WeightRecent, "most recent open topic")
	}
	if words := tokens(in.Text); len(words) > 0 {
		for _, t := range in.OpenTopics {
			if s := lexicalScore(cfg, words, t.Title+" "+t.Intent); s > 0 {
				addCtx(SignalLexical, t.ID, s, "shares words with the topic")
			}
		}
	}

	candidates := combine(cfg, signals)
	res := RoutingResult{Entities: entities, Candidates: capCandidates(candidates, cfg.MaxCandidates)}
	for _, c := range res.Candidates {
		res.Signals = append(res.Signals, c.Signals...)
	}

	// Several topics named by distinct entities in one message: one message, several topics (no duplication).
	if topics := distinctInOrder(entityOrder); len(topics) > 1 {
		res.Status, res.Source = RoutingMultiTopic, DecisionEntity
		res.Primary = &topics[0]
		res.Additional = append(res.Additional, topics[1:]...)
		res.Confidence = scoreOf(candidates, topics[0])
		for _, t := range topics {
			res.Confidence = minf(res.Confidence, scoreOf(candidates, t))
		}
		return res
	}

	if len(candidates) == 0 {
		return noCandidate(res, entities)
	}
	top := candidates[0]
	if top.Score >= cfg.AutoAssign {
		if !top.hard() && len(candidates) > 1 && candidates[1].Score >= cfg.AutoAssign && top.Score-candidates[1].Score < cfg.TieMargin {
			return ambiguous(res, cfg)
		}
		id := top.TopicID
		res.Status, res.Primary, res.Confidence, res.Source = RoutingAssigned, &id, top.Score, top.strongest().DecisionSource()
		return res
	}
	if top.Score >= cfg.Ambiguous {
		return ambiguous(res, cfg)
	}
	return noCandidate(res, entities)
}

func ambiguous(res RoutingResult, cfg RoutingConfig) RoutingResult {
	res.Status, res.Source = RoutingAmbiguous, DecisionRule
	res.Confidence = res.Candidates[0].Score
	var keep []TopicCandidate
	for _, c := range res.Candidates {
		if c.Score >= cfg.Ambiguous {
			keep = append(keep, c)
		}
	}
	res.Candidates = keep
	return res
}

// noCandidate: nothing reaches the ambiguity threshold. A message that names a subject nobody has opened yet proposes a
// new topic; anything else stays unassigned for a person.
func noCandidate(res RoutingResult, entities []Entity) RoutingResult {
	res.Source = DecisionRule
	if len(entities) > 0 {
		res.Status = RoutingNewTopic
		res.NewTopic = &NewTopicProposal{Title: topicTitle(entities[0]), Entities: entities}
		return res
	}
	res.Status = RoutingUnassigned
	return res
}

func topicTitle(e Entity) string {
	return fmt.Sprintf("%s %s", e.Type.Label(), e.Key)
}

// combine merges the evidence per topic: the strongest signal sets the score, each additional distinct kind adds a
// small agreement bonus (never above 0.99 unless the strongest is already 1.0).
func combine(cfg RoutingConfig, signals map[uuid.UUID][]Signal) []TopicCandidate {
	out := make([]TopicCandidate, 0, len(signals))
	for topic, sigs := range signals {
		best, kinds := 0.0, map[SignalKind]bool{}
		for _, s := range sigs {
			if s.Score > best {
				best = s.Score
			}
			kinds[s.Kind] = true
		}
		score := best
		if best < 1 {
			score = minf(0.99, best+cfg.AgreementBonus*float64(len(kinds)-1))
		}
		sort.SliceStable(sigs, func(i, j int) bool { return sigs[i].Score > sigs[j].Score })
		out = append(out, TopicCandidate{TopicID: topic, Score: round3(score), Signals: sigs})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].hard() != out[j].hard() {
			return out[i].hard()
		}
		return out[i].TopicID.String() < out[j].TopicID.String() // deterministic
	})
	return out
}

func capCandidates(c []TopicCandidate, n int) []TopicCandidate {
	if n > 0 && len(c) > n {
		return c[:n]
	}
	return c
}

func distinctInOrder(ids []uuid.UUID) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	var out []uuid.UUID
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func scoreOf(c []TopicCandidate, id uuid.UUID) float64 {
	for _, x := range c {
		if x.TopicID == id {
			return x.Score
		}
	}
	return 0
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func round3(f float64) float64 { return float64(int64(f*1000+0.5)) / 1000 }

var accentFold = strings.NewReplacer("á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a", "é", "e", "ê", "e", "è", "e", "í", "i", "î", "i",
	"ó", "o", "ô", "o", "õ", "o", "ò", "o", "ú", "u", "ü", "u", "ç", "c")

var stopwords = map[string]bool{"para": true, "como": true, "isso": true, "esse": true, "essa": true, "sobre": true, "ainda": true, "estou": true,
	"minha": true, "meu": true, "mais": true, "muito": true, "pode": true, "podem": true, "favor": true, "obrigado": true, "obrigada": true,
	"voces": true, "vocês": true, "tenho": true, "queria": true, "gostaria": true, "quando": true, "onde": true, "porque": true}

func tokens(s string) map[string]bool {
	s = accentFold.Replace(strings.ToLower(s))
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(w) >= 4 && !stopwords[w] {
			out[w] = true
		}
	}
	return out
}

// lexicalScore is a weak signal: the share of the topic's own words the message repeats, capped by MaxLexical.
func lexicalScore(cfg RoutingConfig, words map[string]bool, topicText string) float64 {
	tw := tokens(topicText)
	if len(tw) == 0 {
		return 0
	}
	shared := 0
	for w := range tw {
		if words[w] {
			shared++
		}
	}
	if shared == 0 {
		return 0
	}
	return minf(cfg.MaxLexical, 0.30+0.30*float64(shared)/float64(len(tw)))
}
