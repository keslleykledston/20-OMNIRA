package authn

import (
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// AuthHandler — HTTP handlers para autenticação
type AuthHandler struct {
	privateKey   *rsa.PrivateKey
	secureCookie bool
	sessionStore SessionStore
}

// NewAuthHandler — cria novo handler de auth. sessionStore cria a sessão
// server-side opaca por trás do cookie omnira_session; o JWT continua no
// corpo da resposta (compat: dev tooling que ainda envia Bearer), mas nunca
// mais é ele que vai para o cookie.
func NewAuthHandler(privateKey *rsa.PrivateKey, sessionStore SessionStore, secureCookie ...bool) *AuthHandler {
	secure := false
	if len(secureCookie) > 0 {
		secure = secureCookie[0]
	}
	return &AuthHandler{
		privateKey: privateKey, secureCookie: secure, sessionStore: sessionStore,
	}
}

// writeJSONError — resposta de erro em JSON consistente com o restante da API
func writeJSONError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": message, "message": message})
}

// DevLogin — POST /api/v1/auth/dev/login.
//
// Ferramenta de desenvolvimento, não um mecanismo de autenticação: entra quem
// está na allowlist de mock_login.go, e não há senha porque não existe auth
// local neste produto. Só é registrada quando o ambiente permite e a flag está
// explicitamente ligada (ver config.DevAuthActive).
func (h *AuthHandler) DevLogin(w http.ResponseWriter, r *http.Request) {
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

	// Gerar token (permanece no corpo da resposta por compat com consumidores
	// Bearer reais; nunca mais é o que vai para o cookie — ver sessão abaixo).
	resp, err := MockLoginHandler(req.Email, h.privateKey)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusUnauthorized)
		return
	}

	if h.sessionStore == nil {
		writeJSONError(w, "session store not configured", http.StatusInternalServerError)
		return
	}
	userID, err := uuid.Parse(resp.User.ID)
	if err != nil {
		writeJSONError(w, "invalid dev user id", http.StatusInternalServerError)
		return
	}
	ttl := time.Duration(resp.ExpiresIn) * time.Second
	sessionID, err := h.sessionStore.CreateSession(r.Context(), userID, "dev", ttl)
	if err != nil {
		writeJSONError(w, "session creation error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: sessionID, Path: "/", MaxAge: resp.ExpiresIn,
		HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteLaxMode})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Logout — POST /api/v1/auth/logout (dev/mock mode). Mirrors OIDCHandler.Logout:
// revokes the server-side session and expires the cookie, so a replayed
// cookie fails authentication instead of merely being absent client-side.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookieName); err == nil && h.sessionStore != nil {
		_ = h.sessionStore.RevokeSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteLaxMode})
	w.WriteHeader(http.StatusNoContent)
}

// HealthCheck — endpoint GET /api/v1/auth/health
func (h *AuthHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"auth":   "mock-jwt-enabled",
	})
}
