package domain

import (
	"time"

	"github.com/google/uuid"
)

// ToolID — identificador único de tool
type ToolID = uuid.UUID

// Tool — ferramenta executável registrada no tenant
type Tool struct {
	ID          ToolID
	TenantID    uuid.UUID
	Name        string
	Description string
	Type        ToolType       // http, webhook, script, etc
	Spec        ToolSpec       // Configuração específica do tipo
	Status      ToolStatus     // active, inactive, deprecated
	Schema      ToolSchema     // Input/output schema para validação
	Version     int            // Versão incremental
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CreatedBy   uuid.UUID
	UpdatedBy   uuid.UUID
}

// ToolType — tipo de ferramenta
type ToolType string

const (
	ToolTypeHTTP    ToolType = "http"
	ToolTypeWebhook ToolType = "webhook"
	ToolTypeScript  ToolType = "script"
	ToolTypeSQL     ToolType = "sql"
)

// ToolStatus — status da ferramenta
type ToolStatus string

const (
	ToolStatusActive      ToolStatus = "active"
	ToolStatusInactive    ToolStatus = "inactive"
	ToolStatusDeprecated  ToolStatus = "deprecated"
	ToolStatusError       ToolStatus = "error"
)

// ToolSpec — configuração específica
type ToolSpec struct {
	// HTTP
	Endpoint   string            `json:"endpoint,omitempty"`    // URL do endpoint
	Method     string            `json:"method,omitempty"`      // GET, POST, PUT, DELETE
	Headers    map[string]string `json:"headers,omitempty"`     // Headers customizados
	AuthType   string            `json:"auth_type,omitempty"`   // none, basic, bearer, oauth2
	AuthSecret string            `json:"auth_secret,omitempty"` // Referência a secret

	// Script
	Language string `json:"language,omitempty"` // python, javascript, bash
	Code     string `json:"code,omitempty"`     // Código fonte

	// SQL
	Query   string `json:"query,omitempty"` // SQL query
	Timeout int    `json:"timeout,omitempty"` // Segundos

	// Webhook
	WebhookURL string `json:"webhook_url,omitempty"`
}

// ToolSchema — schema para validação de entrada/saída
type ToolSchema struct {
	InputSchema  map[string]interface{} `json:"input_schema,omitempty"`  // JSON Schema
	OutputSchema map[string]interface{} `json:"output_schema,omitempty"` // JSON Schema
	Examples     []ToolExample          `json:"examples,omitempty"`
}

// ToolExample — exemplo de execução
type ToolExample struct {
	Name   string                 `json:"name"`
	Input  map[string]interface{} `json:"input"`
	Output map[string]interface{} `json:"output"`
}

// NewTool — cria nova tool
func NewTool(tenantID, createdBy uuid.UUID, name, description string, toolType ToolType, spec ToolSpec) *Tool {
	return &Tool{
		ID:          uuid.New(),
		TenantID:    tenantID,
		Name:        name,
		Description: description,
		Type:        toolType,
		Spec:        spec,
		Status:      ToolStatusActive,
		Version:     1,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		CreatedBy:   createdBy,
		UpdatedBy:   createdBy,
	}
}

// IsActive — verifica se tool está ativa
func (t *Tool) IsActive() bool {
	return t.Status == ToolStatusActive
}

// Deactivate — desativa tool
func (t *Tool) Deactivate(deactivatedBy uuid.UUID) {
	t.Status = ToolStatusInactive
	t.UpdatedAt = time.Now()
	t.UpdatedBy = deactivatedBy
}

// Activate — ativa tool
func (t *Tool) Activate(activatedBy uuid.UUID) {
	t.Status = ToolStatusActive
	t.UpdatedAt = time.Now()
	t.UpdatedBy = activatedBy
}

// MarkDeprecated — marca tool como deprecated
func (t *Tool) MarkDeprecated(deprecatedBy uuid.UUID) {
	t.Status = ToolStatusDeprecated
	t.UpdatedAt = time.Now()
	t.UpdatedBy = deprecatedBy
}

// Update — atualiza tool
func (t *Tool) Update(name, description string, spec ToolSpec, updatedBy uuid.UUID) {
	t.Name = name
	t.Description = description
	t.Spec = spec
	t.Version++
	t.UpdatedAt = time.Now()
	t.UpdatedBy = updatedBy
}
