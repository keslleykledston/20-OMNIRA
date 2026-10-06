package main

import (
	"context"
	"flag"
	aiusageadapters "github.com/omnira/omnira/internal/aiusage/adapters"
	metachannel "github.com/omnira/omnira/internal/channels/meta"
	ports "github.com/omnira/omnira/internal/channels/ports"
	toolconnectors "github.com/omnira/omnira/internal/tool/connectors"
	"log"
	"os"
	"os/signal"
	"strconv"
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
	crmevidenceadapters "github.com/omnira/omnira/internal/crmevidence/adapters"
	groupsadapters "github.com/omnira/omnira/internal/groups/adapters"
	accountsadapters "github.com/omnira/omnira/internal/accounts/adapters"
	identityadapters "github.com/omnira/omnira/internal/identity/adapters"
	identityapp "github.com/omnira/omnira/internal/identity/application"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
	inboxapplication "github.com/omnira/omnira/internal/inbox/application"
	intelligenceadapters "github.com/omnira/omnira/internal/intelligence/adapters"
	intelligenceapp "github.com/omnira/omnira/internal/intelligence/application"
	intelligencedomain "github.com/omnira/omnira/internal/intelligence/domain"
	"github.com/omnira/omnira/internal/platform/authn"
	"github.com/omnira/omnira/internal/platform/config"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/httpserver"
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
	ticketsadapters "github.com/omnira/omnira/internal/tickets/adapters"
	ticketsapplication "github.com/omnira/omnira/internal/tickets/application"
	"github.com/redis/go-redis/v9"
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
	// ADR-0018 switches (classification, accounts, verified internal identities, conversation kind): read once.
	identityFlags := identityapp.FlagsFromEnv(nil)
	srv.SetupHealth(dbPool, nc)

	// Valkey (presence, IAM4.2-A): optional at boot like NATS above — a
	// heartbeat fails closed (503) rather than the API refusing to start.
	if cfg.ValkeyURL != "" {
		opts, valkeyErr := redis.ParseURL(cfg.ValkeyURL)
		if valkeyErr != nil {
			log.Printf("warning: invalid OMNIRA_VALKEY_URL: %v\n", valkeyErr)
		} else {
			valkeyClient := redis.NewClient(opts)
			defer valkeyClient.Close()
			if pingErr := valkeyClient.Ping(context.Background()).Err(); pingErr != nil {
				log.Printf("warning: failed to connect to Valkey: %v\n", pingErr)
			}
			srv.SetupPresence(valkeyClient)
		}
	}
	srv.SetupRateLimiting()
	srv.RegisterHealthHandlers()
	// omnira_session sempre carrega um session id opaco (auth_sessions),
	// nunca um JWT/ID Token — dev e OIDC usam o mesmo SessionStore.
	sessionStore := authn.NewPostgresSessionStore(dbPool)
	if cfg.AuthMode == "oidc" {
		resolver := authn.NewPostgresIdentityResolver(dbPool)
		oidcAuth, discovery, oidcErr := authn.NewOIDCAuthenticator(context.Background(), cfg.AuthIssuer, cfg.AuthAudience, nil, resolver)
		if oidcErr != nil {
			log.Fatalf("OIDC configuration error: %v", oidcErr)
		}
		srv.RegisterOIDCAuthHandlers(oidcAuth, sessionStore, authn.NewOIDCHandler(oidcAuth, discovery, resolver, sessionStore, cfg.AuthIssuer,
			cfg.AuthClientID, cfg.AuthClientSecret, cfg.AuthRedirectURL, cfg.AuthPostLoginURL, cfg.AuthCookieSecure))
	} else {
		srv.RegisterAuthHandlers(dbPool, cfg.DevAuthActive(), sessionStore, cfg.AuthCookieSecure)
	}
	// Convites por e-mail: com OMNIRA_SMTP_HOST há um sender SMTP real; sem ele a capability
	// de entrega é só o dev auth (o admin copia o link). Ver InvitationsHandler.deliveryAvailable.
	var invitationSender tenancyadapters.InvitationSender
	if cfg.SMTPHost != "" {
		smtpSender, smtpErr := tenancyadapters.NewSMTPInvitationSender(tenancyadapters.SMTPConfig{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
			From: cfg.SMTPFrom, ReplyTo: cfg.SMTPReplyTo, TLSMode: cfg.SMTPTLS,
		})
		if smtpErr != nil {
			log.Fatalf("SMTP configuration error: %v", smtpErr)
		}
		invitationSender = smtpSender
	}
	invitationDeliveryAvailable := cfg.DevAuthActive() || invitationSender != nil
	srv.RegisterTenancyHandlers(dbPool, invitationDeliveryAvailable)
	srv.RegisterPresenceHandlers(dbPool)
	srv.RegisterInvitationHandlers(dbPool, cfg.DevAuthActive(), cfg.WebBaseURL, invitationSender)
	crmHandler := srv.RegisterInboxHandlers(dbPool, cfg)
	providerRegistry := channelapplication.NewMapProviderRegistry()
	permissions := channeladapters.NewPostgresPermissionChecker(dbPool)
	management := channelapplication.NewConnectionManagementService(providerRegistry, permissions)
	metaDescriptor := metachannel.Descriptor(cfg.MetaEnabled, "A integração com o WhatsApp oficial (Meta) não está habilitada neste ambiente.")
	if err := providerRegistry.RegisterDescriptor(metaDescriptor, nil); err != nil {
		log.Fatalf("Meta provider descriptor error: %v", err)
	}
	// Sistemas de retaguarda (CRM/ERP) aparecem na mesma aba de Integrações.
	// Guardam credencial cifrada por tenant e não têm sessão nem pareamento.
	erpCipher, erpCipherErr := channelcrypto.NewAESGCM(cfg.CredentialsKey)
	if erpCipherErr != nil {
		log.Fatalf("ERP credential cipher error: %v", erpCipherErr)
	}
	erpCredentials := channeladapters.NewPostgresCredentialStore(dbPool, erpCipher)
	erpConnections := channeladapters.NewPostgresChannelConnectionRepository(dbPool)
	erpAudit := channeladapters.NewChannelAuditRecorder(auditadapters.NewPostgresAuditEventRepository(dbPool))
	if cfg.MetaEnabled {
		metaClient, metaErr := metachannel.NewClient("", "", nil)
		if metaErr != nil {
			log.Fatalf("Meta client config error: %v", metaErr)
		}
		management.Register(metaDescriptor.ID, channelapplication.NewMetaConnectionService(
			metaDescriptor, erpConnections, erpCredentials, permissions, erpAudit, metachannel.NewAccountProbe(metaClient), cfg.WebBaseURL))
	}
	erpProviders := []struct {
		descriptor ports.ProviderDescriptor
		probe      channelapplication.CredentialProbe
	}{
		{toolconnectors.K3GCRMDescriptor(true, ""), toolconnectors.K3GCRMProbe{}},
		{toolconnectors.IXCDescriptor(true, ""), toolconnectors.IXCProbe{}},
	}
	for _, erp := range erpProviders {
		if err := providerRegistry.RegisterDescriptor(erp.descriptor, nil); err != nil {
			log.Fatalf("ERP descriptor error (%s): %v", erp.descriptor.ID, err)
		}
		management.Register(erp.descriptor.ID, channelapplication.NewERPConnectionService(
			erp.descriptor, erpConnections, erpCredentials, permissions, erpAudit, erp.probe,
		))
	}
	wahaReason := ""
	if !cfg.WahaEnabled {
		wahaReason = "WAHA está desabilitado na configuração do servidor."
	}

	// ADR-0015: the directory only exists when WAHA is enabled; without it the group APIs still serve
	// what is stored and report "no WhatsApp connection" for the picker.
	var groupDirectory groupsadapters.Directory
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
		inboundStore := inboxadapters.NewPostgresInboundStore(dbPool).WithConversationKind(identityFlags.ConversationKindEnabled)
		inboundService := inboxapplication.NewInboundService(inboundStore, inboundStore, inboundStore, inboxadapters.TicketStore{PostgresInboundStore: inboundStore}, inboundStore).
			WithParticipants(inboxadapters.NewPostgresParticipantRecorder(dbPool)).
			// ADR-0018: only VERIFIED internal identities make a sender "staff" (nothing matches until one is verified).
			WithIdentity(identityadapters.NewSenderResolver(dbPool, identityFlags.InternalChannelIdentityEnabled), inboundStore)

		intake := inboxadapters.NewWebhookIntake(dbPool, eventStore, inboundService)
		srv.RegisterWahaWebhook(waha.NewWebhookHandler(provider, resolver, eventStore).
			UseSession(func(ctx context.Context, tenantID uuid.UUID, fn func(context.Context) error) error {
				return platformdb.WithSystemTenantSession(ctx, dbPool, tenantID, fn)
			}).
			UseIntake(intake).
			UseGroups(groupsadapters.NewIntake(dbPool, eventStore).WithMaxBytes(groupsMaxBytes())))
		groupDirectory = groupsadapters.NewWahaDirectory(connectionRepo, provider)
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
	srv.RegisterChannelDirectory(dbPool, channeladapters.NewDirectoryHandler(erpConnections, erpCredentials))
	intelligenceFlags := intelligenceapp.FlagsFromEnv(nil)
	if intelligenceFlags.TopicThreadsEnabled {
		topicRepo := intelligenceadapters.NewPostgresTopicRepository(dbPool)
		routingRepo := intelligenceadapters.NewPostgresRoutingRepository(dbPool)
		routingSvc := intelligenceapp.NewRoutingService(routingRepo, topicRepo, intelligenceFlags, intelligencedomain.DefaultRoutingConfig(), nil)
		summaryRepo := intelligenceadapters.NewPostgresSummaryRepository(dbPool)
		summarySvc := intelligenceapp.NewSummaryService(topicRepo, summaryRepo, intelligenceadapters.NewPostgresContextRepository(dbPool), routingRepo,
			intelligenceadapters.NewTopicSummarizerFromConfig(cfg), intelligenceFlags).WithLedger(aiusageadapters.NewPostgresLedger(dbPool))
		ticketPolicySvc := intelligenceapp.NewTopicTicketService(topicRepo, intelligenceadapters.NewPostgresTicketPolicyRepository(dbPool), routingRepo,
			inboxadapters.TicketStore{PostgresInboundStore: inboxadapters.NewPostgresInboundStore(dbPool)}, intelligenceapp.NewTopicService(topicRepo), intelligenceFlags)
		copilotSvc := intelligenceapp.NewCopilotService(intelligenceapp.NewContextBuilder(topicRepo, intelligenceadapters.NewPostgresContextRepository(dbPool), summaryRepo),
			intelligenceadapters.NewPostgresContextRepository(dbPool), intelligenceadapters.NewModelRouterFromConfig(cfg), intelligenceFlags).WithLedger(aiusageadapters.NewPostgresLedger(dbPool))
		handoffSvc := intelligenceapp.NewHandoffService(intelligenceadapters.NewPostgresHandoffRepository(dbPool), routingRepo, intelligenceFlags, nil)
		topicSvc := intelligenceapp.NewTopicService(topicRepo).WithRouting(routingRepo)
		topicHandler := intelligenceadapters.NewTopicHandler(dbPool, topicSvc, topicRepo).
			WithRouting(routingSvc, routingRepo).WithSummaries(summarySvc).WithTickets(ticketPolicySvc).WithHandoffs(handoffSvc).WithCopilot(copilotSvc).
			WithRestructure(intelligenceapp.NewRestructureService(topicRepo, intelligenceadapters.NewPostgresRestructureRepository(dbPool), routingRepo)).
			WithEvaluation(intelligenceadapters.NewPostgresEvaluationRepository(dbPool))
		// the AI tool gateway: a closed registry of real tools, run with the requesting user's own permissions
		topicHandler.WithTools(intelligenceapp.NewToolGateway(intelligenceadapters.NewPostgresToolCallRepository(dbPool), topicRepo, topicHandler.ToolAuthorizer(),
			intelligenceapp.NewToolExecutors(topicSvc, summarySvc, ticketPolicySvc), intelligenceFlags))
		srv.RegisterIntelligenceHandlers(dbPool, topicHandler)
	}
	if erpCipherErr == nil && erpCipher != nil {
		srv.RegisterAIIntegrationHandlers(dbPool, tenancyadapters.NewAIIntegrationHandler(dbPool, auditadapters.NewPostgresAuditEventRepository(dbPool), erpCipher).WithUsage(aiusageadapters.NewPostgresLedger(dbPool)))
	} else {
		log.Printf("AI integration settings disabled: credential cipher unavailable")
	}
	srv.RegisterGroupHandlers(dbPool, groupsadapters.NewHandler(dbPool, auditadapters.NewPostgresAuditEventRepository(dbPool), groupDirectory))
	if cfg.MetaEnabled {
		connectionRepo := channeladapters.NewPostgresChannelConnectionRepository(dbPool)
		eventStore := channeladapters.NewPostgresWebhookEventStore(dbPool)
		inboundStore := inboxadapters.NewPostgresInboundStore(dbPool).WithConversationKind(identityFlags.ConversationKindEnabled)
		inboundService := inboxapplication.NewInboundService(inboundStore, inboundStore, inboundStore, inboxadapters.TicketStore{PostgresInboundStore: inboundStore}, inboundStore).
			WithParticipants(inboxadapters.NewPostgresParticipantRecorder(dbPool)).
			WithIdentity(identityadapters.NewSenderResolver(dbPool, identityFlags.InternalChannelIdentityEnabled), inboundStore)

		srv.RegisterMetaWebhook(metachannel.Handler{
			Resolver: channeladapters.NewMetaWebhookConnectionResolver(dbPool, connectionRepo),
			Secrets:  channeladapters.NewMetaWebhookSecrets(dbPool, connectionRepo, erpCredentials),
			Intake:   inboxadapters.NewWebhookIntake(dbPool, eventStore, inboundService),
		})
	}

	// PRODUCT.6-M: ativa o caminho real de criação de ticket externo.
	// Reaproveita exatamente os mesmos erpConnections/erpCredentials já
	// construídos acima para a aba de Integrações (REUSE — nenhuma
	// credencial duplicada, nenhum registro de ERP genérico). O resolver é
	// tenant-scoped e resolvido a cada chamada (PRODUCT.6-L) — nenhum
	// conector/credencial global é compartilhado entre tenants aqui.
	if crmHandler != nil {
		// PRODUCT.7B1A: ListCompanies (GET /tenants/{tenant_id}/crm/companies)
		// resolves through the SAME tenant-scoped K3GTicketingRuntimeResolver
		// as ticket creation below — REUSE, not a second resolver/credential.
		// Nothing in crmHandler reads a global K3G client.
		companyResolver := ticketsadapters.NewK3GTicketingRuntimeResolver(dbPool, erpConnections, erpCredentials)
		crmHandler.SetCompanyDirectoryResolver(companyResolver)
		// ADR-0018: the contact classification API validates provider companies in the SAME directory, server-side.
		if ch := srv.ContactClassification(); ch != nil {
			ch.SetCompanyDirectoryResolver(companyResolver)
		}
		// PRODUCT.7B1B: CreateActivity's authorization ("assignee or
		// conversation.manage") and canonical crm_contact_id lookup — reusing
		// the SAME permissions checker already constructed above for
		// ticketing/messaging, and a small Postgres reader scoped to this one
		// narrow need (REUSE → EXTEND → CREATE). CreateActivity does NOT use
		// companyDirectoryResolver: a security review found CompanyDirectory
		// membership insufficient authorization for an external write (it
		// proves a company exists for the tenant, never that it belongs to
		// this conversation's CRM contact), so the route is contained to
		// never call a K3G provider until PRODUCT.7B2 establishes a real
		// Contact/Conversation→Company linkage.
		crmHandler.SetActivityPermissionChecker(permissions)
		crmHandler.SetActivityConversationReader(inboxadapters.NewPostgresActivityConversations(dbPool))
		// PRODUCT.7B2B: durable Contact<->Company evidence, captured
		// best-effort after a successful external ticket create/replay
		// (see CreateTicket's recordTicketSelectionEvidence). Never a CRM
		// contact binding, never able to influence the ticket Result/HTTP
		// response.
		crmHandler.SetConversationContactReader(inboxadapters.NewPostgresConversationContacts(dbPool))
		crmHandler.SetEvidenceStore(crmevidenceadapters.NewPostgresEvidenceStore(dbPool))

		externalTicketService := ticketsapplication.NewService(
			channeladapters.NewPostgresPermissionChecker(dbPool),
			ticketsadapters.NewConversationAuthorizer(dbPool),
			ticketsadapters.NewAttemptStore(dbPool),
			ticketsadapters.NewLocalTicketStore(dbPool),
			ticketsadapters.NewK3GTicketingRuntimeResolver(dbPool, erpConnections, erpCredentials),
		)
		// ADR-0018: the ticket targets the OMNIRA account of the company the directory validated.
		if identityFlags.CustomerAccountsEnabled {
			externalTicketService = externalTicketService.WithAccounts(accountsadapters.NewTicketAccountResolver(dbPool))
		}
		crmHandler.SetExternalTicketService(externalTicketService)

		// PRODUCT.6-O1: ativa o caminho real de leitura de ticket
		// escopado por conversa — LOCAL apenas, nunca chama o provider
		// (PRODUCT.6-O0: READ STRATEGY = LOCAL + EXPLICIT REFRESH).
		// Reaproveita exatamente os mesmos ConversationAuthorizer/
		// LocalTicketStore já construídos acima.
		readTicketService := ticketsapplication.NewReadConversationTicketService(
			channeladapters.NewPostgresPermissionChecker(dbPool),
			ticketsadapters.NewConversationAuthorizer(dbPool),
			ticketsadapters.NewLocalTicketStore(dbPool),
		)
		crmHandler.SetReadTicketService(readTicketService)

		// PRODUCT.6-O1R: ativa o caminho real de refresh explícito da
		// projeção a partir do provider. Reaproveita exatamente os mesmos
		// erpConnections/erpCredentials/ConversationAuthorizer/
		// LocalTicketStore já construídos acima — nenhuma credencial
		// duplicada, nenhum resolver global.
		refreshTicketService := ticketsapplication.NewRefreshTicketProjectionService(
			channeladapters.NewPostgresPermissionChecker(dbPool),
			ticketsadapters.NewConversationAuthorizer(dbPool),
			ticketsadapters.NewLocalTicketStore(dbPool),
			ticketsadapters.NewK3GTicketingRuntimeResolver(dbPool, erpConnections, erpCredentials),
		)
		crmHandler.SetRefreshTicketService(refreshTicketService)

		updateExternalTicketStatusService := ticketsapplication.NewUpdateExternalTicketStatusService(
			channeladapters.NewPostgresPermissionChecker(dbPool),
			ticketsadapters.NewConversationAuthorizer(dbPool),
			ticketsadapters.NewStatusMutationAttemptStore(dbPool),
			ticketsadapters.NewLocalTicketStore(dbPool),
			ticketsadapters.NewK3GTicketingRuntimeResolver(dbPool, erpConnections, erpCredentials),
		)
		crmHandler.SetUpdateExternalTicketStatusService(updateExternalTicketStatusService)
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

// groupsMaxBytes is the cap for the WhatsApp group tables on the local disk (ADR-0015 G6):
// OMNIRA_GROUPS_MAX_BYTES, default 1 GiB; 0 disables it.
func groupsMaxBytes() int64 {
	if v := os.Getenv("OMNIRA_GROUPS_MAX_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
		log.Printf("OMNIRA_GROUPS_MAX_BYTES=%q is not a number; using the default", v)
	}
	return groupsadapters.DefaultMaxBytes
}
