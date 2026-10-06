package authn

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/password"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PasswordHandler gerencia endpoints de reset/mudança de senha
type PasswordHandler struct {
	pool        *pgxpool.Pool
	emailSender EmailSender // pode ser nil se não houver SMTP configurado
}

// EmailSender para reset de senha
type EmailSender interface {
	SendPasswordReset(ctx context.Context, email, resetLink string, expiresAt time.Time) error
}

func NewPasswordHandler(pool *pgxpool.Pool, emailSender EmailSender) *PasswordHandler {
	return &PasswordHandler{
		pool:        pool,
		emailSender: emailSender,
	}
}

// PasswordResetRequestReq — POST /api/v1/auth/password-reset-request
type PasswordResetRequestReq struct {
	Email string `json:"email"`
}

// PasswordResetRequest gera token de reset e envia email
// POST /api/v1/auth/password-reset-request { "email": "..." }
func (h *PasswordHandler) PasswordResetRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PasswordResetRequestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.Email == "" {
		writeJSONError(w, "email is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Buscar user pelo email
	var userID uuid.UUID
	err := h.pool.QueryRow(ctx, `SELECT id FROM users WHERE lower(email)=lower($1)`, req.Email).Scan(&userID)
	if err == pgx.ErrNoRows {
		// Por segurança, não revelar se o email existe
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"message": "Se o e-mail existe, um link de reset foi enviado"})
		return
	}
	if err != nil {
		writeJSONError(w, "database error", http.StatusInternalServerError)
		return
	}

	// Gerar token de reset (similar ao token de convite)
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		writeJSONError(w, "failed to generate reset token", http.StatusInternalServerError)
		return
	}
	rawToken := hex.EncodeToString(tokenBytes)
	tokenHash := sha256.Sum256([]byte(rawToken))
	tokenHashHex := hex.EncodeToString(tokenHash[:])

	expiresAt := time.Now().Add(1 * time.Hour) // 1h válido

	// Guardar token no banco
	if _, err := h.pool.Exec(ctx, `
		INSERT INTO password_resets (user_id, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4)
	`, userID, tokenHashHex, time.Now(), expiresAt); err != nil {
		writeJSONError(w, "failed to create reset token", http.StatusInternalServerError)
		return
	}

	// Enviar email (se configurado)
	if h.emailSender != nil {
		resetLink := "/reset-password?token=" + rawToken // Frontend monta o link completo
		_ = h.emailSender.SendPasswordReset(ctx, req.Email, resetLink, expiresAt)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Se o e-mail existe, um link de reset foi enviado"})
}

// PasswordResetReq — POST /api/v1/auth/password-reset
type PasswordResetReq struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// PasswordReset valida token e reseta a senha
// POST /api/v1/auth/password-reset { "token": "...", "password": "..." }
func (h *PasswordHandler) PasswordReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req PasswordResetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.Token == "" || req.Password == "" {
		writeJSONError(w, "token and password are required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Validar token
	tokenHash := sha256.Sum256([]byte(req.Token))
	tokenHashHex := hex.EncodeToString(tokenHash[:])

	var userID uuid.UUID
	var usedAt *time.Time
	err := h.pool.QueryRow(ctx, `
		SELECT user_id, used_at FROM password_resets
		WHERE token_hash=$1 AND expires_at > NOW()
	`, tokenHashHex).Scan(&userID, &usedAt)

	if err == pgx.ErrNoRows {
		writeJSONError(w, "invalid or expired token", http.StatusUnauthorized)
		return
	}
	if err != nil {
		writeJSONError(w, "database error", http.StatusInternalServerError)
		return
	}

	// Verificar se já foi usado
	if usedAt != nil {
		writeJSONError(w, "token already used", http.StatusUnauthorized)
		return
	}

	// Hash a nova senha
	passwordHash, err := password.Hash(req.Password)
	if err != nil {
		writeJSONError(w, "failed to hash password", http.StatusInternalServerError)
		return
	}

	// Atualizar users e marcar token como usado
	if _, err := h.pool.Exec(ctx, `
		UPDATE users SET password_hash=$2, password_set_at=NOW(), password_expires_at=NULL, updated_at=NOW()
		WHERE id=$1
	`, userID, passwordHash); err != nil {
		writeJSONError(w, "failed to reset password", http.StatusInternalServerError)
		return
	}

	if _, err := h.pool.Exec(ctx, `
		UPDATE password_resets SET used_at=NOW() WHERE token_hash=$1
	`, tokenHashHex); err != nil {
		// Não é critical, log apenas
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Senha resetada com sucesso. Faça login com a nova senha."})
}

// PasswordChangeReq — POST /api/v1/auth/password-change
type PasswordChangeReq struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

// PasswordChange troca senha do usuário logado
// POST /api/v1/auth/password-change { "old_password": "...", "new_password": "..." }
// Requer autenticação (cookie de sessão)
func (h *PasswordHandler) PasswordChange(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validar autenticação
	principal, err := FromContext(r.Context())
	if err != nil {
		writeJSONError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req PasswordChangeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.NewPassword == "" {
		writeJSONError(w, "new_password is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Buscar hash atual se houver senha anterior (se password_expires_at, não precisa de old_password)
	var currentHash *string
	var passwordExpiresAt *time.Time
	err = h.pool.QueryRow(ctx, `
		SELECT password_hash, password_expires_at FROM users WHERE id=$1
	`, principal.UserID).Scan(&currentHash, &passwordExpiresAt)
	if err != nil && err != pgx.ErrNoRows {
		writeJSONError(w, "database error", http.StatusInternalServerError)
		return
	}

	// Se não está em troca obrigatória (password_expires_at IS NOT NULL), validar old_password
	if passwordExpiresAt == nil || time.Now().After(*passwordExpiresAt) {
		// Senha não está expirada, validar old_password
		if req.OldPassword == "" {
			writeJSONError(w, "old_password is required when password has not expired", http.StatusBadRequest)
			return
		}
		if currentHash == nil || !password.Verify(*currentHash, req.OldPassword) {
			writeJSONError(w, "old password is incorrect", http.StatusUnauthorized)
			return
		}
	}

	// Hash a nova senha
	newHash, err := password.Hash(req.NewPassword)
	if err != nil {
		writeJSONError(w, "failed to hash password", http.StatusInternalServerError)
		return
	}

	// Atualizar user: nova senha, limpar flag de expiração
	if _, err := h.pool.Exec(ctx, `
		UPDATE users SET password_hash=$2, password_set_at=NOW(), password_expires_at=NULL, updated_at=NOW()
		WHERE id=$1
	`, principal.UserID, newHash); err != nil {
		writeJSONError(w, "failed to change password", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Senha alterada com sucesso"})
}
