package adapters

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/domain"
	"github.com/omnira/omnira/internal/hub/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

var _ ports.HubRepository = (*PostgresHubRepository)(nil)

type PostgresHubRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresHubRepository(pool *pgxpool.Pool) *PostgresHubRepository {
	return &PostgresHubRepository{pool: pool}
}

// ============ ServiceHub ============

func (r *PostgresHubRepository) GetServiceHubByID(ctx context.Context, hubID uuid.UUID) (*domain.ServiceHub, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	var hub domain.ServiceHub
	err := q.QueryRow(ctx,
		`SELECT id, name, description, status, created_at, updated_at
		 FROM service_hubs WHERE id = $1`,
		hubID,
	).Scan(&hub.ID, &hub.Name, &hub.Description, &hub.Status, &hub.CreatedAt, &hub.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &hub, nil
}

func (r *PostgresHubRepository) CreateServiceHub(ctx context.Context, hub *domain.ServiceHub) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx,
		`INSERT INTO service_hubs (id, name, description, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		hub.ID, hub.Name, hub.Description, hub.Status, hub.CreatedAt, hub.UpdatedAt,
	)
	return err
}

// ============ HubMembership ============

func (r *PostgresHubRepository) GetHubMembership(ctx context.Context, hubID, userID uuid.UUID) (*domain.HubMembership, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	var m domain.HubMembership
	err := q.QueryRow(ctx,
		`SELECT id, hub_id, user_id, role_id, created_at, updated_at
		 FROM hub_memberships WHERE hub_id = $1 AND user_id = $2`,
		hubID, userID,
	).Scan(&m.ID, &m.HubID, &m.UserID, &m.RoleID, &m.CreatedAt, &m.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *PostgresHubRepository) ListHubMemberships(ctx context.Context, hubID uuid.UUID) ([]*domain.HubMembership, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	rows, err := q.Query(ctx,
		`SELECT id, hub_id, user_id, role_id, created_at, updated_at
		 FROM hub_memberships WHERE hub_id = $1 ORDER BY created_at DESC`,
		hubID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memberships []*domain.HubMembership
	for rows.Next() {
		var m domain.HubMembership
		if err := rows.Scan(&m.ID, &m.HubID, &m.UserID, &m.RoleID, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		memberships = append(memberships, &m)
	}
	return memberships, rows.Err()
}

func (r *PostgresHubRepository) CreateHubMembership(ctx context.Context, membership *domain.HubMembership) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx,
		`INSERT INTO hub_memberships (id, hub_id, user_id, role_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		membership.ID, membership.HubID, membership.UserID, membership.RoleID,
		membership.CreatedAt, membership.UpdatedAt,
	)
	return err
}

// ============ ServiceContract ============

func (r *PostgresHubRepository) GetActiveServiceContract(ctx context.Context, hubID, tenantID uuid.UUID) (*domain.ServiceContract, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	var c domain.ServiceContract
	err := q.QueryRow(ctx,
		`SELECT id, hub_id, tenant_id, status, valid_from, valid_until, service_scope, created_at, updated_at
		 FROM hub_tenant_service_contracts
		 WHERE hub_id = $1 AND tenant_id = $2 AND status = 'active'`,
		hubID, tenantID,
	).Scan(&c.ID, &c.HubID, &c.TenantID, &c.Status, &c.ValidFrom, &c.ValidUntil, &c.ServiceScope, &c.CreatedAt, &c.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *PostgresHubRepository) GetServiceContract(ctx context.Context, contractID uuid.UUID) (*domain.ServiceContract, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	var c domain.ServiceContract
	err := q.QueryRow(ctx,
		`SELECT id, hub_id, tenant_id, status, valid_from, valid_until, service_scope, created_at, updated_at
		 FROM hub_tenant_service_contracts WHERE id = $1`,
		contractID,
	).Scan(&c.ID, &c.HubID, &c.TenantID, &c.Status, &c.ValidFrom, &c.ValidUntil, &c.ServiceScope, &c.CreatedAt, &c.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *PostgresHubRepository) ListServiceContractsByHub(ctx context.Context, hubID uuid.UUID) ([]*domain.ServiceContract, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	rows, err := q.Query(ctx,
		`SELECT id, hub_id, tenant_id, status, valid_from, valid_until, service_scope, created_at, updated_at
		 FROM hub_tenant_service_contracts WHERE hub_id = $1 ORDER BY created_at DESC`,
		hubID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contracts []*domain.ServiceContract
	for rows.Next() {
		var c domain.ServiceContract
		if err := rows.Scan(&c.ID, &c.HubID, &c.TenantID, &c.Status, &c.ValidFrom, &c.ValidUntil, &c.ServiceScope, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		contracts = append(contracts, &c)
	}
	return contracts, rows.Err()
}

func (r *PostgresHubRepository) ListServiceContractsByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.ServiceContract, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	rows, err := q.Query(ctx,
		`SELECT id, hub_id, tenant_id, status, valid_from, valid_until, service_scope, created_at, updated_at
		 FROM hub_tenant_service_contracts WHERE tenant_id = $1 ORDER BY created_at DESC`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contracts []*domain.ServiceContract
	for rows.Next() {
		var c domain.ServiceContract
		if err := rows.Scan(&c.ID, &c.HubID, &c.TenantID, &c.Status, &c.ValidFrom, &c.ValidUntil, &c.ServiceScope, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		contracts = append(contracts, &c)
	}
	return contracts, rows.Err()
}

func (r *PostgresHubRepository) CreateServiceContract(ctx context.Context, contract *domain.ServiceContract) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx,
		`INSERT INTO hub_tenant_service_contracts (id, hub_id, tenant_id, status, valid_from, valid_until, service_scope, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		contract.ID, contract.HubID, contract.TenantID, contract.Status,
		contract.ValidFrom, contract.ValidUntil, contract.ServiceScope,
		contract.CreatedAt, contract.UpdatedAt,
	)
	return err
}

func (r *PostgresHubRepository) UpdateServiceContractStatus(ctx context.Context, contractID uuid.UUID, status string) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx,
		`UPDATE hub_tenant_service_contracts SET status = $1, updated_at = now() WHERE id = $2`,
		status, contractID,
	)
	return err
}

// ============ EffectiveAccessGrant ============

func (r *PostgresHubRepository) GetEffectiveGrant(ctx context.Context, hubID, userID, tenantID uuid.UUID) (*domain.EffectiveAccessGrant, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	var g domain.EffectiveAccessGrant
	err := q.QueryRow(ctx,
		`SELECT id, hub_id, user_id, tenant_id, service_contract_id, work_pool_id, status, valid_from, valid_until, grant_version, created_at, updated_at
		 FROM effective_access_grants
		 WHERE hub_id = $1 AND user_id = $2 AND tenant_id = $3 AND status = 'active'`,
		hubID, userID, tenantID,
	).Scan(&g.ID, &g.HubID, &g.UserID, &g.TenantID, &g.ServiceContractID, &g.WorkPoolID,
		&g.Status, &g.ValidFrom, &g.ValidUntil, &g.GrantVersion, &g.CreatedAt, &g.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (r *PostgresHubRepository) ListEffectiveGrantsForUser(ctx context.Context, hubID, userID uuid.UUID) ([]*domain.EffectiveAccessGrant, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	rows, err := q.Query(ctx,
		`SELECT id, hub_id, user_id, tenant_id, service_contract_id, work_pool_id, status, valid_from, valid_until, grant_version, created_at, updated_at
		 FROM effective_access_grants
		 WHERE hub_id = $1 AND user_id = $2 AND status = 'active'
		 ORDER BY created_at DESC`,
		hubID, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var grants []*domain.EffectiveAccessGrant
	for rows.Next() {
		var g domain.EffectiveAccessGrant
		if err := rows.Scan(&g.ID, &g.HubID, &g.UserID, &g.TenantID, &g.ServiceContractID, &g.WorkPoolID,
			&g.Status, &g.ValidFrom, &g.ValidUntil, &g.GrantVersion, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		grants = append(grants, &g)
	}
	return grants, rows.Err()
}

func (r *PostgresHubRepository) ListEffectiveGrantsForTenant(ctx context.Context, hubID, tenantID uuid.UUID) ([]*domain.EffectiveAccessGrant, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	rows, err := q.Query(ctx,
		`SELECT id, hub_id, user_id, tenant_id, service_contract_id, work_pool_id, status, valid_from, valid_until, grant_version, created_at, updated_at
		 FROM effective_access_grants
		 WHERE hub_id = $1 AND tenant_id = $2 AND status = 'active'
		 ORDER BY created_at DESC`,
		hubID, tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var grants []*domain.EffectiveAccessGrant
	for rows.Next() {
		var g domain.EffectiveAccessGrant
		if err := rows.Scan(&g.ID, &g.HubID, &g.UserID, &g.TenantID, &g.ServiceContractID, &g.WorkPoolID,
			&g.Status, &g.ValidFrom, &g.ValidUntil, &g.GrantVersion, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, err
		}
		grants = append(grants, &g)
	}
	return grants, rows.Err()
}

func (r *PostgresHubRepository) CreateEffectiveGrant(ctx context.Context, grant *domain.EffectiveAccessGrant) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx,
		`INSERT INTO effective_access_grants (id, hub_id, user_id, tenant_id, service_contract_id, work_pool_id, status, valid_from, valid_until, grant_version, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		grant.ID, grant.HubID, grant.UserID, grant.TenantID, grant.ServiceContractID, grant.WorkPoolID,
		grant.Status, grant.ValidFrom, grant.ValidUntil, grant.GrantVersion, grant.CreatedAt, grant.UpdatedAt,
	)
	return err
}

func (r *PostgresHubRepository) InvalidateGrantsForContract(ctx context.Context, contractID uuid.UUID) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx,
		`UPDATE effective_access_grants SET status = 'revoked', updated_at = now()
		 WHERE service_contract_id = $1`,
		contractID,
	)
	return err
}

// ============ HubInboxItem ============

func (r *PostgresHubRepository) GetHubInboxItem(ctx context.Context, hubID, tenantID, conversationID uuid.UUID) (*domain.HubInboxItem, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	var i domain.HubInboxItem
	err := q.QueryRow(ctx,
		`SELECT id, hub_id, tenant_id, conversation_id, queue_id, assigned_user_id, customer_name, channel, status, priority, sla_due_at, last_activity_at, unread_count, metadata_json, version, created_at, updated_at
		 FROM hub_inbox_items
		 WHERE hub_id = $1 AND tenant_id = $2 AND conversation_id = $3`,
		hubID, tenantID, conversationID,
	).Scan(&i.ID, &i.HubID, &i.TenantID, &i.ConversationID, &i.QueueID, &i.AssignedUserID,
		&i.CustomerName, &i.Channel, &i.Status, &i.Priority, &i.SLADueAt, &i.LastActivityAt,
		&i.UnreadCount, &i.MetadataJSON, &i.Version, &i.CreatedAt, &i.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &i, nil
}

func (r *PostgresHubRepository) ListHubInboxItems(ctx context.Context, hubID uuid.UUID, limit int, cursor string) ([]*domain.HubInboxItem, string, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)

	if limit <= 0 || limit > 1000 {
		limit = 50
	}

	query := `SELECT id, hub_id, tenant_id, conversation_id, queue_id, assigned_user_id, customer_name, channel, status, priority, sla_due_at, last_activity_at, unread_count, metadata_json, version, created_at, updated_at
		FROM hub_inbox_items
		WHERE hub_id = $1`

	args := []interface{}{hubID}

	if cursor != "" {
		query += ` AND created_at < $2`
		args = append(args, cursor)
	}

	query += ` ORDER BY created_at DESC LIMIT $` + fmt.Sprintf("%d", len(args)+1)
	args = append(args, limit+1)

	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var items []*domain.HubInboxItem
	var lastCursor string

	for rows.Next() {
		if len(items) >= limit {
			break
		}
		var i domain.HubInboxItem
		if err := rows.Scan(&i.ID, &i.HubID, &i.TenantID, &i.ConversationID, &i.QueueID, &i.AssignedUserID,
			&i.CustomerName, &i.Channel, &i.Status, &i.Priority, &i.SLADueAt, &i.LastActivityAt,
			&i.UnreadCount, &i.MetadataJSON, &i.Version, &i.CreatedAt, &i.UpdatedAt); err != nil {
			return nil, "", err
		}
		items = append(items, &i)
		lastCursor = i.CreatedAt.String()
	}

	// Check if there are more results
	moreResults := len(items) > limit
	if moreResults {
		items = items[:limit]
	}

	nextCursor := ""
	if len(items) > 0 && moreResults {
		nextCursor = lastCursor
	}

	return items, nextCursor, rows.Err()
}

func (r *PostgresHubRepository) UpsertHubInboxItem(ctx context.Context, item *domain.HubInboxItem) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx,
		`INSERT INTO hub_inbox_items (id, hub_id, tenant_id, conversation_id, queue_id, assigned_user_id, customer_name, channel, status, priority, sla_due_at, last_activity_at, unread_count, metadata_json, version, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		 ON CONFLICT (hub_id, tenant_id, conversation_id) DO UPDATE SET
		 queue_id=$5, assigned_user_id=$6, customer_name=$7, channel=$8, status=$9, priority=$10,
		 sla_due_at=$11, last_activity_at=$12, unread_count=$13, metadata_json=$14, version=$15, updated_at=$17`,
		item.ID, item.HubID, item.TenantID, item.ConversationID, item.QueueID, item.AssignedUserID,
		item.CustomerName, item.Channel, item.Status, item.Priority, item.SLADueAt, item.LastActivityAt,
		item.UnreadCount, item.MetadataJSON, item.Version, item.CreatedAt, item.UpdatedAt,
	)
	return err
}

func (r *PostgresHubRepository) DeleteHubInboxItem(ctx context.Context, hubID, tenantID, conversationID uuid.UUID) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx,
		`DELETE FROM hub_inbox_items WHERE hub_id = $1 AND tenant_id = $2 AND conversation_id = $3`,
		hubID, tenantID, conversationID,
	)
	return err
}

// ============ WorkPool, Skills, etc. (stub) ============

func (r *PostgresHubRepository) GetWorkPool(ctx context.Context, poolID uuid.UUID) (*domain.WorkPool, error) {
	return nil, errors.New("not yet implemented")
}

func (r *PostgresHubRepository) ListWorkPoolsByHub(ctx context.Context, hubID uuid.UUID) ([]*domain.WorkPool, error) {
	return nil, errors.New("not yet implemented")
}

func (r *PostgresHubRepository) CreateWorkPool(ctx context.Context, pool *domain.WorkPool) error {
	return errors.New("not yet implemented")
}

func (r *PostgresHubRepository) GetSkill(ctx context.Context, skillID uuid.UUID) (*domain.Skill, error) {
	return nil, errors.New("not yet implemented")
}

func (r *PostgresHubRepository) ListSkillsByHub(ctx context.Context, hubID uuid.UUID) ([]*domain.Skill, error) {
	return nil, errors.New("not yet implemented")
}

func (r *PostgresHubRepository) CreateSkill(ctx context.Context, skill *domain.Skill) error {
	return errors.New("not yet implemented")
}

func (r *PostgresHubRepository) GetAgentSkill(ctx context.Context, userID, skillID uuid.UUID) (*domain.AgentSkill, error) {
	return nil, errors.New("not yet implemented")
}

func (r *PostgresHubRepository) ListAgentSkills(ctx context.Context, userID uuid.UUID) ([]*domain.AgentSkill, error) {
	return nil, errors.New("not yet implemented")
}

func (r *PostgresHubRepository) CreateAgentSkill(ctx context.Context, agentSkill *domain.AgentSkill) error {
	return errors.New("not yet implemented")
}
