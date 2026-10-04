package adapters

import (
	"context"
	"errors"
	"log"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/media/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// SecretOpener decrypts a stored secret (the same AES-256-GCM cipher the channel credentials use).
type SecretOpener interface {
	Decrypt(ciphertext []byte) ([]byte, error)
}

// TenantAIResolver reads the tenant's own Gemini configuration in a system session (the worker has no user). It returns
// nil unless the integration is enabled, which the table only allows with a key and a recorded consent. The plaintext key
// lives only in the returned struct, for the duration of one call.
type TenantAIResolver struct {
	pool   *pgxpool.Pool
	cipher SecretOpener
}

var _ ports.TenantAIResolver = (*TenantAIResolver)(nil)

func NewTenantAIResolver(pool *pgxpool.Pool, cipher SecretOpener) *TenantAIResolver {
	return &TenantAIResolver{pool: pool, cipher: cipher}
}

func (r *TenantAIResolver) Resolve(ctx context.Context, tenantID uuid.UUID) (*ports.TenantAI, error) {
	var out *ports.TenantAI
	err := platformdb.WithTenantSession(ctx, r.pool, uuid.Nil, true, func(sctx context.Context) error {
		var enabled bool
		var model string
		var budget float64
		var secret []byte
		err := platformdb.QuerierFromContext(sctx, r.pool).QueryRow(sctx, `
			SELECT enabled, model, monthly_budget_usd::float8, secret_ciphertext FROM tenant_ai_integrations WHERE tenant_id=$1 AND provider='gemini'`, tenantID).
			Scan(&enabled, &model, &budget, &secret)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !enabled || len(secret) == 0 {
			return nil
		}
		key, err := r.cipher.Decrypt(secret)
		if err != nil || len(key) == 0 {
			log.Printf("vision: the stored key of tenant %s cannot be read; treating the integration as off", tenantID)
			return nil
		}
		out = &ports.TenantAI{APIKey: string(key), Model: model, BudgetUSD: budget}
		return nil
	})
	return out, err
}
