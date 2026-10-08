package adapters

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/integrations/domain"
	"github.com/omnira/omnira/internal/integrations/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

var _ ports.IntegrationRepository = (*PostgresIntegrationRepository)(nil)

type PostgresIntegrationRepository struct {
	pool *pgxpool.Pool
}

func NewPostgresIntegrationRepository(pool *pgxpool.Pool) *PostgresIntegrationRepository {
	return &PostgresIntegrationRepository{pool: pool}
}

// ============ IntegrationInstance ============

func (r *PostgresIntegrationRepository) GetIntegrationInstance(ctx context.Context, id uuid.UUID) (*domain.IntegrationInstance, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	var inst domain.IntegrationInstance
	var metadata, response []byte

	err := q.QueryRow(ctx,
		`SELECT id, tenant_id, provider, integration_type, status, environment,
		        configuration_metadata, credential_reference, created_at, updated_at, last_error_at
		 FROM integration_instances WHERE id = $1`,
		id,
	).Scan(&inst.ID, &inst.TenantID, &inst.Provider, &inst.IntegrationType, &inst.Status,
		&inst.Environment, &metadata, &inst.CredentialReference, &inst.CreatedAt, &inst.UpdatedAt, &inst.LastErrorAt)

	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &inst.ConfigurationMetadata)
	}
	if len(response) > 0 {
		_ = json.Unmarshal(response, &inst.ConfigurationMetadata)
	}

	return &inst, nil
}

func (r *PostgresIntegrationRepository) ListIntegrationsByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.IntegrationInstance, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	rows, err := q.Query(ctx,
		`SELECT id, tenant_id, provider, integration_type, status, environment,
		        configuration_metadata, credential_reference, created_at, updated_at, last_error_at
		 FROM integration_instances WHERE tenant_id = $1 ORDER BY created_at DESC`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instances []*domain.IntegrationInstance
	for rows.Next() {
		var inst domain.IntegrationInstance
		var metadata []byte
		if err := rows.Scan(&inst.ID, &inst.TenantID, &inst.Provider, &inst.IntegrationType, &inst.Status,
			&inst.Environment, &metadata, &inst.CredentialReference, &inst.CreatedAt, &inst.UpdatedAt, &inst.LastErrorAt); err != nil {
			return nil, err
		}
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &inst.ConfigurationMetadata)
		}
		instances = append(instances, &inst)
	}
	return instances, rows.Err()
}

func (r *PostgresIntegrationRepository) ListIntegrationsByType(ctx context.Context, tenantID uuid.UUID, integrationType string) ([]*domain.IntegrationInstance, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	rows, err := q.Query(ctx,
		`SELECT id, tenant_id, provider, integration_type, status, environment,
		        configuration_metadata, credential_reference, created_at, updated_at, last_error_at
		 FROM integration_instances WHERE tenant_id = $1 AND integration_type = $2`,
		tenantID, integrationType,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var instances []*domain.IntegrationInstance
	for rows.Next() {
		var inst domain.IntegrationInstance
		var metadata []byte
		if err := rows.Scan(&inst.ID, &inst.TenantID, &inst.Provider, &inst.IntegrationType, &inst.Status,
			&inst.Environment, &metadata, &inst.CredentialReference, &inst.CreatedAt, &inst.UpdatedAt, &inst.LastErrorAt); err != nil {
			return nil, err
		}
		if len(metadata) > 0 {
			_ = json.Unmarshal(metadata, &inst.ConfigurationMetadata)
		}
		instances = append(instances, &inst)
	}
	return instances, rows.Err()
}

func (r *PostgresIntegrationRepository) CreateIntegrationInstance(ctx context.Context, instance *domain.IntegrationInstance) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	metadata, _ := json.Marshal(instance.ConfigurationMetadata)
	_, err := q.Exec(ctx,
		`INSERT INTO integration_instances
		 (id, tenant_id, provider, integration_type, status, environment, configuration_metadata, credential_reference, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		instance.ID, instance.TenantID, instance.Provider, instance.IntegrationType,
		instance.Status, instance.Environment, metadata, instance.CredentialReference,
		instance.CreatedAt, instance.UpdatedAt,
	)
	return err
}

func (r *PostgresIntegrationRepository) UpdateIntegrationInstance(ctx context.Context, instance *domain.IntegrationInstance) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	metadata, _ := json.Marshal(instance.ConfigurationMetadata)
	_, err := q.Exec(ctx,
		`UPDATE integration_instances SET status = $1, configuration_metadata = $2, updated_at = $3, last_error_at = $4
		 WHERE id = $5`,
		instance.Status, metadata, time.Now(), instance.LastErrorAt, instance.ID,
	)
	return err
}

// ============ IntegrationCapability ============

func (r *PostgresIntegrationRepository) GetCapability(ctx context.Context, instanceID uuid.UUID, capability string) (*domain.IntegrationCapability, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	var cap domain.IntegrationCapability
	err := q.QueryRow(ctx,
		`SELECT id, integration_instance_id, capability, enabled, created_at, updated_at
		 FROM integration_capabilities WHERE integration_instance_id = $1 AND capability = $2`,
		instanceID, capability,
	).Scan(&cap.ID, &cap.IntegrationInstanceID, &cap.Capability, &cap.Enabled, &cap.CreatedAt, &cap.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return &cap, err
}

func (r *PostgresIntegrationRepository) ListCapabilities(ctx context.Context, instanceID uuid.UUID) ([]*domain.IntegrationCapability, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	rows, err := q.Query(ctx,
		`SELECT id, integration_instance_id, capability, enabled, created_at, updated_at
		 FROM integration_capabilities WHERE integration_instance_id = $1`,
		instanceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var caps []*domain.IntegrationCapability
	for rows.Next() {
		var cap domain.IntegrationCapability
		if err := rows.Scan(&cap.ID, &cap.IntegrationInstanceID, &cap.Capability, &cap.Enabled, &cap.CreatedAt, &cap.UpdatedAt); err != nil {
			return nil, err
		}
		caps = append(caps, &cap)
	}
	return caps, rows.Err()
}

func (r *PostgresIntegrationRepository) CreateCapability(ctx context.Context, cap *domain.IntegrationCapability) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx,
		`INSERT INTO integration_capabilities (id, integration_instance_id, capability, enabled, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		cap.ID, cap.IntegrationInstanceID, cap.Capability, cap.Enabled, cap.CreatedAt, cap.UpdatedAt,
	)
	return err
}

func (r *PostgresIntegrationRepository) EnableCapability(ctx context.Context, instanceID uuid.UUID, capability string) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx,
		`UPDATE integration_capabilities SET enabled = true, updated_at = now()
		 WHERE integration_instance_id = $1 AND capability = $2`,
		instanceID, capability,
	)
	return err
}

func (r *PostgresIntegrationRepository) DisableCapability(ctx context.Context, instanceID uuid.UUID, capability string) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx,
		`UPDATE integration_capabilities SET enabled = false, updated_at = now()
		 WHERE integration_instance_id = $1 AND capability = $2`,
		instanceID, capability,
	)
	return err
}

// ============ ExternalActionReceipt ============

func (r *PostgresIntegrationRepository) GetReceiptByIdempotencyKey(ctx context.Context, instanceID uuid.UUID, key string) (*domain.ExternalActionReceipt, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	var receipt domain.ExternalActionReceipt
	var reqMeta, respMeta []byte
	err := q.QueryRow(ctx,
		`SELECT id, tenant_id, integration_instance_id, action_type, external_id, status,
		        actor_user_id, hub_id, conversation_id, correlation_id, idempotency_key,
		        sanitized_request_metadata, sanitized_response_metadata, created_at, confirmed_at, updated_at
		 FROM external_action_receipts WHERE integration_instance_id = $1 AND idempotency_key = $2`,
		instanceID, key,
	).Scan(&receipt.ID, &receipt.TenantID, &receipt.IntegrationInstanceID, &receipt.ActionType, &receipt.ExternalID, &receipt.Status,
		&receipt.ActorUserID, &receipt.HubID, &receipt.ConversationID, &receipt.CorrelationID, &receipt.IdempotencyKey,
		&reqMeta, &respMeta, &receipt.CreatedAt, &receipt.ConfirmedAt, &receipt.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(reqMeta, &receipt.SanitizedRequestMetadata)
	_ = json.Unmarshal(respMeta, &receipt.SanitizedResponseMetadata)
	return &receipt, nil
}

func (r *PostgresIntegrationRepository) CreateReceipt(ctx context.Context, receipt *domain.ExternalActionReceipt) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	reqMeta, _ := json.Marshal(receipt.SanitizedRequestMetadata)
	respMeta, _ := json.Marshal(receipt.SanitizedResponseMetadata)
	_, err := q.Exec(ctx,
		`INSERT INTO external_action_receipts
		 (id, tenant_id, integration_instance_id, action_type, external_id, status, actor_user_id, hub_id, conversation_id,
		  correlation_id, idempotency_key, sanitized_request_metadata, sanitized_response_metadata, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		receipt.ID, receipt.TenantID, receipt.IntegrationInstanceID, receipt.ActionType, receipt.ExternalID, receipt.Status,
		receipt.ActorUserID, receipt.HubID, receipt.ConversationID, receipt.CorrelationID, receipt.IdempotencyKey,
		reqMeta, respMeta, receipt.CreatedAt, receipt.UpdatedAt,
	)
	return err
}

func (r *PostgresIntegrationRepository) UpdateReceiptStatus(ctx context.Context, receiptID uuid.UUID, status, externalID string) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx,
		`UPDATE external_action_receipts SET status = $1, external_id = $2, confirmed_at = now(), updated_at = now()
		 WHERE id = $3`,
		status, externalID, receiptID,
	)
	return err
}

// ============ Webhook Deduplication ============

func (r *PostgresIntegrationRepository) CheckDuplicate(ctx context.Context, instanceID uuid.UUID, providerEventID string) (bool, error) {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	var count int
	err := q.QueryRow(ctx,
		`SELECT COUNT(*) FROM webhook_deduplication WHERE integration_instance_id = $1 AND provider_event_id = $2`,
		instanceID, providerEventID,
	).Scan(&count)
	return count > 0, err
}

func (r *PostgresIntegrationRepository) MarkProcessed(ctx context.Context, instanceID uuid.UUID, providerEventID string) error {
	q := platformdb.QuerierFromContext(ctx, r.pool)
	_, err := q.Exec(ctx,
		`INSERT INTO webhook_deduplication (id, integration_instance_id, provider_event_id, processed_at)
		 VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
		uuid.New(), instanceID, providerEventID, time.Now(),
	)
	return err
}
