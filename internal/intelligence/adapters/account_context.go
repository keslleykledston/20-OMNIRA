package adapters

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	"github.com/omnira/omnira/internal/intelligence/domain"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Company context of a topic (ADR-0018 Wave 8). The context belongs to the SUBJECT, never to the conversation. A contact
// that belongs to several companies is never resolved by guessing: the API answers needs_choice with the candidates and a
// person records the choice. An AI may only suggest; nothing here lets it decide.

const permAccountRead = "account.read"

type accountRefDTO struct {
	AccountID uuid.UUID `json:"account_id"`
	Name      string    `json:"name"`
	// Source: topic_link (a person chose it), ticket (the topic's primary ticket targets it) or sole_company (the
	// contact belongs to exactly one company). Only topic_link is persisted; the others are derived on read.
	Source          string `json:"source"`
	Persisted       bool   `json:"persisted"`
	LinkedToContact bool   `json:"linked_to_contact"`
}

type candidateDTO struct {
	AccountID    uuid.UUID `json:"account_id"`
	Name         string    `json:"name"`
	Relationship string    `json:"relationship_type"`
	// ContactPrimary is the contact's own preference. It is NOT an answer: primary is not exclusive.
	ContactPrimary bool `json:"contact_primary"`
}

type accountContextDTO struct {
	Status     string          `json:"status"` // resolved | needs_choice | none
	Primary    *accountRefDTO  `json:"primary,omitempty"`
	Related    []accountRefDTO `json:"related"`
	Candidates []candidateDTO  `json:"candidates"`
}

func (h *TopicHandler) topicContact(ctx context.Context, tenantID, topicID uuid.UUID) (*uuid.UUID, error) {
	var contact *uuid.UUID
	err := platformdb.QuerierFromContext(ctx, h.pool).QueryRow(ctx, `SELECT primary_contact_id FROM topic_threads WHERE tenant_id=$1 AND id=$2`, tenantID, topicID).Scan(&contact)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrTopicNotFound
	}
	return contact, err
}

func (h *TopicHandler) accountContext(ctx context.Context, tenantID, topicID uuid.UUID) (accountContextDTO, error) {
	out := accountContextDTO{Status: "none", Related: []accountRefDTO{}, Candidates: []candidateDTO{}}
	q := platformdb.QuerierFromContext(ctx, h.pool)
	contact, err := h.topicContact(ctx, tenantID, topicID)
	if err != nil {
		return out, err
	}
	// the contact's active companies (used for "linked_to_contact" and for the candidates)
	linked := map[uuid.UUID]candidateDTO{}
	var order []uuid.UUID
	if contact != nil {
		rows, err := q.Query(ctx, `
			SELECT a.id, a.name, l.relationship_type, l.is_primary
			FROM contact_account_links l JOIN customer_accounts a ON a.tenant_id=l.tenant_id AND a.id=l.account_id
			WHERE l.tenant_id=$1 AND l.contact_id=$2 AND l.status='active' AND a.status <> 'archived'
			ORDER BY l.is_primary DESC, lower(a.name), a.id`, tenantID, *contact)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var c candidateDTO
			if err := rows.Scan(&c.AccountID, &c.Name, &c.Relationship, &c.ContactPrimary); err != nil {
				rows.Close()
				return out, err
			}
			linked[c.AccountID] = c
			order = append(order, c.AccountID)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
	}
	// persisted choices of a person
	rows, err := q.Query(ctx, `
		SELECT a.id, a.name, l.relation FROM topic_account_links l JOIN customer_accounts a ON a.tenant_id=l.tenant_id AND a.id=l.account_id
		WHERE l.tenant_id=$1 AND l.topic_thread_id=$2 ORDER BY (l.relation='primary') DESC, lower(a.name), a.id`, tenantID, topicID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var ref accountRefDTO
		var relation string
		if err := rows.Scan(&ref.AccountID, &ref.Name, &relation); err != nil {
			rows.Close()
			return out, err
		}
		_, ref.LinkedToContact = linked[ref.AccountID]
		ref.Source, ref.Persisted = "topic_link", true
		if relation == "primary" {
			r := ref
			out.Primary = &r
		} else {
			out.Related = append(out.Related, ref)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	if out.Primary != nil {
		out.Status = "resolved"
		return out, nil
	}
	// derived, never persisted: the topic's primary ticket already targets an account (set server-side from a validated company)
	var tAcc uuid.UUID
	var tName string
	err = q.QueryRow(ctx, `
		SELECT a.id, a.name FROM topic_ticket_links tl
		JOIN tickets t ON t.tenant_id=tl.tenant_id AND t.id=tl.ticket_id AND t.customer_account_id IS NOT NULL
		JOIN customer_accounts a ON a.tenant_id=t.tenant_id AND a.id=t.customer_account_id
		WHERE tl.tenant_id=$1 AND tl.topic_thread_id=$2 AND tl.relation='primary'`, tenantID, topicID).Scan(&tAcc, &tName)
	if err == nil {
		_, inContact := linked[tAcc]
		out.Primary = &accountRefDTO{AccountID: tAcc, Name: tName, Source: "ticket", LinkedToContact: inContact}
		out.Status = "resolved"
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	switch len(order) {
	case 0:
	case 1: // exactly one company: not a guess
		c := linked[order[0]]
		out.Primary = &accountRefDTO{AccountID: c.AccountID, Name: c.Name, Source: "sole_company", LinkedToContact: true}
		out.Status = "resolved"
	default: // several: a person chooses, nothing is assumed (the contact's primary is a preference, not an answer)
		out.Status = "needs_choice"
		for _, id := range order {
			out.Candidates = append(out.Candidates, linked[id])
		}
	}
	return out, nil
}

// GetAccountContext: GET /tenants/{tenant_id}/topics/{topic_id}/account-context (topic.read AND account.read)
func (h *TopicHandler) GetAccountContext(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permTopicRead)
	if err == nil {
		var ok bool
		if ok, err = h.has(r.Context(), tc, permAccountRead); err == nil && !ok {
			err = errForbidden
		}
	}
	if err != nil {
		fail(w, err)
		return
	}
	id, ok := pathUUID(w, r, "topic_id")
	if !ok {
		return
	}
	ctxDTO, err := h.accountContext(r.Context(), tc.TenantID, id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ctxDTO)
}

type linkTopicAccountRequest struct {
	AccountID uuid.UUID `json:"account_id"`
	Relation  string    `json:"relation"` // primary | related
}

// gate: topic.manage + account.read + being the attendant of the topic (same rule as every topic mutation).
func (h *TopicHandler) accountGate(w http.ResponseWriter, r *http.Request) (*tenancydomain.TenantContext, uuid.UUID, bool) {
	tc, err := h.authorize(r, permTopicManage)
	if err == nil {
		var ok bool
		if ok, err = h.has(r.Context(), tc, permAccountRead); err == nil && !ok {
			err = errForbidden
		}
	}
	if err != nil {
		fail(w, err)
		return nil, uuid.Nil, false
	}
	id, ok := pathUUID(w, r, "topic_id")
	if !ok {
		return nil, uuid.Nil, false
	}
	if _, err := h.svc.GetTopic(r.Context(), id); err != nil {
		fail(w, err)
		return nil, uuid.Nil, false
	}
	if ok, err := h.canOperateTopic(r.Context(), tc, id); err != nil || !ok {
		fail(w, firstErr(err, errForbidden))
		return nil, uuid.Nil, false
	}
	return tc, id, true
}

func (h *TopicHandler) auditTopicAccount(ctx context.Context, tc *tenancydomain.TenantContext, action auditdomain.AuditAction, topicID uuid.UUID, meta map[string]any) {
	ev, err := auditdomain.NewAuditEvent(tc.TenantID, tc.ActorID, action, auditdomain.ResourceTopic, topicID, auditdomain.OutcomeSuccess, uuid.Nil)
	if err != nil {
		return
	}
	for k, v := range meta {
		ev.SetMetadata(k, v)
	}
	_ = auditadapters.NewPostgresAuditEventRepository(h.pool).Store(ctx, ev)
}

// LinkAccount: POST /tenants/{tenant_id}/topics/{topic_id}/accounts. A person records the company of the subject.
// Making an account primary demotes the previous primary to related (at most one primary per topic). The account must
// exist in the tenant and not be archived; one the contact does not belong to is allowed but flagged linked_to_contact=false.
func (h *TopicHandler) LinkAccount(w http.ResponseWriter, r *http.Request) {
	tc, topicID, ok := h.accountGate(w, r)
	if !ok {
		return
	}
	var req linkTopicAccountRequest
	if !decode(w, r, &req) {
		return
	}
	if req.AccountID == uuid.Nil || (req.Relation != "primary" && req.Relation != "related") {
		http.Error(w, "account_id and relation (primary|related) are required", http.StatusUnprocessableEntity)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	var usable bool
	if err := q.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM customer_accounts WHERE tenant_id=$1 AND id=$2 AND status <> 'archived')`, tc.TenantID, req.AccountID).Scan(&usable); err != nil {
		fail(w, err)
		return
	}
	if !usable {
		http.Error(w, "account not found or archived", http.StatusUnprocessableEntity)
		return
	}
	err := platformdb.WithSavepoint(r.Context(), h.pool, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, h.pool)
		if req.Relation == "primary" {
			if _, err := q.Exec(ctx, `UPDATE topic_account_links SET relation='related', updated_at=now() WHERE tenant_id=$1 AND topic_thread_id=$2 AND relation='primary' AND account_id <> $3`, tc.TenantID, topicID, req.AccountID); err != nil {
				return err
			}
		}
		_, err := q.Exec(ctx, `
			INSERT INTO topic_account_links (tenant_id, topic_thread_id, account_id, relation, source, created_by_user_id)
			VALUES ($1,$2,$3,$4,'manual',$5)
			ON CONFLICT (tenant_id, topic_thread_id, account_id) DO UPDATE SET relation=EXCLUDED.relation, updated_at=now()`,
			tc.TenantID, topicID, req.AccountID, req.Relation, tc.ActorID)
		return err
	})
	if err != nil {
		fail(w, err)
		return
	}
	h.auditTopicAccount(r.Context(), tc, auditdomain.ActionTopicAccountLinked, topicID, map[string]any{"account_id": req.AccountID.String(), "relation": req.Relation, "source": "manual"})
	ctxDTO, err := h.accountContext(r.Context(), tc.TenantID, topicID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ctxDTO)
}

// UnlinkAccount: DELETE /tenants/{tenant_id}/topics/{topic_id}/accounts/{account_id}. Removing the primary does not
// promote another: the context falls back to the derived resolution (or asks again).
func (h *TopicHandler) UnlinkAccount(w http.ResponseWriter, r *http.Request) {
	tc, topicID, ok := h.accountGate(w, r)
	if !ok {
		return
	}
	accountID, ok := pathUUID(w, r, "account_id")
	if !ok {
		return
	}
	tag, err := platformdb.QuerierFromContext(r.Context(), h.pool).Exec(r.Context(), `DELETE FROM topic_account_links WHERE tenant_id=$1 AND topic_thread_id=$2 AND account_id=$3`, tc.TenantID, topicID, accountID)
	if err != nil {
		fail(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	h.auditTopicAccount(r.Context(), tc, auditdomain.ActionTopicAccountUnlinked, topicID, map[string]any{"account_id": accountID.String()})
	ctxDTO, err := h.accountContext(r.Context(), tc.TenantID, topicID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ctxDTO)
}
