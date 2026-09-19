package httpserver

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/platform/health"
	"github.com/omnira/omnira/internal/platform/ratelimit"
)

type Server struct {
	srv         *http.Server
	mux         *http.ServeMux
	addr        string
	ln          net.Listener
	closed      chan struct{}
	closedOnce  bool
	health      *health.HealthCheck
	rateLimiter *ratelimit.Limiter
	privateKey  *rsa.PrivateKey
}

func New(addr string) *Server {
	mux := http.NewServeMux()
	s := &Server{
		addr:        addr,
		mux:         mux,
		closed:      make(chan struct{}),
		rateLimiter: ratelimit.NewLimiter(),
	}
	s.srv = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	return s
}

// SetupRateLimiting — ativa rate limiting no mux.
func (s *Server) SetupRateLimiting() {
	middleware := ratelimit.Middleware(s.rateLimiter)
	s.srv.Handler = middleware(s.mux)
}

// SetupHealth — configura health check com dependências.
func (s *Server) SetupHealth(dbPool *pgxpool.Pool, natsConn *nats.Conn) {
	s.health = health.NewHealthCheck(dbPool, natsConn)
}

// RegisterHealthHandlers — registra handlers de health/ready check.
func (s *Server) RegisterHealthHandlers() {
	// /healthz — liveness + readiness (comprehensive health check)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if s.health == nil {
			// Fallback se health não foi configurado
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
			return
		}
		s.health.HealthHandler(w, r)
	})

	// /metrics — Prometheus format
	s.mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		if s.health == nil {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("# No metrics available\n"))
			return
		}
		s.health.MetricsHandler(w, r)
	})

	// Backward compatibility
	s.mux.HandleFunc("GET /internal/health/live", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	s.mux.HandleFunc("GET /internal/health/ready", func(w http.ResponseWriter, r *http.Request) {
		if s.health == nil || s.health.IsHealthy() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ready"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"status":"not ready"}`))
	})

	s.mux.HandleFunc("GET /internal/health/modules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"modules":{"platform":"ok"}}`))
	})
}

// RegisterAuthHandlers — registra endpoints de autenticação (incluindo mock login para testes)
func (s *Server) RegisterAuthHandlers() {
	// Gerar chave RSA para JWT (use valores reais em produção)
	privateKey, _, err := authn.GenerateTestRSAKeys()
	if err != nil {
		// Fallback: chave simplificada
		privateKey = nil
	}
	s.privateKey = privateKey

	if s.privateKey == nil {
		// Health check apenas
		s.mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": "auth not configured"})
		})
		return
	}

	authHandler := authn.NewAuthHandler(s.privateKey)
	s.mux.HandleFunc("POST /api/v1/auth/login", authHandler.MockLogin)
	s.mux.HandleFunc("GET /api/v1/auth/health", authHandler.HealthCheck)
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}
	s.ln = ln

	go func() {
		<-ctx.Done()
		s.Shutdown()
	}()

	return s.srv.Serve(ln)
}

func (s *Server) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !s.closedOnce {
		s.closedOnce = true
		close(s.closed)
	}
	return s.srv.Shutdown(ctx)
}

func (s *Server) Done() <-chan struct{} {
	return s.closed
}
