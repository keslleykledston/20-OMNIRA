package adapters

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// AgentItem representa um agente/técnico disponível para convite
type AgentItem struct {
	ID    uuid.UUID `json:"id"`
	Email string    `json:"email"`
	Name  string    `json:"name"`
}

// AgentsHandler serve endpoints de agentes/técnicos de um tenant
type AgentsHandler struct {
	pool *pgxpool.Pool
}

func NewAgentsHandler(pool *pgxpool.Pool) *AgentsHandler {
	return &AgentsHandler{pool: pool}
}

// ListAgents retorna todos os agentes ativos de um tenant
// GET /tenants/{tenant_id}/users/agents
// Retorna agentes com role=tenant_agent e status=active
func (h *AgentsHandler) ListAgents(w http.ResponseWriter, r *http.Request) {
	// TEMPORARY SEMANTIC COUPLING: este endpoint só alimenta o seletor de convidar/
	// transferir co-atendente, operações que já exigem conversation.manage do ator
	// (routing/application/participant.go). Reavaliar quando as permissões de
	// Agent/Queue forem consolidadas; não criar agent.read só por nomenclatura.
	tc, err := (&TeamHandler{pool: h.pool}).authorize(r, "conversation.manage")
	if err != nil {
		respondAuthzError(w, err)
		return
	}
	tenantID := tc.TenantID

	// Query: usuarios ativos com role tenant_agent
	rows, err := platformdb.QuerierFromContext(r.Context(), h.pool).Query(r.Context(), `
		SELECT u.id, u.email, u.external_subject
		FROM users u
		INNER JOIN memberships m ON m.user_id = u.id
		INNER JOIN roles r ON r.id = m.role_id
		WHERE m.tenant_id = $1
		  AND r.key = 'tenant_agent'
		  AND m.status = 'active'
		  AND u.status = 'active'
		ORDER BY u.email ASC
	`, tenantID)
	if err != nil {
		http.Error(w, "failed to list agents", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	agents := make([]AgentItem, 0)
	for rows.Next() {
		var id uuid.UUID
		var email, name string
		if err := rows.Scan(&id, &email, &name); err != nil {
			http.Error(w, "failed to read agents", http.StatusInternalServerError)
			return
		}
		// external_subject pode ser vazio; usar email como fallback
		if name == "" {
			name = email
		}
		agents = append(agents, AgentItem{
			ID:    id,
			Email: email,
			Name:  name,
		})
	}

	if err := rows.Err(); err != nil {
		http.Error(w, "error reading agents", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"items": agents,
	})
}

// Helper para extrair tenant_id da requisição
