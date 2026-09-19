package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/platform/db"
)

// PostgresChannelConnectionRepository implementa ports.ChannelConnectionRepository.
// Persiste ChannelConnection em channel_connections table (tenant-owned, RLS+FORCE).
type PostgresChannelConnectionRepository struct {
	db          *sql.DB
	credStore   ports.CredentialStore
}

func NewPostgresChannelConnectionRepository(db *sql.DB, credStore ports.CredentialStore) *PostgresChannelConnectionRepository {
	return &PostgresChannelConnectionRepository{
		db:        db,
		credStore: credStore,
	}
}

// Store persiste ChannelConnection. Se credential for fornecida (em campo temporário da struct),
// delega a CredentialStore para armazenar e atualiza SecretRef antes de persistir.
// Atual D3.1: SecretRef é preenchido externamente; Store não toca credential.
func (r *PostgresChannelConnectionRepository) Store(ctx context.Context, conn *domain.ChannelConnection) error {
	if conn.ID == uuid.Nil {
		return fmt.Errorf("ChannelConnection.ID must not be nil")
	}
	if conn.TenantID == uuid.Nil {
		return fmt.Errorf("ChannelConnection.TenantID must not be nil")
	}

	tenantCtx := db.TenantContextFrom(ctx)
	if tenantCtx == nil || tenantCtx.ID != conn.TenantID {
		return fmt.Errorf("TenantContext mismatch: context tenant %v, conn tenant %v",
			tenantCtx.ID, conn.TenantID)
	}

	// Converter Capabilities slice → PostgreSQL array
	capabilitiesArray := make([]string, len(conn.Capabilities))
	for i, cap := range conn.Capabilities {
		capabilitiesArray[i] = string(cap)
	}

	// INSERT channel_connections
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO channel_connections (
			id, tenant_id, channel, provider, provider_kind,
			external_account_id, external_number_id, status, capabilities, secret_ref,
			risk_acknowledged_at, risk_acknowledged_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW(), NOW())`,
		conn.ID, conn.TenantID, string(conn.Channel), conn.Provider, string(conn.ProviderKind),
		conn.ExternalAccountID, conn.ExternalNumberID, string(conn.Status), pq.Array(capabilitiesArray),
		conn.SecretRef, conn.RiskAcknowledgedAt, conn.RiskAcknowledgedBy,
	)
	if err != nil {
		return fmt.Errorf("failed to insert channel_connections: %w", err)
	}

	return nil
}

// FindByID retorna ChannelConnection por ID (sem descriptografar credential).
// RLS policy garante que só conexões do tenant atual são retornadas.
func (r *PostgresChannelConnectionRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.ChannelConnection, error) {
	tenantCtx := db.TenantContextFrom(ctx)
	if tenantCtx == nil || tenantCtx.ID == uuid.Nil {
		return nil, fmt.Errorf("TenantContext not available")
	}

	row := r.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, channel, provider, provider_kind,
		        external_account_id, external_number_id, status, capabilities, secret_ref,
		        risk_acknowledged_at, risk_acknowledged_by, created_at, updated_at
		 FROM channel_connections
		 WHERE id = $1 AND tenant_id = $2`,
		id, tenantCtx.ID,
	)

	return scanChannelConnection(row)
}

// FindByExternalNumberID resolve ChannelConnection por provider + externalNumberID.
// A fonte confiável para webhook inbound (NUNCA resolver tenant do payload).
// RLS policy garante resultado apenas para tenant atual.
func (r *PostgresChannelConnectionRepository) FindByExternalNumberID(
	ctx context.Context,
	provider string,
	externalNumberID string,
) (*domain.ChannelConnection, error) {
	tenantCtx := db.TenantContextFrom(ctx)
	if tenantCtx == nil || tenantCtx.ID == uuid.Nil {
		return nil, fmt.Errorf("TenantContext not available")
	}

	row := r.db.QueryRowContext(ctx,
		`SELECT id, tenant_id, channel, provider, provider_kind,
		        external_account_id, external_number_id, status, capabilities, secret_ref,
		        risk_acknowledged_at, risk_acknowledged_by, created_at, updated_at
		 FROM channel_connections
		 WHERE provider = $1 AND external_number_id = $2 AND tenant_id = $3`,
		provider, externalNumberID, tenantCtx.ID,
	)

	return scanChannelConnection(row)
}

// FindByTenant retorna todas ChannelConnections do tenant.
// RLS policy garante resultado apenas para tenant atual.
func (r *PostgresChannelConnectionRepository) FindByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.ChannelConnection, error) {
	tenantCtx := db.TenantContextFrom(ctx)
	if tenantCtx == nil || tenantCtx.ID != tenantID {
		return nil, fmt.Errorf("TenantContext mismatch")
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT id, tenant_id, channel, provider, provider_kind,
		        external_account_id, external_number_id, status, capabilities, secret_ref,
		        risk_acknowledged_at, risk_acknowledged_by, created_at, updated_at
		 FROM channel_connections
		 WHERE tenant_id = $1
		 ORDER BY created_at DESC`,
		tenantID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query channel_connections: %w", err)
	}
	defer rows.Close()

	var conns []*domain.ChannelConnection
	for rows.Next() {
		conn, err := scanChannelConnectionFromRows(rows)
		if err != nil {
			return nil, err
		}
		conns = append(conns, conn)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return conns, nil
}

// Update atualiza ChannelConnection existente.
// Tipicamente used para mudar status, capabilities, ou risk acknowledgement.
func (r *PostgresChannelConnectionRepository) Update(ctx context.Context, conn *domain.ChannelConnection) error {
	tenantCtx := db.TenantContextFrom(ctx)
	if tenantCtx == nil || tenantCtx.ID != conn.TenantID {
		return fmt.Errorf("TenantContext mismatch")
	}

	capabilitiesArray := make([]string, len(conn.Capabilities))
	for i, cap := range conn.Capabilities {
		capabilitiesArray[i] = string(cap)
	}

	result, err := r.db.ExecContext(ctx,
		`UPDATE channel_connections
		 SET provider_kind = $1, status = $2, capabilities = $3,
		     risk_acknowledged_at = $4, risk_acknowledged_by = $5,
		     updated_at = NOW()
		 WHERE id = $6 AND tenant_id = $7`,
		string(conn.ProviderKind), string(conn.Status), pq.Array(capabilitiesArray),
		conn.RiskAcknowledgedAt, conn.RiskAcknowledgedBy,
		conn.ID, conn.TenantID,
	)
	if err != nil {
		return fmt.Errorf("failed to update channel_connections: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}

	if rowsAffected == 0 {
		return fmt.Errorf("channel connection not found (RLS or invalid ID)")
	}

	return nil
}

// Scanners

func scanChannelConnection(row *sql.Row) (*domain.ChannelConnection, error) {
	var conn domain.ChannelConnection
	var capabilitiesArray pq.StringArray
	var riskAckAt sql.NullTime
	var riskAckBy sql.NullString

	err := row.Scan(
		&conn.ID, &conn.TenantID, (*string)(&conn.Channel), &conn.Provider, (*string)(&conn.ProviderKind),
		&conn.ExternalAccountID, &conn.ExternalNumberID, (*string)(&conn.Status), &capabilitiesArray,
		&conn.SecretRef, &riskAckAt, &riskAckBy, &conn.CreatedAt, &conn.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("channel connection not found")
		}
		return nil, fmt.Errorf("failed to scan row: %w", err)
	}

	// Converter PostgreSQL array → Capabilities slice
	conn.Capabilities = make([]domain.Capability, len(capabilitiesArray))
	for i, cap := range capabilitiesArray {
		conn.Capabilities[i] = domain.Capability(cap)
	}

	// Handle nullable fields
	if riskAckAt.Valid {
		conn.RiskAcknowledgedAt = &riskAckAt.Time
	}
	if riskAckBy.Valid {
		id, err := uuid.Parse(riskAckBy.String)
		if err == nil {
			conn.RiskAcknowledgedBy = &id
		}
	}

	return &conn, nil
}

func scanChannelConnectionFromRows(rows *sql.Rows) (*domain.ChannelConnection, error) {
	var conn domain.ChannelConnection
	var capabilitiesArray pq.StringArray
	var riskAckAt sql.NullTime
	var riskAckBy sql.NullString

	err := rows.Scan(
		&conn.ID, &conn.TenantID, (*string)(&conn.Channel), &conn.Provider, (*string)(&conn.ProviderKind),
		&conn.ExternalAccountID, &conn.ExternalNumberID, (*string)(&conn.Status), &capabilitiesArray,
		&conn.SecretRef, &riskAckAt, &riskAckBy, &conn.CreatedAt, &conn.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to scan row: %w", err)
	}

	conn.Capabilities = make([]domain.Capability, len(capabilitiesArray))
	for i, cap := range capabilitiesArray {
		conn.Capabilities[i] = domain.Capability(cap)
	}

	if riskAckAt.Valid {
		conn.RiskAcknowledgedAt = &riskAckAt.Time
	}
	if riskAckBy.Valid {
		id, err := uuid.Parse(riskAckBy.String)
		if err == nil {
			conn.RiskAcknowledgedBy = &id
		}
	}

	return &conn, nil
}
