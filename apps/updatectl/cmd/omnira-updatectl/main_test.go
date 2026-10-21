package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/platformrelease/agent"
	"github.com/omnira/omnira/internal/platformrelease/domain"
)

// testManifestJSON é o fixture real usado em release/fixtures/manifest-good.json, copiado
// aqui para nunca depender de um caminho relativo frágil entre este diretório e release/.
const testManifestJSON = `{
  "schema_version": 1,
  "release_id": "omnira-2.4.0",
  "platform_version": "2.4.0",
  "channel": "stable",
  "source": {
    "repository": "keslleykledston/20-OMNIRA",
    "commit_sha": "4f6b5c1e2a9d8f3b7c0a1e4d9f2b6c8a0e1d3f5b",
    "workflow_run_id": "123456789"
  },
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

func writeTestManifest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(testManifestJSON), 0o644); err != nil {
		t.Fatalf("escrever manifesto de teste: %v", err)
	}
	return path
}

// writeTestManifestWithReleaseID grava uma variante de testManifestJSON com outro
// release_id — usado para provar que a família de retry nunca confunde releases DISTINTAS
// cujos IDs só coincidem textualmente (ex. "omnira-2.4.0" vs "omnira-2.4.0-retry-rc1", ambos
// legítimos pela mesma regex de release_id).
func writeTestManifestWithReleaseID(t *testing.T, releaseID string) string {
	t.Helper()
	content := strings.Replace(testManifestJSON, `"omnira-2.4.0"`, `"`+releaseID+`"`, 1)
	path := filepath.Join(t.TempDir(), "manifest-"+releaseID+".json")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("escrever manifesto de teste (%s): %v", releaseID, err)
	}
	return path
}

func testLedgerPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "ledger.db")
}

func TestRunStart_CreatesRunInLedger(t *testing.T) {
	ledgerPath := testLedgerPath(t)
	manifestPath := writeTestManifest(t)
	var out bytes.Buffer
	args := []string{"--installation-id", "inst-1", "--ledger-path", ledgerPath, "--manifest", manifestPath, "--approved-out-of-band"}
	if err := runStart(args, &out); err != nil {
		t.Fatalf("runStart: %v", err)
	}
	if !strings.Contains(out.String(), "omnira-2.4.0") {
		t.Fatalf("esperava a saída mencionar a release, veio: %s", out.String())
	}

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()
	runs, err := ledger.AllRuns(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("AllRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("esperava exatamente 1 run criado, veio %d", len(runs))
	}
	if runs[0].State != domain.StateLocked {
		t.Fatalf("esperava o run criado em LOCKED, veio %s", runs[0].State)
	}
}

func TestRunStart_IdempotentByReleaseID(t *testing.T) {
	ledgerPath := testLedgerPath(t)
	manifestPath := writeTestManifest(t)
	var out bytes.Buffer
	args := []string{"--installation-id", "inst-1", "--ledger-path", ledgerPath, "--manifest", manifestPath, "--approved-out-of-band"}
	if err := runStart(args, &out); err != nil {
		t.Fatalf("1ª chamada de runStart: %v", err)
	}
	if err := runStart(args, &out); err != nil {
		t.Fatalf("2ª chamada (mesma release) de runStart: %v", err)
	}

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()
	runs, err := ledger.AllRuns(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("AllRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("duas chamadas de 'start' para a MESMA release nunca deveriam criar dois runs, veio %d", len(runs))
	}
}

// TestRunStart_AllowsRetryAfterTerminalFailure: achado Codex P2 — usar manifest.ReleaseID
// como idempotency_key fixa SEM esta checagem deduplicava PERMANENTEMENTE: uma release que
// falhasse por um problema transitório e terminasse em ROLLED_BACK nunca poderia ser
// tentada de novo via 'start' (ActiveRuns/Reconcile excluem runs terminais, então o run
// antigo devolvido nunca seria reconciliado). Depois que o run anterior termina SEM
// sucesso, uma nova chamada precisa criar um SEGUNDO run (nunca reabrir/retornar o antigo).
func TestRunStart_AllowsRetryAfterTerminalFailure(t *testing.T) {
	ledgerPath := testLedgerPath(t)
	manifestPath := writeTestManifest(t)
	var out bytes.Buffer
	args := []string{"--installation-id", "inst-1", "--ledger-path", ledgerPath, "--manifest", manifestPath, "--approved-out-of-band"}
	if err := runStart(args, &out); err != nil {
		t.Fatalf("1ª chamada de runStart: %v", err)
	}

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	ctx := context.Background()
	runsBefore, err := ledger.AllRuns(ctx, "inst-1")
	if err != nil {
		t.Fatalf("AllRuns: %v", err)
	}
	if len(runsBefore) != 1 {
		t.Fatalf("esperava 1 run após a 1ª chamada, veio %d", len(runsBefore))
	}
	firstRunID := runsBefore[0].RunID
	h, err := ledger.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	// Leva o run a um TERMINAL sem sucesso — simula uma falha real resolvida em ROLLED_BACK
	// (ver internal/platformrelease/agent: R0/R1/R2).
	for _, s := range []domain.RunState{domain.StateFailed, domain.StateRecovering, domain.StateRolledBack} {
		if err := ledger.TransitionRun(ctx, h, firstRunID, s); err != nil {
			t.Fatalf("TransitionRun %s: %v", s, err)
		}
	}
	if err := ledger.ReleaseLock(ctx, h); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := runStart(args, &out); err != nil {
		t.Fatalf("2ª chamada (depois de ROLLED_BACK) de runStart: %v", err)
	}

	ledger2, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("reabrir ledger: %v", err)
	}
	defer ledger2.Close()
	runsAfter, err := ledger2.AllRuns(ctx, "inst-1")
	if err != nil {
		t.Fatalf("AllRuns (depois): %v", err)
	}
	if len(runsAfter) != 2 {
		t.Fatalf("esperava um SEGUNDO run criado depois de ROLLED_BACK, veio %d runs: %+v", len(runsAfter), runsAfter)
	}

	// Achado Codex P2: uma TERCEIRA chamada enquanto o retry (2º run, ainda LOCKED) está
	// ativo nunca deveria criar um TERCEIRO run — precisa deduplicar contra a tentativa
	// MAIS RECENTE da família (a key com "-retry-"), não só contra a key original.
	if err := runStart(args, &out); err != nil {
		t.Fatalf("3ª chamada (retry ainda ativo) de runStart: %v", err)
	}
	runsFinal, err := ledger2.AllRuns(ctx, "inst-1")
	if err != nil {
		t.Fatalf("AllRuns (final): %v", err)
	}
	if len(runsFinal) != 2 {
		t.Fatalf("esperava ainda só 2 runs (3ª chamada deveria deduplicar contra o retry ativo), veio %d: %+v", len(runsFinal), runsFinal)
	}
}

// TestRunStart_NeverConflatesReleasesWithTextuallySimilarIDs: achado Codex P2 — a família de
// retry precisa ser definida pelo manifest.ReleaseID REAL (desserializado), nunca por um
// prefixo textual sobre a idempotency_key. "omnira-2.4.0-retry-rc1" é um release_id
// LEGÍTIMO e DISTINTO de "omnira-2.4.0" (a mesma regex de release_id permite esse sufixo) —
// iniciar "omnira-2.4.0" nunca pode reaproveitar/retornar o run dessa OUTRA release só
// porque o nome dela coincide textualmente com o padrão de retry.
// TestRunStart_AllowsReapplyingAlreadySucceededRelease: achado Codex P2 — tratar SUCCEEDED
// como "sempre reusar a key" deduplicava PERMANENTEMENTE até uma release que já teve
// sucesso: se o operador quisesse reaplicar essa MESMA release depois (ex. um downgrade de
// volta para ela após promover para outra release), 'start' devolveria o run ANTIGO já
// terminal (nunca reconciliado de novo) em vez de agendar a nova tentativa. Uma vez que o
// run anterior TERMINA — com sucesso ou falha —, uma nova chamada de 'start' para a MESMA
// release precisa criar um SEGUNDO run.
func TestRunStart_AllowsReapplyingAlreadySucceededRelease(t *testing.T) {
	ledgerPath := testLedgerPath(t)
	manifestPath := writeTestManifest(t)
	var out bytes.Buffer
	args := []string{"--installation-id", "inst-1", "--ledger-path", ledgerPath, "--manifest", manifestPath, "--approved-out-of-band"}
	if err := runStart(args, &out); err != nil {
		t.Fatalf("1ª chamada de runStart: %v", err)
	}

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	ctx := context.Background()
	runs, err := ledger.AllRuns(ctx, "inst-1")
	if err != nil {
		t.Fatalf("AllRuns: %v", err)
	}
	firstRunID := runs[0].RunID
	h, err := ledger.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	// Leva o run a SUCCEEDED de verdade (sequência completa pós-trava).
	for _, s := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
		domain.StateObserving, domain.StateSucceeded,
	} {
		if err := ledger.TransitionRun(ctx, h, firstRunID, s); err != nil {
			t.Fatalf("TransitionRun %s: %v", s, err)
		}
	}
	if err := ledger.ReleaseLock(ctx, h); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := runStart(args, &out); err != nil {
		t.Fatalf("2ª chamada (reaplicar release já SUCCEEDED) de runStart: %v", err)
	}

	ledger2, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("reabrir ledger: %v", err)
	}
	defer ledger2.Close()
	runsAfter, err := ledger2.AllRuns(ctx, "inst-1")
	if err != nil {
		t.Fatalf("AllRuns (depois): %v", err)
	}
	if len(runsAfter) != 2 {
		t.Fatalf("esperava um SEGUNDO run para reaplicar a release já SUCCEEDED, veio %d: %+v", len(runsAfter), runsAfter)
	}
}

func TestRunStart_NeverConflatesReleasesWithTextuallySimilarIDs(t *testing.T) {
	ledgerPath := testLedgerPath(t)

	// Uma release TOTALMENTE DISTINTA, cujo ID só coincide textualmente com o padrão de
	// retry ("<releaseID>-retry-*") — ativa (LOCKED), nunca tocada pela lógica de retry.
	otherManifest := writeTestManifestWithReleaseID(t, "omnira-2.4.0-retry-rc1")
	var out bytes.Buffer
	otherArgs := []string{"--installation-id", "inst-1", "--ledger-path", ledgerPath, "--manifest", otherManifest, "--approved-out-of-band"}
	if err := runStart(otherArgs, &out); err != nil {
		t.Fatalf("start da release 'omnira-2.4.0-retry-rc1': %v", err)
	}

	// A release "real" que o teste quer iniciar — nunca deveria ser confundida com a de
	// cima, mesmo que o PREFIXO de uma delas bata com o nome da outra.
	manifestPath := writeTestManifest(t) // release_id = "omnira-2.4.0"
	args := []string{"--installation-id", "inst-1", "--ledger-path", ledgerPath, "--manifest", manifestPath, "--approved-out-of-band"}
	if err := runStart(args, &out); err != nil {
		t.Fatalf("start de 'omnira-2.4.0': %v", err)
	}

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()
	runs, err := ledger.AllRuns(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("AllRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("esperava 2 runs distintos (releases diferentes), veio %d: %+v", len(runs), runs)
	}
	seen := map[string]bool{}
	for _, r := range runs {
		m, err := domain.ParseManifest([]byte(r.ManifestJSON))
		if err != nil {
			t.Fatalf("ParseManifest do run %s: %v", r.RunID, err)
		}
		seen[m.ReleaseID] = true
	}
	if !seen["omnira-2.4.0"] || !seen["omnira-2.4.0-retry-rc1"] {
		t.Fatalf("esperava um run para CADA release distinta, veio releases: %v", seen)
	}
}

func TestRunStart_RejectsInvalidManifest(t *testing.T) {
	ledgerPath := testLedgerPath(t)
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"not":"a manifest"}`), 0o644); err != nil {
		t.Fatalf("escrever manifesto inválido: %v", err)
	}
	var out bytes.Buffer
	args := []string{"--installation-id", "inst-1", "--ledger-path", ledgerPath, "--manifest", path, "--approved-out-of-band"}
	if err := runStart(args, &out); err == nil {
		t.Fatal("runStart deveria recusar um manifesto que não valida")
	}
}

// TestRunStart_RequiresApprovedOutOfBand: achado Codex P1 — domain.ParseManifest valida só a
// FORMA do manifesto, nunca assinatura/proveniência/revogação (BLOCKED_EXTERNAL nesta
// instalação). Sem esta flag OBRIGATÓRIA e nunca-default, 'start' nunca deveria criar um run
// executável a partir de QUALQUER manifesto estruturalmente válido.
func TestRunStart_RequiresApprovedOutOfBand(t *testing.T) {
	ledgerPath := testLedgerPath(t)
	manifestPath := writeTestManifest(t)
	var out bytes.Buffer
	args := []string{"--installation-id", "inst-1", "--ledger-path", ledgerPath, "--manifest", manifestPath}
	if err := runStart(args, &out); err == nil {
		t.Fatal("runStart deveria recusar sem --approved-out-of-band, mesmo com um manifesto estruturalmente válido")
	}
}

// TestRunStart_RejectsInvalidLease: achado Codex P2 — start chama AcquireLock sem passar por
// cfg.Build()/Validate() completo; precisa da checagem isolada (ValidateLease).
func TestRunStart_RejectsInvalidLease(t *testing.T) {
	ledgerPath := testLedgerPath(t)
	manifestPath := writeTestManifest(t)
	var out bytes.Buffer
	args := []string{
		"--installation-id", "inst-1", "--ledger-path", ledgerPath,
		"--manifest", manifestPath, "--approved-out-of-band", "--lease", "0s",
	}
	if err := runStart(args, &out); err == nil {
		t.Fatal("runStart deveria recusar --lease=0s")
	}
}

func TestRunStart_RequiresInstallationIDAndManifest(t *testing.T) {
	var out bytes.Buffer
	if err := runStart([]string{"--ledger-path", testLedgerPath(t), "--manifest", writeTestManifest(t)}, &out); err == nil {
		t.Fatal("runStart deveria exigir --installation-id")
	}
	if err := runStart([]string{"--installation-id", "inst-1", "--ledger-path", testLedgerPath(t)}, &out); err == nil {
		t.Fatal("runStart deveria exigir --manifest")
	}
}

func TestRunStatus_ListsRunsAndWarnsAboutPendingRecovery(t *testing.T) {
	ledgerPath := testLedgerPath(t)
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	ctx := context.Background()
	h, err := ledger.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, s := range []domain.RunState{domain.StateFailed, domain.StateRecovering, domain.StateRecoveryRequired} {
		if err := ledger.TransitionRun(ctx, h, run.RunID, s); err != nil {
			t.Fatalf("TransitionRun %s: %v", s, err)
		}
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var out bytes.Buffer
	if err := runStatus([]string{"--installation-id", "inst-1", "--ledger-path", ledgerPath}, &out); err != nil {
		t.Fatalf("runStatus: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, run.RunID) {
		t.Fatalf("esperava a listagem mencionar o run %s, veio: %s", run.RunID, got)
	}
	if !strings.Contains(got, "RECOVERY_REQUIRED") {
		t.Fatalf("esperava avisar sobre a recuperação pendente, veio: %s", got)
	}
}

func TestRunStatus_RunDetailShowsCheckpoints(t *testing.T) {
	ledgerPath := testLedgerPath(t)
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	ctx := context.Background()
	h, err := ledger.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := ledger.RecordCheckpointDone(ctx, h, run.RunID, "backup", "dump em /tmp/x.dump"); err != nil {
		t.Fatalf("RecordCheckpointDone: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var out bytes.Buffer
	if err := runStatus([]string{"--installation-id", "inst-1", "--ledger-path", ledgerPath, "--run", run.RunID}, &out); err != nil {
		t.Fatalf("runStatus --run: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "backup") || !strings.Contains(got, "dump em /tmp/x.dump") {
		t.Fatalf("esperava o detalhe do checkpoint de backup, veio: %s", got)
	}
}

func TestRunAcknowledgeRecovery_UnblocksRun(t *testing.T) {
	ledgerPath := testLedgerPath(t)
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	ctx := context.Background()
	h, err := ledger.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, s := range []domain.RunState{domain.StateFailed, domain.StateRecovering, domain.StateRecoveryRequired} {
		if err := ledger.TransitionRun(ctx, h, run.RunID, s); err != nil {
			t.Fatalf("TransitionRun %s: %v", s, err)
		}
	}
	// Libera o lock de setup — runAcknowledgeRecovery usa seu PRÓPRIO holder (default
	// hostname:pid), nunca "holder-a", e precisaria esperar o lease inteiro expirar sem isto.
	if err := ledger.ReleaseLock(ctx, h); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var out bytes.Buffer
	if err := runAcknowledgeRecovery([]string{"--installation-id", "inst-1", "--ledger-path", ledgerPath, "--run", run.RunID}, &out); err != nil {
		t.Fatalf("runAcknowledgeRecovery: %v", err)
	}

	ledger2, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("reabrir ledger: %v", err)
	}
	defer ledger2.Close()
	pending, err := ledger2.HasUnresolvedRecoveryRequired(ctx, "inst-1")
	if err != nil {
		t.Fatalf("HasUnresolvedRecoveryRequired: %v", err)
	}
	if pending {
		t.Fatal("deveria estar destravado após acknowledge-recovery")
	}
}

func TestRunAcknowledgeRecovery_RequiresRunFlag(t *testing.T) {
	var out bytes.Buffer
	err := runAcknowledgeRecovery([]string{"--installation-id", "inst-1", "--ledger-path", testLedgerPath(t)}, &out)
	if err == nil {
		t.Fatal("runAcknowledgeRecovery deveria exigir --run")
	}
}

func TestResolveRollbackTarget_ExplicitRunID(t *testing.T) {
	ledgerPath := testLedgerPath(t)
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
	run1, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run-1: %v", err)
	}
	if _, err := ledger.CreateRun(ctx, h, "run-2", "idem-2", "{}", domain.StateLocked); err != nil {
		t.Fatalf("CreateRun run-2: %v", err)
	}

	got, err := resolveRollbackTarget(ctx, ledger, "inst-1", run1.RunID)
	if err != nil {
		t.Fatalf("resolveRollbackTarget: %v", err)
	}
	if got.RunID != run1.RunID {
		t.Fatalf("esperava run-1 (explícito), veio %s", got.RunID)
	}
}

func TestResolveRollbackTarget_MostRecentWhenOmitted(t *testing.T) {
	ledgerPath := testLedgerPath(t)
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
	if _, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked); err != nil {
		t.Fatalf("CreateRun run-1: %v", err)
	}
	run2, err := ledger.CreateRun(ctx, h, "run-2", "idem-2", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run-2: %v", err)
	}

	got, err := resolveRollbackTarget(ctx, ledger, "inst-1", "")
	if err != nil {
		t.Fatalf("resolveRollbackTarget: %v", err)
	}
	if got.RunID != run2.RunID {
		t.Fatalf("esperava o run MAIS RECENTE (run-2) quando --run é omitido, veio %s", got.RunID)
	}
}

func TestResolveRollbackTarget_NoRunsIsError(t *testing.T) {
	ledgerPath := testLedgerPath(t)
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()
	if _, err := resolveRollbackTarget(context.Background(), ledger, "inst-sem-runs", ""); err == nil {
		t.Fatal("resolveRollbackTarget deveria falhar quando não há nenhum run")
	}
}

func TestSnapshotUnits_FiltersEmptyDigestsAndSorts(t *testing.T) {
	snap := &agent.Snapshot{Digests: map[agent.Unit]string{
		agent.UnitWeb:    "ghcr.io/x/web@sha256:" + strings.Repeat("c", 64),
		agent.UnitAPI:    "ghcr.io/x/api@sha256:" + strings.Repeat("a", 64),
		agent.UnitWorker: "", // sem digest utilizável — nunca deveria aparecer
	}}
	units := snapshotUnits(snap)
	if len(units) != 2 {
		t.Fatalf("esperava 2 units (worker sem digest excluída), veio %d: %v", len(units), units)
	}
	if units[0] != agent.UnitAPI || units[1] != agent.UnitWeb {
		t.Fatalf("esperava ordem determinística [api, web], veio %v", units)
	}
}

// TestSignalContext_CancelsOnSIGINT: achado Codex P1 — 'reconcile'/'rollback' precisam reagir
// a SIGINT/SIGTERM para cancelar killOnCancel/parar o heartbeat de forma limpa (o mesmo
// contrato que apps/update-agent já tem). Prova REAL: envia um SIGINT de verdade ao próprio
// processo de teste — signal.NotifyContext intercepta e absorve o sinal enquanto o contexto
// está ativo, nunca deixando a ação padrão (matar o processo) ocorrer.
func TestSignalContext_CancelsOnSIGINT(t *testing.T) {
	ctx, stop := signalContext()
	defer stop()
	go func() {
		time.Sleep(20 * time.Millisecond)
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Errorf("enviar SIGINT ao próprio processo: %v", err)
		}
	}()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("o contexto deveria ter sido cancelado pelo SIGINT")
	}
}

func TestConfirm_AcceptsYesVariantsOnly(t *testing.T) {
	cases := map[string]bool{
		"s\n": true, "sim\n": true, "y\n": true, "yes\n": true,
		"n\n": false, "\n": false, "qualquer coisa\n": false,
	}
	for input, want := range cases {
		var out bytes.Buffer
		got := confirm(&out, strings.NewReader(input))
		if got != want {
			t.Errorf("confirm(%q) = %v, esperava %v", input, got, want)
		}
	}
}

func TestRunReconcile_RequiresInstallationID(t *testing.T) {
	var out bytes.Buffer
	err := runReconcile([]string{"--ledger-path", testLedgerPath(t), "--database-url", "postgres://x"}, &out)
	if err == nil {
		t.Fatal("runReconcile deveria exigir --installation-id")
	}
}

func TestRunRollback_RequiresInstallationID(t *testing.T) {
	var out bytes.Buffer
	err := runRollback([]string{"--ledger-path", testLedgerPath(t), "--database-url", "postgres://x"}, &out, strings.NewReader(""))
	if err == nil {
		t.Fatal("runRollback deveria exigir --installation-id")
	}
}

// TestRunRollback_RefusesForTerminalRun: achado Codex — 'rollback' nunca deveria agir sobre
// um run já SUCCEEDED/ROLLED_BACK (nada pendente) nem sobre um RECOVERY_REQUIRED (precisa de
// acknowledge-recovery + uma nova release explícita, nunca um rollback automático de dados).
// Puramente lógico: a recusa ocorre antes de qualquer mutação Docker, sem precisar de
// Docker real.
func TestRunRollback_RefusesForTerminalRun(t *testing.T) {
	ledgerPath := testLedgerPath(t)
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	ctx := context.Background()
	h, err := ledger.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", testManifestJSON, domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, s := range []domain.RunState{domain.StateFailed, domain.StateRecovering, domain.StateRecoveryRequired} {
		if err := ledger.TransitionRun(ctx, h, run.RunID, s); err != nil {
			t.Fatalf("TransitionRun %s: %v", s, err)
		}
	}
	if err := ledger.ReleaseLock(ctx, h); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var out bytes.Buffer
	args := []string{
		"--installation-id", "inst-1", "--ledger-path", ledgerPath,
		"--database-url", "postgres://fake", "--run", run.RunID, "--yes",
	}
	err = runRollback(args, &out, strings.NewReader(""))
	if err == nil {
		t.Fatal("rollback deveria recusar um run em RECOVERY_REQUIRED (precisa de acknowledge-recovery, nunca rollback automático)")
	}
	if !strings.Contains(err.Error(), string(domain.StateRecoveryRequired)) {
		t.Fatalf("esperava a mensagem de erro mencionar o estado real do run, veio: %v", err)
	}
}

// TestRunRollback_RefusesRunFromAnotherInstallation: achado Codex P1 — um --run explícito
// nunca pode pertencer a OUTRA instalação.
func TestRunRollback_RefusesRunFromAnotherInstallation(t *testing.T) {
	ledgerPath := testLedgerPath(t)
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	ctx := context.Background()
	h, err := ledger.AcquireLock(ctx, "inst-B", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	run, err := ledger.CreateRun(ctx, h, "run-of-b", "idem-b", testManifestJSON, domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := ledger.TransitionRun(ctx, h, run.RunID, domain.StateFailed); err != nil {
		t.Fatalf("TransitionRun FAILED: %v", err)
	}
	if err := ledger.ReleaseLock(ctx, h); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var out bytes.Buffer
	args := []string{
		"--installation-id", "inst-A", "--ledger-path", ledgerPath,
		"--database-url", "postgres://fake", "--run", run.RunID, "--yes",
	}
	err = runRollback(args, &out, strings.NewReader(""))
	if err == nil {
		t.Fatal("rollback deveria recusar um --run que pertence a outra instalação")
	}
}
