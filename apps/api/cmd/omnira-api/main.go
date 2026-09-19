package main

import (
	"context"
	"flag"
	metachannel "github.com/omnira/omnira/internal/channels/meta"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
	"github.com/omnira/omnira/internal/channels/adapters/waha"
	channelapplication "github.com/omnira/omnira/internal/channels/application"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	inboxapplication "github.com/omnira/omnira/internal/inbox/application"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/platform/config"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/httpserver"
)

func main() {
	flag.Parse()

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config error: %v", err)
	}

	// Database
	dbPool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer dbPool.Close()

	if err := platformdb.RequireUnprivilegedRole(context.Background(), dbPool); err != nil {
		if !cfg.AllowPrivilegedDB {
			log.Fatalf("refusing to start: %v (set OMNIRA_ALLOW_PRIVILEGED_DB=true only for a deliberate, non-production exception)", err)
		}
		log.Printf("WARNING: %v -- RLS is NOT enforced for this process", err)
	}

	// NATS (optional for API)
	var nc *nats.Conn
	if cfg.NatsURL != "" {
		nc, err = nats.Connect(cfg.NatsURL)
		if err != nil {
			log.Printf("warning: failed to connect to NATS: %v\n", err)
		}
		if nc != nil {
			defer nc.Close()
		}
	}

	srv := httpserver.New(cfg.HTTPAddr)
	srv.SetupHealth(dbPool, nc)
	srv.SetupRateLimiting()
	srv.RegisterHealthHandlers()
	if cfg.AuthMode == "oidc" {
		resolver := authn.NewPostgresIdentityResolver(dbPool)
		oidcAuth, discovery, oidcErr := authn.NewOIDCAuthenticator(context.Background(), cfg.AuthIssuer, cfg.AuthAudience, nil, resolver)
		if oidcErr != nil {
			log.Fatalf("OIDC configuration error: %v", oidcErr)
		}
		srv.RegisterOIDCAuthHandlers(oidcAuth, authn.NewOIDCHandler(oidcAuth, discovery, resolver, cfg.AuthIssuer,
			cfg.AuthClientID, cfg.AuthClientSecret, cfg.AuthRedirectURL, cfg.AuthPostLoginURL, cfg.AuthCookieSecure))
	} else {
		srv.RegisterAuthHandlers(cfg.AuthCookieSecure)
	}
	srv.RegisterTenancyHandlers(dbPool)
	srv.RegisterInboxHandlers(dbPool)
	providerRegistry := channelapplication.NewMapProviderRegistry()
	permissions := channeladapters.NewPostgresPermissionChecker(dbPool)
	management := channelapplication.NewConnectionManagementService(providerRegistry, permissions)
	if err := providerRegistry.RegisterDescriptor(metachannel.Descriptor(), nil); err != nil {
		log.Fatalf("Meta provider descriptor error: %v", err)
	}
	wahaReason := ""
	if !cfg.WahaEnabled {
		wahaReason = "WAHA está desabilitado na configuração do servidor."
	}
	if cfg.WahaEnabled {
		cipher, cipherErr := channelcrypto.NewAESGCM(cfg.CredentialsKey)
		if cipherErr != nil {
			log.Fatalf("WAHA credential cipher error: %v", cipherErr)
		}
		credentialStore := channeladapters.NewPostgresCredentialStore(dbPool, cipher)
		connectionRepo := channeladapters.NewPostgresChannelConnectionRepository(dbPool)
		eventStore := channeladapters.NewPostgresWebhookEventStore(dbPool)
		client, clientErr := waha.NewClient(cfg.WahaBaseURL, cfg.WahaAPIKey, nil)
		if clientErr != nil {
			log.Fatalf("WAHA client config error: %v", clientErr)
		}
		provider, providerErr := waha.NewProvider(client, credentialStore)
		if providerErr != nil {
			log.Fatalf("WAHA provider config error: %v", providerErr)
		}
		if err := providerRegistry.RegisterDescriptor(waha.Descriptor(true, ""), provider); err != nil {
			log.Fatalf("WAHA provider descriptor error: %v", err)
		}
		resolver := channeladapters.NewWahaWebhookConnectionResolver(dbPool, connectionRepo)
		inboundStore := inboxadapters.NewPostgresInboundStore(dbPool)
		inboundService := inboxapplication.NewInboundService(inboundStore, inboundStore, inboundStore, inboxadapters.TicketStore{PostgresInboundStore: inboundStore}, inboundStore)
		intake := inboxadapters.NewWebhookIntake(dbPool, eventStore, inboundService)
		srv.RegisterWahaWebhook(waha.NewWebhookHandler(provider, resolver, eventStore).
			UseSession(func(ctx context.Context, tenantID uuid.UUID, fn func(context.Context) error) error {
				return platformdb.WithSystemTenantSession(ctx, dbPool, tenantID, fn)
			}).
			UseIntake(intake))
		wahaConnections := channelapplication.NewWahaConnectionService(
			connectionRepo, credentialStore, waha.NewSessionController(provider),
			permissions,
			channeladapters.NewChannelAuditRecorder(auditadapters.NewPostgresAuditEventRepository(dbPool)),
			cfg.PublicBaseURL,
		)
		management.Register("waha", wahaConnections)
		srv.RegisterWahaConnectionHandlers(dbPool, channeladapters.NewConnectionHandler(wahaConnections))
	} else if err := providerRegistry.RegisterDescriptor(waha.Descriptor(false, wahaReason), nil); err != nil {
		log.Fatalf("WAHA provider descriptor error: %v", err)
	}
	srv.RegisterChannelManagementHandlers(dbPool, channeladapters.NewManagementHandler(management))
	if cfg.MetaEnabled {
		if cfg.MetaVerifyToken == "" || cfg.MetaAppSecret == "" {
			log.Fatal("OMNIRA_META_VERIFY_TOKEN and OMNIRA_META_APP_SECRET are required when OMNIRA_META_ENABLED=true")
		}
		connectionRepo := channeladapters.NewPostgresChannelConnectionRepository(dbPool)
		eventStore := channeladapters.NewPostgresWebhookEventStore(dbPool)
		inboundStore := inboxadapters.NewPostgresInboundStore(dbPool)
		inboundService := inboxapplication.NewInboundService(inboundStore, inboundStore, inboundStore, inboxadapters.TicketStore{PostgresInboundStore: inboundStore}, inboundStore)
		srv.RegisterMetaWebhook(metachannel.Handler{
			VerifyToken: cfg.MetaVerifyToken,
			AppSecret:   cfg.MetaAppSecret,
			Resolver:    channeladapters.NewMetaWebhookConnectionResolver(dbPool, connectionRepo),
			Intake:      inboxadapters.NewWebhookIntake(dbPool, eventStore, inboundService),
		})
	}

	errChan := make(chan error, 1)
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		log.Printf("API starting on %s (env: %s)\n", cfg.HTTPAddr, cfg.Env)
		errChan <- srv.ListenAndServe(ctx)
	}()

	select {
	case err := <-errChan:
		if err != nil {
			log.Fatalf("server error: %v", err)
		}
	case sig := <-sigChan:
		log.Printf("signal received: %v, graceful shutdown in %d seconds...\n", sig, cfg.GracefulShutdown)
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Duration(cfg.GracefulShutdown)*time.Second)
		defer shutdownCancel()

		if err := srv.Shutdown(); err != nil {
			log.Printf("shutdown error: %v\n", err)
			os.Exit(1)
		}

		select {
		case <-srv.Done():
			log.Println("API shutdown complete")
		case <-shutdownCtx.Done():
			log.Println("shutdown timeout exceeded")
			os.Exit(1)
		}
	}
}
