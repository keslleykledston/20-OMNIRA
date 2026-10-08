package domain

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestNewTenantContext(t *testing.T) {
	userID := uuid.New()
	tenantID := uuid.New()

	tests := []struct {
		name      string
		tenantID  uuid.UUID
		actorID   uuid.UUID
		source    AccessSource
		wantErr   bool
		wantSrc   AccessSource
	}{
		{
			name:     "direct access",
			tenantID: tenantID,
			actorID:  userID,
			source:   AccessSourceDirect,
			wantErr:  false,
			wantSrc:  AccessSourceDirect,
		},
		{
			name:     "no source defaults to direct",
			tenantID: tenantID,
			actorID:  userID,
			source:   "",
			wantErr:  false,
			wantSrc:  AccessSourceDirect,
		},
		{
			name:     "system access",
			tenantID: tenantID,
			actorID:  uuid.Nil,
			source:   AccessSourceSystem,
			wantErr:  false,
			wantSrc:  AccessSourceSystem,
		},
		{
			name:     "nil tenant_id fails",
			tenantID: uuid.Nil,
			actorID:  userID,
			source:   AccessSourceDirect,
			wantErr:  true,
		},
		{
			name:     "nil actor_id fails for human access",
			tenantID: tenantID,
			actorID:  uuid.Nil,
			source:   AccessSourceDirect,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, err := NewTenantContext(tt.tenantID, tt.actorID, tt.source)
			if (err != nil) != tt.wantErr {
				t.Errorf("wantErr=%v, got=%v", tt.wantErr, err)
			}
			if err == nil && ctx.Source != tt.wantSrc {
				t.Errorf("wantSrc=%v, got=%v", tt.wantSrc, ctx.Source)
			}
		})
	}
}

func TestNewHubTenantContext(t *testing.T) {
	userID := uuid.New()
	tenantID := uuid.New()
	hubID := uuid.New()
	contractID := uuid.New()
	grantID := uuid.New()
	correlationID := "corr-123"

	tests := []struct {
		name        string
		tenantID    uuid.UUID
		actorID     uuid.UUID
		hubID       uuid.UUID
		contractID  uuid.UUID
		grantID     uuid.UUID
		correlation string
		wantErr     bool
		wantSource  AccessSource
	}{
		{
			name:        "valid hub context",
			tenantID:    tenantID,
			actorID:     userID,
			hubID:       hubID,
			contractID:  contractID,
			grantID:     grantID,
			correlation: correlationID,
			wantErr:     false,
			wantSource:  AccessSourceHub,
		},
		{
			name:       "nil tenant fails",
			tenantID:   uuid.Nil,
			actorID:    userID,
			hubID:      hubID,
			contractID: contractID,
			grantID:    grantID,
			wantErr:    true,
		},
		{
			name:       "nil actor fails",
			tenantID:   tenantID,
			actorID:    uuid.Nil,
			hubID:      hubID,
			contractID: contractID,
			grantID:    grantID,
			wantErr:    true,
		},
		{
			name:       "nil contract fails",
			tenantID:   tenantID,
			actorID:    userID,
			hubID:      hubID,
			contractID: uuid.Nil,
			grantID:    grantID,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, err := NewHubTenantContext(tt.tenantID, tt.actorID, tt.hubID, tt.contractID, tt.grantID, tt.correlation)
			if (err != nil) != tt.wantErr {
				t.Errorf("wantErr=%v, got=%v", tt.wantErr, err)
			}
			if err == nil {
				if ctx.Source != tt.wantSource {
					t.Errorf("wantSource=%v, got=%v", tt.wantSource, ctx.Source)
				}
				if ctx.HubID == nil || *ctx.HubID != tt.hubID {
					t.Errorf("HubID mismatch")
				}
				if ctx.CorrelationID != tt.correlation {
					t.Errorf("CorrelationID mismatch")
				}
			}
		})
	}
}

func TestContextStorage(t *testing.T) {
	userID := uuid.New()
	tenantID := uuid.New()

	ctx, _ := NewTenantContext(tenantID, userID, AccessSourceDirect)

	// Store in go context
	ctxWithTC := WithTenantContext(context.Background(), ctx)

	// Retrieve
	retrieved, err := FromContext(ctxWithTC)
	if err != nil {
		t.Fatalf("FromContext failed: %v", err)
	}
	if retrieved.TenantID != ctx.TenantID || retrieved.ActorID != ctx.ActorID {
		t.Errorf("Context mismatch after storage/retrieval")
	}
}
