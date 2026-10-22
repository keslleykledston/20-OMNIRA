// Package agent contém o Update Agent (F5, docs/release/spec/..._PACOTE_EXECUCAO.md §2.2/§5,
// ADR-0054 §2.1/§2.3): o processo independente da API e dos workers que de fato executa um
// platform_update_run. Nenhum import de pgx/HTTP aqui — o agente nunca depende do Postgres
// estar disponível para decidir o próximo passo seguro após um crash (especificação §6.1
// "Crash/restart do agente: reavaliar estado durável e nunca repetir migração ou deploy de
// forma cega"); a única dependência é este ledger local (SQLite com WAL, ADR-0054 §2.3) e o
// domínio puro já construído em F1 (internal/platformrelease/domain).
//
// ledger.go: lock/fencing/heartbeat por instalação e o journal de checkpoints por run. Este é
// o ÚNICO estado que sobrevive a um crash do processo do agente — nunca em memória, nunca
// apenas no Postgres (especificação §6.1 "Lock por installation_id... não depender só de lock
// em memória ou de PG caso PG esteja indisponível").
package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/omnira/omnira/internal/platformrelease/domain"
)

// ErrFencingLost é retornado por qualquer operação de mutação quando o fencing_token
// apresentado não bate mais com o gravado no ledger — outro processo adquiriu o lock desde
// então (o holder anterior morreu/expirou, ou um bug tentou operar depois de perder o lock).
// Todo chamador deve abortar IMEDIATAMENTE qualquer operação em andamento ao ver este erro,
// nunca tentar "continuar mesmo assim".
var ErrFencingLost = errors.New("agent: fencing token perdido — outro processo detém o lock desta instalação")

// ErrLockHeld é retornado por AcquireLock quando outro holder detém um lock ainda válido
// (heartbeat dentro do lease).
var ErrLockHeld = errors.New("agent: lock já detido por outro processo (heartbeat ainda válido)")

const schemaSQL = `
CREATE TABLE IF NOT EXISTS agent_lock (
  installation_id TEXT PRIMARY KEY,
  fencing_token    INTEGER NOT NULL,
  holder           TEXT NOT NULL,
  acquired_at      TEXT NOT NULL,
  heartbeat_at     TEXT NOT NULL,
  lease_ms         INTEGER NOT NULL,
  released         INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS update_runs (
  run_id                TEXT PRIMARY KEY,
  installation_id       TEXT NOT NULL,
  idempotency_key       TEXT NOT NULL,
  state                 TEXT NOT NULL,
  fencing_token         INTEGER NOT NULL,
  manifest_json         TEXT NOT NULL,
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL,
  recovery_acknowledged INTEGER NOT NULL DEFAULT 0,
  UNIQUE(installation_id, idempotency_key)
);

CREATE TABLE IF NOT EXISTS run_checkpoints (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id      TEXT NOT NULL REFERENCES update_runs(run_id),
  step        TEXT NOT NULL,
  status      TEXT NOT NULL CHECK (status IN ('started','completed','failed')),
  detail      TEXT NOT NULL DEFAULT '',
  recorded_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS run_checkpoints_run_id_idx ON run_checkpoints(run_id, id);

-- F9 (release/policies/signing.yaml "anti_replay.revalidate_before_apply"): espelho LOCAL da
-- revogação registrada no Postgres (updatemgr.Service.RevokeRelease escreve nas duas, nesta
-- ordem). O agente nunca depende do Postgres/API estarem disponíveis para decidir o próximo
-- passo seguro (comentário do pacote acima) — sem este espelho local, uma release revogada
-- DEPOIS do despacho (StartRun) mas ANTES do agente de fato aplicar os steps perigosos (que
-- pode ser bem depois: poll interval, daemon reiniciado) seria instalada mesmo revogada,
-- porque Executor.Execute nunca consulta o Postgres diretamente.
CREATE TABLE IF NOT EXISTS revoked_releases (
  release_id TEXT PRIMARY KEY,
  reason     TEXT NOT NULL,
  revoked_at TEXT NOT NULL
);

-- F9, achado Codex [P1] rodada 7: "nenhuma revogação pendente" (entrega eventual, achado
-- anterior) só prova que CADA revogação que o agente conseguiu enxergar foi entregue — nunca
-- que o Postgres não tem uma revogação NOVA que simplesmente nunca chegou a ser verificada,
-- porque a API inteira ficou fora do ar por mais tempo do que qualquer retry individual
-- cobre. Esta linha única (id=1) é o relógio dessa garantia: só
-- updatemgr.deliverRevocationToLedger/redeliverPendingRevocations a atualizam, e só depois de
-- confirmar com sucesso o estado ATUAL do Postgres. RevocationSyncFresh compara contra
-- signing.yaml anti_replay.revocation.max_cache_age_seconds (300s) — "nunca revogada" só é
-- confiável enquanto esta marca for recente; mais antiga que isso é fail-closed, nunca
-- "presumo que está tudo bem".
--
-- standalone (achado Codex [P1] rodada 10 da fatia de retenção/GC, corrigindo a rodada 9):
-- NENHUMA linha NUNCA pode significar "fresco" por decreto — uma instalação GERENCIADA
-- (API habilitada em algum lugar) cuja primeira sincronização simplesmente ainda não
-- aconteceu (ex. a API ficou fora do ar durante o bootstrap) ficaria assim indefinidamente
-- "confiável" sem NUNCA ter confirmado o estado real do Postgres — exatamente o oposto do
-- que esta tabela existe para garantir. Só uma instalação standalone DE VERDADE
-- (OMNIRA_AGENT_STANDALONE=true, decisão EXPLÍCITA do operador, nunca inferida pela simples
-- ausência de uma linha) grava standalone=1 (MarkStandalone, chamado uma vez no startup do
-- daemon) e é tratada como fresca para sempre, sem depender de nenhuma sincronização real.
CREATE TABLE IF NOT EXISTS revocation_sync (
  id         INTEGER PRIMARY KEY CHECK (id = 1),
  synced_at  TEXT NOT NULL,
  standalone INTEGER NOT NULL DEFAULT 0
);

-- F9 (monitoramento, "status do agente"): o lock de agent_lock só existe ENQUANTO um run está
-- ativamente sendo processado — entre runs ele é liberado, então seu heartbeat_at nunca indica
-- se o PROCESSO do agente ainda está vivo. Esta linha única por instalação é atualizada por um
-- ticker INDEPENDENTE do ciclo de reconcile (achado Codex [P2]: gravar só no início de
-- agentconfig.ReconcileOnce faria o relógio parar de avançar durante qualquer reconcile
-- genuinamente longo — um ObserveWindow de 10 minutos, um backup grande — marcando um daemon
-- SAUDÁVEL e trabalhando como obsoleto). observed_digests (achado Codex [P2]) é o snapshot
-- REAL mais recente do Docker (ComposeAdapter.CurrentSnapshot), gravado a cada ciclo de
-- reconcile — nem o ledger nem o Postgres observam containers de verdade por si só; sem isto,
-- um container substituído manualmente (ou uma implantação que falhou deixando outra imagem
-- rodando) nunca apareceria em nenhum sinal de monitoramento.
CREATE TABLE IF NOT EXISTS agent_heartbeat (
  installation_id   TEXT PRIMARY KEY,
  holder            TEXT NOT NULL,
  last_poll_at      TEXT NOT NULL,
  observed_digests  TEXT NOT NULL DEFAULT '{}',
  observed_at       TEXT
);
`

// Ledger é o estado durável do agente para UMA instalação (há uma só instalação real hoje,
// ADR-0054 §2.4, mas a chave installation_id já existe em todo lugar para nunca presumir isso
// em código). Seguro para uso concorrente por goroutines dentro do MESMO processo; a segurança
// entre PROCESSOS diferentes (dois agentes, ou o agente e `updatectl`, abrindo o mesmo arquivo)
// vem da tabela agent_lock em si, nunca de um mutex em memória.
type Ledger struct {
	db *sql.DB
}

// OpenLedger abre (criando se necessário) o arquivo SQLite em path, em modo WAL com
// busy_timeout generoso — dois processos do SO escrevendo no mesmo arquivo devem esperar a
// transação um do outro terminar, nunca falhar imediatamente com "database is locked" por uma
// contenção de alguns milissegundos. `_txlock=immediate` (achado Codex): toda transação deste
// pacote faz um SELECT antes de um INSERT/UPDATE (lê o estado atual, decide, então escreve); em
// modo WAL, uma transação DEFERRED (o padrão) que começou lendo um snapshot pode falhar com
// SQLITE_BUSY_SNAPSHOT ao tentar fazer upgrade para escritora se outra transação commitou nesse
// intervalo — um erro que busy_timeout NÃO resolve (não é uma espera por lock comum, é uma
// rejeição imediata de upgrade de snapshot). Abrir toda transação já como escritora
// (BEGIN IMMEDIATE) elimina essa janela: a serialização vira uma espera normal por
// busy_timeout, nunca uma falha instantânea.
func OpenLedger(path string) (*Ledger, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_txlock=immediate", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("agent: abrir ledger %s: %w", path, err)
	}
	// Um processo de agente é um daemon de longa duração com poucas conexões concorrentes
	// (goroutines internas, nunca um pool grande) — limitar evita que o SQLite veja mais
	// conexões simultâneas do que realmente precisa.
	db.SetMaxOpenConns(4)
	// Achado Codex P2: db.Exec(schemaSQL) FORA de uma transação explícita não se beneficiava
	// da mesma serialização via _txlock=immediate que addColumnIfMissing usa — N processos
	// abrindo o MESMO arquivo legado ao mesmo tempo (o cenário real exercitado por
	// TestOpenLedger_ConcurrentMigration_NeverFailsWithDuplicateColumn) podiam colidir aqui
	// com SQLITE_BUSY de forma intermitente (reproduzido: ~2 em 10 execuções), tornando
	// OpenLedger não-determinístico sob concorrência real. Envolver a criação do schema
	// inicial na MESMA disciplina de transação explícita elimina essa janela.
	if err := retryOnBusy(func() error { return createSchema(db) }); err != nil {
		db.Close()
		return nil, fmt.Errorf("agent: criar schema do ledger: %w", err)
	}
	if err := retryOnBusy(func() error { return migrateSchema(db) }); err != nil {
		db.Close()
		return nil, fmt.Errorf("agent: migrar schema do ledger: %w", err)
	}
	// F9, achado Codex [P1] rodada 9: deliberadamente NÃO grava nenhum baseline em
	// revocation_sync aqui. A rodada 7 inicializava com "agora" na primeira abertura — mas
	// isso certificava como "fresco" um ledger que nunca de fato consultou o Postgres: um
	// ledger pré-F9 reaberto pela primeira vez depois de um upgrade, ou um ledger novo numa
	// instalação onde o Postgres já tinha revogações, passava os primeiros 300s "confiável"
	// sem nenhuma sincronização real ter acontecido. Ver Ledger.RevocationSyncFresh — nenhuma
	// linha é tratado como "nunca sincronizado" (gate não se aplica, nunca "fresco por
	// decreto"), e só updatemgr.MarkRevocationSynced (depois de confirmar com sucesso o
	// estado do Postgres) grava a primeira linha de verdade.
	return &Ledger{db: db}, nil
}

// retryOnBusy repete `fn` algumas vezes com um pequeno atraso quando o erro é um SQLITE_BUSY —
// achado Codex P2: mesmo dentro de uma transação explícita (_txlock=immediate deveria
// serializar isso via busy_timeout), N PROCESSOS abrindo e inicializando pela PRIMEIRA vez o
// MESMO arquivo .db concorrentemente ainda reproduziam "database is locked (5)"
// ocasionalmente (~1 em 10 execuções mesmo após a transação) — aparenta ser uma janela na
// própria ABERTURA/criação dos arquivos .db-wal/.db-shm auxiliares, anterior a qualquer
// PRAGMA busy_timeout já surtir efeito, não uma contenção normal de transação. Isto só
// acontece na INICIALIZAÇÃO do ledger (createSchema/migrateSchema), nunca durante operação
// normal — ambas as operações são idempotentes e totalmente seguras de repetir.
func retryOnBusy(fn func() error) error {
	const attempts = 20
	var lastErr error
	for i := 0; i < attempts; i++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}
		if !strings.Contains(lastErr.Error(), "SQLITE_BUSY") && !strings.Contains(lastErr.Error(), "database is locked") {
			return lastErr
		}
		time.Sleep(time.Duration(10*(i+1)) * time.Millisecond)
	}
	return lastErr
}

func createSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("iniciar transação de criação de schema: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schemaSQL); err != nil {
		return err
	}
	return tx.Commit()
}

// migrateSchema aplica, de forma idempotente, as colunas acrescentadas DEPOIS da criação
// original de uma tabela — achado Codex P1: `CREATE TABLE IF NOT EXISTS` nunca altera uma
// tabela que já existe, então um ledger criado por uma versão anterior do agente (o caso
// normal em produção: o binário é atualizado, mas o arquivo .db no disco é o mesmo) ficaria
// sem `recovery_acknowledged` — toda query que a referencia falharia com "no such column",
// quebrando reconciliação e leitura de runs logo depois de um upgrade do PRÓPRIO agente.
// `addColumnIfMissing` consulta o schema real via PRAGMA table_info antes de alterar, nunca
// tenta um ALTER TABLE ADD COLUMN "às cegas" (SQLite recusaria com "duplicate column name" se
// a coluna já existisse — o que aconteceria em TODA abertura de um ledger já migrado).
func migrateSchema(db *sql.DB) error {
	return addColumnIfMissing(db, "update_runs", "recovery_acknowledged", "INTEGER NOT NULL DEFAULT 0")
}

// addColumnIfMissing inspeciona e, se necessário, altera o schema DENTRO DE UMA TRANSAÇÃO —
// achado Codex P2: a versão anterior fazia a leitura (PRAGMA table_info) e a escrita (ALTER
// TABLE) como duas operações soltas no *sql.DB; dois PROCESSOS abrindo o mesmo ledger legado
// ao mesmo tempo (ex. o agente e `updatectl` no primeiro boot depois de um upgrade do próprio
// binário) podiam ambos ver a coluna ausente antes de qualquer um rodar o ALTER — o primeiro
// tinha sucesso, o segundo falhava com "duplicate column name", fazendo OpenLedger recusar um
// banco que na verdade JÁ estava migrado corretamente. A transação IMMEDIATE (DSN
// `_txlock=immediate`, ver OpenLedger) serializa com qualquer outra transação concorrente via
// o próprio lock do SQLite — o segundo processo espera (busy_timeout) a transação do primeiro
// COMMITAR e então vê a coluna já presente, nunca tenta o ALTER duplicado.
func addColumnIfMissing(db *sql.DB, table, column, ddl string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("iniciar transação de migração de schema: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("inspecionar schema de %s: %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dfltValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("ler coluna de %s: %w", table, err)
		}
		if name == column {
			found = true
			break
		}
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return fmt.Errorf("iterar colunas de %s: %w", table, rowsErr)
	}
	if found {
		return nil // já migrado, nada a fazer — nem o ALTER precisa rodar
	}
	if _, err := tx.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, ddl)); err != nil {
		return fmt.Errorf("adicionar coluna %s.%s: %w", table, column, err)
	}
	return tx.Commit()
}

func (l *Ledger) Close() error { return l.db.Close() }

// LockHandle é a prova de posse do lock de uma instalação, devolvida por AcquireLock. Toda
// operação de mutação (TransitionRun, RecordCheckpoint*) exige um LockHandle e falha com
// ErrFencingLost se o token nele não bater mais com o gravado no banco.
type LockHandle struct {
	InstallationID string
	FencingToken   int64
	Holder         string
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// AcquireLock tenta adquirir (ou renovar, se holder já é o dono atual) o lock de uma
// instalação. lease é por quanto tempo sem heartbeat o lock é considerado abandonado — um
// holder diferente pode então roubá-lo, incrementando fencing_token (o holder anterior, se
// ainda estiver vivo mas particionado/lento, perde qualquer operação em andamento na próxima
// chamada que apresentar o token antigo).
func (l *Ledger) AcquireLock(ctx context.Context, installationID, holder string, lease time.Duration) (*LockHandle, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("agent: iniciar transação de lock: %w", err)
	}
	defer tx.Rollback()

	var existingHolder string
	var existingToken int64
	var heartbeatAt string
	var existingLeaseMs int64
	var released int
	err = tx.QueryRowContext(ctx,
		`SELECT holder, fencing_token, heartbeat_at, lease_ms, released FROM agent_lock WHERE installation_id = ?`, installationID,
	).Scan(&existingHolder, &existingToken, &heartbeatAt, &existingLeaseMs, &released)

	now := time.Now().UTC()
	if errors.Is(err, sql.ErrNoRows) {
		// Nunca houve lock para esta instalação: primeira aquisição, token 1.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agent_lock(installation_id, fencing_token, holder, acquired_at, heartbeat_at, lease_ms, released)
			 VALUES (?, 1, ?, ?, ?, ?, 0)`,
			installationID, holder, nowRFC3339(), nowRFC3339(), lease.Milliseconds(),
		); err != nil {
			return nil, fmt.Errorf("agent: criar lock inicial: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("agent: commit do lock inicial: %w", err)
		}
		return &LockHandle{InstallationID: installationID, FencingToken: 1, Holder: holder}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("agent: ler lock existente: %w", err)
	}

	hbAt, perr := time.Parse(time.RFC3339Nano, heartbeatAt)
	if perr != nil {
		return nil, fmt.Errorf("agent: heartbeat_at corrompido no ledger: %w", perr)
	}
	// Liberado explicitamente (released=1) conta como expirado incondicionalmente — nunca
	// espera o lease original vencer. `released` é a fonte de verdade explícita de
	// "este lock não está mais em uso de propósito"; a comparação por heartbeat_at/lease só
	// cobre o caso de abandono por crash (sem Release explícito).
	expired := released == 1 || now.Sub(hbAt) > time.Duration(existingLeaseMs)*time.Millisecond

	if existingHolder != holder && !expired {
		// Outro holder, lock ainda válido: recusa — nunca duas atualizações simultâneas da
		// mesma instalação (especificação §6.1).
		return nil, ErrLockHeld
	}

	// Chegamos aqui porque (a) é o MESMO holder pedindo de novo — uma reconexão/retomada, cujo
	// handle anterior (se ainda existir em algum lugar, por exemplo um goroutine zumbi) deve
	// se tornar obsoleto a partir de agora, só o handle retornado por ESTA chamada é válido —
	// ou (b) outro holder expirado/liberado está sendo substituído. Em AMBOS os casos o
	// fencing_token SEMPRE avança, nunca é reaproveitado (achado Codex: tratar "mesmo holder"
	// como uma renovação sem custo do token permitia um handle obtido antes de um
	// ReleaseLock+reaquisição continuar válido). lease_ms também é sempre atualizado para o
	// valor desta chamada — renovar com um lease diferente do original passa a valer de
	// verdade, não fica silenciosamente preso ao lease da aquisição anterior. released volta
	// para 0: este É o novo detentor ativo.
	newToken := existingToken + 1
	if _, err := tx.ExecContext(ctx,
		`UPDATE agent_lock SET fencing_token = ?, holder = ?, acquired_at = ?, heartbeat_at = ?, lease_ms = ?, released = 0
		 WHERE installation_id = ?`,
		newToken, holder, nowRFC3339(), nowRFC3339(), lease.Milliseconds(), installationID,
	); err != nil {
		return nil, fmt.Errorf("agent: roubar lock expirado: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("agent: commit do roubo de lock: %w", err)
	}
	return &LockHandle{InstallationID: installationID, FencingToken: newToken, Holder: holder}, nil
}

// Heartbeat renova o lease. Exige released = 0: um handle que já foi liberado (seja pelo
// próprio chamador via ReleaseLock, seja por ter perdido o fencing) nunca pode "ressuscitar" o
// lock através de um heartbeat concorrente — achado Codex: antes desta checagem, uma
// goroutine de heartbeat ainda rodando em loop depois de ReleaseLock já ter sido chamado
// conseguia reescrever heartbeat_at para "agora" e impedir outro processo de assumir, mesmo
// com o lock formalmente liberado.
func (l *Ledger) Heartbeat(ctx context.Context, h *LockHandle) error {
	res, err := l.db.ExecContext(ctx,
		`UPDATE agent_lock SET heartbeat_at = ? WHERE installation_id = ? AND holder = ? AND fencing_token = ? AND released = 0`,
		nowRFC3339(), h.InstallationID, h.Holder, h.FencingToken,
	)
	if err != nil {
		return fmt.Errorf("agent: heartbeat: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrFencingLost
	}
	return nil
}

// ReleaseLock libera um lock detido de propósito (encerramento limpo do agente). Marca
// released=1 explicitamente — não é mais uma heurística baseada num heartbeat_at antigo, que
// um Heartbeat concorrente podia sobrescrever (achado Codex). Não falha se o fencing já foi
// perdido: liberar algo que já não se detém mais é inofensivo (noop).
func (l *Ledger) ReleaseLock(ctx context.Context, h *LockHandle) error {
	_, err := l.db.ExecContext(ctx,
		`UPDATE agent_lock SET released = 1 WHERE installation_id = ? AND holder = ? AND fencing_token = ?`,
		h.InstallationID, h.Holder, h.FencingToken,
	)
	if err != nil {
		return fmt.Errorf("agent: liberar lock: %w", err)
	}
	return nil
}

// checkFencing confirma, DENTRO da transação de uma mutação, que h ainda é o detentor
// legítimo do lock (released=0 e o token bate) — nunca confia apenas no valor em memória de h
// depois de obtido uma vez.
func checkFencing(ctx context.Context, tx *sql.Tx, h *LockHandle) error {
	var current int64
	var released int
	err := tx.QueryRowContext(ctx,
		`SELECT fencing_token, released FROM agent_lock WHERE installation_id = ? AND holder = ?`,
		h.InstallationID, h.Holder,
	).Scan(&current, &released)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (current != h.FencingToken || released == 1)) {
		return ErrFencingLost
	}
	if err != nil {
		return fmt.Errorf("agent: verificar fencing: %w", err)
	}
	return nil
}

// RunRecord é o estado persistido de um platform_update_run no ledger local do agente.
type RunRecord struct {
	RunID          string
	InstallationID string
	IdempotencyKey string
	State          domain.RunState
	FencingToken   int64
	ManifestJSON   string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// RecoveryAcknowledged: true só depois de AcknowledgeRecovery — um operador confirmou,
	// fora de banda (updatectl, runbook), que investigou um RECOVERY_REQUIRED e a instalação
	// está segura para seguir recebendo novos runs. Achado Codex: sem isto, o bloqueio de
	// "não processar mais runs desta instalação" só durava a chamada de Reconcile em que o
	// RECOVERY_REQUIRED aconteceu — a PRÓXIMA chamada já não veria mais esse run (terminal,
	// fora de ActiveRuns) e seguiria para runs novos sem nenhuma intervenção manual ter
	// ocorrido de verdade.
	RecoveryAcknowledged bool
}

// CreateRun cria um novo run OU, se já existe um com a mesma (installation_id,
// idempotency_key), retorna o EXISTENTE sem criar duplicata (especificação §6.1 "Deduplicar
// pedido com idempotency_key" + acceptance matrix "Dois administradores clicam atualizar
// simultaneamente -> uma execução, outra recusada/associada via idempotência").
func (l *Ledger) CreateRun(ctx context.Context, h *LockHandle, runID, idempotencyKey, manifestJSON string, initial domain.RunState) (*RunRecord, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("agent: iniciar transação de criação de run: %w", err)
	}
	defer tx.Rollback()
	if err := checkFencing(ctx, tx, h); err != nil {
		return nil, err
	}

	var existing RunRecord
	var state, createdAt, updatedAt string
	var recoveryAck int
	err = tx.QueryRowContext(ctx,
		`SELECT run_id, state, fencing_token, manifest_json, created_at, updated_at, recovery_acknowledged FROM update_runs
		 WHERE installation_id = ? AND idempotency_key = ?`,
		h.InstallationID, idempotencyKey,
	).Scan(&existing.RunID, &state, &existing.FencingToken, &existing.ManifestJSON, &createdAt, &updatedAt, &recoveryAck)
	if err == nil {
		existing.InstallationID = h.InstallationID
		existing.IdempotencyKey = idempotencyKey
		existing.State = domain.RunState(state)
		existing.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		existing.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		existing.RecoveryAcknowledged = recoveryAck != 0
		return &existing, nil // dedup: run já existe, nunca duplicar
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("agent: verificar run existente: %w", err)
	}

	now := nowRFC3339()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO update_runs(run_id, installation_id, idempotency_key, state, fencing_token, manifest_json, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		runID, h.InstallationID, idempotencyKey, string(initial), h.FencingToken, manifestJSON, now, now,
	); err != nil {
		return nil, fmt.Errorf("agent: inserir novo run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("agent: commit da criação de run: %w", err)
	}
	createdAtT, _ := time.Parse(time.RFC3339Nano, now)
	return &RunRecord{
		RunID: runID, InstallationID: h.InstallationID, IdempotencyKey: idempotencyKey,
		State: initial, FencingToken: h.FencingToken, ManifestJSON: manifestJSON,
		CreatedAt: createdAtT, UpdatedAt: createdAtT,
	}, nil
}

// TransitionRun move um run de seu estado atual para `to`, validando a transição contra a
// ÚNICA tabela de verdade (domain.ValidateTransition, a mesma usada pelo backend F6 e pela UI
// F7) e exigindo o fencing_token correto. Nunca escreve um estado sem checar as duas coisas.
func (l *Ledger) TransitionRun(ctx context.Context, h *LockHandle, runID string, to domain.RunState) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("agent: iniciar transação de transição: %w", err)
	}
	defer tx.Rollback()
	if err := checkFencing(ctx, tx, h); err != nil {
		return err
	}

	// Achado Codex: um handle válido só para a instalação A não pode transicionar um run que
	// pertence à instalação B — a query (e o UPDATE abaixo) precisam exigir
	// installation_id = h.InstallationID, nunca confiar só em run_id ser único o bastante.
	var from string
	if err := tx.QueryRowContext(ctx,
		`SELECT state FROM update_runs WHERE run_id = ? AND installation_id = ?`, runID, h.InstallationID,
	).Scan(&from); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("agent: run %s não encontrado para a instalação %s", runID, h.InstallationID)
		}
		return fmt.Errorf("agent: ler estado atual do run: %w", err)
	}
	if err := domain.ValidateTransition(domain.RunState(from), to); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE update_runs SET state = ?, updated_at = ? WHERE run_id = ? AND installation_id = ?`,
		string(to), nowRFC3339(), runID, h.InstallationID,
	); err != nil {
		return fmt.Errorf("agent: gravar transição: %w", err)
	}
	return tx.Commit()
}

// GetRun lê o estado atual de um run.
func (l *Ledger) GetRun(ctx context.Context, runID string) (*RunRecord, error) {
	var r RunRecord
	var state, createdAt, updatedAt string
	var recoveryAck int
	err := l.db.QueryRowContext(ctx,
		`SELECT run_id, installation_id, idempotency_key, state, fencing_token, manifest_json, created_at, updated_at, recovery_acknowledged
		 FROM update_runs WHERE run_id = ?`, runID,
	).Scan(&r.RunID, &r.InstallationID, &r.IdempotencyKey, &state, &r.FencingToken, &r.ManifestJSON, &createdAt, &updatedAt, &recoveryAck)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("agent: run %s não encontrado", runID)
		}
		return nil, fmt.Errorf("agent: ler run: %w", err)
	}
	r.State = domain.RunState(state)
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	r.RecoveryAcknowledged = recoveryAck != 0
	return &r, nil
}

// HasUnresolvedRecoveryRequired reporta se ALGUM run desta instalação está em
// RECOVERY_REQUIRED sem um AcknowledgeRecovery correspondente — achado Codex P1: o gate que
// impede novos runs de avançar enquanto uma instalação tem uma recuperação pendente precisa
// sobreviver a uma reconciliação subsequente (um novo processo do agente, ou a mesma chamada
// de Reconcile em outro ciclo), não só ao loop atual. RECOVERY_REQUIRED é terminal
// (domain.IsTerminal), então ActiveRuns por si só NUNCA mostraria este run de novo — este é o
// único lugar que ainda sabe que ele existe e não foi resolvido.
func (l *Ledger) HasUnresolvedRecoveryRequired(ctx context.Context, installationID string) (bool, error) {
	var n int
	err := l.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM update_runs WHERE installation_id = ? AND state = ? AND recovery_acknowledged = 0`,
		installationID, string(domain.StateRecoveryRequired),
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("agent: verificar recuperação pendente: %w", err)
	}
	return n > 0, nil
}

// RecordAgentHeartbeat (F9, monitoramento "status do agente") grava que o daemon acabou de
// completar um ciclo de poll — chamado por agentconfig.ReconcileOnce a CADA ciclo, mesmo sem
// nenhum run ativo (nunca só quando há trabalho a fazer, ou o sinal deixaria de distinguir
// "nada a fazer" de "processo morto"). Idempotente/UPSERT: nunca falha numa segunda chamada
// para a mesma instalação. Nunca exige um LockHandle: não compete pelo recurso que agent_lock
// protege, só registra um fato de liveness independente.
func (l *Ledger) RecordAgentHeartbeat(ctx context.Context, installationID, holder string) error {
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO agent_heartbeat (installation_id, holder, last_poll_at) VALUES (?, ?, ?)
		 ON CONFLICT(installation_id) DO UPDATE SET holder = excluded.holder, last_poll_at = excluded.last_poll_at`,
		installationID, holder, nowRFC3339())
	if err != nil {
		return fmt.Errorf("agent: gravar heartbeat do agente: %w", err)
	}
	return nil
}

// RecordObservedDigests (F9, monitoramento "versão real/digest divergente", achado Codex
// [P2]) grava o snapshot REAL mais recente do Docker (ComposeAdapter.CurrentSnapshot,
// serializado como JSON de map[Unit]string) — nem o ledger (histórico de runs) nem o Postgres
// (platform_installations) observam containers de verdade por si só; sem isto, um container
// substituído manualmente, ou uma implantação que falhou deixando outra imagem rodando, nunca
// apareceria em nenhum sinal de monitoramento. Chamado a cada ciclo de reconcile — NUNCA
// atualiza last_poll_at (responsabilidade exclusiva de RecordAgentHeartbeat, num ticker
// independente — ver o comentário em agent_heartbeat): as duas escritas têm cadências
// diferentes de propósito.
func (l *Ledger) RecordObservedDigests(ctx context.Context, installationID, holder, digestsJSON string) error {
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO agent_heartbeat (installation_id, holder, last_poll_at, observed_digests, observed_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(installation_id) DO UPDATE SET holder = excluded.holder, observed_digests = excluded.observed_digests, observed_at = excluded.observed_at`,
		installationID, holder, nowRFC3339(), digestsJSON, nowRFC3339())
	if err != nil {
		return fmt.Errorf("agent: gravar digests observados do agente: %w", err)
	}
	return nil
}

// AgentHeartbeatInfo (F9, monitoramento "status do agente"/"versão real/digest divergente") é
// o que AgentHeartbeat devolve — os dois sinais gravados por cadências independentes
// (RecordAgentHeartbeat/RecordObservedDigests) sobre a MESMA linha por instalação.
type AgentHeartbeatInfo struct {
	Holder          string
	LastPollAt      time.Time
	ObservedDigests string // JSON de map[Unit]string — "{}" se nenhum snapshot foi gravado ainda
	ObservedAt      time.Time
}

// AgentHeartbeat (F9) devolve o último estado registrado para installationID. found=false
// (sem erro) significa que este daemon nunca completou um ciclo de poll para esta instalação —
// ou porque o processo nunca rodou, ou porque está rodando uma versão anterior a esta
// funcionalidade; o chamador decide como tratar isso (ex. "status desconhecido", nunca
// "daemon confirmado morto"). ObservedAt pode ser zero mesmo com found=true: um heartbeat de
// liveness já aconteceu, mas nenhum snapshot de Docker foi gravado ainda (cadências
// independentes).
func (l *Ledger) AgentHeartbeat(ctx context.Context, installationID string) (info AgentHeartbeatInfo, found bool, err error) {
	var lastPollRaw string
	var observedAtRaw *string
	err = l.db.QueryRowContext(ctx, `SELECT holder, last_poll_at, observed_digests, observed_at FROM agent_heartbeat WHERE installation_id = ?`, installationID).
		Scan(&info.Holder, &lastPollRaw, &info.ObservedDigests, &observedAtRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentHeartbeatInfo{}, false, nil
	}
	if err != nil {
		return AgentHeartbeatInfo{}, false, fmt.Errorf("agent: ler heartbeat do agente: %w", err)
	}
	info.LastPollAt, err = time.Parse(time.RFC3339Nano, lastPollRaw)
	if err != nil {
		return AgentHeartbeatInfo{}, false, fmt.Errorf("agent: heartbeat do agente com timestamp ilegível: %w", err)
	}
	if observedAtRaw != nil {
		info.ObservedAt, err = time.Parse(time.RFC3339Nano, *observedAtRaw)
		if err != nil {
			return AgentHeartbeatInfo{}, false, fmt.Errorf("agent: observed_at do agente com timestamp ilegível: %w", err)
		}
	}
	return info, true, nil
}

// RevokeRelease (F9) grava, no ledger LOCAL, que release_id nunca deve ser aplicado por este
// agente — espelho do que updatemgr.Service.RevokeRelease já gravou no Postgres (nesta mesma
// chamada, sempre Postgres primeiro — ver o comentário em Service.RevokeRelease). Idempotente
// (INSERT OR REPLACE): revogar de novo o MESMO release_id só atualiza reason/revoked_at, nunca
// falha — mas RevokeRelease do Service já impede uma segunda revogação de verdade (constraint
// de chave primária na tabela do Postgres), então na prática isto só reexecuta com os MESMOS
// valores. Nunca exige um LockHandle: não compete pelo recurso que agent_lock protege (quais
// mutações de Docker/migration estão em andamento), só registra um fato independente.
func (l *Ledger) RevokeRelease(ctx context.Context, releaseID, reason string) error {
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO revoked_releases (release_id, reason, revoked_at) VALUES (?, ?, ?)
		 ON CONFLICT(release_id) DO UPDATE SET reason = excluded.reason, revoked_at = excluded.revoked_at`,
		releaseID, reason, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("agent: gravar revogação local de %s: %w", releaseID, err)
	}
	return nil
}

// IsReleaseRevoked (F9) consulta o ledger LOCAL — nunca o Postgres — porque o agente nunca
// depende da API/Postgres estarem disponíveis para decidir o próximo passo seguro (comentário
// do pacote). Chamada por Executor.Execute ANTES de qualquer step perigoso, para que uma
// release revogada DEPOIS do despacho (StartRun) mas ANTES do agente de fato aplicar (poll
// interval, daemon reiniciado) seja recusada mesmo assim — signing.yaml
// "anti_replay.revalidate_before_apply" não distingue "revalidar no despacho" de "revalidar no
// apply real": são o MESMO requisito, e o apply real é aqui, não em StartRun.
func (l *Ledger) IsReleaseRevoked(ctx context.Context, releaseID string) (revoked bool, reason string, err error) {
	err = l.db.QueryRowContext(ctx, `SELECT reason FROM revoked_releases WHERE release_id = ?`, releaseID).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("agent: verificar revogação local de %s: %w", releaseID, err)
	}
	return true, reason, nil
}

// MarkRevocationSynced (F9, achado Codex [P1] rodada 7) avança o relógio de revocation_sync
// para agora — chamado SÓ depois que updatemgr confirma, com sucesso, que refletiu neste
// ledger o estado ATUAL de revogações do Postgres (nenhuma pendência de entrega, ou toda
// pendência entregue com sucesso). Nunca chamado pelo agente em si (que não tem como saber se
// está "em dia" com o Postgres — só o processo da API, que de fato consultou o Postgres,
// pode avançar este relógio).
func (l *Ledger) MarkRevocationSynced(ctx context.Context) error {
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO revocation_sync (id, synced_at, standalone) VALUES (1, ?, 0) ON CONFLICT(id) DO UPDATE SET synced_at = excluded.synced_at`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("agent: marcar sincronização de revogação: %w", err)
	}
	return nil
}

// MarkStandalone (F9, achado Codex [P1] rodada 10 da fatia de retenção/GC) registra, UMA
// ÚNICA VEZ e por decisão EXPLÍCITA do operador (OMNIRA_AGENT_STANDALONE=true, chamado pelo
// daemon no startup — ver apps/update-agent), que esta instalação NUNCA terá uma API
// sincronizando revogações, e portanto "nenhuma linha em revocation_sync" para ela é
// legitimamente "fresco para sempre", nunca "obsoleto". Idempotente; nunca sobrescreve
// synced_at de uma sincronização real que já tenha acontecido antes (ex. a flag foi
// adicionada depois de a instalação já ter sido gerenciada por um tempo).
func (l *Ledger) MarkStandalone(ctx context.Context) error {
	_, err := l.db.ExecContext(ctx,
		`INSERT INTO revocation_sync (id, synced_at, standalone) VALUES (1, ?, 1) ON CONFLICT(id) DO UPDATE SET standalone = 1`,
		time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("agent: marcar instalação standalone: %w", err)
	}
	return nil
}

// RevocationSyncFresh (F9, achados Codex [P1] rodadas 7 e 10) responde se a última
// sincronização bem-sucedida com o Postgres (MarkRevocationSynced) é mais recente que
// maxAge — signing.yaml anti_replay.revocation.max_cache_age_seconds (300s) — OU se esta
// instalação foi marcada standalone de propósito (MarkStandalone), caso em que é sempre
// fresca. "Não revogada" só pode ser confiada enquanto este relógio estiver fresco: se a API
// ficou fora do ar por mais tempo do que isso, uma revogação pode ter acontecido no Postgres
// sem NUNCA ter sido verificada por este ledger — IsReleaseRevoked responderia "false" por
// simples ausência de dados, nunca por confirmação real.
//
// NENHUMA linha (achado Codex [P1] rodada 10, corrigindo a rodada 9) agora é FAIL-CLOSED —
// nunca mais "fresco por decreto". A rodada 9 tratava a ausência de linha como fresca para
// nunca bloquear uma instalação standalone — mas isso também fazia uma instalação GERENCIADA
// (API habilitada em algum lugar) cuja PRIMEIRA sincronização simplesmente ainda não
// aconteceu (ex. API fora do ar durante o bootstrap) ficar "confiável" indefinidamente, sem
// NUNCA ter confirmado o estado real do Postgres. Só MarkStandalone (decisão explícita do
// operador) pode tornar "nenhuma linha"/"nunca sincronizado" fresco; sem essa marca, é
// obsoleto desde o início — e o agente/updatectl recusam operações perigosas até a primeira
// sincronização real acontecer (ou até o operador confirmar standalone de propósito).
// lastSync é devolvido mesmo quando fresh=false, só para contexto em mensagens de erro/log —
// o chamador nunca decide nada a partir dele diretamente.
func (l *Ledger) RevocationSyncFresh(ctx context.Context, maxAge time.Duration) (fresh bool, lastSync time.Time, err error) {
	var raw string
	var standalone bool
	err = l.db.QueryRowContext(ctx, `SELECT synced_at, standalone FROM revocation_sync WHERE id = 1`).Scan(&raw, &standalone)
	if errors.Is(err, sql.ErrNoRows) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, fmt.Errorf("agent: ler sincronização de revogação: %w", err)
	}
	lastSync, err = time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return false, time.Time{}, fmt.Errorf("agent: sincronização de revogação com timestamp ilegível: %w", err)
	}
	if standalone {
		return true, lastSync, nil
	}
	return time.Since(lastSync) <= maxAge, lastSync, nil
}

// AcknowledgeRecovery marca um run RECOVERY_REQUIRED como resolvido fora de banda — a única
// forma de destravar novos runs da mesma instalação depois disso (ver
// HasUnresolvedRecoveryRequired). Nunca automático: só um operador confirmando via
// `updatectl`/runbook que investigou, restaurou e verificou os dados chama isto. Exige que o
// run exista, pertença à instalação do handle e esteja de fato em RECOVERY_REQUIRED — nunca
// "reconhece" um run em qualquer outro estado (preveniria usar isto como um atalho para
// destravar algo que nunca foi sinalizado).
func (l *Ledger) AcknowledgeRecovery(ctx context.Context, h *LockHandle, runID string) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("agent: iniciar transação de reconhecimento de recuperação: %w", err)
	}
	defer tx.Rollback()
	if err := checkFencing(ctx, tx, h); err != nil {
		return err
	}
	var state string
	if err := tx.QueryRowContext(ctx,
		`SELECT state FROM update_runs WHERE run_id = ? AND installation_id = ?`, runID, h.InstallationID,
	).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("agent: run %s não encontrado para a instalação %s", runID, h.InstallationID)
		}
		return fmt.Errorf("agent: ler estado do run antes de reconhecer recuperação: %w", err)
	}
	if domain.RunState(state) != domain.StateRecoveryRequired {
		return fmt.Errorf("agent: run %s não está em RECOVERY_REQUIRED (estado atual: %s) — nada para reconhecer", runID, state)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE update_runs SET recovery_acknowledged = 1, updated_at = ? WHERE run_id = ? AND installation_id = ?`,
		nowRFC3339(), runID, h.InstallationID,
	); err != nil {
		return fmt.Errorf("agent: gravar reconhecimento de recuperação: %w", err)
	}
	return tx.Commit()
}

// ActiveRuns lista os runs de uma instalação cujo estado NÃO é terminal — é isto que a
// reconciliação pós-crash do agente consulta ao iniciar (especificação §6.1).
func (l *Ledger) ActiveRuns(ctx context.Context, installationID string) ([]RunRecord, error) {
	all, err := l.AllRuns(ctx, installationID)
	if err != nil {
		return nil, err
	}
	var out []RunRecord
	for _, r := range all {
		if !domain.IsTerminal(r.State) {
			out = append(out, r)
		}
	}
	return out, nil
}

// AllRuns lista TODOS os runs de uma instalação, inclusive os terminais (SUCCEEDED,
// ROLLED_BACK, RECOVERY_REQUIRED, CANCELLED, BLOCKED) — usado por `updatectl status` (dar ao
// operador o histórico completo, não só o que ainda está em andamento) e por `updatectl
// rollback` (achar o snapshot do run anterior mais recente, mesmo que ele já tenha
// terminado). ActiveRuns é um filtro sobre isto, nunca uma query separada.
func (l *Ledger) AllRuns(ctx context.Context, installationID string) ([]RunRecord, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT run_id, installation_id, idempotency_key, state, fencing_token, manifest_json, created_at, updated_at, recovery_acknowledged
		 FROM update_runs WHERE installation_id = ? ORDER BY created_at ASC`, installationID,
	)
	if err != nil {
		return nil, fmt.Errorf("agent: listar runs: %w", err)
	}
	defer rows.Close()
	var out []RunRecord
	for rows.Next() {
		var r RunRecord
		var state, createdAt, updatedAt string
		var recoveryAck int
		if err := rows.Scan(&r.RunID, &r.InstallationID, &r.IdempotencyKey, &state, &r.FencingToken, &r.ManifestJSON, &createdAt, &updatedAt, &recoveryAck); err != nil {
			return nil, fmt.Errorf("agent: ler linha de run: %w", err)
		}
		r.State = domain.RunState(state)
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		r.RecoveryAcknowledged = recoveryAck != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// Checkpoint é um ponto do journal: um passo perigoso (backup, pull, migrate, deploy de uma
// unit) começado ou concluído. O agente consulta isto ao reiniciar para nunca repetir um passo
// cegamente — um "started" sem "completed" correspondente significa reconciliar com o estado
// REAL observado (docker compose ps, migrate-sql.sh status), nunca re-executar por suposição.
type Checkpoint struct {
	RunID      string
	Step       string
	Status     string // started | completed | failed
	Detail     string
	RecordedAt time.Time
}

func (l *Ledger) recordCheckpoint(ctx context.Context, h *LockHandle, runID, step, status, detail string) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("agent: iniciar transação de checkpoint: %w", err)
	}
	defer tx.Rollback()
	if err := checkFencing(ctx, tx, h); err != nil {
		return err
	}
	// Mesmo achado Codex de TransitionRun: sem isto, um handle válido só para a instalação A
	// podia gravar checkpoints no run de uma instalação B.
	var belongsToInstallation int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM update_runs WHERE run_id = ? AND installation_id = ?`, runID, h.InstallationID,
	).Scan(&belongsToInstallation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("agent: run %s não encontrado para a instalação %s", runID, h.InstallationID)
		}
		return fmt.Errorf("agent: verificar run antes de gravar checkpoint: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO run_checkpoints(run_id, step, status, detail, recorded_at) VALUES (?, ?, ?, ?, ?)`,
		runID, step, status, detail, nowRFC3339(),
	); err != nil {
		return fmt.Errorf("agent: gravar checkpoint: %w", err)
	}
	return tx.Commit()
}

func (l *Ledger) RecordCheckpointStart(ctx context.Context, h *LockHandle, runID, step string) error {
	return l.recordCheckpoint(ctx, h, runID, step, "started", "")
}
func (l *Ledger) RecordCheckpointDone(ctx context.Context, h *LockHandle, runID, step, detail string) error {
	return l.recordCheckpoint(ctx, h, runID, step, "completed", detail)
}
func (l *Ledger) RecordCheckpointFailed(ctx context.Context, h *LockHandle, runID, step, detail string) error {
	return l.recordCheckpoint(ctx, h, runID, step, "failed", detail)
}

// StepStatus devolve o status do checkpoint MAIS RECENTE de `step` dentro de `runID` ("" se o
// passo nunca foi tentado). Decidir "pode pular este passo" é do chamador (reconcile.go), não
// deste método — aqui só se expõe o fato puro do ledger.
func (l *Ledger) StepStatus(ctx context.Context, runID, step string) (string, error) {
	var status string
	err := l.db.QueryRowContext(ctx,
		`SELECT status FROM run_checkpoints WHERE run_id = ? AND step = ? ORDER BY id DESC LIMIT 1`,
		runID, step,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("agent: ler status de checkpoint: %w", err)
	}
	return status, nil
}

// Checkpoints lista o journal completo de um run, em ordem — usado por `updatectl status`
// para dar ao operador a trilha exata do que já aconteceu.
func (l *Ledger) Checkpoints(ctx context.Context, runID string) ([]Checkpoint, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT run_id, step, status, detail, recorded_at FROM run_checkpoints WHERE run_id = ? ORDER BY id ASC`, runID,
	)
	if err != nil {
		return nil, fmt.Errorf("agent: listar checkpoints: %w", err)
	}
	defer rows.Close()
	var out []Checkpoint
	for rows.Next() {
		var c Checkpoint
		var recordedAt string
		if err := rows.Scan(&c.RunID, &c.Step, &c.Status, &c.Detail, &recordedAt); err != nil {
			return nil, fmt.Errorf("agent: ler linha de checkpoint: %w", err)
		}
		c.RecordedAt, _ = time.Parse(time.RFC3339Nano, recordedAt)
		out = append(out, c)
	}
	return out, rows.Err()
}
