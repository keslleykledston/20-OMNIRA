package updatemgr_test

// Prova contra Postgres real (nunca mockada) de que o Update Manager API (F6, ADR-0054 §2.1)
// nunca aceita nada além de um platform operator ATIVO — a especificação pede explicitamente
// testar negativa de tenant admin, delegado de Hub, operador K3G (positivo) e "CRM externo"/API
// Headless (que nem chega a autenticar como authn.Principal: simulado aqui por uuid.Nil, o
// mesmo valor que o middleware produziria se não houvesse authn.Principal algum). Também prova o
// preflight (compatibility.yaml) e o caminho real de StartRun escrevendo no ledger SQLite local
// (o MESMO arquivo que o Update Agent usa, nunca um caminho HTTP/Docker separado).

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/platformrelease/agent"
	"github.com/omnira/omnira/internal/platformrelease/domain"
	"github.com/omnira/omnira/internal/platformrelease/updatemgr"
)

func manifestJSON(t *testing.T, releaseID string, fromVersions []string, minAgentProtocol int) []byte {
	t.Helper()
	return manifestJSONv(t, releaseID, "9.9.1", fromVersions, minAgentProtocol)
}

func manifestJSONv(t *testing.T, releaseID, platformVersion string, fromVersions []string, minAgentProtocol int) []byte {
	t.Helper()
	m := domain.ReleaseManifest{
		SchemaVersion:   domain.SupportedSchemaVersion,
		ReleaseID:       releaseID,
		PlatformVersion: platformVersion,
		Channel:         domain.ChannelCandidate,
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
			MinAgentProtocol: minAgentProtocol,
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
	// o próprio parser estrito precisa aceitar o que construímos aqui — provamos a integração
	// real com F1, nunca um atalho que o backend aceitaria mas o verificador rejeitaria.
	if _, err := domain.ParseManifest(raw); err != nil {
		t.Fatalf("manifesto de teste não passa no próprio parser: %v", err)
	}
	return raw
}

func sha64(seed string) string {
	// 64 hex chars determinísticos a partir de uma seed curta — não precisa ser um sha256 real,
	// só satisfazer o regex de digest do validador (nunca usado para comparar bytes reais aqui).
	out := make([]byte, 64)
	for i := range out {
		out[i] = "0123456789abcdef"[(seed[i%len(seed)]+byte(i))%16]
	}
	return string(out)
}

// testAllowAllVerifier nunca é usado por apps/api/cmd/omnira-api/main.go — o default de
// produção (blockedExternalVerifier, dentro de updatemgr.New) recusa toda ingestão até um
// verificador real existir (F9, achado Codex [P1]: forma válida nunca é autenticidade). Os
// testes deste pacote testam o PLANNER/PREFLIGHT/DESPACHO, não a cadeia de assinatura real
// (BLOCKED_EXTERNAL, F0 §7) — por isso injetam este verificador, nomeado para nunca ser
// confundido com um verificador de produção.
type testAllowAllVerifier struct{}

func (testAllowAllVerifier) Verify([]byte) error { return nil }

func newService(t *testing.T, w *world) *updatemgr.Service {
	t.Helper()
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	return updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})
}

func TestUpdateMgr_AuthorizationBoundary(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")
	tenantAdmin := w.tenantAdmin("tadmin")
	hubAdmin := w.hubDelegate("hubadmin")
	headless := uuid.Nil // a API Headless nem autentica como authn.Principal de usuário

	manifest := manifestJSON(t, "omnira-9.9.1", []string{"9.9.0"}, 1)

	t.Run("a platform operator ativo passa", func(t *testing.T) {
		if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0001", manifest); err != nil {
			t.Fatalf("IngestRelease: %v", err)
		}
		if _, err := svc.GetInstallation(w.ctx, op); err != nil {
			t.Fatalf("GetInstallation: %v", err)
		}
		if _, err := svc.ListReleases(w.ctx, op, 10, nil); err != nil {
			t.Fatalf("ListReleases: %v", err)
		}
	})

	for name, u := range map[string]uuid.UUID{"tenant admin": tenantAdmin, "hub delegate": hubAdmin, "CRM externo/headless": headless, "nobody": uuid.New()} {
		t.Run(name+" é recusado em toda operação", func(t *testing.T) {
			if _, _, err := svc.IngestRelease(w.ctx, u, "ingest-key-0002", manifest); !errors.Is(err, updatemgr.ErrForbidden) {
				t.Errorf("IngestRelease: %v", err)
			}
			if _, err := svc.GetInstallation(w.ctx, u); !errors.Is(err, updatemgr.ErrForbidden) {
				t.Errorf("GetInstallation: %v", err)
			}
			if _, err := svc.ListReleases(w.ctx, u, 10, nil); !errors.Is(err, updatemgr.ErrForbidden) {
				t.Errorf("ListReleases: %v", err)
			}
			if _, _, err := svc.CreatePlan(w.ctx, u, "plan-key-0001", "omnira-9.9.1"); !errors.Is(err, updatemgr.ErrForbidden) {
				t.Errorf("CreatePlan: %v", err)
			}
			if _, err := svc.StartRun(w.ctx, u, uuid.New(), "some-key-00001"); !errors.Is(err, updatemgr.ErrForbidden) {
				t.Errorf("StartRun: %v", err)
			}
			if _, err := svc.GetRun(w.ctx, u, "run-x"); !errors.Is(err, updatemgr.ErrForbidden) {
				t.Errorf("GetRun: %v", err)
			}
			if _, err := svc.ListRuns(w.ctx, u, 10, nil); !errors.Is(err, updatemgr.ErrForbidden) {
				t.Errorf("ListRuns: %v", err)
			}
			if _, _, err := svc.RecordBackupEvidence(w.ctx, u, "backup-key-00000001", "logical_dump", time.Now(), true, nil, "", nil); !errors.Is(err, updatemgr.ErrForbidden) {
				t.Errorf("RecordBackupEvidence: %v", err)
			}
		})
	}

	t.Run("um operador revogado perde o acesso imediatamente", func(t *testing.T) {
		if _, err := svc.ListReleases(w.ctx, op, 10, nil); err != nil {
			t.Fatalf("antes de revogar: %v", err)
		}
		w.revoke(op)
		if _, err := svc.ListReleases(w.ctx, op, 10, nil); !errors.Is(err, updatemgr.ErrForbidden) {
			t.Errorf("depois de revogar: %v", err)
		}
		w.exec(`UPDATE platform_operators SET status = 'active', revoked_at = NULL WHERE user_id = $1`, op)
	})

	// platform_releases é uma tabela GLOBAL (sem tenant_id) compartilhada por TODAS as funções
	// de teste deste pacote na mesma execução de `go test` (mesmo banco Postgres descartável,
	// sem isolamento por transação entre funções) — por isso o escopo é pelo release_id desta
	// função, nunca uma contagem absoluta da tabela inteira.
	if n := w.count(`SELECT count(*) FROM platform_releases WHERE release_id = $1`, "omnira-9.9.1"); n != 1 {
		t.Errorf("exatamente a ingestão do operador legítimo deveria ter persistido, achei %d releases", n)
	}
}

func TestUpdateMgr_IngestRelease_ValidationAndIdempotency(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	t.Run("manifesto inválido é recusado e nada é persistido", func(t *testing.T) {
		before := w.count(`SELECT count(*) FROM platform_releases`)
		if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0003", []byte(`{"not":"a manifest"}`)); !errors.Is(err, updatemgr.ErrInvalid) {
			t.Errorf("%v", err)
		}
		if after := w.count(`SELECT count(*) FROM platform_releases`); after != before {
			t.Errorf("manifesto inválido persistiu: %d -> %d", before, after)
		}
	})

	m1 := manifestJSON(t, "omnira-9.9.2", []string{"9.9.1"}, 1)
	rel, replayed, err := svc.IngestRelease(w.ctx, op, "ingest-key-0004", m1)
	if err != nil || replayed {
		t.Fatalf("primeira ingestão: %v replayed=%v", err, replayed)
	}
	if rel.ReleaseID != "omnira-9.9.2" {
		t.Fatalf("release_id: %q", rel.ReleaseID)
	}

	t.Run("reingestão do MESMO manifesto é um replay idempotente, nunca duplica", func(t *testing.T) {
		_, replayed, err := svc.IngestRelease(w.ctx, op, "ingest-key-0005", m1)
		if err != nil || !replayed {
			t.Fatalf("%v replayed=%v", err, replayed)
		}
		if n := w.count(`SELECT count(*) FROM platform_releases WHERE release_id = $1`, "omnira-9.9.2"); n != 1 {
			t.Errorf("duplicou: %d linhas", n)
		}
	})

	t.Run("o MESMO release_id com manifesto diferente é recusado — release assinada é imutável", func(t *testing.T) {
		m2 := manifestJSON(t, "omnira-9.9.2", []string{"8.0.0"}, 1) // mesmo release_id, from_versions diferente
		if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0006", m2); !errors.Is(err, updatemgr.ErrKeyMismatch) {
			t.Errorf("%v", err)
		}
	})

	t.Run("reingestão com a MESMA chave e o MESMO manifesto é um replay, nunca recusado", func(t *testing.T) {
		rel2, replayed, err := svc.IngestRelease(w.ctx, op, "ingest-key-0004", m1)
		if err != nil || !replayed || rel2.ReleaseID != "omnira-9.9.2" {
			t.Fatalf("%v replayed=%v %+v", err, replayed, rel2)
		}
	})

	// Achado Codex [P2]: o dedup por release_id protege contra reingerir a MESMA release, mas
	// sozinho nunca detectava "a MESMA Idempotency-Key usada para uma release DIFERENTE" — o
	// contrato documentado (422) promete a chave em si como identidade do pedido.
	t.Run("a MESMA Idempotency-Key usada para uma release DIFERENTE é recusada, nunca aceita outra release", func(t *testing.T) {
		m3 := manifestJSON(t, "omnira-9.9.20", []string{"1.0.0"}, 1) // release_id DIFERENTE de m1
		if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0004", m3); !errors.Is(err, updatemgr.ErrKeyMismatch) {
			t.Errorf("%v", err)
		}
		if n := w.count(`SELECT count(*) FROM platform_releases WHERE release_id = $1`, "omnira-9.9.20"); n != 0 {
			t.Errorf("a release de conteúdo diferente foi aceita sob a mesma chave: %d", n)
		}
	})
}

func TestUpdateMgr_PreflightBlocksIncompatibleVersionAndAgentProtocol(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	w.exec(`UPDATE platform_installations SET current_version = '5.0.0'`)

	incompatibleVersion := manifestJSON(t, "omnira-9.9.3", []string{"1.0.0"}, 1) // nunca inclui 5.0.0
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0007", incompatibleVersion); err != nil {
		t.Fatal(err)
	}
	plan, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0002", "omnira-9.9.3")
	if err != nil {
		t.Fatal(err)
	}
	if plan.PreflightStatus != "BLOCK" {
		t.Fatalf("esperava BLOCK por incompatibilidade de versão, veio %s: %s", plan.PreflightStatus, plan.PreflightDetail)
	}
	if _, err := svc.StartRun(w.ctx, op, plan.ID, "run-key-block-0001"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("StartRun a partir de um plano BLOCK deveria ser recusado: %v", err)
	}
	if n := w.count(`SELECT count(*) FROM platform_update_runs`); n != 0 {
		t.Errorf("um run foi criado a partir de um preflight BLOCK: %d", n)
	}

	highProtocol := manifestJSON(t, "omnira-9.9.4", []string{"5.0.0"}, 99)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0008", highProtocol); err != nil {
		t.Fatal(err)
	}
	plan2, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0003", "omnira-9.9.4")
	if err != nil {
		t.Fatal(err)
	}
	if plan2.PreflightStatus != "BLOCK" {
		t.Fatalf("esperava BLOCK por protocolo de agente insuficiente, veio %s: %s", plan2.PreflightStatus, plan2.PreflightDetail)
	}
}

// Achado Codex [P1]: uma instalação recém-migrada (current_version NULL, o estado de TODA
// instalação nova já que nada neste programa ainda o popula a partir de um deploy real) era
// tratada como "compatível com qualquer release" — fail-OPEN, o oposto do que a especificação
// exige. Prova aqui que BLOCK é o padrão, e que SetInstallationVersion é o único jeito de saí-lo.
func TestUpdateMgr_PreflightBlocksWhenInstalledVersionUnknownAndSetInstallationVersionUnblocksIt(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	// platform_installations é um singleton GLOBAL de verdade (ADR-0054 §2.4) — a MESMA linha é
	// compartilhada por toda função de teste deste pacote na mesma execução de `go test` (nunca
	// recriada por newWorld). Reseta explicitamente para simular uma instalação fresca, em vez
	// de assumir isso do estado deixado por outra função de teste.
	w.exec(`UPDATE platform_installations SET current_version = NULL, current_release_id = NULL`)

	m := manifestJSON(t, "omnira-9.9.7", []string{"7.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0009", m); err != nil {
		t.Fatal(err)
	}

	t.Run("versão instalada desconhecida bloqueia, nunca passa por omissão", func(t *testing.T) {
		inst, err := svc.GetInstallation(w.ctx, op)
		if err != nil || inst.CurrentVersion != "" {
			t.Fatalf("esperava uma instalação fresca sem versão: %v %+v", err, inst)
		}
		plan, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0004", "omnira-9.9.7")
		if err != nil {
			t.Fatal(err)
		}
		if plan.PreflightStatus != "BLOCK" {
			t.Fatalf("esperava BLOCK com a versão desconhecida, veio %s: %s", plan.PreflightStatus, plan.PreflightDetail)
		}
		if !bytes.Contains(plan.PreflightDetail, []byte(`"version_known":false`)) {
			t.Errorf("preflight_detail deveria marcar version_known=false: %s", plan.PreflightDetail)
		}
	})

	t.Run("um não-operador não pode registrar a versão instalada", func(t *testing.T) {
		outsider := w.user("outsider-version")
		if _, err := svc.SetInstallationVersion(w.ctx, outsider, "setver-key-0001", "7.0.0", ""); !errors.Is(err, updatemgr.ErrForbidden) {
			t.Errorf("%v", err)
		}
	})

	t.Run("depois de SetInstallationVersion, o mesmo release passa", func(t *testing.T) {
		inst, err := svc.SetInstallationVersion(w.ctx, op, "setver-key-0002", "7.0.0", "")
		if err != nil || inst.CurrentVersion != "7.0.0" {
			t.Fatalf("%v %+v", err, inst)
		}
		plan, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0005", "omnira-9.9.7")
		if err != nil {
			t.Fatal(err)
		}
		if plan.PreflightStatus != "PASS" {
			t.Fatalf("esperava PASS depois de registrar a versão compatível, veio %s: %s", plan.PreflightStatus, plan.PreflightDetail)
		}
	})
}

// Achado Codex [P1]: um plano PASS permanece válido por planExpiry (15min) por tempo, mas a
// instalação pode ter mudado de versão DENTRO dessa janela (outro run concluído) — StartRun
// precisa revalidar a compatibilidade contra o estado ATUAL, nunca confiar só no cache do plano.
func TestUpdateMgr_StartRun_RevalidatesCompatibilityAtDispatchTime(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	w.exec(`UPDATE platform_installations SET current_version = '6.0.0'`)
	m := manifestJSON(t, "omnira-9.9.8", []string{"6.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0010", m); err != nil {
		t.Fatal(err)
	}
	plan, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0006", "omnira-9.9.8")
	if err != nil {
		t.Fatal(err)
	}
	if plan.PreflightStatus != "PASS" {
		t.Fatalf("esperava PASS, veio %s: %s", plan.PreflightStatus, plan.PreflightDetail)
	}

	// Simula outro run tendo mudado a instalação ENQUANTO este plano ainda era válido.
	w.exec(`UPDATE platform_installations SET current_version = '8.0.0'`)

	before := w.count(`SELECT count(*) FROM platform_update_runs WHERE release_id = $1`, "omnira-9.9.8")
	if _, err := svc.StartRun(w.ctx, op, plan.ID, "run-key-stale-0001"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("StartRun deveria recusar um plano que ficou incompatível desde que foi criado: %v", err)
	}
	if after := w.count(`SELECT count(*) FROM platform_update_runs WHERE release_id = $1`, "omnira-9.9.8"); after != before {
		t.Errorf("um run foi criado a partir de uma revalidação que deveria ter bloqueado: %d -> %d", before, after)
	}
}

func TestUpdateMgr_StartRun_WritesTheSameLocalLedgerTheAgentReads(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc = updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	m := manifestJSON(t, "omnira-9.9.5", []string{"5.0.0"}, 1)
	w.exec(`UPDATE platform_installations SET current_version = '5.0.0'`)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0011", m); err != nil {
		t.Fatal(err)
	}
	plan, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0007", "omnira-9.9.5")
	if err != nil {
		t.Fatal(err)
	}
	if plan.PreflightStatus != "PASS" {
		t.Fatalf("esperava PASS, veio %s: %s", plan.PreflightStatus, plan.PreflightDetail)
	}

	run, err := svc.StartRun(w.ctx, op, plan.ID, "run-key-pass-0001")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if run.State != string(domain.StateLocked) {
		t.Fatalf("estado do run espelhado no Postgres: %q", run.State)
	}

	t.Run("o MESMO ledger local que o Update Agent usa tem o run, no estado LOCKED", func(t *testing.T) {
		ledger, err := agent.OpenLedger(ledgerPath)
		if err != nil {
			t.Fatal(err)
		}
		defer ledger.Close()
		rec, err := ledger.GetRun(w.ctx, run.ID)
		if err != nil {
			t.Fatalf("o run não está no ledger local do agente: %v", err)
		}
		if rec.State != domain.StateLocked || rec.InstallationID != "test-install-1" {
			t.Errorf("%+v", rec)
		}
	})

	t.Run("GetRun responde e o evento run.locked está na trilha append-only", func(t *testing.T) {
		got, err := svc.GetRun(w.ctx, op, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != run.ID {
			t.Errorf("%+v", got)
		}
		events, err := svc.ListEvents(w.ctx, op, run.ID, -1) // -1: do início, nunca viu nenhum evento ainda
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 0 || events[0].Kind != "run.locked" {
			t.Errorf("%+v", events)
		}
	})

	t.Run("repetir a mesma Idempotency-Key devolve o MESMO run, nunca cria outro no ledger nem no Postgres", func(t *testing.T) {
		before := w.count(`SELECT count(*) FROM platform_update_runs`)
		again, err := svc.StartRun(w.ctx, op, plan.ID, "run-key-pass-0001")
		if err != nil || again.ID != run.ID {
			t.Fatalf("%v %+v", err, again)
		}
		if w.count(`SELECT count(*) FROM platform_update_runs`) != before {
			t.Error("um retry com a mesma chave criou um segundo run")
		}
	})

	t.Run("plano já expirado é recusado, mesmo PASS", func(t *testing.T) {
		w.exec(`UPDATE platform_update_plans SET expires_at = now() - interval '1 minute' WHERE id = $1`, plan.ID)
		if _, err := svc.StartRun(w.ctx, op, plan.ID, "run-key-expired-0001"); !errors.Is(err, updatemgr.ErrInvalid) {
			t.Errorf("%v", err)
		}
	})

	t.Run("quando o ledger reporta SUCCEEDED, a versão instalada é atualizada a partir do manifesto do próprio run", func(t *testing.T) {
		// Achado Codex [P1]: sem isto, platform_installations.current_version ficava preso na
		// versão ANTERIOR para sempre (nada neste programa reporta de volta à API), e o
		// preflight seguinte aprovaria uma release que só é compatível com a versão antiga,
		// mesmo a instalação real já tendo mudado. Dirige o MESMO ledger local (nunca um
		// atalho) pela sequência legal completa (domain.ValidateTransition) até SUCCEEDED.
		ledger, err := agent.OpenLedger(ledgerPath)
		if err != nil {
			t.Fatal(err)
		}
		defer ledger.Close()
		h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		for _, st := range []domain.RunState{
			domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
			domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
			domain.StateObserving, domain.StateSucceeded,
		} {
			if err := ledger.TransitionRun(w.ctx, h, run.ID, st); err != nil {
				t.Fatalf("transição para %s: %v", st, err)
			}
		}
		if err := ledger.ReleaseLock(w.ctx, h); err != nil {
			t.Fatal(err)
		}

		got, err := svc.GetRun(w.ctx, op, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State != string(domain.StateSucceeded) {
			t.Fatalf("GetRun não sincronizou o SUCCEEDED do ledger: %q", got.State)
		}
		inst, err := svc.GetInstallation(w.ctx, op)
		if err != nil {
			t.Fatal(err)
		}
		if inst.CurrentVersion != "9.9.1" || inst.CurrentReleaseID != "omnira-9.9.5" {
			t.Errorf("a versão instalada não foi atualizada a partir do manifesto do run concluído: %+v", inst)
		}
	})
}

// Achado Codex [P1]: forma válida (domain.ParseManifest/Validate) nunca é autenticidade — sem
// um SignatureVerifier real injetado, o default de PRODUÇÃO (nunca sobrescrito por
// apps/api/cmd/omnira-api/main.go nesta fase) precisa recusar toda ingestão, honestamente, em
// vez de fingir que verificou algo.
func TestUpdateMgr_IngestRelease_DefaultVerifierBlocksEverything(t *testing.T) {
	w := newWorld(t)
	op := w.operator("op")
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc := updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute) // SEM WithVerifier — o default real
	m := manifestJSON(t, "omnira-9.9.9", []string{"1.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0012", m); !errors.Is(err, updatemgr.ErrSignatureUnverifiable) {
		t.Errorf("esperava ErrSignatureUnverifiable sem um verificador real configurado, veio %v", err)
	}
	if n := w.count(`SELECT count(*) FROM platform_releases WHERE release_id = $1`, "omnira-9.9.9"); n != 0 {
		t.Errorf("um manifesto não verificado foi persistido: %d", n)
	}
}

// Achado Codex [P1]: duas Idempotency-Keys diferentes podiam ser reservadas e despachadas uma
// após a outra para a MESMA instalação, cada uma compatível só no instante do próprio pedido —
// prova que a segunda é recusada enquanto a primeira ainda está ativa.
func TestUpdateMgr_StartRun_RejectsSecondConcurrentRunForTheSameInstallation(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")
	w.resetActiveRuns()
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc = updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})
	w.exec(`UPDATE platform_installations SET current_version = '4.0.0'`)

	m1 := manifestJSON(t, "omnira-9.10.1", []string{"4.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0013", m1); err != nil {
		t.Fatal(err)
	}
	plan1, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0008", "omnira-9.10.1")
	if err != nil || plan1.PreflightStatus != "PASS" {
		t.Fatalf("%v %+v", err, plan1)
	}
	run1, err := svc.StartRun(w.ctx, op, plan1.ID, "run-key-concurrent-a-0001")
	if err != nil {
		t.Fatalf("primeiro run: %v", err)
	}
	if run1.State != string(domain.StateLocked) {
		t.Fatalf("%+v", run1)
	}

	// "9.9.1" é o platform_version fixo que manifestJSON atribui a toda release (ver seu
	// comentário) — inclui aqui porque run1, ao suceder de verdade no ledger mais abaixo,
	// vai atualizar platform_installations.current_version para "9.9.1" (o platform_version
	// do PRÓPRIO m1), e este teste precisa que plan2 continue válido depois disso.
	m2 := manifestJSON(t, "omnira-9.10.2", []string{"4.0.0", "9.9.1"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0014", m2); err != nil {
		t.Fatal(err)
	}
	plan2, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0009", "omnira-9.10.2")
	if err != nil || plan2.PreflightStatus != "PASS" {
		t.Fatalf("%v %+v", err, plan2)
	}
	if _, err := svc.StartRun(w.ctx, op, plan2.ID, "run-key-concurrent-b-0001"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("um segundo run concorrente para a mesma instalação deveria ser recusado: %v", err)
	}
	if n := w.count(`SELECT count(*) FROM platform_update_runs WHERE release_id = $1`, "omnira-9.10.2"); n != 0 {
		t.Errorf("o segundo run foi criado mesmo recusado: %d", n)
	}

	// Depois que o primeiro run termina de verdade — NO LEDGER também, nunca só um atalho no
	// espelho Postgres (achado Codex: a checagem "só um run ativo" agora consulta o ledger
	// diretamente, a autoridade real de execução) — um segundo pedido volta a ser aceito.
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
		domain.StateObserving, domain.StateSucceeded,
	} {
		if err := ledger.TransitionRun(w.ctx, h, run1.ID, st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	w.must(ledger.ReleaseLock(w.ctx, h))
	w.must(ledger.Close())

	run2, err := svc.StartRun(w.ctx, op, plan2.ID, "run-key-concurrent-b-0002")
	if err != nil {
		t.Fatalf("depois do primeiro terminar: %v", err)
	}
	if run2.State != string(domain.StateLocked) {
		t.Fatalf("%+v", run2)
	}
}

// Achado Codex [P1]: um pedido reservado (stateRequested) cujo despacho ao ledger nunca se
// confirmou (simulando um crash do processo API bem nesse instante) não pode ser redespachado
// às cegas quando reexecutado — a instalação pode ter deixado de ser compatível nesse meio tempo.
func TestUpdateMgr_StartRun_VoidsAStalePendingRequestInsteadOfBlindlyRedispatching(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")
	w.resetActiveRuns()
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc = updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})
	w.exec(`UPDATE platform_installations SET current_version = '3.0.0'`)

	m := manifestJSON(t, "omnira-9.10.3", []string{"3.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0015", m); err != nil {
		t.Fatal(err)
	}
	plan, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0010", "omnira-9.10.3")
	if err != nil || plan.PreflightStatus != "PASS" {
		t.Fatalf("%v %+v", err, plan)
	}

	// Simula de verdade "o despacho ao ledger falhou" (achado Codex, ex. "o daemon detém o
	// lock"): um holder DIFERENTE segura o lock da MESMA instalação antes do primeiro
	// StartRun — writeLedgerRun.AcquireLock recusa com ErrLockHeld, nunca um atalho artificial
	// mexendo direto no Postgres.
	blocker, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	bh, err := blocker.AcquireLock(w.ctx, "test-install-1", "blocking-daemon", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	run, err := svc.StartRun(w.ctx, op, plan.ID, "run-key-stale-pending-0001")
	if err == nil {
		t.Fatalf("esperava falha de despacho (lock detido por outro holder), veio sucesso: %+v", run)
	}
	var runID, state string
	w.must(w.owner.QueryRow(w.ctx, `SELECT id, state FROM platform_update_runs WHERE idempotency_key = $1`, "run-key-stale-pending-0001").Scan(&runID, &state))
	if state != "REQUESTED" {
		t.Fatalf("achado Codex: a reserva precisa sobreviver a uma falha de despacho — esperava REQUESTED, veio %q", state)
	}

	// O "daemon" libera o lock (ex. terminou/crashou) e a instalação muda de versão enquanto
	// o pedido ficava pendente sem nunca ter chegado ao ledger.
	w.must(blocker.ReleaseLock(w.ctx, bh))
	w.must(blocker.Close())
	w.exec(`UPDATE platform_installations SET current_version = '99.0.0'`)

	if _, err := svc.StartRun(w.ctx, op, plan.ID, "run-key-stale-pending-0001"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("replay de um pedido pendente que ficou incompatível deveria ser recusado: %v", err)
	}
	w.must(w.owner.QueryRow(w.ctx, `SELECT state FROM platform_update_runs WHERE id = $1`, runID).Scan(&state))
	if state != "COMPATIBILITY_VOIDED" {
		t.Errorf("esperava COMPATIBILITY_VOIDED, veio %q", state)
	}
}

// Achado Codex [P1]: StartRun lia platform_installations ANTES de sincronizar um run pendente
// com o ledger — um run anterior podia já ter concluído (A -> B) no ledger sem que ninguém
// tivesse chamado GetRun/ListRuns/ListEvents para espelhar isso no Postgres, deixando um
// SEGUNDO plano (criado enquanto o Postgres ainda mostrava A, compatível só com A) passar pela
// checagem de compatibilidade com dados já obsoletos.
func TestUpdateMgr_StartRun_RefreshesInstallationFromLedgerBeforeCheckingCompatibility(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")
	w.resetActiveRuns()
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc = updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	w.exec(`UPDATE platform_installations SET current_version = '1.0.0'`)
	releaseX := manifestJSONv(t, "omnira-9.11.1", "2.0.0", []string{"1.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0016", releaseX); err != nil {
		t.Fatal(err)
	}
	planX, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0011", "omnira-9.11.1")
	if err != nil || planX.PreflightStatus != "PASS" {
		t.Fatalf("%v %+v", err, planX)
	}
	runX, err := svc.StartRun(w.ctx, op, planX.ID, "run-key-order-a-0001")
	if err != nil {
		t.Fatalf("despacho de X: %v", err)
	}

	// Um segundo plano, para uma release Y que só é compatível com 1.0.0 (nunca com 2.0.0) —
	// criado ENQUANTO X ainda está LOCKED (a instalação de fato ainda é 1.0.0 neste instante),
	// então o preflight corretamente captura PASS. Achado Codex [P2], já corrigido:
	// CreatePlan agora TAMBÉM sincroniza com o ledger antes de avaliar — mas aqui ainda não há
	// nada para sincronizar (X nem chegou a SUCCEEDED), então isto reflete o estado real.
	releaseY := manifestJSONv(t, "omnira-9.11.2", "3.0.0", []string{"1.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0017", releaseY); err != nil {
		t.Fatal(err)
	}
	planY, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0012", "omnira-9.11.2")
	if err != nil || planY.PreflightStatus != "PASS" {
		t.Fatalf("plano Y deveria passar com a instalação ainda em 1.0.0 (X só LOCKED): %v %+v", err, planY)
	}

	// SÓ AGORA X termina (SUCCEEDED) — dirigido diretamente no ledger, exatamente como o
	// Update Agent faria, SEM chamar nenhum método do serviço que sincronizaria o Postgres
	// (GetRun/ListRuns/CreatePlan). platform_installations.current_version continua '1.0.0'
	// no Postgres neste instante, mesmo a instalação real já estando em 2.0.0 — e o plano Y,
	// criado ANTES disso, ficou obsoleto sem que nada tenha avisado.
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
		domain.StateObserving, domain.StateSucceeded,
	} {
		if err := ledger.TransitionRun(w.ctx, h, runX.ID, st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	w.must(ledger.ReleaseLock(w.ctx, h))
	w.must(ledger.Close())
	if got := w.count(`SELECT count(*) FROM platform_update_runs WHERE id = $1 AND state = $2`, runX.ID, string(domain.StateLocked)); got != 1 {
		t.Fatalf("pré-condição: o espelho Postgres deveria AINDA mostrar LOCKED (não sincronizado), achei %d", got)
	}

	// StartRun precisa sincronizar X (SUCCEEDED, instalação agora 2.0.0) ANTES de revalidar o
	// plano Y (criado quando 1.0.0 ainda era PASS) contra a instalação — recusado.
	if _, err := svc.StartRun(w.ctx, op, planY.ID, "run-key-order-b-0001"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("Y deveria ser recusado contra a versão real (2.0.0), nunca contra o 1.0.0 obsoleto do Postgres: %v", err)
	}
	inst, err := svc.GetInstallation(w.ctx, op)
	if err != nil || inst.CurrentVersion != "2.0.0" {
		t.Fatalf("a instalação deveria ter sido sincronizada para 2.0.0 durante o StartRun de Y: %v %+v", err, inst)
	}
	if n := w.count(`SELECT count(*) FROM platform_update_runs WHERE release_id = $1`, "omnira-9.11.2"); n != 0 {
		t.Errorf("um run de Y foi criado mesmo devendo ser recusado: %d", n)
	}
}

// Achado Codex [P2]: diferente de StartRun/ListRuns, CreatePlan nunca sincronizava com o
// ledger antes de ler platform_installations — um plano criado bem depois de um run SUCCEEDED
// (sem nenhum GetRun/ListRuns/StartRun entre o fim do run e este CreatePlan) ainda lia a
// versão OBSOLETA, gravando um BLOCK permanente e desnecessário para uma release que já era
// compatível com a versão REAL.
func TestUpdateMgr_CreatePlan_RefreshesInstallationFromLedgerBeforeEvaluating(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")
	w.resetActiveRuns()
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc = updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})

	w.exec(`UPDATE platform_installations SET current_version = '4.0.0'`)
	releaseX := manifestJSONv(t, "omnira-9.12.1", "5.0.0", []string{"4.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0018", releaseX); err != nil {
		t.Fatal(err)
	}
	planX, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0013", "omnira-9.12.1")
	if err != nil || planX.PreflightStatus != "PASS" {
		t.Fatalf("%v %+v", err, planX)
	}
	runX, err := svc.StartRun(w.ctx, op, planX.ID, "run-key-plan-refresh-0001")
	if err != nil {
		t.Fatalf("despacho de X: %v", err)
	}

	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
		domain.StateObserving, domain.StateSucceeded,
	} {
		if err := ledger.TransitionRun(w.ctx, h, runX.ID, st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	w.must(ledger.ReleaseLock(w.ctx, h))
	w.must(ledger.Close())
	var staleVersion string
	w.must(w.owner.QueryRow(w.ctx, `SELECT current_version FROM platform_installations`).Scan(&staleVersion))
	if staleVersion != "4.0.0" {
		t.Fatalf("pré-condição: o Postgres deveria AINDA mostrar a versão antiga (ninguém sincronizou ainda), achei %q", staleVersion)
	}

	// releaseY só é compatível com 5.0.0 (a versão REAL pós-X) — nunca com 4.0.0 (a obsoleta).
	// Sem o refresh, CreatePlan gravaria BLOCK; com ele, PASS.
	releaseY := manifestJSONv(t, "omnira-9.12.2", "6.0.0", []string{"5.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0019", releaseY); err != nil {
		t.Fatal(err)
	}
	planY, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0014", "omnira-9.12.2")
	if err != nil {
		t.Fatal(err)
	}
	if planY.PreflightStatus != "PASS" {
		t.Fatalf("CreatePlan não sincronizou com o ledger antes de avaliar: %s %s", planY.PreflightStatus, planY.PreflightDetail)
	}
}

// Achado Codex [P1]: um run criado por `updatectl start` (processo separado, legítimo mesmo
// com a API fora do ar — ADR-0054 §2.1 "Recovery CLI") escreve DIRETO no ledger local, sem
// NENHUMA linha correspondente em platform_update_runs. A contagem "só um run ativo" baseada
// só no Postgres nunca via esse run — StartRun precisa consultar o ledger diretamente.
func TestUpdateMgr_StartRun_RejectsWhenTheLedgerHasARunCreatedOutsideTheAPI(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")
	w.resetActiveRuns()
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc = updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})
	w.exec(`UPDATE platform_installations SET current_version = '7.0.0'`)

	// Simula `updatectl start`: escreve direto no ledger, nunca através deste serviço — zero
	// linhas em platform_update_runs para este run.
	cliManifest := manifestJSON(t, "omnira-9.13.1", []string{"7.0.0"}, 1)
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "updatectl-cli", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.CreateRun(w.ctx, h, "run-from-cli", "cli-idem-key-0001", string(cliManifest), domain.StateLocked); err != nil {
		t.Fatal(err)
	}
	w.must(ledger.ReleaseLock(w.ctx, h))
	w.must(ledger.Close())
	if n := w.count(`SELECT count(*) FROM platform_update_runs WHERE id = $1`, "run-from-cli"); n != 0 {
		t.Fatalf("pré-condição: o run da CLI não deveria ter linha no Postgres, achei %d", n)
	}

	releaseAPI := manifestJSON(t, "omnira-9.13.2", []string{"7.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0020", releaseAPI); err != nil {
		t.Fatal(err)
	}
	planAPI, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0015", "omnira-9.13.2")
	if err != nil || planAPI.PreflightStatus != "PASS" {
		t.Fatalf("%v %+v", err, planAPI)
	}
	if _, err := svc.StartRun(w.ctx, op, planAPI.ID, "run-key-vs-cli-0001"); !errors.Is(err, updatemgr.ErrInvalid) {
		t.Errorf("StartRun deveria recusar: o ledger já tem um run ativo criado pela CLI: %v", err)
	}
	if n := w.count(`SELECT count(*) FROM platform_update_runs WHERE release_id = $1`, "omnira-9.13.2"); n != 0 {
		t.Errorf("um run via API foi criado mesmo com a CLI já ativa no ledger: %d", n)
	}
}

// Achado Codex [P1]: um run concluído por `updatectl start` (sem NENHUMA linha em
// platform_update_runs) precisa atualizar platform_installations.current_version do mesmo
// jeito que um run criado pela API — a sincronização (applyLatestSucceededRunToInstallation)
// escaneia o ledger INTEIRO desta instalação, nunca só os runs que o Postgres já conhece.
func TestUpdateMgr_RefreshPendingRun_SyncsInstallationFromACLIOnlyRunThatSucceeded(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")
	w.resetActiveRuns()
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc = updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})
	w.exec(`UPDATE platform_installations SET current_version = '8.0.0', last_synced_run_id = NULL`)

	cliManifest := manifestJSONv(t, "omnira-9.14.1", "9.0.0", []string{"8.0.0"}, 1)
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "updatectl-cli", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.CreateRun(w.ctx, h, "run-from-cli-success", "cli-idem-key-0002", string(cliManifest), domain.StateLocked); err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
		domain.StateObserving, domain.StateSucceeded,
	} {
		if err := ledger.TransitionRun(w.ctx, h, "run-from-cli-success", st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	w.must(ledger.ReleaseLock(w.ctx, h))
	w.must(ledger.Close())
	if n := w.count(`SELECT count(*) FROM platform_update_runs WHERE id = $1`, "run-from-cli-success"); n != 0 {
		t.Fatalf("pré-condição: o run da CLI não deveria ter linha no Postgres, achei %d", n)
	}

	// Um release só compatível com a versão REAL pós-CLI (9.0.0), nunca com a obsoleta (8.0.0).
	releaseAPI := manifestJSON(t, "omnira-9.14.2", []string{"9.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-0021", releaseAPI); err != nil {
		t.Fatal(err)
	}
	planAPI, _, err := svc.CreatePlan(w.ctx, op, "plan-key-0016", "omnira-9.14.2")
	if err != nil {
		t.Fatal(err)
	}
	if planAPI.PreflightStatus != "PASS" {
		t.Fatalf("CreatePlan não sincronizou a versão do run concluído pela CLI: %s %s", planAPI.PreflightStatus, planAPI.PreflightDetail)
	}
	inst, err := svc.GetInstallation(w.ctx, op)
	if err != nil || inst.CurrentVersion != "9.0.0" || inst.CurrentReleaseID != "omnira-9.14.1" {
		t.Fatalf("%v %+v", err, inst)
	}
}

// Achado Codex [P2]: GetInstallation nunca sincronizava com o ledger — um cliente que só
// chamasse este endpoint (nunca CreatePlan/ListRuns/StartRun) via a versão antiga para sempre,
// mesmo depois de um update real (inclusive via `updatectl`) ter concluído.
func TestUpdateMgr_GetInstallation_RefreshesFromLedgerBeforeReturning(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")
	w.resetActiveRuns()
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc = updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})
	w.exec(`UPDATE platform_installations SET current_version = '10.0.0', last_synced_run_id = NULL`)

	cliManifest := manifestJSONv(t, "omnira-9.15.1", "11.0.0", []string{"10.0.0"}, 1)
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "updatectl-cli", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.CreateRun(w.ctx, h, "run-from-cli-getinstall", "cli-idem-key-0003", string(cliManifest), domain.StateLocked); err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
		domain.StateObserving, domain.StateSucceeded,
	} {
		if err := ledger.TransitionRun(w.ctx, h, "run-from-cli-getinstall", st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	w.must(ledger.ReleaseLock(w.ctx, h))
	w.must(ledger.Close())

	// Chamada DIRETA a GetInstallation — nenhum CreatePlan/ListRuns/StartRun antes.
	inst, err := svc.GetInstallation(w.ctx, op)
	if err != nil {
		t.Fatal(err)
	}
	if inst.CurrentVersion != "11.0.0" || inst.CurrentReleaseID != "omnira-9.15.1" {
		t.Fatalf("GetInstallation não sincronizou com o ledger: %+v", inst)
	}
}

// Achado Codex [P2]: POST /platform/backups exige Idempotency-Key no contrato (ADR-0054
// §2.2.1, toda mutação), mas o handler nunca a lia — um retry sempre criava uma segunda linha.
func TestUpdateMgr_RecordBackupEvidence_Idempotency(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	takenAt := time.Now().Add(-time.Hour)
	rec1, replayed1, err := svc.RecordBackupEvidence(w.ctx, op, "backup-key-0001", "logical_dump", takenAt, true, nil, "", map[string]any{"path": "/backups/x.dump"})
	if err != nil || replayed1 {
		t.Fatalf("primeira chamada: %v replayed=%v", err, replayed1)
	}

	t.Run("retry com a MESMA chave devolve o MESMO registro, nunca cria outro", func(t *testing.T) {
		before := w.count(`SELECT count(*) FROM platform_backup_records`)
		rec2, replayed2, err := svc.RecordBackupEvidence(w.ctx, op, "backup-key-0001", "logical_dump", takenAt, true, nil, "", map[string]any{"path": "/backups/x.dump"})
		if err != nil || !replayed2 || rec2.ID != rec1.ID {
			t.Fatalf("%v replayed=%v %+v", err, replayed2, rec2)
		}
		if w.count(`SELECT count(*) FROM platform_backup_records`) != before {
			t.Error("um retry com a mesma chave criou uma segunda linha de evidência")
		}
	})

	t.Run("a MESMA chave com campos diferentes é recusada", func(t *testing.T) {
		if _, _, err := svc.RecordBackupEvidence(w.ctx, op, "backup-key-0001", "logical_dump", takenAt, false, nil, "", nil); !errors.Is(err, updatemgr.ErrKeyMismatch) {
			t.Errorf("%v", err)
		}
	})

	t.Run("chave curta é recusada", func(t *testing.T) {
		if _, _, err := svc.RecordBackupEvidence(w.ctx, op, "short", "logical_dump", takenAt, true, nil, "", nil); !errors.Is(err, updatemgr.ErrInvalid) {
			t.Errorf("%v", err)
		}
	})
}

// Achado Codex [P2]: POST /platform/updates/plans exige Idempotency-Key no contrato (ADR-0054
// §2.2.1, toda mutação), mas o handler nunca a persistia — um retry sempre criava um SEGUNDO
// plano (e um segundo evento de auditoria) em vez de recuperar o original.
func TestUpdateMgr_CreatePlan_Idempotency(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	m := manifestJSON(t, "omnira-9.16.1", []string{"1.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-plan-idem-0001", m); err != nil {
		t.Fatal(err)
	}
	plan1, replayed1, err := svc.CreatePlan(w.ctx, op, "plan-key-idem-0001", "omnira-9.16.1")
	if err != nil || replayed1 {
		t.Fatalf("primeira chamada: %v replayed=%v", err, replayed1)
	}

	t.Run("retry com a MESMA chave devolve o MESMO plano, nunca cria outro", func(t *testing.T) {
		before := w.count(`SELECT count(*) FROM platform_update_plans`)
		plan2, replayed2, err := svc.CreatePlan(w.ctx, op, "plan-key-idem-0001", "omnira-9.16.1")
		if err != nil || !replayed2 || plan2.ID != plan1.ID {
			t.Fatalf("%v replayed=%v %+v", err, replayed2, plan2)
		}
		if w.count(`SELECT count(*) FROM platform_update_plans`) != before {
			t.Error("um retry com a mesma chave criou um segundo plano")
		}
	})

	t.Run("a MESMA chave para um release_id DIFERENTE é recusada", func(t *testing.T) {
		m2 := manifestJSON(t, "omnira-9.16.2", []string{"1.0.0"}, 1)
		if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-plan-idem-0002", m2); err != nil {
			t.Fatal(err)
		}
		if _, _, err := svc.CreatePlan(w.ctx, op, "plan-key-idem-0001", "omnira-9.16.2"); !errors.Is(err, updatemgr.ErrKeyMismatch) {
			t.Errorf("%v", err)
		}
	})

	t.Run("chave curta é recusada", func(t *testing.T) {
		if _, _, err := svc.CreatePlan(w.ctx, op, "short", "omnira-9.16.1"); !errors.Is(err, updatemgr.ErrInvalid) {
			t.Errorf("%v", err)
		}
	})
}

// Achado Codex [P2]: sem idempotência, um retry (resposta perdida) de SetInstallationVersion
// reescrevia a MESMA versão antiga e avançava updated_at de novo — se isso acontecesse DEPOIS
// de um run real ter sucedido nesse meio tempo, a sincronização trataria o retry obsoleto como
// "mais recente" que o sucesso genuíno (achado da rodada anterior), revertendo silenciosamente
// a versão instalada corretamente sincronizada de volta para a antiga.
func TestUpdateMgr_SetInstallationVersion_Idempotency(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")

	inst1, err := svc.SetInstallationVersion(w.ctx, op, "setver-idem-0001", "30.0.0", "omnira-9.18.1")
	if err != nil || inst1.CurrentVersion != "30.0.0" {
		t.Fatalf("%v %+v", err, inst1)
	}
	firstUpdatedAt := inst1.UpdatedAt

	t.Run("retry com a MESMA chave não escreve de novo (updated_at não avança)", func(t *testing.T) {
		inst2, err := svc.SetInstallationVersion(w.ctx, op, "setver-idem-0001", "30.0.0", "omnira-9.18.1")
		if err != nil || inst2.CurrentVersion != "30.0.0" {
			t.Fatalf("%v %+v", err, inst2)
		}
		if !inst2.UpdatedAt.Equal(firstUpdatedAt) {
			t.Errorf("um retry com a mesma chave avançou updated_at: %v -> %v", firstUpdatedAt, inst2.UpdatedAt)
		}
	})

	t.Run("a MESMA chave com campos diferentes é recusada", func(t *testing.T) {
		if _, err := svc.SetInstallationVersion(w.ctx, op, "setver-idem-0001", "31.0.0", ""); !errors.Is(err, updatemgr.ErrKeyMismatch) {
			t.Errorf("%v", err)
		}
	})

	t.Run("chave curta é recusada", func(t *testing.T) {
		if _, err := svc.SetInstallationVersion(w.ctx, op, "short", "30.0.0", ""); !errors.Is(err, updatemgr.ErrInvalid) {
			t.Errorf("%v", err)
		}
	})
}

// Achado Codex [P2]: depois de um run SUCCEEDED (A -> B) seguido de uma recuperação manual
// APROVADA que restaura para A (ADR-0054 §8 R3/R4, via SetInstallationVersion — nada neste
// programa reporta isso automaticamente), o despacho de uma release só compatível com A não
// podia mais acontecer: a rechecagem sob o lock (writeLedgerRun) confiava CIEGAMENTE no
// histórico do ledger (B), que nunca seria corrigido porque o próprio despacho estava
// bloqueado. A versão manualmente registrada, por ser MAIS RECENTE que o último run do
// ledger, precisa vencer.
func TestUpdateMgr_StartRun_ManualVersionOverrideAfterRecoveryWinsOverStaleLedgerHistory(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")
	w.resetActiveRuns()
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc = updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})
	w.exec(`UPDATE platform_installations SET current_version = '20.0.0', last_synced_run_id = NULL`)

	// Run X: 20.0.0 -> 21.0.0, concluído de verdade no ledger.
	releaseX := manifestJSONv(t, "omnira-9.17.1", "21.0.0", []string{"20.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-recovery-0001", releaseX); err != nil {
		t.Fatal(err)
	}
	planX, _, err := svc.CreatePlan(w.ctx, op, "plan-key-recovery-0001", "omnira-9.17.1")
	if err != nil || planX.PreflightStatus != "PASS" {
		t.Fatalf("%v %+v", err, planX)
	}
	runX, err := svc.StartRun(w.ctx, op, planX.ID, "run-key-recovery-0001")
	if err != nil {
		t.Fatalf("despacho de X: %v", err)
	}
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.RunState{
		domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying,
		domain.StatePreparing, domain.StateMigratingExpand, domain.StateDeploying, domain.StateHealthChecking,
		domain.StateObserving, domain.StateSucceeded,
	} {
		if err := ledger.TransitionRun(w.ctx, h, runX.ID, st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	w.must(ledger.ReleaseLock(w.ctx, h))
	w.must(ledger.Close())

	// Sincroniza X (SUCCEEDED, 21.0.0) no Postgres — sem isto o teste não provaria nada sobre
	// "o ledger diz 21.0.0" vs "o operador restaurou manualmente para 20.0.0" depois.
	if _, err := svc.GetInstallation(w.ctx, op); err != nil {
		t.Fatal(err)
	}
	if inst, err := svc.GetInstallation(w.ctx, op); err != nil || inst.CurrentVersion != "21.0.0" {
		t.Fatalf("pré-condição: X deveria ter sido sincronizado: %v %+v", err, inst)
	}

	// Incidente simulado + recuperação APROVADA: um operador restaura manualmente para 20.0.0
	// (R3/R4) — isto acontece DEPOIS do último run conhecido do ledger.
	if _, err := svc.SetInstallationVersion(w.ctx, op, "setver-key-0003", "20.0.0", ""); err != nil {
		t.Fatal(err)
	}

	// Uma release só compatível com 20.0.0 (nunca com 21.0.0, que o ledger "lembra" como o
	// último sucesso) precisa poder ser despachada — a recuperação manual, mais recente,
	// precisa vencer o histórico mais antigo do ledger.
	releaseY := manifestJSON(t, "omnira-9.17.2", []string{"20.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-recovery-0002", releaseY); err != nil {
		t.Fatal(err)
	}
	planY, _, err := svc.CreatePlan(w.ctx, op, "plan-key-recovery-0002", "omnira-9.17.2")
	if err != nil || planY.PreflightStatus != "PASS" {
		t.Fatalf("%v %+v", err, planY)
	}
	runY, err := svc.StartRun(w.ctx, op, planY.ID, "run-key-recovery-0002")
	if err != nil {
		t.Fatalf("StartRun de Y deveria aceitar a versão manualmente restaurada (20.0.0), nunca ficar presa no histórico do ledger (21.0.0): %v", err)
	}
	if runY.State != string(domain.StateLocked) {
		t.Fatalf("%+v", runY)
	}
}

// Achado Codex [P2]: Executor.Reconcile (o agente real) se recusa a processar QUALQUER run de
// uma instalação enquanto ela tiver um RECOVERY_REQUIRED não reconhecido
// (Ledger.HasUnresolvedRecoveryRequired). Sem a MESMA checagem aqui, CreatePlan/StartRun
// aceitavam um pedido que o agente nunca executaria — ficaria preso em LOCKED para sempre,
// silenciosamente, já que RECOVERY_REQUIRED é tratado como terminal só para a contagem de
// "run ativo", nunca significando "instalação livre para novos runs".
func TestUpdateMgr_CreatePlanAndStartRun_BlockWhileRecoveryIsUnresolved(t *testing.T) {
	w := newWorld(t)
	svc := newService(t, w)
	op := w.operator("op")
	w.resetActiveRuns()
	ledgerPath := filepath.Join(t.TempDir(), "ledger.db")
	svc = updatemgr.New(w.app, ledgerPath, "test-install-1", "test-api", time.Minute).WithVerifier(testAllowAllVerifier{})
	w.exec(`UPDATE platform_installations SET current_version = '40.0.0'`)

	releaseX := manifestJSON(t, "omnira-9.19.1", []string{"40.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-recovery2-0001", releaseX); err != nil {
		t.Fatal(err)
	}
	planX, _, err := svc.CreatePlan(w.ctx, op, "plan-key-recovery2-0001", "omnira-9.19.1")
	if err != nil || planX.PreflightStatus != "PASS" {
		t.Fatalf("%v %+v", err, planX)
	}
	runX, err := svc.StartRun(w.ctx, op, planX.ID, "run-key-recovery2-0001")
	if err != nil {
		t.Fatalf("despacho de X: %v", err)
	}

	// Dirige X até RECOVERY_REQUIRED (FAILED -> RECOVERING -> RECOVERY_REQUIRED), direto no
	// ledger, SEM reconhecer (sem AcknowledgeRecovery) — exatamente o estado real que faria
	// Executor.Reconcile recusar qualquer run novo desta instalação.
	ledger, err := agent.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	h, err := ledger.AcquireLock(w.ctx, "test-install-1", "test-direct-driver", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []domain.RunState{domain.StateFailed, domain.StateRecovering, domain.StateRecoveryRequired} {
		if err := ledger.TransitionRun(w.ctx, h, runX.ID, st); err != nil {
			t.Fatalf("transição para %s: %v", st, err)
		}
	}
	w.must(ledger.ReleaseLock(w.ctx, h))
	w.must(ledger.Close())

	releaseY := manifestJSON(t, "omnira-9.19.2", []string{"40.0.0"}, 1)
	if _, _, err := svc.IngestRelease(w.ctx, op, "ingest-key-recovery2-0002", releaseY); err != nil {
		t.Fatal(err)
	}

	t.Run("CreatePlan bloqueia com a recuperação pendente, mesmo com from_versions compatível", func(t *testing.T) {
		planY, _, err := svc.CreatePlan(w.ctx, op, "plan-key-recovery2-0002", "omnira-9.19.2")
		if err != nil {
			t.Fatal(err)
		}
		if planY.PreflightStatus != "BLOCK" {
			t.Fatalf("esperava BLOCK por recuperação pendente, veio %s: %s", planY.PreflightStatus, planY.PreflightDetail)
		}
		if !bytes.Contains(planY.PreflightDetail, []byte("recuperação pendente")) {
			t.Errorf("preflight_detail deveria citar a recuperação pendente: %s", planY.PreflightDetail)
		}
	})
}

func TestUpdateMgr_TheServiceRefusesOnItsOwn(t *testing.T) {
	// Defesa em profundidade (mesmo espírito de internal/hub/companies): mesmo que o HTTP algum
	// dia pare de checar, o Service roda numa sessão de SISTEMA e precisa recusar por conta
	// própria — nada mais o impediria.
	w := newWorld(t)
	svc := newService(t, w)
	outsider := w.user("outsider")
	if _, _, err := svc.IngestRelease(w.ctx, outsider, "ingest-key-0022", manifestJSON(t, "omnira-9.9.6", []string{"1.0.0"}, 1)); !errors.Is(err, updatemgr.ErrForbidden) {
		t.Errorf("%v", err)
	}
	if n := w.count(`SELECT count(*) FROM platform_releases WHERE release_id = $1`, "omnira-9.9.6"); n != 0 {
		t.Errorf("um outsider conseguiu persistir uma release: %d", n)
	}
}
