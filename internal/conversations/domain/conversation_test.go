package domain_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/conversations/domain"
)

func TestConversationLifecycle(t *testing.T) {
	c, err := domain.NewConversation(uuid.New(), uuid.New(), nil)
	if err != nil || c.Status != domain.StatusOpen {
		t.Fatalf("unexpected conversation: %+v %v", c, err)
	}
	c.Close()
	if c.Status != domain.StatusClosed || c.ClosedAt == nil {
		t.Fatal("conversation did not close")
	}
}
