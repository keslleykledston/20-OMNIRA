package execution

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/tool/domain"
)

func TestExecuteHTTP_Success(t *testing.T) {
	// Mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","result":"success"}`))
	}))
	defer server.Close()

	executor := NewExecutor(10 * time.Second)
	tool := &domain.Tool{
		ID:     uuid.New(),
		Name:   "test-http",
		Type:   domain.ToolTypeHTTP,
		Spec:   domain.ToolSpec{Endpoint: server.URL, Method: "POST"},
		Status: domain.ToolStatusActive,
	}

	input := map[string]interface{}{"key": "value"}
	output, err := executor.Execute(context.Background(), tool, input)

	if err != nil {
		t.Errorf("execution failed: %v", err)
	}

	if output["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", output["status"])
	}
}

func TestExecuteHTTP_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	executor := NewExecutor(10 * time.Second)
	tool := &domain.Tool{
		Type: domain.ToolTypeHTTP,
		Spec: domain.ToolSpec{Endpoint: server.URL, Method: "GET"},
	}

	_, err := executor.Execute(context.Background(), tool, nil)

	if err == nil {
		t.Errorf("expected error for HTTP 500")
	}
}

func TestExecuteHTTP_InvalidEndpoint(t *testing.T) {
	executor := NewExecutor(10 * time.Second)
	tool := &domain.Tool{
		Type: domain.ToolTypeHTTP,
		Spec: domain.ToolSpec{Endpoint: "http://invalid-host-123456789.local", Method: "GET"},
	}

	_, err := executor.Execute(context.Background(), tool, nil)

	if err == nil {
		t.Errorf("expected error for invalid endpoint")
	}
}

func TestExecuteHTTP_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := NewExecutor(50 * time.Millisecond) // Timeout menor que delay do server
	tool := &domain.Tool{
		Type: domain.ToolTypeHTTP,
		Spec: domain.ToolSpec{Endpoint: server.URL, Method: "GET"},
	}

	_, err := executor.Execute(context.Background(), tool, nil)

	if err == nil {
		t.Errorf("expected timeout error")
	}
}

func TestExecuteWebhook_Success(t *testing.T) {
	received := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	executor := NewExecutor(10 * time.Second)
	tool := &domain.Tool{
		Name:   "test-webhook",
		Type:   domain.ToolTypeWebhook,
		Spec:   domain.ToolSpec{WebhookURL: server.URL},
		Status: domain.ToolStatusActive,
	}

	output, err := executor.Execute(context.Background(), tool, map[string]interface{}{"data": "test"})

	if err != nil {
		t.Errorf("webhook execution failed: %v", err)
	}

	if !received {
		t.Errorf("webhook was not called")
	}

	if output["status"] != "sent" {
		t.Errorf("expected status=sent")
	}
}

func TestExecuteWebhook_MissingURL(t *testing.T) {
	executor := NewExecutor(10 * time.Second)
	tool := &domain.Tool{
		Type: domain.ToolTypeWebhook,
		Spec: domain.ToolSpec{}, // No webhook URL
	}

	_, err := executor.Execute(context.Background(), tool, nil)

	if err == nil {
		t.Errorf("expected error for missing webhook URL")
	}
}

func TestExecuteScript_NotImplemented(t *testing.T) {
	executor := NewExecutor(10 * time.Second)
	tool := &domain.Tool{
		Type: domain.ToolTypeScript,
		Spec: domain.ToolSpec{Language: "python", Code: "print('hello')"},
	}

	_, err := executor.Execute(context.Background(), tool, nil)

	if err == nil {
		t.Errorf("expected error for unimplemented script execution")
	}
}

func TestExecuteSQL_NotImplemented(t *testing.T) {
	executor := NewExecutor(10 * time.Second)
	tool := &domain.Tool{
		Type: domain.ToolTypeSQL,
		Spec: domain.ToolSpec{Query: "SELECT * FROM users"},
	}

	_, err := executor.Execute(context.Background(), tool, nil)

	if err == nil {
		t.Errorf("expected error for unimplemented SQL execution")
	}
}

func TestExecuteUnsupportedType(t *testing.T) {
	executor := NewExecutor(10 * time.Second)
	tool := &domain.Tool{
		Type: domain.ToolType("unsupported"),
	}

	_, err := executor.Execute(context.Background(), tool, nil)

	if err == nil {
		t.Errorf("expected error for unsupported tool type")
	}
}
