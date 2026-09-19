package authn

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// MockLoginRequest — request para mock login
type MockLoginRequest struct {
	Email string `json:"email"`
}

// MockLoginResponse — response com JWT token
type MockLoginResponse struct {
	Token     string    `json:"token"`
	ExpiresIn int       `json:"expires_in"`
	User      MockUser  `json:"user"`
	Tenant    MockTenant `json:"tenant"`
}

type MockUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

type MockTenant struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// MockLoginHandler — gera JWT para testes (NUNCA use em produção)
func MockLoginHandler(email string, privateKey *rsa.PrivateKey) (*MockLoginResponse, error) {
	// Mapping de emails conhecidos para tenants/users
	knownUsers := map[string]struct {
		userID   string
		tenantID string
		name     string
	}{
		"test@omnira.local": {
			userID:   "22222222-2222-2222-2222-222222222222",
			tenantID: "11111111-1111-1111-1111-111111111111",
			name:     "Test User",
		},
		"admin@omnira.local": {
			userID:   "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			tenantID: "11111111-1111-1111-1111-111111111111",
			name:     "Admin User",
		},
	}

	user, ok := knownUsers[email]
	if !ok {
		return nil, fmt.Errorf("email não encontrado: %s", email)
	}

	// Claims do JWT
	now := time.Now()
	claims := jwt.MapClaims{
		"sub":       email,
		"user_id":   user.userID,
		"tenant_id": user.tenantID,
		"email":     email,
		"iat":       now.Unix(),
		"exp":       now.Add(24 * time.Hour).Unix(),
		"iss":       "omnira-mock",
		"aud":       "omnira-api",
	}

	// Gerar token
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		return nil, err
	}

	return &MockLoginResponse{
		Token:     tokenString,
		ExpiresIn: 86400, // 24 horas
		User: MockUser{
			ID:    user.userID,
			Email: email,
			Name:  user.name,
		},
		Tenant: MockTenant{
			ID:   user.tenantID,
			Name: "Test Company LTDA",
		},
	}, nil
}

// LoadPrivateKey — carrega chave privada RSA (para testes)
func LoadPrivateKey(pemData []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("falha ao decodificar PEM")
	}

	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	return privateKey, nil
}

// GenerateTestRSAKeys — gera par RSA para testes (NÃO usar em produção)
func GenerateTestRSAKeys() (*rsa.PrivateKey, *rsa.PublicKey, error) {
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(`-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA0Z3VS5JJcds3u41ayW2LqAZnvl3zSLlBkJ5VJ7w/TYmQrj4H
3R5CmSJuAd7KL1V+TJ5cI9gIRYZKXkJ4P7YvHT3v8khKKmGYnYqFP3WxJuHqNTJI
pVKbVEO2aFW2RIzH7HkQZCgZjsVULECG6WJd9XqEzYKrFyUcKSqKU2sLyJ2sF5Z4
V3vkLzYpJ3LdRKZqFOA3nHVnpLdF5cQf5QW9qZvHKfL8CKvL1fBqW3wZL9RQOJZK
bqJUVvK3c8U8ZCKq8XvYXX2GKcKKU7JZ3nKVjZGUVzFkHH7X8jLGBZqKVmJ7KYvB
K3mYHh8Xvu5v5UG5cKnJZcvDXZQvQJ7K5wIDAQABAoIBABsLkQp3Dd6+lKS3PJpq
RwYKzKqVDvj5PJnJ2GrH9dXeG5VXF5aN1d9V3d2g8dH3gKl2dUHKLrJgLBQqK2cJ
pSqL5rJeU5QzJ7h5tZKzKHpH7NczqQ8UJJqXKJzJKkVJ5nKX3qVKpVGqF7dZVJpC
L7w5kCU5l5C5qV3zKJ5VRrJ8hJ5cLqVPzQqQ7zVP5Z9v3w7zQVCp5RZ5nJ3j3L3q
5xUjKJVzJZGZ5jVZjZJQzZZl3ZZx5c5rD7vH9B5LJ7T5J5DZNzZ7TJ5L7N9Z7zPp
Z9NzB5vGF5j9P5Y9R5hZVJ5hZx5jZ5rZzJ9j59IB79DJ99IJ19IRB1hRjz1fP1TB
fVVYQAECgYEA7dE8uDfDKJNL5ELvfVQQHf4f4X3PVPdI+E1PrFJ5VLH8LqVvZYyz
i7mPY4R5zZ7F5rn7D3zL7t3L9p7L7n3L/p3L7r3L/r3L7t3L/v3L/v/////
-----END RSA PRIVATE KEY-----`))
	if err != nil {
		return nil, nil, err
	}

	publicKey := &privateKey.PublicKey
	return privateKey, publicKey, nil
}
