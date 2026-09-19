package postgres_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"testing"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/adapters/crypto"
	"github.com/omnira/omnira/internal/channels/adapters/postgres"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/platform/db"
)

// setupTestCipherAndStore: cria cipher e CredentialStore para testes.
// Nota: requer DB real e TenantContext configurado no contexto.
func setupTestCipherAndStore(t *testing.T, dbConn *sql.DB) *postgres.PostgresCredentialStore {
	// Gerar e setar OMNIRA_CREDENTIALS_KEY para teste
	key := make([]byte, 32)
	rand.Read(key)
	keyB64 := base64.StdEncoding.EncodeToString(key)
	t.Setenv("OMNIRA_CREDENTIALS_KEY", keyB64)

	cipher, err := crypto.NewAES256GCMCipher()
	if err != nil {
		t.Fatalf("failed to create cipher: %v", err)
	}

	store := postgres.NewPostgresCredentialStore(dbConn, cipher)
	return store
}

// TestPostgresCredentialStore_Store_Resolve_Roundtrip: testa store + resolve.
// NOTA: este teste requer DB real e será executado apenas em ambiente integração.
// Para MVP local sem DB real, pode ser comentado ou mockado.
func TestPostgresCredentialStore_Store_Resolve_Roundtrip(t *testing.T) {
	// Skip se DB não disponível
	dbConn := getTestDB(t)
	if dbConn == nil {
		t.Skip("database not available for integration tests")
	}

	store := setupTestCipherAndStore(t, dbConn)

	tenantID := uuid.New()
	connectionID := uuid.New()
	credential := ports.Credential{
		Fields: map[string]string{
			"access_token": "test_token_abc123",
			"account_id":   "5511999999999",
		},
	}

	// Store
	ctx := createContextWithTenant(context.Background(), tenantID)
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

	// Validar
	if resolved.Fields["access_token"] != credential.Fields["access_token"] {
		t.Errorf("access_token mismatch: got %s, want %s",
			resolved.Fields["access_token"], credential.Fields["access_token"])
	}
	if resolved.Fields["account_id"] != credential.Fields["account_id"] {
		t.Errorf("account_id mismatch: got %s, want %s",
			resolved.Fields["account_id"], credential.Fields["account_id"])
	}
}

func TestPostgresCredentialStore_Resolve_WrongTenantID_NotFound(t *testing.T) {
	dbConn := getTestDB(t)
	if dbConn == nil {
		t.Skip("database not available for integration tests")
	}

	store := setupTestCipherAndStore(t, dbConn)

	tenantA := uuid.New()
	tenantB := uuid.New()
	connectionID := uuid.New()
	credential := ports.Credential{
		Fields: map[string]string{"access_token": "secret_token"},
	}

	// Store em Tenant A
	ctxA := createContextWithTenant(context.Background(), tenantA)
	secretRef, err := store.Store(ctxA, connectionID, credential)
	if err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Tentar Resolve em Tenant B
	ctxB := createContextWithTenant(context.Background(), tenantB)
	_, err = store.Resolve(ctxB, secretRef)
	if err == nil {
		t.Error("Resolve should fail when tenant_id doesn't match (RLS)")
	}
}

func TestPostgresCredentialStore_Rotate_MaintainsSecretRef(t *testing.T) {
	dbConn := getTestDB(t)
	if dbConn == nil {
		t.Skip("database not available for integration tests")
	}

	store := setupTestCipherAndStore(t, dbConn)

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

	// Resolve: deve retornar credencial nova
	resolved, err := store.Resolve(ctx, secretRef)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if resolved.Fields["access_token"] != "rotated_token" {
		t.Errorf("Rotate didn't update credential: got %s, want rotated_token",
			resolved.Fields["access_token"])
	}
}

func TestPostgresCredentialStore_Rotate_WrongTenant_Fails(t *testing.T) {
	dbConn := getTestDB(t)
	if dbConn == nil {
		t.Skip("database not available for integration tests")
	}

	store := setupTestCipherAndStore(t, dbConn)

	tenantA := uuid.New()
	tenantB := uuid.New()
	connectionID := uuid.New()
	credential := ports.Credential{
		Fields: map[string]string{"access_token": "token"},
	}

	ctxA := createContextWithTenant(context.Background(), tenantA)
	secretRef, _ := store.Store(ctxA, connectionID, credential)

	// Tentar Rotate em Tenant B
	ctxB := createContextWithTenant(context.Background(), tenantB)
	newCred := ports.Credential{
		Fields: map[string]string{"access_token": "new_token"},
	}
	err := store.Rotate(ctxB, secretRef, newCred)
	if err == nil {
		t.Error("Rotate should fail when tenant_id doesn't match (RLS)")
	}
}

// Helpers

// getTestDB: retorna conexão DB para testes, ou nil se não disponível.
// Em MVP local sem DB real, retorna nil.
func getTestDB(t *testing.T) *sql.DB {
	// TODO: implementar conexão de teste (docker-compose up postgres, conectar)
	// Por enquanto, retornar nil (tests serão skip'd)
	return nil
}

// createContextWithTenant: cria context com TenantContext injetado.
// Mock para testes; produção usa middleware para setar via context.
func createContextWithTenant(ctx context.Context, tenantID uuid.UUID) context.Context {
	// TODO: usar db.NewTenantContext ou equivalente
	// Por enquanto, usar context.WithValue como exemplo
	return ctx // Placeholder; implementação real requer helper do pacote db
}

// Testes unitários (sem DB) serão adicionados quando cipher for mockado.
