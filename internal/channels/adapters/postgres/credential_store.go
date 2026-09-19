package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/channels/ports"
)

// PostgresCredentialStore implementa ports.CredentialStore.
// Persiste credenciais encriptadas em channel_credentials table (tenant-owned, RLS+FORCE).
type PostgresCredentialStore struct {
	db     *sql.DB
	cipher ports.CredentialCipher
}

func NewPostgresCredentialStore(db *sql.DB, cipher ports.CredentialCipher) *PostgresCredentialStore {
	return &PostgresCredentialStore{db: db, cipher: cipher}
}

// Store criptografa e persiste credencial, retorna secretRef (UUID da linha).
// Credencial é serializada como JSON, encriptada com AES-256-GCM, armazenada como [nonce + ciphertext].
// TenantID é resolvido do context (via TenantContext); falha se não disponível.
func (s *PostgresCredentialStore) Store(ctx context.Context, connectionID uuid.UUID, cred ports.Credential) (secretRef string, err error) {
	// 1. Resolver TenantID do context
	tenantID, err := tenantIDFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("TenantContext not available or empty")
	}

	// 2. Serializar credential → JSON
	credsJSON, err := json.Marshal(cred.Fields)
	if err != nil {
		return "", fmt.Errorf("failed to marshal credential: %w", err)
	}

	// 3. Encriptar
	encryptedData, err := s.cipher.Encrypt(credsJSON)
	if err != nil {
		return "", fmt.Errorf("failed to encrypt credential: %w", err)
	}

	// 4. Extrair nonce (primeiros 12 bytes) e ciphertext (resto)
	nonce := encryptedData[:12]
	ciphertext := encryptedData[12:]

	// 5. INSERT em channel_credentials
	var secretUUID uuid.UUID
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO channel_credentials (connection_id, tenant_id, nonce, ciphertext)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id`,
		connectionID, tenantID, nonce, ciphertext,
	).Scan(&secretUUID)
	if err != nil {
		return "", fmt.Errorf("failed to store credential: %w", err)
	}

	return secretUUID.String(), nil
}

// Resolve descriptografa e retorna credencial associada a secretRef.
// Chamado exclusivamente server-side (dentro de provider adapter).
// RLS policy garante que só credentials do tenant atual são acessíveis.
func (s *PostgresCredentialStore) Resolve(ctx context.Context, secretRef string) (ports.Credential, error) {
	// Parse secretRef como UUID
	secretUUID, err := uuid.Parse(secretRef)
	if err != nil {
		return ports.Credential{}, fmt.Errorf("invalid secretRef: %w", err)
	}

	tenantID, err := tenantIDFromContext(ctx)
	if err != nil {
		return ports.Credential{}, fmt.Errorf("TenantContext not available or empty")
	}

	// Query credential (RLS policy + explicit WHERE para defense-in-depth)
	var nonce, ciphertext []byte
	err = s.db.QueryRowContext(ctx,
		`SELECT nonce, ciphertext FROM channel_credentials
		 WHERE id = $1 AND tenant_id = $2`,
		secretUUID, tenantID,
	).Scan(&nonce, &ciphertext)
	if err != nil {
		if err == sql.ErrNoRows {
			return ports.Credential{}, fmt.Errorf("credential not found (RLS or invalid secretRef)")
		}
		return ports.Credential{}, fmt.Errorf("failed to query credential: %w", err)
	}

	// Concatenar nonce + ciphertext para Decrypt
	encryptedData := append(nonce, ciphertext...)

	// Descriptografa
	plaintext, err := s.cipher.Decrypt(encryptedData)
	if err != nil {
		return ports.Credential{}, fmt.Errorf("failed to decrypt credential: %w", err)
	}

	// Deserializar JSON → Credential.Fields
	var fields map[string]string
	err = json.Unmarshal(plaintext, &fields)
	if err != nil {
		return ports.Credential{}, fmt.Errorf("failed to unmarshal credential: %w", err)
	}

	return ports.Credential{Fields: fields}, nil
}

// Rotate substitui credencial associada a secretRef, mantendo o mesmo UUID.
// Permite rotação de tokens sem reescrever ChannelConnection.SecretRef.
func (s *PostgresCredentialStore) Rotate(ctx context.Context, secretRef string, newCred ports.Credential) error {
	secretUUID, err := uuid.Parse(secretRef)
	if err != nil {
		return fmt.Errorf("invalid secretRef: %w", err)
	}

	tenantID, err := tenantIDFromContext(ctx)
	if err != nil {
		return fmt.Errorf("TenantContext not available or empty")
	}

	// Serializar + encriptar nova credencial
	credsJSON, err := json.Marshal(newCred.Fields)
	if err != nil {
		return fmt.Errorf("failed to marshal credential: %w", err)
	}

	encryptedData, err := s.cipher.Encrypt(credsJSON)
	if err != nil {
		return fmt.Errorf("failed to encrypt credential: %w", err)
	}

	nonce := encryptedData[:12]
	ciphertext := encryptedData[12:]

	// UPDATE com RLS protection
	result, err := s.db.ExecContext(ctx,
		`UPDATE channel_credentials
		 SET nonce = $1, ciphertext = $2, updated_at = NOW()
		 WHERE id = $3 AND tenant_id = $4`,
		nonce, ciphertext, secretUUID, tenantID,
	)
	if err != nil {
		return fmt.Errorf("failed to rotate credential: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("credential not found (RLS or invalid secretRef)")
	}

	return nil
}
