package adapters

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func (e *env) user() uuid.UUID {
	u := uuid.New()
	e.exec(`INSERT INTO users(id,external_subject,email,status) VALUES($1,$2,$3,'active')`, u, u, u.String()+"@invalid")
	e.t.Cleanup(func() { _, _ = e.seed.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, u) })
	return u
}

func (e *env) platformOperator() uuid.UUID {
	u := e.user()
	e.exec(`INSERT INTO platform_operators(user_id, granted_by) VALUES($1, 'test')`, u)
	return u
}

// Enabled is what ContextWindowRunner.WithLiveGate checks on EVERY tick — it must see a toggle made
// through Set without any restart or cache involved.
func TestContextIntelligenceV2FeatureFlagEnabledReflectsTheLatestSet(t *testing.T) {
	e := newEnv(t)
	e.exec(`UPDATE context_intelligence_v2_feature SET enabled = false, updated_by = NULL`)
	repo := NewPostgresContextIntelligenceV2FeatureFlagRepository(e.app)
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
func TestContextIntelligenceV2FeatureFlagSetRefusesANonOperator(t *testing.T) {
	e := newEnv(t)
	repo := NewPostgresContextIntelligenceV2FeatureFlagRepository(e.app)
	notAnOperator := e.user()

	if err := repo.Set(context.Background(), true, notAnOperator); err == nil {
		t.Fatal("set by a non-operator succeeded, want ErrNotPlatformOperator")
	}
}
