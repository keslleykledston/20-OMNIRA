package adapters

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tenancy/domain"

	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PermissionReadInternal — read and operate the conversations of contacts an operator declared internal (ADR-0018 addendum, migration 000120).
// Administrators and supervisors hold it; agents do not; a delegated (Hub) context never does (the key is not delegable).
const PermissionReadInternal = "conversation.read_internal"

// CanReadInternalConversations says whether this request may see conversations of internal contacts. System work (workers) is not an
// actor and is not restricted here; every actor, member or delegated, is asked of the database in the same transaction.
func CanReadInternalConversations(ctx context.Context, q platformdb.Querier, tc *domain.TenantContext) (bool, error) {
	if tc == nil {
		return false, nil
	}
	if tc.Source == domain.AccessSourceSystem {
		return true, nil
	}
	return ActorHasPermission(ctx, q, tc.TenantID, tc.ActorID, PermissionReadInternal)
}

// internalContactSuffixes are the contact sub-resources that show conversations (history, tickets, topics). The contact itself, its
// classification and its notes stay reachable: whoever cannot read the conversations must still be able to see who the contact is and,
// with contact.classify, change the classification back.
var internalContactSuffixes = []string{"/conversations", "/tickets", "/topics"}

// RefusesInternalConversation answers "is this request about a conversation (or a message, or the conversation list of a contact) of an
// internal contact that the actor may not read?". True means: answer 404, exactly as for a conversation that does not exist, so the
// existence of an internal conversation is not revealed. It runs in the request's tenant session, after the tenant context is known.
// A request that names no conversation, message or contact sub-resource is never refused here.
func RefusesInternalConversation(ctx context.Context, q platformdb.Querier, r *http.Request, tc *domain.TenantContext) (bool, error) {
	conversationID, hasConversation := uuidPath(r, "conversation_id")
	messageID, hasMessage := uuidPath(r, "message_id")
	contactID, hasContact := uuidPath(r, "contact_id")
	if hasContact && !hasConversation && !hasMessage {
		hasContact = false
		for _, suffix := range internalContactSuffixes {
			if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/contacts/"+contactID.String()+suffix) {
				hasContact = true
				break
			}
		}
	}
	if !hasConversation && !hasMessage && !hasContact {
		return false, nil
	}
	var internal bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM contacts co
		  WHERE co.tenant_id = $1 AND co.kind = 'internal' AND co.id IN (
		    SELECT $4::uuid
		    UNION ALL
		    SELECT cv.contact_id FROM conversations cv WHERE cv.tenant_id = $1 AND cv.id = $2::uuid
		    UNION ALL
		    SELECT cv.contact_id FROM messages m
		      JOIN conversations cv ON cv.tenant_id = m.tenant_id AND cv.id = m.conversation_id
		     WHERE m.tenant_id = $1 AND m.id = $3::uuid))`,
		tc.TenantID, nullable(conversationID, hasConversation), nullable(messageID, hasMessage), nullable(contactID, hasContact)).Scan(&internal)
	if err != nil || !internal {
		return false, err
	}
	allowed, err := CanReadInternalConversations(ctx, q, tc)
	return !allowed, err
}

func uuidPath(r *http.Request, name string) (uuid.UUID, bool) {
	raw := r.PathValue(name)
	if raw == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, false // a malformed id is the handler's 400/404, not ours
	}
	return id, true
}

func nullable(id uuid.UUID, ok bool) any {
	if !ok {
		return nil
	}
	return id
}

// WriteInternalRefusal answers a refused request: 404, with the text the main read route of that resource gives for a missing one
// ("conversation not found", "media not found", "contact not found"), so neither the status nor, on those routes, the body tells a hidden
// conversation from a missing one. Other routes keep their own wording for a missing resource; the status is the same everywhere.
func WriteInternalRefusal(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.PathValue("conversation_id") != "":
		http.Error(w, "conversation not found", http.StatusNotFound)
	case r.PathValue("message_id") != "":
		http.Error(w, "media not found", http.StatusNotFound)
	default:
		http.Error(w, "contact not found", http.StatusNotFound)
	}
}
