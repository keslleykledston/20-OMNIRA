// Package application holds the use cases of Service Context & Operations (ADR-0049). Workspace, API and flows all call this one service: the rules
// live here, never in the frontend (ADR-09).
package application

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/servicectx/domain"
	"github.com/omnira/omnira/internal/servicectx/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Permissions (migration 000150).
const (
	PermRead            = "service.read"
	PermCreate          = "service.create"
	PermUpdate          = "service.update"
	PermClose           = "service.close"
	PermAssign          = "service.assign"
	PermLinkExternal    = "service.link_external"
	PermRequestExternal = "service.request_external"
)

// Audit actions and resource types (plain strings: the shared audit domain file stays untouched).
const (
	ActionSessionOpened     = "service_session.opened"
	ActionSessionClassified = "service_session.classified"
	ActionSessionStatus     = "service_session.status_changed"
	ActionSessionClosed     = "service_session.closed"
	ActionSessionReopened   = "service_session.reopened"
	ResourceSession         = "service_session"
)

type Service struct {
	repo         ports.Repository
	authz        ports.Authorizer
	audit        ports.Auditor
	events       ports.Events
	links        ports.LinkCounter
	timeline     ports.TimelineRepository
	cases        ports.CaseRepository
	tasks        ports.TaskRepository
	ctxRepo      ports.ContextRepository
	catalog      ports.CatalogRepository
	ext          ports.ExternalRepository
	gateways     ports.GatewayResolver
	gatewayTO    time.Duration
	reconcileAge time.Duration
	reopenWindow time.Duration
	metrics      Metrics
}

// Metrics is the counter surface of the module (closed label sets only; never an id or a text). Optional.
type Metrics interface {
	Inc(name string, labels ...string)
}

type noMetrics struct{}

func (noMetrics) Inc(string, ...string) {}

func NewService(repo ports.Repository, authz ports.Authorizer, audit ports.Auditor) *Service {
	return &Service{repo: repo, authz: authz, audit: audit, events: ports.NopEvents{}, reopenWindow: domain.DefaultReopenWindow, metrics: noMetrics{}, gateways: ports.NotConfigured{}, gatewayTO: 15 * time.Second, reconcileAge: 10 * time.Minute}
}

func (s *Service) WithEvents(e ports.Events) *Service {
	if e != nil {
		s.events = e
	}
	return s
}

func (s *Service) WithLinkCounter(l ports.LinkCounter) *Service { s.links = l; return s }

// WithCatalog gives the flow engine's facade the company's catalogs (a flow names a category by its code).
func (s *Service) WithCatalog(c ports.CatalogRepository) *Service { s.catalog = c; return s }

func (s *Service) WithReopenWindow(d time.Duration) *Service {
	if d > 0 {
		s.reopenWindow = d
	}
	return s
}

func (s *Service) WithMetrics(m Metrics) *Service {
	if m != nil {
		s.metrics = m
	}
	return s
}

// caller is who is acting: a person (authorized by permission) or the system (a flow, or the finalization that was already authorized by its own rules).
type caller struct {
	tc     *tenancydomain.TenantContext
	system bool
	// onBehalf names the person a system session acts for (a Hub agent finalizing through the Hub's write path); nil otherwise.
	onBehalf *uuid.UUID
	// kind overrides the actor kind of the events a system session writes (an inbound ERP update is "integration", a flow is "flow").
	kind string
}

func (c caller) actorID() *uuid.UUID {
	if c.onBehalf != nil {
		return c.onBehalf
	}
	if c.system || c.tc.ActorID == uuid.Nil {
		return nil
	}
	id := c.tc.ActorID
	return &id
}

func (c caller) actorKind() string {
	if c.kind != "" && c.actorID() == nil {
		return c.kind
	}
	if c.actorID() == nil {
		return domain.ActorSystem
	}
	return domain.ActorUser
}

func (s *Service) who(ctx context.Context) (caller, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return caller{}, errors.New("servicectx: tenant context required")
	}
	if tc.Source == tenancydomain.AccessSourceSystem {
		c := caller{tc: tc, system: true}
		if ctx.Value(flowActorKey{}) != nil { // the flow engine's facade (flow.go) marks the context; a system session of anything else keeps its own kind
			c.kind = domain.ActorFlow
		}
		return c, nil
	}
	if tc.ActorID == uuid.Nil {
		return caller{}, errors.New("servicectx: authenticated tenant context required")
	}
	return caller{tc: tc}, nil
}

// allow asks for the key of the operation. The system session is the flows and the finalization hook: they are authorized by their own rules.
func (s *Service) allow(ctx context.Context, c caller, perm string) error {
	if c.system {
		return nil
	}
	ok, err := s.authz.Has(ctx, c.tc.ActorID, perm)
	if err != nil {
		return err
	}
	if !ok {
		return domain.ErrForbidden
	}
	// Every write also needs to READ the module: its tables are read by service.read, and the row guards of a write look at the rows it touches. A role with a
	// write key and no service.read could only fail halfway through, so it is refused up front (risk R-6).
	if perm != PermRead && perm != PermManageCatalog {
		if ok, err = s.authz.Has(ctx, c.tc.ActorID, PermRead); err != nil {
			return err
		} else if !ok {
			return domain.ErrForbidden
		}
	}
	return nil
}

// OpenResult says whether the call created the session or returned one that already stood. Replay is true only when an Idempotency-Key was recognised (the same
// request again); returning the conversation's active session to a request that did not repeat anything is not a replay.
type OpenResult struct {
	Session *domain.Session
	Created bool
	Replay  bool
}

// replay recognises an Idempotency-Key: it serializes the requests that carry the key, and returns the receipt an earlier request with the SAME fingerprint left
// (nil when the key is new). The same key with another request is refused.
func (s *Service) replay(ctx context.Context, scope, key, hash string) (*ports.Receipt, error) {
	if key == "" {
		return nil, nil
	}
	if err := s.repo.LockScopedIdempotency(ctx, scope, key); err != nil {
		return nil, err
	}
	rec, err := s.repo.ReceiptFor(ctx, scope, key)
	if err != nil || rec == nil {
		return nil, err
	}
	if rec.RequestHash != hash {
		return nil, domain.ErrIdempotencyMismatch
	}
	return rec, nil
}

// remember leaves the receipt of a request that carried a key, whatever its answer was.
func (s *Service) remember(ctx context.Context, scope, key, hash string, id uuid.UUID) error {
	if key == "" {
		return nil
	}
	return s.repo.SaveReceipt(ctx, scope, key, hash, id)
}

// Open starts a session, or returns the one already standing for the request: the same Idempotency-Key with the same request, or the conversation's
// active session. The conversation row is locked first (the order everywhere: conversation, then session, then counter).
func (s *Service) Open(ctx context.Context, raw domain.OpenInput) (OpenResult, error) {
	c, err := s.who(ctx)
	if err != nil {
		return OpenResult{}, err
	}
	in, err := raw.Normalize()
	if err != nil {
		return OpenResult{}, err
	}
	if err := s.allow(ctx, c, PermCreate); err != nil {
		return OpenResult{}, err
	}
	return s.open(ctx, c, in)
}

// open is Open after the caller was identified, the input normalized and the key service.create proven.
func (s *Service) open(ctx context.Context, c caller, in domain.OpenInput) (OpenResult, error) {
	hash := in.Hash()
	rec, err := s.replay(ctx, ports.ScopeSessionOpen, in.IdempotencyKey, hash)
	if err != nil {
		return OpenResult{}, err
	}
	if rec != nil && rec.SessionID != nil {
		prev, err := s.repo.GetSession(ctx, *rec.SessionID)
		if err != nil {
			return OpenResult{}, err
		}
		return OpenResult{Session: prev, Replay: true}, nil
	}
	var facts *ports.ConversationFacts
	if in.ConversationID != nil {
		if facts, err = s.repo.LockConversation(ctx, *in.ConversationID); err != nil {
			return OpenResult{}, err
		}
		if facts.ContactID == nil || facts.Kind == "internal" {
			return OpenResult{}, domain.ErrNotACustomerChat
		}
		if facts.Status == "closed" {
			return OpenResult{}, domain.ErrConversationClosed
		}
		active, err := s.repo.ActiveByConversation(ctx, *in.ConversationID)
		if err != nil {
			return OpenResult{}, err
		}
		if active != nil {
			// the key still leaves its receipt: a retry after this session closed must get THIS answer, not open another
			if err := s.remember(ctx, ports.ScopeSessionOpen, in.IdempotencyKey, hash, active.ID); err != nil {
				return OpenResult{}, err
			}
			return OpenResult{Session: active}, nil
		}
	}
	var sess *domain.Session
	err = s.repo.InSavepoint(ctx, func(ctx context.Context) (e error) {
		sess, e = s.create(ctx, c, in, facts, hash)
		return e
	})
	if errors.Is(err, domain.ErrAlreadyActive) && in.ConversationID != nil {
		// two people opened an attendance on the conversation at the same instant (a Hub delegate cannot lock the conversation): the one that committed first
		// stands, the number this call took went back with the savepoint, and this call answers with the winner
		active, aerr := s.repo.ActiveByConversation(ctx, *in.ConversationID)
		if aerr != nil {
			return OpenResult{}, aerr
		}
		if active != nil {
			if err := s.remember(ctx, ports.ScopeSessionOpen, in.IdempotencyKey, hash, active.ID); err != nil {
				return OpenResult{}, err
			}
			return OpenResult{Session: active}, nil
		}
		return OpenResult{}, domain.ErrConflict // an earlier session of the conversation ends after this one would start: try again
	}
	if err != nil {
		return OpenResult{}, err
	}
	if err := s.remember(ctx, ports.ScopeSessionOpen, in.IdempotencyKey, hash, sess.ID); err != nil {
		return OpenResult{}, err
	}
	return OpenResult{Session: sess, Created: true}, nil
}

// create validates the references, takes the protocol and inserts. The conversation (when any) is already locked by the caller.
func (s *Service) create(ctx context.Context, c caller, in domain.OpenInput, facts *ports.ConversationFacts, hash string) (*domain.Session, error) {
	if facts != nil && in.OriginChannel == "other" && facts.Channel != "" {
		if ch := channelToken(facts.Channel); ch != "" {
			in.OriginChannel = ch
		}
	}
	contact := in.ContactID
	if facts != nil {
		contact = facts.ContactID
	}
	if facts == nil { // a contact that comes from the conversation row is valid by its foreign key; only one the caller names has to be proven to exist
		if err := s.mustExist(ctx, contact, s.repo.ContactExists, "contact_id"); err != nil {
			return nil, err
		}
	}
	if err := s.mustExist(ctx, in.CustomerID, s.repo.CustomerExists, "customer_id"); err != nil {
		return nil, err
	}
	if err := s.mustExist(ctx, in.QueueID, s.repo.QueueExists, "queue_id"); err != nil {
		return nil, err
	}
	if err := s.mustExist(ctx, in.CategoryID, s.repo.ActiveCategoryExists, "category_id"); err != nil {
		return nil, err
	}
	prefix, seq, err := s.repo.NextProtocol(ctx, "session")
	if err != nil {
		return nil, err
	}
	now, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	sess := &domain.Session{
		ID: uuid.New(), TenantID: c.tc.TenantID, Protocol: domain.FormatProtocol(prefix, seq), ProtocolSequence: seq,
		ConversationID: in.ConversationID, ContactID: contact, CustomerID: in.CustomerID, QueueID: in.QueueID, CategoryID: in.CategoryID,
		Status: domain.SessionOpen, OriginChannel: in.OriginChannel, OpenedSource: in.Source, OpenedBy: c.actorID(), OpenedAt: now,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if in.ExternalProtocol != "" {
		ep := in.ExternalProtocol
		sess.ExternalProtocol = &ep
	}
	if err := s.repo.InsertSession(ctx, sess); err != nil {
		return nil, err
	}
	if err := s.repo.OpenEpisode(ctx, sess.ID, sess.OpenedAt); err != nil {
		return nil, err
	}
	if err := s.emit(ctx, c, domain.EvSessionOpened, sess, now, map[string]any{"source": in.Source, "channel": sess.OriginChannel, "has_conversation": sess.ConversationID != nil}, ""); err != nil {
		return nil, err
	}
	s.record(ctx, c, ActionSessionOpened, ResourceSession, sess.ID, map[string]any{"source": in.Source, "protocol": sess.Protocol})
	s.metrics.Inc("service_sessions_opened_total", in.Source)
	return sess, nil
}

func (s *Service) mustExist(ctx context.Context, id *uuid.UUID, check func(context.Context, uuid.UUID) (bool, error), field string) error {
	if id == nil {
		return nil
	}
	ok, err := check(ctx, *id)
	if err != nil {
		return err
	}
	if !ok {
		return domain.Invalid("%s does not exist", field)
	}
	return nil
}

// channelToken turns a provider name into the closed token shape of origin_channel; anything odd becomes "".
func channelToken(p string) string {
	out := make([]rune, 0, len(p))
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+32)
		case r == '-' || r == ' ' || r == '.':
			out = append(out, '_')
		}
	}
	if len(out) == 0 || len(out) > 40 {
		return ""
	}
	return string(out)
}

// record writes the audit row. A system session acting on behalf of a person (a Hub agent finalizing through the Hub's write path) has no actor of its own, so the
// person is named in the metadata: the trail never loses who did it.
func (s *Service) record(ctx context.Context, c caller, action, resource string, id uuid.UUID, meta map[string]any) {
	if meta == nil {
		meta = map[string]any{}
	}
	if c.onBehalf != nil {
		meta["acting_user_id"] = c.onBehalf.String()
	}
	s.audit.Record(ctx, action, resource, id, meta)
}

// emit records the event on the timeline in the same transaction as the change.
func (s *Service) emit(ctx context.Context, c caller, typ string, sess *domain.Session, at time.Time, payload map[string]any, note string) error {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["protocol"] = sess.Protocol
	ev := domain.ContextEvent{
		ID: uuid.New(), TenantID: sess.TenantID, EventType: typ, EntityType: "service_session", EntityID: sess.ID,
		SessionID: &sess.ID, ConversationID: sess.ConversationID, ContactID: sess.ContactID, CustomerID: sess.CustomerID,
		ActorType: c.actorKind(), ActorID: c.actorID(), SourceSystem: "omnira", OccurredAt: at, RecordedAt: at,
		SchemaVersion: domain.SchemaVersion, Payload: payload, Note: note,
	}
	if tid, err := uuid.Parse(c.tc.CorrelationID); err == nil {
		ev.CorrelationID = &tid
	}
	return s.events.Record(ctx, ev)
}

// lockedSession reads the session with the lock, taking the conversation lock FIRST so the order is the same as in Open and Finalize (no deadlock).
func (s *Service) lockedSession(ctx context.Context, id uuid.UUID) (*domain.Session, *ports.ConversationFacts, error) {
	peek, err := s.repo.GetSession(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	var facts *ports.ConversationFacts
	if peek.ConversationID != nil {
		if facts, err = s.repo.LockConversation(ctx, *peek.ConversationID); err != nil {
			return nil, nil, err
		}
	}
	sess, err := s.repo.LockSession(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return sess, facts, nil
}

func checkVersion(sess *domain.Session, expected int) error {
	if expected <= 0 {
		return domain.ErrPreconditionNeeded
	}
	if sess.Version != expected {
		return domain.ErrConflict
	}
	return nil
}

// Get returns a session and, when it belongs to a conversation, who holds that conversation now (the responsible person is the conversation's, not copied).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (*domain.Session, *uuid.UUID, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, nil, err
	}
	sess, err := s.repo.GetSession(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return s.withAssignee(ctx, sess)
}

func (s *Service) withAssignee(ctx context.Context, sess *domain.Session) (*domain.Session, *uuid.UUID, error) {
	if sess.ConversationID == nil {
		return sess, nil, nil
	}
	f, err := s.repo.ReadConversation(ctx, *sess.ConversationID)
	if errors.Is(err, domain.ErrNotFound) {
		return sess, nil, nil // the conversation is not visible to this caller: the session still is
	}
	if err != nil {
		return nil, nil, err
	}
	return sess, f.AssignedTo, nil
}

// ForConversation returns the active session of a conversation, or the latest one when none is active (nil when it never had one).
func (s *Service) ForConversation(ctx context.Context, conversationID uuid.UUID) (*domain.Session, *uuid.UUID, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, nil, err
	}
	if _, err := s.repo.ReadConversation(ctx, conversationID); err != nil {
		return nil, nil, err
	}
	sess, err := s.repo.ActiveByConversation(ctx, conversationID)
	if err != nil {
		return nil, nil, err
	}
	if sess == nil {
		if sess, err = s.repo.LatestByConversation(ctx, conversationID); err != nil {
			return nil, nil, err
		}
	}
	if sess == nil {
		return nil, nil, nil
	}
	return s.withAssignee(ctx, sess)
}

func (s *Service) List(ctx context.Context, f ports.SessionFilter) ([]domain.Session, string, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, "", err
	}
	if f.Status != "" && !f.Status.Valid() {
		return nil, "", domain.Invalid("status")
	}
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	return s.repo.ListSessions(ctx, f)
}

// Messages lists the messages that belong to the session (those of its conversation inside its interval), by reference.
func (s *Service) Messages(ctx context.Context, id uuid.UUID, cursor string, limit int) ([]ports.MessageRef, string, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := s.allow(ctx, c, PermRead); err != nil {
		return nil, "", err
	}
	sess, err := s.repo.GetSession(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.repo.SessionMessages(ctx, sess, cursor, limit)
}

// Patch classifies a session: who the customer is, which department, which category. Only an active session can be edited.
func (s *Service) Patch(ctx context.Context, id uuid.UUID, in domain.PatchInput, expectedVersion int) (*domain.Session, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermUpdate); err != nil {
		return nil, err
	}
	if in.Empty() {
		return nil, domain.Invalid("nothing to change")
	}
	sess, _, err := s.lockedSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(sess, expectedVersion); err != nil {
		return nil, err
	}
	if !sess.Status.Active() {
		return nil, domain.ErrInvalidTransition
	}
	if err := s.mustExist(ctx, in.CustomerID, s.repo.CustomerExists, "customer_id"); err != nil {
		return nil, err
	}
	if err := s.mustExist(ctx, in.QueueID, s.repo.QueueExists, "queue_id"); err != nil {
		return nil, err
	}
	if err := s.mustExist(ctx, in.CategoryID, s.repo.ActiveCategoryExists, "category_id"); err != nil {
		return nil, err
	}
	changed := map[string]any{}
	if in.CustomerID != nil {
		sess.CustomerID = in.CustomerID
		changed["customer_id"] = in.CustomerID.String()
	}
	if in.QueueID != nil {
		sess.QueueID = in.QueueID
		changed["queue_id"] = in.QueueID.String()
	}
	if in.CategoryID != nil {
		sess.CategoryID = in.CategoryID
		changed["category_id"] = in.CategoryID.String()
	}
	if err := s.repo.UpdateSession(ctx, sess, expectedVersion); err != nil {
		return nil, err
	}
	at, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.emit(ctx, c, domain.EvSessionClassified, sess, at, changed, ""); err != nil {
		return nil, err
	}
	s.record(ctx, c, ActionSessionClassified, ResourceSession, sess.ID, map[string]any{"fields": len(changed)})
	return sess, nil
}

// SetStatus moves an active session between open, in progress and waiting. Closing and reopening have their own operations (they ask for other keys).
func (s *Service) SetStatus(ctx context.Context, id uuid.UUID, to domain.SessionStatus, expectedVersion int) (*domain.Session, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermUpdate); err != nil {
		return nil, err
	}
	if to != domain.SessionInProgress && to != domain.SessionWaiting {
		return nil, domain.Invalid("status must be in_progress or waiting; use close, abandon or reopen")
	}
	sess, _, err := s.lockedSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(sess, expectedVersion); err != nil {
		return nil, err
	}
	if !sess.Status.Active() || !domain.CanTransition(sess.Status, to) {
		return nil, domain.ErrInvalidTransition
	}
	from := sess.Status
	sess.Status = to
	if err := s.repo.UpdateSession(ctx, sess, expectedVersion); err != nil {
		return nil, err
	}
	at, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.emit(ctx, c, domain.EvSessionStatus, sess, at, map[string]any{"from": string(from), "to": string(to)}, ""); err != nil {
		return nil, err
	}
	s.record(ctx, c, ActionSessionStatus, ResourceSession, sess.ID, map[string]any{"from": string(from), "to": string(to)})
	return sess, nil
}

// Close ends an active session with a result. The result is the PRIMARY classification; tasks, cases and service orders are links, not results.
func (s *Service) Close(ctx context.Context, id uuid.UUID, raw domain.CloseInput, expectedVersion int) (*domain.Session, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	in, err := raw.Normalize()
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermClose); err != nil {
		return nil, err
	}
	sess, _, err := s.lockedSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(sess, expectedVersion); err != nil {
		return nil, err
	}
	return s.finish(ctx, c, sess, in, domain.SourceOperator, expectedVersion)
}

// finish writes the closing of an already locked, version-checked session.
func (s *Service) finish(ctx context.Context, c caller, sess *domain.Session, in domain.CloseInput, source string, expectedVersion int) (*domain.Session, error) {
	if !sess.Status.Active() {
		return nil, domain.ErrInvalidTransition
	}
	canonical, err := s.repo.ResolveResult(ctx, in.ResolutionCode)
	if err != nil {
		return nil, err
	}
	to := domain.SessionClosed
	if in.Abandon {
		to = domain.SessionAbandoned
		if canonical != domain.ResAbandoned {
			return nil, domain.Invalid("an abandoned session has the result 'abandoned'")
		}
	} else if canonical == domain.ResAbandoned {
		return nil, domain.Invalid("use abandon to end a session as abandoned")
	}
	at, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	if sess.FirstResponseAt == nil {
		if sess.FirstResponseAt, err = s.repo.FirstHumanReplyAt(ctx, sess); err != nil {
			return nil, err
		}
	}
	from := sess.Status
	code := in.ResolutionCode
	sess.Status, sess.ResolutionCode, sess.ResolutionCanonical, sess.ResolutionNote = to, &code, &canonical, in.Note
	sess.ClosedAt, sess.ClosedBy = &at, c.actorID()
	src := source
	sess.ClosedSource = &src
	if err := s.repo.UpdateSession(ctx, sess, expectedVersion); err != nil {
		return nil, err
	}
	if err := s.repo.EndEpisode(ctx, sess.ID, at); err != nil {
		return nil, err
	}
	payload := map[string]any{"from": string(from), "to": string(to), "result": canonical, "source": source, "has_first_response": sess.FirstResponseAt != nil}
	if code != canonical {
		payload["result_code"] = code
	}
	if err := s.emit(ctx, c, domain.EvSessionClosed, sess, at, payload, ""); err != nil {
		return nil, err
	}
	s.record(ctx, c, ActionSessionClosed, ResourceSession, sess.ID, map[string]any{"result": canonical, "to": string(to), "source": source})
	s.metrics.Inc("service_sessions_closed_total", canonical)
	return sess, nil
}

// Reopen puts a closed or abandoned session back in progress. It is a narrow exception to "closed is final" and only holds when ALL of these do:
// the conversation is still open (ADR-0020: a closed conversation is final), it is the conversation's latest session, no other one is active, and the
// window since closing has not passed. The protocol is kept; the earlier result stays in the history event.
func (s *Service) Reopen(ctx context.Context, id uuid.UUID, raw domain.ReopenInput, expectedVersion int) (*domain.Session, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, err
	}
	in, err := raw.Normalize()
	if err != nil {
		return nil, err
	}
	if err := s.allow(ctx, c, PermClose); err != nil {
		return nil, err
	}
	sess, facts, err := s.lockedSession(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(sess, expectedVersion); err != nil {
		return nil, err
	}
	if !sess.Status.Ended() {
		return nil, domain.ErrInvalidTransition
	}
	if facts != nil {
		if facts.Status != "open" {
			return nil, domain.ErrReopenNotAllowed
		}
		active, err := s.repo.ActiveByConversation(ctx, *sess.ConversationID)
		if err != nil {
			return nil, err
		}
		if active != nil {
			return nil, domain.ErrReopenNotAllowed
		}
		later, err := s.repo.HasLaterSession(ctx, sess)
		if err != nil {
			return nil, err
		}
		if later {
			return nil, domain.ErrReopenNotAllowed
		}
	}
	at, err := s.repo.Now(ctx)
	if err != nil {
		return nil, err
	}
	if sess.ClosedAt != nil && at.Sub(*sess.ClosedAt) > s.reopenWindow {
		return nil, domain.ErrReopenNotAllowed
	}
	prev := ""
	if sess.ResolutionCanonical != nil {
		prev = *sess.ResolutionCanonical
	}
	sess.Status = domain.SessionInProgress
	sess.ResolutionCode, sess.ResolutionCanonical, sess.ResolutionNote = nil, nil, ""
	sess.ClosedAt, sess.ClosedBy, sess.ClosedSource = nil, nil, nil
	sess.ReopenCount++
	reason := in.Reason
	sess.LastReopenReason = &reason
	if err := s.repo.UpdateSession(ctx, sess, expectedVersion); err != nil {
		return nil, err
	}
	if err := s.repo.OpenEpisode(ctx, sess.ID, at); err != nil {
		return nil, err
	}
	if err := s.emit(ctx, c, domain.EvSessionReopened, sess, at, map[string]any{"previous_result": prev, "reopen_count": sess.ReopenCount}, in.Reason); err != nil {
		return nil, err
	}
	s.record(ctx, c, ActionSessionReopened, ResourceSession, sess.ID, map[string]any{"reopen_count": sess.ReopenCount})
	s.metrics.Inc("service_sessions_reopened_total")
	return sess, nil
}

// OpenOnClaim is the hook of "assuming" a conversation (OMNIRA_SERVICE_AUTO_OPEN_ON_CLAIM_ENABLED): the person who took the conversation gets its protocol without
// a click. It returns the session (nil when the person may not open one, which is not an error: taking a conversation never depends on this module), and says
// whether it created it. A session that already stands is returned as it is. In a member's context the person needs service.create; in a system session (the Hub
// write path, which already proved the delegated key) the person is named as the one acting.
func (s *Service) OpenOnClaim(ctx context.Context, conversationID, actor uuid.UUID) (*domain.Session, bool, error) {
	c, err := s.who(ctx)
	if err != nil {
		return nil, false, err
	}
	if c.system && actor != uuid.Nil {
		c.onBehalf = &actor
	}
	in, err := domain.OpenInput{ConversationID: &conversationID, Source: domain.SourceOperator}.Normalize()
	if err != nil {
		return nil, false, err
	}
	if err := s.allow(ctx, c, PermCreate); err != nil {
		if errors.Is(err, domain.ErrForbidden) {
			log.Printf("service-context: no protocol on claim of conversation %s: the person lacks service.create here", conversationID)
			return nil, false, nil
		}
		return nil, false, err
	}
	res, err := s.open(ctx, c, in)
	if errors.Is(err, domain.ErrConversationClosed) || errors.Is(err, domain.ErrNotACustomerChat) {
		log.Printf("service-context: no protocol on claim of conversation %s: %v", conversationID, err)
		return nil, false, nil // nothing to attend: not a customer chat, or already ended
	}
	if err != nil {
		return nil, false, err
	}
	return res.Session, res.Created, nil
}

// CloseForFinalize is called by the attendance finalization (ADR-0020), in ITS transaction and after it locked the conversation: the session of the
// conversation is closed with the result that matches the finalization, or created and closed in one go when the attendance never had one (an
// informational attendance gets its protocol when it ends); a conversation whose session the operator already closed gets nothing more. It does not ask for permissions: finalizing was authorized by its own rules. Idempotent.
func (s *Service) CloseForFinalize(ctx context.Context, conversationID uuid.UUID, reason string, source string, actor uuid.UUID) error {
	c, err := s.who(ctx)
	if err != nil {
		return err
	}
	if actor != uuid.Nil {
		c.onBehalf = &actor
	}
	facts, err := s.repo.LockConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	if facts.ContactID == nil || facts.Kind == "internal" {
		return nil
	}
	sess, err := s.repo.ActiveByConversation(ctx, conversationID)
	if err != nil {
		return err
	}
	if sess == nil {
		// The attendance already had its protocol and the operator closed it by hand before finalizing: that protocol is the record of this
		// attendance, so finalizing opens no second (empty, instantly closed) one that would only burn a number.
		if prev, err := s.repo.LatestByConversation(ctx, conversationID); err != nil {
			return err
		} else if prev != nil {
			return nil
		}
		in := domain.OpenInput{ConversationID: &conversationID, Source: source, OriginChannel: "other"}
		if in, err = in.Normalize(); err != nil {
			return err
		}
		if sess, err = s.create(ctx, c, in, facts, ""); err != nil {
			return err
		}
	} else if sess, err = s.repo.LockSession(ctx, sess.ID); err != nil {
		return err
	}
	to, code := domain.ResolutionFromFinalize(reason)
	if s.links != nil {
		o, cs, ts, err := s.links.CountLinks(ctx, sess.ID)
		if err != nil {
			return err
		}
		code = domain.RefineResolution(code, o, cs, ts)
	}
	_, err = s.finish(ctx, c, sess, domain.CloseInput{ResolutionCode: code, Abandon: to == domain.SessionAbandoned}, source, sess.Version)
	return err
}
