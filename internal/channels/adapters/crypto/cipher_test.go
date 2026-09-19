package crypto_test

import (
	"crypto/rand"
	"encoding/base64"
	"os"
	"testing"

	"github.com/omnira/omnira/internal/channels/adapters/crypto"
)

// Helper: gera OMNIRA_CREDENTIALS_KEY válida (base64, 32 bytes)
func genValidKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// Helper: setup test environment com chave válida
func setupCipher(t *testing.T) *crypto.AES256GCMCipher {
	keyB64, err := genValidKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	t.Setenv("OMNIRA_CREDENTIALS_KEY", keyB64)

	cipher, err := crypto.NewAES256GCMCipher()
	if err != nil {
		t.Fatalf("failed to create cipher: %v", err)
	}

	return cipher
}

func TestAES256GCMCipher_Encrypt_Decrypt_Roundtrip(t *testing.T) {
	cipher := setupCipher(t)

	plaintext := []byte(`{"access_token":"test_token_12345"}`)

	// Encriptar
	encrypted, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Descriptografar
	decrypted, err := cipher.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	// Validar
	if string(decrypted) != string(plaintext) {
		t.Errorf("roundtrip failed: got %s, want %s", string(decrypted), string(plaintext))
	}
}

func TestAES256GCMCipher_Encrypt_GeneratesFreshNonce(t *testing.T) {
	cipher := setupCipher(t)

	plaintext := []byte("same plaintext")
	nonces := make(map[string]bool)

	// Encriptar 100 vezes
	for i := 0; i < 100; i++ {
		encrypted, err := cipher.Encrypt(plaintext)
		if err != nil {
			t.Fatalf("Encrypt failed: %v", err)
		}

		// Extrair nonce (primeiros 12 bytes)
		nonce := encrypted[:12]
		nonceStr := string(nonce)

		// Verificar que nonce é único
		if nonces[nonceStr] {
			t.Errorf("nonce reused at iteration %d", i)
		}
		nonces[nonceStr] = true
	}

	// Verificar que temos 100 nonces únicos
	if len(nonces) != 100 {
		t.Errorf("expected 100 unique nonces, got %d", len(nonces))
	}
}

func TestAES256GCMCipher_Decrypt_WrongKey_Fails(t *testing.T) {
	cipher := setupCipher(t)

	plaintext := []byte("sensitive data")
	encrypted, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Criar cipher com chave diferente
	keyB64, err := genValidKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	t.Setenv("OMNIRA_CREDENTIALS_KEY", keyB64)

	wrongCipher, err := crypto.NewAES256GCMCipher()
	if err != nil {
		t.Fatalf("failed to create cipher: %v", err)
	}

	// Tentar descriptografar com chave errada
	_, err = wrongCipher.Decrypt(encrypted)
	if err == nil {
		t.Error("expected Decrypt to fail with wrong key")
	}
}

func TestAES256GCMCipher_Decrypt_TamperedCiphertext_Fails(t *testing.T) {
	cipher := setupCipher(t)

	plaintext := []byte("tamper test")
	encrypted, err := cipher.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	// Tampar último byte do ciphertext
	encrypted[len(encrypted)-1] ^= 0xFF

	// Tentar descriptografar ciphertext tampered
	_, err = cipher.Decrypt(encrypted)
	if err == nil {
		t.Error("expected Decrypt to fail with tampered ciphertext")
	}
}

func TestNewAES256GCMCipher_MissingKey_Fails(t *testing.T) {
	// Remover variável de ambiente
	t.Setenv("OMNIRA_CREDENTIALS_KEY", "")

	_, err := crypto.NewAES256GCMCipher()
	if err == nil {
		t.Error("expected NewAES256GCMCipher to fail when OMNIRA_CREDENTIALS_KEY is not set")
	}
}

func TestNewAES256GCMCipher_InvalidBase64_Fails(t *testing.T) {
	// Base64 inválido
	t.Setenv("OMNIRA_CREDENTIALS_KEY", "not-valid-base64!!!")

	_, err := crypto.NewAES256GCMCipher()
	if err == nil {
		t.Error("expected NewAES256GCMCipher to fail with invalid base64")
	}
}

func TestNewAES256GCMCipher_WrongKeySize_Fails(t *testing.T) {
	// Chave de 16 bytes (não 32)
	key := make([]byte, 16)
	rand.Read(key)
	keyB64 := base64.StdEncoding.EncodeToString(key)
	t.Setenv("OMNIRA_CREDENTIALS_KEY", keyB64)

	_, err := crypto.NewAES256GCMCipher()
	if err == nil {
		t.Error("expected NewAES256GCMCipher to fail with wrong key size")
	}
}

func TestEncrypt_WithEmptyPlaintext(t *testing.T) {
	cipher := setupCipher(t)

	encrypted, err := cipher.Encrypt([]byte{})
	if err != nil {
		t.Fatalf("Encrypt failed for empty plaintext: %v", err)
	}

	decrypted, err := cipher.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}

	if len(decrypted) != 0 {
		t.Errorf("expected empty plaintext, got %d bytes", len(decrypted))
	}
}

func TestDecrypt_WithShortCiphertext_Fails(t *testing.T) {
	cipher := setupCipher(t)

	// Ciphertext muito curto (< 12 bytes nonce)
	_, err := cipher.Decrypt([]byte("short"))
	if err == nil {
		t.Error("expected Decrypt to fail with short ciphertext")
	}
}
