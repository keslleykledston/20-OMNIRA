// Package domain is the pure core of the group copilot (ADR-0050): the memory items, the rules that keep them honest (every item
// carries verifiable evidence, a person's confirmation is never overwritten by the AI), the parsers of what a model returns and of a
// WhatsApp export, and the discard rule of an imported file. It imports nothing from a database, a provider or an HTTP layer.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/platform/untrusted"
)

type Kind string

const (
	KindRequest         Kind = "request"
	KindHandledTopic    Kind = "handled_topic"
	KindFact            Kind = "fact"
	KindAgreement       Kind = "agreement"
	KindParticipantRole Kind = "participant_role"
)

func (k Kind) Valid() bool {
	switch k {
	case KindRequest, KindHandledTopic, KindFact, KindAgreement, KindParticipantRole:
		return true
	}
	return false
}

type Tier string

const (
	TierEpisodic     Tier = "episodic"
	TierConsolidated Tier = "consolidated"
)

type Status string

const (
	StatusOpen       Status = "open"
	StatusResolved   Status = "resolved"
	StatusSuperseded Status = "superseded"
)

func (s Status) Valid() bool { return s == StatusOpen || s == StatusResolved || s == StatusSuperseded }

// TruthLevel says how much a person should trust an item (ADR-0050 §2), the same idea as the contact memory of ADR-0020.
type TruthLevel string

const (
	TruthSaidInGroup    TruthLevel = "said_in_group"
	TruthAIInferred     TruthLevel = "ai_inferred"
	TruthHumanConfirmed TruthLevel = "human_confirmed"
)

type Source string

const (
	SourceLive          Source = "live"
	SourceImport        Source = "import"
	SourceHuman         Source = "human"
	SourceConsolidation Source = "consolidation"
)

const (
	MaxSummaryRunes    = 600
	MaxActorRunes      = 120
	MaxQuoteRunes      = 200
	MaxEvidencePerItem = 5

	MinRelevance = 0.0
	MaxRelevance = 10.0
	// NewRelevance is where a fresh item starts; confirming or using it raises it, time lowers it.
	NewRelevance = 1.0
	// RelevanceHalfLife: an item nobody touches loses half its weight in the prompt every 60 days. It is a ranking, never a deletion.
	RelevanceHalfLife = 60 * 24 * time.Hour
)

var (
	ErrInvalidItem = errors.New("groupai: invalid memory item")
	// ErrProtectedItem: a person confirmed or pinned the item, so the AI may only propose a new version, never overwrite it.
	ErrProtectedItem = errors.New("groupai: the item is confirmed or pinned by a person")
)

// Evidence is where an item came from. Ref names the message (a provider message id, or "import:<id>#<n>" for an imported file);
// Quote is a short excerpt of it, verified against the real message text before it is kept.
type Evidence struct {
	Ref   string    `json:"ref"`
	Quote string    `json:"quote,omitempty"`
	At    time.Time `json:"at"`
}

type Item struct {
	ID               uuid.UUID
	TenantID         uuid.UUID
	GroupID          uuid.UUID
	Kind             Kind
	Tier             Tier
	Status           Status
	Truth            TruthLevel
	Summary          string
	ActorLabel       string
	Evidence         []Evidence
	Pinned           bool
	Relevance        float64
	LastReinforcedAt time.Time
	Source           Source
	SourceImportID   *uuid.UUID
	PromptVersion    string
	SupersededBy     *uuid.UUID
	Fingerprint      string
	Version          int
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Protected reports whether the AI is forbidden from changing the item: a person's confirmation or pin wins over any later inference.
func (i Item) Protected() bool { return i.Pinned || i.Truth == TruthHumanConfirmed }

// CleanSummary validates and sanitizes the text of an item: control and bidirectional characters removed, bounded, never a credential.
func CleanSummary(s string) (string, error) {
	out := strings.TrimSpace(untrusted.SanitizeDerivedText(s))
	out = strings.Join(strings.Fields(strings.ReplaceAll(out, "\n", " ")), " ")
	if out == "" {
		return "", fmt.Errorf("%w: empty summary", ErrInvalidItem)
	}
	if untrusted.ContainsCredential(out) {
		return "", fmt.Errorf("%w: the summary looks like it carries a credential", ErrInvalidItem)
	}
	if r := []rune(out); len(r) > MaxSummaryRunes {
		out = strings.TrimSpace(string(r[:MaxSummaryRunes-1])) + "…"
	}
	return out, nil
}

// CleanActor sanitizes who an item is about. It never keeps a phone number: those are masked to a neutral label.
func CleanActor(s string) string {
	out := strings.Join(strings.Fields(untrusted.SanitizeDerivedText(s)), " ")
	out = MaskPII(out)
	if r := []rune(out); len(r) > MaxActorRunes {
		out = string(r[:MaxActorRunes])
	}
	return out
}

// Fingerprint is the dedupe key of an item: the same kind and the same words (case, accents, punctuation and spacing aside) are the
// same fact, so the same pending request is never stored twice.
func Fingerprint(kind Kind, summary string) string {
	sum := sha256.Sum256([]byte(string(kind) + "\n" + normalizeWords(summary)))
	return hex.EncodeToString(sum[:])
}

func normalizeWords(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(stripAccents(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			space = false
		case !space:
			b.WriteRune(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

var accentMap = map[rune]rune{
	'á': 'a', 'à': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e', 'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u', 'ç': 'c', 'ñ': 'n',
}

func stripAccents(s string) string {
	var b strings.Builder
	for _, r := range s {
		if m, ok := accentMap[unicode.ToLower(r)]; ok {
			b.WriteRune(m)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// DecayedRelevance is the weight of an item now: it halves every RelevanceHalfLife since it was last reinforced. A pinned item or one a
// person confirmed never falls under 1, so what people vouched for stays in reach.
func DecayedRelevance(stored float64, lastReinforced, now time.Time, protected bool) float64 {
	age := now.Sub(lastReinforced)
	if age < 0 {
		age = 0
	}
	v := stored * math.Pow(0.5, float64(age)/float64(RelevanceHalfLife))
	if protected && v < 1 {
		v = 1
	}
	return clampRelevance(v)
}

// Reinforced is the stored relevance after the item was used, confirmed or seen again: it is first brought to its present value, then raised.
func Reinforced(stored float64, lastReinforced, now time.Time, protected bool, boost float64) float64 {
	return clampRelevance(DecayedRelevance(stored, lastReinforced, now, protected) + boost)
}

func clampRelevance(v float64) float64 {
	if math.IsNaN(v) || v < MinRelevance {
		return MinRelevance
	}
	if v > MaxRelevance {
		return MaxRelevance
	}
	return v
}

// CleanQuote bounds an excerpt to MaxQuoteRunes on one line.
func CleanQuote(s string) string {
	out := strings.Join(strings.Fields(untrusted.SanitizeDerivedText(s)), " ")
	if r := []rune(out); len(r) > MaxQuoteRunes {
		out = string(r[:MaxQuoteRunes-1]) + "…"
	}
	return out
}

// VerifiedQuote returns the excerpt to keep for a model-proposed quote: the model's own words only if they really occur in the source
// message (comparison without case, accents or spacing); otherwise the beginning of the real message. A model can therefore never put
// words in someone's mouth through the evidence.
func VerifiedQuote(proposed, source string) string {
	src := normalizeWords(source)
	p := normalizeWords(proposed)
	if p != "" && strings.Contains(src, p) {
		return CleanQuote(proposed)
	}
	return CleanQuote(source)
}
