package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/outbox/domain"
	"github.com/omnira/omnira/internal/outbox/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// PostgresOutboxRepository — implementação PostgreSQL do OutboxEventRepository.
type PostgresOutboxRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresOutboxRepository — cria um novo PostgresOutboxRepository.
func NewPostgresOutboxRepository(pool *pgxpool.Pool) ports.OutboxEventRepository {
	return &PostgresOutboxRepository{pool: pool}
}

// Store — armazena um novo evento no outbox.
func (r *PostgresOutboxRepository) Store(ctx context.Context, event *domain.OutboxEvent) error {
	payloadJSON, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	query := `
		INSERT INTO outbox_events
		(id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, causation_id, payload, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`

	_, err = platformdb.QuerierFromContext(ctx, r.pool).Exec(ctx, query,
		event.ID,
		event.TenantID,
		string(event.EventType),
		string(event.AggregateType),
		event.AggregateID,
		event.CorrelationID,
		event.CausationID,
		payloadJSON,
		event.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to store outbox event: %w", err)
	}

	return nil
}

// FindByID — busca evento por ID.
func (r *PostgresOutboxRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.OutboxEvent, error) {
	query := `
		SELECT id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, causation_id,
		       payload, published_at, attempts, created_at
		FROM outbox_events
		WHERE id = $1
	`

	row := platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx, query, id)
	event, err := scanOutboxEvent(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find outbox event: %w", err)
	}

	return event, nil
}

// FindUnpublished — busca eventos não publicados.
//
// A row whose aggregate_id/correlation_id/causation_id (loosely-typed TEXT
// columns every current writer nonetheless always populates with a UUID
// string) is not a valid UUID is a permanent, isolated failure — not a
// transient one — and must not abort the batch for every valid event behind
// it in created_at order (confirmed live during IAM4.2 pilot verification:
// one malformed row wedged the publisher indefinitely). Such a row is
// quarantined (marked, never deleted — evidence stays queryable) and
// excluded from future scans; the loop continues to the next row. A scan
// failure NOT explained by one of these three columns is treated as before
// (aborts the batch) — that class is a real DB/connection anomaly, which is
// transient and should be retried, not permanently isolated.
func (r *PostgresOutboxRepository) FindUnpublished(ctx context.Context, limit int) ([]*domain.OutboxEvent, error) {
	query := `
		SELECT id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, causation_id,
		       payload, published_at, attempts, created_at
		FROM outbox_events
		WHERE published_at IS NULL AND quarantined_at IS NULL
		ORDER BY created_at ASC
		LIMIT $1
	`

	rows, err := platformdb.QuerierFromContext(ctx, r.pool).Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query unpublished events: %w", err)
	}

	var events []*domain.OutboxEvent
	var toQuarantine []quarantineCandidate
	for rows.Next() {
		event, invalid, err := scanOutboxEventTolerant(rows)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}
		if invalid != nil {
			toQuarantine = append(toQuarantine, *invalid)
			continue
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}
	rows.Close()

	for _, q := range toQuarantine {
		// Best-effort: a failure to record the quarantine must not itself
		// block the valid events already collected above from publishing.
		// The row remains in the next batch's WHERE scan and gets retried
		// (harmless — the same isolation logic applies again).
		_, _ = platformdb.QuerierFromContext(ctx, r.pool).Exec(ctx,
			`UPDATE outbox_events SET quarantined_at = now(), quarantine_reason = $2
			 WHERE id = $1 AND quarantined_at IS NULL`,
			q.id, q.reason)
	}

	return events, nil
}

// quarantineCandidate names one row found to be permanently unpublishable
// and why — bounded, no payload/PII, only the specific column and parse
// error class.
type quarantineCandidate struct {
	id     uuid.UUID
	reason string
}

const maxQuarantineReasonLen = 200

func quarantineReason(column string, err error) string {
	reason := fmt.Sprintf("invalid %s: %v", column, err)
	if len(reason) > maxQuarantineReasonLen {
		reason = reason[:maxQuarantineReasonLen]
	}
	return reason
}

// Update — atualiza um evento.
func (r *PostgresOutboxRepository) Update(ctx context.Context, event *domain.OutboxEvent) error {
	payloadJSON, err := json.Marshal(event.Payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	query := `
		UPDATE outbox_events
		SET payload = $1, published_at = $2, attempts = $3
		WHERE id = $4
	`

	result, err := platformdb.QuerierFromContext(ctx, r.pool).Exec(ctx, query, payloadJSON, event.PublishedAt, event.Attempts, event.ID)
	if err != nil {
		return fmt.Errorf("failed to update outbox event: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("event not found: %s", event.ID)
	}

	return nil
}

// Delete — deleta um evento.
func (r *PostgresOutboxRepository) Delete(ctx context.Context, id uuid.UUID) error {
	query := `DELETE FROM outbox_events WHERE id = $1`

	result, err := platformdb.QuerierFromContext(ctx, r.pool).Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("failed to delete outbox event: %w", err)
	}

	if result.RowsAffected() == 0 {
		return fmt.Errorf("event not found: %s", id)
	}

	return nil
}

// FindByTenant — busca eventos por tenant.
func (r *PostgresOutboxRepository) FindByTenant(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]*domain.OutboxEvent, error) {
	query := `
		SELECT id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, causation_id,
		       payload, published_at, attempts, created_at
		FROM outbox_events
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`

	rows, err := platformdb.QuerierFromContext(ctx, r.pool).Query(ctx, query, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query events by tenant: %w", err)
	}
	defer rows.Close()

	var events []*domain.OutboxEvent
	for rows.Next() {
		event, err := scanOutboxEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}
		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return events, nil
}

// scanOutboxEvent — helper para ler OutboxEvent da query.
func scanOutboxEvent(row interface {
	Scan(dest ...interface{}) error
}) (*domain.OutboxEvent, error) {
	var (
		id            uuid.UUID
		tenantID      uuid.UUID
		eventType     string
		aggregateType string
		aggregateID   uuid.UUID
		correlationID uuid.UUID
		causationID   uuid.UUID
		payloadJSON   []byte
		publishedAt   *time.Time
		attempts      int
		createdAt     time.Time
	)

	err := row.Scan(
		&id,
		&tenantID,
		&eventType,
		&aggregateType,
		&aggregateID,
		&correlationID,
		&causationID,
		&payloadJSON,
		&publishedAt,
		&attempts,
		&createdAt,
	)
	if err != nil {
		return nil, err
	}

	var payload map[string]interface{}
	if len(payloadJSON) > 0 {
		if err := json.Unmarshal(payloadJSON, &payload); err != nil {
			return nil, fmt.Errorf("failed to unmarshal payload: %w", err)
		}
	}

	event := &domain.OutboxEvent{
		ID:            id,
		TenantID:      tenantID,
		EventType:     domain.EventType(eventType),
		AggregateType: domain.AggregateType(aggregateType),
		AggregateID:   aggregateID,
		CorrelationID: correlationID,
		CausationID:   causationID,
		Payload:       payload,
		PublishedAt:   publishedAt,
		Attempts:      attempts,
		CreatedAt:     createdAt,
	}

	return event, nil
}

// scanOutboxEventTolerant is FindUnpublished's scan path: aggregate_id,
// correlation_id and causation_id are scanned as raw strings first — never
// directly into uuid.UUID — so a single malformed value can never abort
// row.Scan for the whole row (which is what let one poisoned row wedge the
// entire batch, see FindUnpublished). Returns (event, nil, nil) on success,
// (nil, invalid, nil) when this specific row is unpublishable but the scan
// itself succeeded (quarantine it and keep going), or (nil, nil, err) only
// for a genuine, unrelated scan/connection failure (still batch-aborting —
// that class is transient, not a poisoned row).
func scanOutboxEventTolerant(row interface {
	Scan(dest ...interface{}) error
}) (*domain.OutboxEvent, *quarantineCandidate, error) {
	var (
		id               uuid.UUID
		tenantID         uuid.UUID
		eventType        string
		aggregateType    string
		aggregateIDStr   string
		correlationIDStr *string
		causationIDStr   *string
		payloadJSON      []byte
		publishedAt      *time.Time
		attempts         int
		createdAt        time.Time
	)

	if err := row.Scan(
		&id,
		&tenantID,
		&eventType,
		&aggregateType,
		&aggregateIDStr,
		&correlationIDStr,
		&causationIDStr,
		&payloadJSON,
		&publishedAt,
		&attempts,
		&createdAt,
	); err != nil {
		return nil, nil, err
	}

	aggregateID, err := uuid.Parse(aggregateIDStr)
	if err != nil {
		return nil, &quarantineCandidate{id: id, reason: quarantineReason("aggregate_id", err)}, nil
	}

	var correlationID uuid.UUID
	if correlationIDStr != nil {
		if correlationID, err = uuid.Parse(*correlationIDStr); err != nil {
			return nil, &quarantineCandidate{id: id, reason: quarantineReason("correlation_id", err)}, nil
		}
	}

	var causationID uuid.UUID
	if causationIDStr != nil {
		if causationID, err = uuid.Parse(*causationIDStr); err != nil {
			return nil, &quarantineCandidate{id: id, reason: quarantineReason("causation_id", err)}, nil
		}
	}

	var payload map[string]interface{}
	if len(payloadJSON) > 0 {
		if err := json.Unmarshal(payloadJSON, &payload); err != nil {
			return nil, &quarantineCandidate{id: id, reason: quarantineReason("payload", err)}, nil
		}
	}

	return &domain.OutboxEvent{
		ID:            id,
		TenantID:      tenantID,
		EventType:     domain.EventType(eventType),
		AggregateType: domain.AggregateType(aggregateType),
		AggregateID:   aggregateID,
		CorrelationID: correlationID,
		CausationID:   causationID,
		Payload:       payload,
		PublishedAt:   publishedAt,
		Attempts:      attempts,
		CreatedAt:     createdAt,
	}, nil, nil
}
