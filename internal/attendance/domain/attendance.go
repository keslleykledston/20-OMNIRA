// Package domain is the attendance lifecycle (ADR-0020): finalizing a conversation and remembering what is pending or was
// promised to the contact. Pure rules, no I/O.
package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/platform/untrusted"
)

type CloseReason string

const (
	ReasonResolved    CloseReason = "resolved"
	ReasonNoResponse  CloseReason = "no_response"
	ReasonDuplicate   CloseReason = "duplicate"
	ReasonSpam        CloseReason = "spam"
	ReasonTransferred CloseReason = "transferred"
	ReasonOther       CloseReason = "other"
)

func (r CloseReason) Valid() bool {
	switch r {
	case ReasonResolved, ReasonNoResponse, ReasonDuplicate, ReasonSpam, ReasonTransferred, ReasonOther:
		return true
	}
	return false
}

// CloseSource says who finalized: the attendant who owns the conversation, a supervisor acting on someone else's (or an
// unassigned) one, or the system (reserved for the later inactivity rule).
type CloseSource string

const (
	SourceAgent      CloseSource = "agent"
	SourceSupervisor CloseSource = "supervisor"
	SourceSystem     CloseSource = "system"
)

// Truth is the ADR-0017 scale reduced to what attendance text can be: confirmed by a person, or inferred by a model.
type Truth string

const (
	TruthAgentConfirmed Truth = "agent_confirmed"
	TruthAIInferred     Truth = "ai_inferred"
)

func (t Truth) Valid() bool { return t == TruthAgentConfirmed || t == TruthAIInferred }

type FollowUpKind string

const (
	KindPending FollowUpKind = "pending" // something to do
	KindPromise FollowUpKind = "promise" // something promised to the contact
	KindInfo    FollowUpKind = "info"    // a fact worth remembering
)

func (k FollowUpKind) Valid() bool { return k == KindPending || k == KindPromise || k == KindInfo }

type FollowUpStatus string

const (
	StatusOpen    FollowUpStatus = "open"
	StatusDone    FollowUpStatus = "done"
	StatusDropped FollowUpStatus = "dropped"
)

const (
	MaxNote       = 1000
	MaxSummary    = 4000
	MaxFollowUp   = 500
	MaxResolution = 500
	MaxFollowUps  = 20
)

var (
	ErrNotFound       = errors.New("attendance: not found")
	ErrForbidden      = errors.New("attendance: not allowed")
	ErrInvalid        = errors.New("attendance: invalid input")
	ErrNotAContact    = errors.New("attendance: only a conversation with a contact can be finalized")
	ErrAlreadyHandled = errors.New("attendance: the item is already resolved")
)

// FollowUpInput is one item the person lists while finalizing.
type FollowUpInput struct {
	Kind        FollowUpKind `json:"kind"`
	Text        string       `json:"text"`
	OwnerUserID *uuid.UUID   `json:"owner_user_id,omitempty"`
	DueAt       *time.Time   `json:"due_at,omitempty"`
}

// FinalizeInput is everything the person provides. Tenant and actor come from the TenantContext, never from here.
type FinalizeInput struct {
	ConversationID uuid.UUID       `json:"-"`
	Reason         CloseReason     `json:"reason"`
	Note           string          `json:"note"`
	Summary        string          `json:"summary"`
	SummaryTruth   Truth           `json:"summary_truth"`
	FollowUps      []FollowUpInput `json:"follow_ups"`
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

func checkText(name, v string, max int, required bool) (string, error) {
	v = strings.TrimSpace(v)
	if required && v == "" {
		return "", invalid("%s is required", name)
	}
	if utf8.RuneCountInString(v) > max {
		return "", invalid("%s is longer than %d characters", name, max)
	}
	if !utf8.ValidString(v) {
		return "", invalid("%s is not valid text", name)
	}
	if untrusted.ContainsCredential(v) {
		return "", invalid("%s looks like it contains a credential; remove it", name)
	}
	return v, nil
}

// Normalize validates the input and returns it trimmed. It never mutates the receiver.
func (in FinalizeInput) Normalize() (FinalizeInput, error) {
	out := in
	if in.ConversationID == uuid.Nil {
		return out, invalid("conversation is required")
	}
	if !in.Reason.Valid() {
		return out, invalid("reason must be one of resolved, no_response, duplicate, spam, transferred, other")
	}
	if out.SummaryTruth == "" {
		out.SummaryTruth = TruthAgentConfirmed
	}
	if !out.SummaryTruth.Valid() {
		return out, invalid("summary_truth must be agent_confirmed or ai_inferred")
	}
	var err error
	if out.Note, err = checkText("note", in.Note, MaxNote, false); err != nil {
		return out, err
	}
	if out.Summary, err = checkText("summary", in.Summary, MaxSummary, false); err != nil {
		return out, err
	}
	if len(in.FollowUps) > MaxFollowUps {
		return out, invalid("at most %d follow-up items", MaxFollowUps)
	}
	out.FollowUps = make([]FollowUpInput, 0, len(in.FollowUps))
	for i, f := range in.FollowUps {
		if !f.Kind.Valid() {
			return out, invalid("follow-up %d: kind must be pending, promise or info", i+1)
		}
		text, err := checkText(fmt.Sprintf("follow-up %d text", i+1), f.Text, MaxFollowUp, true)
		if err != nil {
			return out, err
		}
		f.Text = text
		out.FollowUps = append(out.FollowUps, f)
	}
	return out, nil
}

// Closure is the immutable record of a finalized conversation.
type Closure struct {
	ID                 uuid.UUID   `json:"id"`
	TenantID           uuid.UUID   `json:"-"`
	ConversationID     uuid.UUID   `json:"conversation_id"`
	ContactID          uuid.UUID   `json:"contact_id"`
	ClosedBy           *uuid.UUID  `json:"closed_by_user_id"`
	Source             CloseSource `json:"source"`
	Reason             CloseReason `json:"reason"`
	Note               string      `json:"note"`
	Summary            string      `json:"summary"`
	SummaryTruth       Truth       `json:"summary_truth"`
	LocalTicketsClosed int         `json:"local_tickets_closed"`
	TicketsKept        int         `json:"tickets_kept"`
	CreatedAt          time.Time   `json:"created_at"`
}

// FollowUp is something to remember about a contact beyond the conversation it came from.
type FollowUp struct {
	ID             uuid.UUID      `json:"id"`
	TenantID       uuid.UUID      `json:"-"`
	ContactID      uuid.UUID      `json:"contact_id"`
	ConversationID uuid.UUID      `json:"conversation_id"`
	ClosureID      *uuid.UUID     `json:"closure_id"`
	Kind           FollowUpKind   `json:"kind"`
	Text           string         `json:"text"`
	OwnerUserID    *uuid.UUID     `json:"owner_user_id"`
	DueAt          *time.Time     `json:"due_at"`
	Status         FollowUpStatus `json:"status"`
	Truth          Truth          `json:"truth"`
	CreatedBy      *uuid.UUID     `json:"created_by_user_id"`
	CreatedAt      time.Time      `json:"created_at"`
	ResolvedAt     *time.Time     `json:"resolved_at"`
	ResolvedBy     *uuid.UUID     `json:"resolved_by_user_id"`
	ResolutionNote string         `json:"resolution_note"`
}

// ResolveFollowUpInput closes an open item as done or dropped.
type ResolveFollowUpInput struct {
	Status FollowUpStatus `json:"status"`
	Note   string         `json:"note"`
}

func (in ResolveFollowUpInput) Normalize() (ResolveFollowUpInput, error) {
	if in.Status != StatusDone && in.Status != StatusDropped {
		return in, invalid("status must be done or dropped")
	}
	note, err := checkText("note", in.Note, MaxResolution, false)
	if err != nil {
		return in, err
	}
	in.Note = note
	return in, nil
}

const (
	MinSearchRunes     = 2
	MaxSearchRunes     = 100
	DefaultSearchLimit = 5
	MaxSearchLimit     = 8
	MaxSnippetRunes    = 240
	CredentialOmitted  = "[conteúdo omitido: parece conter uma credencial]"
)

// SearchInput asks for earlier messages of ONE contact. The contact is never part of the input: it is derived by the caller
// from a conversation or a topic the user can already see.
type SearchInput struct {
	Query                 string
	Limit                 int
	ExcludeConversationID *uuid.UUID // e.g. the conversation being looked at (the attendant already sees it)
	ExcludeTopicID        *uuid.UUID // e.g. the topic the copilot is already given (its messages are already in context)
}

func (in SearchInput) Normalize() (SearchInput, error) {
	q := strings.TrimSpace(in.Query)
	if n := utf8.RuneCountInString(q); n < MinSearchRunes || n > MaxSearchRunes || !utf8.ValidString(q) {
		return in, invalid("query must have between %d and %d characters", MinSearchRunes, MaxSearchRunes)
	}
	if untrusted.ContainsCredential(q) {
		return in, invalid("query looks like it contains a credential")
	}
	in.Query = q
	if in.Limit <= 0 {
		in.Limit = DefaultSearchLimit
	}
	if in.Limit > MaxSearchLimit {
		in.Limit = MaxSearchLimit
	}
	return in, nil
}

// HistoryHit is one earlier message that matched. The text is a short snippet around the match, cleaned, and replaced when
// it looks like a credential; it is customer/agent content, so it is DATA, never instructions.
type HistoryHit struct {
	At             time.Time
	Role           string // customer | agent
	Snippet        string
	ConversationID uuid.UUID
}

// Snippet cuts a window of text around the first (case-insensitive) occurrence of the query.
func Snippet(body, query string) string {
	clean := untrusted.SanitizeDerivedText(body)
	if untrusted.ContainsCredential(clean) {
		return CredentialOmitted
	}
	runes := []rune(clean)
	if len(runes) <= MaxSnippetRunes {
		return clean
	}
	lower, q := []rune(strings.ToLower(clean)), []rune(strings.ToLower(query))
	at := 0
	for i := 0; i+len(q) <= len(lower); i++ {
		if string(lower[i:i+len(q)]) == string(q) {
			at = i
			break
		}
	}
	start := at - MaxSnippetRunes/3
	if start < 0 {
		start = 0
	}
	end := start + MaxSnippetRunes
	if end > len(runes) {
		end = len(runes)
		if start = end - MaxSnippetRunes; start < 0 {
			start = 0
		}
	}
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}
