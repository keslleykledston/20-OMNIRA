package httpserver

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	auditapplication "github.com/omnira/omnira/internal/audit/application"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	contactsadapters "github.com/omnira/omnira/internal/contacts/adapters"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/platform/authn"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/health"
	"github.com/omnira/omnira/internal/platform/ratelimit"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
	routingapplication "github.com/omnira/omnira/internal/routing/application"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	tenancyapplication "github.com/omnira/omnira/internal/tenancy/application"
	tenancydomain "github.com/omnira/omnira/internal/tenancy/domain"
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
	natsConn      *nats.Conn
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
func (s *Server) RegisterAuthHandlers(devAuthEnabled bool, secureCookie ...bool) {
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

	authHandler := authn.NewAuthHandler(s.privateKey, secureCookie...)
	s.mux.HandleFunc("GET /api/v1/auth/health", authHandler.HealthCheck)

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

func (s *Server) RegisterOIDCAuthHandlers(authenticator authn.Authenticator, handler oidcHTTPHandler) {
	if authenticator == nil || handler == nil {
		return
	}
	s.authenticator = authenticator
	s.mux.HandleFunc("GET /api/v1/auth/oidc/start", handler.Start)
	s.mux.HandleFunc("GET /api/v1/auth/oidc/callback", handler.Callback)
	s.mux.Handle("GET /api/v1/auth/session", authn.Middleware(authenticator)(http.HandlerFunc(handler.Session)))
	s.mux.HandleFunc("POST /api/v1/auth/logout", handler.Logout)
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

	authnMiddleware := authn.Middleware(s.authenticator)

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

	// Membros (legado: sem checagem de permissão em nível de aplicação, mas
	// protegido por RLS — INSERT/UPDATE em memberships exigem
	// has_active_admin_membership. Sem uso pelo frontend nem no contrato.)
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/members", authnMiddleware(tenantSession(http.HandlerFunc(tenantHandler.ListMemberships))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/members", authnMiddleware(tenantSession(http.HandlerFunc(tenantHandler.CreateMembership))))
	s.mux.Handle("DELETE /api/v1/tenants/{tenant_id}/members/{membership_id}", authnMiddleware(tenantSession(http.HandlerFunc(tenantHandler.RevokeMembership))))

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

	// Auditoria
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/audit", authnMiddleware(tenantSession(http.HandlerFunc(auditHandler.ListTenantAuditEvents))))
}

// RegisterInvitationHandlers — convites de membership (IAM2B).
//
// devExposeInviteURL segue o mesmo par ambiente+flag do login de
// desenvolvimento (config.DevAuthActive): só nesse caso a resposta de criação
// devolve o link de convite. publicBaseURL monta esse link; sem ele, fica
// vazio e o link não é exposto de qualquer forma.
//
// O aceite não tem tenant_id na URL — quem aceita pode ainda não ter
// membership em lugar nenhum, e a AuthorizationMiddleware normal exigiria
// isso. Por isso usa só authnMiddleware, e o handler resolve o tenant a
// partir do próprio token.
func (s *Server) RegisterInvitationHandlers(dbPool *pgxpool.Pool, devExposeInviteURL bool, publicBaseURL string) {
	if s.authenticator == nil {
		return
	}
	authnMiddleware := authn.Middleware(s.authenticator)
	authzSvc := tenancyapplication.NewAuthorizationService(
		tenancyadapters.NewPostgresMembershipRepository(dbPool),
		tenancyadapters.NewPostgresTenantRepository(dbPool),
	)
	tenantSession := tenancyadapters.AuthorizationMiddleware(dbPool, authzSvc)
	auditRepo := auditadapters.NewPostgresAuditEventRepository(dbPool)
	h := tenancyadapters.NewInvitationsHandler(dbPool, auditRepo, nil, devExposeInviteURL, publicBaseURL)

	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/team/invitations", authnMiddleware(tenantSession(http.HandlerFunc(h.CreateInvitation))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/team/invitations", authnMiddleware(tenantSession(http.HandlerFunc(h.ListInvitations))))
	s.mux.Handle("PATCH /api/v1/tenants/{tenant_id}/team/invitations/{invitation_id}", authnMiddleware(tenantSession(http.HandlerFunc(h.RevokeInvitation))))

	s.mux.Handle("GET /api/v1/invitations/{token}/status", authnMiddleware(http.HandlerFunc(h.InvitationStatus)))
	s.mux.Handle("POST /api/v1/invitations/{token}/accept", authnMiddleware(http.HandlerFunc(h.AcceptInvitation)))
}

// RegisterInboxHandlers exposes tenant-scoped, read-only Inbox queries and realtime SSE.
// Retorna o crmHandler para que possa ser configurado com o K3G CRM client.
func (s *Server) RegisterInboxHandlers(dbPool *pgxpool.Pool) *inboxadapters.CRMHandlers {
	if s.authenticator == nil {
		return nil
	}
	authnMiddleware := authn.Middleware(s.authenticator)
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

	contactsHandler := contactsadapters.NewContactsAPIHandler(dbPool)
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/contacts", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.ListContacts))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/contacts/{contact_id}", authnMiddleware(tenantSession(http.HandlerFunc(contactsHandler.GetContact))))

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
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.CreateTicket))))
	s.mux.Handle("GET /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/{ticket_id}", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.GetTicket))))
	s.mux.Handle("PATCH /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/{ticket_id}", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.UpdateTicket))))
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket/{ticket_id}/close", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.CloseTicket))))
	// R5.2: Create activity (atendimento WHATSAPP) in CRM
	s.mux.Handle("POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/crm/activity", authnMiddleware(tenantSession(http.HandlerFunc(crmHandler.CreateActivity))))
	// R5.2: List companies from K3G CRM (without tenant_id in path, just authn)
	s.mux.Handle("GET /api/v1/integrations/companies", authnMiddleware(http.HandlerFunc(crmHandler.ListCompanies)))

	return crmHandler
}

// RegisterWahaConnectionHandlers exposes tenant-scoped WAHA connection/session
// management (admin-only via channel.manage), behind authn + tenant session.
func (s *Server) RegisterWahaConnectionHandlers(dbPool *pgxpool.Pool, h *channeladapters.ConnectionHandler) {
	if s.authenticator == nil || h == nil {
		return
	}
	authnMiddleware := authn.Middleware(s.authenticator)
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
	authnMiddleware := authn.Middleware(s.authenticator)
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
