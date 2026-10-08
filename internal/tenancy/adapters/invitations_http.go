package adapters

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	"github.com/omnira/omnira/internal/password"
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
	Send(ctx context.Context, msg InvitationMessage) error
}

// NoopInvitationSender não envia nada. É o sender de produção enquanto não
// existir integração de e-mail real — explícito e nunca loga o link (ver
// devExposeInviteURL para o caminho de dev/lab).
type NoopInvitationSender struct{}

func (NoopInvitationSender) Send(context.Context, InvitationMessage) error { return nil }

type InvitationsHandler struct {
	pool   *pgxpool.Pool
	audit  auditports.AuditEventRepository
	sender InvitationSender
	// devExposeInviteURL só é true quando o dev auth do servidor está ativo
	// (config.DevAuthActive) — o mesmo par ambiente+flag que já guarda o login
	// de desenvolvimento. Nunca fica true em produção.
	devExposeInviteURL bool
	// webBaseURL é o host que o NAVEGADOR do convidado enxerga (OMNIRA_WEB_BASE_URL),
	// usado só para montar o link do e-mail — não a URL server-to-server dos webhooks.
	webBaseURL string
	// deliveryAvailable é a capability real: existe uma forma de o convidado
	// receber o link. Em dev/lab é o próprio devExposeInviteURL (o admin
	// copia e entrega manualmente); em produção, só quando um sender de
	// verdade está configurado — NoopInvitationSender não conta. Sem isso,
	// CreateInvitation persistiria um convite que ninguém jamais recebe.
	deliveryAvailable bool
}

func NewInvitationsHandler(pool *pgxpool.Pool, audit auditports.AuditEventRepository, sender InvitationSender, devExposeInviteURL bool, webBaseURL string) *InvitationsHandler {
	if sender == nil {
		sender = NoopInvitationSender{}
	}
	return &InvitationsHandler{
		pool: pool, audit: audit, sender: sender, devExposeInviteURL: devExposeInviteURL, webBaseURL: webBaseURL,
		deliveryAvailable: devExposeInviteURL || !isNoopSender(sender),
	}
}

func isNoopSender(s InvitationSender) bool {
	_, ok := s.(NoopInvitationSender)
	return ok
}

// InvitationDeliveryAvailable expõe a mesma capability para outros handlers
// (MyAccess) sem duplicar a regra.
func (h *InvitationsHandler) InvitationDeliveryAvailable() bool { return h.deliveryAvailable }

type Invitation struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	RoleKey   string    `json:"role_key"`
	RoleName  string    `json:"role_name"`
	Status    string    `json:"status"` // pending | accepted | revoked | expired (derivado)
	CreatedBy string    `json:"created_by_email"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	// SentAt: última entrega bem-sucedida do e-mail (nil = ainda não entregue).
	SentAt     *time.Time `json:"sent_at,omitempty"`
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
	// Fail-closed: sem forma de o convidado receber o link, o convite seria
	// dado válido porém inútil. Antes de qualquer escrita — nem o registro de
	// duplicidade nem o insert acontecem.
	if !h.deliveryAvailable {
		http.Error(w, "invitation delivery is not configured", http.StatusServiceUnavailable)
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

	// Gerar senha temporária para primeira autenticação
	tempPassword, err := password.Generate()
	if err != nil {
		http.Error(w, "failed to generate temporary password", http.StatusInternalServerError)
		return
	}

	q := platformdb.QuerierFromContext(r.Context(), h.pool)

	// Quem já é membro ativo não precisa de convite; mudar o papel é PATCH /team
	// (com o invariante do último admin), nunca via convite.
	if h.isActiveMember(r.Context(), q, tc.TenantID, email) {
		http.Error(w, "user is already an active member", http.StatusConflict)
		return
	}
	// ADR-0039: só o administrador do Hub coloca uma pessoa em mais de uma instância.
	if h.worksElsewhere(r.Context(), q, tc.TenantID, email) {
		http.Error(w, errOtherInstanceMessage, http.StatusConflict)
		return
	}

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

	// Hash a senha temporária para validação no accept
	tempPasswordHash, err := password.Hash(tempPassword)
	if err != nil {
		http.Error(w, "failed to hash temporary password", http.StatusInternalServerError)
		return
	}

	var inv Invitation
	var roleID uuid.UUID
	expiresAt := time.Now().UTC().Add(invitationTTL)
	err = q.QueryRow(r.Context(), `
		WITH role AS (SELECT id, name FROM roles WHERE key=$1 AND tenant_id IS NULL)
		INSERT INTO membership_invitations (tenant_id, email, role_id, token_hash, temporary_password_hash, created_by, expires_at)
		SELECT $2, $3, role.id, $4, $5, $6, $7 FROM role
		RETURNING id, (SELECT id FROM role), (SELECT name FROM role), created_at`,
		req.RoleKey, tc.TenantID, email, hash, tempPasswordHash, tc.ActorID, expiresAt,
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

	acceptPath := "/invite/" + raw
	if h.devExposeInviteURL {
		// Único lugar onde o token bruto aparece: a resposta desta chamada,
		// só quando o servidor tem o login de desenvolvimento ativo (mesmo
		// gate de config.DevAuthActive do login). Nunca aparece em list.
		inv.InviteURL = acceptPath
	}
	if err := h.deliver(r.Context(), q, tc, &inv, raw, tempPassword); err != nil {
		http.Error(w, "invitation created but the e-mail could not be delivered; use resend", http.StatusBadGateway)
		return
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
		SELECT i.id, i.email, r.key, r.name, i.status, i.expires_at, i.created_at, i.sent_at,
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
			&inv.ExpiresAt, &inv.CreatedAt, &inv.SentAt, &inv.AcceptedAt, &inv.RevokedAt, &inv.CreatedBy); err != nil {
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

// deliver entrega o e-mail do convite e registra sent_at. Sem sender real (dev/lab com dev
// auth) não há e-mail: o admin copia o invite_url. Falha do provedor vira erro para o
// chamador responder 502; o convite continua pending com sent_at nulo e pode ser reenviado.
// O log traz só o id do convite — nunca o token, o link, senha nem o corpo do erro do provedor.
func (h *InvitationsHandler) deliver(ctx context.Context, q platformdb.Querier, tc *domain.TenantContext, inv *Invitation, raw, tempPassword string) error {
	if isNoopSender(h.sender) {
		return nil
	}
	var tenantName, inviter string
	_ = q.QueryRow(ctx, `SELECT COALESCE(legal_name,'') FROM tenants WHERE id=$1`, tc.TenantID).Scan(&tenantName)
	_ = q.QueryRow(ctx, `SELECT COALESCE(email,'') FROM users WHERE id=$1`, tc.ActorID).Scan(&inviter)
	msg := InvitationMessage{
		To: inv.Email, TenantName: tenantName, InviterEmail: inviter,
		AcceptURL: strings.TrimRight(h.webBaseURL, "/") + "/invite/" + raw,
		TemporaryPassword: tempPassword,
		PasswordExpiresAt: password.ExpiresAt(),
		ExpiresAt: inv.ExpiresAt,
	}
	if err := h.sender.Send(ctx, msg); err != nil {
		log.Printf("tenancy: invitation %s e-mail delivery failed", inv.ID)
		return err
	}
	var sentAt time.Time
	if err := q.QueryRow(ctx, `UPDATE membership_invitations SET sent_at=now(), updated_at=now()
		WHERE id=$1 AND tenant_id=$2 RETURNING sent_at`, inv.ID, tc.TenantID).Scan(&sentAt); err != nil {
		return err
	}
	inv.SentAt = &sentAt
	return nil
}

// errOtherInstanceMessage is the answer (409) when a company administrator invites someone who already works in another
// instance. The wording is stable: the web app recognises it.
const errOtherInstanceMessage = "this person already works in another instance; only the Hub administrator can authorize them in more than one"

// worksElsewhere asks the database (person_works_in_other_instance, migration 101) whether the invited e-mail belongs to a
// person who already works in an instance other than this one. A failed question counts as "yes": fail closed.
func (h *InvitationsHandler) worksElsewhere(ctx context.Context, q platformdb.Querier, tenantID uuid.UUID, email string) bool {
	var yes bool
	if err := q.QueryRow(ctx, `SELECT person_works_in_other_instance($1, $2)`, tenantID, email).Scan(&yes); err != nil {
		return true
	}
	return yes
}

func (h *InvitationsHandler) isActiveMember(ctx context.Context, q platformdb.Querier, tenantID uuid.UUID, email string) bool {
	var ok bool
	_ = q.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM memberships m JOIN users u ON u.id = m.user_id
		              WHERE m.tenant_id=$1 AND m.status='active' AND lower(u.email)=lower($2))`,
		tenantID, email).Scan(&ok)
	return ok
}

// ResendInvitation — POST /api/v1/tenants/{tenant_id}/team/invitations/{invitation_id}/resend
//
// Reemite o token (o link anterior deixa de funcionar), renova o prazo e reenvia o e-mail.
// Só vale para convite ainda pending (inclusive vencido); aceito ou revogado não é reaberto.
func (h *InvitationsHandler) ResendInvitation(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permissionMembershipManage)
	if err != nil {
		respondAuthzError(w, err)
		return
	}
	if !h.deliveryAvailable {
		http.Error(w, "invitation delivery is not configured", http.StatusServiceUnavailable)
		return
	}
	invitationID, err := uuid.Parse(r.PathValue("invitation_id"))
	if err != nil {
		http.Error(w, "invalid invitation_id", http.StatusBadRequest)
		return
	}
	q := platformdb.QuerierFromContext(r.Context(), h.pool)

	var inv Invitation
	var status string
	err = q.QueryRow(r.Context(), `
		SELECT i.id, i.email, r.key, r.name, i.status, i.created_at
		FROM membership_invitations i JOIN roles r ON r.id = i.role_id
		WHERE i.id=$1 AND i.tenant_id=$2 FOR UPDATE OF i`, invitationID, tc.TenantID,
	).Scan(&inv.ID, &inv.Email, &inv.RoleKey, &inv.RoleName, &status, &inv.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "invitation not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read invitation", http.StatusInternalServerError)
		return
	}
	if status != "pending" {
		http.Error(w, "only pending invitations can be resent", http.StatusConflict)
		return
	}
	if h.isActiveMember(r.Context(), q, tc.TenantID, inv.Email) {
		http.Error(w, "user is already an active member", http.StatusConflict)
		return
	}
	if h.worksElsewhere(r.Context(), q, tc.TenantID, inv.Email) {
		http.Error(w, errOtherInstanceMessage, http.StatusConflict)
		return
	}

	raw, hash, err := generateInvitationToken()
	if err != nil {
		http.Error(w, "failed to generate invitation", http.StatusInternalServerError)
		return
	}
	tempPassword, err := password.Generate()
	if err != nil {
		http.Error(w, "failed to generate temporary password", http.StatusInternalServerError)
		return
	}
	tempPasswordHash, err := password.Hash(tempPassword)
	if err != nil {
		http.Error(w, "failed to hash temporary password", http.StatusInternalServerError)
		return
	}
	inv.ExpiresAt = time.Now().UTC().Add(invitationTTL)
	if _, err := q.Exec(r.Context(), `
		UPDATE membership_invitations
		SET token_hash=$3, temporary_password_hash=$4, expires_at=$5, sent_at=NULL, updated_at=now()
		WHERE id=$1 AND tenant_id=$2`, invitationID, tc.TenantID, hash, tempPasswordHash, inv.ExpiresAt); err != nil {
		http.Error(w, "failed to reissue invitation", http.StatusInternalServerError)
		return
	}
	inv.Status = "pending"
	h.recordInvitation(r.Context(), tc, auditdomain.ActionInvitationResent, inv.ID, inv.Email, inv.RoleKey)

	if h.devExposeInviteURL {
		inv.InviteURL = "/invite/" + raw
	}
	if err := h.deliver(r.Context(), q, tc, &inv, raw, tempPassword); err != nil {
		http.Error(w, "invitation reissued but the e-mail could not be delivered; try again", http.StatusBadGateway)
		return
	}
	writeTeamJSON(w, inv)
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
		if !h.emailVerified(ctx, q, principal.UserID, email) {
			resp.Status = "email_unverified"
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
//
// Requer POST body com { "password": "<senha temporária>" } para validar identidade.
type acceptInvitationRequest struct {
	Password string `json:"password"`
}

func (h *InvitationsHandler) AcceptInvitation(w http.ResponseWriter, r *http.Request) {
	principal, err := authn.FromContext(r.Context())
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req acceptInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		http.Error(w, "password is required", http.StatusBadRequest)
		return
	}

	token := r.PathValue("token")
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])

	var tenantID, invitationID uuid.UUID
	err = platformdb.WithTenantSession(r.Context(), h.pool, principal.UserID, true, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, h.pool)

		var email, status, roleKey, tempPasswordHash string
		var roleID uuid.UUID
		var expiresAt time.Time
		err := q.QueryRow(ctx, `
			SELECT i.id, i.tenant_id, i.email, i.status, i.expires_at, i.role_id, i.temporary_password_hash, r.key
			FROM membership_invitations i JOIN roles r ON r.id = i.role_id
			WHERE i.token_hash = $1 FOR UPDATE OF i`, hash,
		).Scan(&invitationID, &tenantID, &email, &status, &expiresAt, &roleID, &tempPasswordHash, &roleKey)
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
		if !h.emailVerified(ctx, q, principal.UserID, email) {
			return errInvitationEmailUnverified
		}
		switch effectiveStatus(status, expiresAt) {
		case "accepted":
			return errInvitationAlreadyUsed
		case "revoked":
			return errInvitationRevoked
		case "expired":
			return errInvitationExpired
		}

		// Validar a senha temporária contra o hash armazenado
		if !password.Verify(tempPasswordHash, req.Password) {
			return errInvitationPasswordInvalid
		}
		// ADR-0039, de novo: o convite pode ter sido emitido antes de a pessoa passar a atuar em outra instância.
		if h.worksElsewhere(ctx, q, tenantID, email) {
			return errInvitationOtherInstance
		}

		// Hash a senha temporária para armazenar como password_hash inicial
		passwordHash, err := password.Hash(req.Password)
		if err != nil {
			return err
		}
		passwordExpiresAt := password.ExpiresAt()

		if _, err := q.Exec(ctx, `
			UPDATE membership_invitations
			SET status='accepted', accepted_by_user_id=$2, accepted_at=now(), updated_at=now()
			WHERE id=$1`, invitationID, principal.UserID); err != nil {
			return err
		}

		// Criar ou atualizar user com password_hash e flag de troca obrigatória
		// (password_expires_at não nulo = força troca de senha no login)
		if _, err := q.Exec(ctx, `
			UPDATE users
			SET password_hash=$2, password_expires_at=$3, updated_at=now()
			WHERE id=$1`,
			principal.UserID, passwordHash, passwordExpiresAt); err != nil {
			return err
		}

		// Reativa uma membership antiga (ex.: revogada e convidada de novo) com o papel
		// do convite. Se já estiver ativa, o papel atual é preservado: um convite não
		// rebaixa nem promove quem já é membro (isso é PATCH /team, com o invariante do
		// último admin).
		if _, err := q.Exec(ctx, `
			INSERT INTO memberships (id, tenant_id, user_id, role_id, status)
			VALUES ($1, $2, $3, $4, 'active')
			ON CONFLICT (tenant_id, user_id)
			DO UPDATE SET role_id = CASE WHEN memberships.status = 'active' THEN memberships.role_id ELSE EXCLUDED.role_id END,
			              status = 'active', updated_at = now()`,
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
	case errors.Is(err, errInvitationEmailUnverified):
		http.Error(w, "email not verified by the identity provider", http.StatusForbidden)
	case errors.Is(err, errInvitationPasswordInvalid):
		http.Error(w, "invalid temporary password", http.StatusUnauthorized)
	case errors.Is(err, errInvitationOtherInstance):
		http.Error(w, errOtherInstanceMessage, http.StatusConflict)
	case err != nil:
		http.Error(w, "failed to accept invitation", http.StatusInternalServerError)
	default:
		writeTeamJSON(w, map[string]any{"tenant_id": tenantID})
	}
}

var (
	errInvitationNotFound         = errors.New("invitation not found")
	errInvitationAlreadyUsed      = errors.New("invitation already accepted")
	errInvitationOtherInstance    = errors.New("person already works in another instance")
	errInvitationRevoked          = errors.New("invitation revoked")
	errInvitationExpired          = errors.New("invitation expired")
	errInvitationEmailUnverified  = errors.New("invitation e-mail not verified by the identity provider")
	errInvitationPasswordInvalid  = errors.New("invitation temporary password is invalid")
)

// emailVerified: o e-mail do convite só vale como prova de identidade se o IdP afirmou
// email_verified=true para uma identidade deste usuário com esse mesmo e-mail. Com o dev
// auth ativo (dev/lab) não há IdP, então a regra não se aplica; nunca fica true em produção.
func (h *InvitationsHandler) emailVerified(ctx context.Context, q platformdb.Querier, userID uuid.UUID, email string) bool {
	if h.devExposeInviteURL {
		return true
	}
	var ok bool
	_ = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_identities
		WHERE user_id=$1 AND email_verified AND lower(email)=lower($2))`, userID, email).Scan(&ok)
	return ok
}
