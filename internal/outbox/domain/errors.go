package domain

import "errors"

var (
	ErrInvalidTenantID = errors.New("tenant_id is required")
	ErrNotPublished    = errors.New("event has not been published yet")
)
