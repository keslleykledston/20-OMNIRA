package adapters

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/meta"
	"github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/platform/db"
)

// MetaWebhookSecrets resolves the secrets of the unauthenticated Meta webhook PER CONNECTION (never a global one):
// the app secret that signs a payload and the verify token of the handshake. It reads the encrypted credential in the
// connection's own system tenant session, with the tenant taken from the persisted connection row.
type MetaWebhookSecrets struct {
	pool        *pgxpool.Pool
	repo        ports.ChannelConnectionRepository
	credentials ports.CredentialStore
}

var _ meta.SecretResolver = (*MetaWebhookSecrets)(nil)

func NewMetaWebhookSecrets(pool *pgxpool.Pool, repo ports.ChannelConnectionRepository, credentials ports.CredentialStore) *MetaWebhookSecrets {
	return &MetaWebhookSecrets{pool: pool, repo: repo, credentials: credentials}
}

func (s *MetaWebhookSecrets) credential(ctx context.Context, conn *domain.ChannelConnection) (ports.Credential, error) {
	if conn == nil || conn.Provider != domain.ProviderMetaCloud || conn.SecretRef == "" {
		return ports.Credential{}, errors.New("meta webhook: no credential")
	}
	var cred ports.Credential
	err := db.WithSystemTenantSession(ctx, s.pool, conn.TenantID, func(sc context.Context) error {
		var e error
		cred, e = s.credentials.Resolve(sc, conn.SecretRef)
		return e
	})
	return cred, err
}

func (s *MetaWebhookSecrets) AppSecret(ctx context.Context, conn *domain.ChannelConnection) (string, error) {
	cred, err := s.credential(ctx, conn)
	if err != nil {
		return "", err
	}
	return cred.Fields[meta.FieldAppSecret], nil
}

// CheckVerifyToken validates a handshake token. The token is "omn-<connection id>-<random>": the id finds the
// connection, the random part is compared in constant time with the one stored encrypted. Anything else is refused.
func (s *MetaWebhookSecrets) CheckVerifyToken(ctx context.Context, token string) (bool, error) {
	const prefix = "omn-"
	if !strings.HasPrefix(token, prefix) || len(token) < len(prefix)+36+2 {
		return false, nil
	}
	id, err := uuid.Parse(token[len(prefix) : len(prefix)+36])
	if err != nil {
		return false, nil
	}
	var conn *domain.ChannelConnection
	if err := db.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(sc context.Context) error {
		var e error
		conn, e = s.repo.FindByID(sc, id)
		return e
	}); err != nil {
		return false, err
	}
	if conn == nil || conn.Provider != domain.ProviderMetaCloud || conn.ProviderKind != domain.ProviderKindOfficial {
		return false, nil
	}
	cred, err := s.credential(ctx, conn)
	if err != nil {
		return false, err
	}
	want := cred.Fields[meta.FieldVerifyToken]
	return want != "" && subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1, nil
}
