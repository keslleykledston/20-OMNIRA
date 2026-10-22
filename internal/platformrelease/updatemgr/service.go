// Package updatemgr é o "Update Manager API" do ADR-0054 §2.1 (F6 — Backend e autorização
// global): release service, planner, preflight, update runs/event store e a comunicação
// autenticada com o Update Agent (F5). Autoridade é sempre IsPlatformOperator
// (internal/hub/provisioning), nunca tenant_id/membership de tenant/Hub/API Headless — mesmo
// modelo de confiança de internal/hub/companies (serviço roda numa sessão de SISTEMA,
// re-autorizando o operador explicitamente em toda chamada, nunca confiando em RLS como única
// barreira).
//
// Comunicação com o agente (especificação seção E: "API nunca executa shell ou docker
// diretamente"): este serviço NUNCA toca Docker. Ele escreve um run no MESMO ledger SQLite
// local que o Update Agent (internal/platformrelease/agent, apps/update-agent) já usa —
// exatamente como apps/updatectl já faz hoje (AcquireLock -> CreateRun -> ReleaseLock) — e
// espelha o resultado no Postgres para a view administrativa (histórico/auditoria). A
// autoridade de execução continua sendo o ledger local (ADR-0054 §2.4): "nunca duas fontes de
// verdade concorrentes para o que fazer agora". O comentário de apps/update-agent/cmd/
// omnira-update-agent/main.go já antecipava isto: "quando o backend F6 existir, também ele,
// sempre pelo MESMO ledger local, nunca por um caminho separado".
package updatemgr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnira/omnira/internal/hub/provisioning"
	platformdb "github.com/omnira/omnira/internal/platform/db"
	"github.com/omnira/omnira/internal/platformrelease/agent"
	"github.com/omnira/omnira/internal/platformrelease/domain"
)

var (
	// ErrForbidden: o chamador não é um platform operator ativo. Deve ser respondido como um
	// 404 uniforme (mesmo padrão de internal/hub/adapters), nunca 403 — a API não confirma nem
	// nega que o recurso existe para quem não tem acesso.
	ErrForbidden = errors.New("updatemgr: not allowed")
	ErrInvalid   = errors.New("updatemgr: invalid request")
	ErrNotFound  = errors.New("updatemgr: not found")
	// ErrKeyMismatch: a mesma Idempotency-Key foi reusada com um pedido diferente.
	ErrKeyMismatch = errors.New("updatemgr: idempotency key reused with a different request")
)

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// currentAgentProtocol espelha release/policies/compatibility.yaml "agent_protocol.current" —
// mantenha sincronizado (mesmo padrão de internal/platformrelease/domain.SupportedSchemaVersion
// espelhando o JSON Schema: uma constante Go, não um parser de YAML em runtime, porque o valor
// é uma decisão de política rara, nunca algo que varia por requisição).
const currentAgentProtocol = 1

// backupEvidenceMaxAgeHours espelha release/policies/backup.yaml
// "promotion_gate.stable_channel.max_evidence_age_hours" — a partir de F9, GATE real que
// bloqueia qualquer plano no canal "stable" sem evidência fresca (ver checkStableBackupGate);
// nos demais canais (dev/candidate) permanece só um WARN informativo, como já era em F6.
const backupEvidenceMaxAgeHours = 24

// backupEvidenceRestoreVerifiedKind é o ÚNICO valor de RecordBackupEvidence.kind que o gate do
// canal stable aceita (release/policies/backup.yaml recovery_classes.R3.evidence_sources:
// scripts/backup-restore-check.sh / scripts/backup-restore-hub-check.sh) — achado Codex [P1]:
// um `kind="logical_dump"` com `verified=true` só prova que o pg_dump não falhou/não está
// vazio, nunca que os dados são de fato restauráveis. "verified" e "restore testado" são
// afirmações DIFERENTES; aceitar a primeira como a segunda seria "inventar proteção"
// (especificação §4). Candidate/dev continuam aceitando qualquer kind verificado (WARN-only,
// comportamento de F6 preservado) — só o GATE bloqueante de stable exige este valor exato.
const backupEvidenceRestoreVerifiedKind = "restore_verified"

// revocationMaxCacheAgeSeconds espelha release/policies/signing.yaml
// "revocation.max_cache_age_seconds" — documenta que a lista de revogação nunca é cacheada
// indefinidamente; nesta implementação cada checkRevocation já consulta o Postgres
// diretamente (sem cache de processo nenhum), então o limite é satisfeito trivialmente (idade
// efetiva = 0) — a constante existe para a intenção da política ficar auditável no código,
// nunca para implementar um cache que ainda não existe.
const revocationMaxCacheAgeSeconds = 300

// agentHeartbeatStaleAfter (F9, monitoramento "status do agente") — generoso em relação a
// qualquer --poll-interval real do daemon (segundos a poucos minutos): um gap maior que isto
// sem nenhum heartbeat novo significa o processo morreu ou está preso num único ciclo de
// reconcile por tempo desproporcional, nunca uma variação normal de carga. Julgamento
// operacional, não uma política externa documentada — ajustável sem quebrar nenhum contrato.
const agentHeartbeatStaleAfter = 5 * time.Minute

// stalledRunThreshold (F9, monitoramento "update estagnado") — um run ATIVO (não-terminal)
// sem nenhuma transição de estado por mais tempo que isto é tratado como estagnado. Generoso
// em relação ao tempo real de qualquer step individual (backup/migração/deploy/observação)
// nesta base de código; julgamento operacional, ajustável sem quebrar nenhum contrato.
const stalledRunThreshold = 15 * time.Minute

// observedDigestsStaleAfter (F9, monitoramento "versão real/digest divergente", achado Codex
// [P2] rodada 3) — a observação real do Docker (RecordObservedDigests) tem sua PRÓPRIA
// cadência (uma vez por ciclo de reconcile, nunca o ticker independente de heartbeat de 30s),
// e um ciclo de reconcile pode legitimamente demorar; generoso como stalledRunThreshold pela
// mesma razão. Mais antiga que isto sem nenhuma observação nova significa o Docker parou de
// responder (ou o próprio daemon morreu — AgentStale já cobriria isso separadamente).
const observedDigestsStaleAfter = 15 * time.Minute

// channelRetentionDays espelha release/policies/channels.yaml "retention_days" por canal —
// uma constante Go, não um parser de YAML em runtime, nunca a autoridade real (o arquivo de
// política é a fonte de verdade; esta cópia precisa ser atualizada no MESMO commit que mudar
// o YAML — mesmo espírito de currentAgentProtocol/backupEvidenceMaxAgeHours acima).
var channelRetentionDays = map[domain.Channel]int{
	domain.ChannelDev:       14,
	domain.ChannelCandidate: 30,
	domain.ChannelStable:    180,
}

// Service é o Update Manager API. Mantém também o acesso ao ledger LOCAL do Update Agent
// (mesmo arquivo, mesmo host) para StartRun — ver comentário do pacote.
// SignatureVerifier decide se um manifesto cru é autêntico (assinatura, proveniência,
// revogação — release/policies/signing.yaml) antes de IngestRelease aceitar qualquer coisa.
// ADR-0054 §2.2.4: "Assinatura inválida = bloqueio (fail-closed), sem exceção configurável via
// UI." signing.yaml também proíbe explicitamente substituir o mecanismo real (GitHub Artifact
// Attestation / Sigstore keyless) por "uma assinatura simétrica local provisória tratada como
// equivalente" — por isso o default de produção (blockedExternalVerifier) nunca finge verificar
// coisa alguma; ele BLOQUEIA tudo, honestamente, até um verificador real existir (F9, quando
// gh CLI/credenciais GHCR — hoje BLOCKED_EXTERNAL, achado F0 §7 — estiverem disponíveis).
type SignatureVerifier interface {
	Verify(manifestRaw []byte) error
}

// ErrSignatureUnverifiable: nenhum SignatureVerifier real está configurado nesta instalação.
var ErrSignatureUnverifiable = errors.New("updatemgr: assinatura do manifesto não pode ser verificada nesta instalação (BLOCKED_EXTERNAL — ver release/policies/signing.yaml; gh CLI/credenciais GHCR ausentes, achado F0 §7)")

// blockedExternalVerifier é o default de PRODUÇÃO: recusa todo manifesto, sempre. Achado Codex
// [P1]: antes deste tipo existir, IngestRelease aceitava qualquer manifesto bem-formado (forma
// válida, nunca autenticidade) e StartRun despachava isso ao agente sem qualquer verificação de
// assinatura — exatamente o que o ADR proíbe. Um verificador real é instalado via WithVerifier
// quando F9 o construir; até então, o Update Manager API inteiro fica corretamente BLOCKED para
// qualquer ingestão real, mas totalmente testável com WithVerifier(umVerificadorDeTeste{}).
type blockedExternalVerifier struct{}

func (blockedExternalVerifier) Verify([]byte) error { return ErrSignatureUnverifiable }

type Service struct {
	pool           *pgxpool.Pool
	ledgerPath     string
	installationID string
	holder         string
	lease          time.Duration
	verifier       SignatureVerifier
}

// New cria o serviço. ledgerPath/installationID/lease devem ser EXATAMENTE os mesmos valores
// configurados para o Update Agent deste host (OMNIRA_AGENT_LEDGER_PATH/
// OMNIRA_AGENT_INSTALLATION_ID/OMNIRA_AGENT_LEASE) — são o mesmo arquivo compartilhado, nunca
// um caminho separado. holder identifica este processo (omnira-api) no lock, distinto do
// holder do daemon. O SignatureVerifier default bloqueia toda ingestão (ver WithVerifier).
func New(pool *pgxpool.Pool, ledgerPath, installationID, holder string, lease time.Duration) *Service {
	return &Service{pool: pool, ledgerPath: ledgerPath, installationID: installationID, holder: holder, lease: lease, verifier: blockedExternalVerifier{}}
}

// WithVerifier substitui o verificador de assinatura default (que bloqueia tudo) por um real.
// Nunca chamado por apps/api/cmd/omnira-api/main.go nesta fase — documentado como pendência
// explícita até F9 (ver docs/release/IMPLEMENTATION-STATUS.md).
func (s *Service) WithVerifier(v SignatureVerifier) *Service { s.verifier = v; return s }

// StartRevocationDeliveryLoop (F9, achado Codex [P1] rodada 4): reentrega periodicamente
// qualquer revogação ainda pendente (delivered_to_agent=false) ao ledger local —
// INDEPENDENTE de qualquer tráfego de operador. reconcileRevocationsToLedger, chamada por
// refreshPendingRun, já dava entrega eventual a cada CreatePlan/StartRun/GetInstallation/
// ListRuns real — mas um ambiente sem NENHUMA chamada de operador nesse intervalo (ex. um
// agente autônomo aplicando runs já despachados, sem qualquer `updatectl` ou UI sendo usados)
// nunca veria uma revogação cuja escrita síncrona ao ledger local falhou. Chamado uma única
// vez no startup do processo (apps/api/cmd/omnira-api), nunca por request — reconcileRevocationsToLedger
// é idempotente e barata o suficiente (SELECT vazio na maioria dos ticks) para rodar num
// intervalo curto sem custo real. Retorna uma stop() idempotente que bloqueia até o laço
// encerrar; o chamador também pode só cancelar ctx (ex. no shutdown normal do processo).
func (s *Service) StartRevocationDeliveryLoop(ctx context.Context, interval time.Duration) (stop func()) {
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	syncOnce := func() {
		err := s.tx(loopCtx, func(c context.Context, q platformdb.Querier) error {
			return reconcileRevocationsToLedger(c, q, s.ledgerPath)
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("updatemgr: StartRevocationDeliveryLoop: %v", err)
		}
	}
	go func() {
		defer close(done)
		// Achado Codex [P1], F9 (rodada 9): time.NewTicker só dispara DEPOIS do primeiro
		// `interval` — sem esta chamada imediata, um ledger recém-criado ou recém-upgradado
		// (Ledger.RevocationSyncFresh sem nenhuma linha ainda) ficaria até `interval`
		// segundos antes da primeira sincronização de verdade acontecer, mesmo com a API já
		// rodando. Sincronizar assim que o laço inicia (ANTES do primeiro tick) encurta essa
		// janela ao mínimo operacionalmente possível quando a API está de fato habilitada.
		syncOnce()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				syncOnce()
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

func (s *Service) tx(ctx context.Context, fn func(c context.Context, q platformdb.Querier) error) error {
	return platformdb.WithTenantSession(ctx, s.pool, uuid.Nil, true, func(c context.Context) error {
		return fn(c, platformdb.QuerierFromContext(c, s.pool))
	})
}

// authorize é o gate em transação: o chamador precisa ser um platform operator ATIVO. Nunca
// aceita tenant_id/membership de tenant ou Hub, nunca aceita a API Headless (que nem autentica
// com um authn.Principal de usuário — ver internal/external/adapters.Middleware) como
// autoridade para nada aqui.
func authorize(ctx context.Context, q platformdb.Querier, operator uuid.UUID) error {
	if operator == uuid.Nil {
		return ErrForbidden
	}
	isOp, err := provisioning.IsPlatformOperator(ctx, q, operator)
	if err != nil {
		return err
	}
	if !isOp {
		return ErrForbidden
	}
	return nil
}

func audit(ctx context.Context, q platformdb.Querier, operator uuid.UUID, action string, meta map[string]any) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	// Achado real ao testar contra Postgres: $2 não pode ser reusado para actor_id (UUID) E
	// resource_id (TEXT) no mesmo statement — "inconsistent types deduced for parameter $2".
	// resource_id é sempre o operador como texto, nunca o mesmo valor UUID tipado de actor_id.
	_, err = q.Exec(ctx, `INSERT INTO audit_events (id, tenant_id, actor_id, action, resource_type, resource_id, outcome, correlation_id, metadata)
	                      VALUES ($1, NULL, $2, $3, 'platform', $4, 'success', $5, $6)`,
		uuid.New(), operator, action, operator.String(), uuid.NewString(), raw)
	return err
}

// --- Release (catálogo local, F3/F9 publicam a fonte remota assinada; aqui só ingestão +
// validação do manifesto já obtido) ---

type Release struct {
	ReleaseID       string          `json:"release_id"`
	Channel         string          `json:"channel"`
	PlatformVersion string          `json:"platform_version"`
	Manifest        json.RawMessage `json:"manifest"`
	IngestedBy      uuid.UUID       `json:"ingested_by"`
	IngestedAt      time.Time       `json:"ingested_at"`
}

// IngestRelease valida (internal/platformrelease/domain.ParseManifest/Validate — o MESMO
// verificador estrito de F1) e armazena um manifesto de release já obtido (de um workflow
// release-build.yml real ou de um arquivo local). Idempotente por release_id: uma segunda
// ingestão do MESMO manifesto (mesmos bytes canônicos) é um replay; um release_id repetido com
// conteúdo diferente é ErrKeyMismatch — um manifesto já publicado é imutável (especificação:
// release é imutável depois de assinada).
//
// Buscar o catálogo remoto assinado de verdade (GHCR/índice) é BLOCKED_EXTERNAL nesta sessão
// (F0 §7, sem gh CLI/credenciais) — esta função cobre o que é testável localmente: validação e
// armazenamento de um manifesto já obtido.
func (s *Service) IngestRelease(ctx context.Context, operator uuid.UUID, idempotencyKey string, manifestRaw []byte) (Release, bool, error) {
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return Release{}, false, invalid("Idempotency-Key deve ter 8-128 caracteres")
	}
	m, err := domain.ParseManifest(manifestRaw)
	if err != nil {
		return Release{}, false, invalid("manifesto inválido: %v", err)
	}
	if err := m.Validate(); err != nil {
		return Release{}, false, invalid("manifesto reprovado na validação: %v", err)
	}
	sum := sha256.Sum256(manifestRaw)
	hash := hex.EncodeToString(sum[:])
	var out Release
	var replayed bool
	err = s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		// ADR-0054 §2.2.4, achado Codex [P1]: forma válida (checada acima, domain.Validate)
		// nunca é autenticidade. Sem isto, qualquer manifesto bem-formado (artifacts/digests
		// arbitrários) era aceito e ficava disponível para StartRun despachar ao agente.
		if err := s.verifier.Verify(manifestRaw); err != nil {
			return err
		}
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, operator.String()+"/"+idempotencyKey); err != nil {
			return err
		}
		// Achado Codex [P2]: o dedup por release_id (abaixo) protege contra reingerir a MESMA
		// release imutável, mas sozinho nunca detectava "esta MESMA Idempotency-Key foi usada
		// para uma release DIFERENTE" — o contrato documentado (422 nesse caso) promete a
		// chave em si como identidade do pedido, não só o release_id do corpo.
		var prevKeyHash, prevKeyReleaseID string
		kperr := q.QueryRow(c, `SELECT request_hash, release_id FROM platform_release_ingest_requests WHERE operator_id = $1 AND idempotency_key = $2`, operator, idempotencyKey).
			Scan(&prevKeyHash, &prevKeyReleaseID)
		switch {
		case kperr == nil:
			if prevKeyHash != hash {
				return ErrKeyMismatch
			}
			replayed = true
			return q.QueryRow(c, `SELECT release_id, channel, platform_version, manifest_json, ingested_by, ingested_at FROM platform_releases WHERE release_id = $1`, prevKeyReleaseID).
				Scan(&out.ReleaseID, &out.Channel, &out.PlatformVersion, &out.Manifest, &out.IngestedBy, &out.IngestedAt)
		case !errors.Is(kperr, pgx.ErrNoRows):
			return kperr
		}
		// Achado Codex [P2] (rodada anterior): sem este segundo lock, duas ingestões
		// concorrentes do MESMO release_id (ainda inexistente, chaves diferentes ou de
		// operadores diferentes) podiam ambas passar pelo SELECT abaixo sem achar nada e as
		// duas tentarem o INSERT — a segunda falhava a UNIQUE constraint da PK como um erro
		// 500 genérico, nunca o replay idempotente (200) ou o 422 de conteúdo divergente.
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "platform-release:"+m.ReleaseID); err != nil {
			return err
		}
		var prevHash string
		perr := q.QueryRow(c, `SELECT encode(sha256(manifest_json::text::bytea), 'hex') FROM platform_releases WHERE release_id = $1`, m.ReleaseID).Scan(&prevHash)
		switch {
		case perr == nil:
			// manifest_json round-trips through Postgres jsonb (key order/whitespace may
			// change), então comparamos o hash do release_id reportado pelo PRÓPRIO
			// manifesto já armazenado contra o novo, nunca bytes crus — um manifesto
			// assinado é imutável pelo seu conteúdo semântico, não pela formatação exata.
			var storedCanonical []byte
			if err := q.QueryRow(c, `SELECT manifest_json FROM platform_releases WHERE release_id = $1`, m.ReleaseID).Scan(&storedCanonical); err != nil {
				return err
			}
			var stored domain.ReleaseManifest
			if err := json.Unmarshal(storedCanonical, &stored); err != nil {
				return err
			}
			reStored, _ := json.Marshal(stored)
			reNew, _ := json.Marshal(m)
			if string(reStored) != string(reNew) {
				return ErrKeyMismatch
			}
			replayed = true
		case !errors.Is(perr, pgx.ErrNoRows):
			return perr
		default:
			if _, err := q.Exec(c, `INSERT INTO platform_releases (release_id, channel, platform_version, manifest_json, ingested_by) VALUES ($1, $2, $3, $4, $5)`,
				m.ReleaseID, string(m.Channel), m.PlatformVersion, manifestRaw, operator); err != nil {
				return err
			}
			if err := audit(c, q, operator, "platform.release.ingested", map[string]any{"release_id": m.ReleaseID, "channel": m.Channel, "platform_version": m.PlatformVersion}); err != nil {
				return err
			}
		}
		// Registra a chave DESTE pedido -> release_id, esteja a release acima recém-criada ou
		// já existente (ex. outro operador ingeriu a mesma release imutável antes) — um
		// replay futuro com esta MESMA chave precisa reconhecer isso de qualquer forma.
		if _, err := q.Exec(c, `INSERT INTO platform_release_ingest_requests (operator_id, idempotency_key, request_hash, release_id) VALUES ($1,$2,$3,$4)`,
			operator, idempotencyKey, hash, m.ReleaseID); err != nil {
			return err
		}
		return q.QueryRow(c, `SELECT release_id, channel, platform_version, manifest_json, ingested_by, ingested_at FROM platform_releases WHERE release_id = $1`, m.ReleaseID).
			Scan(&out.ReleaseID, &out.Channel, &out.PlatformVersion, &out.Manifest, &out.IngestedBy, &out.IngestedAt)
	})
	if err != nil {
		return Release{}, false, err
	}
	return out, replayed, nil
}

// ListReleases retorna o catálogo local, mais recentes primeiro.
func (s *Service) ListReleases(ctx context.Context, operator uuid.UUID, limit int, before *time.Time) ([]Release, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var out []Release
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		var rows pgx.Rows
		var err error
		if before != nil {
			rows, err = q.Query(c, `SELECT release_id, channel, platform_version, manifest_json, ingested_by, ingested_at FROM platform_releases
				WHERE ingested_at < $1 ORDER BY ingested_at DESC LIMIT $2`, *before, limit)
		} else {
			rows, err = q.Query(c, `SELECT release_id, channel, platform_version, manifest_json, ingested_by, ingested_at FROM platform_releases
				ORDER BY ingested_at DESC LIMIT $1`, limit)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Release
			if err := rows.Scan(&r.ReleaseID, &r.Channel, &r.PlatformVersion, &r.Manifest, &r.IngestedBy, &r.IngestedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Release{}
	}
	return out, err
}

// RetentionCandidate (F9, spec "Retenção, garbage collection segura" — release/policies/
// channels.yaml) descreve uma release do catálogo em relação à sua janela de retenção. Nunca
// decide por si só: só relata — o mesmo espírito de "declaração lida por humanos" que
// backup.yaml já documentava antes do gate real de F9 existir. DeleteReleaseCatalogEntry
// REVALIDA tudo isto no momento da exclusão, nunca confia numa lista antiga.
type RetentionCandidate struct {
	ReleaseID       string    `json:"release_id"`
	Channel         string    `json:"channel"`
	IngestedAt      time.Time `json:"ingested_at"`
	AgeDays         int       `json:"age_days"`
	RetentionDays   int       `json:"retention_days"`
	PastRetention   bool      `json:"past_retention"`
	SafeToDelete    bool      `json:"safe_to_delete"`
	BlockingReasons []string  `json:"blocking_reasons,omitempty"`
}

// ListRetentionCandidates (F9) relata, para cada release do catálogo, se já passou da janela
// de retenção do seu canal (channelRetentionDays) e se é seguro removê-la — nunca remove nada
// por si só. "Seguro" aqui replica a garantia que o próprio schema do Postgres já aplica via
// foreign key (platform_release_ingest_requests/platform_update_plans/platform_update_runs/
// platform_release_revocations todas referenciam platform_releases SEM CASCADE) — esta função
// só torna essa restrição LEGÍVEL antes da tentativa, nunca a substitui.
func (s *Service) ListRetentionCandidates(ctx context.Context, operator uuid.UUID) ([]RetentionCandidate, error) {
	var out []RetentionCandidate
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		rows, err := q.Query(c, `SELECT release_id, channel, ingested_at FROM platform_releases ORDER BY ingested_at ASC`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rc RetentionCandidate
			var channel string
			if err := rows.Scan(&rc.ReleaseID, &channel, &rc.IngestedAt); err != nil {
				return err
			}
			rc.Channel = channel
			rc.AgeDays = int(time.Since(rc.IngestedAt).Hours() / 24)
			rc.RetentionDays = channelRetentionDays[domain.Channel(channel)]
			rc.PastRetention = rc.AgeDays >= rc.RetentionDays
			out = append(out, rc)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range out {
			safe, reasons, rerr := releaseSafeToDelete(c, q, out[i].ReleaseID, out[i].PastRetention)
			if rerr != nil {
				return rerr
			}
			out[i].SafeToDelete = safe
			out[i].BlockingReasons = reasons
		}
		return nil
	})
	if out == nil {
		out = []RetentionCandidate{}
	}
	return out, err
}

// releaseSafeToDelete (F9) é a ÚNICA fonte da decisão "é seguro excluir releaseID do
// catálogo" — usada tanto pelo relatório (ListRetentionCandidates) quanto pela exclusão real
// (DeleteReleaseCatalogEntry), nunca duplicada entre as duas. Checa, EXPLICITAMENTE e por
// nome, as mesmas quatro restrições que o schema do Postgres já aplicaria via foreign key sem
// CASCADE (ingest_requests, plans, runs, revocação) — nunca para pular essas checagens, mas
// para dar um motivo LEGÍVEL em vez de um erro genérico de constraint quando a exclusão real
// for tentada, e para o relatório conseguir explicar "por quê não" antes de qualquer tentativa.
func releaseSafeToDelete(ctx context.Context, q platformdb.Querier, releaseID string, pastRetention bool) (safe bool, reasons []string, err error) {
	if !pastRetention {
		reasons = append(reasons, "ainda dentro da janela de retenção do canal (release/policies/channels.yaml)")
	}
	// Achado Codex [P1] (rodada 1 desta fatia): platform_installations.current_release_id
	// NUNCA tem foreign key para platform_releases (de propósito — ver o comentário da
	// coluna) porque SetInstallationVersion/updatectl podem registrar a versão instalada
	// SEM nenhum plano/run correspondente no Postgres (ex. uma recuperação manual R3/R4, ou
	// o bootstrap inicial de uma instalação). Sem esta checagem EXPLÍCITA, uma release assim
	// passava pelas três checagens abaixo (revogação/run/plano, todas zero) e era REMOVIDA
	// do catálogo mesmo sendo a release REALMENTE instalada agora — deixando
	// current_release_id apontando para nada E, pior, tornando essa release IMPOSSÍVEL de
	// revogar depois (RevokeRelease exige a linha do catálogo existir) se um CVE for
	// descoberto para o que está genuinamente rodando.
	var currentReleaseID *string
	if err := q.QueryRow(ctx, `SELECT current_release_id FROM platform_installations`).Scan(&currentReleaseID); err != nil {
		return false, nil, err
	}
	if currentReleaseID != nil && *currentReleaseID == releaseID {
		reasons = append(reasons, "release atualmente instalada (platform_installations.current_release_id) — nunca removida do catálogo, mesmo sem run/plano correspondente")
	}
	var n int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM platform_release_revocations WHERE release_id = $1`, releaseID).Scan(&n); err != nil {
		return false, nil, err
	}
	if n > 0 {
		reasons = append(reasons, "release revogada — o histórico de revogação é permanente, nunca removido (ADR-0054 R4)")
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM platform_update_runs WHERE release_id = $1`, releaseID).Scan(&n); err != nil {
		return false, nil, err
	}
	if n > 0 {
		reasons = append(reasons, "existe pelo menos um run (histórico de execução real, nunca removido) referenciando esta release")
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM platform_update_plans WHERE release_id = $1`, releaseID).Scan(&n); err != nil {
		return false, nil, err
	}
	if n > 0 {
		reasons = append(reasons, "existe pelo menos um plano de preflight referenciando esta release")
	}
	return len(reasons) == 0, reasons, nil
}

// DeleteReleaseCatalogEntry (F9) remove releaseID do catálogo — só quando releaseSafeToDelete
// confirma, NO MOMENTO da chamada (nunca a partir de um relatório antigo de
// ListRetentionCandidates), que a release passou da retenção do seu canal e não tem nenhuma
// revogação/plano/run referenciando-a. platform_release_ingest_requests é removida JUNTO (a
// mesma transação) — é só bookkeeping operacional de idempotência de ingestão, nunca um
// registro de auditoria que precise sobreviver à própria release que protegia; TODO o resto
// (revogação, planos, runs) nunca é tocado aqui, e sua simples EXISTÊNCIA já bloqueia a
// exclusão (foreign key sem CASCADE, reforçado explicitamente por releaseSafeToDelete acima).
// Idempotente (ADR-0054 §2.2.1): repetir com a MESMA Idempotency-Key depois de já ter
// excluído devolve deleted=true sem erro, nunca tenta excluir de novo algo que já não existe.
func (s *Service) DeleteReleaseCatalogEntry(ctx context.Context, operator uuid.UUID, idempotencyKey, releaseID string) (deleted bool, err error) {
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return false, invalid("Idempotency-Key deve ter 8-128 caracteres")
	}
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" {
		return false, invalid("release_id é obrigatório")
	}
	hash := requestHashGC(operator, releaseID)
	err = s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, operator.String()+"/"+idempotencyKey); err != nil {
			return err
		}
		// Achado Codex [P1]: o MESMO lock que SetInstallationVersion/refreshPendingRun usam
		// para serializar escritas em platform_installations — sem isto, uma chamada
		// concorrente podia marcar ESTA release como current_release_id depois da checagem
		// de releaseSafeToDelete abaixo, mas antes do DELETE comitar, deixando a instalação
		// apontar para uma release que já não existe mais no catálogo.
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "platform-installation-runs:"+s.installationID); err != nil {
			return err
		}
		var prevHash, prevReleaseID string
		perr := q.QueryRow(c, `SELECT request_hash, release_id FROM platform_release_gc_requests WHERE operator_id = $1 AND idempotency_key = $2`, operator, idempotencyKey).
			Scan(&prevHash, &prevReleaseID)
		switch {
		case perr == nil:
			if prevHash != hash {
				return ErrKeyMismatch
			}
			deleted = true
			return nil
		case !errors.Is(perr, pgx.ErrNoRows):
			return perr
		}
		var ingestedAt time.Time
		var channel string
		if gerr := q.QueryRow(c, `SELECT channel, ingested_at FROM platform_releases WHERE release_id = $1`, releaseID).Scan(&channel, &ingestedAt); gerr != nil {
			if errors.Is(gerr, pgx.ErrNoRows) {
				return invalid("release_id %q não existe no catálogo", releaseID)
			}
			return gerr
		}
		pastRetention := int(time.Since(ingestedAt).Hours()/24) >= channelRetentionDays[domain.Channel(channel)]
		safe, reasons, serr := releaseSafeToDelete(c, q, releaseID, pastRetention)
		if serr != nil {
			return serr
		}
		if !safe {
			return invalid("release %q não pode ser removida do catálogo agora: %s", releaseID, strings.Join(reasons, "; "))
		}
		if _, err := q.Exec(c, `DELETE FROM platform_release_ingest_requests WHERE release_id = $1`, releaseID); err != nil {
			return err
		}
		if _, err := q.Exec(c, `DELETE FROM platform_releases WHERE release_id = $1`, releaseID); err != nil {
			return err
		}
		if _, err := q.Exec(c, `INSERT INTO platform_release_gc_requests (operator_id, idempotency_key, request_hash, release_id) VALUES ($1,$2,$3,$4)`,
			operator, idempotencyKey, hash, releaseID); err != nil {
			return err
		}
		deleted = true
		return audit(c, q, operator, "platform.release.gc_deleted", map[string]any{"release_id": releaseID, "channel": channel})
	})
	return deleted, err
}

// --- Installation (singleton — ADR-0054 §2.4: "hoje: uma só") ---

type Installation struct {
	Channel          string          `json:"channel"`
	AgentProtocol    int             `json:"agent_protocol"`
	CurrentVersion   string          `json:"current_version,omitempty"`
	CurrentReleaseID string          `json:"current_release_id,omitempty"`
	CurrentDigests   json.RawMessage `json:"current_digests"`
	UpdatedAt        time.Time       `json:"updated_at"`
}

func (s *Service) GetInstallation(ctx context.Context, operator uuid.UUID) (Installation, error) {
	// Achado Codex [P2]: sem isto, um cliente que só chama GetInstallation (nunca CreatePlan/
	// ListRuns/StartRun, que já sincronizam) via a versão antiga para sempre, mesmo depois de
	// um update real ter concluído — inclusive um iniciado por `updatectl`.
	if err := s.refreshPendingRun(ctx, operator); err != nil {
		return Installation{}, err
	}
	var out Installation
	var version, releaseID *string
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		return q.QueryRow(c, `SELECT channel, agent_protocol, current_version, current_release_id, current_digests, updated_at FROM platform_installations`).
			Scan(&out.Channel, &out.AgentProtocol, &version, &releaseID, &out.CurrentDigests, &out.UpdatedAt)
	})
	if version != nil {
		out.CurrentVersion = *version
	}
	if releaseID != nil {
		out.CurrentReleaseID = *releaseID
	}
	return out, err
}

// MonitoringStatus (F9, spec "F9 — ... hardening e revisão independente", item "Monitoramento:
// status do agente, falhas de backup, versão real/digest divergente, update estagnado, erros
// após release") agrega os cinco sinais num único read-only, pensado para um operador ou uma
// sonda externa consultar periodicamente. Nenhum campo aqui aciona nada por si só (sem
// alerta/e-mail/fila — CLAUDE.md proíbe segredo em fila e este pacote nunca ganhou essa
// dependência) — é só a VERDADE observável, exposta para quem for decidir o que fazer com ela.
type MonitoringStatus struct {
	// Agente: heartbeat gravado a cada ciclo de poll do daemon (RecordAgentHeartbeat),
	// independente de agent_lock (que só existe enquanto um run está ativo).
	AgentHeartbeatSeen bool       `json:"agent_heartbeat_seen"`
	AgentLastHeartbeat *time.Time `json:"agent_last_heartbeat,omitempty"`
	AgentHolder        string     `json:"agent_holder,omitempty"`
	AgentStale         bool       `json:"agent_stale"`

	// Backup: a MESMA evidência real de platform_backup_records que CreatePlan já usa (nunca
	// uma fonte nova/reinventada) — qualquer kind, visão de monitoramento (nunca o gate
	// restrito a restore_verified do canal stable).
	BackupEvidencePresent  bool     `json:"backup_evidence_present"`
	BackupEvidenceAgeHours *float64 `json:"backup_evidence_age_hours,omitempty"`
	BackupEvidenceVerified bool     `json:"backup_evidence_verified"`
	BackupEvidenceStale    bool     `json:"backup_evidence_stale"`

	// Versão/digest divergente: o que o ledger LOCAL do agente diz ter concluído com sucesso
	// por último, contra o que platform_installations (Postgres) pensa que está instalado —
	// depois de uma tentativa de sincronização real (refreshPendingRun), nunca antes.
	LedgerVersion         string `json:"ledger_version,omitempty"`
	LedgerReleaseID       string `json:"ledger_release_id,omitempty"`
	InstallationVersion   string `json:"installation_version,omitempty"`
	InstallationReleaseID string `json:"installation_release_id,omitempty"`
	VersionDivergence     bool   `json:"version_divergence"`

	// Achado Codex [P2] (rodada 3): o digest REAL observado (RecordObservedDigests) tem sua
	// PRÓPRIA cadência — se o Docker fica indisponível depois de uma observação bem-sucedida,
	// o heartbeat de liveness continua avançando (ticker independente, achado rodada 1), mas
	// observed_digests/observed_at ficam CONGELADOS na última observação boa, para sempre.
	// VersionDivergence acima NUNCA é suprimida por isto (uma divergência já detectada nunca
	// é escondida só porque a observação envelheceu) — mas um VersionDivergence=false
	// baseado em dados velhos não pode ser lido como "confirmado agora" sem este sinal.
	ObservedDigestsAt    *time.Time `json:"observed_digests_at,omitempty"`
	ObservedDigestsStale bool       `json:"observed_digests_stale"`

	// Update estagnado: um run ATIVO (não-terminal) sem nenhuma transição de estado há mais
	// tempo que stalledRunThreshold.
	StalledRunID    string     `json:"stalled_run_id,omitempty"`
	StalledRunState string     `json:"stalled_run_state,omitempty"`
	StalledSince    *time.Time `json:"stalled_since,omitempty"`

	// Erros após release: a MESMA checagem que já bloqueia CreatePlan/StartRun
	// (ledgerHasUnresolvedRecovery) — um RECOVERY_REQUIRED nunca reconhecido é, por
	// definição, um erro real acontecido depois de uma release ter sido aplicada.
	UnresolvedRecoveryRequired bool `json:"unresolved_recovery_required"`
}

// GetMonitoringStatus agrega os cinco sinais de monitoramento do F9 — ver MonitoringStatus.
// Read-only: nunca muta nada, só observa o ledger local e o Postgres. authorize() é o MESMO
// gate de qualquer outra leitura desta API (platform operator ativo).
func (s *Service) GetMonitoringStatus(ctx context.Context, operator uuid.UUID) (MonitoringStatus, error) {
	// Mesma razão de GetInstallation (achado Codex [P2] original): sem isto, a comparação de
	// versão/digest abaixo compararia contra um valor potencialmente obsoleto no Postgres.
	if err := s.refreshPendingRun(ctx, operator); err != nil {
		return MonitoringStatus{}, err
	}

	var out MonitoringStatus
	var instVersion, instReleaseID *string
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		if err := q.QueryRow(c, `SELECT current_version, current_release_id FROM platform_installations`).
			Scan(&instVersion, &instReleaseID); err != nil {
			return err
		}
		var lastEvidence *time.Time
		var verified bool
		_ = q.QueryRow(c, `SELECT taken_at, verified FROM platform_backup_records ORDER BY taken_at DESC LIMIT 1`).Scan(&lastEvidence, &verified)
		if lastEvidence != nil {
			out.BackupEvidencePresent = true
			ageHours := time.Since(*lastEvidence).Hours()
			out.BackupEvidenceAgeHours = &ageHours
			out.BackupEvidenceVerified = verified
			out.BackupEvidenceStale = ageHours > backupEvidenceMaxAgeHours || !verified
		} else {
			out.BackupEvidenceStale = true
		}
		return nil
	})
	if err != nil {
		return MonitoringStatus{}, err
	}
	if instVersion != nil {
		out.InstallationVersion = *instVersion
	}
	if instReleaseID != nil {
		out.InstallationReleaseID = *instReleaseID
	}

	ledger, lerr := agent.OpenLedger(s.ledgerPath)
	if lerr != nil {
		// Ambiente sem ledger (ex. nenhum run foi criado ainda, ou este processo nunca
		// escreveu nele) — melhor esforço, nunca falha a chamada inteira por isto: os sinais
		// derivados do Postgres acima continuam válidos e são devolvidos normalmente.
		return out, nil
	}
	defer ledger.Close()

	var heartbeat agent.AgentHeartbeatInfo
	if info, found, herr := ledger.AgentHeartbeat(ctx, s.installationID); herr == nil && found {
		heartbeat = info
		out.AgentHeartbeatSeen = true
		out.AgentHolder = info.Holder
		lp := info.LastPollAt
		out.AgentLastHeartbeat = &lp
		out.AgentStale = time.Since(info.LastPollAt) > agentHeartbeatStaleAfter
	}

	if version, releaseID, _, _, found, verr := latestSucceededVersion(ctx, ledger, s.installationID); verr == nil && found {
		out.LedgerVersion = version
		out.LedgerReleaseID = releaseID
		out.VersionDivergence = out.InstallationReleaseID != "" && releaseID != out.InstallationReleaseID
	}

	// Achado Codex [P2] (rodada 1 desta fatia): nem o ledger (histórico de runs) nem o
	// Postgres observam containers de verdade — um container substituído manualmente, ou uma
	// implantação que falhou deixando outra imagem rodando, nunca apareceria na comparação
	// acima. Compara o que o daemon de fato observou no Docker (RecordObservedDigests) contra
	// o esperado POR UNIT através de TODO o histórico (achado Codex [P2] rodada 2: só olhar a
	// release SUCCEEDED mais recente no geral perde drift em units tocadas por uma release
	// MAIS ANTIGA quando a mais recente só atualizou outras — mesmo problema de
	// rollbackTargetReleaseIDs, resolvido aqui com a mesma ideia de proveniência por unit).
	if !heartbeat.ObservedAt.IsZero() {
		oa := heartbeat.ObservedAt
		out.ObservedDigestsAt = &oa
		out.ObservedDigestsStale = time.Since(heartbeat.ObservedAt) > observedDigestsStaleAfter
	}
	if heartbeat.ObservedDigests != "" && heartbeat.ObservedDigests != "{}" {
		var observed map[string]string
		if jerr := json.Unmarshal([]byte(heartbeat.ObservedDigests), &observed); jerr == nil {
			if expected, eerr := expectedDigestsAcrossHistory(ctx, ledger, s.installationID); eerr == nil {
				for unit, want := range expected {
					got, ok := observed[unit]
					if !ok {
						// Achado Codex [P2] rodada 2: ausente de verdade do mapa observado
						// (nunca visto por CurrentSnapshot, ex. uma unit fora do escopo deste
						// daemon) é "ainda não é possível confirmar" — nunca divergência.
						continue
					}
					// got == "" é uma OBSERVAÇÃO REAL e bem-sucedida de "nenhum container
					// rodando para esta unit" (CurrentSnapshot resolveu o container e não
					// achou nada, ou achou algo sem digest utilizável) — distinto de "nunca
					// observado". Quando uma release SUCCEEDED declara um digest esperado
					// para essa unit, um got="" prova que o container esperado está
					// AUSENTE/substituído por algo não-imutável — divergência real, nunca
					// silenciosamente ignorada.
					if got != want {
						out.VersionDivergence = true
						break
					}
				}
			}
		}
	}

	if active, aerr := ledger.ActiveRuns(ctx, s.installationID); aerr == nil {
		for i := range active {
			r := &active[i]
			if time.Since(r.UpdatedAt) > stalledRunThreshold {
				out.StalledRunID = r.RunID
				out.StalledRunState = string(r.State)
				since := r.UpdatedAt
				out.StalledSince = &since
				break
			}
		}
	}

	if pending, perr := ledger.HasUnresolvedRecoveryRequired(ctx, s.installationID); perr == nil {
		out.UnresolvedRecoveryRequired = pending
	}

	return out, nil
}

// SetInstallationVersion registra explicitamente a versão/release ATUALMENTE instalada. Nada
// nesta fase (F6) popula isso a partir de um deploy real — o agente (F5) não reporta de volta
// à API (ver o comentário do pacote) — então, sem este método, checkCompatibility bloquearia
// TODO plano para sempre (fail-closed correto, mas inútil sem uma saída). Uso esperado: o
// operador registra o bootstrap uma vez; qualquer passo futuro que ligue isto a um relato real
// do agente (F8+) deve chamar o MESMO método, nunca escrever a coluna direto.
func requestHashInstallationVersion(operator uuid.UUID, version, releaseID string) string {
	sum := sha256.Sum256([]byte(operator.String() + "\x00" + version + "\x00" + releaseID))
	return hex.EncodeToString(sum[:])
}

// SetInstallationVersion é uma mutação (ADR-0054 §2.2.1 exige Idempotency-Key em toda mutação
// de plataforma) — achado Codex [P2]: sem idempotência, um retry (resposta perdida) DEPOIS de
// um update real ter concluído reescrevia a versão ANTIGA e avançava updated_at de novo,
// fazendo a sincronização (applyLatestSucceededRunToInstallation/writeLedgerRun, achado Codex
// anterior) tratar esse registro obsoleto como "mais recente" que o run SUCCEEDED genuíno que
// aconteceu nesse meio tempo — um retry nunca pode reescrever o que já foi escrito.
func (s *Service) SetInstallationVersion(ctx context.Context, operator uuid.UUID, idempotencyKey, version, releaseID string) (Installation, error) {
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return Installation{}, invalid("Idempotency-Key deve ter 8-128 caracteres")
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return Installation{}, invalid("version é obrigatório")
	}
	hash := requestHashInstallationVersion(operator, version, releaseID)
	var out Installation
	var v, r *string
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		// Achado Codex [P2]: sem este lock (o MESMO usado por StartRun/refreshPendingRun/
		// writeLedgerRun), uma sincronização automática do ledger podia ler
		// platform_installations ANTES desta UPDATE e escrever DEPOIS dela — sobrescrevendo
		// a versão manualmente registrada (mais recente, uma recuperação aprovada) com um
		// fato do ledger mais antigo, já que as duas escritas nunca estavam serializadas
		// entre si.
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "platform-installation-runs:"+s.installationID); err != nil {
			return err
		}
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, operator.String()+"/"+idempotencyKey); err != nil {
			return err
		}
		var prevHash string
		perr := q.QueryRow(c, `SELECT request_hash FROM platform_installation_version_requests WHERE operator_id = $1 AND idempotency_key = $2`, operator, idempotencyKey).Scan(&prevHash)
		switch {
		case perr == nil:
			if prevHash != hash {
				return ErrKeyMismatch
			}
			// Replay: nunca escreve de novo (a mutação real só acontece uma vez, abaixo) —
			// devolve o estado ATUAL da instalação, que é o estado correto mesmo que algo
			// mais recente e legítimo (um sync do ledger) tenha avançado desde a 1ª chamada.
			return q.QueryRow(c, `SELECT channel, agent_protocol, current_version, current_release_id, current_digests, updated_at FROM platform_installations`).
				Scan(&out.Channel, &out.AgentProtocol, &v, &r, &out.CurrentDigests, &out.UpdatedAt)
		case !errors.Is(perr, pgx.ErrNoRows):
			return perr
		}
		var relArg any
		if releaseID != "" {
			relArg = releaseID
		}
		// Achado Codex [P2]: now() no Postgres devolve o início da TRANSAÇÃO, não o instante
		// real desta instrução — se esta transação ficou esperando o lock de instalação
		// (porque um run do agente estava em andamento), now() podia registrar um horário
		// ANTERIOR ao sucesso real desse run, fazendo a recuperação manual parecer mais ANTIGA
		// que o run que ela deveria suplantar. clock_timestamp() sempre lê o relógio real no
		// momento em que É EXECUTADA, nunca o início da transação.
		if _, err := q.Exec(c, `UPDATE platform_installations SET current_version = $1, current_release_id = $2, updated_at = clock_timestamp()`, version, relArg); err != nil {
			return err
		}
		if _, err := q.Exec(c, `INSERT INTO platform_installation_version_requests (operator_id, idempotency_key, request_hash, version, release_id) VALUES ($1,$2,$3,$4,$5)`,
			operator, idempotencyKey, hash, version, relArg); err != nil {
			return err
		}
		if err := audit(c, q, operator, "platform.installation.version_set", map[string]any{"version": version, "release_id": releaseID}); err != nil {
			return err
		}
		return q.QueryRow(c, `SELECT channel, agent_protocol, current_version, current_release_id, current_digests, updated_at FROM platform_installations`).
			Scan(&out.Channel, &out.AgentProtocol, &v, &r, &out.CurrentDigests, &out.UpdatedAt)
	})
	if v != nil {
		out.CurrentVersion = *v
	}
	if r != nil {
		out.CurrentReleaseID = *r
	}
	return out, err
}

// --- Backup evidence (release/policies/backup.yaml — bookkeeping informativo em F6) ---

type BackupRecord struct {
	ID        uuid.UUID       `json:"id"`
	Kind      string          `json:"kind"`
	TakenAt   time.Time       `json:"taken_at"`
	Verified  bool            `json:"verified"`
	SizeBytes *int64          `json:"size_bytes,omitempty"`
	SHA256Hex string          `json:"sha256_hex,omitempty"`
	Detail    json.RawMessage `json:"detail"`
	CreatedBy uuid.UUID       `json:"created_by"`
	CreatedAt time.Time       `json:"created_at"`
}

func requestHashBackup(operator uuid.UUID, kind string, takenAt time.Time, verified bool, sizeBytes *int64, sha256Hex string, detailRaw []byte) string {
	size := "nil"
	if sizeBytes != nil {
		size = strconv.FormatInt(*sizeBytes, 10)
	}
	sum := sha256.Sum256([]byte(operator.String() + "\x00" + kind + "\x00" + takenAt.UTC().Format(time.RFC3339Nano) + "\x00" +
		strconv.FormatBool(verified) + "\x00" + size + "\x00" + sha256Hex + "\x00" + string(detailRaw)))
	return hex.EncodeToString(sum[:])
}

// RecordBackupEvidence é uma mutação (ADR-0054 §2.2.1 exige Idempotency-Key em toda mutação) —
// achado Codex [P2]: faltava até aqui, então uma resposta perdida/timeout fazia um retry do
// operador criar uma SEGUNDA linha de evidência em vez de reaproveitar a primeira. Mesmo padrão
// de IngestRelease/StartRun: lock consultivo por (operator, key), hash do pedido, replay ou 422.
func (s *Service) RecordBackupEvidence(ctx context.Context, operator uuid.UUID, idempotencyKey, kind string, takenAt time.Time, verified bool, sizeBytes *int64, sha256Hex string, detail map[string]any) (rec BackupRecord, replayed bool, err error) {
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return BackupRecord{}, false, invalid("Idempotency-Key deve ter 8-128 caracteres")
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return BackupRecord{}, false, invalid("kind é obrigatório")
	}
	if takenAt.IsZero() || takenAt.After(time.Now().Add(time.Minute)) {
		return BackupRecord{}, false, invalid("taken_at inválido")
	}
	if detail == nil {
		detail = map[string]any{}
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return BackupRecord{}, false, invalid("detail inválido: %v", err)
	}
	hash := requestHashBackup(operator, kind, takenAt, verified, sizeBytes, sha256Hex, raw)
	rec = BackupRecord{ID: uuid.New(), Kind: kind, TakenAt: takenAt.UTC(), Verified: verified, SizeBytes: sizeBytes, SHA256Hex: sha256Hex, CreatedBy: operator}
	err = s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, operator.String()+"/"+idempotencyKey); err != nil {
			return err
		}
		var prevHash string
		var prevID uuid.UUID
		perr := q.QueryRow(c, `SELECT request_hash, created_record_id FROM platform_backup_record_requests WHERE operator_id = $1 AND idempotency_key = $2`, operator, idempotencyKey).
			Scan(&prevHash, &prevID)
		switch {
		case perr == nil:
			if prevHash != hash {
				return ErrKeyMismatch
			}
			rec.ID = prevID
			replayed = true
			return q.QueryRow(c, `SELECT kind, taken_at, verified, size_bytes, sha256_hex, detail, created_by, created_at FROM platform_backup_records WHERE id = $1`, prevID).
				Scan(&rec.Kind, &rec.TakenAt, &rec.Verified, &rec.SizeBytes, &rec.SHA256Hex, &rec.Detail, &rec.CreatedBy, &rec.CreatedAt)
		case !errors.Is(perr, pgx.ErrNoRows):
			return perr
		}
		if _, err := q.Exec(c, `INSERT INTO platform_backup_records (id, kind, taken_at, verified, size_bytes, sha256_hex, detail, created_by) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			rec.ID, rec.Kind, rec.TakenAt, rec.Verified, rec.SizeBytes, rec.SHA256Hex, raw, operator); err != nil {
			return err
		}
		if _, err := q.Exec(c, `INSERT INTO platform_backup_record_requests (operator_id, idempotency_key, request_hash, created_record_id) VALUES ($1,$2,$3,$4)`,
			operator, idempotencyKey, hash, rec.ID); err != nil {
			return err
		}
		return q.QueryRow(c, `SELECT detail, created_at FROM platform_backup_records WHERE id = $1`, rec.ID).Scan(&rec.Detail, &rec.CreatedAt)
	})
	return rec, replayed, err
}

// --- Revocation (F9, release/policies/signing.yaml "revocation") ---

type Revocation struct {
	ReleaseID string    `json:"release_id"`
	RevokedAt time.Time `json:"revoked_at"`
	Reason    string    `json:"reason"`
	RevokedBy uuid.UUID `json:"revoked_by"`
}

func requestHashRevoke(operator uuid.UUID, releaseID, reason string) string {
	sum := sha256.Sum256([]byte(operator.String() + "\x00" + releaseID + "\x00" + reason))
	return hex.EncodeToString(sum[:])
}

func requestHashGC(operator uuid.UUID, releaseID string) string {
	sum := sha256.Sum256([]byte(operator.String() + "\x00" + releaseID))
	return hex.EncodeToString(sum[:])
}

// RevokeRelease (F9) é uma mutação (ADR-0054 §2.2.1 exige Idempotency-Key em toda mutação) —
// mesmo padrão de RecordBackupEvidence. platform_release_revocations é append-only de verdade
// (migration 000174: REVOKE UPDATE/DELETE + trigger) — uma segunda chamada com a MESMA
// Idempotency-Key reaproveita a revogação já registrada (replay normal); uma chamada para o
// MESMO release_id com uma Idempotency-Key DIFERENTE falha na constraint de chave primária da
// tabela (um release_id só pode ser revogado uma vez — revogar de novo com um motivo diferente
// não é uma operação suportada por este método; exigiria uma decisão de operador documentada
// fora desta tabela, como o comentário da migration já explica).
func (s *Service) RevokeRelease(ctx context.Context, operator uuid.UUID, idempotencyKey, releaseID, reason string) (rec Revocation, replayed bool, err error) {
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return Revocation{}, false, invalid("Idempotency-Key deve ter 8-128 caracteres")
	}
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" {
		return Revocation{}, false, invalid("release_id é obrigatório")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Revocation{}, false, invalid("reason é obrigatório")
	}
	hash := requestHashRevoke(operator, releaseID, reason)
	err = s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, operator.String()+"/"+idempotencyKey); err != nil {
			return err
		}
		var prevHash string
		perr := q.QueryRow(c, `SELECT request_hash FROM platform_release_revocation_requests WHERE operator_id = $1 AND idempotency_key = $2`, operator, idempotencyKey).Scan(&prevHash)
		switch {
		case perr == nil:
			if prevHash != hash {
				return ErrKeyMismatch
			}
			replayed = true
			return q.QueryRow(c, `SELECT release_id, revoked_at, reason, revoked_by FROM platform_release_revocations WHERE release_id = $1`, releaseID).
				Scan(&rec.ReleaseID, &rec.RevokedAt, &rec.Reason, &rec.RevokedBy)
		case !errors.Is(perr, pgx.ErrNoRows):
			return perr
		}
		if _, err := q.Exec(c, `SELECT 1 FROM platform_releases WHERE release_id = $1`, releaseID); err != nil {
			return err
		}
		if _, err := q.Exec(c, `INSERT INTO platform_release_revocations (release_id, reason, revoked_by) VALUES ($1,$2,$3)`, releaseID, reason, operator); err != nil {
			return err
		}
		// Achado Codex [P1]: registra a OBRIGAÇÃO de entrega ao ledger local na MESMA
		// transação da revogação em si — nunca uma segunda escrita "melhor esforço" sem
		// rastro, que um crash entre as duas deixaria permanentemente perdida. A entrega
		// real (abaixo, fora desta transação) e o retry (reconcileRevocationsToLedger,
		// chamado por refreshPendingRun) são o que FECHAM esta obrigação.
		if _, err := q.Exec(c, `INSERT INTO platform_release_revocation_delivery (release_id) VALUES ($1)`, releaseID); err != nil {
			return err
		}
		if _, err := q.Exec(c, `INSERT INTO platform_release_revocation_requests (operator_id, idempotency_key, request_hash, release_id) VALUES ($1,$2,$3,$4)`,
			operator, idempotencyKey, hash, releaseID); err != nil {
			return err
		}
		if err := q.QueryRow(c, `SELECT release_id, revoked_at, reason, revoked_by FROM platform_release_revocations WHERE release_id = $1`, releaseID).
			Scan(&rec.ReleaseID, &rec.RevokedAt, &rec.Reason, &rec.RevokedBy); err != nil {
			return err
		}
		return audit(c, q, operator, "platform.release.revoked", map[string]any{"release_id": releaseID, "reason": reason})
	})
	if err != nil {
		return rec, replayed, err
	}
	// Tenta a entrega imediatamente (caminho feliz: sem nenhuma falha, o operador nunca
	// percebe que existiu uma "obrigação" separada) — mas o que FECHA a garantia de verdade
	// é reconcileRevocationsToLedger, chamado por refreshPendingRun em quase toda operação
	// real desta API, nunca só esta tentativa síncrona. Achado Codex (ao testar): a
	// atualização de delivered_to_agent precisa rodar numa sessão AUTORIZADA (s.tx, mesmo
	// padrão de toda mutação deste serviço) — uma escrita direta no pool, fora de
	// WithTenantSession, cai sob RLS sem is_system_admin()/is_platform_operator() válidos e
	// afeta ZERO linhas silenciosamente (nunca um erro), deixando delivered_to_agent=false
	// para sempre mesmo com o ledger já tendo recebido a revogação de verdade.
	lerr := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		return deliverRevocationToLedger(c, s.ledgerPath, q, releaseID, reason)
	})
	if lerr != nil {
		return rec, replayed, fmt.Errorf("updatemgr: release %s revogada no Postgres (obrigação de entrega registrada, será reentregue automaticamente nas próximas operações desta API), mas a entrega imediata ao ledger local falhou: %w", releaseID, lerr)
	}
	return rec, replayed, nil
}

// deliverRevocationToLedger (F9) é a ÚNICA função que escreve no ledger local E marca
// delivered_to_agent=true no Postgres — usada tanto pela tentativa síncrona de RevokeRelease
// quanto pelo retry em reconcileRevocationsToLedger, nunca duplicada entre as duas. `q` precisa
// vir de uma sessão JÁ autorizada (s.tx) — ver achado Codex no comentário de RevokeRelease. As
// duas escritas (ledger, depois Postgres) nunca são atômicas entre si — mas isso é seguro
// aqui: se o ledger grava e o Postgres falha em marcar delivered_to_agent, o PIOR caso é
// reentregar a MESMA revogação de novo depois (Ledger.RevokeRelease é idempotente, UPSERT),
// nunca perder a entrega real que já aconteceu.
func deliverRevocationToLedger(ctx context.Context, ledgerPath string, q platformdb.Querier, releaseID, reason string) error {
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		return fmt.Errorf("abrir ledger local: %w", err)
	}
	defer ledger.Close()
	if err := ledger.RevokeRelease(ctx, releaseID, reason); err != nil {
		return fmt.Errorf("gravar no ledger local: %w", err)
	}
	if _, err := q.Exec(ctx, `UPDATE platform_release_revocation_delivery SET delivered_to_agent = true, delivered_at = now() WHERE release_id = $1`, releaseID); err != nil {
		return fmt.Errorf("marcar delivered_to_agent no Postgres (ledger JÁ recebeu a revogação, nunca perdida — só esta marcação de progresso falhou): %w", err)
	}
	return nil
}

// reconcileRevocationsToLedger (F9) é a garantia REAL de entrega confiável: entrega TODA
// release revogada conhecida pelo Postgres, nunca só as marcadas delivered_to_agent=false —
// achado Codex [P1] (rodada 8): um ledger SQLite restaurado de um backup ANTERIOR a uma
// revogação já confirmada entregue (delivered_to_agent=true permanente) perderia essa
// revogação PARA SEMPRE, porque uma consulta filtrada por "pendente" nunca a encontraria de
// novo — o relógio de sincronização seguiria avançando, certificando como "em dia" um espelho
// que na verdade está incompleto. Reentregar o conjunto COMPLETO a cada chamada é seguro e
// barato (Ledger.RevokeRelease é um UPSERT idempotente; revogação é um evento raro — nunca um
// volume alto) e elimina essa classe de problema por completo: não importa o que o ledger
// local perdeu ou nunca teve, a PRÓXIMA reconciliação sempre o restaura a partir da fonte de
// verdade. Chamada por refreshPendingRun, que já roda em quase toda operação real desta API
// (CreatePlan, StartRun, GetInstallation, ListRuns), e por StartRevocationDeliveryLoop. `q` é
// o MESMO Querier, JÁ autorizado, da transação do chamador — nunca uma sessão nova. "Nenhuma
// revogação" e "ledger indisponível neste ambiente" nunca são erros fatais aqui — a próxima
// chamada tenta de novo.
func reconcileRevocationsToLedger(ctx context.Context, q platformdb.Querier, ledgerPath string) error {
	rows, err := q.Query(ctx, `SELECT release_id, reason FROM platform_release_revocations`)
	if err != nil {
		return fmt.Errorf("listar revogações: %w", err)
	}
	type revocation struct{ releaseID, reason string }
	var items []revocation
	for rows.Next() {
		var r revocation
		if err := rows.Scan(&r.releaseID, &r.reason); err != nil {
			rows.Close()
			return fmt.Errorf("ler revogação: %w", err)
		}
		items = append(items, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("listar revogações: %w", err)
	}
	allDelivered := true
	for _, r := range items {
		if err := deliverRevocationToLedger(ctx, ledgerPath, q, r.releaseID, r.reason); err != nil {
			// melhor esforço aqui: a PRÓXIMA chamada de refreshPendingRun tenta de novo — mas
			// loga, porque uma entrega pendente de revogação é um evento de segurança real,
			// nunca silenciado.
			log.Printf("updatemgr: reconciliação da revogação de %s com o ledger local falhou (nova tentativa na próxima operação): %v", r.releaseID, err)
			allDelivered = false
		}
	}
	// Achado Codex [P1], F9 (rodada 7): só avança o relógio de revocation_sync (consultado
	// pelo agente via Ledger.RevocationSyncFresh antes de confiar em "não revogada") quando
	// esta chamada confirmou, de verdade, que o estado COMPLETO do Postgres foi refletido
	// neste ledger — o SELECT acima já teve sucesso (ou já teríamos retornado um erro antes
	// de chegar aqui) e toda revogação conhecida foi entregue. Se alguma entrega falhou,
	// NUNCA marca synced — o relógio deve continuar envelhecendo até uma tentativa futura ter
	// sucesso de verdade, nunca fingir que está em dia.
	if allDelivered {
		ledger, lerr := agent.OpenLedger(ledgerPath)
		if lerr != nil {
			log.Printf("updatemgr: marcar sincronização de revogação: abrir ledger local: %v", lerr)
			return nil
		}
		defer ledger.Close()
		if err := ledger.MarkRevocationSynced(ctx); err != nil {
			log.Printf("updatemgr: marcar sincronização de revogação: %v", err)
		}
	}
	return nil
}

// --- Plan (preflight — especificação §7.1) ---

type Plan struct {
	ID              uuid.UUID       `json:"id"`
	ReleaseID       string          `json:"release_id"`
	PreflightStatus string          `json:"preflight_status"` // PASS | BLOCK
	PreflightDetail json.RawMessage `json:"preflight_detail"`
	CreatedBy       uuid.UUID       `json:"created_by"`
	CreatedAt       time.Time       `json:"created_at"`
	ExpiresAt       time.Time       `json:"expires_at"`
}

// planExpiry: um plano "PASS" só é válido por esta janela — um StartRun tardio precisa revalidar
// o preflight (a instalação pode ter mudado de versão nesse meio tempo). Especificação §7.1 pede
// "preflight fail-closed", não um resultado cacheado indefinidamente.
const planExpiry = 15 * time.Minute

// checkCompatibility aplica release/policies/compatibility.yaml contra o estado REAL da
// instalação. Achado Codex [P1]: uma versão instalada DESCONHECIDA (current_version NULL — o
// estado de toda instalação recém-migrada, já que nada neste programa ainda popula essa coluna
// a partir de um deploy real) era tratada como "compatível com qualquer release", o oposto de
// fail-closed. Agora uma versão desconhecida SEMPRE bloqueia — o operador precisa registrá-la
// explicitamente (SetInstallationVersion) antes de qualquer plano poder passar.
func checkCompatibility(m *domain.ReleaseManifest, installVersion *string, agentProtocol int) (status string, detail map[string]any) {
	reasons := []string{}
	versionKnown := installVersion != nil && *installVersion != ""
	versionCompatible := versionKnown && slices.Contains(m.Compatibility.FromVersions, *installVersion)
	switch {
	case !versionKnown:
		reasons = append(reasons, "a versão instalada desta plataforma não foi registrada ainda (platform_installations.current_version é NULL) — registre-a (SetInstallationVersion) antes de qualquer plano poder passar")
	case !versionCompatible:
		reasons = append(reasons, fmt.Sprintf("compatibility.from_versions não inclui a versão instalada (%s)", *installVersion))
	}
	agentProtocolOK := agentProtocol >= m.Compatibility.MinAgentProtocol
	if !agentProtocolOK {
		reasons = append(reasons, fmt.Sprintf("esta instalação está no protocolo de agente %d, a release exige no mínimo %d", agentProtocol, m.Compatibility.MinAgentProtocol))
	}
	status = "PASS"
	if !versionCompatible || !agentProtocolOK {
		status = "BLOCK"
	}
	return status, map[string]any{
		"version_known":      versionKnown,
		"version_compatible": versionCompatible,
		"agent_protocol_ok":  agentProtocolOK,
		"reasons":            reasons,
	}
}

// checkRevocation (F9, release/policies/signing.yaml "revocation"/"anti_replay") consulta
// platform_release_revocations SEMPRE por uma query real ao Postgres — nunca um cache de
// processo, nunca confiada do manifesto (revogação é decidida pelo catálogo, não pela release
// que está sendo avaliada: uma release revogada nunca "sabe" disso em seu próprio JSON). Fail
// closed: um erro de consulta é tratado como "não sei, recusar" pelo chamador (devolvido como
// erro, nunca interpretado como "não revogada"). Chamada em TODO ponto que decide
// preflight/despacho (CreatePlan, StartRun replay E StartRun novo) — nunca só na ingestão —
// porque signing.yaml exige revalidar ANTES do apply, não só do download
// (anti_replay.revalidate_before_apply): um manifesto baixado/planejado antes de uma revogação
// nunca pode ser aplicado depois dela só porque já estava em disco/plano local.
func checkRevocation(ctx context.Context, q platformdb.Querier, releaseID string) (revoked bool, reason string, err error) {
	var revokedAt time.Time
	err = q.QueryRow(ctx, `SELECT revoked_at, reason FROM platform_release_revocations WHERE release_id = $1`, releaseID).Scan(&revokedAt, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("updatemgr: verificar revogação de %s: %w", releaseID, err)
	}
	return true, reason, nil
}

// checkStableBackupGate (F9, release/policies/backup.yaml "promotion_gate.stable_channel")
// torna a evidência de backup um GATE real e bloqueante — nunca só um WARN informativo — para
// QUALQUER release cujo manifesto declare channel="stable" (ADR-0054 §2.6: o canal é metadado
// de roteamento, mas "stable" é a única classe instalável na instalação SaaS compartilhada, e
// é exatamente aí que backup.yaml exige evidência fresca ANTES da promoção). Candidate/dev
// permanecem no comportamento WARN-only de F6 — nunca bloqueados por este gate, de propósito:
// backup.yaml declara o gate como exigência de PROMOÇÃO a "stable", nunca de staging/dev.
// restoreVerifiedFresh (F9) consulta platform_backup_records SEMPRE por uma query real —
// nunca um valor cacheado de um plano anterior (achado Codex [P2]: o gate só corria em
// CreatePlan; a evidência pode ter ficado obsoleta DURANTE a janela de validade do plano
// (planExpiry, 15 minutos) ou um replay de StartRun pode reaproveitar uma reserva antiga sem
// nunca revalidar isto de novo — exatamente a mesma razão de checkRevocation ser chamada em
// TODO ponto de revalidação, nunca só na ingestão/criação do plano).
func restoreVerifiedFresh(ctx context.Context, q platformdb.Querier) bool {
	var lastRestoreVerified *time.Time
	_ = q.QueryRow(ctx, `SELECT taken_at FROM platform_backup_records WHERE kind = $1 AND verified ORDER BY taken_at DESC LIMIT 1`, backupEvidenceRestoreVerifiedKind).Scan(&lastRestoreVerified)
	return lastRestoreVerified != nil && time.Since(*lastRestoreVerified).Hours() <= backupEvidenceMaxAgeHours
}

func checkStableBackupGate(m *domain.ReleaseManifest, restoreVerifiedFresh bool) (blocked bool, reason string) {
	if m.Channel != domain.ChannelStable {
		return false, ""
	}
	if restoreVerifiedFresh {
		return false, ""
	}
	return true, fmt.Sprintf("release/policies/backup.yaml: promoção ao canal stable exige evidência de RESTORE (kind=%q, scripts/backup-restore-check.sh ou -hub-check.sh) verificada e com no máximo %dh — um backup apenas tirado (nunca restaurado em teste) não satisfaz isso", backupEvidenceRestoreVerifiedKind, backupEvidenceMaxAgeHours)
}

// CreatePlan monta o preflight de uma release já ingerida contra o estado REAL da instalação —
// nunca inventa compatibilidade. BLOCK é fail-closed: nenhuma mutação segue de um plano BLOCK
// (StartRun recusa, e revalida de novo contra o estado ATUAL antes de despachar — ver StartRun).
func requestHashPlan(operator uuid.UUID, releaseID string) string {
	sum := sha256.Sum256([]byte(operator.String() + "\x00" + releaseID))
	return hex.EncodeToString(sum[:])
}

// CreatePlan é uma mutação (ADR-0054 §2.2.1 exige Idempotency-Key em toda mutação de
// plataforma) — achado Codex [P2]: faltava até aqui, então uma resposta perdida/timeout fazia
// um retry do operador criar um SEGUNDO plano (e um segundo evento de auditoria) em vez de
// recuperar o original. Replay devolve o plano JÁ CALCULADO na primeira chamada — nunca
// recalcula o preflight contra o estado atual (que pode ter mudado), pois isso violaria a
// semântica de "mesmo pedido, mesma resposta" que Idempotency-Key promete.
func (s *Service) CreatePlan(ctx context.Context, operator uuid.UUID, idempotencyKey, releaseID string) (out Plan, replayed bool, err error) {
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return Plan{}, false, invalid("Idempotency-Key deve ter 8-128 caracteres")
	}
	releaseID = strings.TrimSpace(releaseID)
	if releaseID == "" {
		return Plan{}, false, invalid("release_id é obrigatório")
	}
	// Achado Codex [P2]: sem isto, um plano criado bem depois de um run SUCCEEDED (A -> B)
	// ainda podia ler a versão OBSOLETA (A) só porque ninguém chamou GetRun/ListRuns/
	// StartRun entre o fim do run e este CreatePlan — gravando um BLOCK permanente e
	// desnecessário (ou, piOr, um PASS que StartRun rejeitaria de qualquer forma, exigindo um
	// novo plano). Transação PRÓPRIA, mesmo motivo de StartRun/ListRuns.
	if err := s.refreshPendingRun(ctx, operator); err != nil {
		return Plan{}, false, err
	}
	hash := requestHashPlan(operator, releaseID)
	err = s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, operator.String()+"/"+idempotencyKey); err != nil {
			return err
		}
		var prevHash string
		var prevPlanID uuid.UUID
		kperr := q.QueryRow(c, `SELECT request_hash, created_plan_id FROM platform_update_plan_requests WHERE operator_id = $1 AND idempotency_key = $2`, operator, idempotencyKey).
			Scan(&prevHash, &prevPlanID)
		switch {
		case kperr == nil:
			if prevHash != hash {
				return ErrKeyMismatch
			}
			replayed = true
			return q.QueryRow(c, `SELECT id, release_id, preflight_status, preflight_detail, created_by, created_at, expires_at FROM platform_update_plans WHERE id = $1`, prevPlanID).
				Scan(&out.ID, &out.ReleaseID, &out.PreflightStatus, &out.PreflightDetail, &out.CreatedBy, &out.CreatedAt, &out.ExpiresAt)
		case !errors.Is(kperr, pgx.ErrNoRows):
			return kperr
		}
		var manifestRaw []byte
		if err := q.QueryRow(c, `SELECT manifest_json FROM platform_releases WHERE release_id = $1`, releaseID).Scan(&manifestRaw); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		m, err := domain.ParseManifest(manifestRaw)
		if err != nil {
			return invalid("release armazenada não valida mais: %v", err)
		}
		var installVersion *string
		var agentProtocol int
		if err := q.QueryRow(c, `SELECT current_version, agent_protocol FROM platform_installations`).Scan(&installVersion, &agentProtocol); err != nil {
			return err
		}

		status, detail := checkCompatibility(m, installVersion, agentProtocol)
		// Achado Codex [P2]: o MESMO gate que Executor.Reconcile usa antes de processar
		// QUALQUER run desta instalação (Ledger.HasUnresolvedRecoveryRequired) — sem isto,
		// um plano PASS parecia aceitável, mas o agente real nunca executaria o run
		// resultante (fica preso em LOCKED para sempre, silenciosamente) até um operador
		// reconhecer a recuperação pendente (AcknowledgeRecovery).
		if pending, err := s.ledgerHasUnresolvedRecovery(c); err != nil {
			return fmt.Errorf("updatemgr: verificar recuperação pendente no ledger local do agente: %w", err)
		} else if pending {
			status = "BLOCK"
			reasons, _ := detail["reasons"].([]string)
			detail["reasons"] = append(reasons, "esta instalação tem uma recuperação pendente não reconhecida (RECOVERY_REQUIRED) — o Update Agent recusa processar qualquer run novo até isso ser resolvido via updatectl/runbook")
		}
		// F9 (release/policies/signing.yaml "revocation"): fail-closed — uma release revogada
		// nunca passa, independente de compatibilidade/canal.
		if revoked, reason, err := checkRevocation(c, q, releaseID); err != nil {
			return err
		} else if revoked {
			status = "BLOCK"
			reasons, _ := detail["reasons"].([]string)
			detail["reasons"] = append(reasons, fmt.Sprintf("esta release foi REVOGADA (%s) — nunca instalável, mesmo com compatibilidade/canal corretos", reason))
			detail["revoked"] = true
		}

		var lastEvidence *time.Time
		var lastVerified bool
		_ = q.QueryRow(c, `SELECT taken_at, verified FROM platform_backup_records ORDER BY taken_at DESC LIMIT 1`).Scan(&lastEvidence, &lastVerified)
		backupDetail := map[string]any{"present": lastEvidence != nil}
		if lastEvidence != nil {
			ageHours := time.Since(*lastEvidence).Hours()
			backupDetail["age_hours"] = ageHours
			backupDetail["verified"] = lastVerified
			backupDetail["fresh"] = ageHours <= backupEvidenceMaxAgeHours && lastVerified
			if ageHours > backupEvidenceMaxAgeHours || !lastVerified {
				backupDetail["warn"] = "evidência de backup ausente/desatualizada — não bloqueia em F6 (gate de promoção stable é F9, ver release/policies/backup.yaml), mas revise antes de aprovar"
			}
		} else {
			backupDetail["warn"] = "nenhuma evidência de backup registrada — não bloqueia em F6 (gate de promoção stable é F9), mas revise antes de aprovar"
		}
		// Achado Codex [P1]: "fresh" acima aceita QUALQUER kind verificado (um `pg_dump`
		// bem-sucedido, nunca testado em restore) — release/policies/backup.yaml exige
		// especificamente evidência de RESTORE (scripts/backup-restore-check.sh ou
		// -hub-check.sh), não só "o dump não falhou". O gate do canal stable usa esta
		// consulta SEPARADA, restrita a backupEvidenceRestoreVerifiedKind, nunca a
		// "fresh" genérico acima (que continua servindo só o WARN informativo de F6).
		restoreFresh := restoreVerifiedFresh(c, q)
		backupDetail["restore_verified_fresh"] = restoreFresh
		detail["backup_evidence"] = backupDetail
		// F9 (release/policies/backup.yaml "promotion_gate.stable_channel"): GATE real — nunca
		// só um WARN — quando a release é do canal "stable".
		if blocked, reason := checkStableBackupGate(m, restoreFresh); blocked {
			status = "BLOCK"
			reasons, _ := detail["reasons"].([]string)
			detail["reasons"] = append(reasons, reason)
		}

		detailRaw, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		out = Plan{ID: uuid.New(), ReleaseID: releaseID, PreflightStatus: status, PreflightDetail: detailRaw, CreatedBy: operator, ExpiresAt: time.Now().Add(planExpiry)}
		if _, err := q.Exec(c, `INSERT INTO platform_update_plans (id, release_id, preflight_status, preflight_detail, created_by, expires_at) VALUES ($1,$2,$3,$4,$5,$6)`,
			out.ID, out.ReleaseID, out.PreflightStatus, detailRaw, operator, out.ExpiresAt); err != nil {
			return err
		}
		if err := q.QueryRow(c, `SELECT created_at FROM platform_update_plans WHERE id = $1`, out.ID).Scan(&out.CreatedAt); err != nil {
			return err
		}
		if _, err := q.Exec(c, `INSERT INTO platform_update_plan_requests (operator_id, idempotency_key, request_hash, created_plan_id) VALUES ($1,$2,$3,$4)`,
			operator, idempotencyKey, hash, out.ID); err != nil {
			return err
		}
		return audit(c, q, operator, "platform.update.plan_created", map[string]any{"plan_id": out.ID, "release_id": releaseID, "preflight_status": status})
	})
	if err != nil {
		return Plan{}, false, err
	}
	return out, replayed, nil
}

func (s *Service) GetPlan(ctx context.Context, operator, planID uuid.UUID) (Plan, error) {
	var out Plan
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		err := q.QueryRow(c, `SELECT id, release_id, preflight_status, preflight_detail, created_by, created_at, expires_at FROM platform_update_plans WHERE id = $1`, planID).
			Scan(&out.ID, &out.ReleaseID, &out.PreflightStatus, &out.PreflightDetail, &out.CreatedBy, &out.CreatedAt, &out.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return out, err
}

// --- Run (ledger local + espelho Postgres) ---

type Run struct {
	ID             string    `json:"id"`
	PlanID         uuid.UUID `json:"plan_id"`
	ReleaseID      string    `json:"release_id"`
	State          string    `json:"state"`
	IdempotencyKey string    `json:"idempotency_key"`
	ActorID        uuid.UUID `json:"actor_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Event struct {
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"`
	Detail    json.RawMessage `json:"detail"`
	CreatedAt time.Time       `json:"created_at"`
}

func requestHashRun(operator uuid.UUID, planID uuid.UUID) string {
	sum := sha256.Sum256([]byte(operator.String() + "\x00" + planID.String()))
	return hex.EncodeToString(sum[:])
}

// stateRequested é um marcador SÓ do espelho Postgres (nunca um domain.RunState, nunca escrito
// no ledger local): "o pedido foi aceito e reservado, o despacho ao ledger ainda não foi
// confirmado". Achado Codex [P1]: sem reservar esta linha ANTES de escrever no ledger, um
// crash/falha entre o ledger e o espelho deixava o agente livre para executar um run que o
// Postgres nunca chegou a registrar — GetRun/ListRuns exigiam uma linha já existente para
// funcionar, então não havia como descobrir nem auditar isso depois. Reservar primeiro torna o
// pedido visível e auditável desde o instante em que foi aceito, nunca só depois do despacho.
const stateRequested = "REQUESTED"

// stateDispatchConflict: o ledger local JÁ tinha um run sob a MESMA (installation_id,
// idempotency_key) que não é o run reservado aqui — só pode acontecer se algo fora desta API
// (ex. apps/updatectl, chamado diretamente) usou a mesma chave primeiro. Nunca "adivinhamos" um
// id substituto: a linha reservada fica marcada como conflito e o pedido é recusado.
const stateDispatchConflict = "DISPATCH_CONFLICT"

// stateCompatibilityVoided: um pedido reservado (stateRequested) cujo primeiro despacho ao
// ledger nunca se confirmou, e que ao ser reexecutado (replay) não é mais compatível com a
// instalação ATUAL — achado Codex [P1]: sem revalidar aqui, um replay tardio (o daemon estava
// fora do ar, outro run mudou a versão instalada nesse meio tempo) despacharia de qualquer
// jeito, só porque "já estava reservado". Terminal: nunca despachado.
const stateCompatibilityVoided = "COMPATIBILITY_VOIDED"

// terminalMirrorStates: estados (reais do domain.RunState OU sentinelas só do espelho Postgres)
// a partir dos quais nenhuma instalação pode ter um SEGUNDO run concorrente — usado pela
// checagem de "só um update ativo por instalação" em StartRun (achado Codex [P1]: duas chaves
// de idempotência DIFERENTES, cada uma compatível com a versão instalada NO MOMENTO do próprio
// pedido, podiam ser reservadas e despachadas uma após a outra pelo mesmo daemon — a segunda
// executando contra uma instalação que o primeiro run já tinha mudado, nunca revalidada).
var terminalMirrorStates = []string{
	string(domain.StateSucceeded), string(domain.StateRolledBack), string(domain.StateRecoveryRequired),
	string(domain.StateBlocked), string(domain.StateCancelled), stateDispatchConflict, stateCompatibilityVoided,
}

// StartRun é a "comunicação autenticada com o agente" do ADR-0054 §2.2: depois de reconfirmar
// que o chamador é um platform operator ativo, que o plano ainda é PASS e não expirou, E que a
// instalação continua compatível AGORA (não só no instante em que o plano foi criado — achado
// Codex [P1]: outro run podia ter mudado a versão instalada dentro da janela de validade do
// plano), reserva o run no Postgres (stateRequested), despacha para o MESMO ledger SQLite local
// do Update Agent (nunca um caminho HTTP/Docker separado — ver o comentário do pacote) e só
// então confirma LOCKED no espelho. A partir daqui, é o Update Agent (F5, já rodando ou chamado
// via `updatectl reconcile`) que de fato executa — este serviço nunca toca Docker.
func (s *Service) StartRun(ctx context.Context, operator uuid.UUID, planID uuid.UUID, idempotencyKey string) (Run, error) {
	if len(idempotencyKey) < 8 || len(idempotencyKey) > 128 {
		return Run{}, invalid("Idempotency-Key deve ter 8-128 caracteres")
	}
	// Achado Codex [P1], CORRIGIDO DUAS VEZES: a primeira correção ainda fazia o refresh
	// DENTRO da mesma transação que podia rejeitar o pedido (plano incompatível, run já
	// ativo) — e WithTenantSession dá ROLLBACK em QUALQUER erro devolvido do callback,
	// inclusive nas gravações do próprio refresh. Um fato já confirmado pelo ledger (run
	// SUCCEEDED, instalação mudou de A para B) PRECISA sobreviver independente do que a
	// validação do pedido decidir depois — por isso roda numa transação PRÓPRIA, que só
	// confirma ou não confirma a sincronização em si, nunca misturada com a decisão de
	// aceitar/recusar ESTE StartRun. Chamado antes de qualquer leitura de
	// platform_installations/platform_update_runs abaixo (caminho novo OU replay).
	if err := s.refreshPendingRun(ctx, operator); err != nil {
		return Run{}, err
	}
	hash := requestHashRun(operator, planID)

	var releaseID, runID string
	var manifestRaw []byte
	var needsDispatch, needsVoid bool
	var voidReason string
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		// Achado Codex [P1]: o lock por (operator, idempotencyKey) abaixo só serializa RETRIES
		// da MESMA chave — duas chaves DIFERENTES nunca colidiam nele, então duas chamadas
		// concorrentes podiam ambas observar "zero runs ativos" e reservar, uma depois
		// dispachando contra uma instalação que a outra já tinha mudado. Um lock pela
		// INSTALAÇÃO (nunca pela chave) serializa TODA tentativa de reserva desta instalação,
		// nunca só os retries de uma chave específica.
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "platform-installation-runs:"+s.installationID); err != nil {
			return err
		}
		// serializa retries da mesma chave, depois reaproveita ou recusa — mesmo padrão de
		// internal/hub/companies.Service.Create.
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, operator.String()+"/"+idempotencyKey); err != nil {
			return err
		}
		var prevHash, prevRunID, prevState string
		perr := q.QueryRow(c, `SELECT rr.request_hash, rr.created_run_id, r.state FROM platform_update_run_requests rr
			JOIN platform_update_runs r ON r.id = rr.created_run_id WHERE rr.operator_id = $1 AND rr.idempotency_key = $2`, operator, idempotencyKey).
			Scan(&prevHash, &prevRunID, &prevState)
		switch {
		case perr == nil:
			if prevHash != hash {
				return ErrKeyMismatch
			}
			runID = prevRunID
			if prevState != stateRequested {
				// Já despachado (ou morto num estado terminal) numa tentativa anterior —
				// devolve como está, nunca redespacha.
				return nil
			}
			// Replay de um pedido cujo despacho ao ledger pode nunca ter sido confirmado (crash
			// entre a reserva e a confirmação). Achado Codex [P1]: "ainda está reservado" não
			// significa "ainda é seguro despachar" — o tempo pode ter passado (o daemon estava
			// fora do ar) e a instalação pode ter mudado nesse meio tempo (outro run concluído).
			// Revalida a compatibilidade AGORA, com a MESMA regra do caminho novo, antes de
			// reexecutar; CreateRun no ledger já é idempotente por (installation_id,
			// idempotency_key), então reexecutar o despacho em si é seguro.
			if err := q.QueryRow(c, `SELECT manifest_json FROM platform_update_runs WHERE id = $1`, runID).Scan(&manifestRaw); err != nil {
				return err
			}
			m, err := domain.ParseManifest(manifestRaw)
			if err != nil {
				return invalid("run reservado %s: manifesto congelado não valida mais: %v", runID, err)
			}
			var installVersion *string
			var agentProtocol int
			if err := q.QueryRow(c, `SELECT current_version, agent_protocol FROM platform_installations`).Scan(&installVersion, &agentProtocol); err != nil {
				return err
			}
			status, detail := checkCompatibility(m, installVersion, agentProtocol)
			if status == "PASS" {
				if pending, perr := s.ledgerHasUnresolvedRecovery(c); perr != nil {
					return fmt.Errorf("updatemgr: verificar recuperação pendente no ledger local do agente: %w", perr)
				} else if pending {
					status = "BLOCK"
					reasons, _ := detail["reasons"].([]string)
					detail["reasons"] = append(reasons, "esta instalação tem uma recuperação pendente não reconhecida (RECOVERY_REQUIRED) — o Update Agent recusa processar qualquer run novo até isso ser resolvido")
				}
			}
			if status == "PASS" {
				// F9: revalida revogação no momento do RE-despacho (replay) — signing.yaml
				// "anti_replay.revalidate_before_apply".
				if revoked, reason, err := checkRevocation(c, q, m.ReleaseID); err != nil {
					return err
				} else if revoked {
					status = "BLOCK"
					reasons, _ := detail["reasons"].([]string)
					detail["reasons"] = append(reasons, fmt.Sprintf("esta release foi REVOGADA (%s) — nunca despachável, mesmo já reservada", reason))
				}
			}
			// Achado Codex [P2]: o gate de backup-evidence do canal stable só corria em
			// CreatePlan — a evidência pode ter expirado DURANTE a janela de validade do
			// plano, ou um replay como este pode reaproveitar uma reserva antiga sem nunca
			// revalidar isto de novo.
			if status == "PASS" && m.Channel == domain.ChannelStable {
				if blocked, reason := checkStableBackupGate(m, restoreVerifiedFresh(c, q)); blocked {
					status = "BLOCK"
					reasons, _ := detail["reasons"].([]string)
					detail["reasons"] = append(reasons, reason)
				}
			}
			if status != "PASS" {
				// Achado Codex [P2]: gravar stateCompatibilityVoided AQUI e então devolver um
				// erro deste callback faz WithTenantSession dar ROLLBACK na transação inteira
				// (inclusive nesta própria gravação) — a linha ficava PRESA em stateRequested
				// para sempre, contando como "ativa" e bloqueando todo update futuro. A
				// gravação real acontece numa transação SEPARADA, depois que esta aqui
				// confirma com sucesso (sem erro) — ver abaixo, fora de s.tx.
				detailRaw, _ := json.Marshal(detail)
				voidReason = fmt.Sprintf("o run reservado %s deixou de ser compatível com a instalação enquanto aguardava despacho: %s", runID, detailRaw)
				needsVoid = true
				return nil
			}
			needsDispatch = true
			return nil
		case !errors.Is(perr, pgx.ErrNoRows):
			return perr
		}

		var plan Plan
		if err := q.QueryRow(c, `SELECT id, release_id, preflight_status, preflight_detail, created_by, created_at, expires_at FROM platform_update_plans WHERE id = $1`, planID).
			Scan(&plan.ID, &plan.ReleaseID, &plan.PreflightStatus, &plan.PreflightDetail, &plan.CreatedBy, &plan.CreatedAt, &plan.ExpiresAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if plan.PreflightStatus != "PASS" {
			return invalid("plano %s não está PASS (%s) — nenhum run pode começar de um preflight BLOCK", planID, plan.PreflightStatus)
		}
		if time.Now().After(plan.ExpiresAt) {
			return invalid("plano %s expirou em %s — crie um novo plano para revalidar o preflight contra o estado atual", planID, plan.ExpiresAt)
		}
		if err := q.QueryRow(c, `SELECT manifest_json FROM platform_releases WHERE release_id = $1`, plan.ReleaseID).Scan(&manifestRaw); err != nil {
			return err
		}
		m, err := domain.ParseManifest(manifestRaw)
		if err != nil {
			return invalid("release armazenada não valida mais: %v", err)
		}

		// A sincronização com o ledger (run pendente mais recente -> platform_installations)
		// já aconteceu ANTES desta transação (refreshPendingRun, acima, em sua PRÓPRIA
		// transação) — nunca aqui dentro, onde uma rejeição deste pedido a desfaria junto.
		//
		// Revalidação Codex [P1]: a instalação pode ter mudado DENTRO da janela de validade do
		// plano (outro run concluído entre a criação do plano e este StartRun) — o preflight
		// cacheado nunca é suficiente por si só para autorizar a mutação.
		var installVersion *string
		var agentProtocol int
		if err := q.QueryRow(c, `SELECT current_version, agent_protocol FROM platform_installations`).Scan(&installVersion, &agentProtocol); err != nil {
			return err
		}
		status, detail := checkCompatibility(m, installVersion, agentProtocol)
		if status == "PASS" {
			if pending, err := s.ledgerHasUnresolvedRecovery(c); err != nil {
				return fmt.Errorf("updatemgr: verificar recuperação pendente no ledger local do agente: %w", err)
			} else if pending {
				status = "BLOCK"
				reasons, _ := detail["reasons"].([]string)
				detail["reasons"] = append(reasons, "esta instalação tem uma recuperação pendente não reconhecida (RECOVERY_REQUIRED) — o Update Agent recusa processar qualquer run novo até isso ser resolvido")
			}
		}
		if status == "PASS" {
			// F9: revalida revogação no momento do APPLY (StartRun), nunca só no momento do
			// plano/download — signing.yaml "anti_replay.revalidate_before_apply": uma release
			// revogada DEPOIS do plano PASS, mas ANTES deste StartRun, nunca pode ser
			// despachada só porque o plano cacheado dizia PASS.
			if revoked, reason, err := checkRevocation(c, q, m.ReleaseID); err != nil {
				return err
			} else if revoked {
				status = "BLOCK"
				reasons, _ := detail["reasons"].([]string)
				detail["reasons"] = append(reasons, fmt.Sprintf("esta release foi REVOGADA (%s) — nunca despachável", reason))
			}
		}
		// Achado Codex [P2]: o gate de backup-evidence do canal stable só corria em
		// CreatePlan (15 minutos antes, planExpiry) — a evidência pode ter expirado dentro
		// dessa janela, e este StartRun nunca revalidava de novo antes de despachar.
		if status == "PASS" && m.Channel == domain.ChannelStable {
			if blocked, reason := checkStableBackupGate(m, restoreVerifiedFresh(c, q)); blocked {
				status = "BLOCK"
				reasons, _ := detail["reasons"].([]string)
				detail["reasons"] = append(reasons, reason)
			}
		}
		if status != "PASS" {
			detailRaw, _ := json.Marshal(detail)
			return invalid("a instalação deixou de ser compatível com esta release desde que o plano %s foi criado: %s", planID, detailRaw)
		}

		// Achado Codex [P1]: duas chaves de idempotência DIFERENTES podiam ser reservadas e
		// despachadas uma após a outra — cada uma compatível com a versão instalada NO MOMENTO
		// do próprio pedido, mas a segunda nunca revalidada contra o que a PRIMEIRA já havia
		// mudado. Só um run ativo por instalação (hoje: só existe uma) de cada vez; um segundo
		// pedido espera o primeiro terminar (sucesso, falha ou bloqueio) antes de ser aceito.
		// Serializado pelo lock de instalação acima (nunca só pelo lock por chave); o refresh
		// já rodou acima, então esta contagem já reflete o ledger.
		var activeCount int
		if err := q.QueryRow(c, `SELECT count(*) FROM platform_update_runs WHERE NOT (state = ANY($1))`, terminalMirrorStates).Scan(&activeCount); err != nil {
			return err
		}
		if activeCount > 0 {
			return invalid("já existe um update em andamento (ou pendente de despacho) para esta instalação — aguarde ele terminar antes de iniciar outro")
		}
		// Achado Codex [P1]: a contagem acima só vê runs que JÁ TÊM linha em
		// platform_update_runs — um run criado fora desta API (ex. `updatectl start`, que
		// escreve direto no ledger, sem plano/release no Postgres) é invisível para ela.
		// Consulta a AUTORIDADE de execução diretamente (o mesmo agent.Ledger.ActiveRuns que
		// Executor.Reconcile usa) antes de reservar — fail-closed: se o ledger não puder ser
		// lido, este pedido é recusado, nunca "seguimos sem saber".
		if active, err := s.ledgerHasActiveRun(ctx); err != nil {
			return fmt.Errorf("updatemgr: verificar runs ativos no ledger local do agente: %w", err)
		} else if active {
			return invalid("o ledger local do Update Agent já tem um run ativo para esta instalação (possivelmente iniciado fora desta API, ex. updatectl) — aguarde ele terminar antes de iniciar outro")
		}

		releaseID = plan.ReleaseID
		runID = "run-" + uuid.NewString()
		if _, err := q.Exec(c, `INSERT INTO platform_update_runs (id, plan_id, release_id, state, manifest_json, idempotency_key, actor_id) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			runID, planID, releaseID, stateRequested, manifestRaw, idempotencyKey, operator); err != nil {
			return err
		}
		if _, err := q.Exec(c, `INSERT INTO platform_update_run_requests (operator_id, idempotency_key, request_hash, created_run_id) VALUES ($1,$2,$3,$4)`,
			operator, idempotencyKey, hash, runID); err != nil {
			return err
		}
		needsDispatch = true
		return audit(c, q, operator, "platform.update.run_requested", map[string]any{"run_id": runID, "plan_id": planID, "release_id": releaseID})
	})
	if err != nil {
		return Run{}, err
	}

	if needsVoid {
		// Transação SEPARADA (ver comentário acima, achado Codex [P2]): esta UPDATE precisa
		// de fato confirmar, nunca ser rolada de volta junto com o erro de validação devolvido
		// ao chamador.
		if verr := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
			if err := authorize(c, q, operator); err != nil {
				return err
			}
			_, err := q.Exec(c, `UPDATE platform_update_runs SET state = $2, updated_at = now() WHERE id = $1 AND state = $3`,
				runID, stateCompatibilityVoided, stateRequested)
			return err
		}); verr != nil {
			return Run{}, verr
		}
		return Run{}, invalid("%s", voidReason)
	}

	if needsDispatch {
		// Escreve no ledger LOCAL fora da transação Postgres (arquivos SQLite não participam
		// de 2PC com o Postgres) — por isso o Postgres já reservou a linha ACIMA, antes deste
		// passo: um crash aqui deixa a linha visível em stateRequested, nunca perdida.
		rec, err := s.writeLedgerRun(ctx, operator, runID, idempotencyKey, string(manifestRaw))
		if err != nil {
			if errors.Is(err, ErrInvalid) {
				// Achado Codex [P1]: ErrInvalid aqui é SEMPRE a rechecagem de compatibilidade
				// sob o lock (latestSucceededVersion), nunca uma falha transitória de
				// infraestrutura (lock contestado, ledger inacessível) — a reserva NUNCA mais
				// seria despachável sem um novo plano, então marca como voided em vez de
				// deixá-la presa em REQUESTED (o que a contaria como "run ativo" para sempre).
				//
				// Achado Codex [P2]: SEM a condição `AND state = stateRequested`, um retry
				// CONCORRENTE da MESMA Idempotency-Key que chegasse aqui depois de outra
				// chamada já ter despachado (e o run real já ter avançado no ledger — LOCKED
				// ou até SUCCEEDED) sobrescreveria esse estado REAL com COMPATIBILITY_VOIDED,
				// corrompendo o histórico de um run que de fato executou. A condição garante
				// que só uma reserva AINDA NUNCA despachada (ainda REQUESTED) pode ser voided
				// por este caminho.
				_ = s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
					if aerr := authorize(c, q, operator); aerr != nil {
						return aerr
					}
					_, uerr := q.Exec(c, `UPDATE platform_update_runs SET state = $2, updated_at = now() WHERE id = $1 AND state = $3`,
						runID, stateCompatibilityVoided, stateRequested)
					return uerr
				})
				return Run{}, err
			}
			return Run{}, fmt.Errorf("updatemgr: gravar run no ledger local do agente: %w", err)
		}
		// Achado Codex [P1]: o ledger (escopo installation_id+idempotency_key, diferente do
		// escopo operator_id+idempotency_key do Postgres) pode já ter um run distinto sob a
		// MESMA chave — nunca assumir que o runID que reservamos foi de fato o usado.
		if rec.RunID != runID {
			_ = s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
				if err := authorize(c, q, operator); err != nil {
					return err
				}
				_, err := q.Exec(c, `UPDATE platform_update_runs SET state = $2, updated_at = now() WHERE id = $1`, runID, stateDispatchConflict)
				return err
			})
			return Run{}, fmt.Errorf("updatemgr: esta Idempotency-Key já identifica um run diferente (%s) no ledger local do agente — escolha outra chave", rec.RunID)
		}
		err = s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
			if err := authorize(c, q, operator); err != nil {
				return err
			}
			if _, err := q.Exec(c, `UPDATE platform_update_runs SET state = $2, updated_at = now() WHERE id = $1 AND state = $3`,
				runID, string(domain.StateLocked), stateRequested); err != nil {
				return err
			}
			_, err := q.Exec(c, `INSERT INTO platform_update_events (run_id, seq, kind, detail, actor_id) VALUES ($1, 0, 'run.locked', $2, $3) ON CONFLICT (run_id, seq) DO NOTHING`,
				runID, []byte(`{}`), operator)
			return err
		})
		if err != nil {
			return Run{}, err
		}
	}

	var out Run
	err = s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		return q.QueryRow(c, `SELECT id, plan_id, release_id, state, idempotency_key, actor_id, created_at, updated_at FROM platform_update_runs WHERE id = $1`, runID).
			Scan(&out.ID, &out.PlanID, &out.ReleaseID, &out.State, &out.IdempotencyKey, &out.ActorID, &out.CreatedAt, &out.UpdatedAt)
	})
	return out, err
}

// ledgerHasActiveRun consulta a AUTORIDADE de execução (o ledger local, nunca o espelho
// Postgres) por qualquer run não-terminal desta instalação — o MESMO agent.Ledger.ActiveRuns
// que Executor.Reconcile usa para decidir o que reconciliar. Existe porque
// platform_update_runs só sabe sobre runs criados POR ESTA API; um run criado por
// `updatectl start` direto no ledger (processo separado, legítimo mesmo com a API fora do ar
// — ADR-0054 §2.1 "Recovery CLI") é invisível para qualquer contagem só de Postgres. Nunca
// escreve nada, nunca adquire o lock (lock é só para MUTAR o ledger).
func (s *Service) ledgerHasActiveRun(ctx context.Context) (bool, error) {
	ledger, err := agent.OpenLedger(s.ledgerPath)
	if err != nil {
		return false, err
	}
	defer ledger.Close()
	runs, err := ledger.ActiveRuns(ctx, s.installationID)
	if err != nil {
		return false, err
	}
	return len(runs) > 0, nil
}

// ledgerHasUnresolvedRecovery consulta o MESMO gate que Executor.Reconcile usa
// (Ledger.HasUnresolvedRecoveryRequired) antes de processar qualquer run desta instalação —
// achado Codex [P2]: RECOVERY_REQUIRED é tratado como terminal pela contagem "só um run
// ativo" (terminalMirrorStates, corretamente: nenhuma transição automática parte dele), mas
// isso não significa que a instalação esteja livre para receber um NOVO run — o agente real
// se recusa a processar QUALQUER coisa enquanto essa recuperação não for reconhecida
// (AcknowledgeRecovery). Sem esta checagem, CreatePlan/StartRun aceitavam um pedido que o
// agente nunca executaria, deixando-o preso em LOCKED para sempre, silenciosamente.
func (s *Service) ledgerHasUnresolvedRecovery(ctx context.Context) (bool, error) {
	ledger, err := agent.OpenLedger(s.ledgerPath)
	if err != nil {
		return false, err
	}
	defer ledger.Close()
	return ledger.HasUnresolvedRecoveryRequired(ctx, s.installationID)
}

// writeLedgerRun abre o ledger local do Update Agent, adquire o lock da instalação (o mesmo
// lock que o daemon/updatectl usam) e cria o run — nunca interpreta/executa o manifesto, só o
// registra para o agente reconciliar. A posse do lock é devolvida imediatamente: este serviço
// nunca retém o lock entre chamadas (faria o daemon travar). Devolve o RunRecord REAL que o
// ledger usou — achado Codex [P1]: CreateRun pode devolver um run EXISTENTE (dedup por
// installation_id+idempotency_key) cujo RunID diverge do que esta chamada propôs; o chamador
// precisa desse valor real, nunca o descartar.
func (s *Service) writeLedgerRun(ctx context.Context, operator uuid.UUID, runID, idempotencyKey, manifestJSON string) (*agent.RunRecord, error) {
	ledger, err := agent.OpenLedger(s.ledgerPath)
	if err != nil {
		return nil, err
	}
	defer ledger.Close()

	m, err := domain.ParseManifest([]byte(manifestJSON))
	if err != nil {
		return nil, fmt.Errorf("updatemgr: manifesto congelado do run não valida mais no momento do despacho: %w", err)
	}

	// Achado Codex [P1], rodada final: ler platform_installations numa transação Postgres que
	// JÁ COMMITOU (liberando o lock de instalação) antes de abrir o lock do ledger deixava uma
	// janela real — SetInstallationVersion (que também exige o MESMO lock) podia intercalar
	// bem ali. A ÚNICA forma de fechar isso de verdade é nunca soltar o lock de instalação
	// entre a leitura e o CreateRun: todo o resto desta função (AcquireLock do ledger,
	// rechecagem de runs ativos, rechecagem de compatibilidade, CreateRun) vive DENTRO desta
	// mesma transação Postgres, com o lock detido do início ao fim. SetInstallationVersion
	// bloqueia até esta transação terminar, nunca intercalando no meio.
	var rec *agent.RunRecord
	err = s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "platform-installation-runs:"+s.installationID); err != nil {
			return err
		}
		var instVersion *string
		var instUpdatedAt time.Time
		if err := q.QueryRow(c, `SELECT current_version, updated_at FROM platform_installations`).Scan(&instVersion, &instUpdatedAt); err != nil {
			return err
		}

		h, err := ledger.AcquireLock(c, s.installationID, s.holder, s.lease)
		if err != nil {
			return err
		}
		defer ledger.ReleaseLock(context.Background(), h)
		// Achado Codex [P1]: ledgerHasActiveRun (chamado pelo chamador, ANTES deste
		// AcquireLock) e este ponto não são atômicos entre si — `updatectl start` podia
		// adquirir o lock e criar um run NESSA janela. Rechecagem AGORA, já com o lock
		// detido: CreateRun só exige checkFencing (o token deste handle), nunca uma segunda
		// trava de "só um run ativo" — essa regra é desta API, não do ledger em si, então
		// precisa ser imposta aqui explicitamente. Preserva o retry de um pedido já criado
		// (mesma idempotency_key): só recusa se o run ativo encontrado for de uma chave
		// DIFERENTE.
		active, err := ledger.ActiveRuns(c, s.installationID)
		if err != nil {
			return err
		}
		for _, r := range active {
			if r.IdempotencyKey != idempotencyKey {
				return fmt.Errorf("%w: run ativo %s (outra Idempotency-Key) já existe no ledger para esta instalação", ErrInvalid, r.RunID)
			}
		}
		// Achado Codex [P2]: um run `updatectl` pode ter entrado em RECOVERY_REQUIRED
		// EXATAMENTE na janela entre a checagem inicial de StartRun e este ponto —
		// RECOVERY_REQUIRED é terminal (nunca aparece em ActiveRuns), então a checagem
		// acima não pega isso. Rechecagem AGORA, já com o lock do ledger detido: o MESMO
		// gate que Executor.Reconcile usa antes de processar qualquer run, atomicamente com
		// o CreateRun que seguirá.
		if pending, err := ledger.HasUnresolvedRecoveryRequired(c, s.installationID); err != nil {
			return err
		} else if pending {
			return fmt.Errorf("%w: a instalação tem uma recuperação pendente não reconhecida (RECOVERY_REQUIRED) — o Update Agent recusaria processar este run", ErrInvalid)
		}
		// Achado Codex [P2]: confiar CIEGAMENTE no run SUCCEEDED mais recente do ledger
		// quebra uma recuperação manual aprovada (ADR-0054 §8, R3/R4): se um operador já
		// chamou SetInstallationVersion DEPOIS do último run do ledger (ex. restaurou para
		// uma versão anterior após um incidente), o histórico do ledger fica desatualizado
		// e esta checagem bloquearia QUALQUER despacho futuro permanentemente. Decide pelo
		// fato mais RECENTE entre os dois, comparando updated_at: o ledger só vence se seu
		// run SUCCEEDED for estritamente mais novo que a última escrita conhecida em
		// platform_installations (que já reflete tanto syncs automáticos quanto
		// SetInstallationVersion manual).
		effectiveVersion := instVersion
		if ver, _, _, runUpdatedAt, ok, err := latestSucceededVersion(c, ledger, s.installationID); err != nil {
			return err
		} else if ok && runUpdatedAt.After(instUpdatedAt) {
			effectiveVersion = &ver
		}
		if effectiveVersion != nil && *effectiveVersion != "" && !slices.Contains(m.Compatibility.FromVersions, *effectiveVersion) {
			return fmt.Errorf("%w: a instalação (versão %s, confirmada agora) deixou de ser compatível com esta release entre a validação e o despacho", ErrInvalid, *effectiveVersion)
		}
		rec, err = ledger.CreateRun(c, h, runID, idempotencyKey, manifestJSON, domain.StateLocked)
		return err
	})
	if err != nil {
		return nil, err
	}
	return rec, nil
}

// latestSucceededVersion lê DIRETO do ledger (nunca do espelho Postgres, que pode estar
// defasado por definição neste instante) qual é o run SUCCEEDED mais recente desta instalação
// e devolve a platform_version/release_id do manifesto congelado nele. ok=false quando nenhum
// run jamais teve sucesso nesta instalação (ex. instalação recém-bootstrapada, antes de
// qualquer SetInstallationVersion/primeiro deploy) — o chamador decide o que fazer nesse caso,
// nunca tratado aqui como "qualquer versão serve".
// latestSucceededRun devolve o RunRecord SUCCEEDED mais recente desta instalação — a busca
// bruta compartilhada por latestSucceededVersion e latestSucceededManifest, nunca duplicada
// entre as duas (que só diferem no que extraem do manifesto depois de achá-lo).
func latestSucceededRun(ctx context.Context, ledger *agent.Ledger, installationID string) (*agent.RunRecord, bool, error) {
	all, err := ledger.AllRuns(ctx, installationID)
	if err != nil {
		return nil, false, err
	}
	var latest *agent.RunRecord
	for i := range all {
		r := &all[i]
		if r.State != domain.StateSucceeded {
			continue
		}
		if latest == nil || r.UpdatedAt.After(latest.UpdatedAt) {
			latest = r
		}
	}
	return latest, latest != nil, nil
}

func latestSucceededVersion(ctx context.Context, ledger *agent.Ledger, installationID string) (version, releaseID, runID string, updatedAt time.Time, ok bool, err error) {
	latest, found, err := latestSucceededRun(ctx, ledger, installationID)
	if err != nil || !found {
		return "", "", "", time.Time{}, false, err
	}
	m, err := domain.ParseManifest([]byte(latest.ManifestJSON))
	if err != nil {
		return "", "", "", time.Time{}, false, err
	}
	return m.PlatformVersion, m.ReleaseID, latest.RunID, latest.UpdatedAt, true, nil
}

// expectedDigestsAcrossHistory (F9, monitoramento "versão real/digest divergente", achado
// Codex [P2] rodada 2) devolve o digest esperado POR UNIT através de TODO o histórico de runs
// SUCCEEDED desta instalação — nunca só da release SUCCEEDED mais recente no geral, que
// rolloutUnits permite tocar só um SUBCONJUNTO dos workloads (release A só atualiza a API;
// release B, depois, só o web — olhar só B perderia drift na API, mesmo com o digest
// observado disponível). Para cada unit, usa o digest declarado pela release SUCCEEDED mais
// recente que a tocou — mesma ideia de proveniência por unit de
// Executor.rollbackTargetReleaseIDs, mas só SUCCEEDED aqui (nunca RECOVERY_REQUIRED: este
// sinal é sobre o que foi INTENCIONALMENTE confirmado como bem-sucedido, não sobre o que pode
// ter ficado parcialmente aplicado — distinto do propósito de segurança do rollback). Um
// manifesto histórico ilegível é melhor esforço (pulado, nunca falha a chamada inteira): isto
// é monitoramento, nunca um gate que bloqueia operação real.
func expectedDigestsAcrossHistory(ctx context.Context, ledger *agent.Ledger, installationID string) (map[string]string, error) {
	all, err := ledger.AllRuns(ctx, installationID)
	if err != nil {
		return nil, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].UpdatedAt.After(all[j].UpdatedAt) })
	result := map[string]string{}
	for i := range all {
		r := &all[i]
		if r.State != domain.StateSucceeded {
			continue
		}
		m, perr := domain.ParseManifest([]byte(r.ManifestJSON))
		if perr != nil {
			continue
		}
		for unit, digest := range agent.ExpectedDigests(m) {
			if _, already := result[unit]; !already {
				result[unit] = digest
			}
		}
	}
	return result, nil
}

// refreshPendingRun sincroniza o run NÃO-TERMINAL mais recente (se existir) com o ledger local,
// numa transação PRÓPRIA que sempre confirma por si só — nunca misturada com a decisão de
// aceitar/recusar o StartRun que a chama (ver o comentário em StartRun: a primeira tentativa de
// corrigir isto fazia o refresh dentro da MESMA transação que podia rejeitar o pedido, e
// WithTenantSession desfazia o refresh junto com a rejeição). "Nenhum run pendente" e "ledger
// indisponível neste ambiente" não são erros — só uma falha real de autorização/Postgres é.
func (s *Service) refreshPendingRun(ctx context.Context, operator uuid.UUID) error {
	return s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		if _, err := q.Exec(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "platform-installation-runs:"+s.installationID); err != nil {
			return err
		}
		ledger, err := agent.OpenLedger(s.ledgerPath)
		if err != nil {
			return nil // melhor esforço: ambiente sem ledger (ex. nenhum run foi criado ainda)
		}
		defer ledger.Close()
		// F9 (achado Codex [P1]): reentrega qualquer revogação cuja escrita no ledger local
		// falhou/nunca aconteceu (crash/timeout entre o commit do Postgres e essa escrita) —
		// esta chamada roda em QUASE TODA operação real desta API (CreatePlan, StartRun,
		// GetInstallation, ListRuns), dando entrega confiável e eventual sem precisar de um
		// worker de fundo separado. Melhor esforço: uma falha aqui nunca bloqueia o resto
		// desta função (o preflight/dispatch que a chamou), só loga e tenta de novo depois.
		if err := reconcileRevocationsToLedger(c, q, s.ledgerPath); err != nil {
			log.Printf("updatemgr: reconcileRevocationsToLedger: %v", err)
		}
		// Achado Codex [P1]: isto precisa rodar INCONDICIONALMENTE, mesmo sem nenhum run
		// pendente no Postgres — um run criado por `updatectl start` pode ter concluído sem
		// NUNCA ter tido linha aqui.
		if err := s.applyLatestSucceededRunToInstallation(c, q, ledger); err != nil {
			return err
		}
		var pendingRunID string
		perr := q.QueryRow(c, `SELECT id FROM platform_update_runs WHERE NOT (state = ANY($1)) ORDER BY created_at DESC LIMIT 1`, terminalMirrorStates).Scan(&pendingRunID)
		if errors.Is(perr, pgx.ErrNoRows) {
			return nil
		}
		if perr != nil {
			return perr
		}
		rec, err := ledger.GetRun(c, pendingRunID)
		if err != nil {
			return nil // melhor esforço: run pode ter sido removido/corrompido no ledger
		}
		_, err = q.Exec(c, `UPDATE platform_update_runs SET state = $2, updated_at = now() WHERE id = $1 AND state <> $2`, pendingRunID, string(rec.State))
		return err
	})
}

// applyLatestSucceededRunToInstallation aplica a platform_installations o manifesto do run
// SUCCEEDED mais recente (por updated_at) de TODO o ledger desta instalação — nunca só de um
// run específico. Achado Codex [P1], duas rodadas: (1) um run criado fora desta API
// (`updatectl start`, legítimo mesmo com a API fora do ar — ADR-0054 §2.1 "Recovery CLI")
// nunca tem linha em platform_update_runs, então uma sincronização que só olhasse para o
// espelho Postgres nunca o veria; (2) escolher sempre o GLOBALMENTE mais recente (nunca "o
// run que esta chamada específica estava olhando") torna a aplicação idempotente e segura sob
// chamadas concorrentes — nunca um fato mais antigo sobrescreve um mais novo.
// last_synced_run_id evita reaplicar o mesmo fato (e o custo de reabrir/reler o ledger) a cada
// chamada. Chamador precisa já deter a trava de instalação (pg_advisory_xact_lock) e já ter
// aberto `ledger`.
func (s *Service) applyLatestSucceededRunToInstallation(ctx context.Context, q platformdb.Querier, ledger *agent.Ledger) error {
	version, releaseID, runID, runUpdatedAt, ok, err := latestSucceededVersion(ctx, ledger, s.installationID)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	var lastSynced *string
	var instUpdatedAt time.Time
	if err := q.QueryRow(ctx, `SELECT last_synced_run_id, updated_at FROM platform_installations`).Scan(&lastSynced, &instUpdatedAt); err != nil {
		return err
	}
	if lastSynced != nil && *lastSynced == runID {
		return nil
	}
	// Achado Codex [P2]: simétrico ao gate de writeLedgerRun — nunca sobrescrever um
	// current_version mais recente (ex. um SetInstallationVersion manual pós-recuperação,
	// ADR-0054 §8 R3/R4) com um fato do ledger mais ANTIGO só porque "ainda não tínhamos
	// sincronizado esse run_id específico" (ele pode ser genuinamente mais antigo que a
	// última escrita manual).
	if !instUpdatedAt.Before(runUpdatedAt) {
		return nil
	}
	// Achado Codex [P1]: usar now() (o instante em que ESTA chamada de sync roda) em vez do
	// timestamp do PRÓPRIO run no ledger (runUpdatedAt) misturava dois relógios diferentes —
	// se um run Y sucedesse no ledger EXATAMENTE durante esta transação (depois de
	// latestSucceededVersion já ter lido X, antes deste UPDATE comitar), platform_installations
	// ficava com a versão de X mas um updated_at JÁ POSTERIOR ao sucesso real de Y. A próxima
	// sincronização comparava runUpdatedAt(Y) < instUpdatedAt(agora, pós-X) e descartava Y PARA
	// SEMPRE — a versão instalada ficava presa em X mesmo com Y já tendo sucedido de verdade.
	// Gravar o timestamp do PRÓPRIO run elimina esse descompasso: a comparação volta a ser
	// sempre ledger-contra-ledger, nunca ledger-contra-relógio-de-aplicação.
	_, err = q.Exec(ctx, `UPDATE platform_installations SET current_version = $1, current_release_id = $2, last_synced_run_id = $3, updated_at = $4`,
		version, releaseID, runID, runUpdatedAt)
	return err
}

// refreshFromLedger lê o estado ATUAL do run no ledger local (a autoridade de execução,
// ADR-0054 §2.4) e espelha no Postgres qualquer checkpoint/estado ainda não refletido — nunca o
// contrário. Chamado por GetRun antes de responder, para que a view administrativa nunca fique
// presa num estado antigo só porque ninguém chamou um endpoint de "sincronizar" separado.
// Erros de leitura do ledger (arquivo ausente, agente nunca rodou neste host) são tolerados e
// logados pelo chamador como um aviso — a view ainda responde com o último estado conhecido no
// Postgres, nunca falha a requisição por isso.
func (s *Service) refreshFromLedger(ctx context.Context, q platformdb.Querier, runID string) error {
	// Achado Codex [P1]: duas chamadas CONCORRENTES desta função para o MESMO run podiam
	// intercalar — uma lê o ledger num estado intermediário (ex. DEPLOYING) e pausa, outra lê
	// SUCCEEDED e confirma (inclusive atualizando platform_installations), e a primeira,
	// retomando, SOBRESCREVE SUCCEEDED com o seu próprio estado JÁ OBSOLETO (a condição
	// `state <> $2` abaixo não garante ORDEM, só desigualdade). Serializado pela MESMA trava
	// de instalação usada por StartRun/refreshPendingRun — segura chamar de novo na mesma
	// transação Postgres (pg_advisory_xact_lock é idempotente para o mesmo backend/chave).
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "platform-installation-runs:"+s.installationID); err != nil {
		return err
	}
	ledger, err := agent.OpenLedger(s.ledgerPath)
	if err != nil {
		return err
	}
	defer ledger.Close()
	rec, err := ledger.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `UPDATE platform_update_runs SET state = $2, updated_at = now() WHERE id = $1 AND state <> $2`, runID, string(rec.State)); err != nil {
		return err
	}
	// Achado Codex [P1] (rodadas 7-8): a versão instalada precisa vir do run SUCCEEDED mais
	// recente de TODO o ledger desta instalação — nunca só deste runID específico — porque um
	// run criado fora desta API (`updatectl start`) também muda a instalação, e este runID
	// pode nem ser o mais recente. ver applyLatestSucceededRunToInstallation.
	if err := s.applyLatestSucceededRunToInstallation(ctx, q, ledger); err != nil {
		return err
	}
	checkpoints, err := ledger.Checkpoints(ctx, runID)
	if err != nil {
		return err
	}
	var maxSeq int64
	if err := q.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM platform_update_events WHERE run_id = $1`, runID).Scan(&maxSeq); err != nil {
		return err
	}
	for i, cp := range checkpoints {
		seq := int64(i) + 1
		if seq <= maxSeq {
			continue
		}
		detail := map[string]any{"step": cp.Step, "status": cp.Status, "detail": cp.Detail}
		raw, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `INSERT INTO platform_update_events (run_id, seq, kind, detail) VALUES ($1, $2, 'checkpoint', $3) ON CONFLICT (run_id, seq) DO NOTHING`,
			runID, seq, raw); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) GetRun(ctx context.Context, operator uuid.UUID, runID string) (Run, error) {
	var out Run
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		if err := q.QueryRow(c, `SELECT id, plan_id, release_id, state, idempotency_key, actor_id, created_at, updated_at FROM platform_update_runs WHERE id = $1`, runID).
			Scan(&out.ID, &out.PlanID, &out.ReleaseID, &out.State, &out.IdempotencyKey, &out.ActorID, &out.CreatedAt, &out.UpdatedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		// Melhor esforço: o ledger local pode não existir neste ambiente de teste/CI (o
		// agente nunca rodou aqui) — a view ainda responde com o espelho Postgres conhecido.
		if err := s.refreshFromLedger(c, q, runID); err == nil {
			_ = q.QueryRow(c, `SELECT id, plan_id, release_id, state, idempotency_key, actor_id, created_at, updated_at FROM platform_update_runs WHERE id = $1`, runID).
				Scan(&out.ID, &out.PlanID, &out.ReleaseID, &out.State, &out.IdempotencyKey, &out.ActorID, &out.CreatedAt, &out.UpdatedAt)
		}
		return nil
	})
	return out, err
}

func (s *Service) ListRuns(ctx context.Context, operator uuid.UUID, limit int, before *time.Time) ([]Run, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	// Achado Codex [P2]: sem isto, um cliente que só chama ListRuns (nunca GetRun/ListEvents,
	// que também sincronizam com o ledger) via LOCKED para sempre depois do agente já ter
	// concluído/falhado de verdade. Transação PRÓPRIA (mesmo motivo de StartRun): a listagem
	// abaixo nunca deve desfazer uma sincronização real só porque, por exemplo, a paginação
	// pedida é inválida.
	if err := s.refreshPendingRun(ctx, operator); err != nil {
		return nil, err
	}
	var out []Run
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		var rows pgx.Rows
		var err error
		if before != nil {
			rows, err = q.Query(c, `SELECT id, plan_id, release_id, state, idempotency_key, actor_id, created_at, updated_at FROM platform_update_runs
				WHERE created_at < $1 ORDER BY created_at DESC LIMIT $2`, *before, limit)
		} else {
			rows, err = q.Query(c, `SELECT id, plan_id, release_id, state, idempotency_key, actor_id, created_at, updated_at FROM platform_update_runs
				ORDER BY created_at DESC LIMIT $1`, limit)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Run
			if err := rows.Scan(&r.ID, &r.PlanID, &r.ReleaseID, &r.State, &r.IdempotencyKey, &r.ActorID, &r.CreatedAt, &r.UpdatedAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Run{}
	}
	return out, err
}

func (s *Service) ListEvents(ctx context.Context, operator uuid.UUID, runID string, after int64) ([]Event, error) {
	var out []Event
	err := s.tx(ctx, func(c context.Context, q platformdb.Querier) error {
		if err := authorize(c, q, operator); err != nil {
			return err
		}
		var exists bool
		if err := q.QueryRow(c, `SELECT EXISTS (SELECT 1 FROM platform_update_runs WHERE id = $1)`, runID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		_ = s.refreshFromLedger(c, q, runID)
		rows, err := q.Query(c, `SELECT seq, kind, detail, created_at FROM platform_update_events WHERE run_id = $1 AND seq > $2 ORDER BY seq ASC`, runID, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e Event
			if err := rows.Scan(&e.Seq, &e.Kind, &e.Detail, &e.CreatedAt); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Event{}
	}
	return out, err
}
