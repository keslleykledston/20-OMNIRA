package ports

import (
	"context"

	"github.com/google/uuid"
)

type AssignmentRepository interface {
	ClaimUnassigned(context.Context, uuid.UUID, uuid.UUID, string) (bool, error)
}
