package updatemgr_test

// F9 (spec "F9 — ... hardening e revisão independente", item "Monitoramento: status do
// agente, falhas de backup, versão real/digest divergente, update estagnado, erros após
// release"): prova contra Postgres real (nunca mockado) e o ledger local real (SQLite) de que
// Service.GetMonitoringStatus reporta corretamente cada um dos cinco sinais.

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/omnira/omnira/internal/platformrelease/agent"
	"github.com/omnira/omnira/internal/platformrelease/domain"
	"github.com/omnira/omnira/internal/platformrelease/updatemgr"
)

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
