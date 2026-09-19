package domain

import "errors"

var (
	ErrInvalidTenantID = errors.New("tenant_id is required")
	ErrInvalidActorID  = errors.New("actor_id is required")
)
