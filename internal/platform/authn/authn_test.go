package authn

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestJWTVerify(t *testing.T) {
	// Gerar chave RSA para testes
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	auth := NewJWTAuthenticator(&privateKey.PublicKey, "test-issuer", "test-audience")

	// Criar token válido
	claims := &Claims{
		Subject: "test-user",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:   "test-issuer",
			Audience: []string{"test-audience"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenString, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	// Test: token válido
	principal, err := auth.Verify(tokenString)
	if err != nil {
		t.Errorf("expected valid token, got error: %v", err)
	}
	if principal == nil {
		t.Error("expected principal, got nil")
	}
	if principal.Subject != "test-user" {
		t.Errorf("expected subject 'test-user', got %s", principal.Subject)
	}

	// Test: token expirado
	expiredClaims := &Claims{
		Subject: "test-user",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:   "test-issuer",
			Audience: []string{"test-audience"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
		},
	}
	expiredToken := jwt.NewWithClaims(jwt.SigningMethodRS256, expiredClaims)
	expiredTokenString, _ := expiredToken.SignedString(privateKey)

	_, err = auth.Verify(expiredTokenString)
	if err == nil {
		t.Error("expected error for expired token")
	}

	// Test: issuer inválido
	wrongIssuerClaims := &Claims{
		Subject: "test-user",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:   "wrong-issuer",
			Audience: []string{"test-audience"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	wrongIssuerToken := jwt.NewWithClaims(jwt.SigningMethodRS256, wrongIssuerClaims)
	wrongIssuerTokenString, _ := wrongIssuerToken.SignedString(privateKey)

	_, err = auth.Verify(wrongIssuerTokenString)
	if err == nil {
		t.Error("expected error for wrong issuer")
	}
}

func TestContextPrincipal(t *testing.T) {
	ctx := context.Background()
	principal := &Principal{
		UserID:  uuid.New(),
		Subject: "test-subject",
	}

	// Armazenar no context
	ctx = WithPrincipal(ctx, principal)

	// Recuperar do context
	retrieved, err := FromContext(ctx)
	if err != nil {
		t.Fatalf("expected to retrieve principal, got error: %v", err)
	}
	if retrieved.UserID != principal.UserID {
		t.Errorf("expected UserID %s, got %s", principal.UserID, retrieved.UserID)
	}

	// Test: sem principal no context
	emptyCtx := context.Background()
	_, err = FromContext(emptyCtx)
	if err == nil {
		t.Error("expected error when principal not in context")
	}
}

func TestMiddleware(t *testing.T) {
	// Setup
	privateKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	auth := NewJWTAuthenticator(&privateKey.PublicKey, "test-issuer", "test-audience")

	// Handler que lê Principal do context
	handler := Middleware(auth)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := FromContext(r.Context())
		if err != nil {
			http.Error(w, "no principal", http.StatusInternalServerError)
			return
		}
		w.Header().Set("X-Subject", principal.Subject)
		w.WriteHeader(http.StatusOK)
	}))

	// Token válido
	claims := &Claims{
		Subject: "test-user",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:   "test-issuer",
			Audience: []string{"test-audience"},
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenString, _ := token.SignedString(privateKey)

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}
	if w.Header().Get("X-Subject") != "test-user" {
		t.Errorf("expected X-Subject 'test-user', got %s", w.Header().Get("X-Subject"))
	}

	// Test: sem token
	req2 := httptest.NewRequest("GET", "/", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusUnauthorized {
		t.Errorf("expected status 401, got %d", w2.Code)
	}
}
