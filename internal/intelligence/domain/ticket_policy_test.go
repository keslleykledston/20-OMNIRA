package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestDecideTicketIsConservative(t *testing.T) {
	topic, other, tk := uuid.New(), uuid.New(), uuid.New()
	base := TicketPolicyInput{TopicID: topic, TopicOpen: true, Conversations: 1, MessageCount: 3}
	with := func(f func(*TicketPolicyInput)) TicketPolicyInput { in := base; f(&in); return in }
	cases := []struct {
		name string
		in   TicketPolicyInput
		want TicketAction
	}{
		{"closed topic", with(func(i *TicketPolicyInput) { i.TopicOpen = false }), TicketActionNone},
		{"already has a primary", with(func(i *TicketPolicyInput) { i.HasPrimaryTicket = true }), TicketActionNone},
		{"group only", with(func(i *TicketPolicyInput) { i.InGroupOnly, i.Conversations = true, 0 }), TicketActionNeedsAgent},
		{"spans two conversations", with(func(i *TicketPolicyInput) { i.Conversations = 2 }), TicketActionNeedsAgent},
		{"no messages", with(func(i *TicketPolicyInput) { i.MessageCount = 0 }), TicketActionNone},
		{"no active ticket", base, TicketActionCreate},
		{"unowned placeholder", with(func(i *TicketPolicyInput) { i.ActiveTicket = &ActiveTicketFacts{TicketID: tk} }), TicketActionAdoptActive},
		{"owned by another topic", with(func(i *TicketPolicyInput) { i.ActiveTicket = &ActiveTicketFacts{TicketID: tk, PrimaryTopicID: &other} }), TicketActionNeedsAgent},
		{"owned by this topic", with(func(i *TicketPolicyInput) { i.ActiveTicket = &ActiveTicketFacts{TicketID: tk, PrimaryTopicID: &topic} }), TicketActionNone},
	}
	for _, c := range cases {
		if got := DecideTicket(c.in); got.Action != c.want || got.Reason == "" {
			t.Errorf("%s: %+v, want %s", c.name, got, c.want)
		}
	}
	// a second concurrent ticket is never advised, whatever the other facts
	adv := DecideTicket(with(func(i *TicketPolicyInput) {
		i.ActiveTicket = &ActiveTicketFacts{TicketID: tk, PrimaryTopicID: &other}
		i.OtherOpenTopics = 3
	}))
	if adv.CanApply(TicketActionCreate) || adv.CanApply(TicketActionAdoptActive) || !adv.CanApply(TicketActionShareActive) {
		t.Errorf("allowed = %v", adv.Allowed)
	}
	if adv := DecideTicket(base); !adv.CanApply(TicketActionCreate) || adv.CanApply(TicketActionShareActive) {
		t.Errorf("create case allowed = %v", adv.Allowed)
	}
	if DecideTicket(base).CanApply(TicketActionNone) || DecideTicket(base).CanApply(TicketActionNeedsAgent) {
		t.Error("none / needs_agent are not applicable actions")
	}
}
