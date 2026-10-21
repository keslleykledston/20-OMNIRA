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
  run_id          TEXT PRIMARY KEY,
  installation_id TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  state           TEXT NOT NULL,
  fencing_token   INTEGER NOT NULL,
  manifest_json   TEXT NOT NULL,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
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
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("agent: criar schema do ledger: %w", err)
	}
	return &Ledger{db: db}, nil
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
	err = tx.QueryRowContext(ctx,
		`SELECT run_id, state, fencing_token, manifest_json, created_at, updated_at FROM update_runs
		 WHERE installation_id = ? AND idempotency_key = ?`,
		h.InstallationID, idempotencyKey,
	).Scan(&existing.RunID, &state, &existing.FencingToken, &existing.ManifestJSON, &createdAt, &updatedAt)
	if err == nil {
		existing.InstallationID = h.InstallationID
		existing.IdempotencyKey = idempotencyKey
		existing.State = domain.RunState(state)
		existing.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		existing.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
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
	err := l.db.QueryRowContext(ctx,
		`SELECT run_id, installation_id, idempotency_key, state, fencing_token, manifest_json, created_at, updated_at
		 FROM update_runs WHERE run_id = ?`, runID,
	).Scan(&r.RunID, &r.InstallationID, &r.IdempotencyKey, &state, &r.FencingToken, &r.ManifestJSON, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("agent: run %s não encontrado", runID)
		}
		return nil, fmt.Errorf("agent: ler run: %w", err)
	}
	r.State = domain.RunState(state)
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
	return &r, nil
}

// ActiveRuns lista os runs de uma instalação cujo estado NÃO é terminal — é isto que a
// reconciliação pós-crash do agente consulta ao iniciar (especificação §6.1).
func (l *Ledger) ActiveRuns(ctx context.Context, installationID string) ([]RunRecord, error) {
	rows, err := l.db.QueryContext(ctx,
		`SELECT run_id, installation_id, idempotency_key, state, fencing_token, manifest_json, created_at, updated_at
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
		if err := rows.Scan(&r.RunID, &r.InstallationID, &r.IdempotencyKey, &state, &r.FencingToken, &r.ManifestJSON, &createdAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("agent: ler linha de run: %w", err)
		}
		r.State = domain.RunState(state)
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
		r.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedAt)
		if !domain.IsTerminal(r.State) {
			out = append(out, r)
		}
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
