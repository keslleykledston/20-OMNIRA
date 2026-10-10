package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	auditdomain "github.com/omnira/omnira/internal/audit/domain"
	auditports "github.com/omnira/omnira/internal/audit/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/tenancy/domain"
)

// Papéis que a administração de um tenant pode conceder. system_admin e
// hub_admin existem no catálogo mas são globais: deixá-los no seletor
// permitiria a um tenant_admin se promover para fora do próprio tenant.
var tenantAssignableRoles = []string{"tenant_admin", "tenant_supervisor", "tenant_agent"}

const (
	permissionMembershipRead   = "membership.read"
	permissionMembershipManage = "membership.manage"
)

// TeamMember é o read model da tela de equipe: membership, identidade e papel
// numa linha só. Sem tenant_id — a sessão já estabeleceu o tenant.
type TeamMember struct {
	MembershipID uuid.UUID `json:"membership_id"`
	UserID       uuid.UUID `json:"user_id"`
	Name         string    `json:"name"`
	Email        string    `json:"email"`
	RoleKey      string    `json:"role_key"`
	RoleName     string    `json:"role_name"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	// Login mais recente da identidade global (user_identities.last_login_at),
	// nunca presença/atividade no tenant — isso é last_seen do agente, IAM4.
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

type RoleOption struct {
	ID   uuid.UUID `json:"id"`
	Key  string    `json:"key"`
	Name string    `json:"name"`
	// Permissions é o conjunto fixo do papel (somente leitura): a matriz da tela
	// "Funções e permissões". As chaves vêm de role_permissions, nunca de constante.
	Permissions []string `json:"permissions"`
}

type TeamHandler struct {
	pool  *pgxpool.Pool
	audit auditports.AuditEventRepository
	// invitationDeliveryAvailable espelha InvitationsHandler.deliveryAvailable
	// (mesma regra, calculada uma vez no wiring) — MyAccess é onde o
	// frontend já consulta permissões, então reaproveitar aqui evita um
	// endpoint novo só para esta capability.
	invitationDeliveryAvailable bool
}

func NewTeamHandler(pool *pgxpool.Pool, audit auditports.AuditEventRepository, invitationDeliveryAvailable bool) *TeamHandler {
	return &TeamHandler{pool: pool, audit: audit, invitationDeliveryAvailable: invitationDeliveryAvailable}
}

// authorize resolve a permissão pelo papel da membership ativa do ator, e não
// por comparação com o nome de um role: a matriz role→permission é a fonte, e
// checar string de papel espalharia a política pelo código.
func (h *TeamHandler) authorize(r *http.Request, permission string) (*domain.TenantContext, error) {
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

var errPermissionDenied = errors.New("permission denied")

func respondAuthzError(w http.ResponseWriter, err error) {
	if errors.Is(err, errPermissionDenied) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

// MyAccess — GET /api/v1/tenants/{tenant_id}/me/access
//
// Fonte única de effective permissions: o frontend usa isto para
// habilitar/esconder controles em vez de derivar permissões a partir do nome
// do papel. O backend continua sendo a autoridade — isto só evita que a UI
// ofereça uma ação que o servidor vai recusar.
func (h *TeamHandler) MyAccess(w http.ResponseWriter, r *http.Request) {
	tc, err := domain.FromContext(r.Context())
	if err != nil || tc.TenantID == uuid.Nil {
		http.Error(w, "tenant context not found", http.StatusInternalServerError)
		return
	}

	if tc.Source == domain.AccessSourceHubServe {
		// A Hub agent attending the instance (ADR-0040): the keys the grant and the contract's ceiling both hold, asked live. Never the
		// membership's role: a person who is also a member gets only what THIS context gives.
		var keys []string
		if err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(),
			`SELECT delegated_permissions($1, $2, acting_hub())`, tc.TenantID, tc.ActorID).Scan(&keys); err != nil {
			http.Error(w, "failed to load permissions", http.StatusInternalServerError)
			return
		}
		if keys == nil {
			keys = []string{}
		}
		writeTeamJSON(w, map[string]any{"role_key": "hub_delegate", "permissions": keys, "invitation_delivery_available": false})
		return
	}

	var roleKey string
	if err := platformdb.QuerierFromContext(r.Context(), h.pool).QueryRow(r.Context(), `
		SELECT r.key FROM memberships m JOIN roles r ON r.id = m.role_id
		WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active'`,
		tc.TenantID, tc.ActorID).Scan(&roleKey); err != nil {
		http.Error(w, "membership not found", http.StatusNotFound)
		return
	}

	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `
		SELECT rp.permission_key FROM role_permissions rp
		JOIN memberships m ON m.role_id = rp.role_id
		WHERE m.tenant_id=$1 AND m.user_id=$2 AND m.status='active'`,
		tc.TenantID, tc.ActorID)
	if err != nil {
		http.Error(w, "failed to load permissions", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	permissions := make([]string, 0)
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			http.Error(w, "failed to read permissions", http.StatusInternalServerError)
			return
		}
		permissions = append(permissions, p)
	}
	writeTeamJSON(w, map[string]any{
		"role_key": roleKey, "permissions": permissions,
		"invitation_delivery_available": h.invitationDeliveryAvailable,
	})
}

// ListTeam — GET /api/v1/tenants/{tenant_id}/team
//
// Uma query com join: a tela precisa de nome, e-mail e papel por linha, e
// resolver isso no cliente seria um N+1 por membro.
func (h *TeamHandler) ListTeam(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permissionMembershipRead)
	if err != nil {
		respondAuthzError(w, err)
		return
	}

	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `
		SELECT m.id, u.id, COALESCE(u.display_name,''), COALESCE(u.email,''),
		       r.key, r.name, m.status, m.created_at, i.last_login_at
		FROM memberships m
		JOIN users u ON u.id = m.user_id
		JOIN roles r ON r.id = m.role_id
		LEFT JOIN (
		  SELECT user_id, MAX(last_login_at) AS last_login_at
		  FROM user_identities GROUP BY user_id
		) i ON i.user_id = u.id
		WHERE m.tenant_id = $1
		ORDER BY u.email ASC, m.created_at ASC`, tc.TenantID)
	if err != nil {
		http.Error(w, "failed to list team", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]TeamMember, 0)
	for rows.Next() {
		var m TeamMember
		if err := rows.Scan(&m.MembershipID, &m.UserID, &m.Name, &m.Email,
			&m.RoleKey, &m.RoleName, &m.Status, &m.CreatedAt, &m.LastLoginAt); err != nil {
			http.Error(w, "failed to read team", http.StatusInternalServerError)
			return
		}
		items = append(items, m)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "failed to read team", http.StatusInternalServerError)
		return
	}
	writeTeamJSON(w, map[string]any{"items": items})
}

// ListAssignableRoles — GET /api/v1/tenants/{tenant_id}/roles
func (h *TeamHandler) ListAssignableRoles(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permissionMembershipRead)
	if err != nil {
		respondAuthzError(w, err)
		return
	}
	_ = tc

	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `
		SELECT r.id, r.key, r.name,
		       COALESCE(array_agg(rp.permission_key ORDER BY rp.permission_key)
		                FILTER (WHERE rp.permission_key IS NOT NULL), '{}')
		FROM roles r
		LEFT JOIN role_permissions rp ON rp.role_id = r.id
		WHERE r.tenant_id IS NULL AND r.key = ANY($1)
		GROUP BY r.id, r.key, r.name
		ORDER BY r.key`, tenantAssignableRoles)
	if err != nil {
		http.Error(w, "failed to list roles", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	items := make([]RoleOption, 0, len(tenantAssignableRoles))
	for rows.Next() {
		var o RoleOption
		if err := rows.Scan(&o.ID, &o.Key, &o.Name, &o.Permissions); err != nil {
			http.Error(w, "failed to read roles", http.StatusInternalServerError)
			return
		}
		items = append(items, o)
	}
	writeTeamJSON(w, map[string]any{"items": items})
}

type UpdateMembershipRequest struct {
	RoleKey *string `json:"role_key,omitempty"`
	Status  *string `json:"status,omitempty"`
}

// UpdateMembership — PATCH /api/v1/tenants/{tenant_id}/team/{membership_id}
func (h *TeamHandler) UpdateMembership(w http.ResponseWriter, r *http.Request) {
	tc, err := h.authorize(r, permissionMembershipManage)
	if err != nil {
		respondAuthzError(w, err)
		return
	}
	membershipID, err := uuid.Parse(r.PathValue("membership_id"))
	if err != nil {
		http.Error(w, "invalid membership_id", http.StatusBadRequest)
		return
	}
	var req UpdateMembershipRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	if req.RoleKey == nil && req.Status == nil {
		http.Error(w, "nothing to update", http.StatusBadRequest)
		return
	}
	if req.Status != nil && *req.Status != "active" && *req.Status != "inactive" && *req.Status != "revoked" {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}
	if req.RoleKey != nil && !isAssignableRole(*req.RoleKey) {
		// Papéis globais não são atribuíveis pela administração de um tenant.
		http.Error(w, "role is not assignable in this tenant", http.StatusUnprocessableEntity)
		return
	}

	q := platformdb.QuerierFromContext(r.Context(), h.pool)

	// Uma alteração de papel/status por vez em cada empresa (mesma chave usada pelo painel de acessos do Hub): é o que torna
	// "contar os outros administradores" e "escrever" atômicos. Travar só a linha do alvo deixava dois pedidos, sobre
	// administradores diferentes, enxergarem um ao outro como "o outro admin" e esvaziarem a empresa (Codex).
	if _, err := q.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('omnira.tenant-admins:' || $1::text, 0))`, tc.TenantID); err != nil {
		http.Error(w, "failed to read membership", http.StatusInternalServerError)
		return
	}

	// Trava a linha: a checagem de "último admin" e a escrita precisam ser
	// atômicas, senão dois pedidos simultâneos removem os dois últimos.
	var targetUser uuid.UUID
	var currentRole, currentStatus string
	err = q.QueryRow(r.Context(), `
		SELECT m.user_id, r.key, m.status
		FROM memberships m JOIN roles r ON r.id = m.role_id
		WHERE m.tenant_id=$1 AND m.id=$2 FOR UPDATE OF m`,
		tc.TenantID, membershipID).Scan(&targetUser, &currentRole, &currentStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		// Membership de outro tenant e id inexistente respondem igual.
		http.Error(w, "membership not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to read membership", http.StatusInternalServerError)
		return
	}

	newRole, newStatus := currentRole, currentStatus
	if req.RoleKey != nil {
		newRole = *req.RoleKey
	}
	if req.Status != nil {
		newStatus = *req.Status
	}

	// ADR-0039: reativar a membership de alguém que já atua em outra instância é colocá-lo em mais de uma; isso é do
	// administrador do Hub. O lock por pessoa torna "perguntar e reativar" atômico (duas reativações simultâneas, em
	// empresas diferentes, não passam as duas).
	if currentStatus != "active" && newStatus == "active" {
		if err := lockPerson(r.Context(), q, targetUser); err != nil {
			http.Error(w, "failed to update membership", http.StatusInternalServerError)
			return
		}
		var elsewhere bool
		if err := q.QueryRow(r.Context(), `SELECT user_works_in_other_instance($1, $2)`, tc.TenantID, targetUser).Scan(&elsewhere); err != nil || elsewhere {
			http.Error(w, errOtherInstanceMessage, http.StatusConflict)
			return
		}
	}

	// O tenant não pode ficar sem nenhum administrador ativo: sem isso ninguém
	// mais consegue gerir acessos, e a recuperação exige intervenção no banco.
	losesAdmin := currentRole == "tenant_admin" && currentStatus == "active" &&
		(newRole != "tenant_admin" || newStatus != "active")
	if losesAdmin {
		var otherAdmins int
		// Trava TODOS os administradores ativos antes de contar: com só a linha do alvo travada, dois pedidos que rebaixam
		// administradores diferentes enxergam um ao outro como "o outro admin" e os dois passam (Codex).
		if err := q.QueryRow(r.Context(), `
			SELECT count(*) FROM (
			  SELECT m.id FROM memberships m
			  JOIN roles r ON r.id = m.role_id
			  WHERE m.tenant_id=$1 AND m.id<>$2 AND m.status='active' AND r.key='tenant_admin'
			  FOR UPDATE OF m) locked`,
			tc.TenantID, membershipID).Scan(&otherAdmins); err != nil {
			http.Error(w, "failed to check administrators", http.StatusInternalServerError)
			return
		}
		if otherAdmins == 0 {
			http.Error(w, "the tenant would be left without an active administrator", http.StatusConflict)
			return
		}
	}

	if _, err := q.Exec(r.Context(), `
		UPDATE memberships
		SET role_id = (SELECT id FROM roles WHERE key=$3 AND tenant_id IS NULL),
		    status = $4, updated_at = now()
		WHERE tenant_id=$1 AND id=$2`,
		tc.TenantID, membershipID, newRole, newStatus); err != nil {
		http.Error(w, "failed to update membership", http.StatusInternalServerError)
		return
	}

	h.record(r.Context(), tc, membershipID, targetUser, currentRole, newRole, currentStatus, newStatus)
	writeTeamJSON(w, map[string]any{
		"membership_id": membershipID,
		"role_key":      newRole,
		"status":        newStatus,
	})
}

func isAssignableRole(key string) bool {
	for _, k := range tenantAssignableRoles {
		if k == key {
			return true
		}
	}
	return false
}

// record grava a auditoria na mesma transação da mutação. Sem e-mail nem nome:
// o id do usuário basta para rastrear e evita espalhar PII pelo log.
func (h *TeamHandler) record(ctx context.Context, tc *domain.TenantContext, membershipID, targetUser uuid.UUID,
	prevRole, newRole, prevStatus, newStatus string) {
	if h.audit == nil {
		return
	}
	action := auditdomain.ActionMembershipRoleChanged
	switch {
	case newStatus == "revoked" && prevStatus != "revoked":
		action = auditdomain.ActionMembershipRevoked
	case newStatus == "inactive" && prevStatus != "inactive":
		action = auditdomain.ActionMembershipDeactivated
	case newStatus == "active" && prevStatus != "active":
		action = auditdomain.ActionMembershipReactivated
	}
	meta := map[string]interface{}{"target_user_id": targetUser.String()}
	if prevRole != newRole {
		meta["previous_role"] = prevRole
		meta["new_role"] = newRole
	}
	if prevStatus != newStatus {
		meta["previous_status"] = prevStatus
		meta["new_status"] = newStatus
	}
	_ = h.audit.Store(ctx, &auditdomain.AuditEvent{
		ID: uuid.New(), TenantID: tc.TenantID, ActorID: tc.ActorID, Action: action,
		ResourceType: auditdomain.ResourceMembership, ResourceID: membershipID,
		Outcome: auditdomain.OutcomeSuccess, CorrelationID: uuid.New(), CausationID: uuid.New(),
		Metadata: meta, CreatedAt: time.Now().UTC(),
	})
}

func writeTeamJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
