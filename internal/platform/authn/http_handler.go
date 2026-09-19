package authn

import (
	"crypto/rsa"
	"encoding/json"
	"net/http"
)

// AuthHandler — HTTP handlers para autenticação
type AuthHandler struct {
	privateKey   *rsa.PrivateKey
	secureCookie bool
}

// NewAuthHandler — cria novo handler de auth
func NewAuthHandler(privateKey *rsa.PrivateKey, secureCookie ...bool) *AuthHandler {
	secure := false
	if len(secureCookie) > 0 {
		secure = secureCookie[0]
	}
	return &AuthHandler{
		privateKey: privateKey, secureCookie: secure,
	}
}

// writeJSONError — resposta de erro em JSON consistente com o restante da API
func writeJSONError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message, "message": message})
}

// MockLogin — endpoint POST /api/v1/auth/login (APENAS PARA TESTES)
func (h *AuthHandler) MockLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req MockLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.Email == "" {
		writeJSONError(w, "email é obrigatório", http.StatusBadRequest)
		return
	}

	// Gerar token
	resp, err := MockLoginHandler(req.Email, h.privateKey)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: resp.Token, Path: "/", MaxAge: resp.ExpiresIn,
		HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteLaxMode})

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
