package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	channelsdomain "github.com/omnira/omnira/internal/channels/domain"
	channelports "github.com/omnira/omnira/internal/channels/ports"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	"github.com/omnira/omnira/internal/tickets/ports"
	"github.com/omnira/omnira/internal/tool/connectors"
)

// k3gConnectionProvider is the provider identifier already used, stably,
// by internal/tool/connectors.K3GCRMDescriptor and the dev tenant's real
// configured connection (channel='erp', provider='k3g_crm'). PRODUCT.6-L
// section 11: kept as-is — renaming it now would create projection/
// configuration compatibility problems for no benefit.
const k3gConnectionProvider = "k3g_crm"

// K3GTicketingRuntimeResolver implements ports.TicketingRuntimeResolver by
// REUSING the existing K3G CRM connection/credential
// (internal/channels/adapters), the same one internal/tool/connectors'
// K3GCRMClient and the CreateActivity flow already use — PRODUCT.6-L
// deliberately does not create a second credential, a new integration
// table, or a generic ERP registry (REUSE over CREATE).
type K3GTicketingRuntimeResolver struct {
	pool  *pgxpool.Pool
	conns channelports.ChannelConnectionRepository
	creds channelports.CredentialStore
}

func NewK3GTicketingRuntimeResolver(pool *pgxpool.Pool, conns channelports.ChannelConnectionRepository, creds channelports.CredentialStore) *K3GTicketingRuntimeResolver {
	return &K3GTicketingRuntimeResolver{pool: pool, conns: conns, creds: creds}
}

// Resolve implements PRODUCT.6-L sections 4-8. It never falls back to
// another tenant, a hardcoded connection, or a default credential: every
// non-nil error is a *ports.ResolutionError describing exactly why no
// runtime could be built for this tenant.
func (r *K3GTicketingRuntimeResolver) Resolve(ctx context.Context, tenantID uuid.UUID) (*ports.TicketingRuntime, error) {
	if tenantID == uuid.Nil {
		return nil, errors.New("tickets: tenant id required to resolve ticketing runtime")
	}
	// Defense in depth: the caller's own tenant session (RLS) already
	// scopes internal/channels queries to this tenant, but a resolver
	// that fails closed on ANY mismatch between the requested tenantID
	// and the session's own tenant context can never be tricked into
	// reading another tenant's connection even by a caller bug.
	tc, err := tenancydomain.FromContext(ctx)
	if err != nil || tc.TenantID == uuid.Nil || tc.TenantID != tenantID {
		return nil, errors.New("tickets: tenant context does not match requested tenant for runtime resolution")
	}

	connections, err := r.conns.FindByTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("tickets: load channel connections: %w", err)
	}
	var applicable []*channelsdomain.ChannelConnection
	for _, c := range connections {
		if c.TenantID == tenantID && c.Provider == k3gConnectionProvider && c.Channel == channelsdomain.ChannelERP {
			applicable = append(applicable, c)
		}
	}
	switch len(applicable) {
	case 0:
		return nil, &ports.ResolutionError{Code: ports.ResolutionNoConfiguration, Message: "no K3G connection configured for this tenant"}
	case 1:
		// fall through
	default:
		return nil, &ports.ResolutionError{Code: ports.ResolutionAmbiguousConfiguration, Message: fmt.Sprintf("%d applicable K3G connections found, expected exactly 1", len(applicable))}
	}
	conn := applicable[0]

	// PRODUCT.6-L section 6: persisted connection status is configuration
	// bookkeeping, not proof the K3G API is currently reachable — this
	// resolver only requires a usable, decryptable credential. A live
	// provider outage surfaces later as a connectors.TicketingError from
	// an actual call, never as a resolution failure here.
	if strings.TrimSpace(conn.SecretRef) == "" {
		return nil, &ports.ResolutionError{Code: ports.ResolutionCredentialNotFound, Message: "connection has no credential reference"}
	}
	secretRefID, err := uuid.Parse(conn.SecretRef)
	if err != nil {
		return nil, &ports.ResolutionError{Code: ports.ResolutionCredentialInvalid, Message: "connection credential reference is not a valid identifier"}
	}

	// A discriminating existence check BEFORE calling CredentialStore.Resolve
	// lets this resolver distinguish "no such credential row"
	// (CREDENTIAL_NOT_FOUND) from "row exists but can't be decrypted/used"
	// (CREDENTIAL_INVALID) without changing the shared
	// internal/channels/ports.CredentialStore contract or exposing any
	// secret material — this query only ever returns a boolean.
	var exists bool
	if err := platformdb.QuerierFromContext(ctx, r.pool).QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM channel_credentials WHERE id = $1)`, secretRefID).Scan(&exists); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("tickets: check credential existence: %w", err)
	}
	if !exists {
		return nil, &ports.ResolutionError{Code: ports.ResolutionCredentialNotFound, Message: "referenced credential row does not exist"}
	}

	credential, err := r.creds.Resolve(ctx, conn.SecretRef)
	if err != nil {
		// Never wraps or logs the credential itself — CredentialStore.Resolve
		// never returns secret material inside its own error.
		return nil, &ports.ResolutionError{Code: ports.ResolutionCredentialInvalid, Message: "credential could not be decrypted"}
	}
	baseURL := strings.TrimSpace(credential.Fields["base_url"])
	token := strings.TrimSpace(credential.Fields["token"])
	if baseURL == "" || token == "" {
		return nil, &ports.ResolutionError{Code: ports.ResolutionCredentialInvalid, Message: "credential payload is missing base_url or token"}
	}

	crmClient, err := connectors.NewK3GCRMClient(connectors.K3GCRMConfig{BaseURL: baseURL, Token: token})
	if err != nil {
		return nil, &ports.ResolutionError{Code: ports.ResolutionCredentialInvalid, Message: "credential is not usable to construct the CRM client"}
	}
	ticketingConnector, err := connectors.NewK3GTicketingConnector(connectors.K3GTicketingConfig{BaseURL: baseURL, Token: token})
	if err != nil {
		return nil, &ports.ResolutionError{Code: ports.ResolutionCredentialInvalid, Message: "credential is not usable to construct the ticketing connector"}
	}

	return &ports.TicketingRuntime{
		CompanyDirectory:   NewK3GCompanyDirectory(crmClient),
		TicketingConnector: ticketingConnector,
		// PRODUCT.7B2B: the exact connection this runtime was built from —
		// never re-derived later, never assumed to still be "the same one".
		ConnectionID: conn.ID,
	}, nil
}
