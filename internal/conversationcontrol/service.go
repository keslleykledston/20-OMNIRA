package conversationcontrol

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// Service holds no state: every call takes the caller's querier (the request or worker transaction).
type Service struct{}

func (Service) load(ctx context.Context, q platformdb.Querier, tenantID, conversationID uuid.UUID, lock bool) (State, error) {
	sql := `SELECT id, status, control_kind, control_holder_id, control_version, control_since, control_origin, assigned_to_user_id
	          FROM conversations WHERE tenant_id = $1 AND id = $2`
	if lock {
		sql += ` FOR UPDATE`
	}
	var s State
	var kind, origin *string
	err := q.QueryRow(ctx, sql, tenantID, conversationID).
		Scan(&s.ConversationID, &s.Status, &kind, &s.HolderID, &s.Version, &s.Since, &origin, &s.AssignedUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	if kind != nil {
		k := Kind(*kind)
		s.Kind = &k
	}
	if origin != nil {
		o := Origin(*origin)
		s.Origin = &o
	}
	return s, nil
}

// Get reads the control without locking (informative reads).
func (s Service) Get(ctx context.Context, q platformdb.Querier, tenantID, conversationID uuid.UUID) (State, error) {
	return s.load(ctx, q, tenantID, conversationID, false)
}

// Lock reads it FOR UPDATE: the serialization point of every control change.
func (s Service) Lock(ctx context.Context, q platformdb.Querier, tenantID, conversationID uuid.UUID) (State, error) {
	return s.load(ctx, q, tenantID, conversationID, true)
}

// begin asks the database for the capability to change the hold of an external application in THIS transaction (see migration 138): granted to a system
// session and to a person holding conversation.takeover_external, refused (ErrNotAllowed) to anybody else.
func (Service) begin(ctx context.Context, q platformdb.Querier, tenantID uuid.UUID) error {
	if _, err := q.Exec(ctx, `SELECT conversation_control_begin($1)`, tenantID); err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "OM004" {
			return ErrNotAllowed
		}
		return err
	}
	return nil
}

// record writes the typed history through conversation_control_record: the database fills in what the conversation IS now (version, holder) and refuses a
// writer without the capability, so a row can only describe a change that happened in this transaction.
func (Service) record(ctx context.Context, q platformdb.Querier, tenantID uuid.UUID, before State, actor Actor, reason string) error {
	var fromKind *string
	if before.Kind != nil {
		k := string(*before.Kind)
		fromKind = &k
	}
	var actorID *uuid.UUID
	if actor.ID != uuid.Nil {
		a := actor.ID
		actorID = &a
	}
	nz := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	_, err := q.Exec(ctx, `SELECT conversation_control_record($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		tenantID, before.ConversationID, fromKind, before.HolderID, string(actor.Kind), actorID, reason, nz(actor.RequestID), nz(actor.ClaimedAgentRef))
	return err
}

// requestRouting asks the router to look at a conversation that has just become free, exactly as the inbound path does (a round-robin or
// least-loaded queue gets an assignment job; a manual queue keeps it visible for a person to claim).
func (Service) requestRouting(ctx context.Context, q platformdb.Querier, tenantID, conversationID uuid.UUID) error {
	_, err := q.Exec(ctx, `
		INSERT INTO outbox_events(tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, payload)
		SELECT c.tenant_id, 'job.routing.assign.v1', 'conversation', c.id::text, uuid_generate_v4(), '{}'::jsonb
		  FROM conversations c JOIN queues qq ON qq.tenant_id = c.tenant_id AND qq.id = c.queue_id
		 WHERE c.tenant_id = $1 AND c.id = $2 AND c.status = 'open' AND qq.mode IN ('round_robin', 'least_loaded')`, tenantID, conversationID)
	return err
}

func checkReason(reason string) (string, error) {
	r := strings.TrimSpace(reason)
	if r == "" {
		return "", ErrReasonNeeded
	}
	if utf8.RuneCountInString(r) > ReasonMaxLen {
		r = string([]rune(r)[:ReasonMaxLen])
	}
	return r, nil
}

// ClaimByApplication gives a FREE, open conversation to an external application. Repeating it by the same application changes nothing
// (changed=false, the same version). ifMatch, when given, must equal the current version (a client that read the conversation and wants to be
// sure nothing happened since). A conversation held by anyone else is ErrHeldByOther, never taken.
func (s Service) ClaimByApplication(ctx context.Context, q platformdb.Querier, tenantID, conversationID, installationID uuid.UUID, ifMatch *int64, actor Actor) (State, bool, error) {
	cur, err := s.Lock(ctx, q, tenantID, conversationID)
	if err != nil {
		return cur, false, err
	}
	if cur.HeldBy(KindCRMApplication, installationID) {
		if ifMatch != nil && *ifMatch != cur.Version {
			return cur, false, ErrStaleVersion
		}
		return cur, false, nil
	}
	if cur.Status != "open" {
		return cur, false, ErrClosed
	}
	if !cur.Free() {
		return cur, false, ErrHeldByOther
	}
	if ifMatch != nil && *ifMatch != cur.Version {
		return cur, false, ErrStaleVersion
	}
	if err := s.begin(ctx, q, tenantID); err != nil {
		return cur, false, err
	}
	next := cur.Version + 1
	if _, err := q.Exec(ctx, `
		UPDATE conversations SET control_kind = 'crm_application', control_holder_id = $3, control_version = $4, control_since = now(), control_origin = 'crm', updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`, tenantID, conversationID, installationID, next); err != nil {
		return cur, false, err
	}
	if err := s.record(ctx, q, tenantID, cur, actor, "crm_claim"); err != nil {
		return cur, false, err
	}
	st, err := s.Get(ctx, q, tenantID, conversationID)
	return st, err == nil, err
}

// CreateHeldByApplication grants, inside the caller's transaction, the capability a brand-new conversation needs to be inserted already
// held by an external application (ADR-0050): there is no prior holder to take from — the row does not exist yet — so this is the one
// place control is set directly at INSERT time instead of by an UPDATE transition. insert must perform that INSERT itself (control_kind =
// 'crm_application', control_holder_id, control_version = 1, control_since, control_origin = 'crm') and nothing else may run between the
// capability grant and the insert. The history record describes "before: free" (there was no holder, which a nil Kind/HolderID already
// means) even though, literally, there was no row — the same meaning, the simplest representation.
func (s Service) CreateHeldByApplication(ctx context.Context, q platformdb.Querier, tenantID, conversationID uuid.UUID, insert func(ctx context.Context) error, actor Actor) error {
	if err := s.begin(ctx, q, tenantID); err != nil {
		return err
	}
	if err := insert(ctx); err != nil {
		return err
	}
	return s.record(ctx, q, tenantID, State{ConversationID: conversationID}, actor, "crm_create")
}

// ReleaseByApplication frees a conversation the application holds. The version must match (the application must know what it is releasing);
// releasing what it does not hold is ErrNotHolder, releasing it twice is ErrNotHolder as well (the second time it is free or someone else's).
func (s Service) ReleaseByApplication(ctx context.Context, q platformdb.Querier, tenantID, conversationID, installationID uuid.UUID, ifMatch int64, actor Actor) (State, error) {
	cur, err := s.Lock(ctx, q, tenantID, conversationID)
	if err != nil {
		return cur, err
	}
	if !cur.HeldBy(KindCRMApplication, installationID) {
		return cur, ErrNotHolder
	}
	if ifMatch != cur.Version {
		return cur, ErrStaleVersion
	}
	return s.free(ctx, q, tenantID, cur, actor, "crm_release")
}

// free sets the conversation free (the conversation is locked and loaded by the caller), records it and asks the router to look at it.
func (s Service) free(ctx context.Context, q platformdb.Querier, tenantID uuid.UUID, cur State, actor Actor, reason string) (State, error) {
	if err := s.begin(ctx, q, tenantID); err != nil {
		return cur, err
	}
	next := cur.Version + 1
	if _, err := q.Exec(ctx, `
		UPDATE conversations SET control_kind = NULL, control_holder_id = NULL, control_since = NULL, control_origin = NULL, control_version = $3, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`, tenantID, cur.ConversationID, next); err != nil {
		return cur, err
	}
	if err := s.record(ctx, q, tenantID, cur, actor, reason); err != nil {
		return cur, err
	}
	if err := s.requestRouting(ctx, q, tenantID, cur.ConversationID); err != nil {
		return cur, err
	}
	return s.Get(ctx, q, tenantID, cur.ConversationID)
}

// TakeOver is a supervisor taking a conversation away from an external application: explicit, with a reason, and never automatic after a
// conflict. The supervisor becomes the holder (tenant_user) and assigned_to_user_id follows. The caller has already checked the permission
// (conversation.takeover_external) and that the supervisor is who the session says it is.
func (s Service) TakeOver(ctx context.Context, q platformdb.Querier, tenantID, conversationID, supervisor uuid.UUID, reason string, requestID string) (State, error) {
	why, err := checkReason(reason)
	if err != nil {
		return State{}, err
	}
	cur, err := s.Lock(ctx, q, tenantID, conversationID)
	if err != nil {
		return cur, err
	}
	if cur.Status != "open" {
		return cur, ErrClosed
	}
	if !cur.HeldExternally() {
		return cur, ErrNotExternal
	}
	if err := s.begin(ctx, q, tenantID); err != nil {
		return cur, err
	}
	if cur.Kind != nil && *cur.Kind == KindCRMApplication && cur.HolderID != nil {
		if err := s.cancelQueued(ctx, q, tenantID, conversationID, *cur.HolderID); err != nil {
			return cur, err
		}
	}
	next := cur.Version + 1
	if _, err := q.Exec(ctx, `
		UPDATE conversations
		   SET assigned_to_user_id = $3, assigned_at = now(), control_kind = 'tenant_user', control_holder_id = $3, control_version = $4,
		       control_since = now(), control_origin = 'workspace', updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`, tenantID, conversationID, supervisor, next); err != nil {
		return cur, err
	}
	if err := s.record(ctx, q, tenantID, cur, Actor{Kind: ActorTenantUser, ID: supervisor, RequestID: requestID}, "takeover: "+why); err != nil {
		return cur, err
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO assignment_events(tenant_id, conversation_id, from_user_id, to_user_id, changed_by, reason)
		VALUES ($1,$2,NULL,$3,$3,'takeover_external')`, tenantID, conversationID, supervisor); err != nil {
		return cur, err
	}
	return s.Get(ctx, q, tenantID, conversationID)
}

// Target is where an application sends a conversation it holds: always a queue of the company, and optionally one person who is eligible in it.
type Target struct {
	QueueID uuid.UUID
	UserID  *uuid.UUID
}

// Eligible says whether a person may receive a conversation of a queue now (membership, shift, capacity: the rule the router applies). It is a function
// because the rule lives in the sectors module, which this package does not import.
type Eligible func(ctx context.Context, q platformdb.Querier, tenantID, queueID, userID uuid.UUID) (bool, error)

// TransferByApplication hands a conversation the application holds back to the company: into a queue (the router looks at it exactly as it does for a
// conversation that just arrived) or straight to one eligible person of that queue. The version must match; the application must be the holder; a queue
// that does not exist in the company, or a person who is not eligible in it, is ErrInvalidTarget and nothing changes. The application stops holding it in
// the same statement, so no moment exists in which both sides think they own it.
func (s Service) TransferByApplication(ctx context.Context, q platformdb.Querier, tenantID, conversationID, installationID uuid.UUID, ifMatch int64, to Target, actor Actor, eligible Eligible) (State, error) {
	cur, err := s.Lock(ctx, q, tenantID, conversationID)
	if err != nil {
		return cur, err
	}
	if !cur.HeldBy(KindCRMApplication, installationID) {
		return cur, ErrNotHolder
	}
	if ifMatch != cur.Version {
		return cur, ErrStaleVersion
	}
	if cur.Status != "open" {
		return cur, ErrClosed
	}
	var queueMode string
	if err := q.QueryRow(ctx, `SELECT mode FROM queues WHERE tenant_id = $1 AND id = $2`, tenantID, to.QueueID).Scan(&queueMode); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return cur, ErrInvalidTarget
		}
		return cur, err
	}
	if to.UserID != nil {
		if eligible == nil {
			return cur, ErrInvalidTarget
		}
		ok, err := eligible(ctx, q, tenantID, to.QueueID, *to.UserID)
		if err != nil {
			return cur, err
		}
		if !ok {
			return cur, ErrInvalidTarget
		}
	}
	if err := s.begin(ctx, q, tenantID); err != nil {
		return cur, err
	}
	next := cur.Version + 1
	if to.UserID == nil {
		if _, err := q.Exec(ctx, `
			UPDATE conversations
			   SET queue_id = $3, assigned_to_user_id = NULL, assigned_at = NULL,
			       control_kind = NULL, control_holder_id = NULL, control_since = NULL, control_origin = NULL, control_version = $4,
			       routing_retry_at = CASE WHEN $5 IN ('round_robin', 'least_loaded') THEN now() + interval '90 seconds' ELSE NULL END, updated_at = now()
			 WHERE tenant_id = $1 AND id = $2`, tenantID, conversationID, to.QueueID, next, queueMode); err != nil {
			return cur, err
		}
		if err := s.record(ctx, q, tenantID, cur, actor, "crm_transfer_queue"); err != nil {
			return cur, err
		}
		if err := s.requestRouting(ctx, q, tenantID, conversationID); err != nil {
			return cur, err
		}
		return s.Get(ctx, q, tenantID, conversationID)
	}
	if _, err := q.Exec(ctx, `
		UPDATE conversations
		   SET queue_id = $3, assigned_to_user_id = $5, assigned_at = now(), routing_retry_at = NULL,
		       control_kind = 'tenant_user', control_holder_id = $5, control_since = now(), control_origin = 'crm', control_version = $4, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`, tenantID, conversationID, to.QueueID, next, *to.UserID); err != nil {
		return cur, err
	}
	if err := s.record(ctx, q, tenantID, cur, actor, "crm_transfer_user"); err != nil {
		return cur, err
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO assignment_events(tenant_id, conversation_id, from_user_id, to_user_id, changed_by, reason, actor_source)
		VALUES ($1,$2,NULL,$3,NULL,'crm_transfer','system')`, tenantID, conversationID, *to.UserID); err != nil {
		return cur, err
	}
	return s.Get(ctx, q, tenantID, conversationID)
}

// cancelQueued ends, as failed with the reason control_lost, the messages an application queued that the delivery worker has not sent yet. It is called in the same
// transaction that takes the conversation away from that application AGAINST its will (a supervisor's takeover, a suspension or revocation, an expired lease, a scope or
// allow-list change): a message it queued while it held the conversation must not leave after somebody else took over. When the application itself lets go (release,
// transfer, finalize) what it queued before is still its own and goes out. A message the worker already picked is not touched (it is sent; the UPDATE waits for the worker's
// row lock and finds it no longer queued).
func (Service) cancelQueued(ctx context.Context, q platformdb.Querier, tenantID, conversationID, installationID uuid.UUID) error {
	_, err := q.Exec(ctx, `
		UPDATE messages SET status = 'failed', failure_reason = 'control_lost', updated_at = now()
		 WHERE tenant_id = $1 AND conversation_id = $2 AND sent_by_installation_id = $3 AND direction = 'outbound' AND status = 'queued'`,
		tenantID, conversationID, installationID)
	return err
}

// DrainOutOfScope frees the conversations an installation holds that it may no longer hold: its scopes lost conversation.claim, or the conversation is outside its (new)
// queue or line allow-list. Without it a CRM whose permission was narrowed would keep a conversation it can no longer see or touch, with no way to give it back.
func (s Service) DrainOutOfScope(ctx context.Context, q platformdb.Querier, tenantID, installationID uuid.UUID, reason string) (int, error) {
	why, err := checkReason(reason)
	if err != nil {
		return 0, err
	}
	rows, err := q.Query(ctx, `
		SELECT c.id FROM conversations c
		 WHERE c.tenant_id = $1 AND c.control_kind = 'crm_application' AND c.control_holder_id = $2
		   AND ( NOT EXISTS (SELECT 1 FROM external_app_installations i WHERE i.tenant_id = c.tenant_id AND i.id = $2 AND i.status = 'active' AND 'conversation.claim' = ANY (i.scopes))
		      OR (EXISTS (SELECT 1 FROM external_app_installation_queues x WHERE x.tenant_id = c.tenant_id AND x.installation_id = $2)
		          AND NOT EXISTS (SELECT 1 FROM external_app_installation_queues x WHERE x.tenant_id = c.tenant_id AND x.installation_id = $2 AND x.queue_id = c.queue_id))
		      OR (EXISTS (SELECT 1 FROM external_app_installation_channels x WHERE x.tenant_id = c.tenant_id AND x.installation_id = $2)
		          AND NOT EXISTS (SELECT 1 FROM external_app_installation_channels x WHERE x.tenant_id = c.tenant_id AND x.installation_id = $2 AND x.connection_id = c.channel_connection_id)) )
		 ORDER BY c.id FOR UPDATE OF c`, tenantID, installationID)
	if err != nil {
		return 0, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		cur, err := s.Get(ctx, q, tenantID, id)
		if err != nil {
			return 0, err
		}
		if err := s.cancelQueued(ctx, q, tenantID, id, installationID); err != nil {
			return 0, err
		}
		if _, err := s.free(ctx, q, tenantID, cur, Actor{Kind: ActorSystem}, "installation "+why); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// DrainInstallation frees every open conversation an installation holds, in the transaction that suspends or revokes it, and asks the router to
// look at each. It returns how many it freed. Closed conversations lose their holder too (a closed conversation held by a revoked application would
// be an orphan), without a routing request.
func (s Service) DrainInstallation(ctx context.Context, q platformdb.Querier, tenantID, installationID uuid.UUID, reason string) (int, error) {
	why, err := checkReason(reason)
	if err != nil {
		return 0, err
	}
	rows, err := q.Query(ctx, `
		SELECT id FROM conversations
		 WHERE tenant_id = $1 AND control_kind = 'crm_application' AND control_holder_id = $2
		 ORDER BY id FOR UPDATE`, tenantID, installationID)
	if err != nil {
		return 0, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		cur, err := s.Get(ctx, q, tenantID, id) // already locked by the query above
		if err != nil {
			return n, err
		}
		if cur.HeldBy(KindCRMApplication, installationID) { // not yet closed-and-released
			if err := s.cancelQueued(ctx, q, tenantID, id, installationID); err != nil {
				return n, err
			}
		}
		if _, err := s.free(ctx, q, tenantID, cur, Actor{Kind: ActorSystem}, "installation "+why); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ExpireLease frees a conversation whose holder (an application) stopped renewing its lease. The caller has decided that the lease ran out; this
// re-reads the conversation under its lock and only frees it if THAT installation still holds it (a release, a transfer, a takeover or a close that got
// there first wins). The reason is lease_expired and the router looks at it again.
func (s Service) ExpireLease(ctx context.Context, q platformdb.Querier, tenantID, conversationID, installationID uuid.UUID) (bool, error) {
	return s.ExpireHold(ctx, q, tenantID, conversationID, installationID, "lease_expired")
}

// ExpireHold is ExpireLease with the reason the worker records (lease_expired, no_credential).
func (s Service) ExpireHold(ctx context.Context, q platformdb.Querier, tenantID, conversationID, installationID uuid.UUID, reason string) (bool, error) {
	cur, err := s.Lock(ctx, q, tenantID, conversationID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if !cur.HeldBy(KindCRMApplication, installationID) || cur.Status != "open" {
		return false, nil
	}
	if err := s.cancelQueued(ctx, q, tenantID, conversationID, installationID); err != nil {
		return false, err
	}
	if _, err := s.free(ctx, q, tenantID, cur, Actor{Kind: ActorSystem}, reason); err != nil {
		return false, err
	}
	return true, nil
}

// ReleaseOnClose frees the hold of an external application when the attendance is finalized by anyone (the conversation is already locked and
// closed by the caller). A closed conversation held by an application would be an orphan: the contact's next message starts a NEW conversation.
func (s Service) ReleaseOnClose(ctx context.Context, q platformdb.Querier, tenantID, conversationID uuid.UUID) (bool, error) {
	cur, err := s.Get(ctx, q, tenantID, conversationID)
	if err != nil {
		return false, err
	}
	if !cur.HeldExternally() {
		return false, nil
	}
	if _, err := s.free(ctx, q, tenantID, cur, Actor{Kind: ActorSystem}, "conversation_closed"); err != nil {
		return false, err
	}
	return true, nil
}
