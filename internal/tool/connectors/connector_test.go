package connectors

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegistryRegister(t *testing.T) {
	registry := NewRegistry()
	connector := NewSlackConnector()

	err := registry.Register(connector)
	if err != nil {
		t.Errorf("registration failed: %v", err)
	}

	// Tentar registrar novamente deve falhar
	err = registry.Register(connector)
	if err == nil {
		t.Errorf("should not allow duplicate registration")
	}
}

func TestRegistryGet(t *testing.T) {
	registry := NewRegistry()
	connector := NewSlackConnector()
	registry.Register(connector)

	retrieved, err := registry.Get("slack")
	if err != nil {
		t.Errorf("get failed: %v", err)
	}

	if retrieved.Name() != "slack" {
		t.Errorf("connector name mismatch")
	}

	// Tentar obter conector inexistente
	_, err = registry.Get("invalid")
	if err == nil {
		t.Errorf("should return error for unknown connector")
	}
}

func TestRegistryList(t *testing.T) {
	registry := NewRegistry()
	registry.Register(NewSlackConnector())

	list := registry.List()
	if len(list) != 1 {
		t.Errorf("expected 1 connector, got %d", len(list))
	}

	if list[0] != "slack" {
		t.Errorf("expected slack connector in list")
	}
}

func TestSlackConnectorAuthenticate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	connector := NewSlackConnector()

	// Authenticate com webhook válido
	err := connector.Authenticate(context.Background(), map[string]interface{}{
		"webhook_url": server.URL,
	})
	if err != nil {
		t.Errorf("authentication failed: %v", err)
	}

	// Authenticate sem webhook_url
	err = connector.Authenticate(context.Background(), map[string]interface{}{})
	if err == nil {
		t.Errorf("should fail without webhook_url")
	}
}

func TestSlackConnectorSendMessage(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	connector := NewSlackConnector()
	connector.Authenticate(context.Background(), map[string]interface{}{
		"webhook_url": server.URL,
	})

	output, err := connector.Execute(context.Background(), "send_message", map[string]interface{}{
		"text": "Hello Slack!",
	})

	if err != nil {
		t.Errorf("execute failed: %v", err)
	}

	if !called {
		t.Errorf("webhook was not called")
	}

	if output["status"] != "sent" {
		t.Errorf("expected status=sent")
	}
}

func TestSlackConnectorGetActions(t *testing.T) {
	connector := NewSlackConnector()
	actions := connector.GetActions()

	if len(actions) < 2 {
		t.Errorf("expected at least 2 actions")
	}

	foundSendMessage := false
	foundRichMessage := false

	for _, action := range actions {
		if action.Name == "send_message" {
			foundSendMessage = true
		}
		if action.Name == "send_rich_message" {
			foundRichMessage = true
		}
	}

	if !foundSendMessage || !foundRichMessage {
		t.Errorf("expected both send_message and send_rich_message actions")
	}
}
