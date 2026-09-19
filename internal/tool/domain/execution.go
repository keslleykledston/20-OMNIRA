package domain

import (
	"time"

	"github.com/google/uuid"
)

// ExecutionID — identificador único de execução
type ExecutionID = uuid.UUID

// ToolExecution — registro de execução de uma tool
type ToolExecution struct {
	ID            ExecutionID
	TenantID      uuid.UUID
	ToolID        ToolID
	RequestedBy   uuid.UUID
	CorrelationID uuid.UUID // Para rastreamento distribuído
	Status        ExecutionStatus
	Input         map[string]interface{}
	Output        map[string]interface{}
	Error         string
	Duration      int          // Milliseconds
	StartedAt     time.Time
	CompletedAt   *time.Time
	CreatedAt     time.Time
}

// ExecutionStatus — status da execução
type ExecutionStatus string

const (
	ExecutionStatusPending   ExecutionStatus = "pending"
	ExecutionStatusRunning   ExecutionStatus = "running"
	ExecutionStatusCompleted ExecutionStatus = "completed"
	ExecutionStatusFailed    ExecutionStatus = "failed"
	ExecutionStatusCanceled  ExecutionStatus = "canceled"
	ExecutionStatusTimeout   ExecutionStatus = "timeout"
)

// NewExecution — cria nova execução
func NewExecution(tenantID, toolID, requestedBy, correlationID uuid.UUID, input map[string]interface{}) *ToolExecution {
	return &ToolExecution{
		ID:            uuid.New(),
		TenantID:      tenantID,
		ToolID:        toolID,
		RequestedBy:   requestedBy,
		CorrelationID: correlationID,
		Status:        ExecutionStatusPending,
		Input:         input,
		CreatedAt:     time.Now(),
		StartedAt:     time.Now(),
	}
}

// Start — marca execução como iniciada
func (e *ToolExecution) Start() {
	e.Status = ExecutionStatusRunning
	e.StartedAt = time.Now()
}

// Complete — marca execução como completa
func (e *ToolExecution) Complete(output map[string]interface{}) {
	now := time.Now()
	e.Status = ExecutionStatusCompleted
	e.Output = output
	e.CompletedAt = &now
	e.Duration = int(now.Sub(e.StartedAt).Milliseconds())
}

// Fail — marca execução como falha
func (e *ToolExecution) Fail(errMsg string) {
	now := time.Now()
	e.Status = ExecutionStatusFailed
	e.Error = errMsg
	e.CompletedAt = &now
	e.Duration = int(now.Sub(e.StartedAt).Milliseconds())
}

// Timeout — marca execução como timeout
func (e *ToolExecution) Timeout() {
	now := time.Now()
	e.Status = ExecutionStatusTimeout
	e.Error = "execution timeout"
	e.CompletedAt = &now
	e.Duration = int(now.Sub(e.StartedAt).Milliseconds())
}

// Cancel — cancela execução
func (e *ToolExecution) Cancel() {
	now := time.Now()
	e.Status = ExecutionStatusCanceled
	e.CompletedAt = &now
	e.Duration = int(now.Sub(e.StartedAt).Milliseconds())
}

// IsCompleted — verifica se execução terminou
func (e *ToolExecution) IsCompleted() bool {
	return e.Status == ExecutionStatusCompleted ||
		e.Status == ExecutionStatusFailed ||
		e.Status == ExecutionStatusCanceled ||
		e.Status == ExecutionStatusTimeout
}

// IsSuccessful — verifica se execução foi bem-sucedida
func (e *ToolExecution) IsSuccessful() bool {
	return e.Status == ExecutionStatusCompleted
}
