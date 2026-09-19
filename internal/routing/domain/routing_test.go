package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/routing/domain"
)

func TestRoundRobinSelectsOldestEligibleAgent(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	newer := time.Now()
	first, second := uuid.New(), uuid.New()
	queue := domain.Queue{Mode: domain.ModeRoundRobin}
	got, err := queue.SelectNext([]domain.Agent{
		{UserID: second, Active: true, Available: true, Capacity: 1, LastAssignedAt: &newer},
		{UserID: first, Active: true, Available: true, Capacity: 1, LastAssignedAt: &old},
		{UserID: uuid.New(), Active: true, Available: false, Capacity: 1},
	})
	if err != nil || got != first {
		t.Fatalf("got %s, err=%v", got, err)
	}
}

func TestRoundRobinSkipsFullAndManualRequiresExplicitClaim(t *testing.T) {
	queue := domain.Queue{Mode: domain.ModeRoundRobin}
	if _, err := queue.SelectNext([]domain.Agent{{UserID: uuid.New(), Active: true, Available: true, Capacity: 1, ActiveWorkload: 1}}); err == nil {
		t.Fatal("full agent selected")
	}
	if _, err := (domain.Queue{Mode: domain.ModeManual}).SelectNext(nil); err == nil {
		t.Fatal("manual queue selected agent")
	}
}
