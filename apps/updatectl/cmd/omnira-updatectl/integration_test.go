package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/platformrelease/agent"
	"github.com/omnira/omnira/internal/platformrelease/agentconfig"
	"github.com/omnira/omnira/internal/platformrelease/domain"
)

// requireIntegration é o mesmo padrão de guarda de
// internal/platformrelease/agent/compose_integration_test.go: sem
// OMNIRA_COMPOSE_INTEGRATION_TEST=1 explícito, este teste pula de forma limpa — ele precisa
// de Docker real e de um registry efêmero já publicado com duas imagens de teste (ver
// scripts/release/test-update-agent-cli.sh). Reaproveita as MESMAS variáveis de ambiente que
// o teste de integração do Compose Adapter.
func requireIntegration(t *testing.T) (v1Ref, v2Ref, project string, baseFiles []string) {
	t.Helper()
	if os.Getenv("OMNIRA_COMPOSE_INTEGRATION_TEST") != "1" {
		t.Skip("OMNIRA_COMPOSE_INTEGRATION_TEST não é 1 — pulando teste de integração ponta a ponta do updatectl (precisa de Docker real)")
	}
	v1Ref = os.Getenv("OMNIRA_COMPOSE_TEST_IMAGE_V1")
	v2Ref = os.Getenv("OMNIRA_COMPOSE_TEST_IMAGE_V2")
	project = os.Getenv("OMNIRA_COMPOSE_TEST_PROJECT")
	baseFilesRaw := os.Getenv("OMNIRA_COMPOSE_TEST_BASE_FILES")
	if v1Ref == "" || v2Ref == "" || project == "" || baseFilesRaw == "" {
		t.Fatal("OMNIRA_COMPOSE_INTEGRATION_TEST=1 mas faltam OMNIRA_COMPOSE_TEST_IMAGE_V1/V2/PROJECT/BASE_FILES")
	}
	baseFiles = strings.Split(baseFilesRaw, ":")
	if project == "omnira" || strings.HasPrefix(project, "omnira-hl") {
		t.Fatalf("project %q parece ser uma stack real, recusando usá-lo em teste", project)
	}
	return
}

// fakeManifestJSON: domain.ParseManifest exige artifacts[].image no formato
// "ghcr.io/.../...@sha256:<64 hex>" — uma regra de SEGURANÇA deliberada (nunca aceitar
// imagem de um registry arbitrário numa release real), não contornável nem necessária aqui:
// 'rollback' NUNCA baixa essa imagem — ele usa o SNAPSHOT já gravado no ledger (o digest
// REAL do registry de teste local, gravado manualmente abaixo, simulando o que PREPARING
// teria gravado antes de um upgrade real). O manifesto só precisa VALIDAR sintaticamente
// para que isRollbackSafe/rolloutUnits leiam database_strategy/recovery_class/workloads.
const fakeManifestJSON = `{
  "schema_version": 1,
  "release_id": "omnira-9.9.9",
  "platform_version": "9.9.9",
  "channel": "candidate",
  "source": {"repository": "omnira/omnira", "commit_sha": "4f6b5c1e2a9d8f3b7c0a1e4d9f2b6c8a0e1d3f5b", "workflow_run_id": "1"},
  "artifacts": [{"unit": "omnira-api", "image": "ghcr.io/omnira/omnira-api@sha256:1111111111111111111111111111111111111111111111111111111111111111"}],
  "modules_changed": ["platform_release"],
  "compatibility": {"from_versions": ["0.9.0"], "min_agent_protocol": 1, "database_strategy": "expand_only"},
  "migrations": {"start_after": "000000", "target": "000000", "checksum_bundle": "sha256:4444444444444444444444444444444444444444444444444444444444444444"},
  "rollout": {"workloads": ["omnira-api"], "preflight_policy": "strict", "recovery_class": "R1"},
  "approved_by": "integration-test"
}`

// TestUpdatectl_Integration_RollbackForcesStuckRecoveryDecision: prova, com Docker DE
// VERDADE, que 'updatectl rollback' — agora delegando inteiramente para
// Executor.Execute/recover() — reinstala de verdade o snapshot de uma release anterior para
// um run travado em FAILED, confirmado pelo próprio healthcheck da imagem de teste (sensível
// à versão realmente rodando). Simula o cenário real da especificação §8 "CLI local para
// forçar uma decisão de recuperação quando a API/o daemon estão indisponíveis": v2 já está
// deployada (feita diretamente via ComposeAdapter, simulando um upgrade real que já ocorreu),
// um run existe no ledger em FAILED com o snapshot de v1 já gravado (como PREPARING real
// teria gravado antes desse upgrade), e 'rollback --yes' força a conclusão da recuperação —
// que só tem sucesso real porque v1 de fato fica saudável de novo.
func TestUpdatectl_Integration_RollbackForcesStuckRecoveryDecision(t *testing.T) {
	v1, v2, project, baseFiles := requireIntegration(t)
	workDir := t.TempDir()
	ctx := context.Background()

	cfg := &agentconfig.Config{
		InstallationID:     "integration-test-inst",
		Holder:             "integration-test-holder",
		LedgerPath:         filepath.Join(workDir, "ledger.db"),
		Lease:              time.Minute,
		PollInterval:       time.Minute, // nunca usado por rollback (só o daemon usa), mas Validate() exige > 0
		HealthTimeout:      30 * time.Second,
		ObserveWindow:      time.Second,
		ComposeBaseFiles:   baseFiles,
		ComposeOverrideDir: filepath.Join(workDir, "overrides"),
		ComposeProject:     project,
		ComposeSudo:        true,
		MigrateScript:      "/bin/true", // nunca invocado: recover() nunca re-migra
		MigrationsDir:      workDir,
		DatabaseURL:        "postgres://fake",
		BackupScript:       "/bin/true",
		BackupDir:          workDir,
		DBName:             "fakedb",
	}

	exec, ledger, err := cfg.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ledger.Close()

	// v2 já "deployada" — como um Reconcile real teria feito, chamado diretamente aqui para
	// não precisar de um pull GHCR real só para colocar a unit no ar antes do teste.
	if err := exec.Compose.PullByDigest(ctx, v2); err != nil {
		t.Fatalf("pull v2: %v", err)
	}
	overrideV2, err := exec.Compose.WriteOverride(map[agent.Unit]string{agent.UnitAPI: v2})
	if err != nil {
		t.Fatalf("WriteOverride v2: %v", err)
	}
	if err := exec.Compose.Apply(ctx, overrideV2, []agent.Unit{agent.UnitAPI}); err != nil {
		t.Fatalf("Apply v2: %v", err)
	}
	if err := exec.Compose.WaitHealthy(ctx, agent.UnitAPI, 30*time.Second); err != nil {
		t.Fatalf("v2 não ficou saudável: %v", err)
	}

	// Run travado em FAILED, com o snapshot de v1 já gravado — exatamente o que PREPARING
	// grava antes de um upgrade real, e que recover()/SnapshotFromCheckpoints leem de volta.
	h, err := ledger.AcquireLock(ctx, cfg.InstallationID, cfg.Holder, cfg.Lease)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	run, err := ledger.CreateRun(ctx, h, "run-v2-upgrade", "idem-v2", fakeManifestJSON, domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	digestsJSON, err := json.Marshal(map[agent.Unit]string{agent.UnitAPI: v1})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := ledger.RecordCheckpointDone(ctx, h, run.RunID, "snapshot", string(digestsJSON)); err != nil {
		t.Fatalf("RecordCheckpointDone: %v", err)
	}
	if err := ledger.TransitionRun(ctx, h, run.RunID, domain.StateFailed); err != nil {
		t.Fatalf("TransitionRun FAILED: %v", err)
	}
	if err := ledger.ReleaseLock(ctx, h); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}

	cliArgs := []string{
		"--installation-id", cfg.InstallationID,
		"--holder", cfg.Holder,
		"--ledger-path", cfg.LedgerPath,
		"--lease", "1m",
		"--health-timeout", "30s",
		"--compose-base-files", strings.Join(baseFiles, ":"),
		"--compose-override-dir", cfg.ComposeOverrideDir,
		"--compose-project", project,
		"--compose-sudo",
		"--migrate-script", "/bin/true",
		"--migrations-dir", workDir,
		"--database-url", "postgres://fake",
		"--backup-script", "/bin/true",
		"--backup-dir", workDir,
		"--db-name", "fakedb",
		"--run", run.RunID,
		"--yes",
	}
	var out bytes.Buffer
	if err := runRollback(cliArgs, &out, strings.NewReader("")); err != nil {
		t.Fatalf("rollback: %v\nsaída: %s", err, out.String())
	}
	if !strings.Contains(out.String(), "rollback aplicado e confirmado saudável") {
		t.Fatalf("esperava confirmação de rollback saudável, veio: %s", out.String())
	}

	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateRolledBack {
		t.Fatalf("esperava o run resolvido para ROLLED_BACK, veio %s", got.State)
	}

	snap, err := exec.Compose.CurrentSnapshot(ctx)
	if err != nil {
		t.Fatalf("CurrentSnapshot pós-rollback: %v", err)
	}
	if snap.Digests[agent.UnitAPI] != v1 {
		t.Fatalf("esperava a unit de volta em v1 (%s) depois do rollback, veio %q", v1, snap.Digests[agent.UnitAPI])
	}
}
