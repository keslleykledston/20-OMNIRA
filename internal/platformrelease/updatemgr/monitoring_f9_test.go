package updatemgr_test

// F9 (spec "F9 — ... hardening e revisão independente", item "Monitoramento: status do
// agente, falhas de backup, versão real/digest divergente, update estagnado, erros após
// release"): prova contra Postgres real (nunca mockado) e o ledger local real (SQLite) de que
// Service.GetMonitoringStatus reporta corretamente cada um dos cinco sinais.

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/omnira/omnira/internal/platformrelease/agent"
	"github.com/omnira/omnira/internal/platformrelease/domain"
	"github.com/omnira/omnira/internal/platformrelease/updatemgr"
)

// manifestJSONForUnit (F9, monitoramento "versão real/digest divergente", achado Codex [P2]
// rodada 2) constrói um manifesto que toca SÓ `unit` — manifestJSONChannel (mesmo arquivo de
// teste) está fixo em UnitAPI; os testes de drift parcial precisam de releases que tocam
// units DIFERENTES, exatamente como rolloutUnits permite de verdade.
func manifestJSONForUnit(t *testing.T, releaseID string, unit domain.BuildUnit, image string, fromVersions []string) []byte {
	t.Helper()
	m := domain.ReleaseManifest{
		SchemaVersion:   domain.SupportedSchemaVersion,
		ReleaseID:       releaseID,
		PlatformVersion: "9.9.1",
		Channel:         domain.ChannelCandidate,
		Source: domain.Source{
			Repository:    "k3g/omnira",
			CommitSHA:     "4f6b5c1e2a9d8f3b7c0a1e4d9f2b6c8a0e1d3f5b",
			WorkflowRunID: "999",
		},
		Artifacts:      []domain.Artifact{{Unit: unit, Image: image}},
		ModulesChanged: []string{"platform_release"},
		Compatibility: domain.Compatibility{
			FromVersions:     fromVersions,
			MinAgentProtocol: 1,
			DatabaseStrategy: "expand_only",
		},
		Migrations: domain.Migrations{
			StartAfter:     "000168",
			Target:         "000172",
			ChecksumBundle: "sha256:" + sha64("mig-"+releaseID),
		},
		Rollout: domain.Rollout{
			Workloads:       []domain.BuildUnit{unit},
			PreflightPolicy: "strict",
			RecoveryClass:   domain.RecoveryR1,
		},
		ApprovedBy: "test-suite",
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := domain.ParseManifest(raw); err != nil {
		t.Fatalf("manifesto de teste não passa no próprio parser: %v", err)
	}
	return raw
}

// backdateLedgerColumn simula o tempo passando sem precisar esperar de verdade (os limiares de
// agentHeartbeatStaleAfter/stalledRunThreshold são minutos reais) — abre uma SEGUNDA conexão
// direta ao MESMO arquivo .db e grava um timestamp já antigo, nunca inventa um mecanismo de
// tempo simulado no código de produção.
func backdateLedgerColumn(t *testing.T, ledgerPath, table, column, whereCol, whereVal string, age time.Duration) {
	t.Helper()
	db, err := sql.Open("sqlite", ledgerPath)
	if err != nil {
		t.Fatalf("backdateLedgerColumn: abrir %s: %v", ledgerPath, err)
	}
	defer db.Close()
	old := time.Now().UTC().Add(-age).Format(time.RFC3339Nano)
	if _, err := db.Exec("UPDATE "+table+" SET "+column+" = ? WHERE "+whereCol+" = ?", old, whereVal); err != nil {
		t.Fatalf("backdateLedgerColumn: UPDATE %s.%s: %v", table, column, err)
	}
}

func TestUpdateMgr_GetMonitoringStatus_AgentHeartbeat_UnseenFreshAndStale(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc := updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	// Nenhum daemon jamais rodou (ledger nem existe ainda) — "nunca visto", nunca "obsoleto".
	status, err := svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (sem ledger): %v", err)
	}
	if status.AgentHeartbeatSeen {
		t.Error("sem nenhum ledger/heartbeat, AgentHeartbeatSeen deveria ser false")
	}
	if status.AgentStale {
		t.Error("nunca visto nunca é 'obsoleto' — só um heartbeat visto E velho demais é")
	}

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordAgentHeartbeat(w.ctx, "test-install-1", "daemon-1"); err != nil {
		t.Fatal(err)
	}
	ledger.Close()

	status, err = svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (heartbeat recente): %v", err)
	}
	if !status.AgentHeartbeatSeen || status.AgentStale {
		t.Errorf("heartbeat recém-gravado deveria ser visto e NÃO obsoleto: seen=%v stale=%v", status.AgentHeartbeatSeen, status.AgentStale)
	}
	if status.AgentHolder != "daemon-1" {
		t.Errorf("AgentHolder = %q, esperava daemon-1", status.AgentHolder)
	}

	backdateLedgerColumn(t, ledgerPath, "agent_heartbeat", "last_poll_at", "installation_id", "test-install-1", 10*time.Minute)

	status, err = svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (heartbeat obsoleto): %v", err)
	}
	if !status.AgentHeartbeatSeen || !status.AgentStale {
		t.Errorf("heartbeat de 10min atrás deveria ser visto E obsoleto: seen=%v stale=%v", status.AgentHeartbeatSeen, status.AgentStale)
	}
}

func TestUpdateMgr_GetMonitoringStatus_BackupEvidence_AbsentFreshAndStale(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	svc := newService(t, w)

	status, err := svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (sem evidência nenhuma): %v", err)
	}
	if status.BackupEvidencePresent || !status.BackupEvidenceStale {
		t.Errorf("sem nenhuma evidência: present=%v stale=%v, esperava present=false stale=true", status.BackupEvidencePresent, status.BackupEvidenceStale)
	}

	w.exec(`INSERT INTO platform_backup_records (id, kind, taken_at, verified, detail, created_by) VALUES (gen_random_uuid(), 'logical_dump', now(), true, '{}', $1)`, op)
	status, err = svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (evidência fresca): %v", err)
	}
	if !status.BackupEvidencePresent || status.BackupEvidenceStale || !status.BackupEvidenceVerified {
		t.Errorf("evidência fresca e verificada deveria ser present=true stale=false verified=true, veio present=%v stale=%v verified=%v",
			status.BackupEvidencePresent, status.BackupEvidenceStale, status.BackupEvidenceVerified)
	}

	w.exec(`UPDATE platform_backup_records SET taken_at = now() - interval '48 hours'`)
	status, err = svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (evidência envelhecida): %v", err)
	}
	if !status.BackupEvidencePresent || !status.BackupEvidenceStale {
		t.Errorf("evidência de 48h deveria ser present=true stale=true, veio present=%v stale=%v", status.BackupEvidencePresent, status.BackupEvidenceStale)
	}
}

func TestUpdateMgr_GetMonitoringStatus_VersionDivergence_ManualOverrideNeverSilentlyCorrected(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc := updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	m := manifestJSONChannel(t, "omnira-9.9.201", domain.ChannelCandidate, []string{"0.1.0"})
	if _, err := ledger.CreateRun(w.ctx, h, "run-X", "idem-X", string(m), domain.StateLocked); err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
		domain.StateObserving, domain.StateSucceeded,
	} {
		if err := ledger.TransitionRun(w.ctx, h, "run-X", st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	if err := ledger.ReleaseLock(w.ctx, h); err != nil {
		t.Fatal(err)
	}
	ledger.Close()

	// Sem nenhuma divergência ainda: a sincronização normal (refreshPendingRun) já alinhou
	// platform_installations com o run SUCCEEDED do ledger.
	status, err := svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (sincronizado): %v", err)
	}
	if status.VersionDivergence || status.LedgerReleaseID != "omnira-9.9.201" || status.InstallationReleaseID != "omnira-9.9.201" {
		t.Fatalf("esperava LedgerReleaseID=InstallationReleaseID=omnira-9.9.201 sem divergência, veio ledger=%q installation=%q divergence=%v",
			status.LedgerReleaseID, status.InstallationReleaseID, status.VersionDivergence)
	}

	// Achado real deste sistema (ver Service.applyLatestSucceededRunToInstallation, achado
	// Codex [P2]): um SetInstallationVersion manual (ex. ADR-0054 §8 R3/R4, recuperação fora
	// de banda) NUNCA é silenciosamente sobrescrito pela sincronização automática do ledger —
	// de propósito, para nunca desfazer uma decisão explícita de operador. Isso significa que
	// esta divergência é PERSISTENTE até uma release mais nova suceder, nunca "auto-corrigida"
	// pela próxima chamada — exatamente o que este monitoramento precisa continuar reportando.
	if _, err := svc.SetInstallationVersion(w.ctx, op, "idem-override", "9.9.9", "omnira-MANUAL-OVERRIDE"); err != nil {
		t.Fatalf("SetInstallationVersion: %v", err)
	}
	status, err = svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (depois do override manual): %v", err)
	}
	if !status.VersionDivergence {
		t.Fatal("depois de um SetInstallationVersion manual diferente do ledger, VersionDivergence deveria ser true")
	}
	if status.LedgerReleaseID != "omnira-9.9.201" {
		t.Errorf("LedgerReleaseID deveria continuar refletindo o ledger (omnira-9.9.201), veio %q", status.LedgerReleaseID)
	}
	if status.InstallationReleaseID != "omnira-MANUAL-OVERRIDE" {
		t.Errorf("InstallationReleaseID deveria refletir o override manual, veio %q", status.InstallationReleaseID)
	}
}

func TestUpdateMgr_GetMonitoringStatus_StalledRun_DetectsOldActiveRun(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc := updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	m := manifestJSONChannel(t, "omnira-9.9.202", domain.ChannelCandidate, []string{"0.1.0"})
	if _, err := ledger.CreateRun(w.ctx, h, "run-stalled", "idem-stalled", string(m), domain.StateLocked); err != nil {
		t.Fatal(err)
	}
	if err := ledger.TransitionRun(w.ctx, h, "run-stalled", domain.StateBackingUp); err != nil {
		t.Fatal(err)
	}
	ledger.Close()

	status, err := svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (run ativo recente): %v", err)
	}
	if status.StalledRunID != "" {
		t.Errorf("um run ativo RECÉM-criado nunca deveria ser reportado como estagnado, veio %q", status.StalledRunID)
	}

	backdateLedgerColumn(t, ledgerPath, "update_runs", "updated_at", "run_id", "run-stalled", 20*time.Minute)

	status, err = svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (run estagnado): %v", err)
	}
	if status.StalledRunID != "run-stalled" {
		t.Errorf("StalledRunID = %q, esperava run-stalled", status.StalledRunID)
	}
	if status.StalledRunState != string(domain.StateBackingUp) {
		t.Errorf("StalledRunState = %q, esperava %s", status.StalledRunState, domain.StateBackingUp)
	}
}

func TestUpdateMgr_GetMonitoringStatus_UnresolvedRecoveryRequired_Detected(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc := updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	status, err := svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (sem nenhum run): %v", err)
	}
	if status.UnresolvedRecoveryRequired {
		t.Error("sem nenhum run, UnresolvedRecoveryRequired deveria ser false")
	}

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	m := manifestJSONChannel(t, "omnira-9.9.203", domain.ChannelCandidate, []string{"0.1.0"})
	if _, err := ledger.CreateRun(w.ctx, h, "run-broken", "idem-broken", string(m), domain.StateLocked); err != nil {
		t.Fatal(err)
	}
	// RECOVERY_REQUIRED só é alcançável por FAILED -> RECOVERING -> RECOVERY_REQUIRED
	// (domain.ValidateTransition) — nunca direto de LOCKED.
	for _, st := range []domain.RunState{domain.StateFailed, domain.StateRecovering, domain.StateRecoveryRequired} {
		if err := ledger.TransitionRun(w.ctx, h, "run-broken", st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	ledger.Close()

	status, err = svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (RECOVERY_REQUIRED não reconhecida): %v", err)
	}
	if !status.UnresolvedRecoveryRequired {
		t.Error("com um run em RECOVERY_REQUIRED nunca reconhecido, UnresolvedRecoveryRequired deveria ser true")
	}
}

// TestUpdateMgr_GetMonitoringStatus_RuntimeDigestDrift_DetectedEvenWhenLedgerMatchesPostgres é
// a prova central do achado Codex [P2]: nem o ledger (histórico de runs) nem o Postgres
// (platform_installations) observam containers de verdade por si só — um container
// substituído manualmente, ou uma implantação que falhou deixando outra imagem rodando, nunca
// apareceria na comparação ledger-vs-Postgres (que, aqui, está perfeitamente sincronizada).
// Só o digest REAL observado pelo daemon (RecordObservedDigests) detecta esse drift.
func TestUpdateMgr_GetMonitoringStatus_RuntimeDigestDrift_DetectedEvenWhenLedgerMatchesPostgres(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc := updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	releaseID := "omnira-9.9.204"
	m := manifestJSONChannel(t, releaseID, domain.ChannelCandidate, []string{"0.1.0"})
	if _, err := ledger.CreateRun(w.ctx, h, "run-drift", "idem-drift", string(m), domain.StateLocked); err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
		domain.StateObserving, domain.StateSucceeded,
	} {
		if err := ledger.TransitionRun(w.ctx, h, "run-drift", st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	if err := ledger.ReleaseLock(w.ctx, h); err != nil {
		t.Fatal(err)
	}
	expectedAPIDigest := "ghcr.io/k3g/omnira-api@sha256:" + sha64("api-"+releaseID)

	// Observa o digest EXATO que a release declara — ledger, Postgres (depois de
	// refreshPendingRun) e o Docker real concordam. Nenhuma divergência.
	if err := ledger.RecordObservedDigests(w.ctx, "test-install-1", "test-daemon", `{"api":"`+expectedAPIDigest+`"}`); err != nil {
		t.Fatal(err)
	}
	ledger.Close()

	status, err := svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (digest observado bate): %v", err)
	}
	if status.VersionDivergence {
		t.Fatal("digest observado idêntico ao esperado nunca deveria reportar divergência")
	}

	// Agora o Docker real reporta OUTRO digest para a API (ex. substituição manual de
	// container, ou uma implantação que falhou deixando uma imagem diferente rodando) — o
	// ledger e o Postgres continuam IDÊNTICOS entre si, mas o host real diverge de ambos.
	ledger, err = agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	driftedDigest := "ghcr.io/k3g/omnira-api@sha256:" + sha64("drift-"+releaseID)
	if err := ledger.RecordObservedDigests(w.ctx, "test-install-1", "test-daemon", `{"api":"`+driftedDigest+`"}`); err != nil {
		t.Fatal(err)
	}
	ledger.Close()

	status, err = svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (digest observado diverge): %v", err)
	}
	if !status.VersionDivergence {
		t.Fatal("um digest REAL observado diferente do esperado deveria reportar divergência, mesmo com ledger e Postgres idênticos entre si")
	}
}

// TestUpdateMgr_GetMonitoringStatus_RuntimeDigestDrift_DetectsPartialWorkloadReleases é a
// prova central do achado Codex [P2] (rodada 2): a release SUCCEEDED mais recente no GERAL
// nem sempre toca TODAS as units — release A só atualiza a API (SUCCEEDED); release B, depois,
// só o web (SUCCEEDED, mais recente que A). Um drift na API precisa continuar detectável,
// mesmo que B (não a API) seja a release SUCCEEDED mais recente no geral.
func TestUpdateMgr_GetMonitoringStatus_RuntimeDigestDrift_DetectsPartialWorkloadReleases(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc := updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	driveToSucceeded := func(runID, idemKey string, manifest []byte) {
		t.Helper()
		ledger, err := agent.OpenLedger(ledgerPath)
		if err != nil {
			t.Fatal(err)
		}
		defer ledger.Close()
		h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.CreateRun(w.ctx, h, runID, idemKey, string(manifest), domain.StateLocked); err != nil {
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

	apiDigest := "ghcr.io/k3g/omnira-api@sha256:" + sha64("api-only")
	driveToSucceeded("run-A", "idem-A", manifestJSONForUnit(t, "omnira-9.9.205", domain.UnitAPI, apiDigest, []string{"0.1.0"}))
	webDigest := "ghcr.io/k3g/omnira-web@sha256:" + sha64("web-only")
	driveToSucceeded("run-B", "idem-B", manifestJSONForUnit(t, "omnira-9.9.206", domain.UnitWeb, webDigest, []string{"0.1.0"}))

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	// Ambos observados EXATAMENTE como esperado — nenhuma divergência.
	if err := ledger.RecordObservedDigests(w.ctx, "test-install-1", "test-daemon",
		`{"api":"`+apiDigest+`","web":"`+webDigest+`"}`); err != nil {
		t.Fatal(err)
	}
	ledger.Close()

	status, err := svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (ambos corretos): %v", err)
	}
	if status.VersionDivergence {
		t.Fatal("API e web observados exatamente como as releases A e B declararam nunca deveria reportar divergência")
	}

	// SÓ a API diverge agora — B (web, a release SUCCEEDED mais recente no GERAL) nunca
	// tocou a API, então sem o achado corrigido este drift seria invisível.
	ledger, err = agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	driftedAPIDigest := "ghcr.io/k3g/omnira-api@sha256:" + sha64("api-drifted")
	if err := ledger.RecordObservedDigests(w.ctx, "test-install-1", "test-daemon",
		`{"api":"`+driftedAPIDigest+`","web":"`+webDigest+`"}`); err != nil {
		t.Fatal(err)
	}
	ledger.Close()

	status, err = svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (API drifted): %v", err)
	}
	if !status.VersionDivergence {
		t.Fatal("drift na API deveria ser detectado mesmo com B (web) sendo a release SUCCEEDED mais recente no geral")
	}
}

// TestUpdateMgr_GetMonitoringStatus_RuntimeDigestDrift_DetectsRemovedContainer é a prova
// central do achado Codex [P2] (rodada 2): um got="" é uma observação REAL e bem-sucedida de
// "nenhum container rodando para esta unit" (CurrentSnapshot resolveu e não achou nada) —
// quando uma release SUCCEEDED declara um digest esperado para essa unit, isso é divergência
// de verdade (container removido/substituído por algo não-imutável), nunca "ainda não
// confirmado".
func TestUpdateMgr_GetMonitoringStatus_RuntimeDigestDrift_DetectsRemovedContainer(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc := updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	releaseID := "omnira-9.9.207"
	m := manifestJSONChannel(t, releaseID, domain.ChannelCandidate, []string{"0.1.0"})
	if _, err := ledger.CreateRun(w.ctx, h, "run-removed", "idem-removed", string(m), domain.StateLocked); err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
		domain.StateObserving, domain.StateSucceeded,
	} {
		if err := ledger.TransitionRun(w.ctx, h, "run-removed", st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	if err := ledger.ReleaseLock(w.ctx, h); err != nil {
		t.Fatal(err)
	}
	// CurrentSnapshot resolveu o container e não achou NADA para a API — uma observação real
	// e bem-sucedida, nunca "nunca observado" (que seria a KEY ausente do JSON, não "").
	if err := ledger.RecordObservedDigests(w.ctx, "test-install-1", "test-daemon", `{"api":""}`); err != nil {
		t.Fatal(err)
	}
	ledger.Close()

	status, err := svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (container removido): %v", err)
	}
	if !status.VersionDivergence {
		t.Fatal("um digest esperado com observação real de container AUSENTE deveria reportar divergência, nunca ser ignorado silenciosamente")
	}
}

// observedDigestsStaleAfterForTest espelha o valor não-exportado de
// updatemgr.observedDigestsStaleAfter — mantido em sincronia manualmente (este teste está no
// pacote externo updatemgr_test e não pode importar uma constante não-exportada).
const observedDigestsStaleAfterForTest = 15 * time.Minute

// TestUpdateMgr_GetMonitoringStatus_ObservedDigests_FreshVsStale é a prova central do achado
// Codex [P2] (rodada 3): a observação real do Docker (RecordObservedDigests) tem sua PRÓPRIA
// cadência — se o Docker fica indisponível depois de uma observação bem-sucedida,
// observed_digests/observed_at ficam CONGELADOS para sempre, mas nada avisava disso. Confirma
// que ObservedDigestsAt/ObservedDigestsStale refletem o frescor real, e que um
// VersionDivergence JÁ detectado nunca é escondido só porque a observação envelheceu depois.
func TestUpdateMgr_GetMonitoringStatus_ObservedDigests_FreshVsStale(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc := updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	releaseID := "omnira-9.9.208"
	m := manifestJSONChannel(t, releaseID, domain.ChannelCandidate, []string{"0.1.0"})
	if _, err := ledger.CreateRun(w.ctx, h, "run-obs", "idem-obs", string(m), domain.StateLocked); err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
		domain.StateObserving, domain.StateSucceeded,
	} {
		if err := ledger.TransitionRun(w.ctx, h, "run-obs", st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	if err := ledger.ReleaseLock(w.ctx, h); err != nil {
		t.Fatal(err)
	}
	driftedDigest := "ghcr.io/k3g/omnira-api@sha256:" + sha64("drift-"+releaseID)
	// A observação já é de um digest DIVERGENTE — simula "o Docker caiu bem depois de uma
	// release já ter saído do controle, e desde então nenhuma observação nova aconteceu".
	if err := ledger.RecordObservedDigests(w.ctx, "test-install-1", "test-daemon", `{"api":"`+driftedDigest+`"}`); err != nil {
		t.Fatal(err)
	}
	ledger.Close()

	status, err := svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (observação fresca): %v", err)
	}
	if status.ObservedDigestsAt == nil || status.ObservedDigestsStale {
		t.Errorf("observação recém-gravada deveria ter ObservedDigestsAt definido e ObservedDigestsStale=false, veio at=%v stale=%v", status.ObservedDigestsAt, status.ObservedDigestsStale)
	}
	if !status.VersionDivergence {
		t.Fatal("a divergência já detectada deveria continuar reportada com a observação fresca")
	}

	backdateLedgerColumn(t, ledgerPath, "agent_heartbeat", "observed_at", "installation_id", "test-install-1", observedDigestsStaleAfterForTest+time.Minute)

	status, err = svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (observação obsoleta): %v", err)
	}
	if !status.ObservedDigestsStale {
		t.Error("uma observação de digest antiga deveria ser reportada como obsoleta")
	}
	if !status.VersionDivergence {
		t.Fatal("uma divergência JÁ detectada nunca deveria ser escondida só porque a observação envelheceu depois — ObservedDigestsStale é um sinal SEPARADO, nunca uma supressão")
	}
}

// TestUpdateMgr_GetMonitoringStatus_VersionDivergence_ComparesVersionNotOnlyReleaseID é a
// prova central do achado Codex [P2]: SetInstallationVersion pode registrar uma VERSÃO
// diferente com o MESMO release_id, ou simplesmente omitir o release_id (nenhum dos dois
// campos é obrigatório além de version) — a sincronização automática preserva de propósito
// esse registro manual mais recente, mas comparar só release_id nunca detectava essa
// divergência PERSISTENTE na versão de verdade que checkCompatibility usa.
func TestUpdateMgr_GetMonitoringStatus_VersionDivergence_ComparesVersionNotOnlyReleaseID(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc := updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	releaseID := "omnira-9.9.209"
	m := manifestJSONChannel(t, releaseID, domain.ChannelCandidate, []string{"0.1.0"})
	if _, err := ledger.CreateRun(w.ctx, h, "run-vers", "idem-vers", string(m), domain.StateLocked); err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
		domain.StateObserving, domain.StateSucceeded,
	} {
		if err := ledger.TransitionRun(w.ctx, h, "run-vers", st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	if err := ledger.ReleaseLock(w.ctx, h); err != nil {
		t.Fatal(err)
	}
	ledger.Close()

	// SetInstallationVersion com o MESMO release_id, mas uma versão DIFERENTE da que o
	// manifesto da release realmente declara (9.9.1, via manifestJSONChannel).
	if _, err := svc.SetInstallationVersion(w.ctx, op, "setver-key-f9-0209a", "9.9.999-manual", releaseID); err != nil {
		t.Fatal(err)
	}
	status, err := svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (mesmo release_id, versão diferente): %v", err)
	}
	if status.InstallationReleaseID != status.LedgerReleaseID {
		t.Fatalf("premissa do teste: release_id deveria coincidir, veio ledger=%q installation=%q", status.LedgerReleaseID, status.InstallationReleaseID)
	}
	if !status.VersionDivergence {
		t.Error("mesmo release_id mas versão DIFERENTE deveria reportar divergência — é a versão, não o release_id, que checkCompatibility usa")
	}

	// SetInstallationVersion omitindo o release_id inteiramente — só version é obrigatório.
	if _, err := svc.SetInstallationVersion(w.ctx, op, "setver-key-f9-0209b", "9.9.999-manual-sem-release", ""); err != nil {
		t.Fatal(err)
	}
	status, err = svc.GetMonitoringStatus(w.ctx, op)
	if err != nil {
		t.Fatalf("GetMonitoringStatus (release_id omitido): %v", err)
	}
	if status.InstallationReleaseID != "" {
		t.Fatalf("premissa do teste: InstallationReleaseID deveria estar vazio, veio %q", status.InstallationReleaseID)
	}
	if !status.VersionDivergence {
		t.Error("release_id omitido mas versão DIFERENTE do ledger deveria reportar divergência, nunca ser mascarado por InstallationReleaseID vazio")
	}
}
