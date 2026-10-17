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
