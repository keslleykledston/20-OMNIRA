package domain_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/messages/domain"
)

func TestTextMessageDirectionSetsInitialStatus(t *testing.T) {
	inbound, err := domain.NewTextMessage(uuid.New(), uuid.New(), domain.DirectionInbound, "oi", "provider-1")
	if err != nil || inbound.Status != domain.StatusReceived {
		t.Fatalf("unexpected inbound: %+v %v", inbound, err)
	}
	outbound, err := domain.NewTextMessage(uuid.New(), uuid.New(), domain.DirectionOutbound, "olá", "")
	if err != nil || outbound.Status != domain.StatusQueued {
		t.Fatalf("unexpected outbound: %+v %v", outbound, err)
	}
}
