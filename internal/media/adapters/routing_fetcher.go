package adapters

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/channels/meta"
	chports "github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/media/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
)

// RoutingFetcher picks the fetcher by the provider of the connection the message arrived on. WAHA files keep the
// existing path-rebased fetch; Meta files need the connection's own token (id → short-lived URL → download), resolved
// from the encrypted store in that connection's tenant session and never logged.
type RoutingFetcher struct {
	pool        *pgxpool.Pool
	credentials chports.CredentialStore
	waha        ports.Fetcher // may be nil when WAHA is off
	meta        *meta.Client  // may be nil when Meta is off
}

var (
	_ ports.Fetcher     = (*RoutingFetcher)(nil)
	_ ports.WorkFetcher = (*RoutingFetcher)(nil)
)

func NewRoutingFetcher(pool *pgxpool.Pool, credentials chports.CredentialStore, waha ports.Fetcher, metaClient *meta.Client) *RoutingFetcher {
	return &RoutingFetcher{pool: pool, credentials: credentials, waha: waha, meta: metaClient}
}

// Fetch (no connection known) can only be a WAHA reference.
func (f *RoutingFetcher) Fetch(ctx context.Context, mediaRef string) ([]byte, string, error) {
	if f.waha == nil {
		return nil, "", fmt.Errorf("%w: no fetcher for this reference", ports.ErrSourceGone)
	}
	return f.waha.Fetch(ctx, mediaRef)
}

func (f *RoutingFetcher) FetchWork(ctx context.Context, w ports.Work) ([]byte, string, error) {
	var provider, secretRef string
	err := platformdb.WithSystemTenantSession(ctx, f.pool, w.TenantID, func(sc context.Context) error {
		return platformdb.QuerierFromContext(sc, f.pool).QueryRow(sc,
			`SELECT provider, COALESCE(secret_ref::text, '') FROM channel_connections WHERE tenant_id = $1 AND id = $2`,
			w.TenantID, w.ConnectionID).Scan(&provider, &secretRef)
	})
	if err != nil {
		return nil, "", err
	}
	if provider != domain.ProviderMetaCloud {
		return f.Fetch(ctx, w.MediaRef)
	}
	if f.meta == nil || secretRef == "" {
		return nil, "", fmt.Errorf("%w: meta media without credential", ports.ErrSourceGone)
	}
	var cred chports.Credential
	if err := platformdb.WithSystemTenantSession(ctx, f.pool, w.TenantID, func(sc context.Context) error {
		var e error
		cred, e = f.credentials.Resolve(sc, secretRef)
		return e
	}); err != nil {
		return nil, "", err
	}
	data, mime, err := f.meta.FetchMedia(ctx, cred.Fields[meta.FieldAccessToken], w.MediaRef)
	if err != nil {
		// a permanent refusal (expired/deleted media, host not allowed) will never succeed; transient ones retry
		if errors.Is(err, chports.ErrPermanent) || errors.Is(err, chports.ErrMediaSourceNotAllowed) {
			return nil, "", ports.ErrSourceGone
		}
		return nil, "", err
	}
	return data, mime, nil
}
