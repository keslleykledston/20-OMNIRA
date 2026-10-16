package adapters

import (
	"context"
	"errors"
	"log"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	attendanceapplication "github.com/omnira/omnira/internal/attendance/application"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	messagesports "github.com/omnira/omnira/internal/messages/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/ports"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// Notice tells the customer, in the conversation, that the protocol opened and that it ended (ADR-0049 addendum, OMNIRA_SERVICE_PROTOCOL_NOTICE_ENABLED). It queues
// an ordinary outbound text through the same senders as any reply (permission, holder, line, 24 h window, delivery by the worker), once per protocol and kind
// (idempotency key per session), and never decides anything about the session: every refusal is just "not sent".
type Notice struct {
	pool      *pgxpool.Pool
	store     *messagesadapters.PostgresOutboundStore
	operator  *messagesapplication.Sender
	delegated *messagesapplication.DelegatedSender
	tenants   map[uuid.UUID]bool // empty = every company
}

// NewNotice builds the notice. tenants limits it to those companies (staged roll-out); empty means every company.
func NewNotice(pool *pgxpool.Pool, tenants []uuid.UUID) *Notice {
	store := messagesadapters.NewPostgresOutboundStore(pool)
	n := &Notice{
		pool:      pool,
		store:     store,
		operator:  messagesapplication.NewSender(store, channeladapters.NewPostgresPermissionChecker(pool)),
		delegated: messagesapplication.NewDelegatedSender(store),
		tenants:   map[uuid.UUID]bool{},
	}
	for _, t := range tenants {
		n.tenants[t] = true
	}
	return n
}

func (n *Notice) Opened(ctx context.Context, a ports.Announcement) error {
	return n.send(ctx, a, "opened", application.RenderProtocolOpened)
}

func (n *Notice) Closed(ctx context.Context, a ports.Announcement) error {
	return n.send(ctx, a, "closed", application.RenderProtocolClosed)
}

func (n *Notice) enabledFor(tenant uuid.UUID) bool { return len(n.tenants) == 0 || n.tenants[tenant] }

// notSent are the refusals that only mean "the customer is not told now": a closed window, no text channel, a conversation that is not the person's.
func notSent(err error) bool {
	for _, e := range []error{
		messagesapplication.ErrWindowClosed, messagesapplication.ErrChannelUnavailable, messagesapplication.ErrForbidden,
		messagesapplication.ErrUnassigned, messagesapplication.ErrNotAssignedToYou, messagesapplication.ErrNotFound,
	} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

func (n *Notice) send(ctx context.Context, a ports.Announcement, kind string, render func(name, protocol string) string) error {
	if !n.enabledFor(a.TenantID) || a.ConversationID == uuid.Nil || a.SessionID == uuid.Nil {
		return nil
	}
	key := "service-" + kind + "-" + a.SessionID.String()
	do := func(ctx context.Context) error {
		if used, err := n.store.NoticeAlreadySent(ctx, a.ConversationID, key); err != nil || used {
			return err
		}
		name, err := n.store.ContactName(ctx, a.ConversationID)
		if err != nil {
			return err
		}
		text := render(name, a.Protocol)
		switch a.Mode {
		case ports.AnnounceMember:
			_, err = n.operator.Send(ctx, a.ConversationID, text, key)
		default: // delegated: the key conversation.reply was proven by the caller; the sender still needs the agent to HOLD the conversation, the line and the open window
			_, err = n.delegated.Send(ctx, a.Actor, a.ConversationID, text, key, func(context.Context, *messagesports.SendContext) error { return nil })
		}
		if notSent(err) {
			log.Printf("service-context: protocol %s notice (%s) not sent: %v", a.Protocol, kind, err)
			return nil
		}
		return err
	}
	if a.Mode == ports.AnnounceDelegatedServe {
		// A Hub agent in the delegated request session cannot write messages (no policy opens that table to it). Prove the key in THIS session, then queue the notice in
		// a system session scoped to the company (the Hub's own write path does the same). The service's row lock on the conversation is a share lock for a delegate, so
		// the sender's own share lock does not wait on it.
		var may bool
		if err := platformdb.QuerierFromContext(ctx, n.pool).QueryRow(ctx, `SELECT actor_has_permission($1, $2, 'conversation.reply')`, a.TenantID, a.Actor).Scan(&may); err != nil {
			return err
		}
		if !may {
			log.Printf("service-context: protocol %s notice (%s) not sent: the agent's grant lacks conversation.reply", a.Protocol, kind)
			return nil
		}
		return platformdb.WithSystemTenantSession(ctx, n.pool, a.TenantID, do)
	}
	return do(ctx)
}

// FinalizeNotice is the attendance finalization's way to tell the customer that the protocol ends. It runs INSIDE the finalize transaction BEFORE the conversation is
// marked closed (a closed conversation refuses outbound text), under a savepoint: ending the attendance never waits on a courtesy. A conversation without an active
// protocol gets nothing.
func FinalizeNotice(pool *pgxpool.Pool, notice *Notice) attendanceapplication.SessionNotice {
	repo := NewPostgresRepository(pool)
	return func(ctx context.Context, conversationID, actor uuid.UUID, delegated bool) {
		tc, err := tenancydomain.FromContext(ctx)
		if err != nil || notice == nil || !notice.enabledFor(tc.TenantID) {
			return
		}
		err = platformdb.WithSavepoint(ctx, pool, func(ctx context.Context) error {
			sess, err := repo.ActiveByConversation(ctx, conversationID)
			if err != nil || sess == nil {
				return err
			}
			mode := ports.AnnounceMember
			if delegated {
				mode = ports.AnnounceDelegatedSystem // the Hub's write path: a system session of the company, acting for the agent
			}
			return notice.Closed(ctx, ports.Announcement{TenantID: tc.TenantID, ConversationID: conversationID, SessionID: sess.ID, Protocol: sess.Protocol, Actor: actor, Mode: mode})
		})
		if err != nil {
			log.Printf("service-context: telling the customer the protocol of conversation %s ends failed: %v", conversationID, err)
		}
	}
}
