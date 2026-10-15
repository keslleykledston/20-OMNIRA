package application

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
)

// Audit actions for the external side.
const (
	ActionExternalLinked    = "external_record.linked"
	ActionExternalUnlinked  = "external_record.unlinked"
	ActionExternalStatus    = "external_record.status_changed"
	ActionExternalRequested = "external_request.created"
	ActionExternalRequest   = "external_request.state_changed"
	ResourceExternalRecord  = "external_record"
	ResourceExternalRequest = "external_request"
)

// WithExternal installs the external side: the repository and the resolver of the company's gateways (nil resolver = no integration is configured).
func (s *Service) WithExternal(r ports.ExternalRepository, g ports.GatewayResolver) *Service {
	s.ext = r
	if g != nil {
		s.gateways = g
	}
	return s
}

// WithExternalTimings bounds a call to the ERP, and says how old an unresolved request must be before "the ERP does not know it" is believed (an ERP may be
// eventually consistent: asking for a record right after a timeout and not finding it proves nothing).
func (s *Service) WithExternalTimings(callTimeout, reconcileMinAge time.Duration) *Service {
	if callTimeout > 0 {
		s.gatewayTO = callTimeout
	}
	if reconcileMinAge >= 0 {
		s.reconcileAge = reconcileMinAge
	}
	// A request is only declared "not created" after it is old enough that its own call has surely ended: never less than twice the call's time limit, so a late answer
	// of the original call can never find the request already settled as a failure.
	if floor := 2*s.gatewayTO + ports.ExternalTxTimeout; s.reconcileAge < floor {
		s.reconcileAge = floor
	}
	return s
}

func (s *Service) external() error {
	if s.ext == nil {
		return domain.ErrNotConfigured
	}
	return nil
}

// targetLinks proves the target exists and is visible to the caller and returns the ids the history of the link belongs to.
func (s *Service) targetLinks(ctx context.Context, sessionID, caseID, taskID *uuid.UUID) (links, error) {
	switch {
	case sessionID != nil:
		sess, err := s.repo.GetSession(ctx, *sessionID)
		if err != nil {
			return links{}, err
		}
		return links{session: &sess.ID, conv: sess.ConversationID, contact: sess.ContactID, customer: sess.CustomerID}, nil
	case caseID != nil:
		cs, err := s.cases.GetCase(ctx, *caseID)
		if err != nil {
			return links{}, err
		}
		return caseLinks(cs), nil
	case taskID != nil:
		t, err := s.tasks.GetTask(ctx, *taskID)
		if err != nil {
			return links{}, err
		}
		return taskLinks(t), nil
	}
	return links{}, domain.Invalid("a target is required")
}

// gatewayCall bounds a call to the ERP. It does not stop when the caller goes away: a write may already be on its way, and its outcome must still be written.
func (s *Service) gatewayCall(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), s.gatewayTO)
}

// readSnapshot asks the ERP for a record. A failure is classified; "the ERP did not answer" is an error, never a missing record.
func (s *Service) readSnapshot(ctx context.Context, gw ports.ServiceOrderGateway, externalType, externalID string) (*domain.Snapshot, error) {
	callCtx, cancel := s.gatewayCall(ctx)
	defer cancel()
	snap, err := safeGet(callCtx, gw, externalType, externalID)
	if err != nil {
		switch kind, _ := ports.KindOf(err); kind {
		case ports.GatewayNotFound:
			return nil, domain.Invalid("the external system has no such record")
		case ports.GatewayNotSupported:
			return nil, domain.ErrNotConfigured
		case ports.GatewayRejected:
			return nil, domain.Invalid("the external system refused the lookup")
		default:
			return nil, domain.ErrExternalUnavailable
		}
	}
	clean, err := snap.Clean()
	if err != nil {
		return nil, domain.ErrExternalUnavailable
	}
	return &clean, nil
}

func safeGet(ctx context.Context, gw ports.ServiceOrderGateway, t, id string) (snap *domain.Snapshot, err error) {
	defer func() {
		if r := recover(); r != nil {
			snap, err = nil, &ports.GatewayError{Kind: ports.GatewayUnknown, Class: "panic"}
		}
	}()
	return gw.Get(ctx, t, id)
}

func safeCreate(ctx context.Context, gw ports.ServiceOrderGateway, req ports.CreateOrderRequest) (snap *domain.Snapshot, err error) {
	defer func() {
		if r := recover(); r != nil { // a crash inside an adapter after the request left is an UNKNOWN outcome, never a failure
			snap, err = nil, &ports.GatewayError{Kind: ports.GatewayUnknown, Class: "panic"}
		}
	}()
	return gw.CreateServiceOrder(ctx, req)
}

// recordFromSnapshot builds the projection of a record the ERP just confirmed.
func recordFromSnapshot(tenant, integration uuid.UUID, system string, snap domain.Snapshot, by *uuid.UUID, now time.Time) *domain.ExternalRecord {
	return &domain.ExternalRecord{
		ID: uuid.New(), TenantID: tenant, IntegrationID: integration, ExternalSystem: system, ExternalType: snap.ExternalType, ExternalID: snap.ExternalID,
		DisplayRef: snap.DisplayRef, ExternalStatus: snap.ExternalStatus, StatusClass: snap.StatusClass, SyncState: domain.SyncSynced,
		SourceUpdatedAt: snap.UpdatedAt, LastSyncedAt: &now, CreatedBy: by, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

// LinkResult tells what a link call did.
type LinkResult struct {
	Record        *domain.ExternalRecord
	Link          *domain.ExternalLink
	LinkCreated   bool
	RecordCreated bool
}

// LinkExternalRecord ties a record the ERP already has to a session, a case or a task. The ERP must confirm the record exists (this module never invents one) and the
// projection is refreshed from what it says. Linking twice is not an error: the link that stands is returned.
func (s *Service) LinkExternalRecord(ctx context.Context, raw domain.LinkInput) (LinkResult, error) {
	c, err := s.who(ctx)
	if err != nil {
		return LinkResult{}, err
	}
	in, err := raw.Normalize()
	if err != nil {
		return LinkResult{}, err
	}
	if err := s.allow(ctx, c, PermLinkExternal); err != nil {
		return LinkResult{}, err
	}
	if err := s.external(); err != nil {
		return LinkResult{}, err
	}
	system, ok, err := s.ext.IntegrationSystem(ctx, in.IntegrationID)
	if err != nil {
		return LinkResult{}, err
	}
	if !ok {
		return LinkResult{}, domain.Invalid("integration_id is not an integration of this company")
	}
	l, err := s.targetLinks(ctx, in.SessionID, in.CaseID, in.TaskID)
	if err != nil {
		return LinkResult{}, err
	}
	gw, err := s.gateways.Resolve(ctx, c.tc.TenantID, in.IntegrationID)
	if err != nil {
		return LinkResult{}, err
	}
	snap, err := s.readSnapshot(ctx, gw, in.ExternalType, in.ExternalID)
	if err != nil {
		return LinkResult{}, err
	}
	if snap.ExternalType != in.ExternalType || snap.ExternalID != in.ExternalID {
		return LinkResult{}, domain.Invalid("the external system answered with another record")
	}
	now, err := s.repo.Now(ctx)
	if err != nil {
		return LinkResult{}, err
	}
	rec, created, err := s.ext.InsertRecord(ctx, recordFromSnapshot(c.tc.TenantID, in.IntegrationID, system, *snap, c.actorID(), now))
	if err != nil {
		return LinkResult{}, err
	}
	if !created {
		if rec, err = s.refreshProjection(ctx, c, rec, *snap, now, l); err != nil {
			return LinkResult{}, err
		}
	}
	link := &domain.ExternalLink{ID: uuid.New(), TenantID: c.tc.TenantID, ExternalRecordID: rec.ID, SessionID: in.SessionID, CaseID: in.CaseID, TaskID: in.TaskID, LinkedBy: c.actorID(), LinkedAt: now}
	linkCreated, existing, err := s.ext.InsertLink(ctx, link)
	if err != nil {
		return LinkResult{}, err
	}
	if !linkCreated {
		return LinkResult{Record: rec, Link: existing, RecordCreated: created}, nil
	}
	if err := s.emitFor(ctx, c, domain.EvExternalLinked, "external_record", rec.ID, now, l,
		map[string]any{"external_type": rec.ExternalType, "status_class": string(rec.StatusClass), "record_created": created}, ""); err != nil {
		return LinkResult{}, err
	}
	s.record(ctx, c, ActionExternalLinked, ResourceExternalRecord, rec.ID, map[string]any{"external_type": rec.ExternalType, "record_created": created})
	s.metrics.Inc("service_external_links_total", "linked")
	return LinkResult{Record: rec, Link: link, LinkCreated: true, RecordCreated: created}, nil
}

// refreshProjection applies a snapshot to a record that already stood and writes the status-change fact when the status moved.
func (s *Service) refreshProjection(ctx context.Context, c caller, rec *domain.ExternalRecord, snap domain.Snapshot, now time.Time, _ links) (*domain.ExternalRecord, error) {
	before := rec.StatusClass
	applied, changed, after, err := s.ext.ApplySnapshot(ctx, rec.ID, snap, now)
	if err != nil {
		return nil, err
	}
	if !applied {
		s.metrics.Inc("service_external_updates_total", "out_of_order")
		return after, nil
	}
	if changed {
		payload := map[string]any{"external_type": rec.ExternalType, "from": string(before), "to": string(after.StatusClass)}
		// the change is told where the people look: on the timeline of every session, case or task the record is linked to
		active, err := s.ext.ActiveLinks(ctx, rec.ID)
		if err != nil {
			return nil, err
		}
		told := 0
		for _, lk := range active {
			tl, err := s.targetLinks(ctx, lk.SessionID, lk.CaseID, lk.TaskID)
			if err != nil {
				continue // a target the caller cannot see is not told by this caller (a system session sees all)
			}
			if err := s.emitFor(ctx, c, domain.EvExternalStatusChanged, "external_record", rec.ID, now, tl, payload, ""); err != nil {
				return nil, err
			}
			told++
		}
		if told == 0 {
			if err := s.emitFor(ctx, c, domain.EvExternalStatusChanged, "external_record", rec.ID, now, links{}, payload, ""); err != nil {
				return nil, err
			}
		}
		s.record(ctx, c, ActionExternalStatus, ResourceExternalRecord, rec.ID, map[string]any{"from": string(before), "to": string(after.StatusClass)})
		s.metrics.Inc("service_external_updates_total", "status_changed")
	}
	return after, nil
}

// UnlinkExternalRecord ends a link (the link stays as history). Idempotent.
func (s *Service) UnlinkExternalRecord(ctx context.Context, linkID uuid.UUID) (*domain.ExternalLink, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermLinkExternal); err != nil {
		return nil, err
	}
	if err := s.external(); err != nil {
		return nil, err
	}
	l, err := s.ext.GetLink(ctx, linkID)
	if err != nil {
		return nil, err
	}
	if l.UnlinkedAt != nil {
		return l, nil
	}
	at, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	done, err := s.ext.Unlink(ctx, linkID, c.actorID(), at)
	if err != nil {
		return nil, err
	}
	if done {
		tl, err := s.targetLinks(ctx, l.SessionID, l.CaseID, l.TaskID)
		if err != nil {
			return nil, err
		}
		if err := s.emitFor(ctx, c, domain.EvExternalUnlinked, "external_record", l.ExternalRecordID, at, tl, map[string]any{}, ""); err != nil {
			return nil, err
		}
		s.record(ctx, c, ActionExternalUnlinked, ResourceExternalRecord, l.ExternalRecordID, nil)
	}
	return s.ext.GetLink(ctx, linkID)
}

func (s *Service) GetExternalRecord(ctx context.Context, id uuid.UUID) (*domain.ExternalRecord, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, err
	}
	if err := s.external(); err != nil {
		return nil, err
	}
	return s.ext.GetRecord(ctx, id)
}

func (s *Service) ListExternalRecords(ctx context.Context, f ports.RecordFilter) ([]domain.ExternalRecord, string, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, "", err
	}
	if err := s.external(); err != nil {
		return nil, "", err
	}
	if f.StatusClass != "" && !f.StatusClass.Valid() {
		return nil, "", domain.Invalid("status_class")
	}
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	return s.ext.ListRecords(ctx, f)
}

// RefreshExternalRecord asks the ERP again and updates the projection. An ERP that does not answer leaves the record as it was (marked stale) and says so; an
// ERP that no longer knows the record marks it as a conflict, it does not delete anything.
func (s *Service) RefreshExternalRecord(ctx context.Context, id uuid.UUID) (*domain.ExternalRecord, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermLinkExternal); err != nil {
		return nil, err
	}
	if err := s.external(); err != nil {
		return nil, err
	}
	rec, err := s.ext.GetRecord(ctx, id)
	if err != nil {
		return nil, err
	}
	gw, err := s.gateways.Resolve(ctx, c.tc.TenantID, rec.IntegrationID)
	if err != nil {
		return nil, err
	}
	callCtx, cancel := s.gatewayCall(ctx)
	snap, gerr := safeGet(callCtx, gw, rec.ExternalType, rec.ExternalID)
	cancel()
	now, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	if gerr != nil {
		kind, _ := ports.KindOf(gerr)
		switch kind {
		case ports.GatewayNotFound:
			if err := s.ext.MarkSync(ctx, rec.ID, domain.SyncConflict, now); err != nil {
				return nil, err
			}
			return s.ext.GetRecord(ctx, id)
		default:
			if err := s.ext.MarkSync(ctx, rec.ID, domain.SyncStale, now); err != nil {
				return nil, err
			}
			return s.ext.GetRecord(ctx, id)
		}
	}
	clean, err := snap.Clean()
	if err != nil || clean.ExternalType != rec.ExternalType || clean.ExternalID != rec.ExternalID {
		if err := s.ext.MarkSync(ctx, rec.ID, domain.SyncError, now); err != nil {
			return nil, err
		}
		return s.ext.GetRecord(ctx, id)
	}
	return s.refreshProjection(ctx, c, rec, clean, now, links{})
}

// ExternalUpdate is a change the ERP pushed (a webhook delivery already authenticated and resolved to a company by the integration layer).
type ExternalUpdate struct {
	IntegrationID uuid.UUID
	DeliveryID    string
	Snapshot      domain.Snapshot
}

// UpdateResult says what an inbound update did.
type UpdateResult struct {
	Applied    bool
	Duplicate  bool
	OutOfOrder bool
	Unknown    bool // no record of this company matches: the module does not create records from a push
}

// ApplyExternalUpdate applies a delivery from the ERP. Idempotent by delivery id (the same delivery twice changes nothing), safe against out-of-order delivery (an
// older provider timestamp never rolls the projection back), and it never creates a record: link first. Only a system session (the integration layer, which
// resolved the company from the stored integration and verified the delivery) may call it.
func (s *Service) ApplyExternalUpdate(ctx context.Context, in ExternalUpdate) (UpdateResult, error) {
	c, err := s.who(ctx)
	if err != nil {
		return UpdateResult{}, err
	}
	if !c.system {
		return UpdateResult{}, domain.ErrForbidden
	}
	if err := s.external(); err != nil {
		return UpdateResult{}, err
	}
	delivery := strings.TrimSpace(in.DeliveryID)
	if in.IntegrationID == uuid.Nil || len(delivery) < 8 || len(delivery) > 72 || !domain.ValidIdempotencyKey(delivery) {
		return UpdateResult{}, domain.Invalid("integration_id and a delivery id of 8-72 characters are required")
	}
	snap, err := in.Snapshot.Clean()
	if err != nil {
		return UpdateResult{}, err
	}
	c.kind = domain.ActorIntegration
	key := "ext:" + in.IntegrationID.String() + ":" + delivery
	hash := domain.Fingerprint("ext-update", snap.ExternalType, snap.ExternalID, snap.ExternalStatus, string(snap.StatusClass), snap.DisplayRef, timeKey(snap.UpdatedAt))
	rec, err := s.replay(ctx, ports.ScopeExternalUpd, key, hash)
	if err != nil {
		return UpdateResult{}, err
	}
	if rec != nil {
		return UpdateResult{Duplicate: true}, nil
	}
	known, err := s.ext.FindRecord(ctx, in.IntegrationID, snap.ExternalType, snap.ExternalID)
	if err != nil {
		return UpdateResult{}, err
	}
	if known == nil {
		return UpdateResult{Unknown: true}, nil
	}
	now, err := s.repo.Now(ctx)
	if err != nil {
		return UpdateResult{}, err
	}
	before := known.Version
	after, err := s.refreshProjection(ctx, c, known, snap, now, links{})
	if err != nil {
		return UpdateResult{}, err
	}
	if err := s.remember(ctx, ports.ScopeExternalUpd, key, hash, known.ID); err != nil {
		return UpdateResult{}, err
	}
	applied := after.Version != before
	return UpdateResult{Applied: applied, OutOfOrder: !applied}, nil
}

func timeKey(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// ---- asking the ERP to create a service order ----

// allowAsk is the permission to ASK the ERP and keep what it answered: the key to ask (service.request_external) AND the key to tie the answer to our work
// (service.link_external). The two are different keys on different tables in the database, and holding only one of them does not let anyone forge the other's rows.
func (s *Service) allowAsk(ctx context.Context, c caller) error {
	if err := s.allow(ctx, c, PermRequestExternal); err != nil {
		return err
	}
	return s.allow(ctx, c, PermLinkExternal)
}

type RequestOutcome string

const (
	OutcomeCreated  RequestOutcome = "created"         // this call asked the ERP and it confirmed
	OutcomeReplay   RequestOutcome = "replay"          // the same Idempotency-Key again: the earlier answer, no new call
	OutcomeRejected RequestOutcome = "rejected"        // the ERP definitively refused; nothing was created
	OutcomeUnknown  RequestOutcome = "outcome_unknown" // no answer: the ERP may have processed it; NOTHING is repeated until it is reconciled
	OutcomeBlocked  RequestOutcome = "blocked"         // another request of this session is in flight or of unknown outcome
)

type RequestResult struct {
	Request *domain.ExternalRequest
	Record  *domain.ExternalRecord
	Outcome RequestOutcome
}

func (s *Service) requestResultFor(ctx context.Context, req *domain.ExternalRequest, replay bool) (RequestResult, error) {
	switch req.State {
	case domain.RequestConfirmedSuccess:
		var rec *domain.ExternalRecord
		if req.ExternalRecordID != nil {
			var err error
			if rec, err = s.ext.GetRecord(ctx, *req.ExternalRecordID); err != nil {
				return RequestResult{}, err
			}
		}
		o := OutcomeCreated
		if replay {
			o = OutcomeReplay
		}
		return RequestResult{Request: req, Record: rec, Outcome: o}, nil
	case domain.RequestConfirmedFailure:
		return RequestResult{Request: req, Outcome: OutcomeRejected}, nil
	default:
		return RequestResult{Request: req, Outcome: OutcomeUnknown}, nil
	}
}

// RequestServiceOrder asks the ERP to create a service order for a session. The ERP is the authority: this module keeps the reference, never an order of its own.
//
// The attempt is made DURABLE in its own transaction BEFORE the ERP is called, and its outcome is written in another AFTER, so neither a crash nor a rollback of the
// request can make a call that may have reached the ERP look as if it never happened. A timeout is `outcome_unknown`, never a failure, and it blocks a new request for the
// session until somebody reconciles it. The same Idempotency-Key never calls the ERP twice.
func (s *Service) RequestServiceOrder(ctx context.Context, raw domain.RequestInput) (RequestResult, error) {
	c, err := s.who(ctx)
	if err != nil {
		return RequestResult{}, err
	}
	in, err := raw.Normalize()
	if err != nil {
		return RequestResult{}, err
	}
	if err := s.allowAsk(ctx, c); err != nil {
		return RequestResult{}, err
	}
	if err := s.external(); err != nil {
		return RequestResult{}, err
	}
	system, ok, err := s.ext.IntegrationSystem(ctx, in.IntegrationID)
	if err != nil {
		return RequestResult{}, err
	}
	if !ok {
		return RequestResult{}, domain.Invalid("integration_id is not an integration of this company")
	}
	gw, err := s.gateways.Resolve(ctx, c.tc.TenantID, in.IntegrationID) // not configured is answered BEFORE any durable state exists
	if err != nil {
		return RequestResult{}, err
	}
	sess, err := s.repo.GetSession(ctx, in.SessionID)
	if err != nil {
		return RequestResult{}, err
	}
	if in.CaseID != nil {
		if _, err := s.cases.GetCase(ctx, *in.CaseID); err != nil {
			return RequestResult{}, err
		}
	}
	now, err := s.repo.Now(ctx)
	if err != nil {
		return RequestResult{}, err
	}
	hash := in.Hash()
	row := &domain.ExternalRequest{
		ID: uuid.New(), TenantID: c.tc.TenantID, IntegrationID: in.IntegrationID, SessionID: in.SessionID, CaseID: in.CaseID, Operation: domain.OperationCreateServiceOrder,
		Title: in.Title, IdempotencyKey: in.IdempotencyKey, RequestHash: hash, RequestedBy: c.actorID(), CreatedAt: now,
	}
	row.CorrelationKey = fmt.Sprintf("omnira-%s-%s", strings.ReplaceAll(c.tc.TenantID.String(), "-", "")[:12], strings.ReplaceAll(row.ID.String(), "-", ""))

	// A. the attempt becomes durable
	var acquired, existing, blocker *domain.ExternalRequest
	if err := s.ext.InNewTransaction(ctx, func(ctx context.Context) error {
		var e error
		acquired, existing, blocker, e = s.ext.AcquireRequest(ctx, row)
		if e != nil || acquired == nil {
			return e
		}
		return s.emitFor(ctx, c, domain.EvExternalRequested, "external_request", acquired.ID, now, links{session: &sess.ID, conv: sess.ConversationID, contact: sess.ContactID, customer: sess.CustomerID, caseID: in.CaseID},
			map[string]any{"operation": domain.OperationCreateServiceOrder}, "")
	}); err != nil {
		return RequestResult{}, err
	}
	switch {
	case existing != nil:
		if existing.RequestHash != hash {
			return RequestResult{}, domain.ErrIdempotencyMismatch
		}
		return s.requestResultFor(ctx, existing, true)
	case blocker != nil:
		return RequestResult{Request: blocker, Outcome: OutcomeBlocked}, nil
	}
	s.record(ctx, c, ActionExternalRequested, ResourceExternalRequest, acquired.ID, map[string]any{"operation": domain.OperationCreateServiceOrder})

	// B. the call, with no database lock held
	callCtx, cancel := s.gatewayCall(ctx)
	snap, gerr := safeCreate(callCtx, gw, ports.CreateOrderRequest{CorrelationKey: acquired.CorrelationKey, Title: in.Title, Description: in.Description})
	cancel()

	// C. the outcome, in its own transaction
	var result RequestResult
	err = s.ext.InNewTransaction(ctx, func(ctx context.Context) error {
		at, e := s.repo.Now(ctx)
		if e != nil {
			return e
		}
		if gerr == nil && snap != nil {
			clean, cerr := snap.Clean()
			if cerr == nil && clean.ExternalType == domain.ExternalTypeServiceOrder && (clean.CorrelationKey == "" || clean.CorrelationKey == acquired.CorrelationKey) {
				rec, e := s.settleSuccess(ctx, c, acquired, system, clean, "gateway", at, sess)
				if e != nil {
					return e
				}
				result = RequestResult{Request: acquired, Record: rec, Outcome: OutcomeCreated}
				return nil
			}
			gerr = &ports.GatewayError{Kind: ports.GatewayUnknown, Class: "unusable_answer"} // the ERP answered, but not with a record we can read: treat as unknown
		}
		kind, class := ports.KindOf(gerr)
		from := acquired.State
		if kind == ports.GatewayRejected {
			acquired.State = domain.RequestConfirmedFailure
			cl := safeClass(class)
			src := "gateway"
			acquired.FailureClass, acquired.ResolutionSource, acquired.ResolvedAt = &cl, &src, &at
			result = RequestResult{Request: acquired, Outcome: OutcomeRejected}
		} else {
			acquired.State = domain.RequestOutcomeUnknown
			if class != "" {
				cl := safeClass(class)
				acquired.FailureClass = &cl
			}
			result = RequestResult{Request: acquired, Outcome: OutcomeUnknown}
		}
		if e := s.ext.UpdateRequest(ctx, acquired, acquired.Version); e != nil {
			return e
		}
		return s.requestStateChanged(ctx, c, acquired, from, "gateway", at, sess)
	})
	if err != nil {
		return RequestResult{}, err
	}
	return result, nil
}

func safeClass(class string) string {
	if domain.IsToken(strings.ReplaceAll(strings.ToLower(class), "-", "_")) && len(class) <= 40 {
		return strings.ReplaceAll(strings.ToLower(class), "-", "_")
	}
	return "other"
}

func (s *Service) requestStateChanged(ctx context.Context, c caller, req *domain.ExternalRequest, from domain.RequestState, source string, at time.Time, sess *domain.Session) error {
	l := links{session: &sess.ID, conv: sess.ConversationID, contact: sess.ContactID, customer: sess.CustomerID, caseID: req.CaseID}
	if err := s.emitFor(ctx, c, domain.EvExternalRequestState, "external_request", req.ID, at, l,
		map[string]any{"from": string(from), "to": string(req.State), "source": source}, ""); err != nil {
		return err
	}
	s.record(ctx, c, ActionExternalRequest, ResourceExternalRequest, req.ID, map[string]any{"from": string(from), "to": string(req.State), "source": source})
	s.metrics.Inc("service_external_requests_total", string(req.State))
	return nil
}

// settleSuccess records what the ERP confirmed: the record (found or made), the link to the session (and the case), and the request as confirmed. All in the
// transaction it is called in.
func (s *Service) settleSuccess(ctx context.Context, c caller, req *domain.ExternalRequest, system string, snap domain.Snapshot, source string, at time.Time, sess *domain.Session) (*domain.ExternalRecord, error) {
	rec, created, err := s.ext.InsertRecord(ctx, recordFromSnapshot(req.TenantID, req.IntegrationID, system, snap, req.RequestedBy, at))
	if err != nil {
		return nil, err
	}
	if !created {
		if rec, err = s.refreshProjection(ctx, c, rec, snap, at, links{}); err != nil {
			return nil, err
		}
	}
	l := links{session: &sess.ID, conv: sess.ConversationID, contact: sess.ContactID, customer: sess.CustomerID}
	for _, target := range []struct{ session, cs *uuid.UUID }{{&req.SessionID, nil}, {nil, req.CaseID}} {
		if target.session == nil && target.cs == nil {
			continue
		}
		link := &domain.ExternalLink{ID: uuid.New(), TenantID: req.TenantID, ExternalRecordID: rec.ID, SessionID: target.session, CaseID: target.cs, LinkedBy: c.actorID(), LinkedAt: at}
		made, _, err := s.ext.InsertLink(ctx, link)
		if err != nil {
			return nil, err
		}
		if made {
			ll := l
			if target.cs != nil {
				ll.caseID = target.cs
			}
			if err := s.emitFor(ctx, c, domain.EvExternalLinked, "external_record", rec.ID, at, ll,
				map[string]any{"external_type": rec.ExternalType, "status_class": string(rec.StatusClass), "record_created": created, "via": "request"}, ""); err != nil {
				return nil, err
			}
		}
	}
	from := req.State
	req.State = domain.RequestConfirmedSuccess
	req.FailureClass = nil // the class of an earlier "no answer" is history (the event), not a property of a request that succeeded
	req.ExternalRecordID = &rec.ID
	src := source
	req.ResolutionSource, req.ResolvedAt, req.ResolvedBy = &src, &at, c.actorID()
	if err := s.ext.UpdateRequest(ctx, req, req.Version); err != nil {
		return nil, err
	}
	if err := s.requestStateChanged(ctx, c, req, from, source, at, sess); err != nil {
		return nil, err
	}
	return rec, nil
}

func (s *Service) GetExternalRequest(ctx context.Context, id uuid.UUID) (*domain.ExternalRequest, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, err
	}
	if err := s.external(); err != nil {
		return nil, err
	}
	return s.ext.GetRequest(ctx, id)
}

func (s *Service) ListExternalRequests(ctx context.Context, f ports.RequestFilter) ([]domain.ExternalRequest, string, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, "", err
	}
	if err := s.external(); err != nil {
		return nil, "", err
	}
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	return s.ext.ListRequests(ctx, f)
}

// ReconcileResult says what a reconciliation found.
type ReconcileResult struct {
	Request     *domain.ExternalRequest
	Record      *domain.ExternalRecord
	NeedsManual bool // the ERP offers no way to look the record up again: somebody has to check and call ResolveExternalRequest
	TooEarly    bool // the ERP does not know it YET; not believed until the request is old enough
}

// ReconcileExternalRequest asks the ERP whether a request of unknown outcome was in fact processed, by its correlation key. Found: confirmed. Not found AND the request
// is old enough: confirmed failure (which frees the session for a new request). Anything else changes nothing.
func (s *Service) ReconcileExternalRequest(ctx context.Context, id uuid.UUID) (ReconcileResult, error) {
	c, err := s.who(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	if err := s.allowAsk(ctx, c); err != nil {
		return ReconcileResult{}, err
	}
	if err := s.external(); err != nil {
		return ReconcileResult{}, err
	}
	req, err := s.ext.GetRequest(ctx, id)
	if err != nil {
		return ReconcileResult{}, err
	}
	if !req.State.Blocking() {
		return ReconcileResult{Request: req}, nil
	}
	// a request still in flight may be answered at any moment by its own call: it is not reconciled while that call (or the transactions around it) can still be
	// alive. The longest it can live is its first transaction + the call + the transaction that writes the outcome, plus a margin for scheduling.
	if req.State == domain.RequestInFlight {
		now, nerr := s.repo.Now(ctx)
		if nerr != nil {
			return ReconcileResult{}, nerr
		}
		if now.Sub(req.CreatedAt) < 2*ports.ExternalTxTimeout+s.gatewayTO+30*time.Second {
			return ReconcileResult{Request: req, TooEarly: true}, nil
		}
	}
	gw, err := s.gateways.Resolve(ctx, c.tc.TenantID, req.IntegrationID)
	if err != nil {
		return ReconcileResult{}, err
	}
	finder, ok := gw.(ports.CorrelationLookup)
	if !ok {
		return ReconcileResult{Request: req, NeedsManual: true}, nil
	}
	callCtx, cancel := s.gatewayCall(ctx)
	snap, gerr := finder.FindByCorrelation(callCtx, req.CorrelationKey)
	cancel()
	system, _, err := s.ext.IntegrationSystem(ctx, req.IntegrationID)
	if err != nil {
		return ReconcileResult{}, err
	}
	sess, err := s.repo.GetSession(ctx, req.SessionID)
	if err != nil {
		return ReconcileResult{}, err
	}
	locked, err := s.ext.LockRequest(ctx, id)
	if err != nil {
		return ReconcileResult{}, err
	}
	if !locked.State.Blocking() { // settled meanwhile
		return ReconcileResult{Request: locked}, nil
	}
	at, err := s.repo.Now(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	if gerr == nil && snap != nil {
		clean, cerr := snap.Clean()
		if cerr != nil || clean.ExternalType != domain.ExternalTypeServiceOrder || (clean.CorrelationKey != "" && clean.CorrelationKey != locked.CorrelationKey) {
			return ReconcileResult{Request: locked}, nil // an answer that is not a service order, or that carries ANOTHER correlation key, teaches nothing
		}
		rec, err := s.settleSuccess(ctx, c, locked, system, clean, "reconcile", at, sess)
		if err != nil {
			return ReconcileResult{}, err
		}
		return ReconcileResult{Request: locked, Record: rec}, nil
	}
	if kind, _ := ports.KindOf(gerr); kind != ports.GatewayNotFound {
		return ReconcileResult{Request: locked}, nil // the ERP did not answer: nothing is learned
	}
	if at.Sub(locked.CreatedAt) < s.reconcileAge {
		return ReconcileResult{Request: locked, TooEarly: true}, nil
	}
	from := locked.State
	locked.State = domain.RequestConfirmedFailure
	cl, src := "not_found_by_correlation", "reconcile"
	locked.FailureClass, locked.ResolutionSource, locked.ResolvedAt, locked.ResolvedBy = &cl, &src, &at, c.actorID()
	if err := s.ext.UpdateRequest(ctx, locked, locked.Version); err != nil {
		return ReconcileResult{}, err
	}
	if err := s.requestStateChanged(ctx, c, locked, from, "reconcile", at, sess); err != nil {
		return ReconcileResult{}, err
	}
	return ReconcileResult{Request: locked}, nil
}

// ResolveExternalRequest settles by hand a request whose outcome is unknown, after somebody checked the ERP: it created the record (give its id; the ERP is asked to
// confirm it exists) or it created nothing (give the reason).
func (s *Service) ResolveExternalRequest(ctx context.Context, id uuid.UUID, raw domain.ResolveInput) (ReconcileResult, error) {
	c, err := s.who(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	in, err := raw.Normalize()
	if err != nil {
		return ReconcileResult{}, err
	}
	if err := s.allowAsk(ctx, c); err != nil {
		return ReconcileResult{}, err
	}
	if err := s.external(); err != nil {
		return ReconcileResult{}, err
	}
	req, err := s.ext.GetRequest(ctx, id)
	if err != nil {
		return ReconcileResult{}, err
	}
	if !req.State.Blocking() {
		return ReconcileResult{}, domain.ErrInvalidTransition
	}
	system, _, err := s.ext.IntegrationSystem(ctx, req.IntegrationID)
	if err != nil {
		return ReconcileResult{}, err
	}
	sess, err := s.repo.GetSession(ctx, req.SessionID)
	if err != nil {
		return ReconcileResult{}, err
	}
	var clean *domain.Snapshot
	if in.Created {
		gw, err := s.gateways.Resolve(ctx, c.tc.TenantID, req.IntegrationID)
		if err != nil {
			return ReconcileResult{}, err
		}
		if clean, err = s.readSnapshot(ctx, gw, domain.ExternalTypeServiceOrder, in.ExternalID); err != nil {
			return ReconcileResult{}, err
		}
		if clean.ExternalType != domain.ExternalTypeServiceOrder || clean.ExternalID != in.ExternalID {
			return ReconcileResult{}, domain.Invalid("the external system answered with another record")
		}
	}
	locked, err := s.ext.LockRequest(ctx, id)
	if err != nil {
		return ReconcileResult{}, err
	}
	if !locked.State.Blocking() {
		return ReconcileResult{}, domain.ErrInvalidTransition
	}
	at, err := s.repo.Now(ctx)
	if err != nil {
		return ReconcileResult{}, err
	}
	if in.Created {
		rec, err := s.settleSuccess(ctx, c, locked, system, *clean, "manual", at, sess)
		if err != nil {
			return ReconcileResult{}, err
		}
		return ReconcileResult{Request: locked, Record: rec}, nil
	}
	from := locked.State
	locked.State = domain.RequestConfirmedFailure
	cl, src := "not_created", "manual"
	locked.FailureClass, locked.ResolutionSource, locked.ResolvedAt, locked.ResolvedBy = &cl, &src, &at, c.actorID()
	if err := s.ext.UpdateRequest(ctx, locked, locked.Version); err != nil {
		return ReconcileResult{}, err
	}
	l := links{session: &sess.ID, conv: sess.ConversationID, contact: sess.ContactID, customer: sess.CustomerID, caseID: locked.CaseID}
	if err := s.emitFor(ctx, c, domain.EvExternalRequestState, "external_request", locked.ID, at, l,
		map[string]any{"from": string(from), "to": string(locked.State), "source": "manual"}, in.Reason); err != nil {
		return ReconcileResult{}, err
	}
	s.record(ctx, c, ActionExternalRequest, ResourceExternalRequest, locked.ID, map[string]any{"from": string(from), "to": string(locked.State), "source": "manual"})
	return ReconcileResult{Request: locked}, nil
}
