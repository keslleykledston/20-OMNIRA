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
