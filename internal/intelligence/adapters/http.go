package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/intelligence/application"
	"github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/intelligence/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/pagination"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

const (
	permTopicRead          = "topic.read"
	permTopicManage        = "topic.manage"
	permConversationManage = "conversation.manage"
	maxBody                = 16 << 10
	maxMessageIDsOnCreate  = 100
)

// TopicHandler serves the topic API. Reading needs topic.read; changing needs topic.manage AND being the attendant of
// the conversation (or conversation.manage), the same rule that governs sending messages. The tenant is always the
// session's; an id from another tenant answers 404.
type TopicHandler struct {
	pool        *pgxpool.Pool
	svc         *application.TopicService
	repo        ports.TopicRepository
	routing     *application.RoutingService
	routingRepo ports.RoutingRepository
	summaries   *application.SummaryService
}

// WithSummaries enables the topic summary endpoints.
func (h *TopicHandler) WithSummaries(s *application.SummaryService) *TopicHandler {
	h.summaries = s
	return h
}

// WithRouting enables the ambiguity endpoints (a person resolves what the router could not place).
func (h *TopicHandler) WithRouting(svc *application.RoutingService, repo ports.RoutingRepository) *TopicHandler {
	h.routing, h.routingRepo = svc, repo
	return h
}

func NewTopicHandler(pool *pgxpool.Pool, svc *application.TopicService, repo ports.TopicRepository) *TopicHandler {
	return &TopicHandler{pool: pool, svc: svc, repo: repo}
}

var errForbidden = errors.New("forbidden")

func (h *TopicHandler) has(ctx context.Context, tc *tenancydomain.TenantContext, permission string) (bool, error) {
	var ok bool
	err := platformdb.QuerierFromContext(ctx, h.pool).QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`,
		tc.TenantID, tc.ActorID, permission).Scan(&ok)
	return ok, err
}

// authorize returns the trusted tenant context when the actor holds the permission.
func (h *TopicHandler) authorize(r *http.Request, permission string) (*tenancydomain.TenantContext, error) {
	tc, err := tenancydomain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		return nil, errors.New("tenant context not found")
	}
	ok, err := h.has(r.Context(), tc, permission)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errForbidden
	}
	return tc, nil
}

func (h *TopicHandler) canOperateConversation(ctx context.Context, tc *tenancydomain.TenantContext, assignee *uuid.UUID) (bool, error) {
	if assignee != nil && *assignee == tc.ActorID {
		return true, nil
	}
	return h.has(ctx, tc, permConversationManage)
}

func (h *TopicHandler) canOperateTopic(ctx context.Context, tc *tenancydomain.TenantContext, topicID uuid.UUID) (bool, error) {
	if ok, err := h.has(ctx, tc, permConversationManage); err != nil || ok {
		return ok, err
	}
	return h.repo.ActorOperatesTopic(ctx, tc.TenantID, topicID, tc.ActorID)
}

func fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errForbidden):
		http.Error(w, "forbidden", http.StatusForbidden)
	case errors.Is(err, domain.ErrTopicNotFound), errors.Is(err, domain.ErrReferenceNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, domain.ErrInvalidTopic):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	case errors.Is(err, domain.ErrInvalidTransition), errors.Is(err, domain.ErrPrimaryTicketTaken):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, ports.ErrSummarizerUnavailable):
		http.Error(w, "summaries are unavailable right now", http.StatusServiceUnavailable)
	case errors.Is(err, application.ErrNothingToSummarize):
		http.Error(w, "this topic has no messages to summarize yet", http.StatusUnprocessableEntity)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return false
	}
	return true
}

func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil || id == uuid.Nil {
		http.Error(w, "invalid "+name, http.StatusBadRequest)
		return uuid.Nil, false
	}
	return id, true
}

// --- DTOs ---

type topicDTO struct {
	ID                   uuid.UUID  `json:"id"`
	Title                string     `json:"title"`
	Intent               *string    `json:"intent,omitempty"`
	Category             *string    `json:"category,omitempty"`
	Status               string     `json:"status"`
	PrivacyPolicy        string     `json:"privacy_policy"`
	Source               string     `json:"source"`
	RoutingConfidence    *float64   `json:"routing_confidence,omitempty"`
	LegacyUnsegmented    bool       `json:"legacy_unsegmented"`
	PrimaryContactID     *uuid.UUID `json:"primary_contact_id,omitempty"`
	OriginConversationID *uuid.UUID `json:"origin_conversation_id,omitempty"`
	LastActivityAt       string     `json:"last_activity_at"`
	CreatedAt            string     `json:"created_at"`
	ResolvedAt           *string    `json:"resolved_at,omitempty"`
	MessageCount         *int       `json:"message_count,omitempty"`
	TicketCount          *int       `json:"ticket_count,omitempty"`
	LastMessageAt        *string    `json:"last_message_at,omitempty"`
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func tsp(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}

func toTopicDTO(t *domain.TopicThread) topicDTO {
	return topicDTO{
		ID: t.ID, Title: t.Title, Intent: t.Intent, Category: t.Category, Status: string(t.Status),
		PrivacyPolicy: string(t.PrivacyPolicy), Source: string(t.Source), RoutingConfidence: t.RoutingConfidence,
		LegacyUnsegmented: t.LegacyUnsegmented, PrimaryContactID: t.PrimaryContactID, OriginConversationID: t.OriginConversationID,
		LastActivityAt: ts(t.LastActivityAt), CreatedAt: ts(t.CreatedAt), ResolvedAt: tsp(t.ResolvedAt),
	}
}

func toListDTO(items []ports.TopicListItem) []topicDTO {
	out := make([]topicDTO, 0, len(items))
	for i := range items {
		d := toTopicDTO(&items[i].Topic)
		mc, tc := items[i].MessageCount, items[i].TicketCount
		d.MessageCount, d.TicketCount, d.LastMessageAt = &mc, &tc, tsp(items[i].LastMessageAt)
		out = append(out, d)
	}
	return out
}

// --- handlers ---

// ListConversationTopics: GET /tenants/{tenant_id}/inbox/conversations/{conversation_id}/topics
func (h *TopicHandler) ListConversationTopics(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permTopicRead)
	if err != nil {
		fail(w, err)
		return
	}
	convID, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	info, err := h.repo.ConversationInfo(r.Context(), tc.TenantID, convID)
	if err != nil || !info.Exists {
		fail(w, firstErr(err, domain.ErrReferenceNotFound))
		return
	}
	items, err := h.svc.ListConversationTopics(r.Context(), convID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": toListDTO(items)})
}

func firstErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}

type createTopicRequest struct {
	Title         string   `json:"title"`
	Intent        *string  `json:"intent"`
	Category      *string  `json:"category"`
	PrivacyPolicy *string  `json:"privacy_policy"`
	MessageIDs    []string `json:"message_ids"`
}

// CreateConversationTopic: POST /tenants/{tenant_id}/inbox/conversations/{conversation_id}/topics
func (h *TopicHandler) CreateConversationTopic(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permTopicManage)
	if err != nil {
		fail(w, err)
		return
	}
	convID, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	var req createTopicRequest
	if !decode(w, r, &req) {
		return
	}
	if len(req.MessageIDs) > maxMessageIDsOnCreate {
		http.Error(w, "too many message_ids", http.StatusUnprocessableEntity)
		return
	}
	info, err := h.repo.ConversationInfo(r.Context(), tc.TenantID, convID)
	if err != nil || !info.Exists {
		fail(w, firstErr(err, domain.ErrReferenceNotFound))
		return
	}
	if ok, err := h.canOperateConversation(r.Context(), tc, info.AssignedTo); err != nil || !ok {
		fail(w, firstErr(err, errForbidden))
		return
	}
	in := application.CreateTopicInput{ConversationID: convID, ContactID: &info.ContactID, Title: req.Title, Intent: req.Intent, Category: req.Category}
	if req.PrivacyPolicy != nil {
		in.Privacy = domain.PrivacyPolicy(*req.PrivacyPolicy)
	}
	for _, raw := range req.MessageIDs {
		id, err := uuid.Parse(raw)
		if err != nil || id == uuid.Nil {
			http.Error(w, "invalid message_ids", http.StatusBadRequest)
			return
		}
		in.MessageIDs = append(in.MessageIDs, id)
	}
	topic, err := h.svc.CreateTopic(r.Context(), in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTopicDTO(topic))
}

// GetTopic: GET /tenants/{tenant_id}/topics/{topic_id}
func (h *TopicHandler) GetTopic(w http.ResponseWriter, r *http.Request) {
	if _, err := h.authorize(r, permTopicRead); err != nil {
		fail(w, err)
		return
	}
	id, ok := pathUUID(w, r, "topic_id")
	if !ok {
		return
	}
	topic, err := h.svc.GetTopic(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTopicDTO(topic))
}

type patchTopicRequest struct {
	Title         *string `json:"title"`
	Intent        *string `json:"intent"`
	Category      *string `json:"category"`
	PrivacyPolicy *string `json:"privacy_policy"`
	Status        *string `json:"status"`
}

// PatchTopic: PATCH /tenants/{tenant_id}/topics/{topic_id}
func (h *TopicHandler) PatchTopic(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permTopicManage)
	if err != nil {
		fail(w, err)
		return
	}
	id, ok := pathUUID(w, r, "topic_id")
	if !ok {
		return
	}
	var req patchTopicRequest
	if !decode(w, r, &req) {
		return
	}
	if _, err := h.svc.GetTopic(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	if ok, err := h.canOperateTopic(r.Context(), tc, id); err != nil || !ok {
		fail(w, firstErr(err, errForbidden))
		return
	}
	in := application.UpdateTopicInput{Title: req.Title, Intent: req.Intent, Category: req.Category}
	if req.PrivacyPolicy != nil {
		p := domain.PrivacyPolicy(*req.PrivacyPolicy)
		in.Privacy = &p
	}
	if req.Status != nil {
		s := domain.TopicStatus(*req.Status)
		in.Status = &s
	}
	topic, err := h.svc.UpdateTopic(r.Context(), id, in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTopicDTO(topic))
}

type topicMessageDTO struct {
	ID             uuid.UUID `json:"id"`
	ConversationID uuid.UUID `json:"conversation_id"`
	Direction      string    `json:"direction"`
	MessageType    string    `json:"message_type"`
	Body           string    `json:"body,omitempty"`
	MimeType       string    `json:"mime_type,omitempty"`
	Status         string    `json:"status"`
	CreatedAt      string    `json:"created_at"`
	Relation       string    `json:"relation"`
	Confidence     *float64  `json:"confidence,omitempty"`
	DecisionSource string    `json:"decision_source"`
}

// ListTopicMessages: GET /tenants/{tenant_id}/topics/{topic_id}/messages (newest first, cursor paginated)
func (h *TopicHandler) ListTopicMessages(w http.ResponseWriter, r *http.Request) {
	if _, err := h.authorize(r, permTopicRead); err != nil {
		fail(w, err)
		return
	}
	id, ok := pathUUID(w, r, "topic_id")
	if !ok {
		return
	}
	opts := pagination.ParsePageOptionsFromQuery(r)
	if opts.Sort != "" && opts.Sort != "created_at:desc" {
		http.Error(w, "unsupported sort", http.StatusBadRequest)
		return
	}
	cursor, err := pagination.DecodeCursor(opts.Cursor)
	if err != nil {
		http.Error(w, "invalid cursor", http.StatusBadRequest)
		return
	}
	msgs, hasMore, err := h.svc.ListTopicMessages(r.Context(), id, cursor, opts.Limit)
	if err != nil {
		fail(w, err)
		return
	}
	items := make([]interface{}, 0, len(msgs))
	for _, m := range msgs {
		items = append(items, topicMessageDTO{ID: m.ID, ConversationID: m.ConversationID, Direction: m.Direction, MessageType: m.MessageType,
			Body: m.Body, MimeType: m.MimeType, Status: m.Status, CreatedAt: ts(m.CreatedAt), Relation: string(m.Relation),
			Confidence: m.Confidence, DecisionSource: string(m.DecisionSource)})
	}
	result := pagination.NewPageResult(items, opts.Limit, "", hasMore)
	if hasMore && len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		result.NextCursor = (&pagination.Cursor{ID: last.ID.String(), Timestamp: last.CreatedAt}).Encode()
	}
	pagination.WritePaginationHeaders(w, result)
	writeJSON(w, http.StatusOK, result)
}

type topicTicketDTO struct {
	ID               uuid.UUID `json:"id"`
	Status           string    `json:"status"`
	Priority         string    `json:"priority"`
	Subject          string    `json:"subject"`
	Provider         *string   `json:"provider,omitempty"`
	ExternalTicketID *string   `json:"external_ticket_id,omitempty"`
	Relation         string    `json:"relation"`
	LinkedAt         string    `json:"linked_at"`
}

// ListTopicTickets: GET /tenants/{tenant_id}/topics/{topic_id}/tickets
func (h *TopicHandler) ListTopicTickets(w http.ResponseWriter, r *http.Request) {
	if _, err := h.authorize(r, permTopicRead); err != nil {
		fail(w, err)
		return
	}
	id, ok := pathUUID(w, r, "topic_id")
	if !ok {
		return
	}
	tickets, err := h.svc.ListTopicTickets(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]topicTicketDTO, 0, len(tickets))
	for _, t := range tickets {
		out = append(out, topicTicketDTO{ID: t.ID, Status: t.Status, Priority: t.Priority, Subject: t.Subject, Provider: t.Provider,
			ExternalTicketID: t.ExternalTicketID, Relation: string(t.Relation), LinkedAt: ts(t.LinkedAt)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

type linkTicketRequest struct {
	TicketID string  `json:"ticket_id"`
	Relation *string `json:"relation"`
}

// LinkTicket: POST /tenants/{tenant_id}/topics/{topic_id}/tickets/link. Links an EXISTING ticket; never creates one.
func (h *TopicHandler) LinkTicket(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permTopicManage)
	if err != nil {
		fail(w, err)
		return
	}
	id, ok := pathUUID(w, r, "topic_id")
	if !ok {
		return
	}
	var req linkTicketRequest
	if !decode(w, r, &req) {
		return
	}
	ticketID, err := uuid.Parse(req.TicketID)
	if err != nil || ticketID == uuid.Nil {
		http.Error(w, "invalid ticket_id", http.StatusBadRequest)
		return
	}
	relation := domain.TicketRelated
	if req.Relation != nil {
		relation = domain.TicketRelation(*req.Relation)
	}
	if _, err := h.svc.GetTopic(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	if ok, err := h.canOperateTopic(r.Context(), tc, id); err != nil || !ok {
		fail(w, firstErr(err, errForbidden))
		return
	}
	if err := h.svc.LinkTicket(r.Context(), id, ticketID, relation); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type linkMessageRequest struct {
	MessageID string  `json:"message_id"`
	Relation  *string `json:"relation"`
}

// LinkMessage: POST /tenants/{tenant_id}/topics/{topic_id}/messages (an agent adds a message to a topic)
func (h *TopicHandler) LinkMessage(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permTopicManage)
	if err != nil {
		fail(w, err)
		return
	}
	id, ok := pathUUID(w, r, "topic_id")
	if !ok {
		return
	}
	var req linkMessageRequest
	if !decode(w, r, &req) {
		return
	}
	msgID, err := uuid.Parse(req.MessageID)
	if err != nil || msgID == uuid.Nil {
		http.Error(w, "invalid message_id", http.StatusBadRequest)
		return
	}
	relation := domain.RelationPrimary
	if req.Relation != nil {
		relation = domain.MessageRelation(*req.Relation)
	}
	if _, err := h.svc.GetTopic(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	if ok, err := h.canOperateTopic(r.Context(), tc, id); err != nil || !ok {
		fail(w, firstErr(err, errForbidden))
		return
	}
	if err := h.svc.LinkMessage(r.Context(), id, msgID, relation); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// UnlinkMessage: DELETE /tenants/{tenant_id}/topics/{topic_id}/messages/{message_id}
func (h *TopicHandler) UnlinkMessage(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permTopicManage)
	if err != nil {
		fail(w, err)
		return
	}
	id, ok := pathUUID(w, r, "topic_id")
	if !ok {
		return
	}
	msgID, ok := pathUUID(w, r, "message_id")
	if !ok {
		return
	}
	if _, err := h.svc.GetTopic(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	if ok, err := h.canOperateTopic(r.Context(), tc, id); err != nil || !ok {
		fail(w, firstErr(err, errForbidden))
		return
	}
	if err := h.svc.UnlinkMessage(r.Context(), id, msgID); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListContactTopics: GET /tenants/{tenant_id}/contacts/{contact_id}/topics?status=open
func (h *TopicHandler) ListContactTopics(w http.ResponseWriter, r *http.Request) {
	if _, err := h.authorize(r, permTopicRead); err != nil {
		fail(w, err)
		return
	}
	id, ok := pathUUID(w, r, "contact_id")
	if !ok {
		return
	}
	var status *domain.TopicStatus
	if v := r.URL.Query().Get("status"); v != "" {
		s := domain.TopicStatus(v)
		if !s.Valid() {
			http.Error(w, "invalid status", http.StatusBadRequest)
			return
		}
		status = &s
	}
	items, err := h.svc.ListContactTopics(r.Context(), id, status, 0)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": toListDTO(items)})
}

type ambiguityDTO struct {
	ID         uuid.UUID       `json:"id"`
	MessageID  uuid.UUID       `json:"message_id"`
	Kind       string          `json:"kind"`
	Candidates json.RawMessage `json:"candidates"`
	CreatedAt  string          `json:"created_at"`
}

func toAmbiguityDTOs(items []ports.Ambiguity) []ambiguityDTO {
	out := make([]ambiguityDTO, 0, len(items))
	for _, a := range items {
		out = append(out, ambiguityDTO{ID: a.ID, MessageID: a.Ref.ID, Kind: string(a.Ref.Kind), Candidates: a.Candidates, CreatedAt: ts(a.CreatedAt)})
	}
	return out
}

// ListConversationAmbiguities: GET /tenants/{tenant_id}/inbox/conversations/{conversation_id}/ambiguities
func (h *TopicHandler) ListConversationAmbiguities(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permTopicRead)
	if err != nil {
		fail(w, err)
		return
	}
	convID, ok := pathUUID(w, r, "conversation_id")
	if !ok {
		return
	}
	info, err := h.repo.ConversationInfo(r.Context(), tc.TenantID, convID)
	if err != nil || !info.Exists {
		fail(w, firstErr(err, domain.ErrReferenceNotFound))
		return
	}
	items, err := h.routingRepo.ListOpenAmbiguities(r.Context(), tc.TenantID, ports.KindConversation, convID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": toAmbiguityDTOs(items)})
}

type resolveAmbiguityRequest struct {
	TopicID       *string `json:"topic_id"`
	NewTopicTitle string  `json:"new_topic_title"`
}

// ResolveAmbiguity: POST /tenants/{tenant_id}/ambiguities/{ambiguity_id}/resolve. The attendant of the conversation (or
// conversation.manage) decides; for a group message the decision needs group.manage.
func (h *TopicHandler) ResolveAmbiguity(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permTopicManage)
	if err != nil {
		fail(w, err)
		return
	}
	id, ok := pathUUID(w, r, "ambiguity_id")
	if !ok {
		return
	}
	var req resolveAmbiguityRequest
	if !decode(w, r, &req) {
		return
	}
	var topicID *uuid.UUID
	if req.TopicID != nil {
		t, err := uuid.Parse(*req.TopicID)
		if err != nil || t == uuid.Nil {
			http.Error(w, "invalid topic_id", http.StatusBadRequest)
			return
		}
		topicID = &t
	}
	a, err := h.routingRepo.GetAmbiguity(r.Context(), tc.TenantID, id)
	if err != nil {
		fail(w, err)
		return
	}
	if a.Ref.Kind == ports.KindGroup {
		ok, err := h.has(r.Context(), tc, "group.manage")
		if err != nil || !ok {
			fail(w, firstErr(err, errForbidden))
			return
		}
	} else if ok, err := h.canOperateConversation(r.Context(), tc, a.AssignedTo); err != nil || !ok {
		fail(w, firstErr(err, errForbidden))
		return
	}
	chosen, err := h.routing.ResolveAmbiguity(r.Context(), id, topicID, req.NewTopicTitle, domain.DecisionAgent)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"topic_id": chosen})
}

type summaryDTO struct {
	ID            uuid.UUID  `json:"id"`
	Version       int        `json:"version"`
	Text          string     `json:"summary_text"`
	Status        string     `json:"status"`
	Model         *string    `json:"model_name,omitempty"`
	Provider      *string    `json:"model_provider,omitempty"`
	PromptVersion *string    `json:"prompt_version,omitempty"`
	AuthoredBy    string     `json:"authored_by"`
	CreatedAt     string     `json:"created_at"`
	ConfirmedAt   *string    `json:"confirmed_at,omitempty"`
	SourceSummary *uuid.UUID `json:"source_summary_id,omitempty"`
}

func toSummaryDTO(s domain.TopicSummary) summaryDTO {
	author := "machine"
	if s.Status == domain.SummaryCorrected {
		author = "agent"
	}
	return summaryDTO{ID: s.ID, Version: s.Version, Text: s.SummaryText, Status: string(s.Status), Model: s.ModelName, Provider: s.ModelProvider,
		PromptVersion: s.PromptVersion, AuthoredBy: author, CreatedAt: ts(s.CreatedAt), ConfirmedAt: tsp(s.ConfirmedAt), SourceSummary: s.SourceSummaryID}
}

func (h *TopicHandler) summariesOrFail(w http.ResponseWriter) bool {
	if h.summaries == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return false
	}
	return true
}

// ListSummaries: GET /tenants/{tenant_id}/topics/{topic_id}/summaries (newest version first; history is never rewritten)
func (h *TopicHandler) ListSummaries(w http.ResponseWriter, r *http.Request) {
	if _, err := h.authorize(r, permTopicRead); err != nil {
		fail(w, err)
		return
	}
	if !h.summariesOrFail(w) {
		return
	}
	id, ok := pathUUID(w, r, "topic_id")
	if !ok {
		return
	}
	list, err := h.summaries.List(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	out := make([]summaryDTO, 0, len(list))
	for _, s := range list {
		out = append(out, toSummaryDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *TopicHandler) summaryGate(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	tc, err := h.authorize(r, permTopicManage)
	if err != nil {
		fail(w, err)
		return uuid.Nil, false
	}
	if !h.summariesOrFail(w) {
		return uuid.Nil, false
	}
	id, ok := pathUUID(w, r, "topic_id")
	if !ok {
		return uuid.Nil, false
	}
	if _, err := h.svc.GetTopic(r.Context(), id); err != nil {
		fail(w, err)
		return uuid.Nil, false
	}
	if ok, err := h.canOperateTopic(r.Context(), tc, id); err != nil || !ok {
		fail(w, firstErr(err, errForbidden))
		return uuid.Nil, false
	}
	return id, true
}

// ConfirmSummary: POST /tenants/{tenant_id}/topics/{topic_id}/summary/confirm
func (h *TopicHandler) ConfirmSummary(w http.ResponseWriter, r *http.Request) {
	id, ok := h.summaryGate(w, r)
	if !ok {
		return
	}
	s, err := h.summaries.Confirm(r.Context(), id, domain.DecisionAgent)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSummaryDTO(*s))
}

type correctSummaryRequest struct {
	SummaryText string `json:"summary_text"`
}

// CorrectSummary: POST /tenants/{tenant_id}/topics/{topic_id}/summary/correct (creates a NEW version)
func (h *TopicHandler) CorrectSummary(w http.ResponseWriter, r *http.Request) {
	id, ok := h.summaryGate(w, r)
	if !ok {
		return
	}
	var req correctSummaryRequest
	if !decode(w, r, &req) {
		return
	}
	s, err := h.summaries.Correct(r.Context(), id, req.SummaryText)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSummaryDTO(*s))
}

// GenerateSummary: POST /tenants/{tenant_id}/topics/{topic_id}/summary/generate (machine summary of the current state;
// 200 with the existing one when nothing new happened)
func (h *TopicHandler) GenerateSummary(w http.ResponseWriter, r *http.Request) {
	id, ok := h.summaryGate(w, r)
	if !ok {
		return
	}
	s, created, err := h.summaries.Generate(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, toSummaryDTO(*s))
}
