package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
	"github.com/omnira/omnira/internal/channels/adapters/waha"
	channelapp "github.com/omnira/omnira/internal/channels/application"
	"github.com/omnira/omnira/internal/channels/domain"
	"github.com/omnira/omnira/internal/outbox/adapters"
	"github.com/omnira/omnira/internal/outbox/application"
	"github.com/omnira/omnira/internal/platform/config"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/health"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
	routingapp "github.com/omnira/omnira/internal/routing/application"
	"github.com/omnira/omnira/internal/worker/delivery"
	"github.com/omnira/omnira/internal/worker/publisher"
	"github.com/omnira/omnira/internal/worker/realtime"
	routingworker "github.com/omnira/omnira/internal/worker/routing"
)

func main() {
	flag.Parse()

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config error: %v", err)
	}

	// PostgreSQL connection
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("invalid OMNIRA_DATABASE_URL: %v", err)
	}
	// Publisher + routing + realtime bridge (1 permanent) + up to delivery.MaxInFlight deliveries that
	// each hold a connection during the provider call: never rely on the tiny library default.
	if poolCfg.MaxConns < 24 {
		poolCfg.MaxConns = 24
	}
	dbPool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer dbPool.Close()

	// Validate database connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := dbPool.Ping(ctx); err != nil {
		cancel()
		log.Fatalf("database ping failed: %v", err)
	}
	cancel()

	if err := platformdb.RequireUnprivilegedRole(context.Background(), dbPool); err != nil {
		if !cfg.AllowPrivilegedDB {
			log.Fatalf("refusing to start: %v (set OMNIRA_ALLOW_PRIVILEGED_DB=true only for a deliberate, non-production exception)", err)
		}
		log.Printf("WARNING: %v -- RLS is NOT enforced for this process", err)
	}

	// NATS connection
	nc, err := nats.Connect(cfg.NatsURL)
	if err != nil {
		log.Fatalf("failed to connect to NATS: %v", err)
	}
	defer nc.Close()

	// JetStream context
	js, err := jetstream.New(nc)
	if err != nil {
		log.Fatalf("failed to create JetStream context: %v", err)
	}

	// Repositories
	outboxRepo := adapters.NewPostgresOutboxRepository(dbPool)
	outboxSvc := application.NewOutboxService(outboxRepo)

	// Publisher
	pub := publisher.NewPublisher(outboxSvc, js, 10, 3)
	pub.SetSessionRunner(func(ctx context.Context, fn func(context.Context) error) error {
		return platformdb.WithTenantSession(ctx, dbPool, uuid.Nil, true, fn)
	})
	routingHandler, err := routingworker.NewHandler(
		routingworker.NewPostgresConversationRunner(dbPool),
		routingapp.NewService(routingadapters.NewPostgresAssignmentRepository(dbPool)),
	)
	if err != nil {
		log.Fatalf("failed to configure routing worker: %v", err)
	}

	// Health check
	hc := health.NewHealthCheck(dbPool, nc)

	// HTTP handlers for metrics/health
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", hc.HealthHandler)
	mux.HandleFunc("GET /metrics", hc.MetricsHandler)

	metricsServer := &http.Server{
		Addr:         "0.0.0.0:9090",
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	// Signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	routingConsumer, err := routingworker.StartConsumer(workerCtx, js, routingHandler)
	if err != nil {
		log.Fatalf("failed to start routing consumer: %v", err)
	}
	defer routingConsumer.Stop()

	// Realtime: Postgres NOTIFY (row triggers) -> NATS -> SSE. Best effort, ephemeral.
	go realtime.NewBridge(dbPool, nc).Run(workerCtx)

	// Outbound channel delivery (unofficial WhatsApp via WAHA).
	if cfg.WahaEnabled {
		cipher, err := channelcrypto.NewAESGCM(cfg.CredentialsKey)
		if err != nil {
			log.Fatalf("WAHA credential cipher error: %v", err)
		}
		client, err := waha.NewClient(cfg.WahaBaseURL, cfg.WahaAPIKey, nil)
		if err != nil {
			log.Fatalf("WAHA client config error: %v", err)
		}
		provider, err := waha.NewProvider(client, channeladapters.NewPostgresCredentialStore(dbPool, cipher))
		if err != nil {
			log.Fatalf("WAHA provider config error: %v", err)
		}
		registry := channelapp.NewMapProviderRegistry()
		registry.Register(domain.ProviderWAHA, provider)
		channelSvc := channelapp.NewChannelService(channeladapters.NewPostgresChannelConnectionRepository(dbPool), registry)
		deliveryHandler, err := delivery.NewHandler(delivery.NewPostgresOutboundStore(dbPool), channelSvc, delivery.MaxAttempts)
		if err != nil {
			log.Fatalf("failed to configure delivery worker: %v", err)
		}
		deliveryConsumer, err := delivery.StartConsumer(workerCtx, js, deliveryHandler)
		if err != nil {
			log.Fatalf("failed to start delivery consumer: %v", err)
		}
		defer deliveryConsumer.Stop()
		log.Printf("Outbound delivery consumer started (WAHA)\n")
	} else {
		log.Printf("Outbound delivery disabled (OMNIRA_WAHA_ENABLED != true)\n")
	}

	log.Printf("Worker starting (env: %s)\n", cfg.Env)
	log.Printf("Publish interval: 1 second\n")
	log.Printf("Metrics server: http://0.0.0.0:9090/metrics\n")

	// Metrics server
	go func() {
		if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("metrics server error: %v\n", err)
		}
	}()

	// Worker loop (publisher)
	go func() {
		if err := pub.Start(workerCtx, 1*time.Second); err != nil {
			log.Printf("publisher error: %v", err)
		}
	}()

	// Wait for signal
	sig := <-sigChan
	log.Printf("signal received: %v, graceful shutdown...\n", sig)
	workerCancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Duration(cfg.GracefulShutdown)*time.Second)
	defer shutdownCancel()

	// Shutdown metrics server
	metricsServer.Shutdown(shutdownCtx)

	select {
	case <-shutdownCtx.Done():
		log.Println("Worker shutdown timeout exceeded")
		os.Exit(1)
	default:
		log.Println("Worker shutdown complete")
	}
}
