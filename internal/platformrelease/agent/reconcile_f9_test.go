package agent

// F9 (release/policies/signing.yaml "anti_replay.revalidate_before_apply"): prova que
// Executor.Execute recusa aplicar qualquer step de uma release cujo ledger LOCAL já marca como
// revogada — nunca confia num despacho (StartRun) anterior à revogação. O ledger local, nunca
// o Postgres, é a fonte consultada aqui: exatamente a mesma garantia de ADR-0054 §6.1 ("o
// agente nunca depende do backend/Postgres estar disponível para decidir o próximo passo
// seguro"), agora estendida para cobrir revogação também.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/platformrelease/domain"
)

// revokeOnFirstHealthInspectRunner (F9, achado Codex [P2] rodada 5) entrega a revogação ao
// ledger local exatamente durante a PRIMEIRA chamada de `docker inspect` com o formato de
// saúde (ou seja, DENTRO de WaitHealthy, chamada por revalidateHealthBeforeSuccess) — nunca
// antes, nunca por um goroutine concorrente/timing frágil. Delega qualquer outra chamada
// (docker ps, inspect por {{.Config.Image}}) ao runner real (fakeRunner), preservando o
// comportamento normal de resolução de container/digest.
type revokeOnFirstHealthInspectRunner struct {
	inner        runner
	ledger       *Ledger
	releaseID    string
	inspectCalls int
}

func (r *revokeOnFirstHealthInspectRunner) run(ctx context.Context, name string, args ...string) (string, string, error) {
	if name == "docker" && len(args) > 0 && args[0] == "inspect" && strings.Contains(strings.Join(args, " "), "{{.State.Running}}") {
		r.inspectCalls++
		if r.inspectCalls == 1 {
			if err := r.ledger.RevokeRelease(ctx, r.releaseID, "CVE crítico descoberto durante a revalidação final de saúde"); err != nil {
				return "", "", err
			}
			return "false|starting", "", nil
		}
		return "true|healthy", "", nil
	}
	return r.inner.run(ctx, name, args...)
}

func TestExecute_RevokedRelease_BeforeAnyMutation_ClassifiesAsR0(t *testing.T) {
	ctx := context.Background()
	manifest := testManifest("expand_only")
	fr := fakeRunnerForManifest(manifest)
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	migrate := &fakeMigrateRunner{}
	backup := &fakeBackupRunner{}
	exec, ledger, h := testExecutor(t, migrate, backup, compose)

	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Revoga ANTES de Execute rodar um único step — simula uma revogação que chega entre o
	// despacho (StartRun) e o agente sequer começar a processar o run.
	if err := ledger.RevokeRelease(ctx, manifest.ReleaseID, "vulnerabilidade crítica descoberta pós-build"); err != nil {
		t.Fatalf("RevokeRelease: %v", err)
	}

	if err := exec.Execute(ctx, h, run.RunID, manifest); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	// Nenhuma mutação real aconteceu (nem backup, nem download) — a classificação correta é
	// R0 (recover() sem snapshot de PREPARING concluído), nunca RECOVERY_REQUIRED, que seria
	// um bloqueio desproporcional para uma release que nunca tocou nada.
	if got.State != domain.StateRolledBack {
		t.Fatalf("release revogada ANTES de qualquer mutação deveria classificar R0 -> ROLLED_BACK, veio %s", got.State)
	}
	if migrate.upCalls != 0 || backup.runCalls != 0 {
		t.Errorf("uma release revogada nunca deveria chegar a rodar backup/migrate: backup=%d migrate=%d", backup.runCalls, migrate.upCalls)
	}

	cps, err := ledger.Checkpoints(ctx, run.RunID)
	if err != nil {
		t.Fatalf("Checkpoints: %v", err)
	}
	foundRevocationCause := false
	for _, c := range cps {
		if c.Step == "recovery_decision" {
			foundRevocationCause = true
		}
	}
	if !foundRevocationCause {
		t.Errorf("esperava um checkpoint recovery_decision explicando a revogação, checkpoints: %+v", cps)
	}
}

func TestExecute_NonRevokedRelease_UnaffectedByUnrelatedRevocation(t *testing.T) {
	ctx := context.Background()
	manifest := testManifest("expand_only")
	fr := fakeRunnerForManifest(manifest)
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	migrate := &fakeMigrateRunner{}
	backup := &fakeBackupRunner{}
	exec, ledger, h := testExecutor(t, migrate, backup, compose)

	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	// Revoga uma release DIFERENTE — nunca deve afetar este run.
	if err := ledger.RevokeRelease(ctx, "omnira-outra-release-9.9.9", "motivo não relacionado"); err != nil {
		t.Fatalf("RevokeRelease: %v", err)
	}

	if err := exec.Execute(ctx, h, run.RunID, manifest); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateSucceeded {
		t.Fatalf("revogar uma release NÃO relacionada nunca deveria impedir este run de suceder, veio %s", got.State)
	}
}

// TestExecute_RevokedRelease_AfterMutation_ForcesR4NeverAutoRollback é a prova central do
// achado Codex [P1]: mesmo um manifesto database_strategy=expand_only/recovery_class=R1 (que
// isRollbackSafe normalmente aceitaria como "seguro reverter automaticamente") NUNCA pode
// reverter automaticamente quando a release está REVOGADA — ADR-0054 R4 é sobre a dimensão de
// SEGURANÇA/proveniência, independente de compatibilidade de dados; reverter cegamente para o
// digest anterior seria um "forward-fix" escondido como rollback normal. Avança manualmente até
// logo depois de MIGRATING_EXPAND concluir (mesma técnica de
// TestExecute_ResumeAfterCrash_NeverRepeatsCompletedSteps — PREPARING já gravou o snapshot,
// então há mutação real), revoga SÓ DEPOIS disso (simulando a revogação chegar enquanto o
// agente está "fora do ar"), e retoma: Execute deve recusar o próximo step (DEPLOYING) e
// classificar RECOVERY_REQUIRED, nunca ROLLED_BACK.
func TestExecute_RevokedRelease_AfterMutation_ForcesR4NeverAutoRollback(t *testing.T) {
	ctx := context.Background()
	manifest := testManifest("expand_only") // recovery_class=R1 por padrão em testManifest — normalmente rollback-safe
	fr := fakeRunnerForManifest(manifest)
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	migrate := &fakeMigrateRunner{}
	backup := &fakeBackupRunner{}
	exec, ledger, h := testExecutor(t, migrate, backup, compose)

	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// "Primeira vida": avança manualmente até logo depois de MIGRATING_EXPAND concluir —
	// PREPARING (e seu snapshot) já aconteceu, então há mutação real registrada.
	for _, target := range []domain.RunState{domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying, domain.StatePreparing, domain.StateMigratingExpand} {
		next, action := exec.actionFor(stateBefore(target), run.RunID, manifest)
		if next != target {
			t.Fatalf("sequência inesperada: esperava %s, veio %s", target, next)
		}
		if action != nil {
			if err := exec.runStep(ctx, h, run.RunID, stepForState(target), action); err != nil {
				t.Fatalf("runStep %s: %v", target, err)
			}
		}
		if err := ledger.TransitionRun(ctx, h, run.RunID, target); err != nil {
			t.Fatalf("TransitionRun %s: %v", target, err)
		}
	}

	// Revoga SÓ AGORA — depois que a mutação real já aconteceu, simulando a revogação
	// chegando enquanto o agente estava fora do ar (crash/poll interval).
	if err := ledger.RevokeRelease(ctx, manifest.ReleaseID, "comprometida após o build — CVE crítico encontrado"); err != nil {
		t.Fatalf("RevokeRelease: %v", err)
	}

	// "Crash": nova chamada de Execute, como se o processo tivesse reiniciado — deve recusar
	// o próximo step (DEPLOYING) por causa da revogação, nunca completar o deploy nem
	// reverter automaticamente para o digest anterior.
	if err := exec.Execute(ctx, h, run.RunID, manifest); err != nil {
		t.Fatalf("Execute (retomada) não deveria retornar erro quando a recuperação em si é bem-sucedida: %v", err)
	}
	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateRecoveryRequired {
		t.Fatalf("release REVOGADA com mutação real nunca deveria reverter automaticamente (R4), mesmo com database_strategy=expand_only/R1 — esperava RECOVERY_REQUIRED, veio %s", got.State)
	}
}

// TestExecute_RevokedDuringObserveWindow_NeverCommitsSucceeded é a prova central do achado
// Codex [P1] (rodada 4): a checagem de revogação no TOPO do loop de Execute só roda UMA VEZ
// por iteração, ANTES de actionFor/runStep — se a revogação chega ao ledger local DURANTE a
// própria ação de OBSERVING (ObserveWindow real), a iteração em curso já passou pela checagem
// e vai declarar SUCCEEDED sem nunca reconsultar. Mesma técnica de
// TestExecute_ResumeAtObserving_RevalidatesHealthBeforeSucceeding: avança manualmente até
// OBSERVING concluir seu checkpoint (saúde boa o tempo todo), revoga SÓ DEPOIS disso — ANTES
// da transição para SUCCEEDED ser persistida — e confirma que a retomada (nova chamada de
// Execute, como um "crash" simulado) classifica RECOVERY_REQUIRED, nunca SUCCEEDED.
func TestExecute_RevokedDuringObserveWindow_NeverCommitsSucceeded(t *testing.T) {
	ctx := context.Background()
	manifest := testManifest("expand_only")
	fr := fakeRunnerForManifest(manifest)
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)
	exec.ObserveWindow = 5 * time.Millisecond

	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, target := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
	} {
		next, action := exec.actionFor(stateBefore(target), run.RunID, manifest)
		if action != nil {
			if err := exec.runStep(ctx, h, run.RunID, stepForState(next), action); err != nil {
				t.Fatalf("runStep %s: %v", target, err)
			}
		}
		if err := ledger.TransitionRun(ctx, h, run.RunID, target); err != nil {
			t.Fatalf("TransitionRun %s: %v", target, err)
		}
	}
	nextObs, actionObs := exec.actionFor(domain.StateHealthChecking, run.RunID, manifest)
	if err := exec.runStep(ctx, h, run.RunID, stepForState(nextObs), actionObs); err != nil {
		t.Fatalf("runStep (preparar Observing): %v", err)
	}
	if err := ledger.TransitionRun(ctx, h, run.RunID, domain.StateObserving); err != nil {
		t.Fatalf("TransitionRun Observing: %v", err)
	}
	nextSucc, actionSucc := exec.actionFor(domain.StateObserving, run.RunID, manifest)
	if nextSucc != domain.StateSucceeded {
		t.Fatalf("esperava next=SUCCEEDED a partir de Observing, veio %s", nextSucc)
	}
	if err := exec.runStep(ctx, h, run.RunID, stepForState(nextSucc), actionSucc); err != nil {
		t.Fatalf("runStep (ação de Observing): %v", err)
	}
	// CRASH simulado aqui: o checkpoint da ação de Observing já está "completed", mas a
	// transição para SUCCEEDED NUNCA foi persistida — exatamente a janela em que a checagem
	// de revogação do TOPO do loop (que já passou na "primeira vida") não cobriria mais nada.
	if err := ledger.RevokeRelease(ctx, manifest.ReleaseID, "CVE crítico descoberto durante a janela de observação"); err != nil {
		t.Fatalf("RevokeRelease: %v", err)
	}

	if err := exec.Execute(ctx, h, run.RunID, manifest); err != nil {
		t.Fatalf("Execute (retomada) não deveria retornar erro quando a recuperação em si é bem-sucedida: %v", err)
	}
	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateRecoveryRequired {
		t.Fatalf("uma revogação chegada durante a janela de observação final nunca deveria permitir SUCCEEDED — esperava RECOVERY_REQUIRED, veio %s", got.State)
	}
}

// TestExecute_RevokedDuringFinalHealthRevalidation_NeverCommitsSucceeded é a prova central do
// achado Codex [P2] (rodada 5): a checagem de revogação adicionada depois de
// TestExecute_RevokedDuringObserveWindow_NeverCommitsSucceeded ficava ANTES de
// revalidateHealthBeforeSuccess — mas essa função pode BLOQUEAR por até HealthTimeout por
// unidade (WaitHealthy), uma janela real onde a revogação ainda pode chegar. Prova: avança
// manualmente até o checkpoint de OBSERVING completar (mesma técnica das duas provas
// anteriores), simula o "crash", e na retomada troca o runner do Compose por
// revokeOnFirstHealthInspectRunner — que entrega a revogação exatamente na primeira chamada
// de `docker inspect` de saúde DENTRO de revalidateHealthBeforeSuccess, antes de responder
// "healthy". Confirma RECOVERY_REQUIRED, nunca SUCCEEDED.
func TestExecute_RevokedDuringFinalHealthRevalidation_NeverCommitsSucceeded(t *testing.T) {
	ctx := context.Background()
	manifest := testManifest("expand_only")
	fr := fakeRunnerForManifest(manifest)
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)
	exec.ObserveWindow = 5 * time.Millisecond

	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, target := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
	} {
		next, action := exec.actionFor(stateBefore(target), run.RunID, manifest)
		if action != nil {
			if err := exec.runStep(ctx, h, run.RunID, stepForState(next), action); err != nil {
				t.Fatalf("runStep %s: %v", target, err)
			}
		}
		if err := ledger.TransitionRun(ctx, h, run.RunID, target); err != nil {
			t.Fatalf("TransitionRun %s: %v", target, err)
		}
	}
	nextObs, actionObs := exec.actionFor(domain.StateHealthChecking, run.RunID, manifest)
	if err := exec.runStep(ctx, h, run.RunID, stepForState(nextObs), actionObs); err != nil {
		t.Fatalf("runStep (preparar Observing): %v", err)
	}
	if err := ledger.TransitionRun(ctx, h, run.RunID, domain.StateObserving); err != nil {
		t.Fatalf("TransitionRun Observing: %v", err)
	}
	nextSucc, actionSucc := exec.actionFor(domain.StateObserving, run.RunID, manifest)
	if nextSucc != domain.StateSucceeded {
		t.Fatalf("esperava next=SUCCEEDED a partir de Observing, veio %s", nextSucc)
	}
	if err := exec.runStep(ctx, h, run.RunID, stepForState(nextSucc), actionSucc); err != nil {
		t.Fatalf("runStep (ação de Observing): %v", err)
	}
	// CRASH simulado aqui: checkpoint de Observing já "completed", transição para SUCCEEDED
	// nunca persistida. Troca o runner: a PRÓXIMA revalidateHealthBeforeSuccess (incondicional
	// na retomada) vai entregar a revogação durante sua própria chamada de WaitHealthy.
	compose.r = &revokeOnFirstHealthInspectRunner{inner: fr, ledger: ledger, releaseID: manifest.ReleaseID}

	if err := exec.Execute(ctx, h, run.RunID, manifest); err != nil {
		t.Fatalf("Execute (retomada) não deveria retornar erro quando a recuperação em si é bem-sucedida: %v", err)
	}
	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateRecoveryRequired {
		t.Fatalf("uma revogação chegada DURANTE revalidateHealthBeforeSuccess (WaitHealthy) nunca deveria permitir SUCCEEDED — esperava RECOVERY_REQUIRED, veio %s", got.State)
	}
}

// TestExecute_RollbackTarget_RevokedAfterSucceeding_ForcesRecoveryRequired é a prova central
// do achado Codex [P1] (rodada 6): a checagem de revogação em recover() só olhava a release
// ENTRANDO — nunca a release de DESTINO de um rollback automático. Cenário: release A é
// instalada com sucesso (run-A, SUCCEEDED), DEPOIS é revogada (ex. CVE descoberto após o
// fato), e então release B falha depois de mutar (deploy nunca fica saudável) — o rollback
// "normal" reverteria de volta para A. Prova que isso NUNCA acontece: RECOVERY_REQUIRED, e
// Compose.Rollback (um segundo "docker compose up", detectável por applyCount) nunca chega a
// ser chamado de verdade.
func TestExecute_RollbackTarget_RevokedAfterSucceeding_ForcesRecoveryRequired(t *testing.T) {
	ctx := context.Background()
	manifestA := testManifest("expand_only")
	manifestA.ReleaseID = "omnira-A"
	manifestB := testManifest("expand_only")
	manifestB.ReleaseID = "omnira-B"
	manifestB.Artifacts = []domain.Artifact{
		{Unit: domain.UnitAPI, Image: "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("1", 64)},
		{Unit: domain.UnitWorker, Image: "ghcr.io/omnira/omnira-worker@sha256:" + strings.Repeat("2", 64)},
		{Unit: domain.UnitWeb, Image: "ghcr.io/omnira/omnira-web@sha256:" + strings.Repeat("3", 64)},
	}

	compose := &ComposeAdapter{r: fakeRunnerForManifest(manifestA), Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)
	exec.HealthTimeout = 50 * time.Millisecond

	// Run-A: happy path completo, saúde boa o tempo todo — termina SUCCEEDED normalmente,
	// deixando um run SUCCEEDED rastreado no ledger para esta instalação.
	rawA, err := json.Marshal(manifestA)
	if err != nil {
		t.Fatalf("json.Marshal manifestA: %v", err)
	}
	runA, err := ledger.CreateRun(ctx, h, "run-A", "idem-A", string(rawA), domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run-A: %v", err)
	}
	if err := exec.Execute(ctx, h, runA.RunID, manifestA); err != nil {
		t.Fatalf("Execute run-A: %v", err)
	}
	gotA, err := ledger.GetRun(ctx, runA.RunID)
	if err != nil {
		t.Fatalf("GetRun run-A: %v", err)
	}
	if gotA.State != domain.StateSucceeded {
		t.Fatalf("run-A deveria suceder normalmente antes do cenário do teste começar, veio %s", gotA.State)
	}

	// SÓ DEPOIS de A suceder, revoga A — simula um CVE descoberto após o fato.
	if err := ledger.RevokeRelease(ctx, manifestA.ReleaseID, "CVE crítico descoberto depois do sucesso de A"); err != nil {
		t.Fatalf("RevokeRelease: %v", err)
	}

	// Run-B: igual a TestExecute_FailureWithCompatibleStrategy_RollsBackCode — o deploy nunca
	// fica saudável na primeira tentativa (applyCount<2), forçando recover(). Sem o achado
	// Codex corrigido, isRollbackSafe(manifestB)=true (mesmo database_strategy) deixaria
	// recover() chamar Compose.Rollback de volta para A — exatamente o que não pode acontecer.
	wrapped := &rollbackSucceedsRunner{fakeRunner: fakeRunnerForManifest(manifestB)}
	compose.r = wrapped
	rawB, err := json.Marshal(manifestB)
	if err != nil {
		t.Fatalf("json.Marshal manifestB: %v", err)
	}
	runB, err := ledger.CreateRun(ctx, h, "run-B", "idem-B", string(rawB), domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run-B: %v", err)
	}
	if err := exec.Execute(ctx, h, runB.RunID, manifestB); err != nil {
		t.Fatalf("Execute run-B não deveria retornar erro quando a recuperação em si é bem-sucedida: %v", err)
	}
	gotB, err := ledger.GetRun(ctx, runB.RunID)
	if err != nil {
		t.Fatalf("GetRun run-B: %v", err)
	}
	if gotB.State != domain.StateRecoveryRequired {
		t.Fatalf("rollback automático de volta para uma release REVOGADA (A) nunca deveria completar — esperava RECOVERY_REQUIRED, veio %s", gotB.State)
	}
	if wrapped.applyCount >= 2 {
		t.Errorf("Compose.Rollback nunca deveria ter sido chamado de verdade (reinstalaria a release revogada A) — applyCount=%d", wrapped.applyCount)
	}
}

// TestExecute_StaleRevocationSync_RefusesToApplyNewSteps é a prova central do achado Codex
// [P1] (rodada 7): entrega eventual (refreshPendingRun/StartRevocationDeliveryLoop, achados
// anteriores) só cobre uma falha TRANSIENTE de entrega — se o processo da API inteiro fica
// fora do ar por mais tempo que signing.yaml permite (max_cache_age_seconds=300), nada
// reentrega NADA, e sem esta checagem de frescor o agente continuaria vendo
// IsReleaseRevoked=false por simples ausência de dados, nunca por confirmação real. Backdata
// o relógio de sincronização ANTES de qualquer mutação e confirma que Execute recusa aplicar
// qualquer step — mesma classificação trivial R0 (nada mutou, nada a reverter) de uma
// revogação real chegando antes de qualquer mutação.
func TestExecute_StaleRevocationSync_RefusesToApplyNewSteps(t *testing.T) {
	ctx := context.Background()
	manifest := testManifest("expand_only")
	fr := fakeRunnerForManifest(manifest)
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	migrate := &fakeMigrateRunner{}
	backup := &fakeBackupRunner{}
	exec, ledger, h := testExecutor(t, migrate, backup, compose)

	backdateRevocationSync(t, ledger, maxRevocationSyncAge+time.Minute)

	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := exec.Execute(ctx, h, run.RunID, manifest); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateRolledBack {
		t.Fatalf("sincronização de revogação obsoleta ANTES de qualquer mutação deveria classificar R0 -> ROLLED_BACK (fail-closed, nada a reverter), veio %s", got.State)
	}
	if migrate.upCalls != 0 || backup.runCalls != 0 {
		t.Errorf("com a sincronização obsoleta, nenhum step real deveria ter sido aplicado: backup=%d migrate=%d", backup.runCalls, migrate.upCalls)
	}
}

// TestExecute_RollbackTarget_PartialWorkloadRelease_ChecksPerUnitProvenance é a prova central
// do achado Codex [P1] (rodada 7): rollbackTargetReleaseIDs precisa resolver a proveniência
// POR UNIT, nunca assumir que "a release do run SUCCEEDED mais recente" cobre todas as units.
// Cenário exato do achado: release A atualiza só a API (SUCCEEDED); release B, depois, só o
// web (SUCCEEDED — mais recente que A no geral); A é revogada; release C tenta atualizar a
// API de novo e falha depois de mutar. O rollback da API precisa recusar porque o destino é A
// (revogada) — nunca checar B (que nunca tocou a API) e deixar passar.
func TestExecute_RollbackTarget_PartialWorkloadRelease_ChecksPerUnitProvenance(t *testing.T) {
	ctx := context.Background()

	manifestA := testManifest("expand_only")
	manifestA.ReleaseID = "omnira-A"
	manifestA.Rollout.Workloads = []domain.BuildUnit{domain.UnitAPI}
	manifestA.Artifacts = []domain.Artifact{
		{Unit: domain.UnitAPI, Image: "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("a", 64)},
	}
	manifestB := testManifest("expand_only")
	manifestB.ReleaseID = "omnira-B"
	manifestB.Rollout.Workloads = []domain.BuildUnit{domain.UnitWeb}
	manifestB.Artifacts = []domain.Artifact{
		{Unit: domain.UnitWeb, Image: "ghcr.io/omnira/omnira-web@sha256:" + strings.Repeat("b", 64)},
	}
	manifestC := testManifest("expand_only")
	manifestC.ReleaseID = "omnira-C"
	manifestC.Rollout.Workloads = []domain.BuildUnit{domain.UnitAPI}
	manifestC.Artifacts = []domain.Artifact{
		{Unit: domain.UnitAPI, Image: "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("c", 64)},
	}

	compose := &ComposeAdapter{r: fakeRunnerForManifest(manifestA), Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)
	exec.HealthTimeout = 50 * time.Millisecond

	// Run-A: happy path, só a API.
	rawA, err := json.Marshal(manifestA)
	if err != nil {
		t.Fatalf("json.Marshal manifestA: %v", err)
	}
	runA, err := ledger.CreateRun(ctx, h, "run-A", "idem-A", string(rawA), domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run-A: %v", err)
	}
	if err := exec.Execute(ctx, h, runA.RunID, manifestA); err != nil {
		t.Fatalf("Execute run-A: %v", err)
	}
	if gotA, err := ledger.GetRun(ctx, runA.RunID); err != nil || gotA.State != domain.StateSucceeded {
		t.Fatalf("run-A deveria suceder, state=%v err=%v", gotA, err)
	}

	// Run-B: happy path, só o web — mais recente que A, mas nunca toca a API.
	compose.r = fakeRunnerForManifest(manifestB)
	rawB, err := json.Marshal(manifestB)
	if err != nil {
		t.Fatalf("json.Marshal manifestB: %v", err)
	}
	runB, err := ledger.CreateRun(ctx, h, "run-B", "idem-B", string(rawB), domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run-B: %v", err)
	}
	if err := exec.Execute(ctx, h, runB.RunID, manifestB); err != nil {
		t.Fatalf("Execute run-B: %v", err)
	}
	if gotB, err := ledger.GetRun(ctx, runB.RunID); err != nil || gotB.State != domain.StateSucceeded {
		t.Fatalf("run-B deveria suceder, state=%v err=%v", gotB, err)
	}

	// SÓ DEPOIS de A e B terem sucedido, revoga A.
	if err := ledger.RevokeRelease(ctx, manifestA.ReleaseID, "CVE crítico na API descoberto depois do sucesso de A"); err != nil {
		t.Fatalf("RevokeRelease: %v", err)
	}

	// Run-C: tenta atualizar a API de novo, falha depois de mutar (deploy nunca fica
	// saudável na primeira tentativa). Sem o achado corrigido, rollbackTargetReleaseIDs
	// pegaria B (o SUCCEEDED mais recente no geral) — B não está revogada, e o rollback da
	// API reinstalaria de volta o digest de A (revogado) sem nunca checar A.
	wrapped := &rollbackSucceedsRunner{fakeRunner: fakeRunnerForManifest(manifestC)}
	compose.r = wrapped
	rawC, err := json.Marshal(manifestC)
	if err != nil {
		t.Fatalf("json.Marshal manifestC: %v", err)
	}
	runC, err := ledger.CreateRun(ctx, h, "run-C", "idem-C", string(rawC), domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run-C: %v", err)
	}
	if err := exec.Execute(ctx, h, runC.RunID, manifestC); err != nil {
		t.Fatalf("Execute run-C não deveria retornar erro quando a recuperação em si é bem-sucedida: %v", err)
	}
	gotC, err := ledger.GetRun(ctx, runC.RunID)
	if err != nil {
		t.Fatalf("GetRun run-C: %v", err)
	}
	if gotC.State != domain.StateRecoveryRequired {
		t.Fatalf("rollback automático da API de volta para A (revogada) nunca deveria completar, mesmo com B sendo o SUCCEEDED mais recente no geral — esperava RECOVERY_REQUIRED, veio %s", gotC.State)
	}
	if wrapped.applyCount >= 2 {
		t.Errorf("Compose.Rollback nunca deveria ter sido chamado de verdade (reinstalaria o digest revogado de A na API) — applyCount=%d", wrapped.applyCount)
	}
}

// TestExecute_RollbackTarget_SnapshotDigestNotTrackedByAnySucceededRun_FailsClosed é a prova
// central do achado Codex [P1] (rodada 8): rollbackTargetReleaseIDs não pode aceitar "o
// SUCCEEDED mais recente que tocou esta unit" sem confirmar que o digest que ELE implantou
// bate com o que `snap` de fato reporta agora. Cenário: release A sucede na API; uma
// "correção fora de banda" (RECOVERY_REQUIRED reconhecido manualmente, ou um ajuste direto no
// host) deixa a API rodando um digest que NENHUMA release SUCCEEDED jamais registrou; release
// D tenta atualizar a API de novo e falha depois de mutar. A proveniência do rollback é
// AMBÍGUA (A tocou a API, mas o digest de A não bate com o que está rodando) — isso precisa
// ser fail-closed, nunca "A não está revogada, pode reverter".
func TestExecute_RollbackTarget_SnapshotDigestNotTrackedByAnySucceededRun_FailsClosed(t *testing.T) {
	ctx := context.Background()

	manifestA := testManifest("expand_only")
	manifestA.ReleaseID = "omnira-A"
	manifestA.Rollout.Workloads = []domain.BuildUnit{domain.UnitAPI}
	manifestA.Artifacts = []domain.Artifact{
		{Unit: domain.UnitAPI, Image: "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("a", 64)},
	}
	manifestD := testManifest("expand_only")
	manifestD.ReleaseID = "omnira-D"
	manifestD.Rollout.Workloads = []domain.BuildUnit{domain.UnitAPI}
	manifestD.Artifacts = []domain.Artifact{
		{Unit: domain.UnitAPI, Image: "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("d", 64)},
	}

	compose := &ComposeAdapter{r: fakeRunnerForManifest(manifestA), Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)
	exec.HealthTimeout = 50 * time.Millisecond

	rawA, err := json.Marshal(manifestA)
	if err != nil {
		t.Fatalf("json.Marshal manifestA: %v", err)
	}
	runA, err := ledger.CreateRun(ctx, h, "run-A", "idem-A", string(rawA), domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run-A: %v", err)
	}
	if err := exec.Execute(ctx, h, runA.RunID, manifestA); err != nil {
		t.Fatalf("Execute run-A: %v", err)
	}
	if gotA, err := ledger.GetRun(ctx, runA.RunID); err != nil || gotA.State != domain.StateSucceeded {
		t.Fatalf("run-A deveria suceder, state=%v err=%v", gotA, err)
	}

	// "Correção fora de banda": o digest atual da API passa a ser ALGO que NENHUM run
	// SUCCEEDED jamais registrou (nem A, nem D) — simula um RECOVERY_REQUIRED reconhecido
	// fora de banda ou uma correção manual direta no host.
	frD := fakeRunnerForManifest(manifestD)
	for k := range frD.stdoutByFormat {
		if strings.Contains(k, "{{.Config.Image}}") {
			frD.stdoutByFormat[k] = "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("x", 64)
		}
	}
	wrapped := &rollbackSucceedsRunner{fakeRunner: frD}
	compose.r = wrapped

	rawD, err := json.Marshal(manifestD)
	if err != nil {
		t.Fatalf("json.Marshal manifestD: %v", err)
	}
	runD, err := ledger.CreateRun(ctx, h, "run-D", "idem-D", string(rawD), domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run-D: %v", err)
	}
	if err := exec.Execute(ctx, h, runD.RunID, manifestD); err != nil {
		t.Fatalf("Execute run-D não deveria retornar erro quando a recuperação em si é bem-sucedida: %v", err)
	}
	gotD, err := ledger.GetRun(ctx, runD.RunID)
	if err != nil {
		t.Fatalf("GetRun run-D: %v", err)
	}
	if gotD.State != domain.StateRecoveryRequired {
		t.Fatalf("digest atual não rastreado por nenhuma release SUCCEEDED deveria ser fail-closed (proveniência ambígua) — esperava RECOVERY_REQUIRED, veio %s", gotD.State)
	}
	if wrapped.applyCount >= 2 {
		t.Errorf("Compose.Rollback nunca deveria ter sido chamado de verdade com proveniência ambígua — applyCount=%d", wrapped.applyCount)
	}
}

// TestExecute_RollbackTarget_NeverSynced_StandaloneAgentStillRollsBack é a prova central do
// achado Codex [P1] (rodada 9): com OMNIRA_PLATFORM_RELEASE_API_ENABLED=false (o default,
// ADR-0054) nenhum processo jamais chama Ledger.MarkRevocationSynced — "nunca sincronizado"
// precisa continuar sendo tratado como fresco (gate não se aplica), nunca como obsoleto, ou
// um rollback automático comum (o mesmo cenário de
// TestExecute_FailureWithCompatibleStrategy_RollsBackCode) ficaria permanentemente bloqueado
// cinco minutos depois da criação do ledger — inclusive via 'updatectl rollback', desenhado
// para funcionar explicitamente SEM Postgres.
func TestExecute_RollbackTarget_NeverSynced_StandaloneAgentStillRollsBack(t *testing.T) {
	ctx := context.Background()
	previousDigest := "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("9", 64)
	fr := &rollbackSucceedsRunner{fakeRunner: &fakeRunner{
		stdout:         map[string]string{"docker ps": "container-123"},
		stdoutByFormat: map[string]string{"{{.Config.Image}}": previousDigest},
	}}
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)
	exec.HealthTimeout = 50 * time.Millisecond

	// Achado Codex [P1] (rodada 10, corrigindo a rodada 9): "nunca sincronizado" sozinho NÃO
	// é mais suficiente — testExecutor já sincroniza por padrão (para os testes que não são
	// sobre isso). Simula o estado REAL de uma instalação standalone: MarkStandalone,
	// decisão EXPLÍCITA do operador (OMNIRA_AGENT_STANDALONE=true) — nunca a mera ausência
	// de sincronização.
	if err := ledger.MarkStandalone(ctx); err != nil {
		t.Fatalf("MarkStandalone: %v", err)
	}
	fresh, _, err := ledger.RevocationSyncFresh(ctx, maxRevocationSyncAge)
	if err != nil || !fresh {
		t.Fatalf("premissa do teste: ledger marcado standalone deveria ser fresco, fresh=%v err=%v", fresh, err)
	}

	manifest := testManifest("expand_only")
	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := exec.Execute(ctx, h, run.RunID, manifest); err != nil {
		t.Fatalf("Execute não deveria retornar erro quando a recuperação em si é bem-sucedida: %v", err)
	}
	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateRolledBack {
		t.Fatalf("um agente standalone (nunca sincronizado) nunca deveria ter seu rollback automático bloqueado por frescor — esperava ROLLED_BACK, veio %s", got.State)
	}
}

// TestExecute_RollbackTarget_AcknowledgedRecoveryRequired_StillChecksRevocation é a prova
// central do achado Codex [P1] (rodada 10): rollbackTargetReleaseIDs só considerava runs
// SUCCEEDED como proveniência — mas release A pode mutar (deployar seu digest) e cair em
// RECOVERY_REQUIRED (nunca SUCCEEDED) por uma razão TOTALMENTE independente de revogação (ex.
// database_strategy desconhecido, que recover() sempre trata como rollback automático
// inseguro). Um operador reconhece a recuperação (AcknowledgeRecovery) para desbloquear novos
// runs — isso NUNCA reverte o digest de A, que continua rodando. Se A for revogada DEPOIS
// disso, e uma release E mais nova falhar tentando atualizar a mesma unit, o rollback
// automático de E precisa recusar — nunca "A nunca sucedeu, então não há nada a checar".
func TestExecute_RollbackTarget_AcknowledgedRecoveryRequired_StillChecksRevocation(t *testing.T) {
	ctx := context.Background()

	manifestA := testManifest("") // estratégia desconhecida: isRollbackSafe sempre falso
	manifestA.ReleaseID = "omnira-A"
	manifestA.Rollout.Workloads = []domain.BuildUnit{domain.UnitAPI}
	manifestA.Artifacts = []domain.Artifact{
		{Unit: domain.UnitAPI, Image: "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("a", 64)},
	}
	frA := fakeRunnerForManifest(manifestA)
	frA.stdoutByFormat["{{.State.Running}}"] = "true|starting" // nunca healthy -> força a falha de A

	compose := &ComposeAdapter{r: frA, Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)
	exec.HealthTimeout = 50 * time.Millisecond

	rawA, err := json.Marshal(manifestA)
	if err != nil {
		t.Fatalf("json.Marshal manifestA: %v", err)
	}
	runA, err := ledger.CreateRun(ctx, h, "run-A", "idem-A", string(rawA), domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run-A: %v", err)
	}
	if err := exec.Execute(ctx, h, runA.RunID, manifestA); err != nil {
		t.Fatalf("Execute run-A não deveria retornar erro quando a recuperação em si é bem-sucedida: %v", err)
	}
	gotA, err := ledger.GetRun(ctx, runA.RunID)
	if err != nil {
		t.Fatalf("GetRun run-A: %v", err)
	}
	if gotA.State != domain.StateRecoveryRequired {
		t.Fatalf("premissa do teste: run-A deveria cair em RECOVERY_REQUIRED (database_strategy desconhecido, nunca rollback automático), veio %s", gotA.State)
	}

	// Operador investiga e reconhece a recuperação — desbloqueia novos runs, mas NUNCA
	// reverte o digest de A, que continua rodando.
	if err := ledger.AcknowledgeRecovery(ctx, h, runA.RunID); err != nil {
		t.Fatalf("AcknowledgeRecovery: %v", err)
	}

	// SÓ DEPOIS disso, A é revogada (CVE descoberto depois do fato).
	if err := ledger.RevokeRelease(ctx, manifestA.ReleaseID, "CVE crítico descoberto depois do reconhecimento de A"); err != nil {
		t.Fatalf("RevokeRelease: %v", err)
	}

	manifestE := testManifest("expand_only")
	manifestE.ReleaseID = "omnira-E"
	manifestE.Rollout.Workloads = []domain.BuildUnit{domain.UnitAPI}
	manifestE.Artifacts = []domain.Artifact{
		{Unit: domain.UnitAPI, Image: "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("e", 64)},
	}
	// Reusa frA (SEM alterar seu Config.Image) — o digest "atualmente rodando" continua
	// sendo o de A, exatamente como a realidade simulada até aqui. Só a falha de saúde do
	// PRÓPRIO deploy de E precisa ser forçada.
	wrapped := &rollbackSucceedsRunner{fakeRunner: frA}
	compose.r = wrapped

	rawE, err := json.Marshal(manifestE)
	if err != nil {
		t.Fatalf("json.Marshal manifestE: %v", err)
	}
	runE, err := ledger.CreateRun(ctx, h, "run-E", "idem-E", string(rawE), domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run-E: %v", err)
	}
	if err := exec.Execute(ctx, h, runE.RunID, manifestE); err != nil {
		t.Fatalf("Execute run-E não deveria retornar erro quando a recuperação em si é bem-sucedida: %v", err)
	}
	gotE, err := ledger.GetRun(ctx, runE.RunID)
	if err != nil {
		t.Fatalf("GetRun run-E: %v", err)
	}
	if gotE.State != domain.StateRecoveryRequired {
		t.Fatalf("rollback automático de volta para A (RECOVERY_REQUIRED reconhecida, depois revogada) nunca deveria completar — esperava RECOVERY_REQUIRED, veio %s", gotE.State)
	}
	if wrapped.applyCount >= 2 {
		t.Errorf("Compose.Rollback nunca deveria ter sido chamado de verdade (reinstalaria o digest revogado de A) — applyCount=%d", wrapped.applyCount)
	}
}
