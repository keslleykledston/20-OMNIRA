package agent

// F9 (release/policies/signing.yaml "anti_replay.revalidate_before_apply"): prova que
// Executor.Execute recusa aplicar qualquer step de uma release cujo ledger LOCAL já marca como
// revogada — nunca confia num despacho (StartRun) anterior à revogação. O ledger local, nunca
// o Postgres, é a fonte consultada aqui: exatamente a mesma garantia de ADR-0054 §6.1 ("o
// agente nunca depende do backend/Postgres estar disponível para decidir o próximo passo
// seguro"), agora estendida para cobrir revogação também.

import (
	"context"
	"testing"

	"github.com/omnira/omnira/internal/platformrelease/domain"
)

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
