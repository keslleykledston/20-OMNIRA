package domain

import (
	"testing"

	"github.com/google/uuid"
)

func TestNewTool(t *testing.T) {
	tenantID := uuid.New()
	createdBy := uuid.New()

	spec := ToolSpec{
		Endpoint: "https://api.example.com/action",
		Method:   "POST",
	}

	tool := NewTool(tenantID, createdBy, "my-tool", "A test tool", ToolTypeHTTP, spec)

	if tool.TenantID != tenantID {
		t.Errorf("TenantID mismatch")
	}

	if tool.Name != "my-tool" {
		t.Errorf("Name mismatch")
	}

	if tool.Status != ToolStatusActive {
		t.Errorf("New tool should be active")
	}

	if tool.Version != 1 {
		t.Errorf("New tool should have version 1")
	}
}

func TestToolIsActive(t *testing.T) {
	tool := NewTool(uuid.New(), uuid.New(), "test", "", ToolTypeHTTP, ToolSpec{})

	if !tool.IsActive() {
		t.Errorf("New tool should be active")
	}

	tool.Deactivate(uuid.New())
	if tool.IsActive() {
		t.Errorf("Deactivated tool should not be active")
	}

	tool.Activate(uuid.New())
	if !tool.IsActive() {
		t.Errorf("Activated tool should be active")
	}
}

func TestToolDeactivate(t *testing.T) {
	tool := NewTool(uuid.New(), uuid.New(), "test", "", ToolTypeHTTP, ToolSpec{})
	deactivatedBy := uuid.New()

	tool.Deactivate(deactivatedBy)

	if tool.Status != ToolStatusInactive {
		t.Errorf("Status should be inactive")
	}

	if tool.UpdatedBy != deactivatedBy {
		t.Errorf("UpdatedBy should match")
	}
}

func TestToolMarkDeprecated(t *testing.T) {
	tool := NewTool(uuid.New(), uuid.New(), "test", "", ToolTypeHTTP, ToolSpec{})
	deprecatedBy := uuid.New()

	tool.MarkDeprecated(deprecatedBy)

	if tool.Status != ToolStatusDeprecated {
		t.Errorf("Status should be deprecated")
	}
}

func TestToolUpdate(t *testing.T) {
	tool := NewTool(uuid.New(), uuid.New(), "test", "", ToolTypeHTTP, ToolSpec{})
	initialVersion := tool.Version
	updatedBy := uuid.New()

	newSpec := ToolSpec{
		Endpoint: "https://new-endpoint.com",
		Method:   "GET",
	}

	tool.Update("new-name", "new description", newSpec, updatedBy)

	if tool.Name != "new-name" {
		t.Errorf("Name should be updated")
	}

	if tool.Version != initialVersion+1 {
		t.Errorf("Version should increment")
	}

	if tool.Spec.Endpoint != "https://new-endpoint.com" {
		t.Errorf("Spec should be updated")
	}
}
