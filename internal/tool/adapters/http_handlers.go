package adapters

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tenancy/domain"
	tooldomain "github.com/omnira/omnira/internal/tool/domain"
	"github.com/omnira/omnira/internal/tool/application"
	"github.com/omnira/omnira/internal/tool/execution"
)

// ToolHandler — handlers HTTP para Tool API
type ToolHandler struct {
	toolSvc     *application.ToolService
	executor    *execution.Executor
}

// NewToolHandler — cria novo ToolHandler
func NewToolHandler(toolSvc *application.ToolService, executor *execution.Executor) *ToolHandler {
	return &ToolHandler{
		toolSvc:  toolSvc,
		executor: executor,
	}
}

// CreateToolRequest — request para criar tool
type CreateToolRequest struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Type        string                 `json:"type"` // http, webhook, script, sql
	Spec        map[string]interface{} `json:"spec"`
}

// ToolResponse — resposta de tool
type ToolResponse struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Type        string                 `json:"type"`
	Spec        map[string]interface{} `json:"spec"`
	Status      string                 `json:"status"`
	Version     int                    `json:"version"`
	CreatedAt   string                 `json:"created_at"`
	UpdatedAt   string                 `json:"updated_at"`
}

// CreateTool — POST /tools
func (h *ToolHandler) CreateTool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	tenantCtx, err := domain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	var req CreateToolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Converter spec map para ToolSpec
	specBytes, _ := json.Marshal(req.Spec)
	var spec tooldomain.ToolSpec
	json.Unmarshal(specBytes, &spec)

	// Criar tool
	tool, err := h.toolSvc.CreateTool(
		ctx,
		tenantCtx.TenantID,
		tenantCtx.ActorID,
		req.Name,
		req.Description,
		tooldomain.ToolType(req.Type),
		spec,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(toToolResponse(tool))
}

// ListTools — GET /tools
func (h *ToolHandler) ListTools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	tenantCtx, err := domain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	tools, err := h.toolSvc.ListTools(ctx, tenantCtx.TenantID, 100, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	responses := make([]ToolResponse, len(tools))
	for i, tool := range tools {
		responses[i] = toToolResponse(tool)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tools": responses,
		"count": len(responses),
	})
}

// ExecuteToolRequest — request para executar tool
type ExecuteToolRequest struct {
	Input map[string]interface{} `json:"input"`
}

// ExecutionResponse — resposta de execução
type ExecutionResponse struct {
	ID            string                 `json:"id"`
	ToolID        string                 `json:"tool_id"`
	Status        string                 `json:"status"`
	Input         map[string]interface{} `json:"input"`
	Output        map[string]interface{} `json:"output,omitempty"`
	Error         string                 `json:"error,omitempty"`
	Duration      int                    `json:"duration"` // milliseconds
	StartedAt     string                 `json:"started_at"`
	CompletedAt   *string                `json:"completed_at,omitempty"`
}

// ExecuteTool — POST /tools/{toolID}/execute
func (h *ToolHandler) ExecuteTool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	tenantCtx, err := domain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	// Extrair toolID da URL
	toolIDStr := r.PathValue("toolID")
	toolID, err := uuid.Parse(toolIDStr)
	if err != nil {
		http.Error(w, "invalid tool ID", http.StatusBadRequest)
		return
	}

	var req ExecuteToolRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Registrar execução
	correlationID := uuid.New()
	exec, err := h.toolSvc.RecordExecution(ctx, tenantCtx.TenantID, toolID, tenantCtx.ActorID, correlationID, req.Input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Buscar tool
	tool, err := h.toolSvc.GetTool(ctx, toolID)
	if err != nil {
		http.Error(w, "tool not found", http.StatusNotFound)
		return
	}

	// Executar tool
	exec.Start()
	output, execErr := h.executor.Execute(ctx, tool, req.Input)

	if execErr != nil {
		exec.Fail(execErr.Error())
	} else {
		exec.Complete(output)
	}

	// Atualizar execução no banco
	if err := h.toolSvc.UpdateExecution(ctx, exec); err != nil {
		http.Error(w, "failed to update execution", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	status := http.StatusOK
	if execErr != nil {
		status = http.StatusBadRequest
	}
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(toExecutionResponse(exec))
}

// GetExecution — GET /executions/{executionID}
func (h *ToolHandler) GetExecution(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	tenantCtx, err := domain.FromContext(ctx)
	if err != nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	// Extrair executionID da URL
	execIDStr := r.PathValue("executionID")
	execID, err := uuid.Parse(execIDStr)
	if err != nil {
		http.Error(w, "invalid execution ID", http.StatusBadRequest)
		return
	}

	exec, err := h.toolSvc.GetExecution(ctx, execID)
	if err != nil {
		http.Error(w, "execution not found", http.StatusNotFound)
		return
	}

	// Verificar que execução pertence ao tenant
	if exec.TenantID != tenantCtx.TenantID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(toExecutionResponse(exec))
}

// Helpers

func toToolResponse(tool *tooldomain.Tool) ToolResponse {
	specBytes, _ := json.Marshal(tool.Spec)
	var specMap map[string]interface{}
	json.Unmarshal(specBytes, &specMap)

	return ToolResponse{
		ID:          tool.ID.String(),
		Name:        tool.Name,
		Description: tool.Description,
		Type:        string(tool.Type),
		Spec:        specMap,
		Status:      string(tool.Status),
		Version:     tool.Version,
		CreatedAt:   tool.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt:   tool.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}
}

func toExecutionResponse(exec *tooldomain.ToolExecution) ExecutionResponse {
	resp := ExecutionResponse{
		ID:        exec.ID.String(),
		ToolID:    exec.ToolID.String(),
		Status:    string(exec.Status),
		Input:     exec.Input,
		Output:    exec.Output,
		Error:     exec.Error,
		Duration:  exec.Duration,
		StartedAt: exec.StartedAt.Format("2006-01-02T15:04:05Z"),
	}

	if exec.CompletedAt != nil {
		completedStr := exec.CompletedAt.Format("2006-01-02T15:04:05Z")
		resp.CompletedAt = &completedStr
	}

	return resp
}
