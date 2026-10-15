// Package domain holds the rules of Service Context & Operations (ADR-0049): service sessions (atendimentos) with a protocol, their states and results,
// and the cases, tasks and external records that hang from them. It knows nothing about HTTP or SQL.
package domain

import "errors"

var (
	ErrNotFound            = errors.New("servicectx: not found")
	ErrForbidden           = errors.New("servicectx: forbidden")
	ErrInvalid             = errors.New("servicectx: invalid input")
	ErrConflict            = errors.New("servicectx: the record changed since it was read")
	ErrPreconditionNeeded  = errors.New("servicectx: If-Match is required")
	ErrInvalidTransition   = errors.New("servicectx: transition not allowed")
	ErrIdempotencyMismatch = errors.New("servicectx: idempotency key reused with another request")
	ErrNotACustomerChat    = errors.New("servicectx: only a conversation with a customer has service sessions")
	ErrConversationClosed  = errors.New("servicectx: the conversation is closed")
	ErrReopenNotAllowed    = errors.New("servicectx: the session cannot be reopened")
	ErrUnknownResolution   = errors.New("servicectx: unknown or inactive result code")
	ErrNotConfigured       = errors.New("servicectx: integration not configured for this company")
	ErrOutcomeUnknown      = errors.New("servicectx: an earlier request has an unknown outcome and must be reconciled first")
	ErrAlreadyActive       = errors.New("servicectx: the conversation already has an active session")
	ErrExternalUnavailable = errors.New("servicectx: the external system did not answer")
)
