package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
	"github.com/omnira/omnira/internal/channels/adapters/waha"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	inboxapplication "github.com/omnira/omnira/internal/inbox/application"
	"github.com/omnira/omnira/internal/platform/config"
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
	srv.RegisterAuthHandlers()
	srv.RegisterTenancyHandlers(dbPool)
	srv.RegisterInboxHandlers(dbPool)
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
		resolver := channeladapters.NewWahaWebhookConnectionResolver(dbPool, connectionRepo)
		inboundStore := inboxadapters.NewPostgresInboundStore(dbPool)
		inboundService := inboxapplication.NewInboundService(inboundStore, inboundStore, inboundStore, inboxadapters.TicketStore{PostgresInboundStore: inboundStore}, inboundStore)
		intake := inboxadapters.NewWebhookIntake(dbPool, eventStore, inboundService)
		srv.RegisterWahaWebhook(waha.NewWebhookHandler(provider, resolver, eventStore).UseIntake(intake))
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
