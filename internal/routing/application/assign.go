package application

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/routing/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

const (
	PermissionClaim  = "conversation.claim"
	PermissionManage = "conversation.manage"
)

var (
	ErrForbidden       = errors.New("routing: forbidden")
	ErrNotFound        = errors.New("routing: conversation not found")
	ErrConflict        = errors.New("routing: conversation already assigned to another agent")
	ErrInvalidAssignee = errors.New("routing: assignee is not an eligible agent of this tenant")
)

// AssignResult reports the resulting assignee and whether anything changed.
// Repeating an operation that already holds is a successful no-op
// (Changed=false, no history, no audit).
type AssignResult struct {
	AssignedTo *uuid.UUID
	Changed    bool
}

// Assigner implements manual assign/unassign. Authorization is the actor's
// role permissions in the TenantContext tenant; the conversation id alone
// grants nothing.
type Assigner struct {
	repo  ports.ConversationAssigner
	audit ports.AuditRecorder
}

func NewAssigner(repo ports.ConversationAssigner, audit ports.AuditRecorder) *Assigner {
	return &Assigner{repo: repo, audit: audit}
}

func (a *Assigner) actor(ctx context.Context) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil || tc.Source != tenancydomain.AccessSourceDirect {
		return nil, ErrForbidden
	}
	return tc, nil
}

func (a *Assigner) require(ctx context.Context, user uuid.UUID, permission string) error {
	ok, err := a.repo.HasPermission(ctx, user, permission)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	return nil
}

// Assign gives the conversation to target (uuid.Nil = the actor themself).
//   - Self claim (conversation.claim): only an unassigned conversation, or one
//     already yours (idempotent). Owned by someone else => ErrConflict, so of
//     two simultaneous claimers exactly one wins.
//   - Assign to another agent (conversation.manage): explicit reassignment; the
//     previous assignee is preserved in the append-only history. The target
//     must itself hold conversation.claim in this tenant.
func (a *Assigner) Assign(ctx context.Context, conversationID, target uuid.UUID) (AssignResult, error) {
	if a == nil || a.repo == nil || conversationID == uuid.Nil {
		return AssignResult{}, errors.New("routing: valid assignment is required")
	}
	tc, err := a.actor(ctx)
	if err != nil {
		return AssignResult{}, err
	}
	self := target == uuid.Nil || target == tc.ActorID
	if self {
		target = tc.ActorID
		err = a.require(ctx, tc.ActorID, PermissionClaim)
	} else {
		if err = a.require(ctx, tc.ActorID, PermissionManage); err == nil {
			var eligible bool
			eligible, err = a.repo.HasPermission(ctx, target, PermissionClaim)
			if err == nil && !eligible {
				err = ErrInvalidAssignee
			}
		}
	}
	if err != nil {
		return AssignResult{}, err
	}
	current, found, err := a.repo.LockAssignee(ctx, conversationID)
	if err != nil {
		return AssignResult{}, err
	}
	if !found {
		return AssignResult{}, ErrNotFound
	}
	if current != nil && *current == target {
		return AssignResult{AssignedTo: current}, nil
	}
	reason := "manual_assign"
	if self {
		if current != nil {
			return AssignResult{}, ErrConflict
		}
		reason = "manual_claim"
	}
	change := ports.AssignmentChange{ConversationID: conversationID, From: current, To: &target, Actor: tc.ActorID, Reason: reason}
	if err := a.apply(ctx, change); err != nil {
		return AssignResult{}, err
	}
	return AssignResult{AssignedTo: &target, Changed: true}, nil
}

// Unassign releases the conversation. Releasing your own needs
// conversation.claim; releasing someone else's needs conversation.manage.
// Unassigning an already-unassigned conversation is an idempotent no-op.
func (a *Assigner) Unassign(ctx context.Context, conversationID uuid.UUID) (AssignResult, error) {
	if a == nil || a.repo == nil || conversationID == uuid.Nil {
		return AssignResult{}, errors.New("routing: valid unassignment is required")
	}
	tc, err := a.actor(ctx)
	if err != nil {
		return AssignResult{}, err
	}
	// Baseline permission first: a role with neither permission learns nothing
	// about the conversation.
	if err := a.require(ctx, tc.ActorID, PermissionClaim); err != nil {
		if !errors.Is(err, ErrForbidden) {
			return AssignResult{}, err
		}
		if err := a.require(ctx, tc.ActorID, PermissionManage); err != nil {
			return AssignResult{}, err
		}
	}
	current, found, err := a.repo.LockAssignee(ctx, conversationID)
	if err != nil {
		return AssignResult{}, err
	}
	if !found {
		return AssignResult{}, ErrNotFound
	}
	if current == nil {
		return AssignResult{}, nil
	}
	reason := "manual_release"
	if *current != tc.ActorID {
		if err := a.require(ctx, tc.ActorID, PermissionManage); err != nil {
			return AssignResult{}, err
		}
		reason = "manual_unassign"
	}
	change := ports.AssignmentChange{ConversationID: conversationID, From: current, To: nil, Actor: tc.ActorID, Reason: reason}
	if err := a.apply(ctx, change); err != nil {
		return AssignResult{}, err
	}
	return AssignResult{Changed: true}, nil
}

func (a *Assigner) apply(ctx context.Context, change ports.AssignmentChange) error {
	if err := a.repo.SetAssignee(ctx, change); err != nil {
		return err
	}
	if a.audit != nil {
		return a.audit.ConversationAssignmentChanged(ctx, change)
	}
	return nil
}
