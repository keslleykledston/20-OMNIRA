package application

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/flows/domain"
	"github.com/omnira/omnira/internal/flows/ports"
)

// ADR-0046: the bot never speaks over, starts on, or routes a conversation an external application holds. assigned_to_user_id stays NULL for such a
// conversation, which the rules above would read as "nobody has it", so the facts carry the hold explicitly.
func TestTheBotNeverActsOnAConversationAnExternalApplicationHolds(t *testing.T) {
	contact := uuid.New()
	free := &ports.ConversationFacts{Status: "open", AutomationMode: domain.AutomationBot, ContactID: &contact, Kind: "customer_service"}
	if !controls(free) || !Startable(free) {
		t.Fatal("control: a free customer-service conversation in bot mode is the bot's")
	}
	held := *free
	held.HeldExternally = true
	if controls(&held) {
		t.Error("the bot controls a conversation an application holds")
	}
	if Startable(&held) {
		t.Error("a flow may start on a conversation an application holds")
	}
	// a person holding it is still the person's (unchanged behaviour)
	someone := uuid.New()
	mine := *free
	mine.AssignedTo = &someone
	if controls(&mine) || Startable(&mine) {
		t.Error("the existing rule for a person's conversation changed")
	}
}
