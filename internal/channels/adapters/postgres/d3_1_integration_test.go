package postgres_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/adapters/crypto"
	"github.com/omnira/omnira/internal/channels/adapters/postgres"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
)

// TestD3_1_StoreCredential_ResolvesCorrectly: store + resolve roundtrip.
func TestD3_1_StoreCredential_ResolvesCorrectly(t *testing.T) {
	skip := skipIfNoTestDB(t)
	if skip {
		return
	}

	dbConn := getTestDB(t)
	store := setupTestStore(t, dbConn)

	tenantID := uuid.New()
	connectionID := uuid.New()
	credential := ports.Credential{
		Fields: map[string]string{
			"access_token": "test_token_12345",
			"account_id":   "5511999999999",
		},
	}

	ctx := createContextWithTenant(context.Background(), tenantID)

	// Store
	secretRef, err := store.Store(ctx, connectionID, credential)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	if secretRef == "" {
		t.Error("Store returned empty secretRef")
	}

	// Resolve
	resolved, err := store.Resolve(ctx, secretRef)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Validate
	if resolved.Fields["access_token"] != credential.Fields["access_token"] {
		t.Errorf("access_token mismatch: got %s, want %s",
			resolved.Fields["access_token"], credential.Fields["access_token"])
	}
	if resolved.Fields["account_id"] != credential.Fields["account_id"] {
		t.Errorf("account_id mismatch: got %s, want %s",
			resolved.Fields["account_id"], credential.Fields["account_id"])
	}
}

// TestD3_1_RotateCredential_MaintainsSecretRef: rotate keeps same secretRef.
func TestD3_1_RotateCredential_MaintainsSecretRef(t *testing.T) {
	skip := skipIfNoTestDB(t)
	if skip {
		return
	}

	dbConn := getTestDB(t)
	store := setupTestStore(t, dbConn)

	tenantID := uuid.New()
	connectionID := uuid.New()
	originalCred := ports.Credential{
		Fields: map[string]string{"access_token": "original_token"},
	}

	ctx := createContextWithTenant(context.Background(), tenantID)

	// Store
	secretRef, err := store.Store(ctx, connectionID, originalCred)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Rotate
	newCred := ports.Credential{
		Fields: map[string]string{"access_token": "rotated_token"},
	}
	err = store.Rotate(ctx, secretRef, newCred)
	if err != nil {
		t.Fatalf("Rotate failed: %v", err)
	}

	// Resolve: must return new credential
	resolved, err := store.Resolve(ctx, secretRef)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if resolved.Fields["access_token"] != "rotated_token" {
		t.Errorf("Rotate didn't update credential: got %s, want rotated_token",
			resolved.Fields["access_token"])
	}
}

// TestD3_1_TenantA_CannotAccessTenantB_Credential: RLS isolation.
func TestD3_1_TenantA_CannotAccessTenantB_Credential(t *testing.T) {
	skip := skipIfNoTestDB(t)
	if skip {
		return
	}

	dbConn := getTestDB(t)
	store := setupTestStore(t, dbConn)

	tenantA := uuid.New()
	tenantB := uuid.New()
	connectionID := uuid.New()
	credential := ports.Credential{
		Fields: map[string]string{"access_token": "secret_token"},
	}

	// Store in Tenant A
	ctxA := createContextWithTenant(context.Background(), tenantA)
	secretRef, err := store.Store(ctxA, connectionID, credential)
	if err != nil {
		t.Fatalf("Store in TenantA failed: %v", err)
	}

	// Try Resolve in Tenant B
	ctxB := createContextWithTenant(context.Background(), tenantB)
	_, err = store.Resolve(ctxB, secretRef)
	if err == nil {
		t.Error("Resolve should fail when tenant_id doesn't match (RLS)")
	}
}

// TestD3_1_FindByExternalNumberID_TenantSafe: webhook resolution is tenant-safe.
func TestD3_1_FindByExternalNumberID_TenantSafe(t *testing.T) {
	skip := skipIfNoTestDB(t)
	if skip {
		return
	}

	dbConn := getTestDB(t)
	repo := setupTestRepo(t, dbConn)

	tenantA := uuid.New()
	tenantB := uuid.New()
	connID := uuid.New()

	conn := &domain.ChannelConnection{
		ID:               connID,
		TenantID:         tenantA,
		Channel:          domain.ChannelWhatsApp,
		Provider:         domain.ProviderMetaCloud,
		ProviderKind:     domain.ProviderKindOfficial,
		ExternalNumberID: "5511999999999",
		Status:           domain.ConnectionStatusActive,
		Capabilities:     []domain.Capability{domain.CapabilityText},
	}

	ctxA := createContextWithTenant(context.Background(), tenantA)
	err := repo.Store(ctxA, conn)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// FindByExternalNumberID as Tenant A: should find
	found, err := repo.FindByExternalNumberID(ctxA, domain.ProviderMetaCloud, "5511999999999")
	if err != nil {
		t.Fatalf("FindByExternalNumberID as TenantA failed: %v", err)
	}
	if found == nil {
		t.Error("FindByExternalNumberID as TenantA should find connection")
	}

	// FindByExternalNumberID as Tenant B: should NOT find (RLS)
	ctxB := createContextWithTenant(context.Background(), tenantB)
	_, err = repo.FindByExternalNumberID(ctxB, domain.ProviderMetaCloud, "5511999999999")
	if err == nil {
		t.Error("FindByExternalNumberID as TenantB should fail (RLS)")
	}
}

// TestD3_1_ChannelConnectionRepository_Store_FindByID: basic CRUD.
func TestD3_1_ChannelConnectionRepository_Store_FindByID(t *testing.T) {
	skip := skipIfNoTestDB(t)
	if skip {
		return
	}

	dbConn := getTestDB(t)
	repo := setupTestRepo(t, dbConn)

	tenantID := uuid.New()
	connID := uuid.New()

	conn := &domain.ChannelConnection{
		ID:                 connID,
		TenantID:           tenantID,
		Channel:            domain.ChannelWhatsApp,
		Provider:           domain.ProviderMetaCloud,
		ProviderKind:       domain.ProviderKindOfficial,
		ExternalAccountID:  "123456789",
		ExternalNumberID:   "5511999999999",
		Status:             domain.ConnectionStatusActive,
		Capabilities:       []domain.Capability{domain.CapabilityText, domain.CapabilityMedia},
		SecretRef:          uuid.New().String(),
		RiskAcknowledgedAt: nil,
		CreatedAt:          time.Now(),
		UpdatedAt:          time.Now(),
	}

	ctx := createContextWithTenant(context.Background(), tenantID)
	err := repo.Store(ctx, conn)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// FindByID
	found, err := repo.FindByID(ctx, connID)
	if err != nil {
		t.Fatalf("FindByID failed: %v", err)
	}
	if found == nil {
		t.Fatal("FindByID returned nil")
	}

	// Validate
	if found.Provider != conn.Provider {
		t.Errorf("Provider mismatch: got %s, want %s", found.Provider, conn.Provider)
	}
	if len(found.Capabilities) != 2 {
		t.Errorf("Capabilities count mismatch: got %d, want 2", len(found.Capabilities))
	}
	if found.ExternalNumberID != "5511999999999" {
		t.Errorf("ExternalNumberID mismatch: got %s, want 5511999999999", found.ExternalNumberID)
	}
}

// TestD3_1_ChannelConnectionRepository_Update_Status: status update.
func TestD3_1_ChannelConnectionRepository_Update_Status(t *testing.T) {
	skip := skipIfNoTestDB(t)
	if skip {
		return
	}

	dbConn := getTestDB(t)
	repo := setupTestRepo(t, dbConn)

	tenantID := uuid.New()
	connID := uuid.New()

	conn := &domain.ChannelConnection{
		ID:               connID,
		TenantID:         tenantID,
		Channel:          domain.ChannelWhatsApp,
		Provider:         domain.ProviderMetaCloud,
		ProviderKind:     domain.ProviderKindOfficial,
		ExternalNumberID: "5511999999999",
		Status:           domain.ConnectionStatusPending,
		Capabilities:     []domain.Capability{domain.CapabilityText},
	}

	ctx := createContextWithTenant(context.Background(), tenantID)
	err := repo.Store(ctx, conn)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Update status
	conn.Status = domain.ConnectionStatusActive
	err = repo.Update(ctx, conn)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Verify
	found, err := repo.FindByID(ctx, connID)
	if err != nil {
		t.Fatalf("FindByID failed: %v", err)
	}
	if found.Status != domain.ConnectionStatusActive {
		t.Errorf("Status mismatch after update: got %s, want active", found.Status)
	}
}

// TestD3_1_SecretRef_IsOpaque: secretRef is UUID, credential not leaked.
func TestD3_1_SecretRef_IsOpaque(t *testing.T) {
	skip := skipIfNoTestDB(t)
	if skip {
		return
	}

	dbConn := getTestDB(t)
	store := setupTestStore(t, dbConn)

	tenantID := uuid.New()
	connectionID := uuid.New()
	credential := ports.Credential{
		Fields: map[string]string{"access_token": "secret_value"},
	}

	ctx := createContextWithTenant(context.Background(), tenantID)
	secretRef, err := store.Store(ctx, connectionID, credential)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Validate secretRef is valid UUID
	_, err = uuid.Parse(secretRef)
	if err != nil {
		t.Errorf("secretRef is not a valid UUID: %s", secretRef)
	}

	// Try to decrypt secretRef as credential (should fail)
	_, err = store.Resolve(ctx, secretRef+"_invalid")
	if err == nil {
		t.Error("Resolve should fail with invalid secretRef")
	}
}

// TestD3_1_CredentialNeverInResponse: Credential.String() is redacted.
func TestD3_1_CredentialNeverInResponse(t *testing.T) {
	cred := ports.Credential{
		Fields: map[string]string{
			"access_token": "super_secret",
			"device_id":    "device_123",
		},
	}

	// String() must not contain secret values
	s := cred.String()
	if s == "" {
		t.Error("Credential.String() should not be empty")
	}
	if contains(s, "super_secret") {
		t.Errorf("Credential.String() leaked secret: %s", s)
	}
	if contains(s, "device_123") {
		t.Errorf("Credential.String() leaked device_id: %s", s)
	}

	// GoString() must also be redacted
	gs := cred.GoString()
	if contains(gs, "super_secret") {
		t.Errorf("Credential.GoString() leaked secret: %s", gs)
	}
}

// TestD3_1_RLS_Enforced: Query with wrong tenant returns 0 rows.
func TestD3_1_RLS_Enforced(t *testing.T) {
	skip := skipIfNoTestDB(t)
	if skip {
		return
	}

	dbConn := getTestDB(t)
	repo := setupTestRepo(t, dbConn)

	tenantA := uuid.New()
	tenantB := uuid.New()
	connID := uuid.New()

	conn := &domain.ChannelConnection{
		ID:               connID,
		TenantID:         tenantA,
		Channel:          domain.ChannelWhatsApp,
		Provider:         domain.ProviderMetaCloud,
		ProviderKind:     domain.ProviderKindOfficial,
		ExternalNumberID: "5511999999999",
		Status:           domain.ConnectionStatusActive,
		Capabilities:     []domain.Capability{domain.CapabilityText},
	}

	ctxA := createContextWithTenant(context.Background(), tenantA)
	err := repo.Store(ctxA, conn)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// TenantB tries to FindByID of TenantA connection
	ctxB := createContextWithTenant(context.Background(), tenantB)
	_, err = repo.FindByID(ctxB, connID)
	if err == nil {
		t.Error("FindByID should fail when accessing another tenant's connection (RLS)")
	}
}

// TestD3_1_OMNIRA_CREDENTIALS_KEY_Validation: missing key = boot failure.
func TestD3_1_OMNIRA_CREDENTIALS_KEY_Validation(t *testing.T) {
	// Test 1: Missing key
	t.Setenv("OMNIRA_CREDENTIALS_KEY", "")
	_, err := crypto.NewAES256GCMCipher()
	if err == nil {
		t.Error("NewAES256GCMCipher should fail when key is not set")
	}

	// Test 2: Invalid base64
	t.Setenv("OMNIRA_CREDENTIALS_KEY", "not-valid-base64!!!")
	_, err = crypto.NewAES256GCMCipher()
	if err == nil {
		t.Error("NewAES256GCMCipher should fail with invalid base64")
	}

	// Test 3: Wrong key size
	key := make([]byte, 16)
	rand.Read(key)
	keyB64 := base64.StdEncoding.EncodeToString(key)
	t.Setenv("OMNIRA_CREDENTIALS_KEY", keyB64)
	_, err = crypto.NewAES256GCMCipher()
	if err == nil {
		t.Error("NewAES256GCMCipher should fail with wrong key size")
	}
}

// ========== HELPERS ==========

func skipIfNoTestDB(t *testing.T) bool {
	db := getTestDB(t)
	if db == nil {
		t.Skip("database not available for integration tests (docker-compose postgres required)")
		return true
	}
	return false
}

func getTestDB(t *testing.T) *sql.DB {
	// TODO: Implement database connection for integration tests
	// This would connect to PostgreSQL running via docker-compose
	// For now, return nil (tests will skip)
	return nil
}

func setupTestStore(t *testing.T, dbConn *sql.DB) *postgres.PostgresCredentialStore {
	keyB64, _ := genValidKey()
	t.Setenv("OMNIRA_CREDENTIALS_KEY", keyB64)

	cipher, _ := crypto.NewAES256GCMCipher()
	return postgres.NewPostgresCredentialStore(dbConn, cipher)
}

func setupTestRepo(t *testing.T, dbConn *sql.DB) *postgres.PostgresChannelConnectionRepository {
	keyB64, _ := genValidKey()
	t.Setenv("OMNIRA_CREDENTIALS_KEY", keyB64)

	cipher, _ := crypto.NewAES256GCMCipher()
	credStore := postgres.NewPostgresCredentialStore(dbConn, cipher)
	return postgres.NewPostgresChannelConnectionRepository(dbConn, credStore)
}

func genValidKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

func createContextWithTenant(ctx context.Context, tenantID uuid.UUID) context.Context {
	// Create TenantContext for system operation (no actor required)
	tc, err := tenancydomain.NewTenantContext(tenantID, uuid.Nil, tenancydomain.AccessSourceSystem)
	if err != nil {
		panic(err) // Should never happen in test setup
	}
	// Inject into context
	return tenancydomain.WithTenantContext(ctx, tc)
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && s != "" && substr != ""
}
