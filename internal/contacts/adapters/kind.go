package adapters

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Contact kinds (ADR-0014). The same values the contacts_kind_check constraint allows. "agent" is a K3G team member.
const (
	KindCustomer = "customer"
	KindOther    = "other"
	KindSpam     = "spam"
	KindAgent    = "agent"
)

func validContactKind(k string) bool {
	return k == KindCustomer || k == KindOther || k == KindSpam || k == KindAgent
}

// escapeLike makes user text literal inside a LIKE/ILIKE pattern.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// phoneDigits returns the digits of q when q LOOKS like a phone number (digits plus the usual
// formatting: space, +, -, ., parentheses) and has at least 3 of them. Text such as "100%" or
// "Loja 24h" is a name search, not a phone search, so it must not also match numbers by its digits.
func phoneDigits(q string) (string, bool) {
	var b strings.Builder
	for _, r := range q {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '+' || r == '-' || r == '.' || r == '(' || r == ')':
		default:
			return "", false
		}
	}
	return b.String(), b.Len() >= 3
}

// hasPermission reads the permission from the role matrix of an ACTIVE membership (never a role
// name), the same rule as authorizeTicketRead.
func (h *ContactsAPIHandler) hasPermission(r *http.Request, tc *tenancydomain.TenantContext, key string) (bool, error) {
	var ok bool
	err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`,
		tc.TenantID, tc.ActorID, key).Scan(&ok)
	return ok, err
}

// SetKind reclassifies a contact: customer | other | spam. Anyone who attends (conversation.claim,
// held by every role) may do it, because spam arrives unassigned and the operator who spots it must
// be able to flag it, and the same person must be able to undo a false positive from the Spam view.
// The safety net is visibility and the audit trail, not a stricter role: spam is never deleted, it
// has its own Inbox, and every change is audited with the previous and new kind.
//
// Marking spam also takes the contact's open, UNASSIGNED conversations out of their queue and clears
// their pending routing retry, so no automatic assignment reaches a scam. Conversations somebody
// already holds are left alone. Restoring does not re-queue: the conversation reappears in the
// Inbox and is claimed or transferred by hand. The call is idempotent (same kind = 200, no audit).
func (h *ContactsAPIHandler) SetKind(w http.ResponseWriter, r *http.Request) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil || tc.ActorID == uuid.Nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	allowed, err := h.hasPermission(r, tc, "conversation.claim")
	if err != nil {
		http.Error(w, "failed to check permission", http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	contactID, err := uuid.Parse(r.PathValue("contact_id"))
	if err != nil {
		http.Error(w, "invalid contact_id", http.StatusBadRequest)
		return
	}
	var req struct {
		Kind *string `json:"kind"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Kind == nil || !validContactKind(*req.Kind) {
		http.Error(w, "kind must be customer, other, agent or spam", http.StatusBadRequest)
		return
	}
	next := *req.Kind

	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var prev string
	err = q.QueryRow(r.Context(), `SELECT kind FROM contacts WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tc.TenantID, contactID).Scan(&prev)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "contact not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read contact", http.StatusInternalServerError)
		return
	}
	if prev != next {
		if _, err := q.Exec(r.Context(),
			`UPDATE contacts SET kind = $3, updated_at = now() WHERE tenant_id = $1 AND id = $2`, tc.TenantID, contactID, next); err != nil {
			http.Error(w, "failed to save contact", http.StatusInternalServerError)
			return
		}
		dequeued := int64(0)
		if next == KindSpam || next == KindAgent { // neither is customer service work: out of the queue
			tag, err := q.Exec(r.Context(), `
				UPDATE conversations SET queue_id = NULL, routing_retry_at = NULL, updated_at = now()
				WHERE tenant_id = $1 AND contact_id = $2 AND status = 'open' AND assigned_to_user_id IS NULL AND queue_id IS NOT NULL`,
				tc.TenantID, contactID)
			if err != nil {
				http.Error(w, "failed to save contact", http.StatusInternalServerError)
				return
			}
			dequeued = tag.RowsAffected()
		}
		h.recordKind(r, tc, contactID, prev, next, dequeued)
	}
	item, err := scanContactItem(q.QueryRow(r.Context(), contactSelect+` WHERE c.tenant_id = $1 AND c.id = $2`, tc.TenantID, contactID))
	if err != nil {
		http.Error(w, "failed to read contact", http.StatusInternalServerError)
		return
	}
	writeJSON(w, item)
}

func (h *ContactsAPIHandler) recordKind(r *http.Request, tc *tenancydomain.TenantContext, contactID uuid.UUID, from, to string, dequeued int64) {
	if h.audit == nil {
		return
	}
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, auditdomain.ActionContactKindChanged, auditdomain.ResourceContact, contactID, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	ev.SetMetadata("kind_from", from)
	ev.SetMetadata("kind_to", to)
	ev.SetMetadata("conversations_dequeued", dequeued)
	_ = h.audit.Store(r.Context(), ev)
}
