package adapters

import (
	"context"
	"errors"
	"log"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	attendanceapplication "github.com/omnira/omnira/internal/attendance/application"
	attendancedomain "github.com/omnira/omnira/internal/attendance/domain"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	"github.com/omnira/omnira/internal/entitlements"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	messagesports "github.com/omnira/omnira/internal/messages/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/servicectx/application"
	"github.com/omnira/omnira/internal/servicectx/domain"
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
	checker   *entitlements.Checker // the company's own switch (ADR-0038 entitlement service_protocol_notice, off until an operator turns it on)
}

// NewNotice builds the notice. Whether a company gets it is that company's switch (Hub > Instâncias > Capacidades): off by default.
func NewNotice(pool *pgxpool.Pool) *Notice {
	store := messagesadapters.NewPostgresOutboundStore(pool)
	n := &Notice{
		pool:      pool,
		store:     store,
		operator:  messagesapplication.NewSender(store, channeladapters.NewPostgresPermissionChecker(pool)),
		delegated: messagesapplication.NewDelegatedSender(store),
		checker:   entitlements.NewChecker(pool),
	}
	return n
}

func (n *Notice) Opened(ctx context.Context, a ports.Announcement) error {
	return n.send(ctx, a, "opened", application.RenderProtocolOpened)
}

func (n *Notice) Closed(ctx context.Context, a ports.Announcement) error {
	return n.send(ctx, a, "closed", application.RenderProtocolClosed)
}

// enabledFor reads the company's switch in a system session of that company: a Hub agent's own session cannot read another company's switches, and a failed read
// fails closed (nobody is messaged on a guess).
func (n *Notice) enabledFor(ctx context.Context, tenant uuid.UUID) bool {
	on := false
	err := platformdb.WithSystemTenantSession(ctx, n.pool, tenant, func(c context.Context) error {
		var e error
		on, e = n.checker.Enabled(c, tenant, entitlements.ServiceProtocolNotice)
		return e
	})
	if err != nil {
		log.Printf("service-context: the protocol notice switch of company %s could not be read: %v", tenant, err)
		return false
	}
	return on
}

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
	if !n.enabledFor(ctx, a.TenantID) || a.ConversationID == uuid.Nil || a.SessionID == uuid.Nil {
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
		queue := func(c context.Context) error { return platformdb.WithSystemTenantSession(c, n.pool, a.TenantID, do) }
		// The notice goes out only if the request that opened or closed the protocol COMMITS: the system session is a separate transaction, so queuing it now would tell
		// the customer about a protocol a failed commit then rolled back (Codex 2026-10-16, R-7).
		if platformdb.AfterCommit(ctx, func(c context.Context) {
			if err := queue(c); err != nil {
				log.Printf("service-context: protocol %s notice (%s) failed after the commit: %v", a.Protocol, kind, err)
			}
		}) {
			return nil
		}
		return queue(ctx)
	}
	return do(ctx)
}

// FinalizeNotice is the attendance finalization's way to tell the customer that the protocol ends. It runs INSIDE the finalize transaction BEFORE the conversation is
// marked closed (a closed conversation refuses outbound text), under a savepoint: ending the attendance never waits on a courtesy. An attendance nobody assumed has no
// protocol yet: it gets one here first (not for spam or duplicates, which are not an attendance to tell anyone about), so that the number the customer is told is the number
// that ends up closed. A conversation whose protocol the operator already closed by hand gets nothing more.
func FinalizeNotice(pool *pgxpool.Pool, notice *Notice, svc *application.Service) attendanceapplication.SessionNotice {
	repo := NewPostgresRepository(pool)
	return func(ctx context.Context, conversationID, actor uuid.UUID, delegated bool, reason attendancedomain.CloseReason) {
		if reason == attendancedomain.ReasonSpam || reason == attendancedomain.ReasonDuplicate {
			return // not an attendance to tell anyone about, whether or not it already had a protocol (the protocol still ends, for tracing)
		}
		tc, err := tenancydomain.FromContext(ctx)
		if err != nil || notice == nil || !notice.enabledFor(ctx, tc.TenantID) {
			return
		}
		err = platformdb.WithSavepoint(ctx, pool, func(ctx context.Context) error {
			sess, err := repo.ActiveByConversation(ctx, conversationID)
			if err != nil {
				return err
			}
			if sess == nil && svc != nil {
				if created, err := svc.EnsureForFinalize(ctx, conversationID, actor, domain.SourceFinalize); err != nil {
					return err
				} else if created {
					sess, err = repo.ActiveByConversation(ctx, conversationID)
					if err != nil {
						return err
					}
				}
			}
			if sess == nil {
				return nil
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
