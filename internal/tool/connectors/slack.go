package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// SlackConnector — conector para Slack
type SlackConnector struct {
	httpClient *http.Client
	webhookURL string
}

// NewSlackConnector — cria novo SlackConnector
func NewSlackConnector() *SlackConnector {
	return &SlackConnector{
		httpClient: &http.Client{},
	}
}

// Name — retorna nome do conector
func (s *SlackConnector) Name() string {
	return "slack"
}

// Authenticate — autentica no Slack
func (s *SlackConnector) Authenticate(ctx context.Context, credentials map[string]interface{}) error {
	webhookURL, ok := credentials["webhook_url"].(string)
	if !ok || webhookURL == "" {
		return fmt.Errorf("missing or invalid webhook_url")
	}

	s.webhookURL = webhookURL

	// Validar webhook fazendo uma chamada de teste
	payload := map[string]interface{}{
		"text": "OMNIRA Slack connector test",
	}

	payloadBytes, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(payloadBytes))
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook validation failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook validation failed: %d - %s", resp.StatusCode, string(body))
	}

	return nil
}

// Execute — executa ação no Slack
func (s *SlackConnector) Execute(ctx context.Context, action string, params map[string]interface{}) (map[string]interface{}, error) {
	if s.webhookURL == "" {
		return nil, fmt.Errorf("slack connector not authenticated")
	}

	switch action {
	case "send_message":
		return s.sendMessage(ctx, params)
	case "send_rich_message":
		return s.sendRichMessage(ctx, params)
	default:
		return nil, fmt.Errorf("unknown action: %s", action)
	}
}

// GetActions — retorna ações disponíveis
func (s *SlackConnector) GetActions() []ActionSpec {
	return []ActionSpec{
		{
			Name:        "send_message",
			Description: "Enviar mensagem simples para Slack",
			Params: map[string]ParamSpec{
				"text": {
					Type:        "string",
					Description: "Texto da mensagem",
					Required:    true,
					Example:     "Hello Slack!",
				},
			},
			Returns: map[string]interface{}{
				"success": true,
				"status":  "sent",
			},
		},
		{
			Name:        "send_rich_message",
			Description: "Enviar mensagem formatada (JSON blocks)",
			Params: map[string]ParamSpec{
				"blocks": {
					Type:        "array",
					Description: "Array de Slack Block Kit objects",
					Required:    true,
				},
			},
			Returns: map[string]interface{}{
				"success": true,
				"status":  "sent",
			},
		},
	}
}

// sendMessage — envia mensagem simples
func (s *SlackConnector) sendMessage(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	text, ok := params["text"].(string)
	if !ok || text == "" {
		return nil, fmt.Errorf("missing or invalid text parameter")
	}

	payload := map[string]interface{}{
		"text": text,
	}

	payloadBytes, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", s.webhookURL, bytes.NewReader(payloadBytes))
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to send message: %d - %s", resp.StatusCode, string(body))
	}

	return map[string]interface{}{
		"success": true,
		"status":  "sent",
		"message": text,
	}, nil
}

// sendRichMessage — envia mensagem com Block Kit
func (s *SlackConnector) sendRichMessage(ctx context.Context, params map[string]interface{}) (map[string]interface{}, error) {
	blocks, ok := params["blocks"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("missing or invalid blocks parameter")
	}

	payload := map[string]interface{}{
		"blocks": blocks,
	}

	payloadBytes, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", s.webhookURL, bytes.NewReader(payloadBytes))
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send rich message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to send rich message: %d - %s", resp.StatusCode, string(body))
	}

	return map[string]interface{}{
		"success": true,
		"status":  "sent",
		"blocks":  len(blocks),
	}, nil
}
