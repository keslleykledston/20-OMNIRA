package httpserver

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	aiadapters "github.com/omnira/omnira/internal/ai/adapters"
	aiports "github.com/omnira/omnira/internal/ai/ports"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	auditapplication "github.com/omnira/omnira/internal/audit/application"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	contactsadapters "github.com/omnira/omnira/internal/contacts/adapters"
	dashboardadapters "github.com/omnira/omnira/internal/dashboard/adapters"
	groupsadapters "github.com/omnira/omnira/internal/groups/adapters"
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
	sessionStore  authn.SessionStore
	natsConn      *nats.Conn
	valkeyClient  *redis.Client
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
func (s *Server) RegisterAuthHandlers(devAuthEnabled bool, sessionStore authn.SessionStore, secureCookie ...bool) {
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

	authHandler := authn.NewAuthHandler(s.privateKey, sessionStore, secureCookie...)
	s.mux.HandleFunc("GET /api/v1/auth/health", authHandler.HealthCheck)
	s.mux.HandleFunc("POST /api/v1/auth/logout", authHandler.Logout)

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
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/me/access", authnMiddleware(tenantSession(http.HandlerFunc(teamHandler.MyAccess))))

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
	realtimeHandler := inboxadapters.NewRealtimeHandler(s.natsConn, streamAuth, inboxadapters.RealtimeOptions{})
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations", authnMiddleware(tenantSession(http.HandlerFunc(handler.ListConversations))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}", authnMiddleware(tenantSession(http.HandlerFunc(handler.GetConversation))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/messages", authnMiddleware(tenantSession(http.HandlerFunc(handler.ListMessages))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/events", authnMiddleware(streamSession(http.HandlerFunc(realtimeHandler.StreamInboxEvents))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/events", authnMiddleware(streamSession(http.HandlerFunc(realtimeHandler.StreamConversationEvents))))
	// Media retrieval: GET /api/v1/tenants/{tenant_id}/messages/{message_id}/media
	// Only wired if WAHA is enabled; otherwise media endpoint is not registered.
	// ADR-0016: when a media directory is configured the file comes from the quarantine-and-scan pipeline
	// (only cleared files are ever opened). Without it, the legacy live fetch from WAHA is the fallback.
	if cfg.MediaDir != "" {
		if store, err := mediaadapters.OpenFileStore(cfg.MediaDir); err == nil {
			handler = handler.WithMediaReader(mediaadapters.NewReader(dbPool, store))
			s.mux.Handle("GET /api/v1/tenants/{tenant_id}/messages/{message_id}/media", authnMiddleware(tenantSession(http.HandlerFunc(handler.GetMedia))))
		} else {
			log.Printf("media store unavailable, media downloads disabled: %v", err)
		}
	} else if cfg.WahaEnabled && cfg.WahaBaseURL != "" {
		if mediaRetriever, err := inboxadapters.NewMediaRetriever(dbPool, cfg.WahaBaseURL); err == nil {
			handler = handler.WithMediaRetriever(mediaRetriever)
			s.mux.Handle("GET /api/v1/tenants/{tenant_id}/messages/{message_id}/media", authnMiddleware(tenantSession(http.HandlerFunc(handler.GetMedia))))
		}
	}

	contactsHandler := contactsadapters.NewContactsAPIHandler(dbPool).WithAudit(auditadapters.NewPostgresAuditEventRepository(dbPool))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/contacts", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.ListContacts))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/contacts/{contact_id}", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.GetContact))))
	s.mux.Handle("PATCH /api/v1/tenants/{tenant_id}/contacts/{contact_id}", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.SetKind))))
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
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/assign", authnMiddleware(tenantSession(http.HandlerFunc(assignHandler.Assign))))
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
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/inbox/conversations/{conversation_id}/messages", authnMiddleware(tenantSession(http.HandlerFunc(sendHandler.Send))))

	// CRM ticket handlers
	crmHandler := inboxadapters.NewCRMHandlers(dbPool)
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.GetCurrentTicket))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.CreateTicket))))
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
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/crm/companies", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.ListCompanies))))

	// PRODUCT.7C1: AI conversation summary. generator is nil whenever AI is
	// disabled or incompletely configured (config.Config.AIReady()) — the
	// route is always registered, but NewSummaryHandler with a nil
	// generator always answers 503 without ever attempting a provider call,
	// so a misconfigured/disabled AI subsystem can never affect anything
	// else this function registers.
	var aiGenerator aiports.TextGenerator
	if cfg.AIReady() {
		if gen, err := aiadapters.NewOpenAIGenerator(aiadapters.OpenAIConfig{
			APIKey:  cfg.AIAPIKey,
			Model:   cfg.AIModel,
			Timeout: time.Duration(cfg.AITimeoutSeconds) * time.Second,
		}); err == nil {
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
	s.mux.Handle("POST "+base, wrap(h.Create))
	s.mux.Handle("GET "+base, wrap(h.List))
	s.mux.Handle("GET "+base+"/{connection_id}", wrap(h.Get))
	s.mux.Handle("POST "+base+"/{connection_id}/session/start", wrap(h.StartSession))
	s.mux.Handle("POST "+base+"/{connection_id}/session/stop", wrap(h.StopSession))
	s.mux.Handle("GET "+base+"/{connection_id}/qr", wrap(h.QR))
}

// RegisterChannelManagementHandlers exposes the provider-neutral catalog and
// connection routes introduced in I0. Provider-specific paths remain aliases.
func (s *Server) RegisterChannelManagementHandlers(dbPool *pgxpool.Pool, h *channeladapters.ManagementHandler) {
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
