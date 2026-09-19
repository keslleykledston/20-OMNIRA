package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

func tenantIDFromContext(ctx context.Context) (uuid.UUID, error) {
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("tenant context is required")
	}
	return tc.TenantID, nil
}
