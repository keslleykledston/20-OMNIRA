package adapters

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/omnira/omnira/internal/testhelpers"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
	channelsdomain "github.com/omnira/omnira/internal/channels/domain"
	channelports "github.com/omnira/omnira/internal/channels/ports"
	"github.com/omnira/omnira/internal/tickets/ports"
)

// envDBURLs skips the test if the real-Postgres env vars are absent, and
// newPools opens the seed (superuser) and app (RLS-enforced) pools — same
// pattern as requireAttemptStack, duplicated locally for the two-fixture
// cross-tenant tests below.
func envDBURLs(t *testing.T) (string, string) {
	t.Helper()
	seedURL, appURL := testhelpers.RequireIntegrationDatabase(t)
	return seedURL, appURL
}

func newPools(t *testing.T, seedURL, appURL string) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	seed, err := pgxpool.New(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(seed.Close)
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	return seed, app
}

// PRODUCT.6-L real-Postgres proof: K3GTicketingRuntimeResolver resolves the
// tenant-scoped CompanyDirectory/TicketingConnector pair from the SAME
// existing K3G connection/credential infrastructure
// (internal/channels/adapters), never a duplicate credential, never a
// global fallback. testCipherKey mirrors the exact 32-byte literal already
// used by internal/channels/adapters/postgres_test.go.
const testCipherKey = "01234567890123456789012345678901"

func newRuntimeResolverStack(t *testing.T, f *attemptFixture) (*K3GTicketingRuntimeResolver, channelports.ChannelConnectionRepository, channelports.CredentialStore) {
	t.Helper()
	cipher, err := channelcrypto.NewAESGCM([]byte(testCipherKey))
	if err != nil {
		t.Fatalf("cipher: %v", err)
	}
	connRepo := channeladapters.NewPostgresChannelConnectionRepository(f.app)
	credStore := channeladapters.NewPostgresCredentialStore(f.app, cipher)
	return NewK3GTicketingRuntimeResolver(f.app, connRepo, credStore), connRepo, credStore
}

// seedK3GConnection stores a real credential + connection through the
// existing, already-tested channels adapters — never touching
// channel_credentials/channel_connections columns directly — proving this
// resolver reads exactly what the real configuration flow would write.
func seedK3GConnection(t *testing.T, f *attemptFixture, connRepo channelports.ChannelConnectionRepository, credStore channelports.CredentialStore, fields map[string]string) uuid.UUID {
	t.Helper()
	connID := uuid.New()
	conn := &channelsdomain.ChannelConnection{
		ID: connID, TenantID: f.tenantID, Channel: channelsdomain.ChannelERP, Provider: "k3g_crm",
		ProviderKind: channelsdomain.ProviderKindOfficial, ExternalNumberID: "resolver-test-" + connID.String(),
		Status: channelsdomain.ConnectionStatusActive, Capabilities: []channelsdomain.Capability{},
	}
	if err := f.withSystemSession(t, func(sctx context.Context) error { return connRepo.Store(sctx, conn) }); err != nil {
		t.Fatalf("store connection: %v", err)
	}
	var secretRef string
	if err := f.withSystemSession(t, func(sctx context.Context) error {
		var err error
		secretRef, err = credStore.Store(sctx, connID, channelports.Credential{Fields: fields})
		return err
	}); err != nil {
		t.Fatalf("store credential: %v", err)
	}
	conn.SecretRef = secretRef
	if err := f.withSystemSession(t, func(sctx context.Context) error { return connRepo.Update(sctx, conn) }); err != nil {
		t.Fatalf("update connection with secret_ref: %v", err)
	}
	return connID
}

// A. zero applicable connections.
func TestK3GTicketingRuntimeResolverNoConfiguration(t *testing.T) {
	f, _ := requireAttemptStack(t)
	resolver, _, _ := newRuntimeResolverStack(t, f)
	var resErr *ports.ResolutionError
	err := f.withSystemSession(t, func(ctx context.Context) error {
		_, err := resolver.Resolve(ctx, f.tenantID)
		return err
	})
	if !errors.As(err, &resErr) || resErr.Code != ports.ResolutionNoConfiguration {
		t.Fatalf("err = %v, want ResolutionNoConfiguration", err)
	}
}

// B. exactly one valid configuration resolves both capabilities.
func TestK3GTicketingRuntimeResolverResolvesBothCapabilities(t *testing.T) {
	f, _ := requireAttemptStack(t)
	resolver, connRepo, credStore := newRuntimeResolverStack(t, f)
	seedK3GConnection(t, f, connRepo, credStore, map[string]string{"base_url": "https://api.k3gsolutions.com.br", "token": "test-token-do-not-log"})

	var rt *ports.TicketingRuntime
	err := f.withSystemSession(t, func(ctx context.Context) error {
		var err error
		rt, err = resolver.Resolve(ctx, f.tenantID)
		return err
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt.CompanyDirectory == nil || rt.TicketingConnector == nil {
		t.Fatalf("expected both capabilities resolved, got %+v", rt)
	}
	if rt.TicketingConnector.Name() != "k3g" {
		t.Fatalf("TicketingConnector.Name() = %q, want %q (PRODUCT.6-L section 11: stable provider identity)", rt.TicketingConnector.Name(), "k3g")
	}
}

// C. tenant A cannot resolve using tenant B's tenant id / credential.
func TestK3GTicketingRuntimeResolverRejectsCrossTenantRequest(t *testing.T) {
	seedURL, appURL := envDBURLs(t)
	seed, app := newPools(t, seedURL, appURL)
	tenantA := newAttemptFixture(t, seed, app)
	tenantB := newAttemptFixture(t, seed, app)
	resolver, connRepo, credStore := newRuntimeResolverStack(t, tenantB)
	seedK3GConnection(t, tenantB, connRepo, credStore, map[string]string{"base_url": "https://api.k3gsolutions.com.br", "token": "t"})

	err := tenantA.withSystemSession(t, func(ctx context.Context) error {
		_, err := resolver.Resolve(ctx, tenantB.tenantID)
		return err
	})
	if err == nil {
		t.Fatal("expected an error resolving tenant B's id from tenant A's session")
	}
	var resErr *ports.ResolutionError
	if errors.As(err, &resErr) && resErr.Code != ports.ResolutionNoConfiguration {
		// Either the tenant-context mismatch guard fires (plain error) or,
		// if RLS alone were relied on, it would look like NoConfiguration
		// from tenant A's own (empty) view — both are safe failure shapes,
		// but never tenant B's real connection.
		t.Fatalf("unexpected resolution error code: %v", resErr.Code)
	}
}

// D. missing credential reference (secret_ref left NULL).
func TestK3GTicketingRuntimeResolverMissingCredentialReference(t *testing.T) {
	f, _ := requireAttemptStack(t)
	resolver, connRepo, _ := newRuntimeResolverStack(t, f)
	connID := uuid.New()
	conn := &channelsdomain.ChannelConnection{
		ID: connID, TenantID: f.tenantID, Channel: channelsdomain.ChannelERP, Provider: "k3g_crm",
		ProviderKind: channelsdomain.ProviderKindOfficial, ExternalNumberID: "resolver-test-" + connID.String(),
		Status: channelsdomain.ConnectionStatusActive, Capabilities: []channelsdomain.Capability{},
	}
	if err := f.withSystemSession(t, func(sctx context.Context) error { return connRepo.Store(sctx, conn) }); err != nil {
		t.Fatalf("store connection: %v", err)
	}

	var resErr *ports.ResolutionError
	err := f.withSystemSession(t, func(ctx context.Context) error {
		_, err := resolver.Resolve(ctx, f.tenantID)
		return err
	})
	if !errors.As(err, &resErr) || resErr.Code != ports.ResolutionCredentialNotFound {
		t.Fatalf("err = %v, want ResolutionCredentialNotFound", err)
	}
}

// E. malformed credential payload (missing required fields).
func TestK3GTicketingRuntimeResolverMalformedCredentialPayload(t *testing.T) {
	f, _ := requireAttemptStack(t)
	resolver, connRepo, credStore := newRuntimeResolverStack(t, f)
	seedK3GConnection(t, f, connRepo, credStore, map[string]string{"base_url": "https://api.k3gsolutions.com.br"}) // token missing

	var resErr *ports.ResolutionError
	err := f.withSystemSession(t, func(ctx context.Context) error {
		_, err := resolver.Resolve(ctx, f.tenantID)
		return err
	})
	if !errors.As(err, &resErr) || resErr.Code != ports.ResolutionCredentialInvalid {
		t.Fatalf("err = %v, want ResolutionCredentialInvalid", err)
	}
}

// F. multiple ambiguous applicable connections — never "first row wins".
func TestK3GTicketingRuntimeResolverAmbiguousConfiguration(t *testing.T) {
	f, _ := requireAttemptStack(t)
	resolver, connRepo, credStore := newRuntimeResolverStack(t, f)
	seedK3GConnection(t, f, connRepo, credStore, map[string]string{"base_url": "https://api.k3gsolutions.com.br", "token": "t1"})
	seedK3GConnection(t, f, connRepo, credStore, map[string]string{"base_url": "https://api.k3gsolutions.com.br", "token": "t2"})

	var resErr *ports.ResolutionError
	err := f.withSystemSession(t, func(ctx context.Context) error {
		_, err := resolver.Resolve(ctx, f.tenantID)
		return err
	})
	if !errors.As(err, &resErr) || resErr.Code != ports.ResolutionAmbiguousConfiguration {
		t.Fatalf("err = %v, want ResolutionAmbiguousConfiguration", err)
	}
}

// G. the same resolved secret is used to construct BOTH capabilities
// without any duplicate persistence — Resolve is read-only, so the
// channel_credentials row count for this tenant must be unchanged by it.
func TestK3GTicketingRuntimeResolverDoesNotDuplicateCredential(t *testing.T) {
	f, _ := requireAttemptStack(t)
	resolver, connRepo, credStore := newRuntimeResolverStack(t, f)
	seedK3GConnection(t, f, connRepo, credStore, map[string]string{"base_url": "https://api.k3gsolutions.com.br", "token": "t"})

	countBefore := countChannelCredentials(t, f)
	err := f.withSystemSession(t, func(ctx context.Context) error {
		rt, err := resolver.Resolve(ctx, f.tenantID)
		if err != nil {
			return err
		}
		if rt.CompanyDirectory == nil || rt.TicketingConnector == nil {
			t.Fatal("expected both capabilities from the single resolved credential")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	countAfter := countChannelCredentials(t, f)
	if countBefore != countAfter {
		t.Fatalf("channel_credentials row count changed from %d to %d — Resolve must never persist", countBefore, countAfter)
	}
}

func countChannelCredentials(t *testing.T, f *attemptFixture) int {
	t.Helper()
	var n int
	if err := f.seed.QueryRow(context.Background(), `SELECT count(*) FROM channel_credentials WHERE tenant_id = $1`, f.tenantID).Scan(&n); err != nil {
		t.Fatalf("count channel_credentials: %v", err)
	}
	return n
}

// H. no global/default connector fallback: a tenant with zero connections
// never resolves using another tenant's real, valid configuration.
func TestK3GTicketingRuntimeResolverNoGlobalFallback(t *testing.T) {
	seedURL, appURL := envDBURLs(t)
	seed, app := newPools(t, seedURL, appURL)
	configured := newAttemptFixture(t, seed, app)
	unconfigured := newAttemptFixture(t, seed, app)
	resolver, connRepo, credStore := newRuntimeResolverStack(t, configured)
	seedK3GConnection(t, configured, connRepo, credStore, map[string]string{"base_url": "https://api.k3gsolutions.com.br", "token": "t"})

	var resErr *ports.ResolutionError
	err := unconfigured.withSystemSession(t, func(ctx context.Context) error {
		_, err := resolver.Resolve(ctx, unconfigured.tenantID)
		return err
	})
	if !errors.As(err, &resErr) || resErr.Code != ports.ResolutionNoConfiguration {
		t.Fatalf("err = %v, want ResolutionNoConfiguration (no fallback to another tenant's real connection)", err)
	}
}
