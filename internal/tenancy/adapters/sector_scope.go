package adapters

import (
	"context"
	"net/http"

	"github.com/omnira/omnira/internal/tenancy/domain"

	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PermissionReadAllQueues — see the conversations of every queue (ADR-0043 S3, migration 000165). Administrators and supervisors hold it; agents do not. It only matters
// when the company turned the capability `sector_scope` on (entitlements, starts OFF); the key is not delegable (the Hub has its own scope, the contract's queues).
const PermissionReadAllQueues = "conversation.read_all_queues"

// SectorScopeRestricts says whether the sector scope narrows what THIS request may see: the company turned it on and the actor does not hold the key. System work (workers) is
// never restricted; a delegated context (Hub) or Hub management is never restricted by THIS scope (its own scope is the contract). The decision lives in SQL (sector_scope_restricts) so every surface asks the same thing.
func SectorScopeRestricts(ctx context.Context, q platformdb.Querier, tc *domain.TenantContext) (bool, error) {
	if tc == nil {
		return true, nil
	}
	// only a MEMBER acting directly is narrowed here: system work is not an actor, and a delegated context (the Hub) has its own scope, the contract's queues
	if tc.Source != domain.AccessSourceDirect {
		return false, nil
	}
	var restricts bool
	err := q.QueryRow(ctx, `SELECT sector_scope_restricts($1, $2)`, tc.TenantID, tc.ActorID).Scan(&restricts)
	return restricts, err
}

// RefusesOutOfSectorScope answers "is this request about a conversation (or one of its messages, tickets or topics) that the sector scope hides from the actor?". True means 404,
// exactly as for a conversation that does not exist. A request that names no conversation, message or topic is never refused here: the lists filter their own rows.
func RefusesOutOfSectorScope(ctx context.Context, q platformdb.Querier, r *http.Request, tc *domain.TenantContext) (bool, error) {
	conversationID, hasConversation := uuidPath(r, "conversation_id")
	messageID, hasMessage := uuidPath(r, "message_id")
	topicID, hasTopic := uuidPath(r, "topic_id")
	if !hasConversation && !hasMessage && !hasTopic {
		return false, nil
	}
	restricts, err := SectorScopeRestricts(ctx, q, tc)
	if err != nil || !restricts {
		return false, err
	}
	// every named resource must be inside the scope; a message or a topic is judged by the conversation(s) it belongs to
	if hasConversation {
		var ok bool
		if err := q.QueryRow(ctx, `SELECT conversation_in_sector_scope($1, $2, $3)`, tc.TenantID, tc.ActorID, conversationID).Scan(&ok); err != nil || !ok {
			return true, err
		}
	}
	if hasMessage {
		var ok bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM messages m WHERE m.tenant_id = $1 AND m.id = $3 AND conversation_in_sector_scope($1, $2, m.conversation_id))`,
			tc.TenantID, tc.ActorID, messageID).Scan(&ok); err != nil || !ok {
			return true, err
		}
	}
	if hasTopic {
		// a topic is visible when at least one of its conversations is; a topic linked to none is not shown to a restricted person (fail closed)
		var ok bool
		if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM topic_conversation_links l WHERE l.tenant_id = $1 AND l.topic_thread_id = $3 AND conversation_in_sector_scope($1, $2, l.conversation_id))`,
			tc.TenantID, tc.ActorID, topicID).Scan(&ok); err != nil || !ok {
			return true, err
		}
	}
	return false, nil
}
