package main

import (
	"context"
	"flag"
	aiusageadapters "github.com/omnira/omnira/internal/aiusage/adapters"
	attendanceadapters "github.com/omnira/omnira/internal/attendance/adapters"
	attendanceapplication "github.com/omnira/omnira/internal/attendance/application"
	auditadapters "github.com/omnira/omnira/internal/audit/adapters"
	inboxadapters "github.com/omnira/omnira/internal/inbox/adapters"
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
	aiadapters "github.com/omnira/omnira/internal/ai/adapters"
	channeladapters "github.com/omnira/omnira/internal/channels/adapters"
	channelcrypto "github.com/omnira/omnira/internal/channels/adapters/crypto"
	"github.com/omnira/omnira/internal/channels/adapters/waha"
	channelapp "github.com/omnira/omnira/internal/channels/application"
	"github.com/omnira/omnira/internal/channels/domain"
	metachannel "github.com/omnira/omnira/internal/channels/meta"
	flowsadapters "github.com/omnira/omnira/internal/flows/adapters"
	flowsapplication "github.com/omnira/omnira/internal/flows/application"
	flowsports "github.com/omnira/omnira/internal/flows/ports"
	intelligenceadapters "github.com/omnira/omnira/internal/intelligence/adapters"
	intelligenceapp "github.com/omnira/omnira/internal/intelligence/application"
	intelligencedomain "github.com/omnira/omnira/internal/intelligence/domain"
	mediaadapters "github.com/omnira/omnira/internal/media/adapters"
	mediaapp "github.com/omnira/omnira/internal/media/application"
	mediaports "github.com/omnira/omnira/internal/media/ports"
	messagesadapters "github.com/omnira/omnira/internal/messages/adapters"
	messagesapplication "github.com/omnira/omnira/internal/messages/application"
	"github.com/omnira/omnira/internal/outbox/adapters"
	"github.com/omnira/omnira/internal/outbox/application"
	"github.com/omnira/omnira/internal/platform/config"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platform/health"
	presenceadapters "github.com/omnira/omnira/internal/presence/adapters"
	presenceapplication "github.com/omnira/omnira/internal/presence/application"
	routingadapters "github.com/omnira/omnira/internal/routing/adapters"
	routingapp "github.com/omnira/omnira/internal/routing/application"
	routingports "github.com/omnira/omnira/internal/routing/ports"
	"github.com/omnira/omnira/internal/worker/delivery"
	flowsworker "github.com/omnira/omnira/internal/worker/flows"
	"github.com/omnira/omnira/internal/worker/hubprojector"
	intelligenceworker "github.com/omnira/omnira/internal/worker/intelligence"
	"github.com/omnira/omnira/internal/worker/jobsstream"
	"github.com/omnira/omnira/internal/worker/publisher"
	"github.com/omnira/omnira/internal/worker/realtime"
	routingworker "github.com/omnira/omnira/internal/worker/routing"
	"github.com/redis/go-redis/v9"
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

	// PILOT.4D3-C1: OMNIRA_JOBS' stream policy (retention, MaxAge, MaxBytes,
	// Discard, ...) is ensured exactly once, here, before ANY consumer,
	// publisher, or reconciler starts. jobsstream.Ensure applies the
	// canonical policy, reads it back, and verifies the server actually
	// accepted it — routing/delivery consumers below only ever
	// CreateOrUpdateConsumer their own durable consumer on the stream this
	// returns; neither may configure stream policy anymore. Fail closed: if
	// the stream can't be brought to the canonical policy, nothing that
	// depends on its retention semantics (the reconciler, in particular) may
	// start against a stream in an unknown state.
	if _, err := jobsstream.Ensure(context.Background(), js); err != nil {
		log.Fatalf("failed to ensure OMNIRA_JOBS stream policy: %v", err)
	}

	// Valkey (presence, IAM4.2-A/B1): optional at boot like NATS/WAHA — a
	// misconfigured/unreachable Valkey means presence stays unobserved (and,
	// for any tenant with routing_require_presence=true, automated routing
	// fails closed with ports.ErrPresenceUnavailable), never that the worker
	// refuses to start. presenceChecker is declared as the interface type
	// itself and left as a true nil interface unless Valkey is actually
	// configured — assigning a nil *presenceadapters.Store to it directly
	// would produce a non-nil interface wrapping a nil pointer, defeating
	// PostgresAssignmentRepository's nil-safety check. The reaper/last-seen
	// goroutines that also use this store are started further down, once
	// workerCtx exists.
	var presenceStore *presenceadapters.Store
	var presenceChecker routingports.PresenceChecker
	if cfg.ValkeyURL != "" {
		if valkeyOpts, valkeyErr := redis.ParseURL(cfg.ValkeyURL); valkeyErr != nil {
			log.Printf("warning: invalid OMNIRA_VALKEY_URL: %v\n", valkeyErr)
		} else {
			valkeyClient := redis.NewClient(valkeyOpts)
			defer valkeyClient.Close()
			if pingErr := valkeyClient.Ping(context.Background()).Err(); pingErr != nil {
				log.Printf("warning: failed to connect to Valkey: %v\n", pingErr)
			}
			presenceStore = presenceadapters.NewStore(valkeyClient)
			presenceChecker = presenceStore
		}
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
		routingapp.NewService(routingadapters.NewPostgresAssignmentRepository(dbPool, presenceChecker)),
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

	// Routing liveness (IAM4.2-B0): re-enqueues job.routing.assign.v1 for
	// conversations still durably unassigned after JetStream redelivery gave
	// up — never assigns anything itself, and does not gate on presence
	// (IAM4.2-B1 is separate and not implemented). Safety sweep is
	// unconditional; the presence wakeup only needs NATS, already required.
	// Service Hub inbox projection (read model), only when OMNIRA_HUB_PROJECTOR_ENABLED=true and the Hub migrations
	// are applied. The (hub, tenant) pairs come from persisted contracts only; see internal/worker/hubprojector.
	if cfg.HubProjectorEnabled {
		interval := time.Duration(cfg.HubProjectorIntervalSeconds) * time.Second
		go hubprojector.New(dbPool).Run(workerCtx, interval)
		log.Printf("Hub inbox projector started (every %s)\n", interval)
	}
	livenessRepo := routingadapters.NewPostgresLivenessRepository(dbPool)
	go routingworker.NewSweep(livenessRepo).Run(workerCtx)
	presenceWakeup := routingworker.NewPresenceWakeup(livenessRepo, nc)
	if err := presenceWakeup.Start(workerCtx); err != nil {
		log.Printf("warning: failed to start routing liveness presence wakeup: %v", err)
	} else {
		defer presenceWakeup.Stop()
	}

	// Presence reaper (IAM4.2-A, ADR-0010): the deterministic expiry loop that
	// detects online->offline. Never depends on Valkey keyspace notifications.
	// Reuses the presenceStore connected above; nil when Valkey is not
	// configured or unreachable, in which case presence simply stays
	// unobserved rather than the worker refusing to start.
	if presenceStore != nil {
		presencePublisher := presenceadapters.NewNatsTransitionPublisher(nc)
		presenceLastSeen := presenceadapters.NewCoalescedLastSeenWriter(dbPool)
		go presenceLastSeen.RunFlushLoop(workerCtx, 2*time.Minute)
		go presenceapplication.NewReaper(presenceStore, presencePublisher, presenceLastSeen).Run(workerCtx)
	}

	// Outbound channel delivery (unofficial WhatsApp via WAHA).
	if cfg.WahaEnabled || cfg.MetaEnabled {
		cipher, err := channelcrypto.NewAESGCM(cfg.CredentialsKey)
		if err != nil {
			log.Fatalf("channel credential cipher error: %v", err)
		}
		credentialStore := channeladapters.NewPostgresCredentialStore(dbPool, cipher)
		registry := channelapp.NewMapProviderRegistry()
		if cfg.WahaEnabled {
			client, err := waha.NewClient(cfg.WahaBaseURL, cfg.WahaAPIKey, nil)
			if err != nil {
				log.Fatalf("WAHA client config error: %v", err)
			}
			provider, err := waha.NewProvider(client, credentialStore)
			if err != nil {
				log.Fatalf("WAHA provider config error: %v", err)
			}
			registry.Register(domain.ProviderWAHA, provider)
		}
		if cfg.MetaEnabled {
			metaClient, err := metachannel.NewClient("", "", nil)
			if err != nil {
				log.Fatalf("Meta client config error: %v", err)
			}
			metaProvider, err := metachannel.NewProvider(metaClient, credentialStore)
			if err != nil {
				log.Fatalf("Meta provider config error: %v", err)
			}
			registry.Register(domain.ProviderMetaCloud, metaProvider)
		}
		channelSvc := channelapp.NewChannelService(channeladapters.NewPostgresChannelConnectionRepository(dbPool), registry)
		deliveryHandler, err := delivery.NewHandler(delivery.NewPostgresOutboundStore(dbPool), channelSvc, delivery.MaxAttempts)
		if err != nil {
			log.Fatalf("failed to configure delivery worker: %v", err)
		}
		// ADR-0024: operator files. The worker reads them back verified against their recorded size and SHA-256.
		if cfg.MediaDir != "" {
			if outFiles, ferr := mediaadapters.NewOutboundFiles(cfg.MediaDir); ferr != nil {
				log.Printf("outbound media delivery disabled: %v", ferr)
			} else {
				deliveryHandler.WithMediaFiles(outFiles)
				log.Printf("Outbound media delivery enabled (dir=%s/outbound)\n", cfg.MediaDir)
			}
		}
		deliveryConsumer, err := delivery.StartConsumer(workerCtx, js, deliveryHandler)
		if err != nil {
			log.Fatalf("failed to start delivery consumer: %v", err)
		}
		defer deliveryConsumer.Stop()
		log.Printf("Outbound delivery consumer started (waha=%t meta=%t)\n", cfg.WahaEnabled, cfg.MetaEnabled)

		// Stranded-send reconciliation (PILOT.4D3-B2, activated PILOT.4D3-C1):
		// recreates durable send intent for a queued message whose
		// job.channel.send_text.v1 envelope is old enough that OMNIRA_JOBS'
		// own MaxAge guarantees it is no longer retained. Sourced from the
		// SAME jobsstream.MaxAge/ReconciliationGrace values jobsstream.Ensure
		// applied to the stream above — never a separately-tracked value, so
		// drift between "what the stream actually keeps" and "what the
		// reconciler thinks it can rely on" is impossible by construction.
		// PILOT.4D3-C1 only builds/tests this binary; deploying it (and so
		// actually activating bounded retention live) is PILOT.4D3-C2.
		reconciler := delivery.NewReconciler(delivery.NewPostgresReconciliationStore(dbPool), jobsstream.MaxAge, jobsstream.ReconciliationGrace)
		if reconciler.ShouldRun() {
			go reconciler.Run(workerCtx)
		} else {
			log.Printf("channel-send reconciliation: disabled (OMNIRA_JOBS MaxAge<=0, nothing to reconcile against)")
		}
	} else {
		log.Printf("Outbound delivery disabled (OMNIRA_WAHA_ENABLED != true)\n")
	}

	// Outbound media housekeeping (ADR-0024): independent of which providers deliver, so uploads expire and files are purged either way.
	if cfg.MediaDir != "" {
		if outFiles, ferr := mediaadapters.NewOutboundFiles(cfg.MediaDir); ferr != nil {
			log.Printf("outbound media sweeper disabled: %v", ferr)
		} else {
			go mediaadapters.NewOutboundSweeper(dbPool, outFiles, 60*24*time.Hour).Run(workerCtx, 15*time.Minute)
		}
	}

	// Inbound media (ADR-0016 M1): fetch from WAHA right away (it deletes its copy within minutes), check the real
	// type, quarantine, scan with ClamAV, and only then publish. Fails closed: without the antivirus nothing is
	// ever published, it just waits in quarantine.
	if (cfg.WahaEnabled || cfg.MetaEnabled) && cfg.MediaDir != "" {
		store, err := mediaadapters.NewFileStore(cfg.MediaDir)
		if err != nil {
			log.Fatalf("media store error: %v", err)
		}
		var wahaFetcher mediaports.Fetcher
		if cfg.WahaEnabled {
			wahaClient, err := waha.NewClient(cfg.WahaBaseURL, cfg.WahaAPIKey, nil)
			if err != nil {
				log.Fatalf("media: WAHA client config error: %v", err)
			}
			wf, err := mediaadapters.NewWahaFetcher(wahaClient, cfg.WahaBaseURL)
			if err != nil {
				log.Fatalf("media fetcher error: %v", err)
			}
			wahaFetcher = wf
		}
		var metaMediaClient *metachannel.Client
		if cfg.MetaEnabled {
			if metaMediaClient, err = metachannel.NewClient("", "", nil); err != nil {
				log.Fatalf("media: Meta client config error: %v", err)
			}
		}
		mediaCipher, err := channelcrypto.NewAESGCM(cfg.CredentialsKey)
		if err != nil {
			log.Fatalf("media: credential cipher error: %v", err)
		}
		fetcher := mediaadapters.NewRoutingFetcher(dbPool, channeladapters.NewPostgresCredentialStore(dbPool, mediaCipher), wahaFetcher, metaMediaClient)
		av := mediaadapters.NewClamAV(cfg.ClamAVAddr)
		mediaCounters := mediaapp.NewCounters()
		hc.ExtraMetrics = mediaCounters.Render
		mediaProc, err := mediaapp.NewProcessor(mediaadapters.NewPostgresRepository(dbPool), store, fetcher, av, mediaapp.DefaultConfig(), mediaCounters)
		if err != nil {
			log.Fatalf("media processor error: %v", err)
		}
		go mediaProc.Run(workerCtx, 2*time.Second)
		// Audio transcription (ADR-0016 M2): local engine only, the audio never leaves this machine.
		if cfg.WhisperURL != "" {
			engine, err := mediaadapters.NewWhisper(cfg.WhisperURL, "whisper-large-v3-turbo-q5_0")
			if err != nil {
				log.Fatalf("whisper config error: %v", err)
			}
			transcriber, err := mediaapp.NewTranscriptionProcessor(mediaadapters.NewPostgresRepository(dbPool), store, engine, mediaCounters)
			if err != nil {
				log.Fatalf("transcription processor error: %v", err)
			}
			go transcriber.Run(workerCtx, 3*time.Second)
			log.Printf("Audio transcription started (local engine at %s)\n", cfg.WhisperURL)
		} else {
			log.Printf("Audio transcription disabled (OMNIRA_WHISPER_URL empty)\n")
		}
		// Image / PDF reading with the TENANT'S OWN Gemini key (ADR-0017 Wave 9/10): off unless the flag is on, and even
		// then only tenants that opted in are ever touched. It cannot start without the usage ledger and the budget.
		if intelligenceapp.FlagsFromEnv(nil).MultimodalAnalysisEnabled {
			credCipher, cerr := channelcrypto.NewAESGCM(cfg.CredentialsKey)
			gemini, gerr := mediaadapters.NewGemini("")
			if cerr != nil || gerr != nil {
				log.Printf("Image/PDF analysis disabled (credential cipher: %v, gemini adapter: %v)\n", cerr, gerr)
			} else {
				repo := mediaadapters.NewPostgresRepository(dbPool)
				vision, verr := mediaapp.NewVisionProcessor(repo, repo, store, mediaadapters.NewTenantAIResolver(dbPool, credCipher), gemini, aiusageadapters.NewPostgresLedger(dbPool), mediaCounters)
				if verr != nil {
					log.Fatalf("vision processor error: %v", verr)
				}
				go vision.Run(workerCtx, 5*time.Second)
				log.Printf("Image/PDF analysis started (per-tenant Gemini opt-in, monthly budget enforced)\n")
			}
		} else {
			log.Printf("Image/PDF analysis disabled (OMNIRA_MULTIMODAL_ANALYSIS_ENABLED is not true)\n")
		}
		go func() {
			probe := func() {
				pctx, cancel := context.WithTimeout(workerCtx, 8*time.Second)
				defer cancel()
				err := av.Ping(pctx)
				mediaCounters.SetAntivirusUp(err == nil)
				if err != nil {
					log.Printf("media: antivirus not answering (%v); files wait in quarantine and are NOT published", err)
				}
			}
			probe()
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-workerCtx.Done():
					return
				case <-t.C:
					probe()
				}
			}
		}()
		log.Printf("Inbound media pipeline started (dir=%s, clamd=%s)\n", cfg.MediaDir, cfg.ClamAVAddr)
	} else {
		log.Printf("Inbound media pipeline disabled (needs OMNIRA_WAHA_ENABLED=true and OMNIRA_MEDIA_DIR)\n")
	}

	// Conversation Intelligence (ADR-0017): event -> durable job -> runner. Off with topic_threads_enabled=false. It is never
	// a single point of failure: with this worker stopped, messages are still ingested, read and answered; jobs wait.
	intelligenceFlags := intelligenceapp.FlagsFromEnv(nil)
	if intelligenceFlags.TopicThreadsEnabled {
		intelligenceCounters := intelligenceapp.NewCounters()
		prev := hc.ExtraMetrics
		hc.ExtraMetrics = func() string {
			out := intelligenceCounters.Render()
			if prev != nil {
				out = prev() + out
			}
			return out
		}
		jobStore := intelligenceadapters.NewPostgresJobStore(dbPool)
		topicRepo := intelligenceadapters.NewPostgresTopicRepository(dbPool)
		routingSvc := intelligenceapp.NewRoutingService(intelligenceadapters.NewPostgresRoutingRepository(dbPool), topicRepo, intelligenceFlags, intelligencedomain.DefaultRoutingConfig(), intelligenceCounters)
		session := func(ctx context.Context, tenantID uuid.UUID, fn func(context.Context) error) error {
			return platformdb.WithSystemTenantSession(ctx, dbPool, tenantID, fn)
		}
		var pipeline intelligenceapp.Pipeline = intelligenceapp.RoutingPipeline{Routing: routingSvc}
		if intelligenceFlags.TopicSummariesEnabled {
			// best-effort extra step: a summary problem never fails or delays message processing
			summarySvc := intelligenceapp.NewSummaryService(topicRepo, intelligenceadapters.NewPostgresSummaryRepository(dbPool), intelligenceadapters.NewPostgresContextRepository(dbPool),
				intelligenceadapters.NewPostgresRoutingRepository(dbPool), intelligenceadapters.NewTopicSummarizerFromConfig(cfg), intelligenceFlags).WithLedger(aiusageadapters.NewPostgresLedger(dbPool))
			pipeline = intelligenceapp.SummaryPipeline{Next: pipeline, Summaries: summarySvc}
		}
		if intelligenceFlags.TopicAIRoutingEnabled {
			// shadow only: the proposal is recorded, never applied; a failure never touches the routing
			pipeline = intelligenceapp.ShadowPipeline{Next: pipeline, Classifier: intelligenceapp.NewTopicClassifier(intelligenceadapters.NewPostgresRoutingRepository(dbPool),
				intelligenceadapters.NewPostgresContextRepository(dbPool), intelligenceadapters.NewPostgresSummaryRepository(dbPool), intelligenceadapters.NewModelRouterFromConfig(cfg), intelligenceFlags, intelligenceCounters).WithLedger(aiusageadapters.NewPostgresLedger(dbPool))}
		}
		if intelligenceFlags.AutoTicketPolicyEnabled {
			// only the unambiguous policy actions, and a problem here never fails the job
			pipeline = intelligenceapp.TicketPolicyPipeline{Next: pipeline, Tickets: intelligenceapp.NewTopicTicketService(topicRepo, intelligenceadapters.NewPostgresTicketPolicyRepository(dbPool),
				intelligenceadapters.NewPostgresRoutingRepository(dbPool), inboxadapters.TicketStore{PostgresInboundStore: inboxadapters.NewPostgresInboundStore(dbPool)}, intelligenceapp.NewTopicService(topicRepo), intelligenceFlags)}
		}
		if intelligenceFlags.PrivateHandoffEnabled {
			// before routing: a valid token places the message with the strongest evidence
			pipeline = intelligenceapp.HandoffPipeline{Next: pipeline, Handoffs: intelligenceapp.NewHandoffService(intelligenceadapters.NewPostgresHandoffRepository(dbPool),
				intelligenceadapters.NewPostgresRoutingRepository(dbPool), intelligenceFlags, intelligenceCounters)}
		}
		runner := intelligenceapp.NewJobRunner(jobStore, pipeline, session, intelligenceapp.DefaultJobRunnerConfig(), intelligenceCounters)
		go runner.Run(workerCtx, 2*time.Second)
		handler, err := intelligenceworker.NewHandler(jobStore)
		if err != nil {
			log.Fatalf("intelligence handler error: %v", err)
		}
		intelligenceConsumer, err := intelligenceworker.StartConsumer(workerCtx, js, handler)
		if err != nil {
			log.Fatalf("failed to start intelligence consumer: %v", err)
		}
		defer intelligenceConsumer.Stop()
		log.Printf("Conversation Intelligence pipeline started (auto routing=%v)\n", intelligenceFlags.TopicAutoRoutingEnabled)
	}

	// Flow Builder runtime (ADR-0019), only when OMNIRA_FLOWS_ENABLED=true: the inbound-message consumer and the sweeper for
	// timeouts, stranded conversations and runs of closed conversations.
	if cfg.FlowsEnabled {
		flowRepo := flowsadapters.NewPostgresFlowRepository(dbPool)
		// The contact's "end this attendance" command closes through the same use case as an agent's finalize (source "system").
		attendanceForFlows := attendanceapplication.NewService(attendanceadapters.NewPostgresRepository(dbPool), attendanceadapters.NewAuthorizer(dbPool),
			attendanceadapters.NewAuditor(auditadapters.NewPostgresAuditEventRepository(dbPool)))
		flowEffects := flowsadapters.NewPostgresEffects(dbPool, messagesapplication.NewSystemSender(messagesadapters.NewPostgresOutboundStore(dbPool))).WithCloser(attendanceForFlows)
		flowCounters := flowsapplication.NewCounters()
		prevMetrics := hc.ExtraMetrics
		hc.ExtraMetrics = func() string {
			out := flowCounters.Render()
			if prevMetrics != nil {
				out = prevMetrics() + out
			}
			return out
		}
		// AI nodes (ADR-0019): only with OMNIRA_FLOWS_AI_ENABLED=true AND a ready platform model; otherwise they take their error port.
		var flowAI flowsports.AIGateway
		if cfg.FlowsAIEnabled && cfg.AIReady() {
			gen, gerr := aiadapters.NewTextGenerator(cfg.AIProvider, cfg.AIAPIKey, cfg.AIModel, time.Duration(cfg.AITimeoutSeconds)*time.Second)
			if gerr != nil {
				log.Printf("Flow AI nodes disabled: %v\n", gerr)
			} else {
				flowAI = flowsapplication.NewAIService(gen, aiusageadapters.NewPostgresLedger(dbPool), flowRepo, cfg.AIProvider, cfg.AIModel)
				log.Printf("Flow AI nodes enabled (provider=%s)\n", cfg.AIProvider)
			}
		}
		flowEngine := flowsapplication.NewEngine(flowRepo, flowRepo, flowEffects, flowsapplication.AllExecutorsWith(flowAI)).WithMetrics(flowCounters)
		flowHandler, err := flowsworker.NewHandler(flowsworker.NewPostgresConversationRunner(dbPool), flowEngine)
		if err != nil {
			log.Fatalf("flows handler error: %v", err)
		}
		flowConsumer, err := flowsworker.StartConsumer(workerCtx, js, flowHandler)
		if err != nil {
			log.Fatalf("failed to start flows consumer: %v", err)
		}
		defer flowConsumer.Stop()
		go flowsworker.NewSweeper(dbPool, flowRepo, flowEngine).WithMetrics(flowCounters).Run(workerCtx, 15*time.Second)
		log.Printf("Flow Builder runtime started\n")
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
