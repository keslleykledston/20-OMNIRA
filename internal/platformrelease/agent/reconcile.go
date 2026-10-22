// reconcile.go: o motor de execução/reconciliação do Update Agent (F5, especificação §6.1,
// §7.2, §8). Liga o ledger durável (ledger.go), o Compose Adapter (compose.go) e a máquina de
// estados pura do F1 (internal/platformrelease/domain) num único fluxo: avança um run passo a
// passo, grava um checkpoint ANTES e DEPOIS de cada ação perigosa, e — este é o ponto central —
// pode ser chamado de novo a qualquer momento (depois de um crash do processo do agente) sem
// nunca repetir uma ação perigosa já concluída, nem adivinhar o que fazer: sempre reavalia o
// estado REAL (ledger + sistema) antes de decidir o próximo passo.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/omnira/omnira/internal/platformrelease/domain"
)

// Step identifica um passo perigoso dentro de um RunState — o nome gravado no journal de
// checkpoints (ledger.go). Um por estado da sequência pós-trava (especificação §6.1).
type Step string

const (
	StepBackup   Step = "backup"
	StepDownload Step = "download"
	StepSnapshot Step = "snapshot"
	StepMigrate  Step = "migrate"
	StepDeploy   Step = "deploy"
	StepHealth   Step = "health"
	StepObserve  Step = "observe"
)

// MigrateRunner executa tools/migrate-sql.sh — a ÚNICA forma permitida de aplicar migrations
// (ADR-0054 §2.2 item 5, docs/release/00-audit.md: `make migrate-up/down` são legados e
// perigosos, nunca chamados pelo agente). Uma interface, não uma chamada direta a exec.Command
// aqui, para que os testes possam controlar o comportamento sem depender de um binário real
// quando só a lógica de decisão está sendo exercitada.
type MigrateRunner interface {
	// Up aplica as migrations pendentes até, no máximo, `target` (manifest.Migrations.Target
	// — tools/migrate-sql.sh up com MIGRATE_TARGET) e retorna erro se qualquer uma falhar OU
	// se o verify interno de checksum (F4) detectar uma migration adulterada. Achado Codex
	// P1: sem um alvo explícito, `up` aplicaria TODO *.up.sql pendente no diretório — se o
	// diretório de migrations tiver arquivos mais novos que o que ESTA release declarou
	// (ex. uma reconciliação atrasada processando uma release mais antiga depois que o
	// diretório de migrations já avançou), o schema seria migrado além do que a release
	// selecionada de fato aprovou/testou.
	Up(ctx context.Context, target, checksumBundle string) error
	// Status reporta a migration ledger atual (tools/migrate-sql.sh status) — usado só para
	// evidência/journal, nunca para decisão de controle de fluxo (que usa Up/erro).
	Status(ctx context.Context) (string, error)
}

// execMigrateRunner chama tools/migrate-sql.sh de verdade via exec.Command (nunca `sh -c` com
// string interpolada — argumentos sempre em slice).
type execMigrateRunner struct {
	ScriptPath    string
	MigrationsDir string
	DatabaseURL   string
	CmdPrefix     []string // ex. ["sudo","-n"] em ambiente de teste sem grupo docker
}

// NewMigrateRunner constrói o MigrateRunner real que invoca tools/migrate-sql.sh de verdade
// — o tipo concreto (execMigrateRunner) nunca é exportado, porque nada fora deste pacote
// precisa conhecer sua forma, só a interface. Usado pelo daemon (apps/update-agent) e pela
// CLI (apps/updatectl), as únicas duas coisas fora dos testes deste pacote que de fato
// executam migrations.
func NewMigrateRunner(scriptPath, migrationsDir, databaseURL string, cmdPrefix ...string) MigrateRunner {
	return execMigrateRunner{ScriptPath: scriptPath, MigrationsDir: migrationsDir, DatabaseURL: databaseURL, CmdPrefix: cmdPrefix}
}

// killOnCancel coloca `c` no líder de um NOVO grupo de processos do SO (Setpgid) e substitui
// o cancelamento padrão de exec.CommandContext para matar o GRUPO INTEIRO, não só `c.Process`.
// Achado Codex P1: o comportamento padrão de Cancel só envia o sinal ao processo DIRETO (aqui,
// `sh`/`bash`) — um `psql`/`pg_dump` que o script já lançou como filho NÃO morre com ele (fica
// órfão, reparented para init, continuando a correr). Isso é crítico neste pacote
// especificamente porque Reconcile cancela execCtx no INSTANTE em que perde o fencing
// (heartbeat falhou, outro agente assumiu o lock) — sem isto, uma migração ou um backup já em
// andamento continuaria mutando o Postgres mesmo depois de outro processo já ter assumido a
// instalação e iniciado sua própria decisão de recuperação, uma condição de corrida real sobre
// dados. Setpgid é Linux-only (SysProcAttr não é portável), consistente com o resto deste
// pacote, que já assume Linux/Docker em todo lugar.
func killOnCancel(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return nil
		}
		return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}

// envWithoutKeys devolve `env` sem nenhuma entrada "KEY=..." cuja KEY esteja em `keys` —
// usado para garantir que uma variável operador-only NUNCA seja herdada silenciosamente do
// ambiente do processo do agente para um subprocesso que nunca deveria vê-la.
func envWithoutKeys(env []string, keys ...string) []string {
	drop := make(map[string]bool, len(keys))
	for _, k := range keys {
		drop[k] = true
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if !drop[name] {
			out = append(out, kv)
		}
	}
	return out
}

func (m execMigrateRunner) runWith(ctx context.Context, cmd string, extraEnv ...string) (string, error) {
	full := append(append([]string{}, m.CmdPrefix...), "sh", m.ScriptPath, cmd)
	c := exec.CommandContext(ctx, full[0], full[1:]...)
	killOnCancel(c)
	// os.Environ() como base (achado Codex): um *exec.Cmd novo tem Env=nil, e
	// `append(nil, ...)` descarta TODO o ambiente herdado do processo do agente — inclusive
	// APP_DB_PASSWORD, OMNIRA_SESSION_CTX_KEY e SESSION_CTX_MODE, que tools/migrate-sql.sh usa
	// de verdade para provisionar a role da aplicação e a chave de contexto de sessão
	// assinado (ADR-0042). Perder isso silenciosamente deixaria `up` aplicar as migrations
	// mas nunca instalar/rotacionar a chave — uma regressão de segurança séria e silenciosa.
	//
	// BASELINE_UP_TO removida explicitamente (achado Codex P1): é uma ferramenta EXCLUSIVAMENTE
	// operacional, manual, de uso único ("adotar um banco cujo schema foi aplicado à mão") —
	// NUNCA deve ser usada automaticamente por uma atualização do agente. Se o processo do
	// agente (daemon de longa duração) herdasse essa variável do seu PRÓPRIO ambiente (um
	// operador configurou uma vez no systemd unit e esqueceu de remover, ou um script de
	// deploy a exporta globalmente por engano), `up` marcaria migrations PENDENTES como
	// "aplicadas" SEM RODAR O SQL REAL — o agente então reportaria o schema como migrado e
	// implantaria código novo sobre um banco que nunca foi de fato atualizado.
	base := envWithoutKeys(os.Environ(), "BASELINE_UP_TO")
	env := append(base, "MIGRATIONS_DIR="+m.MigrationsDir, "DATABASE_URL="+m.DatabaseURL)
	c.Env = append(env, extraEnv...)
	out, err := c.CombinedOutput()
	return string(out), err
}

// migrationsInventoryChecksum reproduz EXATAMENTE o algoritmo de
// scripts/release/migrations-checksum.sh (que gera manifest.Migrations.ChecksumBundle no
// momento do build da release): sha256 de "<nome> <sha256 do conteúdo>\n" por arquivo
// *.up.sql, ordenado por nome — nunca uma hash dos bytes concatenados (um rename ou um
// deslocamento de fronteira entre arquivos adjacentes mudaria o significado sem mudar esse
// hash). Reimplementado em Go (não invoca o script .sh) porque este é o processo do AGENTE,
// de longa duração, sem Python/Bash garantido no PATH do container/host de produção.
func migrationsInventoryChecksum(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("ler diretório de migrations %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("nenhum *.up.sql encontrado em %s", dir)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", fmt.Errorf("ler migration %s: %w", name, err)
		}
		contentDigest := sha256.Sum256(content)
		fmt.Fprintf(h, "%s %x\n", name, contentDigest)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// migrationApplied confirma, no texto de `tools/migrate-sql.sh status`, que `target`
// aparece numa linha "applied  <target>  ..." — nunca um "contains" ingênuo (um target que é
// prefixo de outro version string nunca deve contar como aplicado).
// migrationPrefix extrai o mesmo "prefixo de versão" que tools/migrate-sql.sh usa para
// MIGRATE_TARGET/BASELINE_UP_TO (função prefix() do script: basename sem .up.sql, cortado no
// primeiro "_") — ex. "000172_external_start_conversation" -> "000172". manifest.Migrations.
// Target é sempre só esse prefixo numérico (ex. "000172", ver release/fixtures/*.json), nunca
// o nome completo do arquivo.
func migrationPrefix(version string) string {
	if i := strings.Index(version, "_"); i >= 0 {
		return version[:i]
	}
	return version
}

// migrationApplied confirma, no texto de `tools/migrate-sql.sh status`, que uma migration cujo
// PREFIXO bate com `target` aparece numa linha "applied  <version>  ...". Achado Codex P1:
// `status` sempre imprime o nome completo da migration (ex.
// "000172_external_start_conversation"), nunca só o prefixo — uma comparação exata contra
// target="000172" (o formato real de manifest.Migrations.Target) nunca bateria, fazendo toda
// release terminar em RECOVERY_REQUIRED mesmo tendo migrado com sucesso.
func migrationApplied(statusOutput, target string) bool {
	for _, line := range strings.Split(statusOutput, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "applied" && migrationPrefix(fields[1]) == target {
			return true
		}
	}
	return false
}

func (m execMigrateRunner) Up(ctx context.Context, target, checksumBundle string) error {
	if target == "" {
		return fmt.Errorf("agent: tools/migrate-sql.sh up: target vazio — nunca aplicar migrations sem um alvo declarado pela release")
	}
	// Achado Codex P1: o verify interno do script só cobre migrations JÁ APLICADAS (detecta
	// um .up.sql reescrito DEPOIS de rodar) — um arquivo PENDENTE alterado localmente antes
	// de nunca ter sido aplicado passaria batido. Comparar contra o checksum_bundle que a
	// própria release aprovou fecha essa lacuna: ele cobre o inventário INTEIRO do diretório
	// no momento do build da release, então qualquer arquivo pendente divergente (editado,
	// adicionado, removido, renomeado) muda o hash e é recusado ANTES de rodar qualquer SQL.
	got, err := migrationsInventoryChecksum(m.MigrationsDir)
	if err != nil {
		return fmt.Errorf("agent: verificar inventário de migrations antes de aplicar: %w", err)
	}
	if checksumBundle != "" && got != checksumBundle {
		return fmt.Errorf("agent: inventário de migrations em %s (%s) não bate com migrations.checksum_bundle aprovado pela release (%s) — recusando aplicar", m.MigrationsDir, got, checksumBundle)
	}
	out, err := m.runWith(ctx, "up", "MIGRATE_TARGET="+target)
	if err != nil {
		return fmt.Errorf("agent: tools/migrate-sql.sh up: %w (%s)", err, out)
	}
	// Achado Codex P1: limitar o TETO (MIGRATE_TARGET) não prova que o PISO foi alcançado —
	// se o target declarado não existe mais no diretório local (removido, renomeado, ou um
	// agente apontando para um checkout incompleto), `up` aplica o que existir abaixo dele e
	// sai com sucesso, deixando o schema real atrás do que esta release exige sem nenhum
	// erro visível. Confirmar explicitamente, após aplicar, que o target está "applied".
	status, statusErr := m.runWith(ctx, "status")
	if statusErr != nil {
		return fmt.Errorf("agent: confirmar migrations.target=%s após aplicar: %w (%s)", target, statusErr, status)
	}
	if !migrationApplied(status, target) {
		return fmt.Errorf("agent: migrations.target=%s declarado pela release não está applied após 'up' — diretório de migrations local pode estar incompleto/desatualizado", target)
	}
	return nil
}

func (m execMigrateRunner) Status(ctx context.Context) (string, error) {
	out, err := m.runWith(ctx, "status")
	if err != nil {
		return "", fmt.Errorf("agent: tools/migrate-sql.sh status: %w (%s)", err, out)
	}
	return out, nil
}

// BackupRunner executa o backup real da instalação (scripts/backup-omnira-db.sh — ADR-0054
// §2.1 "Backup/Recovery Adapter: reaproveitando scripts/backup-omnira-*.sh existentes, nunca
// os substitui"). Retorna o caminho do dump produzido, para registrar no checkpoint.
type BackupRunner interface {
	Run(ctx context.Context) (dumpPath string, err error)
}

type execBackupRunner struct {
	ScriptPath string
	Env        []string // ex. PG_CONTAINER=..., PG_OWNER=... — nunca BACKUP_DIR/DB_NAME, sempre derivadas de BackupDir/DBName
	CmdPrefix  []string
	BackupDir  string // onde scripts/backup-omnira-db.sh escreve <db>_<timestamp>.dump/.meta.json
	DBName     string
}

// NewBackupRunner constrói o BackupRunner real que invoca scripts/backup-omnira-db.sh de
// verdade. extraEnv é para configuração ADICIONAL do script (ex. PG_CONTAINER, PG_OWNER,
// RETENTION_DAYS) — nunca BACKUP_DIR/DB_NAME, que Run() já deriva de backupDir/dbName e
// sempre sobrescreve (ver resolvedBackupDBDir/achado Codex sobre propagar esses dois campos).
func NewBackupRunner(scriptPath, backupDir, dbName string, extraEnv []string, cmdPrefix ...string) BackupRunner {
	return execBackupRunner{ScriptPath: scriptPath, BackupDir: backupDir, DBName: dbName, Env: extraEnv, CmdPrefix: cmdPrefix}
}

func (b execBackupRunner) Run(ctx context.Context) (string, error) {
	dir := b.resolvedBackupDBDir()
	pattern := filepath.Join(dir, b.DBName+"_*.dump")
	// Achado Codex P1: escolher o dump "mais recente por ORDEM LEXICOGRÁFICA DO NOME" é
	// frágil contra um relógio do host que anda PARA TRÁS (correção de NTP, VM com clock
	// drift) — um dump PRÉ-EXISTENTE com timestamp "no futuro" relativo ao relógio corrigido
	// permaneceria "o mais recente" mesmo sem ter nenhuma relação com ESTA invocação, e um
	// script que silenciosamente falhasse em criar um dump novo ainda "teria sucesso" por
	// acidente (há achado Codex explícito: "o teste do runner aceitava sucesso mesmo quando
	// o script não cria dump nenhum"). Capturar o conjunto de arquivos ANTES de rodar o
	// script e exigir que o resultado seja algo que NÃO estava nesse conjunto antes fecha
	// ambos os problemas de uma vez — a prova nunca depende de comparar nomes/timestamps
	// entre arquivos, só da diferença real produzida por esta chamada específica.
	before, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("agent: listar dumps existentes em %s antes do backup: %w", dir, err)
	}
	beforeSet := make(map[string]bool, len(before))
	for _, f := range before {
		beforeSet[f] = true
	}

	// Achado Codex P1: scripts/backup-omnira-db.sh usa `docker ps`/`docker exec` de verdade
	// (o Postgres roda DENTRO de um container; o dump é feito via `docker exec ... pg_dump`
	// DENTRO dele) — precisa do MESMO acesso privilegiado ao socket Docker que o
	// ComposeAdapter usa, nunca "fala com Postgres via TCP" como um comentário anterior
	// erroneamente assumia. Mas CmdPrefix=["sudo","-n"] sob a política padrão `env_reset` do
	// sudo DESCARTA todo o ambiente do processo chamador antes do script rodar — setar
	// `c.Env` não ajudaria, porque o sudo nunca o propaga ao processo filho. A correção é
	// transportar as variáveis necessárias (BACKUP_DIR/DB_NAME, sempre derivadas de
	// b.BackupDir/b.DBName — nunca de um valor herdado que poderia divergir de
	// resolvedBackupDBDir — mais qualquer b.Env configurado) como ARGUMENTOS do comando
	// `env` (coreutils), que o sudo NUNCA filtra (são parte da linha de comando do processo
	// que o próprio sudo invoca, não do ambiente herdado) — funciona igual bem com ou sem
	// sudo no CmdPrefix.
	// Achado real ao testar: nem todo `env` aceita "--" como separador antes do comando
	// (ex. uutils coreutils, cada vez mais comum em distros novas, rejeita com "No such
	// file or directory") — omitido com segurança aqui, já que o comando seguinte (`bash
	// <script>`) nunca é ambíguo com um par VAR=VALUE.
	// Achado Codex P2: se b.Env também contivesse um BACKUP_DIR/DB_NAME (ex. configuração
	// extra, ou colisão acidental), `env` usa a ÚLTIMA atribuição de cada variável — colocar
	// os valores AUTORITATIVOS (derivados de b.BackupDir/b.DBName, os MESMOS que
	// resolvedBackupDBDir usa para procurar o dump depois) ANTES de b.Env, como a versão
	// anterior fazia, deixava b.Env vencer por vir depois. Filtrar primeiro (nunca confiar
	// na ordem de precedência) e só então anexar os autoritativos ao FINAL garante que eles
	// sempre vençam, nunca o contrário.
	envArgs := append(envWithoutKeys(b.Env, "BACKUP_DIR", "DB_NAME"), "BACKUP_DIR="+b.BackupDir, "DB_NAME="+b.DBName)

	// Achado Codex P2: sob sudo, scripts/backup-omnira-db.sh roda inteiramente como ROOT — e
	// sudo, por padrão, PRESERVA o umask do processo chamador (nunca o reseta). Um daemon
	// systemd com UMask restrito (ex. 0077, o hardening de
	// docs/release/systemd/omnira-update-agent.service) faria o dump e seus metadados
	// nascerem root:root 0700/0600 — ilegíveis para o próprio agente, que roda SEM
	// privilégio, tentar enumerar/ler depois via BackupVerifier. Toda atualização falharia
	// no backup mesmo com o dump criado com sucesso. Corrigido com dois ajustes isolados A
	// ESTE subprocesso (nunca ao processo do agente inteiro — mudar o umask do processo pai
	// seria uma condição de corrida real contra a goroutine de heartbeat, que também cria
	// arquivos): (1) `sudo -n -g <gid do processo atual>` faz o root criar os arquivos com o
	// GRUPO do próprio agente, não o grupo de root; (2) um `sh -c 'umask ...; exec "$@"'`
	// torna o dump/metadados group-readable (0027 → rw-r----- / rwxr-x---), legíveis pelo
	// grupo que acabamos de forçar em (1). Sem sudo no prefixo, isso é um no-op inofensivo
	// (o processo já é o dono, 0027 ainda permite leitura/escrita ao dono).
	cmdPrefix := append([]string{}, b.CmdPrefix...)
	if len(cmdPrefix) > 0 {
		// Achado Codex P1: sudo -g SEM -u explícito NUNCA eleva o UID — só troca o GID
		// efetivo, mantendo o UID do próprio chamador (confirmado ao testar:
		// `sudo -n -g "#<gid>" id` devolve uid=<chamador>, não uid=0). Isso quebrava
		// inteiramente o propósito de --compose-sudo: o backup real (docker exec) voltaria
		// a rodar SEM privilégio e falharia antes de criar qualquer dump, bloqueando toda
		// atualização. "-u root" explícito restaura a elevação; combinado com "-g #<gid>"
		// (confirmado: `sudo -n -u root -g "#<gid>" id` devolve uid=0 gid=<chamador>) dá as
		// DUAS propriedades exigidas ao mesmo tempo — root para o Docker socket, o grupo do
		// agente para o verificador sem privilégio conseguir ler o resultado depois.
		//
		// Achado real ao testar: sudo -g SEM o prefixo "#" trata o argumento como NOME de
		// grupo, procurando por um grupo chamado literalmente "1002" em /etc/group — e
		// falha com "group '1002' not found" mesmo o GID 1002 existindo de verdade. "#gid"
		// é a forma documentada do sudo para especificar um GID numérico diretamente, sem
		// depender de esse GID ter um nome resolvível.
		cmdPrefix = append(cmdPrefix, "-u", "root", "-g", "#"+strconv.Itoa(os.Getgid()))
	}
	inner := append([]string{"env"}, envArgs...)
	inner = append(inner, "bash", b.ScriptPath)
	full := append(cmdPrefix, "sh", "-c", `umask 0027; exec "$@"`, "sh")
	full = append(full, inner...)
	c := exec.CommandContext(ctx, full[0], full[1:]...)
	killOnCancel(c)
	// Base do ambiente do PRÓPRIO processo chamador (PATH, credenciais de rclone já
	// configuradas) — preservada quando NÃO há sudo no meio; quando há, o sudo a substitui
	// pela sua própria política de qualquer forma, e os `env ...` acima garantem o que
	// realmente importa sobreviva.
	c.Env = os.Environ()
	out, err := c.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("agent: %s: %w (%s)", b.ScriptPath, err, out)
	}

	after, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("agent: listar dumps em %s após o backup: %w", dir, err)
	}
	var newDumps []string
	for _, f := range after {
		if !beforeSet[f] {
			newDumps = append(newDumps, f)
		}
	}
	if len(newDumps) == 0 {
		return "", fmt.Errorf("agent: backup reportou sucesso mas nenhum dump NOVO apareceu em %s (saída: %s)", dir, out)
	}
	if len(newDumps) > 1 {
		// Nunca deveria acontecer numa execução serial do agente, mas se acontecer, escolher
		// arbitrariamente seria pior do que falhar alto e visível.
		return "", fmt.Errorf("agent: backup produziu %d dumps novos em %s, esperava exatamente 1: %v", len(newDumps), dir, newDumps)
	}
	return newDumps[0], nil
}

// resolvedBackupDBDir devolve o diretório ONDE scripts/backup-omnira-db.sh de fato escreve o
// dump — achado Codex P2: esse script sempre executa `cd "$(dirname "$0")/.."` ANTES de
// resolver BACKUP_DIR (default relativo "backups"), então um BACKUP_DIR relativo é sempre
// relativo à RAIZ DO REPOSITÓRIO, nunca ao diretório de trabalho de quem o invoca. Se este
// runner resolvesse b.BackupDir relativo ao cwd do PROCESSO DO AGENTE (um daemon systemd pode
// rodar de qualquer diretório, inclusive "/"), um backup bem-sucedido pareceria "sem dump
// encontrado" — ou, pior, um dump ANTIGO sob o cwd errado do agente poderia ser escolhido por
// coincidência de nome. Replicar exatamente a mesma resolução do script (relativa ao diretório
// PAI do próprio ScriptPath) elimina essa divergência; um BackupDir já absoluto é usado como
// está (permite ao operador configurar explicitamente um caminho fora do repositório).
func (b execBackupRunner) resolvedBackupDBDir() string {
	backupDir := b.BackupDir
	if !filepath.IsAbs(backupDir) {
		repoRoot := filepath.Dir(filepath.Dir(b.ScriptPath))
		backupDir = filepath.Join(repoRoot, backupDir)
	}
	return filepath.Join(backupDir, b.DBName)
}

// BackupVerifier confirma, com uma evidência REAL (não apenas "o processo saiu com código 0"),
// que o backup produzido tem alguma chance de ser restaurável — achado Codex P1:
// scripts/backup-omnira-db.sh confirma só o TAMANHO do próprio dump, nunca um restore de
// verdade; avançar para BACKUP_VERIFIED sem nenhuma checagem adicional aqui tratava "o
// processo não retornou erro" como prova de recuperabilidade, o que não é. Um restore
// completo a cada release seria caro para rodar em toda atualização (minutos); esta
// verificação é o mínimo que PODE ser feito sem custo nesse caminho — o restore de verdade
// (scripts/backup-restore-check.sh / backup-restore-hub-check.sh, já testados no F4) continua
// sendo a prova de recuperabilidade real, mantida como uma checagem operacional periódica
// separada, referenciada em release/policies/backup.yaml, não re-executada aqui.
type BackupVerifier interface {
	Verify(ctx context.Context, dumpPath string) error
}

type backupMeta struct {
	SchemaVersion string `json:"schema_version"`
	SizeBytes     int64  `json:"size_bytes"`
	Format        string `json:"format"`
}

// metaFileBackupVerifier lê o <dump>.meta.json que scripts/backup-omnira-db.sh grava ao lado
// de cada dump e confirma que os campos mínimos de evidência estão presentes e plausíveis.
type metaFileBackupVerifier struct {
	MinSizeBytes int64 // default 1024, o mesmo mínimo que o próprio script já aplica
}

// NewBackupVerifier constrói o BackupVerifier real (lê o .meta.json ao lado do dump e
// confirma tamanho/schema_version/format reais no disco). minSizeBytes <= 0 usa o default
// (1024, o mesmo mínimo que scripts/backup-omnira-db.sh já aplica).
func NewBackupVerifier(minSizeBytes int64) BackupVerifier {
	return metaFileBackupVerifier{MinSizeBytes: minSizeBytes}
}

func (v metaFileBackupVerifier) Verify(_ context.Context, dumpPath string) error {
	metaPath := strings.TrimSuffix(dumpPath, ".dump") + ".meta.json"
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("agent: ler evidência de backup %s: %w", metaPath, err)
	}
	var meta backupMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return fmt.Errorf("agent: evidência de backup %s não é JSON válido: %w", metaPath, err)
	}
	min := v.MinSizeBytes
	if min <= 0 {
		min = 1024
	}
	if meta.SchemaVersion == "" {
		return fmt.Errorf("agent: evidência de backup %s não informa schema_version", metaPath)
	}
	if meta.SizeBytes < min {
		return fmt.Errorf("agent: evidência de backup %s reporta size_bytes=%d, abaixo do mínimo plausível %d", metaPath, meta.SizeBytes, min)
	}
	if meta.Format != "pg_dump -Fc" {
		return fmt.Errorf("agent: evidência de backup %s tem format=%q inesperado", metaPath, meta.Format)
	}
	// Achado Codex P2: o .meta.json pode continuar íntegro mesmo que o próprio dump tenha
	// sido apagado ou truncado depois (disco cheio no meio de uma cópia externa, limpeza
	// manual, etc.) — confiar só nos metadados aceitaria um backup que não existe mais de
	// verdade. Confirma que o arquivo existe e que seu tamanho REAL bate com o que o próprio
	// script registrou no momento do dump.
	info, err := os.Stat(dumpPath)
	if err != nil {
		return fmt.Errorf("agent: dump de backup %s inacessível: %w", dumpPath, err)
	}
	if info.Size() != meta.SizeBytes {
		return fmt.Errorf("agent: dump de backup %s tem %d bytes no disco, mas a evidência registrada é de %d bytes — pode ter sido truncado ou substituído", dumpPath, info.Size(), meta.SizeBytes)
	}
	return nil
}

// Executor é o motor de reconciliação de UMA instalação. Nenhum campo depende de um backend
// HTTP/NATS existir — tudo que ele precisa é o ledger local, o Compose Adapter e os dois
// runners acima.
type Executor struct {
	Ledger        *Ledger
	Compose       *ComposeAdapter
	Migrate       MigrateRunner
	Backup        BackupRunner
	BackupVerify  BackupVerifier
	HealthTimeout time.Duration
	ObserveWindow time.Duration
	Clock         func() time.Time // substituível nos testes; nunca time.Now() direto nas decisões de observação
}

func (e *Executor) now() time.Time {
	if e.Clock != nil {
		return e.Clock()
	}
	return time.Now()
}

// stepDone verifica se um Step já foi concluído com sucesso para este run — a checagem que
// torna toda a execução idempotente e retomável após um crash: um passo "completed" nunca é
// refeito, só confirmado.
func (e *Executor) stepDone(ctx context.Context, runID string, s Step) (bool, error) {
	status, err := e.Ledger.StepStatus(ctx, runID, string(s))
	if err != nil {
		return false, err
	}
	return status == "completed", nil
}

// runStep executa `action` só se o Step ainda não estiver "completed" — grava started antes,
// completed/failed depois. Um Step "started" sem "completed" de uma tentativa anterior (o
// processo morreu no meio) é simplesmente tentado de novo aqui: toda ação chamada por este
// método tem que ser segura de re-executar (backup: novo dump, sempre seguro; pull: idempotente
// por digest; migrate up: idempotente pelo ledger do F4; deploy: `docker compose up -d` é
// idempotente; health/observe: só esperam, nunca mutam).
func (e *Executor) runStep(ctx context.Context, h *LockHandle, runID string, s Step, action func(ctx context.Context) (detail string, err error)) error {
	done, err := e.stepDone(ctx, runID, s)
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	if err := e.Ledger.RecordCheckpointStart(ctx, h, runID, string(s)); err != nil {
		return err
	}
	detail, actionErr := action(ctx)
	if actionErr != nil {
		_ = e.Ledger.RecordCheckpointFailed(ctx, h, runID, string(s), actionErr.Error())
		return actionErr
	}
	return e.Ledger.RecordCheckpointDone(ctx, h, runID, string(s), detail)
}

// stateStep é a ação (se houver) associada a cada estado da sequência pós-trava. Estados sem
// ação própria (LOCKED, BACKUP_VERIFIED, VERIFYING) só confirmam e avançam — a verificação já
// é o próprio sucesso do passo anterior.
var postLockSequence = []domain.RunState{
	domain.StateLocked, domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading,
	domain.StateVerifying, domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying,
	domain.StateHealthChecking, domain.StateObserving, domain.StateSucceeded,
}

func isPostLockState(s domain.RunState) bool {
	for _, st := range postLockSequence {
		if st == s {
			return true
		}
	}
	return false
}

// Execute avança um run do seu estado ATUAL até SUCCEEDED ou até uma falha — chamável a
// qualquer momento (primeira execução OU retomada após crash) com o MESMO efeito: nunca repete
// um passo já marcado "completed" no journal, sempre reavalia a partir do estado real do
// ledger. manifest é o manifesto JÁ validado (domain.ParseManifest) desta release.
func (e *Executor) Execute(ctx context.Context, h *LockHandle, runID string, manifest *domain.ReleaseManifest) error {
	for {
		run, err := e.Ledger.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if domain.IsTerminal(run.State) {
			return nil
		}
		// Achado Codex: FAILED e RECOVERING não aparecem em postLockSequence — sem este
		// desvio explícito, um crash do agente exatamente durante a decisão de recuperação
		// (depois de persistir FAILED/RECOVERING mas antes de concluir ROLLED_BACK ou
		// RECOVERY_REQUIRED) deixava o run PRESO para sempre: actionFor devolvia next="",
		// e o loop retornava nil sem nunca terminar a decisão. recover() é idempotente —
		// chamável tanto pela falha recém-ocorrida (handleFailure) quanto por uma retomada
		// a partir deste estado.
		if run.State == domain.StateFailed || run.State == domain.StateRecovering {
			return e.recover(ctx, h, runID, manifest)
		}

		// Achado Codex P2: CreateRun aceita qualquer estado inicial (incluindo os
		// PRÉ-trava: DISCOVERED/PLANNED/PREFLIGHTING/READY/AWAITING_APPROVAL — responsabilidade
		// do backend/planner F6, nunca deste agente), e ActiveRuns inclui todos os
		// não-terminais. Sem esta checagem, um run ativo nesse estado faria actionFor
		// devolver next="" (não está em postLockSequence), e o `if next == "" { return nil }`
		// mais abaixo reportaria SUCESSO silencioso sobre um run que na verdade nunca foi
		// tocado — Reconcile seguiria para o PRÓXIMO run da mesma instalação como se este
		// estivesse resolvido. Falhar explicitamente aqui torna esse estado inesperado
		// visível (e, via o gate de "parar a fila em erro de Execute" acima no chamador,
		// bloqueia processar runs mais novos por cima dele).
		if !isPostLockState(run.State) {
			return fmt.Errorf("agent: run %s está em %s — fora da sequência de execução do agente (trava até SUCCEEDED); isto é responsabilidade do backend/planner, nunca deste agente, e não pode ser tratado como concluído", runID, run.State)
		}

		// Achado Codex [P1], F9 (signing.yaml "anti_replay.revalidate_before_apply"): StartRun
		// só revalida revogação no momento do DESPACHO (gravar no ledger) — mas o apply real
		// (backup/download/migration/deploy) acontece AQUI, que pode ser bem mais tarde (poll
		// interval, daemon reiniciado depois de ter morrido). Sem esta checagem, uma release
		// revogada DEPOIS do despacho ainda seria instalada. Consulta o ledger LOCAL (nunca o
		// Postgres — este agente nunca depende da API estar disponível), e reusa EXATAMENTE a
		// mesma classificação de recuperação (handleFailure -> recover) que qualquer outra
		// falha real já usa: R0 se nada mutou ainda, isRollbackSafe/RECOVERY_REQUIRED caso
		// contrário — nunca uma lógica de bloqueio separada e menos testada.
		if revoked, reason, err := e.isRevokedOrUnsafe(ctx, manifest.ReleaseID); err != nil {
			return fmt.Errorf("agent: verificar revogação local antes de aplicar: %w", err)
		} else if revoked {
			return e.handleFailure(ctx, h, runID, run.State, manifest, fmt.Errorf("agent: release %s foi REVOGADA (%s) — recusando aplicar qualquer step novo", manifest.ReleaseID, reason))
		}

		next, action := e.actionFor(run.State, runID, manifest)
		if action != nil {
			// O Step registrado é o do PRÓXIMO estado (`next`), nunca do atual: a ação
			// retornada por actionFor(current) é exatamente a que PREPARA a transição para
			// `next` (ex. a partir de LOCKED, a ação é "backup", preparando BACKING_UP — não
			// um step associado a LOCKED, que não tem um). Gravar pelo estado atual deixava
			// o journal mentindo sobre qual ação realmente aconteceu em cada checkpoint.
			step := stepForState(next)
			if err := e.runStep(ctx, h, runID, step, action); err != nil {
				return e.handleFailure(ctx, h, runID, run.State, manifest, err)
			}
		}
		if next == "" {
			return nil // estado terminal alcançado sem mais transições (ex. já SUCCEEDED)
		}
		if next == domain.StateSucceeded {
			// Achado Codex P2: a ação de OBSERVING (acima, via actionFor/runStep) é pulável
			// por idempotência quando seu checkpoint já está "completed" — correto para a
			// execução FRESCA (nunca repetir a espera inteira da janela de observação), mas
			// perigoso numa RETOMADA: se o agente crashou DEPOIS de marcar esse checkpoint
			// completed mas ANTES de persistir esta transição para SUCCEEDED, os
			// containers podem ter degradado durante o tempo em que o agente esteve fora —
			// e a retomada pularia a ação (já "completed") e declararia sucesso sobre uma
			// instalação que já não está mais saudável. Esta revalidação é INCONDICIONAL,
			// nunca mediada por stepDone/runStep: uma leitura, nunca repete nada perigoso,
			// e corre toda vez que o loop está prestes a declarar sucesso — na execução
			// fresca é uma segunda confirmação redundante (inofensiva); numa retomada é a
			// ÚNICA confirmação que de fato acontece.
			if err := e.revalidateHealthBeforeSuccess(ctx, manifest); err != nil {
				return e.handleFailure(ctx, h, runID, run.State, manifest, err)
			}
			// Achado Codex [P1/P2], F9: a checagem de revogação no TOPO deste loop (acima) já
			// passou quando esta iteração começou — e revalidateHealthBeforeSuccess (logo
			// acima) pode BLOQUEAR por até HealthTimeout por unidade (WaitHealthy), uma janela
			// real onde a revogação ainda pode chegar ao ledger local. Por isso esta checagem
			// vem DEPOIS da revalidação de saúde, nunca antes — a ÚLTIMA coisa lida antes de
			// confirmar sucesso, o mais perto possível da transição em si. Sem isto, a próxima
			// iteração do loop nunca existiria (o `return nil` logo abaixo, depois de
			// TransitionRun, termina Execute) — uma release revogada exatamente nessa janela
			// seria reportada como SUCCEEDED em vez de RECOVERY_REQUIRED (R4). Mesma
			// classificação de recuperação de qualquer outra falha real (handleFailure ->
			// recover), nunca uma lógica de bloqueio separada.
			if revoked, reason, err := e.isRevokedOrUnsafe(ctx, manifest.ReleaseID); err != nil {
				return fmt.Errorf("agent: verificar revogação local antes de confirmar sucesso: %w", err)
			} else if revoked {
				return e.handleFailure(ctx, h, runID, run.State, manifest, fmt.Errorf("agent: release %s foi REVOGADA (%s) durante a validação final de saúde/observação — recusando confirmar sucesso", manifest.ReleaseID, reason))
			}
		}
		if err := e.Ledger.TransitionRun(ctx, h, runID, next); err != nil {
			return err
		}
		if next == domain.StateSucceeded {
			return nil
		}
	}
}

// revalidateHealthBeforeSuccess confirma, com uma leitura real e recente (nunca mais antiga
// que `e.HealthTimeout`), que todas as units do rollout estão saudáveis ANTES de declarar
// SUCCEEDED — ver o comentário em Execute sobre por que isto nunca pode ser pulado por
// idempotência.
func (e *Executor) revalidateHealthBeforeSuccess(ctx context.Context, manifest *domain.ReleaseManifest) error {
	digests, units := rolloutUnits(manifest)
	for _, u := range units {
		if err := e.Compose.WaitHealthy(ctx, u, e.HealthTimeout); err != nil {
			return fmt.Errorf("agent: %s não está saudável na revalidação final antes de SUCCEEDED: %w", u, err)
		}
	}
	// Achado Codex P1: WaitHealthy só confirma .State.Running/.State.Health.Status — NUNCA
	// qual IMAGEM está de fato rodando. Se o agente crashou depois de marcar o checkpoint de
	// DEPLOYING como completed e, antes da retomada, os containers foram substituídos por
	// uma versão ANTERIOR (ainda saudável — por um rollback manual, por outro processo, por
	// `docker compose up` de um compose file desatualizado fora do controle do agente), a
	// release ERRADA passaria por "saudável" e seria reportada como SUCCEEDED. Confirmar o
	// digest real contra o que esta release declarou fecha essa lacuna.
	snap, err := e.Compose.CurrentSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("agent: ler snapshot real para revalidação final antes de SUCCEEDED: %w", err)
	}
	for u, want := range digests {
		got := snap.Digests[u]
		if got != want {
			return fmt.Errorf("agent: %s está rodando %q, mas esta release implanta %q — a versão real difere da esperada na revalidação final", u, got, want)
		}
	}
	return nil
}

func stepForState(s domain.RunState) Step {
	switch s {
	case domain.StateBackingUp:
		return StepBackup
	case domain.StateDownloading:
		return StepDownload
	case domain.StatePreparing:
		return StepSnapshot
	case domain.StateMigratingExpand:
		return StepMigrate
	case domain.StateDeploying:
		return StepDeploy
	case domain.StateHealthChecking:
		return StepHealth
	case domain.StateObserving:
		return StepObserve
	default:
		return ""
	}
}

// actionFor devolve o próximo estado da sequência linear e a ação (se houver) a executar
// ANTES de transicionar para ele. Um estado sem ação própria (ex. BACKUP_VERIFIED) devolve
// action=nil — ele só confirma o sucesso do passo anterior e avança.
func (e *Executor) actionFor(current domain.RunState, runID string, manifest *domain.ReleaseManifest) (domain.RunState, func(ctx context.Context) (string, error)) {
	idx := -1
	for i, s := range postLockSequence {
		if s == current {
			idx = i
			break
		}
	}
	if idx == -1 || idx == len(postLockSequence)-1 {
		return "", nil
	}
	next := postLockSequence[idx+1]

	switch next {
	case domain.StateBackingUp:
		return next, func(ctx context.Context) (string, error) { return e.Backup.Run(ctx) }
	case domain.StateBackupVerified:
		return next, func(ctx context.Context) (string, error) {
			dumpPath, err := e.lastCompletedDetail(ctx, runID, StepBackup)
			if err != nil || dumpPath == "" {
				return "", fmt.Errorf("agent: sem evidência de backup para verificar (checkpoint de backup ausente): %w", err)
			}
			if err := e.BackupVerify.Verify(ctx, dumpPath); err != nil {
				return "", fmt.Errorf("agent: backup em %s não passou a verificação: %w", dumpPath, err)
			}
			return fmt.Sprintf("backup %s verificado", dumpPath), nil
		}
	case domain.StateDownloading:
		return next, func(ctx context.Context) (string, error) {
			digests, _ := rolloutUnits(manifest)
			for u, ref := range digests {
				if err := e.Compose.PullByDigest(ctx, ref); err != nil {
					return "", fmt.Errorf("pull de %s: %w", u, err)
				}
			}
			return fmt.Sprintf("%d artifact(s) baixados", len(digests)), nil
		}
	case domain.StateVerifying:
		return next, nil
	case domain.StatePreparing:
		return next, func(ctx context.Context) (string, error) {
			snap, err := e.Compose.CurrentSnapshot(ctx)
			if err != nil {
				return "", err
			}
			// Serializado como JSON (nunca só um "%+v" para log): este é o ÚNICO snapshot
			// que um Rollback real pode usar depois de uma falha — precisa ser desserializável
			// programaticamente (ver snapshotFromCheckpoint), não só legível por humano.
			data, err := json.Marshal(snap.Digests)
			if err != nil {
				return "", fmt.Errorf("agent: serializar snapshot: %w", err)
			}
			return string(data), nil
		}
	case domain.StateMigratingExpand:
		return next, func(ctx context.Context) (string, error) {
			if err := e.Migrate.Up(ctx, manifest.Migrations.Target, manifest.Migrations.ChecksumBundle); err != nil {
				return "", err
			}
			status, _ := e.Migrate.Status(ctx)
			return status, nil
		}
	case domain.StateDeploying:
		return next, func(ctx context.Context) (string, error) {
			digests, units := rolloutUnits(manifest)
			overridePath, err := e.Compose.WriteOverride(digests)
			if err != nil {
				return "", err
			}
			if err := e.Compose.Apply(ctx, overridePath, units); err != nil {
				return "", err
			}
			return overridePath, nil
		}
	case domain.StateHealthChecking:
		return next, func(ctx context.Context) (string, error) {
			_, units := rolloutUnits(manifest)
			for _, u := range units {
				if err := e.Compose.WaitHealthy(ctx, u, e.HealthTimeout); err != nil {
					return "", err
				}
			}
			return "todas as units do rollout saudáveis", nil
		}
	case domain.StateObserving:
		return next, func(ctx context.Context) (string, error) {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(e.ObserveWindow):
			}
			// Reconfirma saúde ao FIM da janela de observação — um workload que ficou
			// healthy por um instante e depois caiu não deve ser reportado como sucesso
			// (especificação §7.2 "notificar sucesso somente quando runtime verificado e
			// janela de observação concluída").
			_, units := rolloutUnits(manifest)
			for _, u := range units {
				if err := e.Compose.WaitHealthy(ctx, u, 5*time.Second); err != nil {
					return "", fmt.Errorf("%s deixou de estar saudável durante a observação: %w", u, err)
				}
			}
			return "janela de observação concluída, saúde reconfirmada", nil
		}
	case domain.StateSucceeded:
		return next, nil
	default:
		return next, nil
	}
}

// rolloutUnits resolve, a partir de manifest.Rollout.Workloads (as units que esta release
// DECLARA alterar — especificação §3) e manifest.Artifacts (de onde vem a imagem de cada), o
// conjunto exato de Units que este Executor deve baixar/aplicar/verificar. Achado Codex: um
// manifesto pode legitimamente ter artifacts para TODOS os componentes (um build conjunto) mas
// declarar em Rollout.Workloads só um SUBCONJUNTO como de fato alterado nesta release — usar
// manifest.Artifacts diretamente, sem filtrar por Rollout.Workloads, reiniciava/atualizava
// workloads fora do escopo aprovado da release. Esta função é a ÚNICA fonte do conjunto de
// units a tocar em download/deploy/health/observe/rollback — nunca iterar manifest.Artifacts
// diretamente nessas ações.
func rolloutUnits(manifest *domain.ReleaseManifest) (digests map[Unit]string, units []Unit) {
	byBuildUnit := map[domain.BuildUnit]string{}
	for _, a := range manifest.Artifacts {
		byBuildUnit[a.Unit] = a.Image
	}
	digests = map[Unit]string{}
	for _, w := range manifest.Rollout.Workloads {
		img, ok := byBuildUnit[w]
		if !ok {
			continue // um manifesto bem formado (domain.ParseManifest) nunca cai aqui; defensivo
		}
		u := Unit(unitFromBuildUnit(w))
		digests[u] = img
		units = append(units, u)
	}
	return digests, units
}

// unitFromBuildUnit traduz o nome de build_unit do domínio (ex. "omnira-api") para o nome do
// serviço Docker Compose real (ex. "api") — ver release/components.yaml deploy_units.
func unitFromBuildUnit(b domain.BuildUnit) string {
	switch b {
	case domain.UnitAPI:
		return "api"
	case domain.UnitWorker:
		return "worker"
	case domain.UnitWeb:
		return "web"
	default:
		return string(b)
	}
}

// handleFailure decide, ao vivo, se uma falha pós-trava pode ser revertida com segurança (só
// código/config) ou exige RECOVERY_REQUIRED (especificação §8, ADR-0054 §2.5). Nunca restaura
// dados automaticamente — rollback aqui é SEMPRE Compose Adapter.Rollback (digests), nunca uma
// migration `.down.sql` nem `make migrate-down`.
func (e *Executor) handleFailure(ctx context.Context, h *LockHandle, runID string, failedAt domain.RunState, manifest *domain.ReleaseManifest, cause error) error {
	if err := e.Ledger.TransitionRun(ctx, h, runID, domain.StateFailed); err != nil {
		return fmt.Errorf("agent: transicionar para FAILED após %v: %w (causa original: %v)", failedAt, err, cause)
	}
	return e.recover(ctx, h, runID, manifest)
}

// recover decide e aplica a classe de recuperação (especificação §8, ADR-0054 §2.5) — chamável
// tanto por handleFailure (falha recém-ocorrida, run acabou de virar FAILED) quanto por
// Execute diretamente numa RETOMADA (run já estava em FAILED ou RECOVERING quando o processo
// do agente reiniciou — achado Codex: sem esta função ser chamável independentemente, um
// crash exatamente nesta janela deixava o run preso para sempre sem decisão). Idempotente em
// qualquer ponto: isRollbackSafe/snapshotFromCheckpoint só leem estado; Compose.Rollback
// reaplica `docker compose up -d`, seguro de repetir mesmo que já tenha sido aplicado antes.
func (e *Executor) recover(ctx context.Context, h *LockHandle, runID string, manifest *domain.ReleaseManifest) error {
	run, err := e.Ledger.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if run.State == domain.StateFailed {
		if err := e.Ledger.TransitionRun(ctx, h, runID, domain.StateRecovering); err != nil {
			return err
		}
	}

	// Achado Codex P1: validar o fencing ANTES de qualquer mutação Docker — TransitionRun
	// acima já faz isso quando o run estava em FAILED, mas numa RETOMADA o run já pode estar
	// em RECOVERING (crash/restart do agente, ou uma segunda chamada de Reconcile), e nesse
	// caso o bloco acima é pulado inteiramente: sem esta checagem explícita, `recover`
	// chegaria a `Compose.Rollback` (uma mutação REAL) com um handle `h` já obsoleto, e só o
	// checkpoint/transição GRAVADO DEPOIS do rollback detectaria o fencing perdido — tarde
	// demais, a imagem antiga já teria sido reaplicada por cima do que outro holder possa já
	// estar fazendo. RecordCheckpointStart grava dentro de uma transação fencada
	// (checkFencing) e é seguro de repetir a cada chamada (idempotente no sentido de que só
	// adiciona uma entrada ao journal, nunca decide nada por si).
	if err := e.Ledger.RecordCheckpointStart(ctx, h, runID, "recovery_decision"); err != nil {
		return err
	}

	// Achado Codex P2: classificar R0 ANTES de checar isRollbackSafe — uma release pode
	// declarar recovery_class R3/R4 ou um database_strategy incompatível, mas se a falha
	// ocorreu ANTES de qualquer mutação real (nenhum snapshot de PREPARING concluído ainda),
	// essa incompatibilidade declarada é irrelevante: não há schema migrado nem workload
	// implantado para a release ser "incompatível" com. Checar isRollbackSafe PRIMEIRO
	// bloquearia a instalação com RECOVERY_REQUIRED por uma falha de backup/registry já
	// resolvida, mesmo que a release em si nunca tenha chegado a tocar o schema/deploy.
	//
	// Rollback seguro de código/config: só se já temos um snapshot PREPARING concluído e
	// RECUPERÁVEL — nunca o snapshot "atual" (que já pode refletir uma mutação parcial da
	// tentativa que falhou), sempre o que foi congelado ANTES de qualquer mutação.
	snap, err := e.snapshotFromCheckpoint(ctx, runID)
	if err != nil {
		// Falha ao LER o próprio ledger: não dá para saber se algo mutou ou não — nunca
		// assumir R0 por omissão aqui, o fallback seguro é exigir intervenção manual.
		_ = e.Ledger.RecordCheckpointFailed(ctx, h, runID, "recovery_decision", fmt.Sprintf("falha ao ler snapshot do journal — RECOVERY_REQUIRED: %v", err))
		return e.Ledger.TransitionRun(ctx, h, runID, domain.StateRecoveryRequired)
	}
	if snap == nil {
		// R0 (especificação §8: "antes de qualquer mutação: abortar, manter versão"). O
		// snapshot só é gravado ao FIM de PREPARING, que vem ANTES de
		// MIGRATING_EXPAND/DEPLOYING na sequência linear de postLockStates — se ele nunca
		// completou, nenhuma migração de schema e nenhum deploy de workload podem ter
		// ocorrido (a máquina de estados nunca avança fora de ordem), INDEPENDENTE do que a
		// release declara em database_strategy/recovery_class. ROLLED_BACK é o terminal
		// correto — a instalação nunca saiu do estado em que já estava, então "reverter" é
		// trivialmente verdadeiro, sem chamar Compose.Rollback.
		_ = e.Ledger.RecordCheckpointDone(ctx, h, runID, "recovery_decision", "R0: falha antes de qualquer mutação (sem snapshot de PREPARING concluído) — versão instalada preservada, nada a reverter")
		return e.Ledger.TransitionRun(ctx, h, runID, domain.StateRolledBack)
	}

	// Achado Codex [P1], F9 (ADR-0054 §2.4/§8, R4 "release revogada/comprometida: isolar,
	// nunca downgrade cego"): se a release foi REVOGADA e já houve mutação real, a classe de
	// recuperação DECLARADA pelo manifesto (R1/R2, "seguro reverter") é irrelevante — R4 é
	// sobre a dimensão de SEGURANÇA/proveniência, independente de compatibilidade de dados.
	// Reverter automaticamente para o digest anterior aqui seria um "forward-fix automático"
	// escondido como rollback normal — exatamente o que a especificação proíbe. Força
	// RECOVERY_REQUIRED incondicionalmente quando revogada, mesmo que isRollbackSafe(manifest)
	// diria "seguro" — nunca deixa a release decidir por si mesma que está segura de reverter.
	if revoked, reason, err := e.isRevokedOrUnsafe(ctx, manifest.ReleaseID); err != nil {
		_ = e.Ledger.RecordCheckpointFailed(ctx, h, runID, "recovery_decision", fmt.Sprintf("falha ao verificar revogação local — RECOVERY_REQUIRED (fail-closed): %v", err))
		return e.Ledger.TransitionRun(ctx, h, runID, domain.StateRecoveryRequired)
	} else if revoked {
		_ = e.Ledger.RecordCheckpointFailed(ctx, h, runID, "recovery_decision",
			fmt.Sprintf("R4: release %s está REVOGADA (%s) e já houve mutação real — RECOVERY_REQUIRED, nunca rollback automático (reverter cegamente para o digest anterior seria um forward-fix escondido; isolamento/recovery autorizado é manual)", manifest.ReleaseID, reason))
		return e.Ledger.TransitionRun(ctx, h, runID, domain.StateRecoveryRequired)
	}

	// A partir daqui HOUVE mutação real (snapshot existe) — agora sim a classe de
	// recuperação declarada pela release decide se reverter o código é seguro.
	if !isRollbackSafe(manifest) {
		_ = e.Ledger.RecordCheckpointFailed(ctx, h, runID, "recovery_decision",
			fmt.Sprintf("database_strategy=%s/recovery_class=%s — RECOVERY_REQUIRED, nunca rollback automático de dados", manifest.Compatibility.DatabaseStrategy, manifest.Rollout.RecoveryClass))
		return e.Ledger.TransitionRun(ctx, h, runID, domain.StateRecoveryRequired)
	}

	_, units := rolloutUnits(manifest)

	// Achado Codex [P1], F9 (rodada 6, estendido nas rodadas 7 e 8): a checagem de revogação
	// acima só olha a release ENTRANDO (manifest.ReleaseID) — mas `snap` restaura os digests
	// das releases que estavam rodando ANTES desta tentativa, UMA POR UNIT. rolloutUnits
	// permite releases que tocam só um SUBCONJUNTO dos workloads — "a release do run
	// SUCCEEDED mais recente" NÃO é necessariamente quem implantou o digest de CADA unit
	// sendo revertida (ex. release A atualiza só a API; release B, depois, atualiza só o
	// web; A é revogada; release C falha tentando atualizar a API de novo — o rollback da
	// API restauraria o digest de A, nunca de B, mesmo que B seja o SUCCEEDED mais recente
	// no geral). Por isso rollbackTargetReleaseIDs resolve a proveniência POR UNIT, nunca por
	// "a última release instalada, seja o que for que ela tenha tocado" — E confirma contra o
	// digest REAL em `snap`, nunca assume que o SUCCEEDED mais recente que tocou a unit é
	// necessariamente quem deixou o digest atual rodando (um RECOVERY_REQUIRED reconhecido
	// fora de banda, ou uma correção manual direta no host, pode deixar rodando um digest que
	// nenhum run SUCCEEDED jamais registrou).
	//
	// Uma unit AUSENTE do mapa devolvido (sem erro) é o caso legítimo de NENHUM run SUCCEEDED
	// rastreado por este ledger ter tocado essa unit ainda (ex. a primeira release já
	// instalada por este agente para essa unit, cujo "antes" foi definido fora deste
	// sistema, no provisionamento do host) — não há release_id nenhum para checar contra
	// revogação; o chamador segue sem gate extra para essa unit, exatamente como antes desta
	// checagem existir. Qualquer OUTRA ambiguidade (unit tocada por histórico conhecido, mas
	// nenhum digest bate com o real; ledger ilegível; manifesto corrompido) é fail-closed —
	// "não posso confirmar que é seguro" vira RECOVERY_REQUIRED para TODO o rollback.
	targets, terr := e.rollbackTargetReleaseIDs(ctx, run, units, snap)
	if terr != nil {
		_ = e.Ledger.RecordCheckpointFailed(ctx, h, runID, "recovery_decision",
			fmt.Sprintf("não foi possível identificar com segurança as releases que o rollback automático restauraria — RECOVERY_REQUIRED (fail-closed): %v", terr))
		return e.Ledger.TransitionRun(ctx, h, runID, domain.StateRecoveryRequired)
	}
	for _, u := range units {
		targetReleaseID, found := targets[u]
		if !found {
			continue
		}
		if revoked, reason, err := e.isRevokedOrUnsafe(ctx, targetReleaseID); err != nil {
			_ = e.Ledger.RecordCheckpointFailed(ctx, h, runID, "recovery_decision",
				fmt.Sprintf("falha ao verificar revogação da release de destino do rollback de %s (%s) — RECOVERY_REQUIRED (fail-closed): %v", u, targetReleaseID, err))
			return e.Ledger.TransitionRun(ctx, h, runID, domain.StateRecoveryRequired)
		} else if revoked {
			_ = e.Ledger.RecordCheckpointFailed(ctx, h, runID, "recovery_decision",
				fmt.Sprintf("R4: a release de destino do rollback automático de %s (%s) está REVOGADA (%s) — RECOVERY_REQUIRED, nunca reinstalar uma release revogada mesmo via rollback", u, targetReleaseID, reason))
			return e.Ledger.TransitionRun(ctx, h, runID, domain.StateRecoveryRequired)
		}
	}

	if err := e.Compose.Rollback(ctx, snap, units); err != nil {
		// O próprio Rollback recusa (nunca inventa um digest) se alguma unit não tiver um
		// valor utilizável no snapshot — isso também vira RECOVERY_REQUIRED, nunca uma
		// tentativa de "fazer o melhor possível".
		_ = e.Ledger.RecordCheckpointFailed(ctx, h, runID, "recovery_decision", fmt.Sprintf("rollback de código falhou: %v — RECOVERY_REQUIRED", err))
		return e.Ledger.TransitionRun(ctx, h, runID, domain.StateRecoveryRequired)
	}
	// Achado Codex: `Compose.Rollback` só executa `docker compose up -d` — não espera o
	// container ficar saudável. Se a versão ANTERIOR também não sobe de verdade (ex. um
	// problema de infraestrutura, não só do código novo), marcar ROLLED_BACK aqui estaria
	// mentindo sobre uma recuperação que não aconteceu de verdade. Sem confirmação de saúde,
	// isto também é RECOVERY_REQUIRED — nunca um sucesso presumido pela ausência de erro do
	// `docker compose up` em si.
	for _, u := range units {
		if err := e.Compose.WaitHealthy(ctx, u, e.HealthTimeout); err != nil {
			_ = e.Ledger.RecordCheckpointFailed(ctx, h, runID, "recovery_decision",
				fmt.Sprintf("rollback aplicado mas %s não ficou saudável: %v — RECOVERY_REQUIRED", u, err))
			return e.Ledger.TransitionRun(ctx, h, runID, domain.StateRecoveryRequired)
		}
	}
	_ = e.Ledger.RecordCheckpointDone(ctx, h, runID, "recovery_decision", "rollback de código para o snapshot anterior aplicado e confirmado saudável")
	return e.Ledger.TransitionRun(ctx, h, runID, domain.StateRolledBack)
}

// maxRevocationSyncAge (F9, achado Codex [P1] rodada 7) espelha release/policies/signing.yaml
// anti_replay.revocation.max_cache_age_seconds — o quanto o espelho LOCAL de revogação
// (ledger SQLite, ver Ledger.RevocationSyncFresh) pode envelhecer sem um sync bem-sucedido
// com o Postgres (updatemgr.redeliverPendingRevocations) antes de "não revogada" parar de ser
// uma resposta confiável.
const maxRevocationSyncAge = 300 * time.Second

// isRevokedOrUnsafe (F9, achado Codex [P1] rodada 7) é o ÚNICO ponto que decide se é seguro
// confiar na resposta local sobre revogação de releaseID — substitui toda chamada direta a
// Ledger.IsReleaseRevoked neste arquivo. Uma sincronização OBSOLETA (API inteira fora do ar
// por mais tempo do que qualquer retry individual de updatemgr cobre — achado anterior,
// entrega eventual via refreshPendingRun/StartRevocationDeliveryLoop) é tratada EXATAMENTE
// como revogação: fail-closed, nunca "presumo que está tudo bem" só porque não há registro
// local de revogação — a ausência de dados recentes nunca é confirmação de nada.
func (e *Executor) isRevokedOrUnsafe(ctx context.Context, releaseID string) (blocked bool, reason string, err error) {
	fresh, lastSync, ferr := e.Ledger.RevocationSyncFresh(ctx, maxRevocationSyncAge)
	if ferr != nil {
		return false, "", fmt.Errorf("verificar frescor da sincronização local de revogação: %w", ferr)
	}
	if !fresh {
		return true, fmt.Sprintf("sincronização local de revogação obsoleta (última: %s, limite: %s) — não é possível confirmar que %s não foi revogada", lastSync.Format(time.RFC3339), maxRevocationSyncAge, releaseID), nil
	}
	return e.Ledger.IsReleaseRevoked(ctx, releaseID)
}

// rollbackTargetReleaseIDs (F9, achado Codex [P1] rodadas 6, 7, 8 e 10) identifica, POR UNIT
// sendo revertida, a release que de fato implantou o digest que `snap` restauraria — nunca
// assume que "a release do run SUCCEEDED mais recente" cobre TODAS as units: rolloutUnits
// permite releases que tocam só um SUBCONJUNTO dos workloads (release A só atualiza a API,
// release B só atualiza o web — revertendo a API depois de uma falha de C restaura o digest
// de A, nunca de B, mesmo que B seja o SUCCEEDED mais recente no geral). Para cada unit em
// `units`, percorre o histórico de runs da instalação em ordem DECRESCENTE de CreatedAt (mais
// recente primeiro) e usa o primeiro run SUCCEEDED OU RECOVERY_REQUIRED, anterior ao run
// atual, cujo PRÓPRIO manifesto declara essa unit E cujo digest declarado para essa unit BATE
// com snap.Digests[unit] — achado Codex da rodada 8: "o SUCCEEDED mais recente que tocou esta
// unit" NUNCA é suficiente por si só, porque um RECOVERY_REQUIRED reconhecido fora de banda
// (AcknowledgeRecovery, ver ledger.go — um operador confirma que investigou e autorizou
// seguir, nunca que um digest específico ficou rodando) ou uma correção manual direta no host
// podem deixar rodando um digest que NENHUM run SUCCEEDED jamais registrou como "o atual"
// para essa unit; sem confirmar o digest, o rollback poderia atribuir a unit à release ERRADA
// e deixar passar (ou bloquear por engano) uma revogação. RECOVERY_REQUIRED entra na busca
// (achado Codex da rodada 10) porque recover() só alcança esse estado quando `snap != nil`
// (mutação real já aconteceu) — um run assim pode ter deployado seu próprio digest antes de
// falhar, e isso nunca é desfeito por AcknowledgeRecovery; excluí-lo da busca fazia uma unit
// cuja ÚNICA release conhecida estivesse presa em RECOVERY_REQUIRED cair, por engano, no caso
// de "nenhum histórico" — deixando passar o rollback sem checar revogação. ROLLED_BACK
// continua excluído: o PRÓPRIO digest desse run foi revertido, nunca é o que está rodando.
//
// Uma unit AUSENTE do mapa devolvido (sem erro) é o caso legítimo de NENHUM run
// SUCCEEDED/RECOVERY_REQUIRED rastreado por este ledger ter tocado essa unit ainda (ex. a
// primeira release já instalada por este agente para essa unit, cujo "antes" foi definido
// fora deste sistema, no provisionamento do host) — não há release_id nenhum para checar
// contra revogação; o chamador segue sem gate extra para essa unit, exatamente como antes
// desta checagem existir. Qualquer OUTRA ambiguidade — a unit FOI tocada por histórico
// conhecido, mas nenhum desses runs tem o digest que `snap` de fato reporta agora — é tratada
// como um ERRO (fail-closed), nunca como "nada a checar": "não posso confirmar que é seguro
// reverter", nunca "assumo que está tudo bem". Um erro de verdade na própria leitura (ledger
// ilegível, manifesto corrompido de um run que EXISTE) é outra coisa do mesmo tipo — o
// chamador trata ambos como fail-closed para TODO o rollback, nunca só para a unit afetada.
func (e *Executor) rollbackTargetReleaseIDs(ctx context.Context, run *RunRecord, units []Unit, snap *Snapshot) (map[Unit]string, error) {
	runs, err := e.Ledger.AllRuns(ctx, run.InstallationID)
	if err != nil {
		return nil, fmt.Errorf("listar runs da instalação: %w", err)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt.After(runs[j].CreatedAt) })

	remaining := make(map[Unit]bool, len(units))
	everTouched := make(map[Unit]bool, len(units))
	for _, u := range units {
		remaining[u] = true
	}
	result := make(map[Unit]string, len(units))
	for i := range runs {
		if len(remaining) == 0 {
			break
		}
		r := &runs[i]
		// Achado Codex [P1], F9 (rodada 10): SUCCEEDED não é o único estado em que um run
		// pode ter deixado um digest NOVO rodando. Um run que atinge RECOVERY_REQUIRED
		// (nunca ROLLED_BACK — ver recover(): snap==nil sempre resolve para ROLLED_BACK,
		// então RECOVERY_REQUIRED só é alcançado quando `snap != nil`, ou seja, mutação real
		// já aconteceu) pode ter deployado seu próprio digest ANTES de falhar — e
		// AcknowledgeRecovery nunca reverte isso, só desbloqueia novos runs. Excluir
		// RECOVERY_REQUIRED aqui fazia `everTouched[u]` permanecer false quando a ÚNICA
		// release que já tocou essa unit estava presa em RECOVERY_REQUIRED — caindo no
		// "nenhum histórico, bootstrap legítimo" por engano, e deixando o rollback passar
		// SEM checar a revogação dessa release. ROLLED_BACK continua excluído: o PRÓPRIO
		// digest desse run foi revertido, nunca é o que está rodando agora.
		if r.RunID == run.RunID || !r.CreatedAt.Before(run.CreatedAt) {
			continue
		}
		if r.State != domain.StateSucceeded && r.State != domain.StateRecoveryRequired {
			continue
		}
		m, perr := domain.ParseManifest([]byte(r.ManifestJSON))
		if perr != nil {
			return nil, fmt.Errorf("parsear manifesto do run anterior %s: %w", r.RunID, perr)
		}
		digests, touched := rolloutUnits(m)
		for _, u := range touched {
			if !remaining[u] {
				continue
			}
			everTouched[u] = true
			if digests[u] == snap.Digests[u] {
				result[u] = m.ReleaseID
				delete(remaining, u)
			}
		}
	}
	for u := range remaining {
		if everTouched[u] {
			return nil, fmt.Errorf("unit %s: o digest atual (%q) não corresponde a nenhuma release SUCCEEDED conhecida desta instalação — proveniência do rollback não pode ser confirmada", u, snap.Digests[u])
		}
	}
	return result, nil
}

// snapshotFromCheckpoint recupera o Snapshot serializado pela ação de PREPARING (StepSnapshot)
// deste run — o detail do checkpoint MAIS RECENTE com status "completed" para esse step.
// Retorna (nil, nil) se esse passo nunca foi concluído (ex. a falha ocorreu antes de chegar lá).
func (e *Executor) snapshotFromCheckpoint(ctx context.Context, runID string) (*Snapshot, error) {
	cps, err := e.Ledger.Checkpoints(ctx, runID)
	if err != nil {
		return nil, err
	}
	return SnapshotFromCheckpoints(cps)
}

// lastCompletedDetail devolve o `detail` do checkpoint MAIS RECENTE com status "completed"
// para `step` dentro de `runID` — "" (sem erro) se esse passo nunca foi concluído.
func (e *Executor) lastCompletedDetail(ctx context.Context, runID string, step Step) (string, error) {
	cps, err := e.Ledger.Checkpoints(ctx, runID)
	if err != nil {
		return "", err
	}
	return LastCompletedDetail(cps, step), nil
}

// LastCompletedDetail devolve o `detail` do checkpoint MAIS RECENTE com status "completed"
// para `step` em `checkpoints` (ex. de Ledger.Checkpoints) — "" se esse passo nunca
// completou. Função PURA (nunca toca o Ledger): usada internamente e por `updatectl`, que
// precisa da mesma leitura sem duplicar a lógica nem depender de nenhuma API.
func LastCompletedDetail(checkpoints []Checkpoint, step Step) string {
	for i := len(checkpoints) - 1; i >= 0; i-- {
		c := checkpoints[i]
		if c.Step == string(step) && c.Status == "completed" {
			return c.Detail
		}
	}
	return ""
}

// SnapshotFromCheckpoints desserializa o snapshot (unit -> digest) do checkpoint
// StepSnapshot mais recente CONCLUÍDO em `checkpoints` — (nil, nil) se PREPARING nunca
// completou (nunca houve mutação real, achado Codex R0). Usado por `updatectl rollback` para
// decidir o que reinstalar olhando só o ledger local, sem depender de nenhuma API.
func SnapshotFromCheckpoints(checkpoints []Checkpoint) (*Snapshot, error) {
	detail := LastCompletedDetail(checkpoints, StepSnapshot)
	if detail == "" {
		return nil, nil
	}
	var digests map[Unit]string
	if err := json.Unmarshal([]byte(detail), &digests); err != nil {
		return nil, fmt.Errorf("agent: desserializar snapshot do checkpoint: %w", err)
	}
	return &Snapshot{Digests: digests}, nil
}

// isRollbackSafe reproduz a tabela de classes de recuperação (especificação §8, ADR-0054
// §2.5: R1/R2 revertem só código quando o schema continua compatível; R3/R4 NUNCA permitem
// rollback automático). Dois critérios independentes, ambos precisam passar:
//  1. database_strategy garante que o schema (migrado ou não) aceita o código anterior —
//     expand_only/expand_then_contract_later/no_schema_change preservam isso por definição
//     (release/policies/compatibility.yaml); qualquer outro valor (incluindo desconhecido) é
//     tratado como NÃO seguro.
//  2. recovery_class declarado na própria release: achado Codex — ParseManifest valida
//     database_strategy e recovery_class INDEPENDENTEMENTE, então uma release pode (de forma
//     válida pelo schema) declarar R3 (corrupção/incompatibilidade de dados) ou R4 (release
//     revogada/comprometida) MESMO com um database_strategy compatível — nesses dois casos o
//     problema não é o schema, é que reintroduzir o código anterior não é seguro por outro
//     motivo (dado corrompido sob R3; reintroduzir uma versão comprometida sob R4). Só R1/R2
//     (ou a ausência de uma classe declarada, antes de ADR-0054 exigi-la em todo manifesto)
//     autorizam o rollback automático de código.
func isRollbackSafe(manifest *domain.ReleaseManifest) bool {
	switch manifest.Compatibility.DatabaseStrategy {
	case "no_schema_change", "expand_only", "expand_then_contract_later":
	default:
		return false
	}
	switch manifest.Rollout.RecoveryClass {
	case domain.RecoveryR3, domain.RecoveryR4:
		return false
	default:
		return true
	}
}

// Reconcile é o ponto de entrada chamado pelo daemon ao iniciar (primeira vez OU retomada
// após crash/restart) e, no caso normal, logo após um novo run ser criado. Adquire/renova o
// lock da instalação e avança todo run ainda ativo — nunca repetindo um passo perigoso já
// concluído (Execute já garante isso por run). manifests mapeia run_id -> manifesto já
// validado dessa release (o próprio run guarda o manifest_json congelado no momento do
// plano — o chamador é responsável por desserializá-lo e montar este mapa, mantendo este
// pacote livre de qualquer decisão sobre QUAL release aplicar, só COMO aplicá-la).
func (e *Executor) Reconcile(ctx context.Context, installationID, holder string, lease time.Duration, manifests map[string]*domain.ReleaseManifest) error {
	return e.WithLockAndHeartbeat(ctx, installationID, holder, lease, func(execCtx context.Context, h *LockHandle) error {
		// Achado Codex P1: este gate precisa ser consultado a cada chamada de Reconcile, não
		// só decidir dentro do loop abaixo — RECOVERY_REQUIRED é terminal (domain.IsTerminal),
		// então uma chamada SUBSEQUENTE de Reconcile (um novo processo do agente, ou o próximo
		// ciclo) nunca veria esse run de novo via ActiveRuns e processaria runs novos da
		// MESMA instalação como se nada tivesse acontecido. HasUnresolvedRecoveryRequired
		// consulta o ledger diretamente por esse estado, independente de ActiveRuns, e só é
		// destravado por um AcknowledgeRecovery explícito (operador via updatectl/runbook).
		pending, err := e.Ledger.HasUnresolvedRecoveryRequired(ctx, installationID)
		if err != nil {
			return fmt.Errorf("agent: reconcile: verificar recuperação pendente de %s: %w", installationID, err)
		}
		if pending {
			return fmt.Errorf("agent: reconcile: instalação %s tem uma recuperação pendente (RECOVERY_REQUIRED não reconhecido) — nenhum run novo será processado até intervenção manual", installationID)
		}

		runs, err := e.Ledger.ActiveRuns(ctx, installationID)
		if err != nil {
			return fmt.Errorf("agent: reconcile: listar runs ativos: %w", err)
		}
		return e.reconcileRuns(ctx, execCtx, h, runs, manifests)
	})
}

// WithLockAndHeartbeat adquire o lock da instalação, mantém um heartbeat em BACKGROUND
// durante toda a execução de `fn` (renovando o lease periodicamente — hbInterval = lease/3,
// nunca deixando a operação perder a posse do lock por simplesmente demorar mais que o lease
// original pedido), cancela execCtx no INSTANTE em que o heartbeat perde o fencing (matando
// qualquer exec.CommandContext em andamento antes que ele possa terminar de mutar algo sob um
// lock que já não se detém mais), e libera o lock ao final. Extraído de Reconcile para ser
// reutilizável por qualquer operação externa que precise da MESMA proteção para uma mutação
// potencialmente longa — hoje, além de Reconcile, `updatectl rollback` (achado Codex P1: uma
// versão anterior desse comando chamava Compose.Rollback diretamente, sem heartbeat nenhum,
// deixando uma janela real para o daemon assumir a instalação e reconciliar concorrentemente
// enquanto o rollback manual ainda estava mutando containers).
func (e *Executor) WithLockAndHeartbeat(ctx context.Context, installationID, holder string, lease time.Duration, fn func(execCtx context.Context, h *LockHandle) error) error {
	h, err := e.Ledger.AcquireLock(ctx, installationID, holder, lease)
	if err != nil {
		return fmt.Errorf("agent: adquirir lock de %s: %w", installationID, err)
	}
	// Achado Codex P2: sem isto, o lock ficava detido por ESTE holder até o lease expirar
	// NATURALMENTE, mesmo depois da operação já ter terminado (sucesso, erro, ou bloqueado
	// por RECOVERY_REQUIRED) — um operador tentando `updatectl` logo depois (ex. para chamar
	// AcknowledgeRecovery, que também precisa adquirir o lock) ficaria esperando sem motivo.
	// Registrado ANTES dos defers do heartbeat (LIFO: executa DEPOIS deles) — precisa rodar
	// só depois que a goroutine do heartbeat já tiver parado de verdade (<-hbDone já
	// retornou), nunca enquanto ela ainda pudesse tentar um Heartbeat concorrente sobre um
	// lock já liberado. context.Background() (nunca `ctx`, que pode já estar cancelado pelo
	// chamador) — liberar o lock é sempre válido e rápido, mesmo que a execução tenha sido
	// cancelada ou tenha falhado.
	defer func() { _ = e.Ledger.ReleaseLock(context.Background(), h) }()

	execCtx, cancel := context.WithCancel(ctx)
	hbInterval := lease / 3
	if hbInterval <= 0 {
		hbInterval = time.Second
	}
	hbDone := make(chan struct{})
	// ORDEM DOS DEFERS IMPORTA (bug real encontrado rodando este teste — ver
	// TestReconcile_HeartbeatKeepsLockAliveDuringLongObservation): defers executam LIFO.
	// `<-hbDone` tem que ser registrado ANTES de `cancel()` para executar DEPOIS dele — a
	// goroutine do heartbeat só termina quando execCtx.Done() dispara (ou quando ela mesma
	// chama cancel() ao perder o fencing), e isso só acontece quando cancel() É CHAMADO.
	// Registrar `<-hbDone` primeiro e `cancel()` depois (como estava antes) fazia o chamador
	// esperar a goroutine terminar ANTES de dar a ela qualquer motivo para terminar —
	// deadlock garantido no caminho feliz, sem nenhum erro de heartbeat envolvido.
	defer func() { <-hbDone }()
	defer cancel()
	go func() {
		defer close(hbDone)
		ticker := time.NewTicker(hbInterval)
		defer ticker.Stop()
		for {
			select {
			case <-execCtx.Done():
				return
			case <-ticker.C:
				if err := e.Ledger.Heartbeat(ctx, h); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	return fn(execCtx, h)
}

// reconcileRuns é o corpo do loop de Reconcile — extraído para reduzir aninhamento depois da
// refatoração de WithLockAndHeartbeat, nunca chamado diretamente fora de Reconcile.
func (e *Executor) reconcileRuns(ctx, execCtx context.Context, h *LockHandle, runs []RunRecord, manifests map[string]*domain.ReleaseManifest) error {
	var firstErr error
	for _, run := range runs {
		manifest, ok := manifests[run.RunID]
		if !ok {
			// Achado Codex P1: mesmo risco do erro de Execute abaixo — este run pode estar
			// no meio de um deploy/recuperação, e processar um run POSTERIOR da mesma
			// instalação sem resolver este primeiro poderia invalidar o snapshot de
			// rollback dele ou, numa retomada futura, desfazer a release mais nova. Nunca
			// "pular e seguir" — para a fila inteira desta instalação.
			if firstErr == nil {
				firstErr = fmt.Errorf("agent: reconcile: run %s ativo no ledger mas sem manifesto fornecido pelo chamador — interrompendo a fila desta instalação", run.RunID)
			}
			break
		}
		if err := e.Execute(execCtx, h, run.RunID, manifest); err != nil {
			// Achado Codex P1: um erro de Execute (diferente de retornar nil com o run já
			// num terminal "resolvido" como SUCCEEDED/ROLLED_BACK/RECOVERY_REQUIRED) significa
			// que não sabemos se este run chegou a um estado seguro — por exemplo, o deploy
			// pode ter mutado containers reais mas falhado ao persistir o checkpoint
			// correspondente, ou a própria decisão de recuperação pode não ter sido
			// concluída. Processar o PRÓXIMO run da mesma instalação aqui poderia implantar
			// uma release mais nova por cima de um estado desconhecido — ou, pior, uma
			// retomada futura reaplicar o snapshot de rollback ERRADO (de antes do deploy
			// não confirmado). Para a fila desta instalação inteira, nunca só este run.
			if firstErr == nil {
				firstErr = fmt.Errorf("agent: reconcile: run %s falhou de forma não resolvida — interrompendo a fila desta instalação: %w", run.RunID, err)
			}
			break
		}
		// Mesmo gate acima, mas DENTRO deste mesmo ciclo de Reconcile: se este run acabou de
		// cair em RECOVERY_REQUIRED, os runs SEGUINTES deste mesmo loop (mesma instalação)
		// nunca devem ser processados — o lock da instalação só serializa essas ações, nunca
		// as impede; é esta checagem que de fato para a fila sem esperar a próxima chamada.
		got, err := e.Ledger.GetRun(ctx, run.RunID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		if got.State == domain.StateRecoveryRequired {
			if firstErr == nil {
				firstErr = fmt.Errorf("agent: reconcile: run %s terminou em RECOVERY_REQUIRED — interrompendo a fila desta instalação até intervenção manual", run.RunID)
			}
			break
		}
	}
	return firstErr
}
