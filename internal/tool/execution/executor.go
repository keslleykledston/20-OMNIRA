package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/omnira/omnira/internal/tool/domain"
)

// Executor — executor de ferramentas
type Executor struct {
	httpClient *http.Client
	timeout    time.Duration
}

// NewExecutor — cria novo executor
func NewExecutor(timeout time.Duration) *Executor {
	return &Executor{
		httpClient: &http.Client{Timeout: timeout},
		timeout:    timeout,
	}
}

// Execute — executa uma ferramenta
func (e *Executor) Execute(ctx context.Context, tool *domain.Tool, input map[string]interface{}) (map[string]interface{}, error) {
	// Aplicar timeout ao context
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	switch tool.Type {
	case domain.ToolTypeHTTP:
		return e.executeHTTP(ctx, tool, input)
	case domain.ToolTypeWebhook:
		return e.executeWebhook(ctx, tool, input)
	case domain.ToolTypeScript:
		return e.executeScript(ctx, tool, input)
	case domain.ToolTypeSQL:
		return e.executeSQL(ctx, tool, input)
	default:
		return nil, fmt.Errorf("unsupported tool type: %s", tool.Type)
	}
}

// executeHTTP — executa ferramenta HTTP
func (e *Executor) executeHTTP(ctx context.Context, tool *domain.Tool, input map[string]interface{}) (map[string]interface{}, error) {
	endpoint := tool.Spec.Endpoint
	method := tool.Spec.Method
	if method == "" {
		method = "POST"
	}

	// Preparar request body
	var body io.Reader
	if method != "GET" && input != nil {
		bodyBytes, err := json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal input: %w", err)
		}
		body = bytes.NewReader(bodyBytes)
	}

	// Criar request
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Headers
	req.Header.Set("Content-Type", "application/json")
	for k, v := range tool.Spec.Headers {
		req.Header.Set(k, v)
	}

	// Autenticação
	if tool.Spec.AuthType == "bearer" && tool.Spec.AuthSecret != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tool.Spec.AuthSecret))
	} else if tool.Spec.AuthType == "basic" && tool.Spec.AuthSecret != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Basic %s", tool.Spec.AuthSecret))
	}

	// Executar
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Ler resposta
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Verificar status
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP error: %d - %s", resp.StatusCode, string(respBody))
	}

	// Parsear resposta JSON
	var output map[string]interface{}
	if err := json.Unmarshal(respBody, &output); err != nil {
		// Se não for JSON válido, retornar como string
		output = map[string]interface{}{"response": string(respBody)}
	}

	return output, nil
}

// executeWebhook — executa ferramenta webhook (fire-and-forget)
func (e *Executor) executeWebhook(ctx context.Context, tool *domain.Tool, input map[string]interface{}) (map[string]interface{}, error) {
	webhookURL := tool.Spec.WebhookURL
	if webhookURL == "" {
		return nil, fmt.Errorf("webhook URL not configured")
	}

	// Preparar payload
	payload := map[string]interface{}{
		"input":  input,
		"tool":   tool.Name,
		"time":   time.Now().Unix(),
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Enviar webhook (com timeout curto)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(payloadBytes))
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("webhook failed: %w", err)
	}
	resp.Body.Close()

	return map[string]interface{}{
		"status":  "sent",
		"webhook": webhookURL,
	}, nil
}

// executeScript — executa ferramenta script (simulado - não executa código real)
func (e *Executor) executeScript(ctx context.Context, tool *domain.Tool, input map[string]interface{}) (map[string]interface{}, error) {
	// Em produção, isso executaria em um sandbox (Docker, WebAssembly, etc)
	// Por enquanto, retornamos um erro indicando que precisa de implementação
	return nil, fmt.Errorf("script execution not yet implemented: language=%s", tool.Spec.Language)
}

// executeSQL — executa ferramenta SQL (simulado - retorna erro sem conexão DB)
func (e *Executor) executeSQL(ctx context.Context, tool *domain.Tool, input map[string]interface{}) (map[string]interface{}, error) {
	// Em produção, isso conectaria ao banco de dados e executaria a query
	// Por enquanto, retornamos um erro indicando que precisa de implementação
	return nil, fmt.Errorf("SQL execution not yet implemented: query=%s", tool.Spec.Query)
}
