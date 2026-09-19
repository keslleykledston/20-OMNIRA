package adapters

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/platform/db"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// PostgresCredentialStore stores only encrypted JSON. It intentionally does
// not expose a method that returns ciphertext to callers outside Resolve.
type PostgresCredentialStore struct {
	pool            *pgxpool.Pool
	cipher          ports.CredentialCipher
	resolutionTotal metric.Int64Counter
	resolutionError metric.Int64Counter
}

func NewPostgresCredentialStore(pool *pgxpool.Pool, cipher ports.CredentialCipher) ports.CredentialStore {
	meter := otel.Meter("omnira/channels")
	total, _ := meter.Int64Counter("credential_resolution_total")
	errors, _ := meter.Int64Counter("credential_resolution_error_total")
	return &PostgresCredentialStore{pool: pool, cipher: cipher, resolutionTotal: total, resolutionError: errors}
}

func (s *PostgresCredentialStore) Store(ctx context.Context, connectionID uuid.UUID, credential ports.Credential) (string, error) {
	payload, err := json.Marshal(credential.Fields)
	if err != nil {
		return "", fmt.Errorf("channel: marshal credential: %w", err)
	}
	ciphertext, err := s.cipher.Encrypt(payload)
	if err != nil {
		return "", fmt.Errorf("channel: encrypt credential: %w", err)
	}
	var id uuid.UUID
	err = db.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		INSERT INTO channel_credentials (tenant_id, connection_id, ciphertext)
		SELECT tenant_id, id, $2 FROM channel_connections WHERE id = $1
		ON CONFLICT (connection_id) DO UPDATE SET ciphertext = EXCLUDED.ciphertext, updated_at = now()
		RETURNING id`, connectionID, ciphertext).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("channel: store credential: %w", err)
	}
	return id.String(), nil
}

func (s *PostgresCredentialStore) Resolve(ctx context.Context, secretRef string) (ports.Credential, error) {
	s.resolutionTotal.Add(ctx, 1)
	id, err := uuid.Parse(secretRef)
	if err != nil {
		s.resolutionError.Add(ctx, 1)
		return ports.Credential{}, fmt.Errorf("channel: invalid secret ref: %w", err)
	}
	var ciphertext []byte
	err = db.QuerierFromContext(ctx, s.pool).QueryRow(ctx,
		`SELECT ciphertext FROM channel_credentials WHERE id = $1`, id).Scan(&ciphertext)
	if err != nil {
		s.resolutionError.Add(ctx, 1)
		return ports.Credential{}, fmt.Errorf("channel: resolve credential: %w", err)
	}
	payload, err := s.cipher.Decrypt(ciphertext)
	if err != nil {
		s.resolutionError.Add(ctx, 1)
		return ports.Credential{}, fmt.Errorf("channel: decrypt credential: %w", err)
	}
	var fields map[string]string
	if err := json.Unmarshal(payload, &fields); err != nil {
		s.resolutionError.Add(ctx, 1)
		return ports.Credential{}, fmt.Errorf("channel: decode credential: %w", err)
	}
	return ports.Credential{Fields: fields}, nil
}

func (s *PostgresCredentialStore) Rotate(ctx context.Context, secretRef string, credential ports.Credential) error {
	id, err := uuid.Parse(secretRef)
	if err != nil {
		return fmt.Errorf("channel: invalid secret ref: %w", err)
	}
	payload, err := json.Marshal(credential.Fields)
	if err != nil {
		return fmt.Errorf("channel: marshal credential: %w", err)
	}
	ciphertext, err := s.cipher.Encrypt(payload)
	if err != nil {
		return fmt.Errorf("channel: encrypt credential: %w", err)
	}
	result, err := db.QuerierFromContext(ctx, s.pool).Exec(ctx,
		`UPDATE channel_credentials SET ciphertext = $2, updated_at = now() WHERE id = $1`, id, ciphertext)
	if err != nil {
		return fmt.Errorf("channel: rotate credential: %w", err)
	}
	if result.RowsAffected() != 1 {
		return fmt.Errorf("channel: credential %s not found", id)
	}
	return nil
}

type PostgresChannelConnectionRepository struct{ pool *pgxpool.Pool }

func NewPostgresChannelConnectionRepository(pool *pgxpool.Pool) ports.ChannelConnectionRepository {
	return &PostgresChannelConnectionRepository{pool: pool}
}

// WahaWebhookConnectionResolver performs the unauthenticated callback lookup
// in an explicit system transaction. It derives tenant ownership from the
// persisted connection row; no tenant_id from webhook payload is accepted.
type WahaWebhookConnectionResolver struct {
	pool *pgxpool.Pool
	repo ports.ChannelConnectionRepository
}

type PostgresWebhookEventStore struct{ pool *pgxpool.Pool }

func NewPostgresWebhookEventStore(pool *pgxpool.Pool) ports.WebhookEventStore {
	return &PostgresWebhookEventStore{pool: pool}
}

func (s *PostgresWebhookEventStore) MarkReceived(ctx context.Context, connection domain.ChannelConnection, providerEventID, eventType, payloadDigest string) (bool, error) {
	var id uuid.UUID
	err := db.QuerierFromContext(ctx, s.pool).QueryRow(ctx, `
		INSERT INTO channel_webhook_events
			(tenant_id, connection_id, provider, provider_event_id, event_type, payload_digest)
		SELECT tenant_id, id, provider, $2, $3, $4
		FROM channel_connections
		WHERE id = $1 AND provider = $5
		ON CONFLICT (connection_id, provider_event_id) DO NOTHING
		RETURNING id`, connection.ID, providerEventID, eventType, payloadDigest, domain.ProviderWAHA).Scan(&id)
	if err == pgx.ErrNoRows {
		var exists bool
		if lookupErr := db.QuerierFromContext(ctx, s.pool).QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM channel_connections WHERE id = $1 AND provider = $2)`, connection.ID, domain.ProviderWAHA).Scan(&exists); lookupErr != nil {
			return false, fmt.Errorf("channel: verify webhook connection: %w", lookupErr)
		}
		if !exists {
			return false, fmt.Errorf("channel: webhook connection not eligible")
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("channel: reserve webhook event: %w", err)
	}
	return false, nil
}

func NewWahaWebhookConnectionResolver(pool *pgxpool.Pool, repo ports.ChannelConnectionRepository) *WahaWebhookConnectionResolver {
	return &WahaWebhookConnectionResolver{pool: pool, repo: repo}
}

func (r *WahaWebhookConnectionResolver) ResolveWahaConnection(ctx context.Context, connectionToken string) (*domain.ChannelConnection, error) {
	id, err := uuid.Parse(connectionToken)
	if err != nil {
		return nil, fmt.Errorf("channel: invalid WAHA connection token")
	}
	var connection *domain.ChannelConnection
	err = db.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(systemCtx context.Context) error {
		var lookupErr error
		connection, lookupErr = r.repo.FindByID(systemCtx, id)
		return lookupErr
	})
	if err != nil {
		return nil, err
	}
	if connection == nil || connection.Provider != domain.ProviderWAHA || connection.ProviderKind != domain.ProviderKindUnofficial {
		return nil, fmt.Errorf("channel: unknown WAHA connection")
	}
	return connection, nil
}

func (r *PostgresChannelConnectionRepository) Store(ctx context.Context, c *domain.ChannelConnection) error {
	caps, err := json.Marshal(c.Capabilities)
	if err != nil {
		return fmt.Errorf("channel: marshal capabilities: %w", err)
	}
	_, err = db.QuerierFromContext(ctx, r.pool).Exec(ctx, `
		INSERT INTO channel_connections
		(id, tenant_id, channel, provider, provider_kind, external_account_id,
		 external_number_id, provider_session_ref, status, capabilities, secret_ref,
		 risk_acknowledged_at, risk_acknowledged_by, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (id) DO UPDATE SET updated_at = EXCLUDED.updated_at`,
		c.ID, c.TenantID, string(c.Channel), c.Provider, string(c.ProviderKind),
		c.ExternalAccountID, c.ExternalNumberID, c.ProviderSessionRef, string(c.Status), caps,
		nullableUUID(c.SecretRef), c.RiskAcknowledgedAt, c.RiskAcknowledgedBy, c.CreatedAt, c.UpdatedAt)
	return err
}

func (r *PostgresChannelConnectionRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.ChannelConnection, error) {
	return r.find(ctx, `WHERE id = $1`, id)
}

func (r *PostgresChannelConnectionRepository) FindByExternalNumberID(ctx context.Context, provider, externalNumberID string) (*domain.ChannelConnection, error) {
	return r.find(ctx, `WHERE provider = $1 AND external_number_id = $2`, provider, externalNumberID)
}

func (r *PostgresChannelConnectionRepository) FindByTenant(ctx context.Context, tenantID uuid.UUID) ([]*domain.ChannelConnection, error) {
	rows, err := db.QuerierFromContext(ctx, r.pool).Query(ctx, connectionSelect+` WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*domain.ChannelConnection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (r *PostgresChannelConnectionRepository) Update(ctx context.Context, c *domain.ChannelConnection) error {
	return r.Store(ctx, c)
}

const connectionSelect = `SELECT id, tenant_id, channel, provider, provider_kind,
 external_account_id, external_number_id, provider_session_ref, status, capabilities,
 secret_ref, risk_acknowledged_at, risk_acknowledged_by, created_at, updated_at FROM channel_connections`

func (r *PostgresChannelConnectionRepository) find(ctx context.Context, suffix string, args ...any) (*domain.ChannelConnection, error) {
	row := db.QuerierFromContext(ctx, r.pool).QueryRow(ctx, connectionSelect+" "+suffix, args...)
	c, err := scanConnection(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return c, err
}

type rowScanner interface{ Scan(...any) error }

func scanConnection(row rowScanner) (*domain.ChannelConnection, error) {
	c := &domain.ChannelConnection{}
	var channel, kind, status string
	var caps []byte
	var secretRef *uuid.UUID
	if err := row.Scan(&c.ID, &c.TenantID, &channel, &c.Provider, &kind, &c.ExternalAccountID,
		&c.ExternalNumberID, &c.ProviderSessionRef, &status, &caps, &secretRef, &c.RiskAcknowledgedAt,
		&c.RiskAcknowledgedBy, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Channel, c.ProviderKind, c.Status = domain.Channel(channel), domain.ProviderKind(kind), domain.ConnectionStatus(status)
	if err := json.Unmarshal(caps, &c.Capabilities); err != nil {
		return nil, fmt.Errorf("channel: unmarshal capabilities: %w", err)
	}
	if secretRef != nil {
		c.SecretRef = secretRef.String()
	}
	return c, nil
}

func nullableUUID(ref string) *uuid.UUID {
	if ref == "" {
		return nil
	}
	id, err := uuid.Parse(ref)
	if err != nil {
		return nil
	}
	return &id
}
