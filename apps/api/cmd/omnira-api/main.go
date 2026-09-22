package main

import (
	"context"
	"flag"
	metachannel "github.com/omnira/omnira/internal/channels/meta"
	ports "github.com/omnira/omnira/internal/channels/ports"
	toolconnectors "github.com/omnira/omnira/internal/tool/connectors"
	"log"
	"os"
	"os/signal"
	"strings"
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
	tenancyadapters "github.com/omnira/omnira/internal/tenancy/adapters"
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
	if cfg.AuthMode == "oidc" {
		resolver := authn.NewPostgresIdentityResolver(dbPool)
		sessionStore := authn.NewPostgresSessionStore(dbPool)
		oidcAuth, discovery, oidcErr := authn.NewOIDCAuthenticator(context.Background(), cfg.AuthIssuer, cfg.AuthAudience, nil, resolver)
		if oidcErr != nil {
			log.Fatalf("OIDC configuration error: %v", oidcErr)
		}
		srv.RegisterOIDCAuthHandlers(oidcAuth, authn.NewOIDCHandler(oidcAuth, discovery, resolver, sessionStore, cfg.AuthIssuer,
			cfg.AuthClientID, cfg.AuthClientSecret, cfg.AuthRedirectURL, cfg.AuthPostLoginURL, cfg.AuthCookieSecure))
	} else {
		srv.RegisterAuthHandlers(cfg.DevAuthActive(), cfg.AuthCookieSecure)
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
	if err := providerRegistry.RegisterDescriptor(metachannel.Descriptor(), nil); err != nil {
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
	// Compartilhado entre os wirings WAHA e Meta: o cliente CRM é resolvido no
	// primeiro bloco que executa e reaproveitado pelo outro.
	var k3gClientForAPI *toolconnectors.K3GCRMClient

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

		// R5: Wiring de CRM (K3G) no webhook inbound. Quando mensagem chega,
		// procura contato no CRM; se não existe, cria automaticamente.
		if k3gConnection, k3gErr := erpConnections.FindByTenant(context.Background(), uuid.Nil); k3gErr == nil && k3gConnection != nil {
			for _, conn := range k3gConnection {
				if conn.Provider == "k3g_crm" && conn.Status == "active" {
					k3gCred, credErr := erpCredentials.Resolve(context.Background(), conn.SecretRef)
					if credErr == nil && k3gCred.Fields != nil {
						k3gClient, clientErr := toolconnectors.NewK3GCRMClient(toolconnectors.K3GCRMConfig{
							BaseURL: k3gCred.Fields["base_url"],
							Token:   k3gCred.Fields["token"],
						})
						if clientErr == nil && k3gClient != nil {
							companies, listErr := k3gClient.ListCompanies(context.Background())
							if listErr == nil && len(companies) > 0 {
								var acmeCompanyID string
								for _, co := range companies {
									if strings.Contains(strings.ToUpper(co.Name), "ACME") {
										acmeCompanyID = co.ID
										break
									}
								}
								if acmeCompanyID != "" {
									crmConnector := toolconnectors.NewK3GCRMConnector(k3gClient)
									inboundService.WithCRM(crmConnector, acmeCompanyID)
									k3gClientForAPI = k3gClient
								}
							}
						}
					}
					break
				}
			}
		}

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

		// R5: mesmo wiring de CRM para Meta webhook (reutiliza k3gClientForAPI se já foi configurado)
		if k3gClientForAPI != nil {
			// Use o cliente já configurado
			if k3gConnection, k3gErr := erpConnections.FindByTenant(context.Background(), uuid.Nil); k3gErr == nil && k3gConnection != nil {
				for _, conn := range k3gConnection {
					if conn.Provider == "k3g_crm" && conn.Status == "active" {
						companies, listErr := k3gClientForAPI.ListCompanies(context.Background())
						if listErr == nil && len(companies) > 0 {
							var acmeCompanyID string
							for _, co := range companies {
								if strings.Contains(strings.ToUpper(co.Name), "ACME") {
									acmeCompanyID = co.ID
									break
								}
							}
							if acmeCompanyID != "" {
								crmConnector := toolconnectors.NewK3GCRMConnector(k3gClientForAPI)
								inboundService.WithCRM(crmConnector, acmeCompanyID)
							}
						}
						break
					}
				}
			}
		}

		srv.RegisterMetaWebhook(metachannel.Handler{
			VerifyToken: cfg.MetaVerifyToken,
			AppSecret:   cfg.MetaAppSecret,
			Resolver:    channeladapters.NewMetaWebhookConnectionResolver(dbPool, connectionRepo),
			Intake:      inboxadapters.NewWebhookIntake(dbPool, eventStore, inboundService),
		})
	}

	// R5.2: Configura K3G CRM client no handler para ListCompanies
	if crmHandler != nil && k3gClientForAPI != nil {
		crmHandler.SetK3GCRMClient(k3gClientForAPI)
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
