package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/platform/untrusted"
)

type SessionStatus string

const (
	SessionOpen       SessionStatus = "open"
	SessionInProgress SessionStatus = "in_progress"
	SessionWaiting    SessionStatus = "waiting"
	SessionClosed     SessionStatus = "closed"
	SessionAbandoned  SessionStatus = "abandoned"
)

// Active says whether the session still counts as one in course (at most one per conversation).
func (s SessionStatus) Active() bool {
	return s == SessionOpen || s == SessionInProgress || s == SessionWaiting
}

func (s SessionStatus) Valid() bool { return s.Active() || s == SessionClosed || s == SessionAbandoned }

// Ended is closed or abandoned: the states that carry a result.
func (s SessionStatus) Ended() bool { return s == SessionClosed || s == SessionAbandoned }

// sessionTransitions is the whole state machine. A transfer between people is NOT here: it is not a state (it is a history event). Leaving closed or
// abandoned is a REOPEN, which has its own extra rules (Service.Reopen); everything not listed is refused.
var sessionTransitions = map[SessionStatus][]SessionStatus{
	SessionOpen:       {SessionInProgress, SessionWaiting, SessionClosed, SessionAbandoned},
	SessionInProgress: {SessionWaiting, SessionClosed, SessionAbandoned},
	SessionWaiting:    {SessionInProgress, SessionClosed, SessionAbandoned},
	SessionClosed:     {SessionInProgress},
	SessionAbandoned:  {SessionInProgress},
}

func CanTransition(from, to SessionStatus) bool {
	for _, t := range sessionTransitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// Canonical results: the eight codes every report aggregates by. A company adds its own codes, each pointing to one of these.
const (
	ResInformationProvided  = "information_provided"
	ResResolvedFirstContact = "resolved_first_contact"
	ResTaskCreated          = "task_created"
	ResCaseCreated          = "case_created"
	ResExternalServiceOrder = "external_service_order"
	ResTransferred          = "transferred"
	ResAbandoned            = "abandoned"
	ResOther                = "other"
)

var canonicalResolutions = []string{ResInformationProvided, ResResolvedFirstContact, ResTaskCreated, ResCaseCreated, ResExternalServiceOrder, ResTransferred, ResAbandoned, ResOther}

func CanonicalResolutions() []string { return append([]string(nil), canonicalResolutions...) }

func IsCanonicalResolution(code string) bool {
	for _, c := range canonicalResolutions {
		if c == code {
			return true
		}
	}
	return false
}

// Sources say WHO started or ended a session (the actor id is separate): a person at the Workspace, the Finalize action (ADR-0020), a flow, the API, the system.
const (
	SourceOperator = "operator"
	SourceFinalize = "finalize"
	SourceFlow     = "flow"
	SourceAPI      = "api"
	SourceSystem   = "system"
)

func ValidSource(s string) bool {
	switch s {
	case SourceOperator, SourceFinalize, SourceFlow, SourceAPI, SourceSystem:
		return true
	}
	return false
}

const (
	DefaultSessionPrefix = "AT"
	// DefaultSessionStart is the first protocol number a company gets (AT-10000): it reads like a protocol, not like "1".
	DefaultSessionStart int64 = 10000
	// DefaultReopenWindow is how long after closing a session may be reopened.
	DefaultReopenWindow = 72 * time.Hour
	MaxNoteLen          = 1000
)

// FormatProtocol builds the visible protocol. The number is unique PER COMPANY, never globally (the UUID is the technical key).
func FormatProtocol(prefix string, seq int64) string { return fmt.Sprintf("%s-%d", prefix, seq) }

var (
	tokenRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,39}$`)
	channelRe = regexp.MustCompile(`^[a-z0-9_]{1,40}$`)
	idemRe    = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
)

// IsToken is the shape of a catalog code.
func IsToken(s string) bool { return tokenRe.MatchString(s) }

// ValidIdempotencyKey is the platform-wide shape of an Idempotency-Key.
func ValidIdempotencyKey(s string) bool { return idemRe.MatchString(s) }

// CleanText trims a free text and refuses what must never be stored: a credential, a control character, or more than max runes.
func CleanText(in string, max int, field string) (string, error) {
	out := strings.TrimSpace(in)
	if utf8.RuneCountInString(out) > max {
		return "", invalid("%s is longer than %d characters", field, max)
	}
	for _, r := range out {
		if r < 0x20 && r != '\n' && r != '\t' {
			return "", invalid("%s has a control character", field)
		}
	}
	if untrusted.ContainsCredential(out) {
		return "", invalid("%s looks like it contains a credential; do not store secrets", field)
	}
	return out, nil
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Invalid builds an ErrInvalid with a message (for the other packages of the module).
func Invalid(format string, a ...any) error { return invalid(format, a...) }

type Session struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	Protocol            string
	ProtocolSequence    int64
	ExternalProtocol    *string
	ConversationID      *uuid.UUID
	ContactID           *uuid.UUID
	CustomerID          *uuid.UUID
	QueueID             *uuid.UUID
	CategoryID          *uuid.UUID
	Status              SessionStatus
	ResolutionCode      *string
	ResolutionCanonical *string
	ResolutionNote      string
	OriginChannel       string
	OpenedSource        string
	ClosedSource        *string
	OpenedBy            *uuid.UUID
	ClosedBy            *uuid.UUID
	OpenedAt            time.Time
	FirstResponseAt     *time.Time
	ClosedAt            *time.Time
	ReopenCount         int
	IdempotencyKey      *string
	RequestHash         *string
	Version             int
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// OpenInput starts a session. Everything but the source is optional: a session may start from a conversation, or alone (an attendance by phone logged
// through the API). A contact is taken from the conversation, never trusted from here when a conversation is given.
type OpenInput struct {
	ConversationID   *uuid.UUID
	ContactID        *uuid.UUID
	CustomerID       *uuid.UUID
	QueueID          *uuid.UUID
	CategoryID       *uuid.UUID
	OriginChannel    string
	ExternalProtocol string
	IdempotencyKey   string
	Source           string
}

func (in OpenInput) Normalize() (OpenInput, error) {
	out := in
	if out.Source == "" {
		out.Source = SourceOperator
	}
	if !ValidSource(out.Source) {
		return OpenInput{}, invalid("unknown source")
	}
	if out.OriginChannel == "" {
		out.OriginChannel = "other"
	}
	if !channelRe.MatchString(out.OriginChannel) {
		return OpenInput{}, invalid("origin_channel must be lower case letters, digits or _")
	}
	out.ExternalProtocol = strings.TrimSpace(out.ExternalProtocol)
	if utf8.RuneCountInString(out.ExternalProtocol) > 80 {
		return OpenInput{}, invalid("external_protocol is too long")
	}
	if out.IdempotencyKey != "" && !ValidIdempotencyKey(out.IdempotencyKey) {
		return OpenInput{}, invalid("Idempotency-Key must be 8-128 characters of letters, digits . _ : -")
	}
	if out.ConversationID != nil && *out.ConversationID == uuid.Nil {
		return OpenInput{}, invalid("conversation_id")
	}
	if out.ConversationID != nil {
		out.ContactID = nil // taken from the conversation
	}
	return out, nil
}

// Hash is the stable fingerprint of a request, so a reused Idempotency-Key with another body is caught.
func (in OpenInput) Hash() string {
	return fingerprint("open", id(in.ConversationID), id(in.ContactID), id(in.CustomerID), id(in.QueueID), id(in.CategoryID), in.OriginChannel, in.ExternalProtocol, in.Source)
}

// PatchInput classifies a session. Only the fields that are set change; a field is never cleared in this version.
type PatchInput struct {
	CustomerID *uuid.UUID
	QueueID    *uuid.UUID
	CategoryID *uuid.UUID
}

func (in PatchInput) Empty() bool {
	return in.CustomerID == nil && in.QueueID == nil && in.CategoryID == nil
}

type CloseInput struct {
	ResolutionCode string
	Note           string
	Abandon        bool
}

func (in CloseInput) Normalize() (CloseInput, error) {
	out := in
	out.ResolutionCode = strings.TrimSpace(out.ResolutionCode)
	if out.Abandon && out.ResolutionCode == "" {
		out.ResolutionCode = ResAbandoned
	}
	if out.ResolutionCode == "" {
		return CloseInput{}, invalid("a result is required to close a session")
	}
	if !IsCanonicalResolution(out.ResolutionCode) && !IsToken(out.ResolutionCode) {
		return CloseInput{}, invalid("resolution_code")
	}
	note, err := CleanText(out.Note, MaxNoteLen, "note")
	if err != nil {
		return CloseInput{}, err
	}
	out.Note = note
	return out, nil
}

type ReopenInput struct{ Reason string }

func (in ReopenInput) Normalize() (ReopenInput, error) {
	r, err := CleanText(in.Reason, 300, "reason")
	if err != nil {
		return ReopenInput{}, err
	}
	if r == "" {
		return ReopenInput{}, invalid("a reason is required to reopen a session")
	}
	return ReopenInput{Reason: r}, nil
}

// ResolutionFromFinalize maps the reason of the ADR-0020 finalization to the session's state and a FIRST guess at the result. The links the session
// gathered (service order, case, task) refine it: see RefineResolution.
func ResolutionFromFinalize(reason string) (SessionStatus, string) {
	switch reason {
	case "transferred":
		return SessionClosed, ResTransferred
	case "no_response":
		return SessionAbandoned, ResAbandoned
	case "resolved":
		return SessionClosed, ResInformationProvided
	default: // duplicate, spam, other
		return SessionClosed, ResOther
	}
}

// RefineResolution upgrades a plain "information provided" to the strongest operational link the session produced; any other result is kept.
func RefineResolution(code string, externalOrders, cases, tasks int) string {
	if code != ResInformationProvided {
		return code
	}
	switch {
	case externalOrders > 0:
		return ResExternalServiceOrder
	case cases > 0:
		return ResCaseCreated
	case tasks > 0:
		return ResTaskCreated
	}
	return code
}

func id(p *uuid.UUID) string {
	if p == nil {
		return "-"
	}
	return p.String()
}
