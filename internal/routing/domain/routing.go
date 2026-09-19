package domain

import (
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
)

type Mode string

const (
	ModeManual     Mode = "manual"
	ModeRoundRobin Mode = "round_robin"
)

type Queue struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Name     string
	Mode     Mode
}

type Agent struct {
	UserID         uuid.UUID
	Active         bool
	Available      bool
	Capacity       int
	ActiveWorkload int
	LastAssignedAt *time.Time
}

func (q Queue) SelectNext(agents []Agent) (uuid.UUID, error) {
	if q.Mode == ModeManual {
		return uuid.Nil, errors.New("routing: manual queue requires explicit assignment")
	}
	if q.Mode != ModeRoundRobin {
		return uuid.Nil, errors.New("routing: unsupported queue mode")
	}
	eligible := make([]Agent, 0, len(agents))
	for _, agent := range agents {
		if agent.UserID != uuid.Nil && agent.Active && agent.Available && agent.Capacity > agent.ActiveWorkload {
			eligible = append(eligible, agent)
		}
	}
	if len(eligible) == 0 {
		return uuid.Nil, errors.New("routing: no eligible agent")
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		left, right := eligible[i].LastAssignedAt, eligible[j].LastAssignedAt
		if left == nil && right != nil {
			return true
		}
		if left != nil && right == nil {
			return false
		}
		if left != nil && right != nil && !left.Equal(*right) {
			return left.Before(*right)
		}
		return eligible[i].UserID.String() < eligible[j].UserID.String()
	})
	return eligible[0].UserID, nil
}
