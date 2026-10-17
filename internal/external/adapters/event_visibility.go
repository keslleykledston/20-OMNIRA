package adapters

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// LoadAllowlistsFor is LoadAllowlists for a caller that knows the tenant and the installation but holds no request (the delivery worker).
func LoadAllowlistsFor(ctx context.Context, q platformdb.Querier, tenantID, installationID uuid.UUID) (Allowlists, error) {
	var qs, cs []string
	if err := q.QueryRow(ctx, `SELECT queue_ids, channel_ids FROM external_installation_allowlists($1, $2)`, tenantID, installationID).Scan(&qs, &cs); err != nil {
		if errors.Is(err, pgx.ErrNoRows) { // the installation or the company stopped being usable
			return Allowlists{}, ErrInstallationInactive
		}
		return Allowlists{}, err
	}
	return Allowlists{QueueIDs: parseUUIDs(qs), ChannelIDs: parseUUIDs(cs)}, nil
}

// EventVisible is the rule of the journal's visibility, asked for ONE fact: a fact about a conversation (or one of its messages, tickets) is visible when the
// conversation is visible to the installation; a fact about a contact is visible when the contact is neither internal nor spam and has a visible conversation.
// The events API and the webhook worker both use it, so a fact is never delivered by one door and refused by the other.
func EventVisible(ctx context.Context, q platformdb.Querier, v Visibility, aggregateType string, aggregateID uuid.UUID, conversationID *uuid.UUID) (bool, error) {
	var args Args
	var ok bool
	if IsServiceAggregate(aggregateType) {
		// ADR-0049 gate 4: the service facts need the scope; one tied to a conversation follows that conversation's visibility (queues and lines included); one with
		// no conversation (a case, a task, an ERP record) cannot be placed in a queue, so only an installation with NO limit hears about it.
		if !v.ServiceRead {
			return false, nil
		}
		if conversationID == nil {
			return !v.Restricted(), nil
		}
	}
	switch {
	case conversationID != nil:
		vis := v.Conversation("c", &args)
		err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM conversations c WHERE `+vis+` AND c.id = `+args.Add(*conversationID)+`)`, args.Values()...).Scan(&ok)
		return ok, err
	case aggregateType == "contact":
		vis := v.Conversation("c", &args)
		err := q.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM contacts ct JOIN conversations c ON c.contact_id = ct.id AND c.tenant_id = ct.tenant_id
			                WHERE ct.id = `+args.Add(aggregateID)+` AND ct.kind NOT IN ('internal', 'spam') AND `+vis+`)`, args.Values()...).Scan(&ok)
		return ok, err
	}
	return false, nil
}

// ServiceAggregates are the journal's aggregate types that come from Service Context & Operations (migration 164).
var ServiceAggregates = []string{"service_session", "service_case", "service_task", "external_record", "external_request"}

func IsServiceAggregate(t string) bool {
	for _, a := range ServiceAggregates {
		if a == t {
			return true
		}
	}
	return false
}

// LoadServiceReadFor says whether a live installation still holds the scope service.read: the delivery worker asks it at the moment of sending, so withdrawing the
// scope reaches the deliveries already queued.
func LoadServiceReadFor(ctx context.Context, q platformdb.Querier, tenantID, installationID uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT external_installation_has_scope($1, $2, 'service.read')`, tenantID, installationID).Scan(&ok)
	return ok, err
}
