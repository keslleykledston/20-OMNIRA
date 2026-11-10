package adapters

import (
	"context"
	"testing"
)

// Enabled is what internal/worker/groupalert checks on EVERY event — it must see a toggle made
// through Set without any restart or cache involved.
func TestSLAAlertFeatureFlagEnabledReflectsTheLatestSet(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE group_sla_alert_feature SET enabled = false, updated_by = NULL`)
	repo := NewPostgresSLAAlertFeatureFlagRepository(e.app)
	op := e.platformOperator()

	if enabled, err := repo.Enabled(context.Background()); err != nil || enabled {
		t.Fatalf("enabled = %v, err = %v, want false", enabled, err)
	}

	if err := repo.Set(context.Background(), true, op); err != nil {
		t.Fatalf("set: %v", err)
	}
	if enabled, err := repo.Enabled(context.Background()); err != nil || !enabled {
		t.Fatalf("enabled = %v, err = %v, want true", enabled, err)
	}

	if err := repo.Set(context.Background(), false, op); err != nil {
		t.Fatalf("set: %v", err)
	}
	if enabled, err := repo.Enabled(context.Background()); err != nil || enabled {
		t.Fatalf("enabled = %v, err = %v, want false", enabled, err)
	}
}

// Set re-checks IsPlatformOperator itself (defense in depth): a caller who is not one is refused
// even if, hypothetically, something upstream failed to gate it.
func TestSLAAlertFeatureFlagSetRefusesANonOperator(t *testing.T) {
	e := newEnv(t)
	repo := NewPostgresSLAAlertFeatureFlagRepository(e.app)
	notAnOperator := e.user()

	if err := repo.Set(context.Background(), true, notAnOperator); err == nil {
		t.Fatal("set by a non-operator succeeded, want ErrNotPlatformOperator")
	}
}
