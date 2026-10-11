package httpserver

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	messagesports "github.com/omnira/omnira/internal/messages/ports"
)

// closingNoticeState turns the outcome of queueing the closing notice into what the finalize answers (never an error for a definitive "no": the person asked for
// a courtesy, finalizing the attendance does not depend on it). Anything that is not a clear refusal is returned as an error, which aborts the whole finalize: the
// notice is queued in the same transaction that closes the conversation, so both happen or neither does.
func closingNoticeState(res messagesapplication.SendResult, err error) (attendancedomain.FarewellState, error) {
	switch {
	case err == nil:
		return attendancedomain.FarewellQueued, nil
	case errors.Is(err, messagesapplication.ErrWindowClosed):
		return attendancedomain.FarewellWindowClosed, nil
	case errors.Is(err, messagesapplication.ErrChannelUnavailable):
		return attendancedomain.FarewellNoChannel, nil
	case errors.Is(err, messagesapplication.ErrForbidden), errors.Is(err, messagesapplication.ErrUnassigned), errors.Is(err, messagesapplication.ErrNotAssignedToYou):
		return attendancedomain.FarewellNotAllowed, nil
	}
	return "", err
}

// ClosingNoticeSenders builds the two ways the closing notice is queued inside a finalize (ADR-0040 phase 08): a member through the ordinary operator Sender (same
// permission, assignee, channel and 24 h window rules as any reply), a Hub agent through the delegated sender (the key conversation.reply is proven by the Hub's write path before). The
// message and its delivery job are inserted in the request's own transaction, so they commit or roll back together with the closing; delivery stays with the worker.
func ClosingNoticeSenders(dbPool *pgxpool.Pool) (
	member func(ctx context.Context, conversationID uuid.UUID, text, key string) (attendancedomain.FarewellState, error),
	agent func(ctx context.Context, agent, conversationID uuid.UUID, text, key string) (attendancedomain.FarewellState, error),
) {
	store := messagesadapters.NewPostgresOutboundStore(dbPool)
	operator := messagesapplication.NewSender(store, channeladapters.NewPostgresPermissionChecker(dbPool))
	delegated := messagesapplication.NewDelegatedSender(store)
	member = func(ctx context.Context, conversationID uuid.UUID, text, key string) (attendancedomain.FarewellState, error) {
		res, err := operator.Send(ctx, conversationID, text, key)
		return closingNoticeState(res, err)
	}
	agent = func(ctx context.Context, agent, conversationID uuid.UUID, text, key string) (attendancedomain.FarewellState, error) {
		// the key conversation.reply was proven by the caller in the delegated session, and the Hub's write path (replying.Act) proved the delegation again under the
		// conversation's row lock in this same transaction; the sender still requires the agent to HOLD the conversation, the line and the open window
		res, err := delegated.Send(ctx, agent, conversationID, text, key, func(context.Context, *messagesports.SendContext) error { return nil })
		return closingNoticeState(res, err)
	}
	return member, agent
}
