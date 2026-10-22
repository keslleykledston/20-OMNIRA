package updatemgr_test

// F9 (spec "F9 — ... hardening e revisão independente", item "Retenção, garbage collection
// segura" — release/policies/channels.yaml "retention_days"): prova contra Postgres real de
// que Service.ListRetentionCandidates relata corretamente a janela de retenção e a segurança
// de exclusão, e que Service.DeleteReleaseCatalogEntry NUNCA remove uma release ainda dentro
// da retenção ou referenciada por revogação/plano/run — mesmo quando um relatório antigo
// dizia "seguro".

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/platformrelease/agent"
	"github.com/omnira/omnira/internal/platformrelease/domain"
	"github.com/omnira/omnira/internal/platformrelease/updatemgr"
)

func TestUpdateMgr_ListRetentionCandidates_ReportsAgeAndWithinWindowAsUnsafe(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	m := manifestJSONChannel(t, "omnira-9.9.300", domain.ChannelCandidate, []string{"0.1.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0300", m); err != nil {
		t.Fatal(err)
	}

	candidates, err := svc.ListRetentionCandidates(w.ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	var found *updatemgr.RetentionCandidate
	for i := range candidates {
		if candidates[i].ReleaseID == "omnira-9.9.300" {
			found = &candidates[i]
		}
	}
	if found == nil {
		t.Fatal("omnira-9.9.300 deveria aparecer no relatório de retenção")
	}
	if found.Channel != "candidate" || found.RetentionDays != 30 {
		t.Errorf("channel/retention_days incorretos: %+v", found)
	}
	if found.PastRetention {
		t.Error("uma release recém-ingerida nunca deveria estar passada da retenção")
	}
	if found.SafeToDelete {
		t.Error("uma release ainda dentro da janela de retenção nunca deveria ser safe_to_delete")
	}
	if len(found.BlockingReasons) == 0 {
		t.Error("esperava ao menos um motivo de bloqueio (ainda dentro da retenção)")
	}
}

func TestUpdateMgr_DeleteReleaseCatalogEntry_RefusesWithinRetentionWindow(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	m := manifestJSONChannel(t, "omnira-9.9.301", domain.ChannelCandidate, []string{"0.1.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0301", m); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.DeleteReleaseCatalogEntry(w.ctx, op, "gc-key-f9-0301", "omnira-9.9.301"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("esperava ErrInvalid (ainda dentro da retenção), veio %v", err)
	}
	if n := w.count(`SELECT count(*) FROM platform_releases WHERE release_id = $1`, "omnira-9.9.301"); n != 1 {
		t.Errorf("a release nunca deveria ter sido removida, achei %d linhas", n)
	}
}

func TestUpdateMgr_DeleteReleaseCatalogEntry_RefusesWhenRevokedEvenPastRetention(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	m := manifestJSONChannel(t, "omnira-9.9.302", domain.ChannelCandidate, []string{"0.1.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0302", m); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RevokeRelease(w.ctx, op, "revoke-key-f9-0302", "omnira-9.9.302", "CVE crítico"); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE platform_releases SET ingested_at = now() - interval '400 days' WHERE release_id = $1`, "omnira-9.9.302")

	if _, err := svc.DeleteReleaseCatalogEntry(w.ctx, op, "gc-key-f9-0302", "omnira-9.9.302"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("esperava ErrInvalid (release revogada, histórico permanente), veio %v", err)
	}
	if n := w.count(`SELECT count(*) FROM platform_releases WHERE release_id = $1`, "omnira-9.9.302"); n != 1 {
		t.Errorf("uma release revogada nunca deveria ser removida mesmo passada da retenção, achei %d linhas", n)
	}
}

func TestUpdateMgr_DeleteReleaseCatalogEntry_RefusesWhenReferencedByRun(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	w.exec(`UPDATE platform_installations SET current_version = '0.1.0'`)
	m := manifestJSONChannel(t, "omnira-9.9.303", domain.ChannelCandidate, []string{"0.1.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0303", m); err != nil {
		t.Fatal(err)
	}
	plan, _, err := svc.CreatePlan(w.ctx, op, "plan-key-f9-0303", "omnira-9.9.303")
	if err != nil {
		t.Fatal(err)
	}
	// Insere o run DIRETAMENTE (nunca via StartRun): platform_update_events é append-only
	// (nem este teste, nem nenhum outro, consegue limpá-la depois), e platform_update_runs
	// é uma tabela GLOBAL que outro teste desta suíte
	// (TestUpdateMgr_PreflightBlocksIncompatibleVersionAndAgentProtocol) conta de forma
	// ABSOLUTA esperando zero — StartRun criaria um rastro que este teste NUNCA conseguiria
	// desfazer. Só precisamos de UMA linha real referenciando a release para provar o
	// bloqueio de exclusão (a própria foreign key sem CASCADE), nunca do fluxo completo de
	// despacho.
	runID := "run-f9-0303-direct"
	w.exec(`INSERT INTO platform_update_runs (id, plan_id, release_id, state, manifest_json, idempotency_key, actor_id)
		VALUES ($1, $2, $3, 'LOCKED', '{}'::jsonb, 'direct-insert-f9-0303', $4)`,
		runID, plan.ID, "omnira-9.9.303", op)
	t.Cleanup(func() { w.exec(`DELETE FROM platform_update_runs WHERE id = $1`, runID) })

	w.exec(`UPDATE platform_releases SET ingested_at = now() - interval '400 days' WHERE release_id = $1`, "omnira-9.9.303")

	if _, err := svc.DeleteReleaseCatalogEntry(w.ctx, op, "gc-key-f9-0303a", "omnira-9.9.303"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("esperava ErrInvalid (referenciada por um run real), veio %v", err)
	}
	if n := w.count(`SELECT count(*) FROM platform_releases WHERE release_id = $1`, "omnira-9.9.303"); n != 1 {
		t.Errorf("uma release com histórico de run real nunca deveria ser removida, achei %d linhas", n)
	}
}

func TestUpdateMgr_DeleteReleaseCatalogEntry_SucceedsWhenPastRetentionAndUnreferenced(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	m := manifestJSONChannel(t, "omnira-9.9.304", domain.ChannelCandidate, []string{"0.1.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0304", m); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE platform_releases SET ingested_at = now() - interval '400 days' WHERE release_id = $1`, "omnira-9.9.304")

	deleted, err := svc.DeleteReleaseCatalogEntry(w.ctx, op, "gc-key-f9-0304", "omnira-9.9.304")
	if err != nil {
		t.Fatalf("DeleteReleaseCatalogEntry: %v", err)
	}
	if !deleted {
		t.Error("esperava deleted=true")
	}
	if n := w.count(`SELECT count(*) FROM platform_releases WHERE release_id = $1`, "omnira-9.9.304"); n != 0 {
		t.Errorf("a release deveria ter sido removida, achei %d linhas", n)
	}
	if n := w.count(`SELECT count(*) FROM platform_release_ingest_requests WHERE release_id = $1`, "omnira-9.9.304"); n != 0 {
		t.Errorf("a linha de idempotência de ingestão deveria ter sido removida junto, achei %d", n)
	}
	if n := w.count(`SELECT count(*) FROM audit_events WHERE action = 'platform.release.gc_deleted'`); n != 1 {
		t.Errorf("esperava exatamente 1 evento de auditoria platform.release.gc_deleted, achei %d", n)
	}

	// Idempotência: repetir com a MESMA chave devolve deleted=true sem erro, mesmo com a
	// release já não existindo mais.
	deleted, err = svc.DeleteReleaseCatalogEntry(w.ctx, op, "gc-key-f9-0304", "omnira-9.9.304")
	if err != nil {
		t.Fatalf("DeleteReleaseCatalogEntry (replay): %v", err)
	}
	if !deleted {
		t.Error("replay da MESMA chave deveria devolver deleted=true")
	}
}

// TestUpdateMgr_DeleteReleaseCatalogEntry_RefusesWhenCurrentlyInstalled é a prova central do
// achado Codex [P1] (rodada 1 desta fatia): platform_installations.current_release_id NUNCA
// tem foreign key para platform_releases — SetInstallationVersion (ex. recuperação manual
// R3/R4, ou o bootstrap inicial de uma instalação) pode registrar uma release como
// ATUALMENTE instalada SEM nenhum plano/run correspondente no Postgres. Sem a checagem
// explícita, essa release passava pelas outras três checagens (revogação/run/plano, todas
// zero) e era removida — deixando a instalação apontar para uma release inexistente E
// tornando-a impossível de revogar depois se um CVE for descoberto para o que está
// genuinamente rodando.
func TestUpdateMgr_DeleteReleaseCatalogEntry_RefusesWhenCurrentlyInstalled(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	m := manifestJSONChannel(t, "omnira-9.9.305", domain.ChannelCandidate, []string{"0.1.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0305", m); err != nil {
		t.Fatal(err)
	}
	// Registra como instalada SEM nenhum plano/run — exatamente o caso real que o achado
	// aponta (uma recuperação manual, ou o bootstrap inicial, nunca passa por CreatePlan/
	// StartRun).
	if _, err := svc.SetInstallationVersion(w.ctx, op, "setver-key-f9-0305", "9.9.305", "omnira-9.9.305"); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE platform_releases SET ingested_at = now() - interval '400 days' WHERE release_id = $1`, "omnira-9.9.305")

	candidates, err := svc.ListRetentionCandidates(w.ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	var found *updatemgr.RetentionCandidate
	for i := range candidates {
		if candidates[i].ReleaseID == "omnira-9.9.305" {
			found = &candidates[i]
		}
	}
	if found == nil {
		t.Fatal("omnira-9.9.305 deveria aparecer no relatório")
	}
	if found.SafeToDelete {
		t.Error("a release ATUALMENTE instalada nunca deveria ser safe_to_delete, mesmo passada da retenção e sem run/plano/revogação")
	}

	if _, err := svc.DeleteReleaseCatalogEntry(w.ctx, op, "gc-key-f9-0305", "omnira-9.9.305"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("esperava ErrInvalid (release atualmente instalada), veio %v", err)
	}
	if n := w.count(`SELECT count(*) FROM platform_releases WHERE release_id = $1`, "omnira-9.9.305"); n != 1 {
		t.Errorf("a release atualmente instalada nunca deveria ser removida, achei %d linhas", n)
	}
}

// TestUpdateMgr_DeleteReleaseCatalogEntry_RefusesWhenLedgerStillProvidesAUnit é a prova
// central do achado Codex [P1] (rodada 2 desta fatia): current_release_id só reflete a
// release SUCCEEDED mais recente no GERAL — mas release A (via updatectl, direto no ledger
// LOCAL, SEM nenhum plano/run no Postgres) só atualiza a API; release B, depois, só o web.
// current_release_id passa a ser B (sincronizado por refreshPendingRun), mas A AINDA é a
// proveniência real da API. Sem a checagem por unit via o ledger, A seria removível mesmo
// continuando a fornecer a API de verdade.
func TestUpdateMgr_DeleteReleaseCatalogEntry_RefusesWhenLedgerStillProvidesAUnit(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc := updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	apiDigest := "ghcr.io/k3g/omnira-api@sha256:" + sha64("api-only-9.9.306")
	mA := manifestJSONForUnit(t, "omnira-9.9.306", domain.UnitAPI, apiDigest, []string{"0.1.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0306", mA); err != nil {
		t.Fatal(err)
	}
	webDigest := "ghcr.io/k3g/omnira-web@sha256:" + sha64("web-only-9.9.307")
	mB := manifestJSONForUnit(t, "omnira-9.9.307", domain.UnitWeb, webDigest, []string{"0.1.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0307", mB); err != nil {
		t.Fatal(err)
	}

	// Simula updatectl: cria e conclui os runs DIRETO no ledger local, nunca via
	// CreatePlan/StartRun — nenhum plano/run correspondente existe no Postgres para A ou B.
	driveToSucceeded := func(runID, manifestJSON string) {
		t.Helper()
		ledger, err := agent.OpenLedger(ledgerPath)
		if err != nil {
			t.Fatal(err)
		}
		defer ledger.Close()
		h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-updatectl", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.CreateRun(w.ctx, h, runID, "idem-"+runID, manifestJSON, domain.StateLocked); err != nil {
			t.Fatal(err)
		}
		for _, st := range []domain.RunState{
			domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
			domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
			domain.StateObserving, domain.StateSucceeded,
		} {
			if err := ledger.TransitionRun(w.ctx, h, runID, st); err != nil {
				t.Fatalf("transição para %s: %v", st, err)
			}
		}
		if err := ledger.ReleaseLock(w.ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	driveToSucceeded("run-9.9.306-direct", string(mA))
	driveToSucceeded("run-9.9.307-direct", string(mB))

	w.exec(`UPDATE platform_releases SET ingested_at = now() - interval '400 days' WHERE release_id IN ($1, $2)`, "omnira-9.9.306", "omnira-9.9.307")

	candidates, err := svc.ListRetentionCandidates(w.ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]updatemgr.RetentionCandidate{}
	for _, c := range candidates {
		byID[c.ReleaseID] = c
	}
	if byID["omnira-9.9.306"].SafeToDelete {
		t.Error("omnira-9.9.306 (A, ainda a proveniência da API) nunca deveria ser safe_to_delete, mesmo com B sendo o SUCCEEDED mais recente no geral")
	}
	if byID["omnira-9.9.307"].SafeToDelete {
		t.Error("omnira-9.9.307 (B, current_release_id) nunca deveria ser safe_to_delete")
	}

	if _, err := svc.DeleteReleaseCatalogEntry(w.ctx, op, "gc-key-f9-0306", "omnira-9.9.306"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("esperava ErrInvalid (ainda fornece a API via o ledger local), veio %v", err)
	}
	if n := w.count(`SELECT count(*) FROM platform_releases WHERE release_id = $1`, "omnira-9.9.306"); n != 1 {
		t.Errorf("omnira-9.9.306 nunca deveria ter sido removida, achei %d linhas", n)
	}
}

// TestUpdateMgr_DeleteReleaseCatalogEntry_RefusesWhenActiveRunReferencesRelease é a prova
// central do achado Codex [P2] (rodada 3 desta fatia): um run ATIVO (ainda não SUCCEEDED,
// ex. DEPLOYING/HEALTH_CHECKING já aplicou o digest novo de verdade) para a release ENTRANDO
// nunca aparecia na proveniência histórica (que só olha runs TERMINAIS) nem em
// current_release_id (que só avança quando o run termina com sucesso). Sem proteger isso, GC
// podia remover a release sendo implantada agora mesmo, no meio do próprio deploy.
func TestUpdateMgr_DeleteReleaseCatalogEntry_RefusesWhenActiveRunReferencesRelease(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc := updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	m := manifestJSONChannel(t, "omnira-9.9.308", domain.ChannelCandidate, []string{"0.1.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0308", m); err != nil {
		t.Fatal(err)
	}

	// Run ATIVO, nunca terminal — simula "a implantação está em andamento agora mesmo"
	// (ex. já passou de DEPLOYING, containers novos já de pé, mas HEALTH_CHECKING/OBSERVING
	// ainda não concluíram).
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-updatectl", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.CreateRun(w.ctx, h, "run-9.9.308-active", "idem-9.9.308", string(m), domain.StateLocked); err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.RunState{domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying, domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying} {
		if err := ledger.TransitionRun(w.ctx, h, "run-9.9.308-active", st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	// Nunca chega a SUCCEEDED/ReleaseLock — o run continua ATIVO de propósito.
	ledger.Close()

	w.exec(`UPDATE platform_releases SET ingested_at = now() - interval '400 days' WHERE release_id = $1`, "omnira-9.9.308")

	candidates, err := svc.ListRetentionCandidates(w.ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range candidates {
		if c.ReleaseID == "omnira-9.9.308" && c.SafeToDelete {
			t.Error("uma release com um run ATIVO (deploy em andamento) nunca deveria ser safe_to_delete")
		}
	}

	if _, err := svc.DeleteReleaseCatalogEntry(w.ctx, op, "gc-key-f9-0308", "omnira-9.9.308"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("esperava ErrInvalid (run ativo referenciando a release), veio %v", err)
	}
	if n := w.count(`SELECT count(*) FROM platform_releases WHERE release_id = $1`, "omnira-9.9.308"); n != 1 {
		t.Errorf("omnira-9.9.308 nunca deveria ter sido removida enquanto o deploy está ativo, achei %d linhas", n)
	}
}

// TestUpdateMgr_DeleteReleaseCatalogEntry_FailsClosedWhenLedgerUnreadable é a prova central
// do achado Codex [P1] (rodada 3 desta fatia): um ledger ILEGÍVEL (corrompido, permissão,
// disco) nunca pode ser tratado como "nada protegido" — para uma ação DESTRUTIVA, "não
// consigo confirmar" é fail-closed, nunca "presumo que está tudo bem".
func TestUpdateMgr_DeleteReleaseCatalogEntry_FailsClosedWhenLedgerUnreadable(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	// Um arquivo que EXISTE mas não é um banco SQLite válido — simula corrupção real, sem
	// precisar derrubar permissões/infra de verdade.
	if err := os.WriteFile(ledgerPath, []byte("isto não é um banco SQLite"), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	m := manifestJSONChannel(t, "omnira-9.9.309", domain.ChannelCandidate, []string{"0.1.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0309", m); err != nil {
		t.Fatal(err)
	}
	w.exec(`UPDATE platform_releases SET ingested_at = now() - interval '400 days' WHERE release_id = $1`, "omnira-9.9.309")

	if _, err := svc.ListRetentionCandidates(w.ctx, op); err == nil {
		t.Error("ListRetentionCandidates deveria falhar (fail-closed) com um ledger ilegível, nunca reportar silenciosamente 'nada protegido'")
	}
	if _, err := svc.DeleteReleaseCatalogEntry(w.ctx, op, "gc-key-f9-0309", "omnira-9.9.309"); err == nil {
		t.Error("DeleteReleaseCatalogEntry deveria falhar (fail-closed) com um ledger ilegível, nunca remover a release sem confirmar sua proveniência")
	}
	if n := w.count(`SELECT count(*) FROM platform_releases WHERE release_id = $1`, "omnira-9.9.309"); n != 1 {
		t.Errorf("omnira-9.9.309 nunca deveria ter sido removida com o ledger ilegível, achei %d linhas", n)
	}
}
