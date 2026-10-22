package updatemgr_test

// F9 (spec "F9 — ... hardening e revisão independente", item "Retenção, garbage collection
// segura" — release/policies/channels.yaml "retention_days"): prova contra Postgres real de
// que Service.ListRetentionCandidates relata corretamente a janela de retenção e a segurança
// de exclusão, e que Service.DeleteReleaseCatalogEntry NUNCA remove uma release ainda dentro
// da retenção ou referenciada por revogação/plano/run — mesmo quando um relatório antigo
// dizia "seguro".

import (
	"errors"
	"testing"

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
