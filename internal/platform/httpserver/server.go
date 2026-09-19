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
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	auditapplication "github.com/omnira/omnira/internal/audit/application"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/platform/health"
	"github.com/omnira/omnira/internal/platform/ratelimit"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
)

// Issuer/audience do JWT mock — precisam bater com os valores gravados nas
// claims em internal/platform/authn/mock_login.go.
const (
	mockJWTIssuer   = "omnira-mock"
	mockJWTAudience = "omnira-api"
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
	publicKey   *rsa.PublicKey
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
	// Gerar chave RSA para JWT (use valores reais em produção).
	// A mesma keypair é reutilizada por RegisterTenancyHandlers para
	// verificar os tokens emitidos aqui — por isso fica salva no Server em
	// vez de local à função.
	privateKey, publicKey, err := authn.GenerateTestRSAKeys()
	if err != nil {
		// Fallback: chave simplificada
		privateKey = nil
	}
	s.privateKey = privateKey
	s.publicKey = publicKey

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

// RegisterTenancyHandlers — registra as rotas REST do M01/R0.1 Tenant
// Management: GET /api/v1/me, GET /api/v1/tenants, GET/PATCH
// /api/v1/tenants/{tenant_id}, membros e auditoria. Requer que
// RegisterAuthHandlers já tenha rodado (usa a mesma keypair RSA).
func (s *Server) RegisterTenancyHandlers(dbPool *pgxpool.Pool) {
	if s.publicKey == nil {
		// Sem chave pública não há como verificar tokens; não registrar
		// rotas que dependeriam de autenticação funcional.
		return
	}

	jwtAuth := authn.NewJWTAuthenticator(s.publicKey, mockJWTIssuer, mockJWTAudience)
	authnMiddleware := authn.Middleware(jwtAuth)

	tenantRepo := tenancyadapters.NewPostgresTenantRepository(dbPool)
	memberRepo := tenancyadapters.NewPostgresMembershipRepository(dbPool)
	roleRepo := tenancyadapters.NewPostgresRoleRepository(dbPool)

	tenantSvc := tenancyapplication.NewTenantService(tenantRepo)
	membershipSvc := tenancyapplication.NewMembershipService(memberRepo, roleRepo)
	authzSvc := tenancyapplication.NewAuthorizationService(memberRepo, tenantRepo)

	tenantHandler := tenancyadapters.NewTenantAPIHandler(tenantSvc, membershipSvc)

	auditRepo := auditadapters.NewPostgresAuditEventRepository(dbPool)
	auditSvc := auditapplication.NewAuditService(auditRepo)
	auditHandler := auditadapters.NewAuditAPIHandler(auditSvc)

	userSession := tenancyadapters.UserSessionMiddleware(dbPool)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)

	// GET /api/v1/me — identidade do Principal autenticado.
	s.mux.Handle("GET /api/v1/me", authnMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := authn.FromContext(r.Context())
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"user_id": principal.UserID.String(),
			"subject": principal.Subject,
		})
	})))

	// GET /api/v1/tenants — tenants do usuário autenticado.
	s.mux.Handle("GET /api/v1/tenants", authnMiddleware(userSession(http.HandlerFunc(tenantHandler.ListMyTenants))))

	// GET /api/v1/tenants/{tenant_id} — detalhe de um tenant (autorização
	// derivada de membership real, nunca do valor da URL isoladamente).
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}", authnMiddleware(tenantSession(http.HandlerFunc(tenantHandler.GetTenantMe))))

	// Membros
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/members", authnMiddleware(tenantSession(http.HandlerFunc(tenantHandler.ListMemberships))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/members", authnMiddleware(tenantSession(http.HandlerFunc(tenantHandler.CreateMembership))))
	s.mux.Handle("DELETE /api/v1/tenants/{tenant_id}/members/{membership_id}", authnMiddleware(tenantSession(http.HandlerFunc(tenantHandler.RevokeMembership))))

	// Auditoria
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/audit", authnMiddleware(tenantSession(http.HandlerFunc(auditHandler.ListTenantAuditEvents))))
}

// RegisterWahaWebhook exposes only the connection-scoped WAHA callback.
// Authentication happens inside the handler using the per-connection HMAC
// credential; this route never accepts a tenant_id claim.
func (s *Server) RegisterWahaWebhook(handler http.Handler) {
	if handler == nil {
		return
	}
	s.mux.Handle("POST /webhooks/v1/whatsapp/waha/{connection_token}", handler)
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
