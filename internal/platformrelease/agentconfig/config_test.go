package agentconfig

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/platformrelease/agent"
	"github.com/omnira/omnira/internal/platformrelease/domain"
)

func TestFlagSet_DefaultsFromEnv(t *testing.T) {
	t.Setenv("OMNIRA_AGENT_INSTALLATION_ID", "inst-env")
	t.Setenv("OMNIRA_AGENT_COMPOSE_BASE_FILES", "a.yml:b.yml:c.yml")
	t.Setenv("OMNIRA_AGENT_LEASE", "90s")
	t.Setenv("OMNIRA_AGENT_COMPOSE_SUDO", "true")

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := FlagSet(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.InstallationID != "inst-env" {
		t.Fatalf("esperava installation-id do env, veio %q", cfg.InstallationID)
	}
	if strings.Join([]string(cfg.ComposeBaseFiles), ":") != "a.yml:b.yml:c.yml" {
		t.Fatalf("esperava compose-base-files do env, veio %v", cfg.ComposeBaseFiles)
	}
	if cfg.Lease != 90*time.Second {
		t.Fatalf("esperava lease=90s do env, veio %s", cfg.Lease)
	}
	if !cfg.ComposeSudo {
		t.Fatal("esperava compose-sudo=true do env")
	}
}

// TestFlagSet_DatabaseURLNeverExposedAsFlagDefault: achado Codex P2 — flag.FlagSet grava o
// 3º argumento de StringVar como DefValue, impresso LITERALMENTE por Usage()/-h/qualquer
// erro de parse. Se DATABASE_URL contém uma senha (o schema owner, nunca omnira_app), um
// default "lido do ambiente" vazaria a credencial em qualquer diagnóstico capturado
// (AGENTS.md: "nenhuma automação pode... retornar credenciais desnecessariamente").
func TestFlagSet_DatabaseURLNeverExposedAsFlagDefault(t *testing.T) {
	t.Setenv("OMNIRA_AGENT_DATABASE_URL", "postgres://omnira_owner:super-secret-password@db:5432/omnira")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	FlagSet(fs)
	fl := fs.Lookup("database-url")
	if fl == nil {
		t.Fatal("flag --database-url deveria existir")
	}
	if strings.Contains(fl.DefValue, "super-secret-password") {
		t.Fatalf("o DefValue da flag NUNCA pode conter a credencial (seria impresso por -h/erros de parse), veio %q", fl.DefValue)
	}
	if fl.DefValue != "" {
		t.Fatalf("esperava DefValue vazio (o fallback do env é resolvido depois do parse, nunca como default), veio %q", fl.DefValue)
	}
}

// TestConfig_ApplyEnvFallbacks_ResolvesDatabaseURLFromEnv: confirma que, mesmo com o
// default da flag vazio, o fallback para OMNIRA_AGENT_DATABASE_URL/DATABASE_URL continua
// funcionando de verdade — via Validate()/ValidateLease(), chamados por todo comando que
// de fato usa DatabaseURL.
func TestConfig_ApplyEnvFallbacks_ResolvesDatabaseURLFromEnv(t *testing.T) {
	t.Setenv("OMNIRA_AGENT_DATABASE_URL", "postgres://x:y@host/db")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := FlagSet(fs)
	cfg.InstallationID = "inst-1"
	cfg.ComposeBaseFiles = []string{"docker-compose.yml"}
	cfg.PollInterval = 15 * time.Second
	cfg.Lease = time.Minute
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.DatabaseURL != "" {
		t.Fatalf("esperava DatabaseURL vazio logo após o Parse (resolvido só em Validate), veio %q", cfg.DatabaseURL)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if cfg.DatabaseURL != "postgres://x:y@host/db" {
		t.Fatalf("esperava o fallback do env resolvido por Validate, veio %q", cfg.DatabaseURL)
	}
}

func TestFlagSet_ExplicitFlagWinsOverEnv(t *testing.T) {
	t.Setenv("OMNIRA_AGENT_INSTALLATION_ID", "inst-env")

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := FlagSet(fs)
	if err := fs.Parse([]string{"--installation-id", "inst-flag"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.InstallationID != "inst-flag" {
		t.Fatalf("uma flag explícita deveria vencer o env, veio %q", cfg.InstallationID)
	}
}

func TestFlagSet_ComposeBaseFilesOrderPreserved(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg := FlagSet(fs)
	if err := fs.Parse([]string{"--compose-base-files", "base.yml:override.yml:local.yml"}); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []string{"base.yml", "override.yml", "local.yml"}
	got := []string(cfg.ComposeBaseFiles)
	if len(got) != len(want) {
		t.Fatalf("esperava %v, veio %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			// A ORDEM importa de verdade: o Docker Compose aplica a ÚLTIMA entrada como a
			// que vence em caso de conflito — embaralhar a ordem mudaria qual arquivo
			// prevalece silenciosamente.
			t.Fatalf("ordem alterada: esperava %v, veio %v", want, got)
		}
	}
}

func TestConfig_Validate(t *testing.T) {
	base := func() *Config {
		return &Config{
			InstallationID:   "inst-1",
			DatabaseURL:      "postgres://x",
			ComposeBaseFiles: []string{"docker-compose.yml"},
			PollInterval:     15 * time.Second,
			Lease:            time.Minute,
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("configuração completa deveria validar, veio: %v", err)
	}

	c := base()
	c.InstallationID = ""
	if err := c.Validate(); err == nil {
		t.Fatal("deveria recusar installation_id vazio")
	}

	// Achado Codex P2: Validate()/Build() NÃO exigem mais database_url — 'rollback' nunca
	// migra e precisa funcionar sem credenciais de banco (ex. um operador de emergência sem
	// acesso ao cofre de segredos). Quem de fato pode migrar chama RequireDatabaseURL()
	// explicitamente.
	c = base()
	c.DatabaseURL = ""
	if err := c.Validate(); err != nil {
		t.Fatalf("Validate NÃO deveria exigir database_url (ver RequireDatabaseURL), veio: %v", err)
	}
	if err := c.RequireDatabaseURL(); err == nil {
		t.Fatal("RequireDatabaseURL deveria recusar database_url vazio")
	}

	c = base()
	c.ComposeBaseFiles = nil
	if err := c.Validate(); err == nil {
		t.Fatal("deveria recusar compose_base_files vazio")
	}

	// Achado Codex P2: time.NewTicker panica com uma duração <= 0 — poll_interval
	// zero/negativo precisa ser recusado AQUI, antes de qualquer mutação, nunca deixado
	// para o daemon descobrir via um crash depois da primeira reconciliação.
	for _, invalid := range []time.Duration{0, -time.Second} {
		c = base()
		c.PollInterval = invalid
		if err := c.Validate(); err == nil {
			t.Fatalf("deveria recusar poll_interval=%s", invalid)
		}
	}

	// Achado Codex P2: Ledger.AcquireLock só grava lease_ms (precisão de milissegundo) —
	// um lease zero/negativo/< 1ms deixaria outro processo roubar o lock da instalação
	// IMEDIATAMENTE enquanto uma mutação ainda está em andamento sob o lock anterior.
	for _, invalid := range []time.Duration{0, -time.Second, 500 * time.Microsecond} {
		c = base()
		c.Lease = invalid
		if err := c.Validate(); err == nil {
			t.Fatalf("deveria recusar lease=%s", invalid)
		}
		if err := c.ValidateLease(); err == nil {
			t.Fatalf("ValidateLease deveria recusar lease=%s (chamado por comandos que não passam por Build)", invalid)
		}
	}
}

func TestConfig_Build_RejectsInvalidConfigBeforeOpeningLedger(t *testing.T) {
	c := &Config{} // sem installation_id/database_url
	_, _, err := c.Build()
	if err == nil {
		t.Fatal("Build deveria recusar uma config inválida sem nunca abrir o ledger")
	}
}

// TestConfig_Build_PropagatesPGContainerAndOwnerToBackupRunner: achado Codex P1 — sem expor
// PGContainer/PGOwner e propagá-los para o BackupRunner, o script sempre usaria seus
// defaults ("omnira-postgres"/"omnira"), mesmo numa instalação configurada com nomes
// diferentes — e, com --compose-sudo, nem um b.Env configurado manualmente sobreviveria ao
// env_reset do sudo sem a mesma correção de execBackupRunner.Run (ver
// internal/platformrelease/agent/reconcile_test.go,
// TestExecBackupRunner_EnvSurvivesSudoEnvReset). Prova real: um script fake que falha
// explicitamente se PG_CONTAINER/PG_OWNER não chegarem com os valores CONFIGURADOS (nunca os
// defaults do script), chamado através de exec.Backup montado inteiramente por Build().
func TestConfig_Build_PropagatesPGContainerAndOwnerToBackupRunner(t *testing.T) {
	workDir := t.TempDir()
	script := filepath.Join(workDir, "fake-backup.sh")
	scriptContent := "#!/bin/sh\n" +
		"[ \"$PG_CONTAINER\" = \"custom-pg\" ] || { echo \"PG_CONTAINER=$PG_CONTAINER (esperava custom-pg)\" >&2; exit 1; }\n" +
		"[ \"$PG_OWNER\" = \"custom-owner\" ] || { echo \"PG_OWNER=$PG_OWNER (esperava custom-owner)\" >&2; exit 1; }\n" +
		"dir=\"$BACKUP_DIR/$DB_NAME\"\n" +
		"mkdir -p \"$dir\"\n" +
		"echo conteudo > \"$dir/${DB_NAME}_20260101T000000Z.dump\"\n" +
		"echo backup ok\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("escrever script fake: %v", err)
	}
	c := &Config{
		InstallationID:   "inst-1",
		DatabaseURL:      "postgres://x",
		ComposeBaseFiles: []string{"docker-compose.yml"},
		PollInterval:     15 * time.Second,
		Lease:            time.Minute,
		LedgerPath:       filepath.Join(workDir, "ledger.db"),
		BackupScript:     script,
		BackupDir:        filepath.Join(workDir, "backups"),
		DBName:           "fakedb",
		PGContainer:      "custom-pg",
		PGOwner:          "custom-owner",
	}
	exec, ledger, err := c.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ledger.Close()
	if _, err := exec.Backup.Run(context.Background()); err != nil {
		t.Fatalf("Backup.Run: %v", err)
	}
}

// fakeBackupRunnerErr: um BackupRunner que sempre falha — usado para provar a
// desserialização do manifesto pelo ReconcileOnce sem precisar de um ComposeAdapter real
// (*agent.ComposeAdapter é um ponteiro de struct concreto, não uma interface; deixá-lo nil e
// chegar a tocá-lo causaria um panic de nil pointer, não um erro limpo — a falha de backup
// faz o run resolver via R0 bem antes disso).
type fakeBackupRunnerErr struct{}

func (fakeBackupRunnerErr) Run(context.Context) (string, error) {
	return "", errors.New("backup indisponível (simulado)")
}

func TestReconcileOnce_DeserializesManifestFromLedgerAndReconciles(t *testing.T) {
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()
	ctx := context.Background()
	h, err := ledger.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", testManifestFixture, domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := ledger.ReleaseLock(ctx, h); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}

	exec := &agent.Executor{
		Ledger:        ledger,
		Backup:        fakeBackupRunnerErr{},
		HealthTimeout: time.Second,
		ObserveWindow: 10 * time.Millisecond,
	}

	// ReconcileOnce lê o manifesto do ledger, desserializa (domain.ParseManifest) e o passa
	// para Execute — a falha de backup ocorre ANTES de qualquer mutação (R0), então o run
	// resolve para ROLLED_BACK sem nunca precisar tocar Compose/Migrate. Execute()
	// trata isso como "recuperação bem-sucedida" e retorna nil — prova de que o manifesto
	// REAL do ledger foi lido e usado (um erro de parse teria interrompido a fila antes
	// disso, com uma mensagem diferente).
	if err := ReconcileOnce(ctx, exec, ledger, "inst-1", "holder-a", time.Minute); err != nil {
		t.Fatalf("ReconcileOnce não deveria falhar quando a recuperação em si é bem-sucedida: %v", err)
	}

	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateRolledBack {
		t.Fatalf("esperava ROLLED_BACK (R0: falha de backup antes de qualquer mutação), veio %s", got.State)
	}
}

func TestReconcileOnce_NoActiveRuns_NoError(t *testing.T) {
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()
	exec := &agent.Executor{Ledger: ledger}
	if err := ReconcileOnce(context.Background(), exec, ledger, "inst-sem-runs", "holder-a", time.Minute); err != nil {
		t.Fatalf("ReconcileOnce sem runs ativos nunca deveria falhar: %v", err)
	}
}

func TestReconcileOnce_SkipsRunWithInvalidManifestButStopsQueue(t *testing.T) {
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()
	ctx := context.Background()
	h, err := ledger.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	// manifest_json inválido, congelado no ledger — nunca deveria acontecer no fluxo normal
	// (updatectl start já valida antes de criar), mas precisa ser tratado sem derrubar o
	// processo, e sem permitir que runs mais novos avancem por cima de um estado
	// desconhecido.
	if _, err := ledger.CreateRun(ctx, h, "run-bad", "idem-bad", `{"not":"valid"}`, domain.StateLocked); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := ledger.ReleaseLock(ctx, h); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}

	exec := &agent.Executor{Ledger: ledger}
	err = ReconcileOnce(ctx, exec, ledger, "inst-1", "holder-a", time.Minute)
	if err == nil {
		t.Fatal("ReconcileOnce deveria reportar o manifesto inválido e nunca reportar sucesso silencioso")
	}
}

const testManifestFixture = `{
  "schema_version": 1,
  "release_id": "omnira-2.4.0",
  "platform_version": "2.4.0",
  "channel": "stable",
  "source": {"repository": "keslleykledston/20-OMNIRA", "commit_sha": "4f6b5c1e2a9d8f3b7c0a1e4d9f2b6c8a0e1d3f5b", "workflow_run_id": "123456789"},
  "artifacts": [
    {"unit": "omnira-api", "image": "ghcr.io/keslleykledston/omnira-api@sha256:1111111111111111111111111111111111111111111111111111111111111111"},
    {"unit": "omnira-worker", "image": "ghcr.io/keslleykledston/omnira-worker@sha256:2222222222222222222222222222222222222222222222222222222222222222"},
    {"unit": "omnira-web", "image": "ghcr.io/keslleykledston/omnira-web@sha256:3333333333333333333333333333333333333333333333333333333333333333"}
  ],
  "modules_changed": ["platform_release"],
  "compatibility": {"from_versions": ["2.3.x"], "min_agent_protocol": 1, "database_strategy": "expand_then_contract_later"},
  "migrations": {"start_after": "000168", "target": "000172", "checksum_bundle": "sha256:4444444444444444444444444444444444444444444444444444444444444444"},
  "rollout": {"workloads": ["omnira-api", "omnira-worker", "omnira-web"], "preflight_policy": "strict", "recovery_class": "R1"},
  "approved_by": "release-policy-stable-v1"
}`
