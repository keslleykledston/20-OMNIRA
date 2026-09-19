package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

// AES256GCMCipher implementa ports.CredentialCipher usando AES-256-GCM.
// Cada credencial é armazenada como [nonce (12 bytes) + ciphertext].
type AES256GCMCipher struct {
	key [32]byte // chave mestra de 32 bytes
}

// NewAES256GCMCipher carrega chave de OMNIRA_CREDENTIALS_KEY (base64, 32 bytes).
// Se inválida, ausente ou tamanho errado, retorna erro (nunca fallback silencioso).
func NewAES256GCMCipher() (*AES256GCMCipher, error) {
	keyB64 := os.Getenv("OMNIRA_CREDENTIALS_KEY")
	if keyB64 == "" {
		return nil, fmt.Errorf("OMNIRA_CREDENTIALS_KEY not set")
	}

	keyBytes, err := base64.StdEncoding.DecodeString(keyB64)
	if err != nil {
		return nil, fmt.Errorf("invalid OMNIRA_CREDENTIALS_KEY base64: %w", err)
	}

	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("OMNIRA_CREDENTIALS_KEY must be 32 bytes, got %d", len(keyBytes))
	}

	var key [32]byte
	copy(key[:], keyBytes)
	return &AES256GCMCipher{key: key}, nil
}

// Encrypt encripta plaintext usando AES-256-GCM, gerando nonce novo a cada chamada.
// Retorna concatenação [nonce (12 bytes) + ciphertext].
// Nonce nunca é reaproveitado entre credenciais.
func (c *AES256GCMCipher) Encrypt(plaintext []byte) ([]byte, error) {
	// Gerar nonce (12 bytes para GCM)
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Criar cipher block
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	// Criar GCM mode
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Encriptar plaintext (Seal retorna ciphertext com auth tag)
	ciphertext := aesgcm.Seal(nil, nonce, plaintext, nil)

	// Retornar [nonce + ciphertext] concatenado
	return append(nonce, ciphertext...), nil
}

// Decrypt descriptografa ciphertext usando nonce extraído do prefixo.
// Esperado que ciphertext tenha pelo menos 12 bytes (nonce) + 16 bytes (auth tag).
func (c *AES256GCMCipher) Decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < 12 {
		return nil, fmt.Errorf("ciphertext too short (must include 12-byte nonce)")
	}

	// Extrair nonce (primeiros 12 bytes) e ciphertext (resto)
	nonce := ciphertext[:12]
	ct := ciphertext[12:]

	// Criar cipher block
	block, err := aes.NewCipher(c.key[:])
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	// Criar GCM mode
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Descriptografar (Open valida auth tag)
	plaintext, err := aesgcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt: %w", err)
	}

	return plaintext, nil
}
