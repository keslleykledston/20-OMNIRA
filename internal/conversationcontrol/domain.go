// Package conversationcontrol is the ONE place that changes who holds a conversation when the holder is not decided by a legacy writer: an
// external application claiming, releasing or transferring, a supervisor taking over from an application, and the drain that frees what an
// installation holds when it stops being usable (ADR-0046 §2.3). Every operation runs in the caller's transaction, takes the conversation row
// FOR UPDATE first (the first lock of the project-wide order conversation -> routing state -> timer), advances control_version by exactly one on
// a real change, and leaves an append-only, typed history row. An idempotent repetition changes nothing and writes nothing.
package conversationcontrol

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Kind of holder. crm_agent_verified exists in the vocabulary and in the database CHECK but NOTHING produces it: it needs a real federation of the
// application's operators (H7), and pretending otherwise would be worse than not having it.
type Kind string

const (
	KindTenantUser       Kind = "tenant_user"
	KindHubUser          Kind = "hub_user"
	KindCRMApplication   Kind = "crm_application"
	KindCRMAgentVerified Kind = "crm_agent_verified"
)

func (k Kind) External() bool { return k == KindCRMApplication || k == KindCRMAgentVerified }

type Origin string

const (
	OriginWorkspace Origin = "workspace"
	OriginHub       Origin = "hub"
	OriginCRM       Origin = "crm"
	OriginSystem    Origin = "system"
)

// State is the control of one conversation as it is in the database. A nil Kind means free.
type State struct {
	ConversationID uuid.UUID
	Status         string // open | closed (independent of the control)
	Kind           *Kind
	HolderID       *uuid.UUID
	Version        int64
	Since          *time.Time
	Origin         *Origin
	AssignedUserID *uuid.UUID // the human projection
}

func (s State) Free() bool { return s.Kind == nil }

// HeldBy says whether this exact holder holds it.
func (s State) HeldBy(kind Kind, id uuid.UUID) bool {
	return s.Kind != nil && *s.Kind == kind && s.HolderID != nil && *s.HolderID == id
}

// HeldExternally says whether an application holds it.
func (s State) HeldExternally() bool { return s.Kind != nil && s.Kind.External() }

// ActorKind is who performed an operation, for the history. An application is never a user.
type ActorKind string

const (
	ActorTenantUser     ActorKind = "tenant_user"
	ActorHubUser        ActorKind = "hub_user"
	ActorCRMApplication ActorKind = "crm_application"
	ActorSystem         ActorKind = "system"
)

// Actor of an operation. ClaimedAgentRef is what an application says about its own operator; it is stored as unverified metadata and never
// decides anything.
type Actor struct {
	Kind            ActorKind
	ID              uuid.UUID // user id, or installation id; uuid.Nil for the system
	RequestID       string
	ClaimedAgentRef string
}

var (
	ErrNotFound      = errors.New("conversation not found")
	ErrClosed        = errors.New("conversation is closed")
	ErrHeldByOther   = errors.New("conversation is held by another holder")
	ErrNotHolder     = errors.New("conversation is not held by this holder")
	ErrStaleVersion  = errors.New("control version is not the current one")
	ErrNotExternal   = errors.New("conversation is not held by an external application")
	ErrReasonNeeded  = errors.New("a reason is required")
	ErrInvalidTarget = errors.New("the target cannot hold this conversation")
	// ErrNotAllowed: the session may not change the hold of an external application (it is neither a system session nor a person holding
	// conversation.takeover_external). The database refuses it (SQLSTATE OM004); this is the same refusal, named.
	ErrNotAllowed = errors.New("not allowed to change the control of an external application")
)

// ReasonMaxLen mirrors the CHECK of conversation_control_events.reason.
const ReasonMaxLen = 80
