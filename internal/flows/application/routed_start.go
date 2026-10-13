package application

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
)

// RoutedStart outcomes (the routing engine's "flow" destination, ADR-0044).
const (
	RoutedStarted     = "started"     // a run was created (or this very request already created it)
	RoutedBusy        = "busy"        // the conversation already has an active run: a flow is never started over another one (owner decision)
	RoutedUnavailable = "unavailable" // the flow is gone/unpublished/of another channel, or the conversation is not one the automation may take
)

// StartRouted starts ONE specific published flow on a conversation because the routing engine's overflow chain asked for it. It runs in the
// routing engine's own transaction (the caller's system tenant session), so the decision and the flow start commit together or not at all.
// key is the idempotency key of the request (one per conversation, episode and step): asking again with the same key is a no-op "started".
//
// Rules, all checked here and never assumed by the caller: the conversation is open, unassigned and one the automation may take (the same
// Startable test an inbound message passes); no run is active (busy); the flow is published, of a startable type and matches the channel
// line (the same trigger filter an inbound start honours).
func (e *Engine) StartRouted(ctx context.Context, conversationID, flowID uuid.UUID, key string) (string, error) {
	if _, err := systemTenant(ctx); err != nil {
		return RoutedUnavailable, err
	}
	lookup, ok := e.runs.(ports.FlowLookup)
	if !ok || key == "" {
		return RoutedUnavailable, nil
	}
	facts, err := e.runs.LoadConversation(ctx, conversationID) // row lock: serializes with the conversation's inbound events
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return RoutedUnavailable, nil
		}
		return RoutedUnavailable, err
	}
	if run, err := e.runs.RunByEvent(ctx, key); err == nil && run != nil {
		return RoutedStarted, nil // the same request, again
	} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return RoutedUnavailable, err
	}
	if _, err := e.runs.ActiveRun(ctx, conversationID); err == nil {
		return RoutedBusy, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return RoutedUnavailable, err
	}
	if !Startable(facts) {
		return RoutedUnavailable, nil
	}
	flow, err := lookup.FlowByID(ctx, flowID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return RoutedUnavailable, nil
		}
		return RoutedUnavailable, err
	}
	var conn uuid.UUID
	if facts.ConnectionID != nil {
		conn = *facts.ConnectionID
	}
	if flow.ActiveVersionID == nil || !flow.TriggerFilter.Matches(conn, facts.Provider) {
		return RoutedUnavailable, nil
	}
	if err := e.startRun(ctx, facts, flow, key, nil); err != nil {
		if errors.Is(err, domain.ErrDuplicateEvent) {
			return RoutedStarted, nil
		}
		if errors.Is(err, domain.ErrConversationBusy) {
			return RoutedBusy, nil
		}
		return RoutedUnavailable, err
	}
	return RoutedStarted, nil
}
