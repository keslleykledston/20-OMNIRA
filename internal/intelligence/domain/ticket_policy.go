package domain

import "github.com/google/uuid"

// TicketAction is what the ticket policy advises for a topic that has no primary ticket yet.
type TicketAction string

const (
	TicketActionNone        TicketAction = "none"
	TicketActionAdoptActive TicketAction = "adopt_active" // the conversation's active ticket becomes the topic's primary one
	TicketActionShareActive TicketAction = "share_active" // the topic is related to a ticket that belongs to another topic
	TicketActionCreate      TicketAction = "create"       // the conversation has no active ticket: open one for this topic
	TicketActionNeedsAgent  TicketAction = "needs_agent"  // the policy cannot decide safely: a person chooses
)

// ActiveTicketFacts describes the conversation's active ticket (OMNIRA allows ONE active ticket per conversation,
// migration 000016: a second concurrent process in the same conversation is a policy decision, not something automation does).
type ActiveTicketFacts struct {
	TicketID       uuid.UUID
	PrimaryTopicID *uuid.UUID // the topic that already owns it as primary, if any
}

// TicketPolicyInput is everything the policy looks at. It is computed from stored state, never from the request.
type TicketPolicyInput struct {
	TopicID          uuid.UUID
	TopicOpen        bool
	HasPrimaryTicket bool
	Conversations    int // conversations the topic spans
	InGroupOnly      bool
	ActiveTicket     *ActiveTicketFacts
	OtherOpenTopics  int // other open topics in the same conversation
	MessageCount     int
}

// TicketAdvice is the policy outcome. Reason is for people (pt-BR); Action is what is allowed.
type TicketAdvice struct {
	Action   TicketAction
	TicketID *uuid.UUID
	Reason   string
	// Allowed lists the actions a person may still choose for this topic (the server re-checks on apply).
	Allowed []TicketAction
}

// DecideTicket is deterministic and conservative: it never opens a second concurrent ticket on its own (only a person's
// choice does), never takes a ticket away from another topic and never guesses when a topic spans conversations.
func DecideTicket(in TicketPolicyInput) TicketAdvice {
	none := func(reason string) TicketAdvice { return TicketAdvice{Action: TicketActionNone, Reason: reason} }
	switch {
	case !in.TopicOpen:
		return none("O assunto não está aberto.")
	case in.HasPrimaryTicket:
		return none("O assunto já tem chamado principal.")
	case in.InGroupOnly || in.Conversations == 0:
		return TicketAdvice{Action: TicketActionNeedsAgent, Reason: "O assunto vem de um grupo e não tem conversa individual: um atendente decide se abre chamado."}
	case in.Conversations > 1:
		return TicketAdvice{Action: TicketActionNeedsAgent, Reason: "O assunto atravessa mais de uma conversa: um atendente escolhe o chamado."}
	case in.MessageCount == 0:
		return none("O assunto ainda não tem mensagens.")
	}
	if in.ActiveTicket == nil {
		return TicketAdvice{Action: TicketActionCreate, Reason: "A conversa não tem chamado ativo.", Allowed: []TicketAction{TicketActionCreate}}
	}
	id := in.ActiveTicket.TicketID
	if in.ActiveTicket.PrimaryTopicID == nil {
		return TicketAdvice{Action: TicketActionAdoptActive, TicketID: &id, Reason: "O chamado ativo da conversa ainda não pertence a nenhum assunto.", Allowed: []TicketAction{TicketActionAdoptActive, TicketActionShareActive}}
	}
	if *in.ActiveTicket.PrimaryTopicID == in.TopicID {
		return none("O assunto já tem chamado principal.")
	}
	// The conversation's ticket belongs to another subject. A new subject may need its own process: opening a ticket for
	// it is allowed on a person's choice (never automatically), as is relating the topic to the existing ticket.
	return TicketAdvice{Action: TicketActionNeedsAgent, TicketID: &id,
		Reason:  "O chamado ativo da conversa já pertence a outro assunto. Abra um chamado para este assunto ou relacione-o ao existente.",
		Allowed: []TicketAction{TicketActionCreate, TicketActionShareActive}}
}

// CanApply reports whether an action chosen by a person is among the ones the policy allows right now.
func (a TicketAdvice) CanApply(action TicketAction) bool {
	if action == a.Action && action != TicketActionNone && action != TicketActionNeedsAgent {
		return true
	}
	for _, x := range a.Allowed {
		if x == action {
			return true
		}
	}
	return false
}
