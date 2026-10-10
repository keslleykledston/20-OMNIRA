package httpserver

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"time" // import já existe

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	accountsadapters "github.com/omnira/omnira/internal/accounts/adapters"
	aiadapters "github.com/omnira/omnira/internal/ai/adapters"
	aiports "github.com/omnira/omnira/internal/ai/ports"
	attendanceadapters "github.com/omnira/omnira/internal/attendance/adapters"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	auditapplication "github.com/omnira/omnira/internal/audit/application"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	contactsadapters "github.com/omnira/omnira/internal/contacts/adapters"
	dashboardadapters "github.com/omnira/omnira/internal/dashboard/adapters"
	"github.com/omnira/omnira/internal/entitlements"
	flowsadapters "github.com/omnira/omnira/internal/flows/adapters"
	groupsadapters "github.com/omnira/omnira/internal/groups/adapters"
	hubadapters "github.com/omnira/omnira/internal/hub/adapters"
	identityadapters "github.com/omnira/omnira/internal/identity/adapters"
	identityapp "github.com/omnira/omnira/internal/identity/application"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	intelligenceadapters "github.com/omnira/omnira/internal/intelligence/adapters"
	mediaadapters "github.com/omnira/omnira/internal/media/adapters"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/platform/config"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/health"
	"github.com/omnira/omnira/internal/platform/ratelimit"
	presenceadapters "github.com/omnira/omnira/internal/presence/adapters"
	presenceapplication "github.com/omnira/omnira/internal/presence/application"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
	routingapplication "github.com/omnira/omnira/internal/routing/application"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
	ticketsadapters "github.com/omnira/omnira/internal/tickets/adapters"
	"github.com/redis/go-redis/v9"
)

// Issuer/audience do JWT mock — precisam bater com os valores gravados nas
// claims em internal/platform/authn/mock_login.go.
const (
	mockJWTIssuer   = "omnira-mock"
	mockJWTAudience = "omnira-api"
)

type Server struct {
	srv           *http.Server
	mux           *http.ServeMux
	addr          string
	ln            net.Listener
	closed        chan struct{}
	closedOnce    bool
	health        *health.HealthCheck
	rateLimiter   *ratelimit.Limiter
	privateKey    *rsa.PrivateKey
	publicKey     *rsa.PublicKey
	authenticator authn.Authenticator
	// contactClassification is wired to the company directory by main once the ticketing runtime exists.
	contactClassification *contactsadapters.ClassificationHandler
	sessionStore          authn.SessionStore
	// sessionChecker lets long-lived streams (SSE) re-validate the credential they were opened with (R-3). Optional.
	sessionChecker authn.SessionChecker
	natsConn       *nats.Conn
	valkeyClient   *redis.Client
}

// SetupPresence wires the Valkey client used by presence heartbeats
// (ADR-0010). Optional: when nil, RegisterPresenceHandlers still registers
// routes but every heartbeat fails closed with 503 (never a silent "online").
func (s *Server) SetupPresence(valkeyClient *redis.Client) {
	s.valkeyClient = valkeyClient
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

// ConfigureRateLimits sets the per-user and per-tenant quotas (requests per minute) enforced after tenant authorization (R-2). Values <= 0 keep
// the limiter defaults. Calibrate against real Web traffic before lowering them: a burst of one active operator must never be throttled.
func (s *Server) ConfigureRateLimits(userPerMinute, tenantPerMinute int) {
	if userPerMinute > 0 {
		s.rateLimiter.SetQuota(ratelimit.QuotaTypeUser, userPerMinute, time.Minute)
	}
	if tenantPerMinute > 0 {
		s.rateLimiter.SetQuota(ratelimit.QuotaTypeTenant, tenantPerMinute, time.Minute)
	}
}

// SetupRateLimiting — ativa rate limiting no mux.
func (s *Server) SetupRateLimiting() {
	middleware := ratelimit.Middleware(s.rateLimiter)
	s.srv.Handler = middleware(s.mux)
}

// SetupHealth — configura health check com dependências.
func (s *Server) SetupHealth(dbPool *pgxpool.Pool, natsConn *nats.Conn) {
	s.natsConn = natsConn
	s.health = health.NewHealthCheck(dbPool, natsConn)
}

// RegisterHealthHandlers — registra handlers de health/ready check.
// ContactClassification returns the contact classification handler (nil until RegisterInboxHandlers ran) so main can
// give it the tenant-scoped company directory.
func (s *Server) ContactClassification() *contactsadapters.ClassificationHandler {
	return s.contactClassification
}

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
		if s.health != nil {
			// Fresh probe on every call: IsHealthy() alone only reflects the last /healthz run, so a
			// database outage would keep answering "ready" until somebody hit /healthz.
			s.health.Check(r.Context())
		}
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

// RegisterAuthHandlers — endpoints de autenticação quando não há OIDC.
//
// devAuthEnabled decide se a rota de login de desenvolvimento passa a existir.
// Quando é false a rota não é registrada: responder 403 de dentro do handler
// ainda deixaria a superfície publicada, e o que queremos é que ela não exista.
// Autoridade é do backend — o frontend esconder o formulário não substitui isto.
// sessionIdleTimeoutSeconds: idle timeout da sessão em segundos (padrão 7200 = 2h)
func (s *Server) RegisterAuthHandlers(dbPool *pgxpool.Pool, devAuthEnabled bool, sessionStore authn.SessionStore, sessionIdleTimeoutSeconds int, secureCookie ...bool) {
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
	s.sessionStore = sessionStore
	if publicKey != nil {
		s.authenticator = authn.NewJWTAuthenticator(publicKey, mockJWTIssuer, mockJWTAudience)
	}

	if s.privateKey == nil {
		s.mux.HandleFunc("GET /api/v1/auth/mode", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(authModeResponse{Mode: "unavailable"})
		})
		return
	}

	sessionTTL := time.Duration(sessionIdleTimeoutSeconds) * time.Second
	authHandler := authn.NewAuthHandler(s.privateKey, sessionStore, sessionTTL, secureCookie...)
	s.mux.HandleFunc("GET /api/v1/auth/health", authHandler.HealthCheck)
	s.mux.HandleFunc("POST /api/v1/auth/logout", authHandler.Logout)

	// Password reset handlers
	passwordHandler := authn.NewPasswordHandler(dbPool, nil) // emailSender nil por enquanto (será wired depois)
	s.mux.HandleFunc("POST /api/v1/auth/password-reset-request", passwordHandler.PasswordResetRequest)
	s.mux.HandleFunc("POST /api/v1/auth/password-reset", passwordHandler.PasswordReset)
	s.mux.Handle("POST /api/v1/auth/password-change", authn.WebMiddleware(s.authenticator, sessionStore)(http.HandlerFunc(passwordHandler.PasswordChange)))

	mode := "unavailable"
	if devAuthEnabled {
		// Caminho próprio, separado de /auth/login: não existe login local de
		// verdade, e um path genérico faria o mock parecer o mecanismo normal.
		s.mux.HandleFunc("POST /api/v1/auth/dev/login", authHandler.DevLogin)
		mode = "dev"
	}
	s.mux.HandleFunc("GET /api/v1/auth/mode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(authModeResponse{Mode: mode, DevAuth: devAuthEnabled})
	})
}

// authModeResponse diz ao cliente quais formas de entrar existem de fato, para
// que a tela de login não ofereça um caminho que o servidor não atende.
type authModeResponse struct {
	Mode    string `json:"mode"` // oidc | dev | unavailable
	DevAuth bool   `json:"dev_auth"`
}

// RegisterOIDCAuthHandlers installs the production authentication boundary.
// The browser receives only an HttpOnly cookie; tenant authority is still
// derived later from persisted memberships by the tenancy middleware.
type oidcHTTPHandler interface {
	Start(http.ResponseWriter, *http.Request)
	Callback(http.ResponseWriter, *http.Request)
	Session(http.ResponseWriter, *http.Request)
	Logout(http.ResponseWriter, *http.Request)
}

func (s *Server) RegisterOIDCAuthHandlers(authenticator authn.Authenticator, sessionStore authn.SessionStore, handler oidcHTTPHandler) {
	if authenticator == nil || handler == nil {
		return
	}
	s.authenticator = authenticator
	s.sessionStore = sessionStore
	s.mux.HandleFunc("GET /api/v1/auth/oidc/start", handler.Start)
	s.mux.HandleFunc("GET /api/v1/auth/oidc/callback", handler.Callback)
	// /auth/session is reached right after Callback sets the opaque cookie
	// (never a Bearer header here), so it must resolve through the session
	// store like every other web route — not authn.Middleware's JWT-only path.
	s.mux.Handle("GET /api/v1/auth/session", authn.WebMiddleware(authenticator, sessionStore)(http.HandlerFunc(handler.Session)))
	// Guarded like /auth/mode below: RegisterAuthHandlers may already have
	// registered its own dev-mode logout (real deployments call exactly one
	// of the two registrars, but contract tests call both to exercise the
	// full route surface).
	if _, logoutPattern := s.mux.Handler(&http.Request{Method: http.MethodPost, URL: &url.URL{Path: "/api/v1/auth/logout"}}); logoutPattern == "" {
		s.mux.HandleFunc("POST /api/v1/auth/logout", handler.Logout)
	}
	_, modePattern := s.mux.Handler(&http.Request{Method: http.MethodGet, URL: &url.URL{Path: "/api/v1/auth/mode"}})
	if modePattern == "" {
		s.mux.HandleFunc("GET /api/v1/auth/mode", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(authModeResponse{Mode: "oidc"})
		})
	}
}

// mobileAuthHTTPHandler is the native-client credential surface (ADR-0022, MOBILE.1).
type mobileAuthHTTPHandler interface {
	Token(http.ResponseWriter, *http.Request)
	Refresh(http.ResponseWriter, *http.Request)
	Logout(http.ResponseWriter, *http.Request)
	ListDevices(http.ResponseWriter, *http.Request)
	DeleteDevice(http.ResponseWriter, *http.Request)
}

// deviceAdminHTTPHandler is the administrator view of other users' app installations.
type deviceAdminHTTPHandler interface {
	List(http.ResponseWriter, *http.Request)
	Revoke(http.ResponseWriter, *http.Request)
}

// RegisterDeviceAdminHandlers installs the tenant-scoped administration of app installations. The handler authorizes membership.manage in
// the path tenant AND in every tenant of the target (see tenancyadapters.DeviceAdminHandler). Needs the tenant session like any tenant route.
func (s *Server) RegisterDeviceAdminHandlers(dbPool *pgxpool.Pool, handler deviceAdminHTTPHandler) {
	if s.authenticator == nil || handler == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/team/{membership_id}/devices", authnMiddleware(tenantSession(http.HandlerFunc(handler.List))))
	s.mux.Handle("DELETE /api/v1/tenants/{tenant_id}/team/{membership_id}/devices/{device_id}", authnMiddleware(tenantSession(http.HandlerFunc(handler.Revoke))))
}

// SetSessionChecker installs the credential re-check used by SSE streams.
func (s *Server) SetSessionChecker(c authn.SessionChecker) { s.sessionChecker = c }

// RegisterMobileAuthHandlers installs the native credential endpoints. The authenticator handed to RegisterOIDCAuthHandlers must already
// recognise device access tokens (authn.NewDeviceAuthenticator), so every authenticated route accepts them without further wiring.
// token/refresh/logout carry their own proof (code+PKCE, refresh token, access token); /me/devices go through the normal boundary.
func (s *Server) RegisterMobileAuthHandlers(handler mobileAuthHTTPHandler) {
	if s.authenticator == nil || handler == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	s.mux.HandleFunc("POST /api/v1/auth/mobile/token", handler.Token)
	s.mux.HandleFunc("POST /api/v1/auth/mobile/refresh", handler.Refresh)
	s.mux.HandleFunc("POST /api/v1/auth/mobile/logout", handler.Logout)
	s.mux.Handle("GET /api/v1/me/devices", authnMiddleware(http.HandlerFunc(handler.ListDevices)))
	s.mux.Handle("DELETE /api/v1/me/devices/{device_id}", authnMiddleware(http.HandlerFunc(handler.DeleteDevice)))
}

// RegisterTenancyHandlers — registra as rotas REST do M01/R0.1 Tenant
// Management: GET /api/v1/me, GET /api/v1/tenants, GET/PATCH
// /api/v1/tenants/{tenant_id}, membros e auditoria. Requer que
// RegisterAuthHandlers já tenha rodado (usa a mesma keypair RSA).
// invitationDeliveryAvailable segue a mesma regra que RegisterInvitationHandlers
// usa para InvitationsHandler.deliveryAvailable: passada aqui também porque
// MyAccess (servido por este registro) é onde o frontend consulta a
// capability, e as duas precisam concordar sempre.
func (s *Server) RegisterTenancyHandlers(dbPool *pgxpool.Pool, invitationDeliveryAvailable bool) {
	if s.authenticator == nil {
		// Sem chave pública não há como verificar tokens; não registrar
		// rotas que dependeriam de autenticação funcional.
		return
	}

	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)

	tenantRepo := tenancyadapters.NewPostgresTenantRepository(dbPool)
	memberRepo := tenancyadapters.NewPostgresMembershipRepository(dbPool)
	roleRepo := tenancyadapters.NewPostgresRoleRepository(dbPool)

	tenantSvc := tenancyapplication.NewTenantService(tenantRepo)
	membershipSvc := tenancyapplication.NewMembershipService(memberRepo, roleRepo)
	authzSvc := tenancyapplication.NewAuthorizationService(memberRepo, tenantRepo)

	tenantHandler := tenancyadapters.NewTenantAPIHandler(tenantSvc, membershipSvc)

	auditRepo := auditadapters.NewPostgresAuditEventRepository(dbPool)
	auditSvc := auditapplication.NewAuditService(auditRepo)
	auditHandler := auditadapters.NewAuditAPIHandler(auditSvc, dbPool)

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

	// A API legada /members (GET/POST/DELETE) foi removida no IAM3: não checava permissão
	// nem restringia o role_id (um tenant_admin podia conceder system_admin/hub_admin).
	// Equipe e acesso usa /team, /roles e /team/invitations.

	// Equipe e acesso (IAM2A): payload com identidade+papel resolvidos (evita
	// N+1 no cliente) e autorização por permission (membership.read/manage),
	// não por comparação de string de role.
	teamHandler := tenancyadapters.NewTeamHandler(dbPool, auditRepo, invitationDeliveryAvailable)
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/team", authnMiddleware(tenantSession(http.HandlerFunc(teamHandler.ListTeam))))
	s.mux.Handle("PATCH /api/v1/tenants/{tenant_id}/team/{membership_id}", authnMiddleware(tenantSession(http.HandlerFunc(teamHandler.UpdateMembership))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/roles", authnMiddleware(tenantSession(http.HandlerFunc(teamHandler.ListAssignableRoles))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/me/access", tenancyadapters.Delegable("conversation.read", authnMiddleware(tenantSession(http.HandlerFunc(teamHandler.MyAccess)))))

	// Agentes (para co-atendimento)
	agentsHandler := tenancyadapters.NewAgentsHandler(dbPool)
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/users/agents", authnMiddleware(tenantSession(http.HandlerFunc(agentsHandler.ListAgents))))
	// Queues ("groups" in the UI): list/create/rename/mode/default/delete, gated on
	// agent.read / agent.manage inside the handler.
	queuesHandler := tenancyadapters.NewQueuesHandler(dbPool, auditRepo)
	inboxSettings := tenancyadapters.NewInboxSettingsHandler(dbPool, auditRepo)
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/settings/inbox", authnMiddleware(tenantSession(http.HandlerFunc(inboxSettings.Get))))
	s.mux.Handle("PUT /api/v1/tenants/{tenant_id}/settings/inbox", authnMiddleware(tenantSession(http.HandlerFunc(inboxSettings.Put))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/queues", authnMiddleware(tenantSession(http.HandlerFunc(queuesHandler.List))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/queues", authnMiddleware(tenantSession(http.HandlerFunc(queuesHandler.Create))))
	s.mux.Handle("PATCH /api/v1/tenants/{tenant_id}/queues/{queue_id}", authnMiddleware(tenantSession(http.HandlerFunc(queuesHandler.Update))))
	s.mux.Handle("DELETE /api/v1/tenants/{tenant_id}/queues/{queue_id}", authnMiddleware(tenantSession(http.HandlerFunc(queuesHandler.Delete))))
	// IAM4 operational agents; legacy /users/agents keeps its co-attendance semantics.
	agentProfiles := tenancyadapters.NewAgentProfilesHandler(dbPool, auditRepo)
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/agents", authnMiddleware(tenantSession(http.HandlerFunc(agentProfiles.List))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/agents", authnMiddleware(tenantSession(http.HandlerFunc(agentProfiles.Create))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/agents/{agent_profile_id}", authnMiddleware(tenantSession(http.HandlerFunc(agentProfiles.Get))))
	s.mux.Handle("PATCH /api/v1/tenants/{tenant_id}/agents/{agent_profile_id}", authnMiddleware(tenantSession(http.HandlerFunc(agentProfiles.Update))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/agents/{agent_profile_id}/queues", authnMiddleware(tenantSession(http.HandlerFunc(agentProfiles.ListQueues))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/agents/{agent_profile_id}/queues", authnMiddleware(tenantSession(http.HandlerFunc(agentProfiles.AddQueue))))
	s.mux.Handle("PATCH /api/v1/tenants/{tenant_id}/agents/{agent_profile_id}/queues/{queue_member_id}", authnMiddleware(tenantSession(http.HandlerFunc(agentProfiles.UpdateQueue))))
	s.mux.Handle("DELETE /api/v1/tenants/{tenant_id}/agents/{agent_profile_id}/queues/{queue_member_id}", authnMiddleware(tenantSession(http.HandlerFunc(agentProfiles.RemoveQueue))))

	// Auditoria
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/audit", authnMiddleware(tenantSession(http.HandlerFunc(auditHandler.ListTenantAuditEvents))))
}

// RegisterInvitationHandlers — convites de membership (IAM2B).
//
// devExposeInviteURL segue o mesmo par ambiente+flag do login de
// desenvolvimento (config.DevAuthActive): só nesse caso a resposta de criação
// devolve o link de convite. webBaseURL é o host do frontend usado nos links dos
// e-mails; sender é nil quando não há SMTP configurado (dev).
//
// O aceite não tem tenant_id na URL — quem aceita pode ainda não ter
// membership em lugar nenhum, e a AuthorizationMiddleware normal exigiria
// isso. Por isso usa só authnMiddleware, e o handler resolve o tenant a
// partir do próprio token.
// RegisterHubHandlers mounts the read-only Hub API (feature-flagged by the caller). Every route runs behind
// the authn middleware and UserSessionMiddleware: the caller's own RLS session, never system admin. Tenant
// authority is never taken from the request; see hubadapters.HTTPHandler.
func (s *Server) RegisterHubHandlers(dbPool *pgxpool.Pool, adminAPI, accessAPI bool) {
	if s.authenticator == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	userSession := tenancyadapters.UserSessionMiddleware(dbPool)
	h := hubadapters.NewHTTPHandler(dbPool).WithAdminAPI(adminAPI).WithAccessAPI(accessAPI)
	s.mux.Handle("GET /api/v1/hubs", authnMiddleware(userSession(http.HandlerFunc(h.ListMyHubs))))
	s.mux.Handle("GET /api/v1/hubs/{hub_id}/inbox", authnMiddleware(userSession(http.HandlerFunc(h.ListInbox))))
	s.mux.Handle("GET /api/v1/hubs/{hub_id}/inbox/{item_id}", authnMiddleware(userSession(http.HandlerFunc(h.OpenInboxItem))))
	s.mux.Handle("POST /api/v1/hubs/{hub_id}/inbox/{item_id}/claim", authnMiddleware(userSession(http.HandlerFunc(h.ClaimItem))))
	s.mux.Handle("POST /api/v1/hubs/{hub_id}/inbox/{item_id}/messages", authnMiddleware(userSession(http.HandlerFunc(h.ReplyItem))))
	s.mux.Handle("GET /api/v1/hubs/{hub_id}/inbox/{item_id}/transfer-candidates", authnMiddleware(userSession(http.HandlerFunc(h.TransferCandidates))))
	s.mux.Handle("POST /api/v1/hubs/{hub_id}/inbox/{item_id}/transfer", authnMiddleware(userSession(http.HandlerFunc(h.TransferItem))))
	if adminAPI {
		// ADR-0038 phase 3: the instances this person may manage through the hub (the same flag as the managed channel routes).
		s.mux.Handle("GET /api/v1/hubs/{hub_id}/managed", authnMiddleware(userSession(http.HandlerFunc(h.ListManaged))))
	}
	if accessAPI {
		// ADR-0039: people and permissions. Only an admin of the hub in the path gets past the handler (uniform 404 otherwise).
		a, err := hubadapters.NewAccessHandler(dbPool)
		if err != nil {
			log.Printf("hub access API disabled: %v", err)
		} else {
			s.mux.Handle("GET /api/v1/hubs/{hub_id}/access", authnMiddleware(userSession(http.HandlerFunc(a.Overview))))
			s.mux.Handle("POST /api/v1/hubs/{hub_id}/access/agents", authnMiddleware(userSession(http.HandlerFunc(a.AddAgent))))
			s.mux.Handle("POST /api/v1/hubs/{hub_id}/access/invitations", authnMiddleware(userSession(http.HandlerFunc(a.Invite))))
			s.mux.Handle("DELETE /api/v1/hubs/{hub_id}/access/invitations/{invitation_id}", authnMiddleware(userSession(http.HandlerFunc(a.RevokeInvitation))))
			s.mux.Handle("DELETE /api/v1/hubs/{hub_id}/access/agents/{user_id}", authnMiddleware(userSession(http.HandlerFunc(a.RemoveAgent))))
			s.mux.Handle("PUT /api/v1/hubs/{hub_id}/access/agents/{user_id}/instances/{tenant_id}", authnMiddleware(userSession(http.HandlerFunc(a.SetAccess))))
			s.mux.Handle("PUT /api/v1/hubs/{hub_id}/access/agents/{user_id}/instances/{tenant_id}/management", authnMiddleware(userSession(http.HandlerFunc(a.SetManage))))
			// ADR-0038 phase 4: work pools (who answers for which instance, and automatic distribution). Hub admins only.
			pools := hubadapters.NewPoolsHandler(dbPool)
			// ADR-0038 §6: who changed what in the instances of this hub (read-only, hub admins only).
			audit := hubadapters.NewAuditHandler(dbPool)
			s.mux.Handle("GET /api/v1/hubs/{hub_id}/audit", authnMiddleware(userSession(http.HandlerFunc(audit.List))))
			s.mux.Handle("GET /api/v1/hubs/{hub_id}/pools", authnMiddleware(userSession(http.HandlerFunc(pools.List))))
			s.mux.Handle("POST /api/v1/hubs/{hub_id}/pools", authnMiddleware(userSession(http.HandlerFunc(pools.Create))))
			s.mux.Handle("PATCH /api/v1/hubs/{hub_id}/pools/{pool_id}", authnMiddleware(userSession(http.HandlerFunc(pools.Update))))
			s.mux.Handle("DELETE /api/v1/hubs/{hub_id}/pools/{pool_id}", authnMiddleware(userSession(http.HandlerFunc(pools.Delete))))
			s.mux.Handle("PUT /api/v1/hubs/{hub_id}/pools/{pool_id}/members", authnMiddleware(userSession(http.HandlerFunc(pools.SetMembers))))
			s.mux.Handle("PUT /api/v1/hubs/{hub_id}/pools/{pool_id}/instances", authnMiddleware(userSession(http.HandlerFunc(pools.SetInstances))))
			s.mux.Handle("POST /api/v1/hubs/{hub_id}/access/instances/{tenant_id}/admins", authnMiddleware(userSession(http.HandlerFunc(a.AddInstanceAdmin))))
			s.mux.Handle("DELETE /api/v1/hubs/{hub_id}/access/instances/{tenant_id}/admins/{user_id}", authnMiddleware(userSession(http.HandlerFunc(a.RemoveInstanceAdmin))))
		}
	}
	if adminAPI {
		// Control plane (ADR-0038 phase 1): only an active platform operator who administers the hub gets past the handler.
		a := hubadapters.NewAdminHandler(dbPool)
		s.mux.Handle("GET /api/v1/hubs/{hub_id}/companies", authnMiddleware(userSession(http.HandlerFunc(a.ListCompanies))))
		s.mux.Handle("POST /api/v1/hubs/{hub_id}/companies", authnMiddleware(userSession(http.HandlerFunc(a.CreateCompany))))
		s.mux.Handle("PATCH /api/v1/hubs/{hub_id}/companies/{tenant_id}", authnMiddleware(userSession(http.HandlerFunc(a.UpdateCompany))))
	}
}

func (s *Server) RegisterInvitationHandlers(dbPool *pgxpool.Pool, devExposeInviteURL bool, webBaseURL string, sender tenancyadapters.InvitationSender) {
	if s.authenticator == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)
	auditRepo := auditadapters.NewPostgresAuditEventRepository(dbPool)
	h := tenancyadapters.NewInvitationsHandler(dbPool, auditRepo, sender, devExposeInviteURL, webBaseURL)

	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/team/invitations", authnMiddleware(tenantSession(http.HandlerFunc(h.CreateInvitation))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/team/invitations", authnMiddleware(tenantSession(http.HandlerFunc(h.ListInvitations))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/team/invitations/{invitation_id}/resend", authnMiddleware(tenantSession(http.HandlerFunc(h.ResendInvitation))))
	s.mux.Handle("PATCH /api/v1/tenants/{tenant_id}/team/invitations/{invitation_id}", authnMiddleware(tenantSession(http.HandlerFunc(h.RevokeInvitation))))

	s.mux.Handle("GET /api/v1/invitations/{token}/status", authnMiddleware(http.HandlerFunc(h.InvitationStatus)))
	s.mux.Handle("POST /api/v1/invitations/{token}/accept", authnMiddleware(http.HandlerFunc(h.AcceptInvitation)))
}

// RegisterInboxHandlers exposes tenant-scoped, read-only Inbox queries and realtime SSE.
// Retorna o crmHandler para que possa ser configurado com o K3G CRM client.
func (s *Server) RegisterInboxHandlers(dbPool *pgxpool.Pool, cfg *config.Config) *inboxadapters.CRMHandlers {
	if s.authenticator == nil {
		return nil
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)
	handler := inboxadapters.NewInboxAPIHandler(dbPool)
	// SSE: authorize once + periodically in short transactions; never hold a tx while streaming.
	streamAuth := sessionStreamAuthorizer{pool: dbPool, authz: authzSvc}
	streamSession := inboxadapters.StreamMiddleware(streamAuth)
	realtimeHandler := inboxadapters.NewRealtimeHandler(s.natsConn, streamAuth, inboxadapters.RealtimeOptions{SessionRecheck: s.streamCredentialRecheck()})
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations", tenancyadapters.Delegable("conversation.read", authnMiddleware(tenantSession(http.HandlerFunc(handler.ListConversations)))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}", tenancyadapters.Delegable("conversation.read", authnMiddleware(tenantSession(http.HandlerFunc(handler.GetConversation)))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/messages", tenancyadapters.Delegable("conversation.read", authnMiddleware(tenantSession(http.HandlerFunc(handler.ListMessages)))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/events", authnMiddleware(streamSession(http.HandlerFunc(realtimeHandler.StreamInboxEvents))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/events", authnMiddleware(streamSession(http.HandlerFunc(realtimeHandler.StreamConversationEvents))))
	// Media retrieval: GET /api/v1/tenants/{tenant_id}/messages/{message_id}/media
	// Only wired if WAHA is enabled; otherwise media endpoint is not registered.
	// ADR-0016: when a media directory is configured the file comes from the quarantine-and-scan pipeline
	// (only cleared files are ever opened). Without it, the legacy live fetch from WAHA is the fallback.
	if cfg.MediaDir != "" {
		if store, err := mediaadapters.OpenFileStore(cfg.MediaDir); err == nil {
			handler = handler.WithMediaReader(mediaadapters.NewReader(dbPool, store))
			s.mux.Handle("GET /api/v1/tenants/{tenant_id}/messages/{message_id}/media", tenancyadapters.Delegable("media.read", authnMiddleware(tenantSession(http.HandlerFunc(handler.GetMedia)))))
			// the same file for a Hub agent, with the hub named in the path (an <img>/<audio> cannot send the acting header)
			s.mux.Handle("GET /api/v1/hubs/{hub_id}/serve/{tenant_id}/messages/{message_id}/media", tenancyadapters.Delegable("media.read", authnMiddleware(tenancyadapters.DelegatedPath(dbPool)(http.HandlerFunc(handler.GetMedia)))))
		} else {
			log.Printf("media store unavailable, media downloads disabled: %v", err)
		}
	} else if cfg.WahaEnabled && cfg.WahaBaseURL != "" {
		if mediaRetriever, err := inboxadapters.NewMediaRetriever(dbPool, cfg.WahaBaseURL); err == nil {
			handler = handler.WithMediaRetriever(mediaRetriever)
			s.mux.Handle("GET /api/v1/tenants/{tenant_id}/messages/{message_id}/media", tenancyadapters.Delegable("media.read", authnMiddleware(tenantSession(http.HandlerFunc(handler.GetMedia)))))
			// the same file for a Hub agent, with the hub named in the path (an <img>/<audio> cannot send the acting header)
			s.mux.Handle("GET /api/v1/hubs/{hub_id}/serve/{tenant_id}/messages/{message_id}/media", tenancyadapters.Delegable("media.read", authnMiddleware(tenancyadapters.DelegatedPath(dbPool)(http.HandlerFunc(handler.GetMedia)))))
		}
	}

	identityHandler := identityadapters.NewHandler(dbPool, auditadapters.NewPostgresAuditEventRepository(dbPool))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/identities", authnMiddleware(tenantSession(http.HandlerFunc(identityHandler.List))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/identities", authnMiddleware(tenantSession(http.HandlerFunc(identityHandler.Create))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/identities/{identity_id}/verify", authnMiddleware(tenantSession(http.HandlerFunc(identityHandler.Verify))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/identities/{identity_id}/revoke", authnMiddleware(tenantSession(http.HandlerFunc(identityHandler.Revoke))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/identity-conflicts", authnMiddleware(tenantSession(http.HandlerFunc(identityHandler.ListConflicts))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/identity-conflicts/{conflict_id}/resolve", authnMiddleware(tenantSession(http.HandlerFunc(identityHandler.ResolveConflict))))
	// ADR-0018 switches, read once like the rest of OMNIRA's flags (classification and accounts default ON).
	identityFlags := identityapp.FlagsFromEnv(nil)
	accountsHandler := accountsadapters.NewHandler(dbPool, auditadapters.NewPostgresAuditEventRepository(dbPool)).WithEnabled(identityFlags.CustomerAccountsEnabled)
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/accounts", tenancyadapters.Delegable("account.read", authnMiddleware(tenantSession(http.HandlerFunc(accountsHandler.ListAccounts)))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/accounts", authnMiddleware(tenantSession(http.HandlerFunc(accountsHandler.CreateAccount))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/accounts/{account_id}", authnMiddleware(tenantSession(http.HandlerFunc(accountsHandler.GetAccount))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/accounts/{account_id}/tickets", authnMiddleware(tenantSession(http.HandlerFunc(accountsHandler.ListAccountTickets))))
	s.mux.Handle("PATCH /api/v1/tenants/{tenant_id}/accounts/{account_id}", authnMiddleware(tenantSession(http.HandlerFunc(accountsHandler.UpdateAccount))))
	contactsHandler := contactsadapters.NewContactsAPIHandler(dbPool).WithAudit(auditadapters.NewPostgresAuditEventRepository(dbPool))
	s.mux.Handle("PUT /api/v1/tenants/{tenant_id}/contacts/{contact_id}/details", tenancyadapters.Delegable("contact.classify", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.UpdateDetails)))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/contacts/{contact_id}/notes", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.ListNotes))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/contacts/{contact_id}/notes", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.AddNote))))
	s.mux.Handle("PUT /api/v1/tenants/{tenant_id}/contacts/{contact_id}/notes/{note_id}", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.EditNote))))
	s.mux.Handle("DELETE /api/v1/tenants/{tenant_id}/contacts/{contact_id}/notes/{note_id}", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.DeleteNote))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/people", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.ListPeople))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/contacts", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.ListContacts))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/contacts/{contact_id}", tenancyadapters.Delegable("contact.read", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.GetContact)))))
	s.mux.Handle("PATCH /api/v1/tenants/{tenant_id}/contacts/{contact_id}", tenancyadapters.Delegable("contact.classify", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.SetKind)))))
	classificationHandler := contactsadapters.NewClassificationHandler(dbPool, auditadapters.NewPostgresAuditEventRepository(dbPool)).WithEnabled(identityFlags.ContactClassificationEnabled)
	s.contactClassification = classificationHandler
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/contacts/{contact_id}/classification", tenancyadapters.Delegable("account.read", authnMiddleware(tenantSession(http.HandlerFunc(classificationHandler.GetClassification)))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/contacts/{contact_id}/company-suggestions", tenancyadapters.Delegable("account.read", authnMiddleware(tenantSession(http.HandlerFunc(classificationHandler.ListCompanySuggestions)))))
	s.mux.Handle("PUT /api/v1/tenants/{tenant_id}/contacts/{contact_id}/classification", tenancyadapters.Delegable("contact.classify", authnMiddleware(tenantSession(http.HandlerFunc(classificationHandler.PutClassification)))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/contacts/{contact_id}/accounts", tenancyadapters.Delegable("contact.classify", authnMiddleware(tenantSession(http.HandlerFunc(classificationHandler.LinkAccount)))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/contacts/{contact_id}/accounts/{link_id}/end", tenancyadapters.Delegable("contact.classify", authnMiddleware(tenantSession(http.HandlerFunc(classificationHandler.EndLink)))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/contacts/{contact_id}/accounts/{link_id}/primary", tenancyadapters.Delegable("contact.classify", authnMiddleware(tenantSession(http.HandlerFunc(classificationHandler.SetPrimary)))))
	// CONTACT.360-A: read-only Contact 360 read model. Tickets are gated on
	// ticket.read inside the handler, exactly like GET /tickets.
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/contacts/{contact_id}/conversations", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.ListContactConversations))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/contacts/{contact_id}/tickets", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.ListContactTickets))))

	ticketsHandler := ticketsadapters.NewHandler(dbPool)
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/tickets", authnMiddleware(tenantSession(http.HandlerFunc(ticketsHandler.List))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/tickets/export.csv", authnMiddleware(tenantSession(http.HandlerFunc(ticketsHandler.ExportCSV))))
	// PRODUCT.7A1: read-only ticket reconciliation/audit surface, gated on
	// ticket.reconcile — GET only, no mutation route exists for this path.
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/ticket-reconciliation/create", authnMiddleware(tenantSession(http.HandlerFunc(ticketsHandler.ListCreateAttempts))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/ticket-reconciliation/status", authnMiddleware(tenantSession(http.HandlerFunc(ticketsHandler.ListStatusAttempts))))

	dashboardHandler := dashboardadapters.NewHandler(dbPool)
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/dashboard/snapshot", authnMiddleware(tenantSession(http.HandlerFunc(dashboardHandler.GetSnapshot))))

	auditRec := routingadapters.NewAuditRecorder(auditadapters.NewPostgresAuditEventRepository(dbPool))
	assignHandler := routingadapters.NewAssignHandler(routingapplication.NewAssigner(
		routingadapters.NewPostgresConversationAssigner(dbPool),
		auditRec,
	))
	// ADR-0040 phase 03: a Hub agent attending the instance claims and answers through the Hub's own write path (delegatedWrites); everybody else
	// reaches the original handler untouched. Delegable marks the route and names the key it needs in the delegated context.
	delegatedWrites := hubadapters.NewDelegatedWrites(dbPool)
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/assign", tenancyadapters.Delegable("conversation.claim", authnMiddleware(tenantSession(delegatedWrites.Claim(http.HandlerFunc(assignHandler.Assign))))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/unassign", authnMiddleware(tenantSession(http.HandlerFunc(assignHandler.Unassign))))

	// Co-attendance: invite, transfer, accept, reject, leave
	participantHandler := routingadapters.NewParticipantHandler(routingapplication.NewParticipantService(
		routingadapters.NewPostgresParticipantRepository(dbPool),
		routingadapters.NewPostgresConversationAssigner(dbPool),
		auditRec,
	))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/invite", authnMiddleware(tenantSession(http.HandlerFunc(participantHandler.Invite))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/transfer", authnMiddleware(tenantSession(http.HandlerFunc(participantHandler.Transfer))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/accept-invite", authnMiddleware(tenantSession(http.HandlerFunc(participantHandler.AcceptInvite))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/reject-invite", authnMiddleware(tenantSession(http.HandlerFunc(participantHandler.RejectInvite))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/leave", authnMiddleware(tenantSession(http.HandlerFunc(participantHandler.Leave))))
	sendHandler := messagesadapters.NewSendHandler(messagesapplication.NewSender(
		messagesadapters.NewPostgresOutboundStore(dbPool),
		channeladapters.NewPostgresPermissionChecker(dbPool),
	))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/messages", tenancyadapters.Delegable("conversation.reply", authnMiddleware(tenantSession(delegatedWrites.Reply(http.HandlerFunc(sendHandler.Send))))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/template", authnMiddleware(tenantSession(http.HandlerFunc(sendHandler.SendTemplate))))
	// ADR-0024: operator files to the customer, only through providers this deployment actually delivers with. Off by default; without the antivirus and a writable outbound area nothing is registered.
	mediaProviderReady := func(provider string) bool {
		return (provider == "waha" && cfg.WahaEnabled) || (provider == "meta_cloud" && cfg.MetaEnabled)
	}
	if cfg.OutboundMediaEnabled {
		if files, err := mediaadapters.NewOutboundFiles(cfg.MediaDir); err != nil {
			log.Printf("outbound media disabled: %v", err)
		} else {
			store := messagesadapters.NewPostgresOutboundStore(dbPool)
			sender := messagesapplication.NewSender(store, channeladapters.NewPostgresPermissionChecker(dbPool)).WithMediaProviders(mediaProviderReady).
				WithEntitlements(entitlements.NewChecker(dbPool).Gate)
			attachments := messagesapplication.NewAttachments(sender, store, files, mediaadapters.NewVirusScanner(cfg.ClamAVAddr))
			sendHandler.WithAttachments(attachments, store)
			attachmentsOn := entitlements.NewChecker(dbPool).Require(entitlements.OutboundAttachments) // ADR-0038: per-company switch
			s.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/attachments", authnMiddleware(sendHandler.BufferUpload(tenantSession(attachmentsOn(http.HandlerFunc(sendHandler.Upload))))))
			s.mux.Handle("DELETE /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/attachments/{attachment_id}", authnMiddleware(tenantSession(attachmentsOn(http.HandlerFunc(sendHandler.RemoveAttachment)))))
		}
	}
	linesHandler := inboxadapters.NewChannelLinesHandler(dbPool, channeladapters.NewPostgresPermissionChecker(dbPool)).WithOutboundMedia(cfg.OutboundMediaEnabled, mediaProviderReady)
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/channel", authnMiddleware(tenantSession(http.HandlerFunc(linesHandler.Channel))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/open", authnMiddleware(tenantSession(http.HandlerFunc(linesHandler.Open))))

	// CRM ticket handlers
	crmHandler := inboxadapters.NewCRMHandlers(dbPool)
	// The customer is told, in the conversation, that the ticket was opened and its number. On by default; off with
	// OMNIRA_TICKET_OPEN_NOTICE_ENABLED=false (the ticket flow itself is then untouched).
	if cfg.TicketOpenNoticeEnabled {
		noticeStore := messagesadapters.NewPostgresOutboundStore(dbPool)
		crmHandler.SetTicketOpenNotifier(messagesapplication.NewTicketOpenedNotice(
			messagesapplication.NewSender(noticeStore, channeladapters.NewPostgresPermissionChecker(dbPool)), noticeStore))
		// A Hub agent attending the instance tells the customer through the Hub's own write path (ADR-0037), never through the member sender.
		crmHandler.SetDelegatedTicketOpenNotifier(delegatedWrites.TicketOpenedNotice(noticeStore))
	}
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket", tenancyadapters.DelegableAny(authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.GetCurrentTicket))), "ticket.read", "ticket.create"))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket", tenancyadapters.Delegable("ticket.create", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.CreateTicket)))))
	// PRODUCT.6-O1R: explicit provider projection refresh (a command, never
	// a GET). Registered before the {ticket_id} wildcard route below —
	// net/http's ServeMux resolves the more specific literal segment first
	// regardless of registration order, but keeping it adjacent to the
	// other conversation-scoped ticket routes documents intent.
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/refresh", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.RefreshTicket))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/status", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.UpdateTicketStatus))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/{ticket_id}", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.GetTicket))))
	s.mux.Handle("PATCH /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/{ticket_id}", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.UpdateTicket))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/{ticket_id}/close", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.CloseTicket))))
	// R5.2: Create activity (atendimento WHATSAPP) in CRM
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/crm/activity", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.CreateActivity))))
	// PRODUCT.7B1A: tenant-scoped company directory. Was previously
	// GET /api/v1/integrations/companies with authnMiddleware only (no
	// tenantSession) — a confirmed P0 cross-tenant leak, since every
	// tenant's request resolved through one globally-bootstrapped K3G
	// client. Moved under /tenants/{tenant_id}/ so tenantSession
	// authorizes the path tenant against the caller's membership before
	// the handler ever resolves a K3G credential.
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/crm/companies", tenancyadapters.DelegableAny(authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.ListCompanies))), "ticket.create", "contact.classify"))

	// PRODUCT.7C1: AI conversation summary. generator is nil whenever AI is
	// disabled or incompletely configured (config.Config.AIReady()) — the
	// route is always registered, but NewSummaryHandler with a nil
	// generator always answers 503 without ever attempting a provider call,
	// so a misconfigured/disabled AI subsystem can never affect anything
	// else this function registers.
	var aiGenerator aiports.TextGenerator
	if cfg.AIReady() {
		if gen, err := aiadapters.NewTextGenerator(cfg.AIProvider, cfg.AIAPIKey, cfg.AIModel, time.Duration(cfg.AITimeoutSeconds)*time.Second); err == nil {
			aiGenerator = gen
		}
	}
	aiHandler := aiadapters.NewSummaryHandler(dbPool, aiGenerator, auditadapters.NewPostgresAuditEventRepository(dbPool), 300)
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ai/summary", authnMiddleware(tenantSession(http.HandlerFunc(aiHandler.Summarize))))

	return crmHandler
}

// RegisterPresenceHandlers wires IAM4.2-A: self-scoped heartbeat, supervisor
// snapshot + SSE. Presence never gates routing here — that is IAM4.2-B, a
// separate, explicitly gated rollout (ADR-0010 §11-12). The reaper (offline
// detection) runs in the worker process, not here — see
// internal/presence/application.Reaper and apps/worker/cmd/omnira-worker.
func (s *Server) RegisterPresenceHandlers(dbPool *pgxpool.Pool) {
	if s.authenticator == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)

	var store *presenceadapters.Store
	if s.valkeyClient != nil {
		store = presenceadapters.NewStore(s.valkeyClient)
	}
	publisher := presenceadapters.NewNatsTransitionPublisher(s.natsConn)
	lastSeenWriter := presenceadapters.NewCoalescedLastSeenWriter(dbPool)
	svc := presenceapplication.NewService(store, publisher, lastSeenWriter)
	handler := presenceadapters.NewHandler(dbPool, svc)

	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/me/presence/heartbeat", authnMiddleware(tenantSession(http.HandlerFunc(handler.Heartbeat))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/agents/presence", authnMiddleware(tenantSession(http.HandlerFunc(handler.Snapshot))))

	streamAuth := presenceadapters.NewStreamAuthorizer(dbPool, authzSvc)
	streamHandler := presenceadapters.NewStreamHandler(s.natsConn, streamAuth, presenceadapters.StreamOptions{})
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/agents/presence/events", authnMiddleware(streamHandler.Middleware(http.HandlerFunc(streamHandler.Stream))))

	// Coalesced flush loop: one instance per API process, stopped when the
	// server shuts down. Never per-heartbeat (ADR-0010 §9-10).
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-s.Done()
			cancel()
		}()
		lastSeenWriter.RunFlushLoop(ctx, 2*time.Minute)
	}()
}

// RegisterIntelligenceHandlers exposes the topic API (ADR-0017) behind authn + tenant session. Permissions
// (topic.read / topic.manage) and the "attendant or conversation.manage" rule are checked inside the handler.
func (s *Server) RegisterIntelligenceHandlers(dbPool *pgxpool.Pool, h *intelligenceadapters.TopicHandler) {
	if s.authenticator == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)
	wrap := func(fn http.HandlerFunc) http.Handler { return authnMiddleware(tenantSession(fn)) }
	const t = "/api/v1/tenants/{tenant_id}"
	s.mux.Handle("GET "+t+"/inbox/conversations/{conversation_id}/topics", wrap(h.ListConversationTopics))
	s.mux.Handle("POST "+t+"/inbox/conversations/{conversation_id}/topics", wrap(h.CreateConversationTopic))
	s.mux.Handle("GET "+t+"/topics/{topic_id}", wrap(h.GetTopic))
	s.mux.Handle("PATCH "+t+"/topics/{topic_id}", wrap(h.PatchTopic))
	s.mux.Handle("GET "+t+"/topics/{topic_id}/messages", wrap(h.ListTopicMessages))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/messages", wrap(h.LinkMessage))
	s.mux.Handle("DELETE "+t+"/topics/{topic_id}/messages/{message_id}", wrap(h.UnlinkMessage))
	s.mux.Handle("GET "+t+"/topics/{topic_id}/tickets", wrap(h.ListTopicTickets))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/tickets/link", wrap(h.LinkTicket))
	s.mux.Handle("GET "+t+"/topics/{topic_id}/account-context", wrap(h.GetAccountContext))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/accounts", wrap(h.LinkAccount))
	s.mux.Handle("DELETE "+t+"/topics/{topic_id}/accounts/{account_id}", wrap(h.UnlinkAccount))
	s.mux.Handle("GET "+t+"/contacts/{contact_id}/topics", wrap(h.ListContactTopics))
	s.mux.Handle("GET "+t+"/topics/{topic_id}/summaries", wrap(h.ListSummaries))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/summary/confirm", wrap(h.ConfirmSummary))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/summary/correct", wrap(h.CorrectSummary))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/summary/generate", wrap(h.GenerateSummary))
	s.mux.Handle("GET "+t+"/topics/{topic_id}/ticket-policy", wrap(h.GetTicketPolicy))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/ticket-policy/apply", wrap(h.ApplyTicketPolicy))
	s.mux.Handle("POST "+t+"/inbox/conversations/{conversation_id}/legacy-topic", wrap(h.BackfillLegacyTopic))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/handoffs", wrap(h.CreateHandoff))
	s.mux.Handle("GET "+t+"/topics/{topic_id}/handoffs", wrap(h.ListHandoffs))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/handoffs/{handoff_id}/revoke", wrap(h.RevokeHandoff))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/copilot/suggest-reply", wrap(h.SuggestReply))
	s.mux.Handle("GET "+t+"/topics/{topic_id}/ai/tools", wrap(h.ListAITools))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/ai/tools/invoke", wrap(h.InvokeAITool))
	s.mux.Handle("GET "+t+"/topics/{topic_id}/ai/tool-calls", wrap(h.ListAIToolCalls))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/ai/tool-calls/{call_id}/approve", wrap(h.ApproveAIToolCall))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/ai/tool-calls/{call_id}/reject", wrap(h.RejectAIToolCall))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/merge", wrap(h.MergeTopic))
	s.mux.Handle("POST "+t+"/topics/{topic_id}/split", wrap(h.SplitTopic))
	s.mux.Handle("GET "+t+"/intelligence/evaluation", wrap(h.GetEvaluation))
	s.mux.Handle("GET "+t+"/inbox/conversations/{conversation_id}/ambiguities", wrap(h.ListConversationAmbiguities))
	s.mux.Handle("POST "+t+"/ambiguities/{ambiguity_id}/resolve", wrap(h.ResolveAmbiguity))
}

// RegisterAIIntegrationHandlers exposes the per-tenant external-AI opt-in and key (ADR-0016). Everything is
// gated on tenant.manage inside the handler, and the key is write-only.
func (s *Server) RegisterAIIntegrationHandlers(dbPool *pgxpool.Pool, h *tenancyadapters.AIIntegrationHandler) {
	if s.authenticator == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)
	wrap := func(fn http.HandlerFunc) http.Handler { return authnMiddleware(tenantSession(fn)) }
	const base = "/api/v1/tenants/{tenant_id}/integrations/ai"
	s.mux.Handle("GET "+base, wrap(h.Get))
	s.mux.Handle("PUT "+base, wrap(h.Put))
	s.mux.Handle("DELETE "+base+"/key", wrap(h.DeleteKey))
	s.mux.Handle("POST "+base+"/test", wrap(h.Test))
	s.mux.Handle("GET "+base+"/usage", wrap(h.Usage))
}

// RegisterGroupHandlers exposes the read-only WhatsApp group APIs (ADR-0015) behind authn + tenant
// session. Permissions (group.read / group.manage) are checked by the handler.
func (s *Server) RegisterGroupHandlers(dbPool *pgxpool.Pool, h *groupsadapters.Handler) {
	if s.authenticator == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)
	wrap := func(fn http.HandlerFunc) http.Handler { return authnMiddleware(tenantSession(fn)) }
	const base = "/api/v1/tenants/{tenant_id}/groups"
	s.mux.Handle("GET "+base, wrap(h.List))
	s.mux.Handle("GET "+base+"/available", wrap(h.Available))
	s.mux.Handle("POST "+base, wrap(h.Enable))
	s.mux.Handle("PATCH "+base+"/{group_id}", wrap(h.SetEnabled))
	s.mux.Handle("GET "+base+"/{group_id}/messages", wrap(h.ListMessages))
	s.mux.Handle("DELETE "+base+"/{group_id}/messages", wrap(h.DeleteHistory))
}

// RegisterAttendanceHandlers exposes finalizing an attendance and the contact's attendance history (ADR-0020) behind authn +
// the tenant session. Permissions (conversation.claim / conversation.manage) are checked by the service.
func (s *Server) RegisterAttendanceHandlers(dbPool *pgxpool.Pool, h *attendanceadapters.Handler) {
	if s.authenticator == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)
	h.Routes(s.mux, func(fn http.HandlerFunc) http.Handler { return authnMiddleware(tenantSession(fn)) })
}

// RegisterFlowHandlers exposes the Flow Builder control plane (ADR-0019) behind authn + the tenant session. It is only
// called when OMNIRA_FLOWS_ENABLED is true; per-route permissions (flow.*) are checked by the handler.
func (s *Server) RegisterFlowHandlers(dbPool *pgxpool.Pool, h *flowsadapters.Handler) {
	if s.authenticator == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)
	h.Routes(s.mux, func(fn http.HandlerFunc) http.Handler { return authnMiddleware(tenantSession(fn)) })
}

// RegisterWahaConnectionHandlers exposes tenant-scoped WAHA connection/session
// management (admin-only via channel.manage), behind authn + tenant session.
func (s *Server) RegisterWahaConnectionHandlers(dbPool *pgxpool.Pool, h *channeladapters.ConnectionHandler) {
	if s.authenticator == nil || h == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)
	base := "/api/v1/tenants/{tenant_id}/channels/waha/connections"
	wrap := func(fn http.HandlerFunc) http.Handler { return authnMiddleware(tenantSession(fn)) }
	whatsappOn := entitlements.NewChecker(dbPool).Require(entitlements.WhatsAppChannel) // ADR-0038: per-company switch
	s.mux.Handle("POST "+base, authnMiddleware(tenantSession(whatsappOn(http.HandlerFunc(h.Create)))))
	s.mux.Handle("GET "+base, wrap(h.List))
	s.mux.Handle("GET "+base+"/{connection_id}", wrap(h.Get))
	s.mux.Handle("POST "+base+"/{connection_id}/session/start", wrap(h.StartSession))
	s.mux.Handle("POST "+base+"/{connection_id}/session/stop", wrap(h.StopSession))
	s.mux.Handle("GET "+base+"/{connection_id}/qr", wrap(h.QR))
}

// RegisterChannelDirectory exposes the read-only list of the tenant's WhatsApp lines (the Inbox channel selector) to
// any member of the tenant; it carries no secret and no management data.
func (s *Server) RegisterChannelDirectory(dbPool *pgxpool.Pool, h *channeladapters.DirectoryHandler) {
	if s.authenticator == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/channels/lines", authnMiddleware(tenantSession(http.HandlerFunc(h.List))))
}

// RegisterChannelTemplates exposes syncing (channel.manage) and listing (any member) of WhatsApp Cloud API templates.
func (s *Server) RegisterChannelTemplates(dbPool *pgxpool.Pool, h *channeladapters.TemplatesHandler) {
	if s.authenticator == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/channels/connections/{connection_id}/templates/sync", authnMiddleware(tenantSession(http.HandlerFunc(h.Sync))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/channels/lines/{connection_id}/templates", authnMiddleware(tenantSession(http.HandlerFunc(h.List))))
}

// RegisterChannelManagementHandlers exposes the provider-neutral catalog and
// connection routes introduced in I0. Provider-specific paths remain aliases.
func (s *Server) RegisterChannelManagementHandlers(dbPool *pgxpool.Pool, h *channeladapters.ManagementHandler, hubManaged bool) {
	if s.authenticator == nil || h == nil {
		return
	}
	authnMiddleware := authn.WebMiddleware(s.authenticator, s.sessionStore)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)
	base := "/api/v1/tenants/{tenant_id}/channels/connections"
	wrap := func(fn http.HandlerFunc) http.Handler { return authnMiddleware(tenantSession(fn)) }
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/channels/providers", wrap(h.Providers))
	s.mux.Handle("POST "+base, wrap(h.Create))
	s.mux.Handle("GET "+base, wrap(h.List))
	s.mux.Handle("GET "+base+"/{connection_id}", wrap(h.Get))
	s.mux.Handle("POST "+base+"/{connection_id}/test", wrap(h.TestConnection))
	s.mux.Handle("POST "+base+"/{connection_id}/session/start", wrap(h.StartSession))
	s.mux.Handle("POST "+base+"/{connection_id}/session/stop", wrap(h.StopSession))
	s.mux.Handle("GET "+base+"/{connection_id}/qr", wrap(h.QR))
	if hubManaged {
		// ADR-0038 phase 3: the SAME handlers, reached by a Hub person the contract delegates channel/integration management to.
		// The middleware proves hub -> contract -> role/grant and builds a `hub_manage` context; each permission is then asked of the
		// database again (scope channels / integrations) and the row-level policies of migration 103 are the second barrier.
		hubBase := "/api/v1/hubs/{hub_id}/instances/{tenant_id}/channels/connections"
		manage := hubadapters.ManageSession(dbPool)
		hubWrap := func(fn http.HandlerFunc) http.Handler { return authnMiddleware(manage(fn)) }
		s.mux.Handle("GET /api/v1/hubs/{hub_id}/instances/{tenant_id}/channels/providers", hubWrap(h.Providers))
		s.mux.Handle("POST "+hubBase, hubWrap(h.Create))
		s.mux.Handle("GET "+hubBase, hubWrap(h.List))
		s.mux.Handle("GET "+hubBase+"/{connection_id}", hubWrap(h.Get))
		s.mux.Handle("POST "+hubBase+"/{connection_id}/test", hubWrap(h.TestConnection))
		s.mux.Handle("POST "+hubBase+"/{connection_id}/session/start", hubWrap(h.StartSession))
		s.mux.Handle("POST "+hubBase+"/{connection_id}/session/stop", hubWrap(h.StopSession))
		s.mux.Handle("GET "+hubBase+"/{connection_id}/qr", hubWrap(h.QR))
	}
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

// RegisterMetaWebhook exposes Meta Cloud WhatsApp webhook (verification + inbound).
// D3.2: Webhook is tenant-safe — resolution via phone_number_id, never payload tenant_id.
func (s *Server) RegisterMetaWebhook(handler http.Handler) {
	if handler == nil {
		return
	}
	s.mux.Handle("GET /webhooks/v1/whatsapp/meta", handler)
	s.mux.Handle("POST /webhooks/v1/whatsapp/meta", handler)
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

// sessionStreamAuthorizer authorizes a user for a tenant in a short transaction (SET LOCAL
// app.current_user_id), used by the long-lived SSE endpoints.
type sessionStreamAuthorizer struct {
	pool  *pgxpool.Pool
	authz *tenancyapplication.AuthorizationService
}

func (a sessionStreamAuthorizer) Authorize(ctx context.Context, userID, tenantID uuid.UUID) (*tenancydomain.TenantContext, error) {
	var tc *tenancydomain.TenantContext
	err := platformdb.WithTenantSession(ctx, a.pool, userID, false, func(scoped context.Context) error {
		var authErr error
		tc, authErr = a.authz.AuthorizeAccessToTenant(scoped, tenantID, userID)
		return authErr
	})
	return tc, err
}

// ConversationVisible reports whether the conversation exists in the tenant for the user (RLS applies).
func (a sessionStreamAuthorizer) ConversationVisible(ctx context.Context, userID, tenantID, conversationID uuid.UUID) (bool, error) {
	var visible bool
	err := platformdb.WithTenantSession(ctx, a.pool, userID, false, func(scoped context.Context) error {
		return platformdb.QuerierFromContext(scoped, a.pool).QueryRow(scoped,
			`SELECT EXISTS(SELECT 1 FROM conversations WHERE tenant_id=$1 AND id=$2)`, tenantID, conversationID).Scan(&visible)
	})
	return visible, err
}

// streamCredentialRecheck adapts the optional session checker to the SSE options (nil when none is installed).
func (s *Server) streamCredentialRecheck() func(context.Context, *authn.Principal) error {
	if s.sessionChecker == nil {
		return nil
	}
	return s.sessionChecker.StillValid
}
