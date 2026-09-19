package adapters

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/audit/domain"
	"github.com/omnira/omnira/internal/audit/ports"
	"github.com/omnira/omnira/internal/platform/db"
)

// PostgresAuditEventRepository — implementação PostgreSQL.
type PostgresAuditEventRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresAuditEventRepository — cria um novo PostgresAuditEventRepository.
func NewPostgresAuditEventRepository(pool *pgxpool.Pool) ports.AuditEventRepository {
	return &PostgresAuditEventRepository{pool: pool}
}

func (r *PostgresAuditEventRepository) Store(ctx context.Context, event *domain.AuditEvent) error {
	metadataJSON, _ := json.Marshal(event.Metadata)

	const query = `
		INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, causation_id, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO NOTHING
	`
	_, err := db.QuerierFromContext(ctx, r.pool).Exec(ctx, query,
		event.ID,
		event.TenantID,
		event.ActorID,
		string(event.Action),
		string(event.ResourceType),
		event.ResourceID,
		string(event.Outcome),
		event.CorrelationID,
		event.CausationID,
		metadataJSON,
		event.CreatedAt,
	)
	return err
}

func (r *PostgresAuditEventRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.AuditEvent, error) {
	const query = `
		SELECT id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, causation_id, metadata, created_at
		FROM audit_events
		WHERE id = $1
	`
	row := db.QuerierFromContext(ctx, r.pool).QueryRow(ctx, query, id)
	event := &domain.AuditEvent{}
	var metadataJSON []byte

	err := row.Scan(
		&event.ID,
		&event.TenantID,
		&event.ActorID,
		(*string)(&event.Action),
		(*string)(&event.ResourceType),
		&event.ResourceID,
		(*string)(&event.Outcome),
		&event.CorrelationID,
		&event.CausationID,
		&metadataJSON,
		&event.CreatedAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if metadataJSON != nil {
		json.Unmarshal(metadataJSON, &event.Metadata)
	} else {
		event.Metadata = make(map[string]interface{})
	}

	return event, nil
}

func (r *PostgresAuditEventRepository) FindByTenantAndCorrelation(ctx context.Context, tenantID, correlationID uuid.UUID) ([]*domain.AuditEvent, error) {
	const query = `
		SELECT id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, causation_id, metadata, created_at
		FROM audit_events
		WHERE tenant_id = $1 AND correlation_id = $2
		ORDER BY created_at DESC
	`
	rows, err := db.QuerierFromContext(ctx, r.pool).Query(ctx, query, tenantID, correlationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanAuditEvents(rows)
}

func (r *PostgresAuditEventRepository) FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.AuditEvent, error) {
	const query = `
		SELECT id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, causation_id, metadata, created_at
		FROM audit_events
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := db.QuerierFromContext(ctx, r.pool).Query(ctx, query, tenantID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanAuditEvents(rows)
}

func (r *PostgresAuditEventRepository) FindByAction(ctx context.Context, tenantID uuid.UUID, action domain.AuditAction, limit, offset int) ([]*domain.AuditEvent, error) {
	const query = `
		SELECT id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, causation_id, metadata, created_at
		FROM audit_events
		WHERE tenant_id = $1 AND action = $2
		ORDER BY created_at DESC
		LIMIT $3 OFFSET $4
	`
	rows, err := db.QuerierFromContext(ctx, r.pool).Query(ctx, query, tenantID, string(action), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanAuditEvents(rows)
}

// Helper

func scanAuditEvents(rows pgx.Rows) ([]*domain.AuditEvent, error) {
	var events []*domain.AuditEvent

	for rows.Next() {
		event := &domain.AuditEvent{}
		var metadataJSON []byte

		err := rows.Scan(
			&event.ID,
			&event.TenantID,
			&event.ActorID,
			(*string)(&event.Action),
			(*string)(&event.ResourceType),
			&event.ResourceID,
			(*string)(&event.Outcome),
			&event.CorrelationID,
			&event.CausationID,
			&metadataJSON,
			&event.CreatedAt,
		)
		if err != nil {
			return nil, err
		}

		if metadataJSON != nil {
			json.Unmarshal(metadataJSON, &event.Metadata)
		} else {
			event.Metadata = make(map[string]interface{})
		}

		events = append(events, event)
	}

	return events, rows.Err()
}
