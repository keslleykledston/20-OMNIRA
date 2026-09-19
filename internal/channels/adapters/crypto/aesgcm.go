package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

const keySize = 32

// AESGCM cifra credenciais com AES-256-GCM. O nonce é prefixado ao payload.
type AESGCM struct{ aead cipher.AEAD }

func NewAESGCM(key []byte) (*AESGCM, error) {
	if len(key) != keySize {
		return nil, fmt.Errorf("channels crypto: credential key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("channels crypto: create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("channels crypto: create GCM: %w", err)
	}
	return &AESGCM{aead: aead}, nil
}

func (c *AESGCM) Encrypt(plaintext []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, fmt.Errorf("channels crypto: cipher is not initialized")
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("channels crypto: generate nonce: %w", err)
	}
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

func (c *AESGCM) Decrypt(ciphertext []byte) ([]byte, error) {
	if c == nil || c.aead == nil {
		return nil, fmt.Errorf("channels crypto: cipher is not initialized")
	}
	nonceSize := c.aead.NonceSize()
	if len(ciphertext) < nonceSize+c.aead.Overhead() {
		return nil, fmt.Errorf("channels crypto: ciphertext too short")
	}
	plaintext, err := c.aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
	if err != nil {
		return nil, fmt.Errorf("channels crypto: decrypt credential: %w", err)
	}
	return plaintext, nil
}
