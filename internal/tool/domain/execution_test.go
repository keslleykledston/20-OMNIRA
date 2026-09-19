package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNewExecution(t *testing.T) {
	tenantID := uuid.New()
	toolID := uuid.New()
	requestedBy := uuid.New()
	correlationID := uuid.New()
	input := map[string]interface{}{"key": "value"}

	exec := NewExecution(tenantID, toolID, requestedBy, correlationID, input)

	if exec.TenantID != tenantID {
		t.Errorf("TenantID mismatch")
	}

	if exec.Status != ExecutionStatusPending {
		t.Errorf("New execution should be pending")
	}

	if exec.Input["key"] != "value" {
		t.Errorf("Input should match")
	}
}

func TestExecutionStart(t *testing.T) {
	exec := NewExecution(uuid.New(), uuid.New(), uuid.New(), uuid.New(), map[string]interface{}{})

	exec.Start()

	if exec.Status != ExecutionStatusRunning {
		t.Errorf("Status should be running")
	}
}

func TestExecutionComplete(t *testing.T) {
	exec := NewExecution(uuid.New(), uuid.New(), uuid.New(), uuid.New(), map[string]interface{}{})
	exec.Start()

	time.Sleep(5 * time.Millisecond)

	output := map[string]interface{}{"result": "success"}
	exec.Complete(output)

	if exec.Status != ExecutionStatusCompleted {
		t.Errorf("Status should be completed")
	}

	if exec.Output["result"] != "success" {
		t.Errorf("Output should match")
	}

	if exec.Duration < 5 {
		t.Errorf("Duration should be at least 5ms, got %d", exec.Duration)
	}

	if exec.CompletedAt == nil {
		t.Errorf("CompletedAt should be set")
	}
}

func TestExecutionFail(t *testing.T) {
	exec := NewExecution(uuid.New(), uuid.New(), uuid.New(), uuid.New(), map[string]interface{}{})
	exec.Start()

	exec.Fail("connection timeout")

	if exec.Status != ExecutionStatusFailed {
		t.Errorf("Status should be failed")
	}

	if exec.Error != "connection timeout" {
		t.Errorf("Error message should match")
	}
}

func TestExecutionTimeout(t *testing.T) {
	exec := NewExecution(uuid.New(), uuid.New(), uuid.New(), uuid.New(), map[string]interface{}{})
	exec.Start()

	time.Sleep(10 * time.Millisecond)
	exec.Timeout()

	if exec.Status != ExecutionStatusTimeout {
		t.Errorf("Status should be timeout")
	}

	if exec.Duration < 10 {
		t.Errorf("Duration should be at least 10ms")
	}
}

func TestExecutionCancel(t *testing.T) {
	exec := NewExecution(uuid.New(), uuid.New(), uuid.New(), uuid.New(), map[string]interface{}{})
	exec.Start()

	exec.Cancel()

	if exec.Status != ExecutionStatusCanceled {
		t.Errorf("Status should be canceled")
	}
}

func TestExecutionIsCompleted(t *testing.T) {
	exec := NewExecution(uuid.New(), uuid.New(), uuid.New(), uuid.New(), map[string]interface{}{})

	if exec.IsCompleted() {
		t.Errorf("Pending execution should not be completed")
	}

	exec.Complete(map[string]interface{}{})
	if !exec.IsCompleted() {
		t.Errorf("Completed execution should be completed")
	}

	exec2 := NewExecution(uuid.New(), uuid.New(), uuid.New(), uuid.New(), map[string]interface{}{})
	exec2.Fail("error")
	if !exec2.IsCompleted() {
		t.Errorf("Failed execution should be completed")
	}
}

func TestExecutionIsSuccessful(t *testing.T) {
	exec := NewExecution(uuid.New(), uuid.New(), uuid.New(), uuid.New(), map[string]interface{}{})

	if exec.IsSuccessful() {
		t.Errorf("Pending execution should not be successful")
	}

	exec.Complete(map[string]interface{}{})
	if !exec.IsSuccessful() {
		t.Errorf("Completed execution should be successful")
	}

	exec2 := NewExecution(uuid.New(), uuid.New(), uuid.New(), uuid.New(), map[string]interface{}{})
	exec2.Fail("error")
	if exec2.IsSuccessful() {
		t.Errorf("Failed execution should not be successful")
	}
}
