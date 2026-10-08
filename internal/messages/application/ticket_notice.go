package application

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// NoticeStore is what the notice needs from the database, always inside the request's tenant session.
type NoticeStore interface {
	// ContactName is the name the customer is addressed by in a conversation. Empty is a valid answer (the contact has
	// none worth showing); an error is a failure to read.
	ContactName(ctx context.Context, conversationID uuid.UUID) (string, error)
	// NoticeAlreadySent serialises everyone announcing the same key (a transaction-scoped lock held until the request
	// ends) and then reports whether a message with that key already exists in the conversation, from ANY operator. The
	// Sender's own idempotency is per operator, so without this a second operator repeating the creation after a
	// transfer would message the customer again.
	NoticeAlreadySent(ctx context.Context, conversationID uuid.UUID, key string) (bool, error)
}

// TicketOpenedNotice tells the customer, in the same conversation, that the ticket was opened and under which number.
// It queues an ordinary outbound text through Sender, so it carries the same authorization, the 24 h window rule and
// the delivery guarantees of any operator reply, and it is attributed to the operator who opened the ticket.
type TicketOpenedNotice struct {
	sender *Sender
	store  NoticeStore
}

func NewTicketOpenedNotice(sender *Sender, store NoticeStore) *TicketOpenedNotice {
	return &TicketOpenedNotice{sender: sender, store: store}
}

const (
	maxNoticeNameRunes   = 60
	maxNoticeNumberRunes = 40
)

// ErrNoticeSkipped marks a notice that was deliberately not sent (no usable ticket number); it is not a failure.
var ErrNoticeSkipped = errors.New("messages: ticket opened notice skipped")

// RenderTicketOpened builds the message text. The name comes from the customer's own profile and the number from the
// external system, so both are reduced to one printable line first: a name cannot add paragraphs or fake lines, and a
// missing name falls back to a generic greeting.
func RenderTicketOpened(name, ticketNumber string) string {
	greeting := "Prezado cliente"
	if n := oneLine(name, maxNoticeNameRunes); strings.IndexFunc(n, unicode.IsLetter) >= 0 {
		greeting = "Prezado " + n // a name with no letters (a bare phone number) is no way to greet someone
	}
	return greeting + ",\n\n" +
		"Sua solicitação foi recebida com sucesso. Informamos que o chamado já foi aberto e registrado em nosso sistema.\n\n" +
		"Protocolo do chamado: " + oneLine(ticketNumber, maxNoticeNumberRunes) + "\n" +
		"Nossa equipe realizará a análise e dará continuidade ao atendimento.\n\n" +
		"Atenciosamente,\n\n" +
		"Equipe de Suporte"
}

// oneLine collapses every run of whitespace/control characters into a single space and cuts to max runes.
func oneLine(s string, max int) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) || !unicode.IsPrint(r) && r != ' ' {
			space = b.Len() > 0
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	out := b.String()
	if utf8.RuneCountInString(out) > max {
		out = string([]rune(out)[:max])
	}
	return strings.TrimSpace(out)
}

// NotifyTicketOpened queues the notice. localTicketID makes the idempotency key, so repeating the call for the same
// ticket (a replayed create, a retry, another operator after a transfer) never sends it twice. It must run in the request that created the ticket, as the
// operator who did it.
func (n *TicketOpenedNotice) NotifyTicketOpened(ctx context.Context, conversationID, localTicketID uuid.UUID, ticketNumber string) error {
	if oneLine(ticketNumber, maxNoticeNumberRunes) == "" || localTicketID == uuid.Nil {
		return ErrNoticeSkipped
	}
	key := "ticket-opened-" + localTicketID.String()
	if sent, err := n.store.NoticeAlreadySent(ctx, conversationID, key); err != nil || sent {
		return err
	}
	name, err := n.store.ContactName(ctx, conversationID)
	if err != nil {
		return err
	}
	_, err = n.sender.Send(ctx, conversationID, RenderTicketOpened(name, ticketNumber), key)
	return err
}
