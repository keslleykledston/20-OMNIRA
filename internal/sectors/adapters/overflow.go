package adapters

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/routing/overflow"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
)

// ErrInvalidPolicy carries the reason a policy cannot be saved (it names the failing rule, never tenant data).
type ErrInvalidPolicy struct{ Reason string }

func (e ErrInvalidPolicy) Error() string { return e.Reason }

// LoadPolicy reads the overflow policy of a queue (the ORIGIN of a chain); nil when the queue has none.
func LoadPolicy(ctx context.Context, q platformdb.Querier, tenant, queueID uuid.UUID) (*overflow.Policy, error) {
	var p overflow.Policy
	err := q.QueryRow(ctx, `SELECT id, queue_id, active, max_steps, on_exhausted, revision FROM overflow_policies WHERE tenant_id=$1 AND queue_id=$2`, tenant, queueID).
		Scan(&p.ID, &p.QueueID, &p.Active, &p.MaxSteps, &p.OnExhausted, &p.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT position, name, triggers, wait_seconds, wait_business, destination_kind, destination_id, destination_queue_id, allow_closed, allow_off_shift
		FROM overflow_steps WHERE tenant_id=$1 AND policy_id=$2 ORDER BY position`, tenant, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s overflow.Step
		var trig []string
		if err := rows.Scan(&s.Position, &s.Name, &trig, &s.WaitSeconds, &s.WaitBusiness, &s.DestKind, &s.DestID, &s.DestQueueID, &s.AllowClosed, &s.AllowOffShift); err != nil {
			return nil, err
		}
		for _, t := range trig {
			s.Triggers = append(s.Triggers, overflow.Trigger(t))
		}
		p.Steps = append(p.Steps, s)
	}
	return &p, rows.Err()
}

// SavePolicy replaces the policy of a queue. It validates the policy (pure rules) AND every destination against the database: a queue
// destination must be another queue of THIS tenant, an on-call destination an active roster of THIS tenant. The queue row is locked, so
// two editors serialize, and expectedRevision (when given) turns a lost update into ErrStale.
func SavePolicy(ctx context.Context, q platformdb.Querier, tenant, actor, queueID uuid.UUID, p overflow.Policy, expectedRevision *int) (int, error) {
	p.QueueID = queueID
	if err := p.Validate(queueID); err != nil {
		return 0, ErrInvalidPolicy{err.Error()}
	}
	var lock int
	if err := q.QueryRow(ctx, `SELECT 1 FROM queues WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenant, queueID).Scan(&lock); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	for _, s := range p.Steps {
		var ok bool
		var err error
		switch s.DestKind {
		case overflow.DestQueue:
			err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM queues WHERE tenant_id=$1 AND id=$2)`, tenant, s.DestID).Scan(&ok)
		case overflow.DestOnCall:
			err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM on_call_rosters WHERE tenant_id=$1 AND id=$2 AND active)`, tenant, s.DestID).Scan(&ok)
		case overflow.DestAgent: // an active attendant of THIS company (never a Hub operator: they have no membership here)
			err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships m JOIN agent_profiles ap ON ap.tenant_id=m.tenant_id AND ap.membership_id=m.id
				WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND ap.status='active')`, tenant, s.DestID).Scan(&ok)
		case overflow.DestSector:
			err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sectors WHERE tenant_id=$1 AND id=$2 AND active)`, tenant, s.DestID).Scan(&ok)
		case overflow.DestHub: // the company has a contract with that Hub (it learns the contract, never the operators) and the queue is its own
			err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tenant_hub_contracts($1) c WHERE c.contract_id=$2)
				AND EXISTS(SELECT 1 FROM queues WHERE tenant_id=$1 AND id=$3)`, tenant, s.DestID, s.DestQueueID).Scan(&ok)
		}
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, ErrInvalidPolicy{fmt.Sprintf("step %d: the destination does not exist in this company (or is inactive, or the company has no such contract)", s.Position)}
		}
	}
	var current *int
	var policyID uuid.UUID
	var rev int
	err := q.QueryRow(ctx, `SELECT id, revision FROM overflow_policies WHERE tenant_id=$1 AND queue_id=$2 FOR UPDATE`, tenant, queueID).Scan(&policyID, &rev)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		policyID = uuid.New()
		if expectedRevision != nil && *expectedRevision != 0 {
			return 0, ErrStale
		}
		rev = 1
		if _, err := q.Exec(ctx, `INSERT INTO overflow_policies (id, tenant_id, queue_id, active, max_steps, on_exhausted, revision, updated_by) VALUES ($1,$2,$3,$4,$5,$6,1,$7)`,
			policyID, tenant, queueID, p.Active, p.MaxSteps, p.OnExhausted, actor); err != nil {
			return 0, err
		}
	case err != nil:
		return 0, err
	default:
		current = &rev
		if expectedRevision != nil && *expectedRevision != *current {
			return 0, ErrStale
		}
		rev++
		if _, err := q.Exec(ctx, `UPDATE overflow_policies SET active=$3, max_steps=$4, on_exhausted=$5, revision=$6, updated_by=$7, updated_at=now() WHERE tenant_id=$1 AND id=$2`,
			tenant, policyID, p.Active, p.MaxSteps, p.OnExhausted, rev, actor); err != nil {
			return 0, err
		}
		if _, err := q.Exec(ctx, `DELETE FROM overflow_steps WHERE tenant_id=$1 AND policy_id=$2`, tenant, policyID); err != nil {
			return 0, err
		}
	}
	for _, s := range p.Steps {
		trig := make([]string, len(s.Triggers))
		for i, t := range s.Triggers {
			trig[i] = string(t)
		}
		if _, err := q.Exec(ctx, `INSERT INTO overflow_steps (tenant_id, policy_id, position, name, triggers, wait_seconds, wait_business, destination_kind, destination_id, destination_queue_id, allow_closed, allow_off_shift)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, tenant, policyID, s.Position, s.Name, trig, s.WaitSeconds, s.WaitBusiness, s.DestKind, s.DestID, s.DestQueueID, s.AllowClosed, s.AllowOffShift); err != nil {
			return 0, err
		}
	}
	return rev, nil
}

// ---------------------------------------------------------------- HTTP

// GetOverflowPolicy: GET /queues/{queue_id}/overflow-policy. 200 with `{"policy": null}` when the queue has none.
func (h *Handler) GetOverflowPolicy(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.read")
	if !ok {
		return
	}
	queueID, ok := pathUUID(w, r, "queue_id")
	if !ok {
		return
	}
	if !h.exists(r, q, `SELECT EXISTS(SELECT 1 FROM queues WHERE tenant_id=$1 AND id=$2)`, tc.TenantID, queueID) {
		http.Error(w, "queue not found", http.StatusNotFound)
		return
	}
	p, err := LoadPolicy(r.Context(), q, tc.TenantID, queueID)
	if err != nil {
		http.Error(w, "failed to read the policy", http.StatusInternalServerError)
		return
	}
	if p != nil && p.Steps == nil {
		p.Steps = []overflow.Step{}
	}
	write(w, http.StatusOK, map[string]any{"policy": p})
}

// PutOverflowPolicy: PUT /queues/{queue_id}/overflow-policy {max_steps, on_exhausted, active, steps[], expected_revision?}.
func (h *Handler) PutOverflowPolicy(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	queueID, ok := pathUUID(w, r, "queue_id")
	if !ok {
		return
	}
	var in struct {
		overflow.Policy
		ExpectedRevision *int `json:"expected_revision,omitempty"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	// allow_closed / allow_off_shift bypass the sector calendar or the agent's shift: a stronger power than editing a chain
	for _, st := range in.Steps {
		if st.AllowClosed || st.AllowOffShift {
			ok, err := tenancyadapters.ActorHasPermission(r.Context(), q, tc.TenantID, tc.ActorID, "routing.override")
			if err != nil {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			if !ok {
				http.Error(w, "forbidden: allow_closed and allow_off_shift need the routing.override permission", http.StatusForbidden)
				return
			}
			break
		}
	}
	var rev int
	err := attempt(r, q, func() (e error) {
		rev, e = SavePolicy(r.Context(), q, tc.TenantID, tc.ActorID, queueID, in.Policy, in.ExpectedRevision)
		return e
	})
	var invalid ErrInvalidPolicy
	switch {
	case errors.As(err, &invalid):
		http.Error(w, invalid.Reason, http.StatusBadRequest)
		return
	case errors.Is(err, ErrNotFound):
		http.Error(w, "queue not found", http.StatusNotFound)
		return
	case errors.Is(err, ErrStale):
		http.Error(w, "the policy was changed by someone else; reload it and apply your change again", http.StatusConflict)
		return
	case err != nil:
		http.Error(w, "failed to save the policy", http.StatusInternalServerError)
		return
	}
	if _, err := KickQueue(r.Context(), q, tc.TenantID, queueID); err != nil { // conversations waiting under the old policy are re-evaluated under the new one
		http.Error(w, "failed to re-evaluate the waiting conversations", http.StatusInternalServerError)
		return
	}
	h.record(r, auditdomain.ResourceOverflowPolicy, queueID, auditdomain.ActionOverflowSaved, map[string]any{"revision": rev, "steps": len(in.Steps), "active": in.Active})
	p, err := LoadPolicy(r.Context(), q, tc.TenantID, queueID)
	if err != nil {
		http.Error(w, "failed to read the policy", http.StatusInternalServerError)
		return
	}
	write(w, http.StatusOK, map[string]any{"policy": p})
}

// DeleteOverflowPolicy: DELETE /queues/{queue_id}/overflow-policy. Conversations already in a chain keep the state they have; with no
// policy the engine stops overflowing them.
func (h *Handler) DeleteOverflowPolicy(w http.ResponseWriter, r *http.Request) {
	tc, q, ok := h.authorize(w, r, "agent.manage")
	if !ok {
		return
	}
	queueID, ok := pathUUID(w, r, "queue_id")
	if !ok {
		return
	}
	tag, err := q.Exec(r.Context(), `DELETE FROM overflow_policies WHERE tenant_id=$1 AND queue_id=$2`, tc.TenantID, queueID)
	if err != nil {
		http.Error(w, "failed to remove the policy", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() != 1 {
		http.Error(w, "policy not found", http.StatusNotFound)
		return
	}
	if _, err := KickQueue(r.Context(), q, tc.TenantID, queueID); err != nil {
		http.Error(w, "failed to re-evaluate the waiting conversations", http.StatusInternalServerError)
		return
	}
	h.record(r, auditdomain.ResourceOverflowPolicy, queueID, auditdomain.ActionOverflowRemoved, nil)
	w.WriteHeader(http.StatusNoContent)
}
