package updatemgr_test

// F9 (ADR-0054 §2.1/§2.2, release/policies/{backup,signing}.yaml): prova contra Postgres real
// (nunca mockada) de que (a) o gate de backup-evidence do canal "stable" é BLOQUEANTE de
// verdade — nunca só um WARN como em F6 — e (b) revogação é respeitada em TODO ponto de
// revalidação (CreatePlan, StartRun replay, StartRun novo despacho), nunca só na ingestão —
// exatamente o "anti_replay.revalidate_before_apply" que signing.yaml exige: uma release
// revogada DEPOIS de um plano PASS, mas ANTES do StartRun, nunca pode ser despachada só porque
// o plano cacheado dizia PASS.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/platformrelease/domain"
	"github.com/omnira/omnira/internal/platformrelease/updatemgr"
)

// manifestJSONChannel espelha manifestJSON/manifestJSONv (mesmo arquivo de teste), mas permite
// declarar o canal — necessário aqui porque o gate de backup-evidence só é bloqueante para
// "stable" (release/policies/backup.yaml "promotion_gate.stable_channel"), nunca para
// "candidate"/"dev" (que continuam no WARN-only de F6).
func manifestJSONChannel(t *testing.T, releaseID string, channel domain.Channel, fromVersions []string) []byte {
	t.Helper()
	m := domain.ReleaseManifest{
		SchemaVersion:   domain.SupportedSchemaVersion,
		ReleaseID:       releaseID,
		PlatformVersion: "9.9.1",
		Channel:         channel,
		Source: domain.Source{
			Repository:    "k3g/omnira",
			CommitSHA:     "4f6b5c1e2a9d8f3b7c0a1e4d9f2b6c8a0e1d3f5b",
			WorkflowRunID: "999",
		},
		Artifacts: []domain.Artifact{
			{Unit: domain.UnitAPI, Image: "ghcr.io/k3g/omnira-api@sha256:" + sha64("api-"+releaseID)},
		},
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
			Workloads:       []domain.BuildUnit{domain.UnitAPI},
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

func TestUpdateMgr_StableChannel_BackupEvidenceGate_BlocksWithoutFreshEvidence(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	w.exec(`UPDATE platform_installations SET current_version = '9.0.0'`)
	m := manifestJSONChannel(t, "omnira-9.9.100", domain.ChannelStable, []string{"9.0.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0001", m); err != nil {
		t.Fatal(err)
	}
	plan, _, err := svc.CreatePlan(w.ctx, op, "plan-key-f9-0001", "omnira-9.9.100")
	if err != nil {
		t.Fatal(err)
	}
	if plan.PreflightStatus != "BLOCK" {
		t.Fatalf("release stable SEM evidência de backup deveria BLOCK (gate real de F9, nunca só WARN), veio %s: %s", plan.PreflightStatus, plan.PreflightDetail)
	}
	var detail map[string]any
	if err := json.Unmarshal(plan.PreflightDetail, &detail); err != nil {
		t.Fatal(err)
	}
	reasons, _ := detail["reasons"].([]any)
	found := false
	for _, r := range reasons {
		if s, ok := r.(string); ok && strings.Contains(s, "backup.yaml") {
			found = true
		}
	}
	if !found {
		t.Errorf("reasons deveria citar o gate de backup.yaml, veio: %v", reasons)
	}
	// Nenhuma mutação segue de um plano BLOCK — StartRun recusa.
	if _, err := svc.StartRun(w.ctx, op, plan.ID, "run-key-f9-0001"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("StartRun deveria recusar um plano BLOCK: %v", err)
	}
}

func TestUpdateMgr_StableChannel_BackupEvidenceGate_PassesWithFreshVerifiedEvidence(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	w.exec(`UPDATE platform_installations SET current_version = '9.0.0'`)
	if _, _, err := svc.RecordBackupEvidence(w.ctx, op, "backup-key-f9-0001", "logical_dump", time.Now(), true, nil, "", nil); err != nil {
		t.Fatal(err)
	}
	m := manifestJSONChannel(t, "omnira-9.9.101", domain.ChannelStable, []string{"9.0.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0002", m); err != nil {
		t.Fatal(err)
	}
	plan, _, err := svc.CreatePlan(w.ctx, op, "plan-key-f9-0002", "omnira-9.9.101")
	if err != nil {
		t.Fatal(err)
	}
	if plan.PreflightStatus != "PASS" {
		t.Fatalf("release stable COM evidência fresca/verificada deveria PASS, veio %s: %s", plan.PreflightStatus, plan.PreflightDetail)
	}
}

func TestUpdateMgr_CandidateChannel_BackupEvidenceGate_NeverBlocks(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	w.exec(`UPDATE platform_installations SET current_version = '9.0.0'`)
	// canal candidate, sem NENHUMA evidência de backup registrada — comportamento WARN-only de
	// F6 nunca deve regredir para BLOCK fora do canal stable (release/policies/backup.yaml: o
	// gate é de PROMOÇÃO a stable, nunca de staging/candidate).
	m := manifestJSONChannel(t, "omnira-9.9.102", domain.ChannelCandidate, []string{"9.0.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0003", m); err != nil {
		t.Fatal(err)
	}
	plan, _, err := svc.CreatePlan(w.ctx, op, "plan-key-f9-0003", "omnira-9.9.102")
	if err != nil {
		t.Fatal(err)
	}
	if plan.PreflightStatus != "PASS" {
		t.Fatalf("release candidate sem evidência de backup deveria continuar PASS (WARN-only), veio %s: %s", plan.PreflightStatus, plan.PreflightDetail)
	}
}

// TestUpdateMgr_RevokedRelease_RejectedAtEveryRevalidationPoint é a prova central de
// anti-replay (signing.yaml "anti_replay.revalidate_before_apply"): revoga a release DEPOIS de
// um plano PASS já existir, e confirma que StartRun recusa o despacho mesmo assim — nunca
// confia no preflight cacheado. Também prova CreatePlan (ingestão-adjacente) e a idempotência
// de RevokeRelease.
func TestUpdateMgr_RevokedRelease_RejectedAtEveryRevalidationPoint(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	w.exec(`UPDATE platform_installations SET current_version = '9.0.0'`)
	m := manifestJSONChannel(t, "omnira-9.9.103", domain.ChannelCandidate, []string{"9.0.0"})
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-f9-0004", m); err != nil {
		t.Fatal(err)
	}
	plan, _, err := svc.CreatePlan(w.ctx, op, "plan-key-f9-0004", "omnira-9.9.103")
	if err != nil {
		t.Fatal(err)
	}
	if plan.PreflightStatus != "PASS" {
		t.Fatalf("esperava PASS antes da revogação, veio %s: %s", plan.PreflightStatus, plan.PreflightDetail)
	}

	// Revoga DEPOIS do plano PASS — simula uma release aprovada que se descobre comprometida
	// enquanto um operador ainda tem o plano aberto na tela.
	rec, replayed, err := svc.RevokeRelease(w.ctx, op, "revoke-key-f9-0001", "omnira-9.9.103", "vulnerabilidade descoberta após aprovação")
	if err != nil {
		t.Fatal(err)
	}
	if replayed {
		t.Error("primeira chamada de RevokeRelease nunca deveria ser replayed")
	}
	if rec.ReleaseID != "omnira-9.9.103" || rec.Reason == "" {
		t.Errorf("Revocation incompleta: %+v", rec)
	}

	// StartRun PRECISA recusar — o plano foi calculado ANTES da revogação, mas anti_replay
	// exige revalidar no momento do APPLY, nunca confiar só no PASS cacheado.
	before := w.count(`SELECT count(*) FROM platform_update_runs WHERE release_id = $1`, "omnira-9.9.103")
	if _, err := svc.StartRun(w.ctx, op, plan.ID, "run-key-f9-0002"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("StartRun deveria recusar uma release revogada mesmo com plano PASS: %v", err)
	}
	if after := w.count(`SELECT count(*) FROM platform_update_runs WHERE release_id = $1`, "omnira-9.9.103"); after != before {
		t.Errorf("um run foi criado a partir de uma release revogada: %d -> %d", before, after)
	}

	// Um NOVO CreatePlan para a mesma release (nunca confiando no plano antigo) também recusa.
	plan2, _, err := svc.CreatePlan(w.ctx, op, "plan-key-f9-0005", "omnira-9.9.103")
	if err != nil {
		t.Fatal(err)
	}
	if plan2.PreflightStatus != "BLOCK" {
		t.Fatalf("CreatePlan para uma release revogada deveria BLOCK, veio %s: %s", plan2.PreflightStatus, plan2.PreflightDetail)
	}

	// Idempotência: a MESMA Idempotency-Key reaproveita a revogação já registrada (replay).
	rec2, replayed2, err := svc.RevokeRelease(w.ctx, op, "revoke-key-f9-0001", "omnira-9.9.103", "vulnerabilidade descoberta após aprovação")
	if err != nil {
		t.Fatal(err)
	}
	if !replayed2 {
		t.Error("segunda chamada com a MESMA Idempotency-Key deveria ser replayed")
	}
	if rec2.RevokedAt != rec.RevokedAt {
		t.Errorf("replay deveria devolver o MESMO revoked_at, veio %v (original %v)", rec2.RevokedAt, rec.RevokedAt)
	}

	// Uma Idempotency-Key DIFERENTE para o MESMO release_id já revogado falha (append-only —
	// nunca uma segunda revogação "reescrevendo" a primeira).
	if _, _, err := svc.RevokeRelease(w.ctx, op, "revoke-key-f9-0002-outra", "omnira-9.9.103", "motivo diferente"); err == nil {
		t.Error("uma segunda revogação da MESMA release com Idempotency-Key diferente deveria falhar (append-only, PK já existe)")
	}
}
