package adapters

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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
	// login prepares the invitee's sign-in account at the identity provider (ADR-0046); nil = OMNIRA does not touch the IdP.
	login loginPreparer
}

// loginPreparer is KeycloakLogins as seen by the invitations: Prepare makes the temporary password of the e-mail work for this invitation
// (creating the account, or refreshing one OMNIRA created for it, for an invitation it replaced or for one that is over) and says whether
// it did; Disable turns off the unused account of exactly this invitation.
type loginPreparer interface {
	Prepare(ctx context.Context, inv LoginInvitation) (passwordSet bool, err error)
	Disable(ctx context.Context, email, invitationID string) (disabled bool, err error)
}

// WithLoginPreparer makes invitations prepare the invitee's sign-in account, so the temporary password in the e-mail is the one the
// identity provider accepts.
func (h *InvitationsHandler) WithLoginPreparer(p loginPreparer) *InvitationsHandler {
	h.login = p
	return h
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
	// ADR-0046: what the person will do once they accept: attend, in which queues of this tenant, with what capacity in each.
	Attends       bool        `json:"attends"`
	QueueIDs      []uuid.UUID `json:"queue_ids"`
	QueueCapacity int         `json:"queue_capacity"`
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
	// Optional attendance plan (ADR-0046), applied when the invitation is accepted.
	Attends       bool        `json:"attends,omitempty"`
	QueueIDs      []uuid.UUID `json:"queue_ids,omitempty"`
	QueueCapacity *int        `json:"queue_capacity,omitempty"` // omitted = 1; 0 = no limit
}

const maxPlannedQueues = 50

// planFrom validates the attendance plan of a new invitation. Choosing who attends is agent.manage territory, so a plan also needs it.
// Queues must belong to this tenant (RLS hides the others, so a foreign id simply does not count).
func (h *InvitationsHandler) planFrom(r *http.Request, q platformdb.Querier, tenantID uuid.UUID, req CreateInvitationRequest) (Invitation, int, string) {
	plan := Invitation{Attends: req.Attends, QueueIDs: []uuid.UUID{}, QueueCapacity: 1}
	if !req.Attends {
		if len(req.QueueIDs) > 0 {
			return plan, http.StatusBadRequest, "queue_ids require attends=true"
		}
		return plan, 0, ""
	}
	if _, err := h.authorize(r, "agent.manage"); err != nil {
		return plan, http.StatusForbidden, "choosing who attends needs agent.manage"
	}
	if req.QueueCapacity != nil {
		if *req.QueueCapacity < 0 || *req.QueueCapacity > 100 {
			return plan, http.StatusBadRequest, "queue_capacity must be 0 (no limit) to 100"
		}
		plan.QueueCapacity = *req.QueueCapacity
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range req.QueueIDs {
		if id != uuid.Nil && !seen[id] {
			seen[id] = true
			plan.QueueIDs = append(plan.QueueIDs, id)
		}
	}
	if len(plan.QueueIDs) > maxPlannedQueues {
		return plan, http.StatusBadRequest, "too many queues"
	}
	if len(plan.QueueIDs) > 0 {
		var found int
		if err := q.QueryRow(r.Context(), `SELECT count(*) FROM queues WHERE tenant_id=$1 AND id = ANY($2)`, tenantID, plan.QueueIDs).Scan(&found); err != nil {
			return plan, http.StatusInternalServerError, "failed to check queues"
		}
		if found != len(plan.QueueIDs) {
			return plan, http.StatusUnprocessableEntity, "queue not found in this tenant"
		}
	}
	return plan, 0, ""
}

// prepareLogin makes the temporary password of the e-mail work at the identity provider (ADR-0046). Without a preparer the e-mail keeps
// the password as before; with one, the password goes in the e-mail only when the account was created or refreshed for this invitation.
func (h *InvitationsHandler) prepareLogin(ctx context.Context, tc *domain.TenantContext, inv Invitation, tempPassword string, replaces []string) (includePassword bool, outcome string, err error) {
	if h.login == nil {
		return true, "", nil
	}
	set, err := h.login.Prepare(ctx, LoginInvitation{Email: inv.Email, Password: tempPassword, ID: inv.ID.String(), ExpiresAt: inv.ExpiresAt, Replaces: replaces})
	switch {
	case err != nil:
		log.Printf("tenancy: invitation %s sign-in account not prepared: %v", inv.ID, err)
		includePassword, outcome = false, "failed"
	case set:
		includePassword, outcome = true, "ready"
	default:
		includePassword, outcome = false, "existing"
	}
	h.recordLogin(ctx, tc, auditdomain.ActionInvitationLoginPrepared, inv.ID, inv.Email, outcome, err == nil)
	return includePassword, outcome, err
}

// lockInvitationLogin serializes, across API replicas and until the request's transaction ends, every invitation write that may touch
// the sign-in account of this e-mail (create, resend, cancel): two of them never interleave their identity-provider reads and writes.
// Taken before any invitation row is written or locked, so it never waits while holding a row another of them needs.
func lockInvitationLogin(ctx context.Context, q platformdb.Querier, email string) error {
	_, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('omnira.invitation-login:' || lower($1), 0))`, email)
	return err
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
	plan, status, reason := h.planFrom(r, q, tc.TenantID, req)
	if status != 0 {
		http.Error(w, reason, status)
		return
	}
	if h.login != nil {
		if err := lockInvitationLogin(r.Context(), q, email); err != nil {
			http.Error(w, "failed to create invitation", http.StatusInternalServerError)
			return
		}
	}

	// Política de duplicidade: no máximo um pending por (tenant, email). Um
	// novo convite substitui o anterior — revoga e insere na mesma
	// transação — em vez de deixar dois pendentes indistinguíveis, ou de
	// obrigar o admin a revogar manualmente antes de reenviar.
	rows, err := q.Query(r.Context(), `
		UPDATE membership_invitations SET status='revoked', revoked_at=now(), updated_at=now()
		WHERE tenant_id=$1 AND lower(email)=$2 AND status='pending'
		RETURNING id`,
		tc.TenantID, email)
	if err != nil {
		http.Error(w, "failed to replace previous invitation", http.StatusInternalServerError)
		return
	}
	// The sign-in account an invitation replaced here may be taken over by the new one (ADR-0046).
	var replaced []string
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			replaced = append(replaced, id.String())
		}
	}
	rows.Close()
	if rows.Err() != nil {
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
		INSERT INTO membership_invitations (tenant_id, email, role_id, token_hash, temporary_password_hash, created_by, expires_at,
		                                    attends, queue_ids, queue_capacity)
		SELECT $2, $3, role.id, $4, $5, $6, $7, $8, $9, $10 FROM role
		RETURNING id, (SELECT id FROM role), (SELECT name FROM role), created_at`,
		req.RoleKey, tc.TenantID, email, hash, tempPasswordHash, tc.ActorID, expiresAt,
		plan.Attends, plan.QueueIDs, plan.QueueCapacity,
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
	inv.Attends, inv.QueueIDs, inv.QueueCapacity = plan.Attends, plan.QueueIDs, plan.QueueCapacity
	_ = q.QueryRow(r.Context(), `SELECT COALESCE(email,'') FROM users WHERE id=$1`, tc.ActorID).Scan(&inv.CreatedBy)

	includePassword, login, loginErr := h.prepareLogin(r.Context(), tc, inv, tempPassword, replaced)
	h.recordInvitation(r.Context(), tc, auditdomain.ActionInvitationCreated, inv.ID, email, req.RoleKey, planAudit(inv, login))
	if loginErr != nil {
		// The invitation stays pending; "Reenviar" prepares the account again and sends the e-mail.
		http.Error(w, "invitation created but the sign-in account could not be prepared; use resend", http.StatusBadGateway)
		return
	}

	acceptPath := "/invite/" + raw
	if h.devExposeInviteURL {
		// Único lugar onde o token bruto aparece: a resposta desta chamada,
		// só quando o servidor tem o login de desenvolvimento ativo (mesmo
		// gate de config.DevAuthActive do login). Nunca aparece em list.
		inv.InviteURL = acceptPath
	}
	if err := h.deliver(r.Context(), q, tc, &inv, raw, tempPassword, includePassword); err != nil {
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
		       i.accepted_at, i.revoked_at, COALESCE(u.email, ''), i.attends, i.queue_ids, i.queue_capacity
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
			&inv.ExpiresAt, &inv.CreatedAt, &inv.SentAt, &inv.AcceptedAt, &inv.RevokedAt, &inv.CreatedBy,
			&inv.Attends, &inv.QueueIDs, &inv.QueueCapacity); err != nil {
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
	q := platformdb.QuerierFromContext(r.Context(), h.pool)
	if h.login != nil {
		var lockEmail string
		if err := q.QueryRow(r.Context(), `SELECT email FROM membership_invitations WHERE id=$1 AND tenant_id=$2`, invitationID, tc.TenantID).Scan(&lockEmail); err == nil {
			if err := lockInvitationLogin(r.Context(), q, lockEmail); err != nil {
				http.Error(w, "failed to revoke invitation", http.StatusInternalServerError)
				return
			}
		}
	}
	tag, err := q.Exec(r.Context(), `
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

	// The account OMNIRA created for THIS invitation, if nobody used it, is turned off (a later invitation turns it back on). Best effort:
	// the invitation is cancelled either way, and the audit says whether the account was turned off.
	var extra map[string]interface{}
	if h.login != nil && email != "" {
		outcome := "untouched"
		disabled, err := h.login.Disable(r.Context(), email, invitationID.String())
		if err != nil {
			outcome = "disable_failed"
			log.Printf("tenancy: invitation %s cancelled; unused sign-in account not disabled: %v", invitationID, err)
		} else if disabled {
			outcome = "disabled"
		}
		if err != nil || disabled {
			h.recordLogin(r.Context(), tc, auditdomain.ActionInvitationLoginDisabled, invitationID, email, outcome, err == nil)
		}
		extra = map[string]interface{}{"login": outcome}
	}
	h.recordInvitation(r.Context(), tc, auditdomain.ActionInvitationRevoked, invitationID, email, "", extra)
	w.WriteHeader(http.StatusNoContent)
}

// deliver entrega o e-mail do convite e registra sent_at. Sem sender real (dev/lab com dev
// auth) não há e-mail: o admin copia o invite_url. Falha do provedor vira erro para o
// chamador responder 502; o convite continua pending com sent_at nulo e pode ser reenviado.
// O log traz só o id do convite — nunca o token, o link, senha nem o corpo do erro do provedor.
func (h *InvitationsHandler) deliver(ctx context.Context, q platformdb.Querier, tc *domain.TenantContext, inv *Invitation, raw, tempPassword string, includePassword bool) error {
	if isNoopSender(h.sender) {
		return nil
	}
	if !includePassword {
		tempPassword = "" // the person signs in with the account they already have
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

// lockPerson takes a transaction-scoped lock for one person. Everything that can put a person into an instance asks "do they
// already work elsewhere?" and then writes; holding this lock across both makes the pair atomic per person.
func lockPerson(ctx context.Context, q platformdb.Querier, user uuid.UUID) error {
	_, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('omnira.person-instance:' || $1::text, 0))`, user)
	return err
}

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
	if h.login != nil {
		var lockEmail string
		if err := q.QueryRow(r.Context(), `SELECT email FROM membership_invitations WHERE id=$1 AND tenant_id=$2`, invitationID, tc.TenantID).Scan(&lockEmail); err == nil {
			if err := lockInvitationLogin(r.Context(), q, lockEmail); err != nil {
				http.Error(w, "failed to read invitation", http.StatusInternalServerError)
				return
			}
		}
	}

	var inv Invitation
	var status string
	err = q.QueryRow(r.Context(), `
		SELECT i.id, i.email, r.key, r.name, i.status, i.created_at, i.attends, i.queue_ids, i.queue_capacity
		FROM membership_invitations i JOIN roles r ON r.id = i.role_id
		WHERE i.id=$1 AND i.tenant_id=$2 FOR UPDATE OF i`, invitationID, tc.TenantID,
	).Scan(&inv.ID, &inv.Email, &inv.RoleKey, &inv.RoleName, &status, &inv.CreatedAt, &inv.Attends, &inv.QueueIDs, &inv.QueueCapacity)
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
	includePassword, login, loginErr := h.prepareLogin(r.Context(), tc, inv, tempPassword, nil)
	h.recordInvitation(r.Context(), tc, auditdomain.ActionInvitationResent, inv.ID, inv.Email, inv.RoleKey, planAudit(inv, login))
	if loginErr != nil {
		http.Error(w, "invitation reissued but the sign-in account could not be prepared; try again", http.StatusBadGateway)
		return
	}

	if h.devExposeInviteURL {
		inv.InviteURL = "/invite/" + raw
	}
	if err := h.deliver(r.Context(), q, tc, &inv, raw, tempPassword, includePassword); err != nil {
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

// planAudit is what the audit trail keeps about the attendance plan and the sign-in account (never the password or the token).
func planAudit(inv Invitation, login string) map[string]interface{} {
	meta := map[string]interface{}{}
	if inv.Attends {
		meta["attends"] = true
		meta["queue_count"] = len(inv.QueueIDs)
	}
	if login != "" {
		meta["login"] = login
	}
	return meta
}

// recordLogin audits what happened to the invitee's sign-in account (ADR-0046) as its own event, with a failure outcome when the identity
// provider could not do it. Never the password, never a token.
func (h *InvitationsHandler) recordLogin(ctx context.Context, tc *domain.TenantContext, action auditdomain.AuditAction, invitationID uuid.UUID, email, outcome string, ok bool) {
	if h.audit == nil {
		return
	}
	result := auditdomain.OutcomeSuccess
	if !ok {
		result = auditdomain.OutcomeFailure
	}
	_ = h.audit.Store(ctx, &auditdomain.AuditEvent{
		ID: uuid.New(), TenantID: tc.TenantID, ActorID: tc.ActorID, Action: action,
		ResourceType: auditdomain.ResourceMembership, ResourceID: invitationID,
		Outcome: result, CorrelationID: uuid.New(), CausationID: invitationID,
		Metadata: map[string]interface{}{"email": email, "login": outcome}, CreatedAt: time.Now().UTC(),
	})
}

func (h *InvitationsHandler) recordInvitation(ctx context.Context, tc *domain.TenantContext, action auditdomain.AuditAction, invitationID uuid.UUID, email, roleKey string, extra map[string]interface{}) {
	if h.audit == nil {
		return
	}
	meta := map[string]interface{}{}
	for k, v := range extra {
		meta[k] = v
	}
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
// O corpo pode trazer { "password": "<senha temporária>" } (opcional; ver abaixo).
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	// A senha temporária é opcional. A identidade já é provada pela sessão (SSO) com e-mail verificado pelo IdP e igual ao do
	// convite; a página de aceite (/invite/:token) não envia senha. Exigi-la deixava o aceite pela interface sempre em 400.
	// Se vier senha, ela precisa bater com a do convite e passa a ser a senha inicial da conta (troca obrigatória).
	token := r.PathValue("token")
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])

	var tenantID, invitationID uuid.UUID
	err = platformdb.WithTenantSession(r.Context(), h.pool, principal.UserID, true, func(ctx context.Context) error {
		q := platformdb.QuerierFromContext(ctx, h.pool)

		var email, status, roleKey, tempPasswordHash string
		var roleID uuid.UUID
		var invitedBy *uuid.UUID
		var expiresAt time.Time
		var plan Invitation
		err := q.QueryRow(ctx, `
			SELECT i.id, i.tenant_id, i.email, i.status, i.expires_at, i.role_id, i.temporary_password_hash, r.key,
			       i.attends, i.queue_ids, i.queue_capacity, i.created_by
			FROM membership_invitations i JOIN roles r ON r.id = i.role_id
			WHERE i.token_hash = $1 FOR UPDATE OF i`, hash,
		).Scan(&invitationID, &tenantID, &email, &status, &expiresAt, &roleID, &tempPasswordHash, &roleKey,
			&plan.Attends, &plan.QueueIDs, &plan.QueueCapacity, &invitedBy)
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

		// Senha enviada: precisa bater com a temporária do convite.
		withPassword := strings.TrimSpace(req.Password) != ""
		if withPassword && !password.Verify(tempPasswordHash, req.Password) {
			return errInvitationPasswordInvalid
		}
		// ADR-0039, de novo: o convite pode ter sido emitido antes de a pessoa passar a atuar em outra instância.
		// O lock por pessoa serializa "perguntar e entrar": dois convites aceitos ao mesmo tempo, em empresas diferentes, não
		// passam os dois pela pergunta antes de qualquer um gravar a membership (Codex).
		if err := lockPerson(ctx, q, principal.UserID); err != nil {
			return err
		}
		if h.worksElsewhere(ctx, q, tenantID, email) {
			return errInvitationOtherInstance
		}

		// Aceite sem senha (SSO) não toca na credencial da conta.
		if withPassword {
			passwordHash, err := password.Hash(req.Password)
			if err != nil {
				return err
			}
			// password_expires_at não nulo = força troca de senha no login
			if _, err := q.Exec(ctx, `
				UPDATE users
				SET password_hash=$2, password_expires_at=$3, updated_at=now()
				WHERE id=$1`,
				principal.UserID, passwordHash, password.ExpiresAt()); err != nil {
				return err
			}
		}

		if _, err := q.Exec(ctx, `
			UPDATE membership_invitations
			SET status='accepted', accepted_by_user_id=$2, accepted_at=now(), updated_at=now()
			WHERE id=$1`, invitationID, principal.UserID); err != nil {
			return err
		}

		// Reativa uma membership antiga (ex.: revogada e convidada de novo) com o papel
		// do convite. Se já estiver ativa, o papel atual é preservado: um convite não
		// rebaixa nem promove quem já é membro (isso é PATCH /team, com o invariante do
		// último admin).
		var membershipID uuid.UUID
		if err := q.QueryRow(ctx, `
			INSERT INTO memberships (id, tenant_id, user_id, role_id, status)
			VALUES ($1, $2, $3, $4, 'active')
			ON CONFLICT (tenant_id, user_id)
			DO UPDATE SET role_id = CASE WHEN memberships.status = 'active' THEN memberships.role_id ELSE EXCLUDED.role_id END,
			              status = 'active', updated_at = now()
			RETURNING id`,
			uuid.New(), tenantID, principal.UserID, roleID).Scan(&membershipID); err != nil {
			return err
		}
		if plan.Attends {
			profileID, seated, err := applyAttendancePlan(ctx, q, tenantID, membershipID, principal.UserID, plan.QueueIDs, plan.QueueCapacity)
			if err != nil {
				return err
			}
			// The plan was chosen by whoever invited: the attendant and each queue seat are audited in that person's name, as when an
			// administrator turns on "Recebe conversas" and adds queues in the sheet.
			planner := principal.UserID
			if invitedBy != nil {
				planner = *invitedBy
			}
			h.recordPlanApplied(ctx, tenantID, planner, invitationID, profileID, seated)
		}

		tc := &domain.TenantContext{TenantID: tenantID, ActorID: principal.UserID}
		h.recordInvitation(ctx, tc, auditdomain.ActionInvitationAccepted, invitationID, email, roleKey, planAudit(plan, ""))
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

// applyAttendancePlan makes the new member an attendant as POST /agents does (profile active; somebody with no queue of their own joins
// the company's default overflow queue, ADR-0044) and adds the queues chosen in the invitation that still exist in this tenant.
func applyAttendancePlan(ctx context.Context, q platformdb.Querier, tenantID, membershipID, userID uuid.UUID, queueIDs []uuid.UUID, capacity int) (uuid.UUID, []uuid.UUID, error) {
	var profileID uuid.UUID
	if err := q.QueryRow(ctx, `
		INSERT INTO agent_profiles (tenant_id, membership_id, status) VALUES ($1, $2, 'active')
		ON CONFLICT (tenant_id, membership_id) DO UPDATE SET status = 'active', updated_at = now()
		RETURNING id`, tenantID, membershipID).Scan(&profileID); err != nil {
		return uuid.Nil, nil, err
	}
	var seated []uuid.UUID
	seat := func(sql string, args ...any) error {
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			seated = append(seated, id)
		}
		return rows.Err()
	}
	if len(queueIDs) > 0 {
		if err := seat(`
			INSERT INTO queue_members (tenant_id, queue_id, user_id, active, available, capacity)
			SELECT q.tenant_id, q.id, $3, true, true, $4 FROM queues q WHERE q.tenant_id = $1 AND q.id = ANY($2)
			ON CONFLICT (tenant_id, queue_id, user_id) DO UPDATE SET active = true, available = true, capacity = EXCLUDED.capacity
			RETURNING queue_id`, tenantID, queueIDs, userID, capacity); err != nil {
			return uuid.Nil, nil, err
		}
	}
	if err := seat(`
		INSERT INTO queue_members (tenant_id, queue_id, user_id, active, available, capacity)
		SELECT t.id, t.default_overflow_queue_id, $2, true, true, 5 FROM tenants t
		WHERE t.id = $1 AND t.default_overflow_queue_id IS NOT NULL
		  AND NOT EXISTS (SELECT 1 FROM queue_members qm WHERE qm.tenant_id = t.id AND qm.user_id = $2)
		ON CONFLICT (tenant_id, queue_id, user_id) DO NOTHING
		RETURNING queue_id`, tenantID, userID); err != nil {
		return uuid.Nil, nil, err
	}
	return profileID, seated, nil
}

// recordPlanApplied audits what the invitation's plan did at acceptance: agent.enabled and one agent.queue_assigned per queue seat, in
// the name of the person who chose the plan, with the invitation as the cause.
func (h *InvitationsHandler) recordPlanApplied(ctx context.Context, tenantID, actorID, invitationID, profileID uuid.UUID, queues []uuid.UUID) {
	if h.audit == nil {
		return
	}
	// Inside a savepoint: in PostgreSQL a failed INSERT aborts the whole transaction, and the acceptance must not depend on its audit.
	q := platformdb.QuerierFromContext(ctx, h.pool)
	if _, err := q.Exec(ctx, `SAVEPOINT invitation_plan_audit`); err != nil {
		log.Printf("tenancy: invitation %s plan audit skipped: %v", invitationID, err)
		return
	}
	correlation := uuid.New()
	store := func(action auditdomain.AuditAction, meta map[string]interface{}) error {
		meta["via"] = "invitation"
		meta["invitation_id"] = invitationID.String()
		return h.audit.Store(ctx, &auditdomain.AuditEvent{
			ID: uuid.New(), TenantID: tenantID, ActorID: actorID, Action: action,
			ResourceType: auditdomain.ResourceAgentProfile, ResourceID: profileID,
			Outcome: auditdomain.OutcomeSuccess, CorrelationID: correlation, CausationID: invitationID,
			Metadata: meta, CreatedAt: time.Now().UTC(),
		})
	}
	err := store(auditdomain.ActionAgentEnabled, map[string]interface{}{})
	for _, queue := range queues {
		if err != nil {
			break
		}
		err = store(auditdomain.ActionAgentQueueAssigned, map[string]interface{}{"queue_id": queue.String()})
	}
	if err != nil {
		log.Printf("tenancy: invitation %s plan audit not recorded: %v", invitationID, err)
		_, _ = q.Exec(ctx, `ROLLBACK TO SAVEPOINT invitation_plan_audit`)
		return
	}
	_, _ = q.Exec(ctx, `RELEASE SAVEPOINT invitation_plan_audit`)
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
