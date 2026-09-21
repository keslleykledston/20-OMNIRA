package adapters

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// invitationTTL segue o mesmo padrão de sessionTTL em internal/platform/authn/oidc.go:
// constante no código, não configurável — o projeto não tem convenção de TTL
// por env var, e inventar uma só para isto criaria uma exceção isolada.
const invitationTTL = 72 * time.Hour

// InvitationSender entrega o link de convite. A criação da invitation e a
// entrega são propositalmente independentes: sem isso, "não temos provedor de
// e-mail ainda" viraria desculpa para não ter convite nenhum.
type InvitationSender interface {
	Send(ctx context.Context, email, tenantName, acceptURL string) error
}

// NoopInvitationSender não envia nada. É o sender de produção enquanto não
// existir integração de e-mail real — explícito e nunca loga o link (ver
// devExposeInviteURL para o caminho de dev/lab).
type NoopInvitationSender struct{}

func (NoopInvitationSender) Send(context.Context, string, string, string) error { return nil }

type InvitationsHandler struct {
	pool   *pgxpool.Pool
	audit  auditports.AuditEventRepository
	sender InvitationSender
	// devExposeInviteURL só é true quando o dev auth do servidor está ativo
	// (config.DevAuthActive) — o mesmo par ambiente+flag que já guarda o login
	// de desenvolvimento. Nunca fica true em produção.
	devExposeInviteURL bool
	publicBaseURL      string
}

func NewInvitationsHandler(pool *pgxpool.Pool, audit auditports.AuditEventRepository, sender InvitationSender, devExposeInviteURL bool, publicBaseURL string) *InvitationsHandler {
	if sender == nil {
		sender = NoopInvitationSender{}
	}
	return &InvitationsHandler{pool: pool, audit: audit, sender: sender, devExposeInviteURL: devExposeInviteURL, publicBaseURL: publicBaseURL}
}

type Invitation struct {
	ID         uuid.UUID  `json:"id"`
	Email      string     `json:"email"`
	RoleKey    string     `json:"role_key"`
	RoleName   string     `json:"role_name"`
	Status     string     `json:"status"` // pending | accepted | revoked | expired (derivado)
	CreatedBy  string     `json:"created_by_email"`
	ExpiresAt  time.Time  `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	InviteURL  string     `json:"invite_url,omitempty"` // path relativo (sem host); apenas dev/lab, apenas na resposta de criação
}

// effectiveStatus deriva "expired" de status=pending + expires_at no passado.
// Guardar "expired" como valor persistido geraria uma segunda fonte de
// verdade que precisaria ficar em dia com o relógio.
func effectiveStatus(status string, expiresAt time.Time) string {
	if status == "pending" && time.Now().After(expiresAt) {
		return "expired"
	}
	return status
}

func generateInvitationToken() (raw string, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(sum[:]), nil
}

type CreateInvitationRequest struct {
	Email   string `json:"email"`
	RoleKey string `json:"role_key"`
}

// CreateInvitation — POST /api/v1/tenants/{tenant_id}/team/invitations
//
// Convidar é uma forma de criar Membership, então usa a mesma permission de
// PATCH /team: membership.manage. Não existe invitation.manage separado.
func (h *InvitationsHandler) CreateInvitation(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permissionMembershipManage)
	if err != nil {
		respondAuthzError(w, err)
		return
	}
	var req CreateInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	email := strings.TrimSpace(strings.ToLower(req.Email))
	if email == "" || !strings.Contains(email, "@") {
		http.Error(w, "valid email required", http.StatusBadRequest)
		return
	}
	if !isAssignableRole(req.RoleKey) {
		http.Error(w, "role is not assignable in this tenant", http.StatusUnprocessableEntity)
		return
	}

	raw, hash, err := generateInvitationToken()
	if err != nil {
		http.Error(w, "failed to generate invitation", http.StatusInternalServerError)
		return
	}

	q := platformdb.QuerierFromContext(r.Context(), h.pool)

	// Política de duplicidade: no máximo um pending por (tenant, email). Um
	// novo convite substitui o anterior — revoga e insere na mesma
	// transação — em vez de deixar dois pendentes indistinguíveis, ou de
	// obrigar o admin a revogar manualmente antes de reenviar.
	if _, err := q.Exec(r.Context(), `
		UPDATE membership_invitations SET status='revoked', revoked_at=now(), updated_at=now()
		WHERE tenant_id=$1 AND lower(email)=$2 AND status='pending'`,
		tc.TenantID, email); err != nil {
		http.Error(w, "failed to replace previous invitation", http.StatusInternalServerError)
		return
	}

	var inv Invitation
	var roleID uuid.UUID
	expiresAt := time.Now().UTC().Add(invitationTTL)
	err = q.QueryRow(r.Context(), `
		WITH role AS (SELECT id, name FROM roles WHERE key=$1 AND tenant_id IS NULL)
		INSERT INTO membership_invitations (tenant_id, email, role_id, token_hash, created_by, expires_at)
		SELECT $2, $3, role.id, $4, $5, $6 FROM role
		RETURNING id, (SELECT id FROM role), (SELECT name FROM role), created_at`,
		req.RoleKey, tc.TenantID, email, hash, tc.ActorID, expiresAt,
	).Scan(&inv.ID, &roleID, &inv.RoleName, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "role is not assignable in this tenant", http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		http.Error(w, "failed to create invitation", http.StatusInternalServerError)
		return
	}
	inv.Email = email
	inv.RoleKey = req.RoleKey
	inv.Status = "pending"
	inv.ExpiresAt = expiresAt
	_ = q.QueryRow(r.Context(), `SELECT COALESCE(email,'') FROM users WHERE id=$1`, tc.ActorID).Scan(&inv.CreatedBy)

	h.recordInvitation(r.Context(), tc, auditdomain.ActionInvitationCreated, inv.ID, email, req.RoleKey)

	// Caminho relativo, sem host: publicBaseURL é a URL server-to-server
	// usada em webhooks (aponta para o hostname interno do container, não
	// para o que o navegador de quem clica no link enxerga). O e-mail vai
	// receber isto concatenado ao host correto pelo remetente; em dev, o
	// frontend concatena com o próprio origin ao copiar.
	acceptPath := "/invite/" + raw
	_ = h.sender.Send(r.Context(), email, "", h.publicBaseURL+acceptPath)
	if h.devExposeInviteURL {
		// Único lugar onde o token bruto aparece: a resposta desta chamada,
		// só quando o servidor tem o login de desenvolvimento ativo (mesmo
		// gate de config.DevAuthActive do login). Nunca aparece em list.
		inv.InviteURL = acceptPath
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(inv)
}

// ListInvitations — GET /api/v1/tenants/{tenant_id}/team/invitations
func (h *InvitationsHandler) ListInvitations(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permissionMembershipRead)
	if err != nil {
		respondAuthzError(w, err)
		return
	}
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `
		SELECT i.id, i.email, r.key, r.name, i.status, i.expires_at, i.created_at,
		       i.accepted_at, i.revoked_at, COALESCE(u.email, '')
		FROM membership_invitations i
		JOIN roles r ON r.id = i.role_id
		JOIN users u ON u.id = i.created_by
		WHERE i.tenant_id = $1
		ORDER BY i.created_at DESC`, tc.TenantID)
	if err != nil {
		http.Error(w, "failed to list invitations", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]Invitation, 0)
	for rows.Next() {
		var inv Invitation
		if err := rows.Scan(&inv.ID, &inv.Email, &inv.RoleKey, &inv.RoleName, &inv.Status,
			&inv.ExpiresAt, &inv.CreatedAt, &inv.AcceptedAt, &inv.RevokedAt, &inv.CreatedBy); err != nil {
			http.Error(w, "failed to read invitations", http.StatusInternalServerError)
			return
		}
		inv.Status = effectiveStatus(inv.Status, inv.ExpiresAt)
		items = append(items, inv)
	}
	writeTeamJSON(w, map[string]any{"items": items})
}

// RevokeInvitation — PATCH /api/v1/tenants/{tenant_id}/team/invitations/{invitation_id}
// Único campo aceito: status=revoked. Uma invitation aceita já virou
// Membership; a partir daí quem controla acesso é PATCH /team, não isto.
func (h *InvitationsHandler) RevokeInvitation(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permissionMembershipManage)
	if err != nil {
		respondAuthzError(w, err)
		return
	}
	invitationID, err := uuid.Parse(r.PathValue("invitation_id"))
	if err != nil {
		http.Error(w, "invalid invitation_id", http.StatusBadRequest)
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Status != "revoked" {
		http.Error(w, "only status=revoked is accepted", http.StatusBadRequest)
		return
	}

	var email string
	tag, err := platformdb.QuerierFromContext(r.Context(), h.pool).Exec(r.Context(), `
		UPDATE membership_invitations SET status='revoked', revoked_at=now(), updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND status='pending'`,
		tc.TenantID, invitationID)
	if err != nil {
		http.Error(w, "failed to revoke invitation", http.StatusInternalServerError)
		return
	}
	if tag.RowsAffected() == 0 {
		// Cobre id de outro tenant, id inexistente, e convite que já não está
		// pending — os três respondem 404 igual, sem distinguir qual caso é.
		http.Error(w, "invitation not found", http.StatusNotFound)
		return
	}
	_ = platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(),
		`SELECT email FROM membership_invitations WHERE id=$1`, invitationID).Scan(&email)

	h.recordInvitation(r.Context(), tc, auditdomain.ActionInvitationRevoked, invitationID, email, "")
	w.WriteHeader(http.StatusNoContent)
}

func (h *InvitationsHandler) authorize(r *http.Request, permission string) (*domain.TenantContext, error) {
	tc, err := domain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		return nil, errors.New("tenant context not found")
	}
	var ok bool
	err = platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT EXISTS(
		  SELECT 1 FROM memberships m
		  JOIN role_permissions rp ON rp.role_id = m.role_id
		  WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active' AND rp.permission_key=$3)`,
		tc.TenantID, tc.ActorID, permission).Scan(&ok)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errPermissionDenied
	}
	return tc, nil
}

func (h *InvitationsHandler) recordInvitation(ctx context.Context, tc *domain.TenantContext, action auditdomain.AuditAction, invitationID uuid.UUID, email, roleKey string) {
	if h.audit == nil {
		return
	}
	meta := map[string]interface{}{}
	if roleKey != "" {
		meta["role_key"] = roleKey
	}
	// E-mail é incluído propositalmente: sem ele a trilha de auditoria de um
	// convite não diz para quem foi. Nunca o token ou seu hash.
	if email != "" {
		meta["email"] = email
	}
	_ = h.audit.Store(ctx, &auditdomain.AuditEvent{
		ID: uuid.New(), TenantID: tc.TenantID, ActorID: tc.ActorID, Action: action,
		ResourceType: auditdomain.ResourceMembership, ResourceID: invitationID,
		Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), CausationID: uuid.New(),
		Metadata: meta, CreatedAt: time.Now().UTC(),
	})
}

// ---- Accept flow: sem tenant_id na URL, o tenant vem do próprio convite ----

type invitationStatusResponse struct {
	Status      string `json:"status"` // pending | accepted | revoked | expired | wrong_identity | not_found
	TenantName  string `json:"tenant_name,omitempty"`
	RoleKey     string `json:"role_key,omitempty"`
	RoleName    string `json:"role_name,omitempty"`
	MaskedEmail string `json:"masked_email,omitempty"`
}

func maskEmail(email string) string {
	at := strings.IndexByte(email, '@')
	if at <= 1 {
		return "***" + email[at:]
	}
	return email[:1] + strings.Repeat("*", at-1) + email[at:]
}

// InvitationStatus — GET /api/v1/invitations/{token}/status
//
// Sem tenant_id: quem aceita um convite pode não ter membership em lugar
// nenhum ainda, então a AuthorizationMiddleware normal (que exige membership
// ativa no tenant da URL) nunca deixaria a request chegar. Só exige estar
// autenticado.
func (h *InvitationsHandler) InvitationStatus(w http.ResponseWriter, r *http.Request) {
	principal, err := authn.FromContext(r.Context())
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := r.PathValue("token")
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])

	var resp invitationStatusResponse
	err = platformdb.WithTenantSession(r.Context(), h.pool, principal.UserID, true, func(ctx context.Context) error {
		var email, status, tenantName, roleKey, roleName string
		var expiresAt time.Time
		q := platformdb.QuerierFromContext(ctx, h.pool)
		err := q.QueryRow(ctx, `
			SELECT i.email, i.status, i.expires_at, t.legal_name, r.key, r.name
			FROM membership_invitations i
			JOIN tenants t ON t.id = i.tenant_id
			JOIN roles r ON r.id = i.role_id
			WHERE i.token_hash = $1`, hash).Scan(&email, &status, &expiresAt, &tenantName, &roleKey, &roleName)
		if errors.Is(err, pgx.ErrNoRows) {
			resp.Status = "not_found"
			return nil
		}
		if err != nil {
			return err
		}

		var actorEmail string
		_ = q.QueryRow(ctx, `SELECT COALESCE(email,'') FROM users WHERE id=$1`, principal.UserID).Scan(&actorEmail)
		if !strings.EqualFold(actorEmail, email) {
			// Token existe e é válido, mas para outra pessoa: dizemos isso
			// abertamente, porque só chega aqui quem já tem o token secreto —
			// não há nada a esconder que o segredo já não protegesse.
			resp.Status = "wrong_identity"
			resp.MaskedEmail = maskEmail(email)
			return nil
		}
		resp.Status = effectiveStatus(status, expiresAt)
		resp.TenantName = tenantName
		resp.RoleKey = roleKey
		resp.RoleName = roleName
		resp.MaskedEmail = maskEmail(email)
		return nil
	})
	if err != nil {
		http.Error(w, "failed to read invitation", http.StatusInternalServerError)
		return
	}
	writeTeamJSON(w, resp)
}

// AcceptInvitation — POST /api/v1/invitations/{token}/accept
//
// Roda em sessão de sistema, como ProvisionIdentity: criar a própria
// membership está fora do que RLS permite a uma sessão comum (INSERT em
// memberships exige admin ativo no tenant, que o convidado por definição
// ainda não é). A validação de identidade e de estado do convite é feita
// explicitamente aqui, em Go — a sessão de sistema não tem RLS para
// confiar, tem que ser o código.
func (h *InvitationsHandler) AcceptInvitation(w http.ResponseWriter, r *http.Request) {
	principal, err := authn.FromContext(r.Context())
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	token := r.PathValue("token")
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])

	var tenantID, invitationID uuid.UUID
	err = platformdb.WithTenantSession(r.Context(), h.pool, principal.UserID, true, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, h.pool)

		var email, status, roleKey string
		var roleID uuid.UUID
		var expiresAt time.Time
		err := q.QueryRow(ctx, `
			SELECT i.id, i.tenant_id, i.email, i.status, i.expires_at, i.role_id, r.key
			FROM membership_invitations i JOIN roles r ON r.id = i.role_id
			WHERE i.token_hash = $1 FOR UPDATE OF i`, hash,
		).Scan(&invitationID, &tenantID, &email, &status, &expiresAt, &roleID, &roleKey)
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvitationNotFound
		}
		if err != nil {
			return err
		}

		var actorEmail string
		_ = q.QueryRow(ctx, `SELECT COALESCE(email,'') FROM users WHERE id=$1`, principal.UserID).Scan(&actorEmail)
		if !strings.EqualFold(actorEmail, email) {
			return errInvitationNotFound
		}
		switch effectiveStatus(status, expiresAt) {
		case "accepted":
			return errInvitationAlreadyUsed
		case "revoked":
			return errInvitationRevoked
		case "expired":
			return errInvitationExpired
		}

		if _, err := q.Exec(ctx, `
			UPDATE membership_invitations
			SET status='accepted', accepted_by_user_id=$2, accepted_at=now(), updated_at=now()
			WHERE id=$1`, invitationID, principal.UserID); err != nil {
			return err
		}
		// Reativa/reatribui se já existia uma membership (ex.: revogada
		// antes e convidada de novo), em vez de deixar um estado antigo
		// sobreviver a um convite novo e deliberado.
		if _, err := q.Exec(ctx, `
			INSERT INTO memberships (id, tenant_id, user_id, role_id, status)
			VALUES ($1, $2, $3, $4, 'active')
			ON CONFLICT (tenant_id, user_id)
			DO UPDATE SET role_id = EXCLUDED.role_id, status = 'active', updated_at = now()`,
			uuid.New(), tenantID, principal.UserID, roleID); err != nil {
			return err
		}

		tc := &domain.TenantContext{TenantID: tenantID, ActorID: principal.UserID}
		h.recordInvitation(ctx, tc, auditdomain.ActionInvitationAccepted, invitationID, email, roleKey)
		return nil
	})

	switch {
	case errors.Is(err, errInvitationNotFound):
		http.Error(w, "invitation not found", http.StatusNotFound)
	case errors.Is(err, errInvitationAlreadyUsed):
		http.Error(w, "invitation already accepted", http.StatusConflict)
	case errors.Is(err, errInvitationRevoked):
		http.Error(w, "invitation was revoked", http.StatusConflict)
	case errors.Is(err, errInvitationExpired):
		http.Error(w, "invitation expired", http.StatusGone)
	case err != nil:
		http.Error(w, "failed to accept invitation", http.StatusInternalServerError)
	default:
		writeTeamJSON(w, map[string]any{"tenant_id": tenantID})
	}
}

var (
	errInvitationNotFound    = errors.New("invitation not found")
	errInvitationAlreadyUsed = errors.New("invitation already accepted")
	errInvitationRevoked     = errors.New("invitation revoked")
	errInvitationExpired     = errors.New("invitation expired")
)
