package application

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// ContactNamer gives the name the customer is addressed by in a conversation. An empty name is a valid answer (the
// contact has none worth showing); an error is a failure to read.
type ContactNamer interface {
	ContactName(ctx context.Context, conversationID uuid.UUID) (string, error)
}

// TicketOpenedNotice tells the customer, in the same conversation, that the ticket was opened and under which number.
// It queues an ordinary outbound text through Sender, so it carries the same authorization, the 24 h window rule and
// the delivery guarantees of any operator reply, and it is attributed to the operator who opened the ticket.
type TicketOpenedNotice struct {
	sender *Sender
	names  ContactNamer
}

func NewTicketOpenedNotice(sender *Sender, names ContactNamer) *TicketOpenedNotice {
	return &TicketOpenedNotice{sender: sender, names: names}
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
// ticket (a replayed create, a retry) never sends it twice. It must run in the request that created the ticket, as the
// operator who did it.
func (n *TicketOpenedNotice) NotifyTicketOpened(ctx context.Context, conversationID, localTicketID uuid.UUID, ticketNumber string) error {
	if oneLine(ticketNumber, maxNoticeNumberRunes) == "" || localTicketID == uuid.Nil {
		return ErrNoticeSkipped
	}
	name, err := n.names.ContactName(ctx, conversationID)
	if err != nil {
		return err
	}
	_, err = n.sender.Send(ctx, conversationID, RenderTicketOpened(name, ticketNumber), "ticket-opened-"+localTicketID.String())
	return err
}
