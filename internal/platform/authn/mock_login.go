package authn

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
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

// GenerateTestRSAKeys — gera par RSA para testes (NÃO usar em produção).
//
// Gerado em runtime (2048 bits) em vez de PEM hardcoded: uma chave estática
// no código-fonte é previsível/reversível e um PEM copiado à mão é frágil
// a corrupção silenciosa (um PEM inválido aqui fazia RegisterAuthHandlers
// cair no fallback silencioso e o login mock responder sempre 503).
func GenerateTestRSAKeys() (*rsa.PrivateKey, *rsa.PublicKey, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	return privateKey, &privateKey.PublicKey, nil
}
