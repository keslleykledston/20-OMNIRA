package password

import (
	"crypto/rand"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	BcryptCost = 12
	TempLength = 12
)

// Hash retorna o bcrypt hash de uma senha. Usa cost 12.
func Hash(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), BcryptCost)
	if err != nil {
		return "", fmt.Errorf("failed to hash password: %w", err)
	}
	return string(hash), nil
}

// Verify valida uma senha contra um bcrypt hash. Retorna true se válida.
func Verify(hash, password string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	return err == nil
}

// Generate gera uma senha temporária aleatória segura (12 caracteres alphanumeric + símbolos).
func Generate() (string, error) {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!@#$%&*"
	b := make([]byte, TempLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b), nil
}

// ExpiresAt retorna o tempo de expiração de uma senha temporária (72h no futuro).
func ExpiresAt() time.Time {
	return time.Now().Add(72 * time.Hour)
}
