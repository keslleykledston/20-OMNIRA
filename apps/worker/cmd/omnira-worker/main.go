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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/omnira/omnira/internal/outbox/adapters"
	"github.com/omnira/omnira/internal/outbox/application"
	"github.com/omnira/omnira/internal/platform/config"
	"github.com/omnira/omnira/internal/platform/health"
	"github.com/omnira/omnira/internal/worker/publisher"
)

func main() {
	flag.Parse()

	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config error: %v", err)
	}

	// PostgreSQL connection
	dbPool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
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

	log.Printf("Worker starting (env: %s)\n", cfg.Env)
	log.Printf("PostgreSQL: %s\n", cfg.DatabaseURL)
	log.Printf("NATS: %s\n", cfg.NatsURL)
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
