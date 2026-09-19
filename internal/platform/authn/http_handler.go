package authn

import (
	"crypto/rsa"
	"encoding/json"
	"net/http"
)

// AuthHandler — HTTP handlers para autenticação
type AuthHandler struct {
	privateKey *rsa.PrivateKey
}

// NewAuthHandler — cria novo handler de auth
func NewAuthHandler(privateKey *rsa.PrivateKey) *AuthHandler {
	return &AuthHandler{
		privateKey: privateKey,
	}
}

// MockLogin — endpoint POST /api/v1/auth/login (APENAS PARA TESTES)
func (h *AuthHandler) MockLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req MockLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.Email == "" {
		http.Error(w, "email é obrigatório", http.StatusBadRequest)
		return
	}

	// Gerar token
	resp, err := MockLoginHandler(req.Email, h.privateKey)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HealthCheck — endpoint GET /api/v1/auth/health
func (h *AuthHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"auth":   "mock-jwt-enabled",
	})
}
