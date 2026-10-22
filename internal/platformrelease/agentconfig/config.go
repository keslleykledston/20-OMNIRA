// Package agentconfig monta, a partir de flags + variáveis de ambiente OMNIRA_AGENT_*, a
// configuração compartilhada pelo daemon (apps/update-agent) e pela CLI local
// (apps/updatectl) — os dois únicos binários fora dos testes de internal/platformrelease/agent
// que de fato montam um agent.Executor real contra Docker/Postgres/disco. Mantém esses dois
// binários pequenos e idênticos na forma como se conectam ao mundo real, em vez de duplicar a
// montagem em cada um.
package agentconfig

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/omnira/omnira/internal/platformrelease/agent"
	"github.com/omnira/omnira/internal/platformrelease/domain"
)

// Config é tudo que o agente precisa para operar — nenhum campo depende de uma API HTTP
// existir (especificação §6.1: o agente nunca depende do backend/Postgres da aplicação estar
// disponível para decidir seu próximo passo seguro, só deste ledger local e do Docker real).
type Config struct {
	InstallationID string
	Holder         string
	LedgerPath     string
	Lease          time.Duration
	PollInterval   time.Duration
	HealthTimeout  time.Duration
	ObserveWindow  time.Duration
	// Standalone (F9, achado Codex [P1] rodada 10 da fatia de retenção/GC): decisão
	// EXPLÍCITA do operador de que esta instalação NUNCA terá uma Update Manager API
	// sincronizando revogações — nunca inferida pela simples ausência de sincronização
	// (que passaria a ser indistinguível de uma instalação GERENCIADA cuja primeira
	// sincronização simplesmente ainda não aconteceu). Ver Ledger.MarkStandalone/
	// RevocationSyncFresh.
	Standalone bool

	ComposeBaseFiles   colonSeparatedList
	ComposeOverrideDir string
	ComposeProject     string
	ComposeSudo        bool

	MigrateScript string
	MigrationsDir string
	DatabaseURL   string

	BackupScript string
	BackupDir    string
	DBName       string
	PGContainer  string
	PGOwner      string
}

// colonSeparatedList é um flag.Value para uma lista ORDENADA separada por ':' — usado para
// --compose-base-files, onde a ORDEM importa (a última entrada vence no Docker Compose) e
// uma flag repetida (como stringSliceFlag em apps/releasectl) seria mais verbosa para o caso
// comum de 2-3 arquivos fixos.
type colonSeparatedList []string

func (l *colonSeparatedList) String() string { return strings.Join(*l, ":") }
func (l *colonSeparatedList) Set(v string) error {
	*l = strings.Split(v, ":")
	return nil
}

// FlagSet registra todas as flags de Config em `fs`, com defaults lidos de
// OMNIRA_AGENT_* quando a variável existir — uma flag explícita sempre vence o env. Usado
// por AMBOS os binários para que `--help` mostre exatamente as mesmas opções nos dois.
func FlagSet(fs *flag.FlagSet) *Config {
	c := &Config{}
	fs.StringVar(&c.InstallationID, "installation-id", envStr("OMNIRA_AGENT_INSTALLATION_ID", ""),
		"ID da instalação que este agente gerencia (ADR-0054 §2.4: hoje só existe uma, mas nunca hardcoded)")
	fs.StringVar(&c.Holder, "holder", envStr("OMNIRA_AGENT_HOLDER", defaultHolder()),
		"identidade deste processo do agente no lock (default: hostname:pid)")
	// Achado Codex P2: o default aqui era /var/lib/omnira-agent/..., um nome DIFERENTE do
	// StateDirectory=omnira-update-agent que docs/release/systemd/omnira-update-agent.service
	// declara (systemd cria/possui /var/lib/omnira-update-agent, não /var/lib/omnira-agent) —
	// instalado com o EnvironmentFile mínimo documentado, o daemon falhava ao abrir o ledger
	// (OpenLedger não cria diretórios pai, e o usuário dedicado do serviço não tem permissão
	// para criar outro diretório solto em /var/lib). Alinhado ao nome real do binário/serviço.
	fs.StringVar(&c.LedgerPath, "ledger-path", envStr("OMNIRA_AGENT_LEDGER_PATH", "/var/lib/omnira-update-agent/ledger.db"),
		"caminho do ledger SQLite local (ADR-0054 §2.3)")
	fs.DurationVar(&c.Lease, "lease", envDuration("OMNIRA_AGENT_LEASE", 5*time.Minute),
		"duração do lock da instalação por aquisição")
	fs.DurationVar(&c.PollInterval, "poll-interval", envDuration("OMNIRA_AGENT_POLL_INTERVAL", 15*time.Second),
		"intervalo entre reconciliações automáticas (apps/update-agent)")
	fs.DurationVar(&c.HealthTimeout, "health-timeout", envDuration("OMNIRA_AGENT_HEALTH_TIMEOUT", 60*time.Second),
		"tempo máximo esperando um container ficar saudável")
	fs.DurationVar(&c.ObserveWindow, "observe-window", envDuration("OMNIRA_AGENT_OBSERVE_WINDOW", 2*time.Minute),
		"janela de observação pós-deploy antes de declarar SUCCEEDED (especificação §7.2)")
	fs.BoolVar(&c.Standalone, "standalone", envBool("OMNIRA_AGENT_STANDALONE", false),
		"confirma EXPLICITAMENTE que esta instalação nunca terá uma Update Manager API sincronizando revogações (ADR-0054) — nunca habilitar numa instalação gerenciada")

	c.ComposeBaseFiles = colonSeparatedList(strings.Split(
		envStr("OMNIRA_AGENT_COMPOSE_BASE_FILES", "docker-compose.yml:docker-compose.override.yml"), ":"))
	fs.Var(&c.ComposeBaseFiles, "compose-base-files",
		"lista ORDENADA de compose files reais desta instalação, separados por ':' (nunca omitir o override machine-local)")
	fs.StringVar(&c.ComposeOverrideDir, "compose-override-dir", envStr("OMNIRA_AGENT_COMPOSE_OVERRIDE_DIR", "/var/lib/omnira-update-agent/overrides"),
		"diretório onde o agente escreve os overrides de digest gerados por release")
	fs.StringVar(&c.ComposeProject, "compose-project", envStr("OMNIRA_AGENT_COMPOSE_PROJECT", "omnira"),
		"nome do projeto Docker Compose desta instalação")
	fs.BoolVar(&c.ComposeSudo, "compose-sudo", envBool("OMNIRA_AGENT_COMPOSE_SUDO", false),
		"prefixar comandos docker com 'sudo -n' (ambiente sem o processo no grupo docker)")

	fs.StringVar(&c.MigrateScript, "migrate-script", envStr("OMNIRA_AGENT_MIGRATE_SCRIPT", "tools/migrate-sql.sh"),
		"caminho de tools/migrate-sql.sh")
	fs.StringVar(&c.MigrationsDir, "migrations-dir", envStr("OMNIRA_AGENT_MIGRATIONS_DIR", "migrations"),
		"diretório de migrations/*.up.sql")
	// Achado Codex P2: NUNCA registrar o valor real de DATABASE_URL como default de flag —
	// flag.FlagSet grava o default como DefValue e Usage()/-h/qualquer erro de parse o
	// imprime LITERALMENTE, vazando a senha do schema owner em diagnósticos capturados
	// (AGENTS.md "nenhuma automação pode... retornar PII ou credenciais
	// desnecessariamente"). O fallback para OMNIRA_AGENT_DATABASE_URL/DATABASE_URL é
	// resolvido DEPOIS do parse, em ApplyEnvFallbacks — nunca como default visível aqui.
	fs.StringVar(&c.DatabaseURL, "database-url", "",
		"DATABASE_URL (schema owner, nunca omnira_app) para tools/migrate-sql.sh — lido de OMNIRA_AGENT_DATABASE_URL ou DATABASE_URL se omitido (nunca mostrado em --help)")

	fs.StringVar(&c.BackupScript, "backup-script", envStr("OMNIRA_AGENT_BACKUP_SCRIPT", "scripts/backup-omnira-db.sh"),
		"caminho de scripts/backup-omnira-db.sh")
	fs.StringVar(&c.BackupDir, "backup-dir", envStr("OMNIRA_AGENT_BACKUP_DIR", "backups"),
		"BACKUP_DIR base (relativo à raiz do repositório do script, ou absoluto)")
	fs.StringVar(&c.DBName, "db-name", envStr("OMNIRA_AGENT_DB_NAME", "omnira_dev"),
		"DB_NAME a fazer backup antes de migrar")
	fs.StringVar(&c.PGContainer, "pg-container", envStr("OMNIRA_AGENT_PG_CONTAINER", "omnira-postgres"),
		"PG_CONTAINER: nome do container Postgres real que scripts/backup-omnira-db.sh faz docker exec")
	fs.StringVar(&c.PGOwner, "pg-owner", envStr("OMNIRA_AGENT_PG_OWNER", "omnira"),
		"PG_OWNER: role dona do schema usada pelo backup (pg_dump -U)")

	return c
}

func defaultHolder() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s:%d", host, os.Getpid())
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// ApplyEnvFallbacks resolve, DEPOIS de fs.Parse, qualquer campo cujo valor de ambiente
// NUNCA pode ser exposto como default de flag (ver o comentário em FlagSet sobre
// --database-url) — chamado por TODO comando logo após fs.Parse(args), antes de usar
// c.DatabaseURL (Validate/ValidateLease não bastam: comandos como 'status' chamam só
// ValidateLease ou nada, e precisam do fallback do env do mesmo jeito).
func (c *Config) ApplyEnvFallbacks() {
	if c.DatabaseURL == "" {
		c.DatabaseURL = envStr("OMNIRA_AGENT_DATABASE_URL", os.Getenv("DATABASE_URL"))
	}
}

// Validate confirma os campos obrigatórios para MONTAR o Executor (Ledger/Compose) — nunca
// deixa o agente operar com um installation_id vazio (tocaria a instalação errada) ou sem
// compose_base_files. Achado Codex P2: DATABASE_URL NÃO é exigida aqui — Build() só MONTA o
// MigrateRunner, nunca o invoca; um operador de emergência com acesso ao ledger local e ao
// Docker, mas SEM as credenciais do schema owner no ambiente (ex. um cofre de segredos
// também fora do ar), ainda precisa poder rodar `updatectl rollback`, cujo caminho de
// recuperação (Executor.recover) NUNCA migra — só reverte código/config via Compose. Quem
// de fato pode avançar um run até MIGRATING_EXPAND ('start'/'reconcile', nunca 'rollback')
// chama RequireDatabaseURL() explicitamente antes de usar o Executor.
func (c *Config) Validate() error {
	c.ApplyEnvFallbacks()
	if c.InstallationID == "" {
		return fmt.Errorf("agentconfig: --installation-id (ou OMNIRA_AGENT_INSTALLATION_ID) é obrigatório")
	}
	if len(c.ComposeBaseFiles) == 0 || c.ComposeBaseFiles[0] == "" {
		return fmt.Errorf("agentconfig: --compose-base-files não pode ser vazio")
	}
	// Achado Codex P2: time.NewTicker PANICA com uma duração <= 0 — sem esta checagem,
	// --poll-interval=0s (ou um OMNIRA_AGENT_POLL_INTERVAL negativo) passava pela validação,
	// o daemon reconciliava uma vez e então CRASHAVA ao criar o ticker do loop periódico,
	// nunca mais monitorando runs subsequentes. Falhar aqui, antes de qualquer mutação,
	// torna o erro de configuração visível e imediato em vez de um crash tardio.
	if c.PollInterval <= 0 {
		return fmt.Errorf("agentconfig: --poll-interval (ou OMNIRA_AGENT_POLL_INTERVAL) precisa ser positivo, veio %s", c.PollInterval)
	}
	return c.ValidateLease()
}

// ValidateLease confirma que Lease é suficientemente grande para ser representável no
// ledger (que só grava lease_ms — Ledger.AcquireLock, com precisão de milissegundo) e
// minimamente útil — achado Codex P2: --lease=0s, negativo, ou positivo mas < 1ms passava
// pela validação, AcquireLock gravava um lease_ms <= 0, e qualquer outro processo podia
// adquirir o lock da instalação IMEDIATAMENTE enquanto backup/migração/rollback ainda
// estivessem mutando containers/dados sob o lock anterior. Chamada por Validate() (via
// Build(), usado por 'reconcile'/'rollback'), mas TAMBÉM diretamente por 'start' e
// 'acknowledge-recovery' — esses dois comandos chamam Ledger.AcquireLock sem nunca passar
// por Build()/Validate() completo (não usam Compose/Migrate/Backup), então precisam desta
// checagem isolada para não ficar sem a mesma proteção.
func (c *Config) ValidateLease() error {
	c.ApplyEnvFallbacks()
	if c.Lease < time.Second {
		return fmt.Errorf("agentconfig: --lease (ou OMNIRA_AGENT_LEASE) precisa ser de pelo menos 1s, veio %s", c.Lease)
	}
	return nil
}

// RequireDatabaseURL confirma que DATABASE_URL foi de fato fornecida — chamada
// explicitamente só pelos comandos cujo caminho PODE invocar migrações reais ('start' grava
// o manifesto que será migrado; 'reconcile' pode avançar um run até MIGRATING_EXPAND). Nunca
// chamada por 'rollback' (Executor.recover nunca migra, só reverte código/config via
// Compose) nem por Validate()/Build() em geral — ver o comentário em Validate().
func (c *Config) RequireDatabaseURL() error {
	c.ApplyEnvFallbacks()
	if c.DatabaseURL == "" {
		return fmt.Errorf("agentconfig: --database-url (ou OMNIRA_AGENT_DATABASE_URL / DATABASE_URL) é obrigatório")
	}
	return nil
}

// Build monta o agent.Executor real a partir desta configuração — a ÚNICA função que de fato
// conecta MigrateRunner/BackupRunner/BackupVerifier/ComposeAdapter reais (nunca os fakes dos
// testes) ao Ledger. Chamado por apps/update-agent e apps/updatectl, nunca duplicado entre
// eles. O chamador é responsável por Close() no *agent.Ledger devolvido.
func (c *Config) Build() (*agent.Executor, *agent.Ledger, error) {
	if err := c.Validate(); err != nil {
		return nil, nil, err
	}
	ledger, err := agent.OpenLedger(c.LedgerPath)
	if err != nil {
		return nil, nil, fmt.Errorf("agentconfig: abrir ledger: %w", err)
	}
	// Achado Codex [P1] (rodada 12): Build() é o ÚNICO ponto que monta um Executor/Ledger
	// reais para OS DOIS binários — apps/update-agent (daemon) E apps/updatectl (CLI, sem
	// daemon algum). Marcar standalone só no startup do daemon (como a rodada 10 fez)
	// deixava `updatectl reconcile --standalone`/`rollback --standalone`, usado numa
	// instalação standalone SEM o daemon rodando, com um ledger nunca sincronizado E nunca
	// marcado standalone — o novo gate de frescor (fail-closed por padrão) recusaria TODO
	// run. Idempotente (Ledger.MarkStandalone/revocation_sync são UPSERT) — chamar isto em
	// toda montagem nunca é um problema, mesmo repetido a cada invocação da CLI.
	if c.Standalone {
		if err := ledger.MarkStandalone(context.Background()); err != nil {
			ledger.Close()
			return nil, nil, fmt.Errorf("agentconfig: marcar instalação standalone: %w", err)
		}
	}
	// Achado Codex P1 (duas rodadas): --compose-sudo nunca pode se propagar para
	// tools/migrate-sql.sh — ele fala com o Postgres via TCP/DATABASE_URL, nunca precisando
	// de privilégio para o socket Docker, e `sudo -n` sob a política padrão `env_reset`
	// descartaria DATABASE_URL/MIGRATIONS_DIR do ambiente, fazendo a migração falhar. JÁ
	// scripts/backup-omnira-db.sh usa `docker ps`/`docker exec` DE VERDADE (o Postgres roda
	// DENTRO de um container; o dump é feito via `docker exec ... pg_dump` nesse container)
	// — precisa do MESMO acesso privilegiado que o ComposeAdapter, então --compose-sudo
	// também vale para ele. execBackupRunner.Run transporta BACKUP_DIR/DB_NAME como
	// argumentos de `env` (não como ambiente herdado) justamente para sobreviver ao
	// `env_reset` do sudo — ver o comentário completo lá.
	var backupCmdPrefix []string
	if c.ComposeSudo {
		backupCmdPrefix = []string{"sudo", "-n"}
	}
	compose := agent.NewComposeAdapter([]string(c.ComposeBaseFiles), c.ComposeOverrideDir, c.ComposeProject, c.ComposeSudo)
	migrate := agent.NewMigrateRunner(c.MigrateScript, c.MigrationsDir, c.DatabaseURL)
	// Achado Codex P1: PG_CONTAINER/PG_OWNER também precisam sobreviver ao env_reset do
	// sudo — a mesma razão de BACKUP_DIR/DB_NAME (transportados como argumentos de `env`
	// dentro de execBackupRunner.Run, nunca como ambiente herdado). Sem isto, uma
	// instalação cujo container/role reais divergem dos defaults do script
	// ("omnira-postgres"/"omnira") faria o backup falhar (container não encontrado) ou,
	// pior, fazer backup silenciosamente de um container/role ERRADO que coincida com o
	// default.
	backupEnv := []string{"PG_CONTAINER=" + c.PGContainer, "PG_OWNER=" + c.PGOwner}
	backup := agent.NewBackupRunner(c.BackupScript, c.BackupDir, c.DBName, backupEnv, backupCmdPrefix...)
	verify := agent.NewBackupVerifier(0)
	exec := &agent.Executor{
		Ledger:        ledger,
		Compose:       compose,
		Migrate:       migrate,
		Backup:        backup,
		BackupVerify:  verify,
		HealthTimeout: c.HealthTimeout,
		ObserveWindow: c.ObserveWindow,
	}
	return exec, ledger, nil
}

// ReconcileOnce lê os runs ATIVOS da instalação direto do ledger local (o run já guarda o
// manifest_json congelado no momento em que foi criado — nenhuma chamada de rede necessária
// para saber o que fazer) e chama exec.Reconcile uma vez. Compartilhado por
// apps/update-agent (chamado em loop) e apps/updatectl (chamado uma vez, sob demanda) — a
// ÚNICA lógica de "o que reconciliar" deste programa, nunca duplicada entre os dois
// binários. Nunca propaga o erro de Reconcile como fatal: RECOVERY_REQUIRED pendente ou um
// manifesto ausente são estados operacionais normais para quem chama isto, não um motivo
// para o processo morrer — por isso retorna o erro (para o chamador decidir/logar), em vez
// de tratar internamente.
func ReconcileOnce(ctx context.Context, exec *agent.Executor, ledger *agent.Ledger, installationID, holder string, lease time.Duration) error {
	runs, err := ledger.ActiveRuns(ctx, installationID)
	if err != nil {
		return fmt.Errorf("listar runs ativos: %w", err)
	}
	if len(runs) == 0 {
		return nil
	}
	manifests := make(map[string]*domain.ReleaseManifest, len(runs))
	for _, r := range runs {
		m, err := domain.ParseManifest([]byte(r.ManifestJSON))
		if err != nil {
			// Um manifesto que não valida mais não pode travar a reconciliação dos OUTROS
			// runs desta instalação — mas também nunca é passado adiante como se fosse
			// válido. Reconcile vai reportar esse run como "sem manifesto fornecido" e parar
			// a fila (achado Codex P1 em internal/platformrelease/agent/reconcile.go) —
			// exatamente o comportamento certo para um manifesto congelado no ledger que não
			// passa mais no próprio verificador.
			log.Printf("agentconfig: manifesto do run %s não valida: %v", r.RunID, err)
			continue
		}
		manifests[r.RunID] = m
	}
	return exec.Reconcile(ctx, installationID, holder, lease, manifests)
}
