package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/omnira/omnira/internal/platformrelease/domain"
)

var errUnavailable = errors.New("agent: backup indisponível (simulado no teste)")

// rollbackSucceedsRunner simula realisticamente "a versão nova nunca fica saudável, mas a
// versão anterior (reaplicada pelo Rollback) sim": a saúde reportada depende de quantas vezes
// `docker compose up` (Apply) já foi chamado — a 1ª chamada é o deploy original (DEPLOYING),
// a 2ª é o Rollback dentro de recover(). Necessário porque o fix para o achado Codex "Rollback
// não verifica saúde" faz o PRÓPRIO rollback chamar WaitHealthy — um fakeRunner estático que
// sempre reporta "starting" faria o rollback nunca ter sucesso, mascarando o cenário que estes
// testes querem provar (rollback de verdade bem-sucedido).
type rollbackSucceedsRunner struct {
	*fakeRunner
	applyCount int
}

func (r *rollbackSucceedsRunner) run(ctx context.Context, name string, args ...string) (string, string, error) {
	if name == "docker" && len(args) > 0 && args[0] == "compose" {
		r.applyCount++
	}
	if name == "docker" && len(args) > 0 && args[0] == "inspect" && strings.Contains(strings.Join(args, " "), "{{.State.Running}}") {
		if r.applyCount >= 2 {
			return "true|healthy", "", nil
		}
		return "true|starting", "", nil
	}
	return r.fakeRunner.run(ctx, name, args...)
}

// fakeMigrateRunner conta quantas vezes Up() foi de fato chamado — a prova de idempotência:
// depois de um run concluir MIGRATING_EXPAND, uma retomada (crash simulado) NUNCA deve chamar
// Up() de novo.
type fakeMigrateRunner struct {
	upCalls  int
	upErr    error
	upTarget string // último target recebido por Up — prova de que o chamador propaga manifest.Migrations.Target
	upBundle string // último checksum_bundle recebido por Up
}

func (f *fakeMigrateRunner) Up(_ context.Context, target, checksumBundle string) error {
	f.upCalls++
	f.upTarget = target
	f.upBundle = checksumBundle
	return f.upErr
}
func (f *fakeMigrateRunner) Status(context.Context) (string, error) {
	return "000001_initial up to date", nil
}

type fakeBackupRunner struct {
	runCalls int
	err      error
}

func (f *fakeBackupRunner) Run(context.Context) (string, error) {
	f.runCalls++
	if f.err != nil {
		return "", f.err
	}
	return "/backups/fake.dump", nil
}

// fakeBackupVerifier: sucesso por padrão; err configurável para testar o caminho de evidência
// insuficiente sem depender de um .meta.json real no disco.
type fakeBackupVerifier struct {
	err error
}

func (f *fakeBackupVerifier) Verify(context.Context, string) error { return f.err }

func testManifest(strategy string) *domain.ReleaseManifest {
	return &domain.ReleaseManifest{
		SchemaVersion:   1,
		ReleaseID:       "rel-test",
		PlatformVersion: "1.0.0",
		Channel:         domain.ChannelCandidate,
		Source:          domain.Source{Repository: "omnira/omnira", CommitSHA: "deadbeef", WorkflowRunID: "1"},
		Artifacts: []domain.Artifact{
			{Unit: domain.UnitAPI, Image: "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("a", 64)},
			{Unit: domain.UnitWorker, Image: "ghcr.io/omnira/omnira-worker@sha256:" + strings.Repeat("b", 64)},
			{Unit: domain.UnitWeb, Image: "ghcr.io/omnira/omnira-web@sha256:" + strings.Repeat("c", 64)},
		},
		Compatibility: domain.Compatibility{FromVersions: []string{"0.9.0"}, MinAgentProtocol: 1, DatabaseStrategy: strategy},
		Migrations:    domain.Migrations{StartAfter: "000172", Target: "000172", ChecksumBundle: "sha256:" + strings.Repeat("d", 64)},
		Rollout:       domain.Rollout{Workloads: []domain.BuildUnit{domain.UnitAPI, domain.UnitWorker, domain.UnitWeb}, PreflightPolicy: "strict", RecoveryClass: domain.RecoveryR1},
		ApprovedBy:    "test-operator",
	}
}

// fakeRunnerForManifest monta um fakeRunner cujos containers e digests reportados batem
// EXATAMENTE com os artifacts de `manifest` — necessário para qualquer teste que precise
// completar com SUCCEEDED depois do achado Codex P1 "revalidateHealthBeforeSuccess também
// confere o digest real, não só saúde/running": sem diferenciar o container ID e a imagem
// reportada POR UNIT, CurrentSnapshot nunca bateria com os digests esperados (um fakeRunner
// genérico, com uma única resposta fixa de {{.Config.Image}} para todas as units, faria a
// revalidação final falhar sempre). Diferencia por fragmento do comando completo: o filtro
// de serviço (`label=com.docker.compose.service=<unit>`) escolhe o ID sintético de cada unit;
// o próprio ID (presente no comando de inspect) escolhe o digest reportado para esse ID.
func fakeRunnerForManifest(manifest *domain.ReleaseManifest) *fakeRunner {
	byFormat := map[string]string{"{{.State.Running}}": "true|healthy"}
	for _, a := range manifest.Artifacts {
		id := "container-" + string(a.Unit)
		byFormat[fmt.Sprintf("label=com.docker.compose.service=%s", unitFromBuildUnit(a.Unit))] = id
		byFormat[fmt.Sprintf("{{.Config.Image}} %s", id)] = a.Image
	}
	return &fakeRunner{stdoutByFormat: byFormat}
}

func testExecutor(t *testing.T, migrate MigrateRunner, backup BackupRunner, compose *ComposeAdapter) (*Executor, *Ledger, *LockHandle) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	ledger, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })
	// F9, achado Codex [P1] rodada 10 (fatia de retenção/GC): desde essa rodada, "nenhuma
	// sincronização de revogação ainda" é fail-closed por padrão (nunca mais "fresco por
	// decreto") — só uma instalação EXPLICITAMENTE marcada standalone, ou que já sincronizou
	// ao menos uma vez, é tratada como confiável. A GRANDE maioria dos testes deste arquivo
	// não é sobre revogação/frescor — são sobre outros comportamentos do agente — então
	// começam com uma sincronização "agora" por padrão (nunca standalone: isso quebraria
	// TestExecute_StaleRevocationSync_RefusesToApplyNewSteps, que precisa que o relógio de
	// IDADE ainda importe depois de um backdate). Testes que precisam EXATAMENTE do estado
	// "nunca sincronizado"/"standalone" constroem o ledger deles mesmos, sem usar este helper.
	if err := ledger.MarkRevocationSynced(context.Background()); err != nil {
		t.Fatalf("MarkRevocationSynced: %v", err)
	}
	h, err := ledger.AcquireLock(context.Background(), "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	exec := &Executor{
		Ledger:        ledger,
		Compose:       compose,
		Migrate:       migrate,
		Backup:        backup,
		BackupVerify:  &fakeBackupVerifier{},
		HealthTimeout: time.Second,
		ObserveWindow: 10 * time.Millisecond,
	}
	return exec, ledger, h
}

func TestExecute_HappyPath_RunsAllStepsInOrder(t *testing.T) {
	ctx := context.Background()
	manifest := testManifest("expand_only")
	// fakeRunnerForManifest: desde o achado Codex P1 "revalidar o digest real antes de
	// SUCCEEDED", um fakeRunner que reporta a MESMA imagem genérica para todas as units
	// nunca bateria com os digests reais do manifesto na revalidação final.
	fr := fakeRunnerForManifest(manifest)
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	migrate := &fakeMigrateRunner{}
	backup := &fakeBackupRunner{}
	exec, ledger, h := testExecutor(t, migrate, backup, compose)

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
	if got.State != domain.StateSucceeded {
		t.Fatalf("esperava SUCCEEDED, veio %s", got.State)
	}
	if migrate.upCalls != 1 {
		t.Fatalf("esperava 1 chamada de migrate up, veio %d", migrate.upCalls)
	}
	if migrate.upTarget != manifest.Migrations.Target {
		t.Fatalf("Up() deveria receber manifest.Migrations.Target=%q, veio %q", manifest.Migrations.Target, migrate.upTarget)
	}
	if backup.runCalls != 1 {
		t.Fatalf("esperava 1 chamada de backup, veio %d", backup.runCalls)
	}

	cps, err := ledger.Checkpoints(ctx, run.RunID)
	if err != nil {
		t.Fatalf("Checkpoints: %v", err)
	}
	wantSteps := []Step{StepBackup, StepDownload, StepSnapshot, StepMigrate, StepDeploy, StepHealth, StepObserve}
	completedSteps := map[Step]int{}
	for _, c := range cps {
		if c.Status == "completed" {
			completedSteps[Step(c.Step)]++
		}
	}
	for _, s := range wantSteps {
		if completedSteps[s] != 1 {
			t.Errorf("step %s: esperava exatamente 1 checkpoint completed, veio %d", s, completedSteps[s])
		}
	}
}

// TestExecute_ResumeAfterCrash_NeverRepeatsCompletedSteps: simula um crash DEPOIS que
// MIGRATING_EXPAND terminou mas antes de DEPLOYING concluir — Execute é chamado de novo
// (como o daemon faria ao reiniciar) e NUNCA deve rodar `migrate up` outra vez.
func TestExecute_ResumeAfterCrash_NeverRepeatsCompletedSteps(t *testing.T) {
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

	// Primeira "vida" do processo: avança manualmente até logo depois de MIGRATING_EXPAND
	// concluir, parando ali (simulando um crash exatamente nesse ponto) — chamando a mesma
	// máquina passo a passo que Execute usaria internamente, mas interrompendo no meio.
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
	if migrate.upCalls != 1 {
		t.Fatalf("esperava 1 chamada de migrate up na primeira vida, veio %d", migrate.upCalls)
	}

	// "Crash": nova chamada de Execute, como se o processo tivesse reiniciado. Deve retomar
	// de MIGRATING_EXPAND (já completed, confirma e avança) sem rodar migrate up outra vez.
	if err := exec.Execute(ctx, h, run.RunID, manifest); err != nil {
		t.Fatalf("Execute (retomada): %v", err)
	}
	if migrate.upCalls != 1 {
		t.Fatalf("retomada após crash NÃO deveria repetir migrate up: upCalls=%d", migrate.upCalls)
	}
	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateSucceeded {
		t.Fatalf("esperava SUCCEEDED após retomada, veio %s", got.State)
	}
}

// TestExecute_ResumeAtObserving_RevalidatesHealthBeforeSucceeding: achado Codex P2 — a ação de
// OBSERVING é pulável por idempotência (seu checkpoint, gravado com step="" por
// stepForState(StateSucceeded), já "completed") numa retomada. Se o agente crashou DEPOIS de
// marcar esse checkpoint completed mas ANTES de persistir SUCCEEDED, os containers podem ter
// degradado nesse meio tempo — retomar nunca deve declarar sucesso sem reconfirmar saúde de
// verdade. Prova: simula exatamente esse crash (checkpoint completed, run ainda em OBSERVING),
// degrada a saúde reportada DEPOIS disso, e confirma que Execute nunca chega a SUCCEEDED.
func TestExecute_ResumeAtObserving_RevalidatesHealthBeforeSucceeding(t *testing.T) {
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
	// Avança manualmente (saúde BOA o tempo todo) até logo depois de OBSERVING concluir seu
	// checkpoint — mas sem persistir a transição final para SUCCEEDED, simulando o crash
	// exatamente nesse instante.
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
	// Agora em HEALTH_CHECKING. Avança para OBSERVING e executa a ação de observação (que
	// espera ObserveWindow e reconfirma saúde) ENQUANTO ainda está saudável — grava seu
	// checkpoint como completed.
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
		t.Fatalf("runStep (ação de Observing, saúde boa): %v", err)
	}
	// CRASH simulado aqui: o checkpoint da ação de Observing (step="") já está "completed",
	// mas a transição para SUCCEEDED NUNCA foi persistida.

	// Degrada a saúde reportada DEPOIS do checkpoint ter sido marcado completed — simula algo
	// quebrando durante o tempo em que o agente ficou fora.
	fr.stdoutByFormat["{{.State.Running}}"] = "true|starting"

	// "Nova vida" do processo: sem a revalidação incondicional, isto pularia a ação (já
	// completed) e declararia SUCCEEDED às cegas.
	if err := exec.Execute(ctx, h, run.RunID, manifest); err != nil {
		t.Fatalf("Execute não deveria retornar erro quando a recuperação em si é bem-sucedida: %v", err)
	}
	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State == domain.StateSucceeded {
		t.Fatal("NUNCA deveria declarar SUCCEEDED sem reconfirmar saúde na retomada — degradação pós-checkpoint não foi detectada")
	}
}

// stateBefore devolve o estado imediatamente anterior a `s` na sequência pós-trava — usado só
// neste teste para dirigir actionFor passo a passo.
func stateBefore(s domain.RunState) domain.RunState {
	for i, st := range postLockSequence {
		if st == s && i > 0 {
			return postLockSequence[i-1]
		}
	}
	return domain.StateLocked
}

func TestExecute_FailureWithCompatibleStrategy_RollsBackCode(t *testing.T) {
	ctx := context.Background()
	// "docker inspect" sempre reporta "starting" (nunca "healthy") no formato de saúde,
	// forçando WaitHealthy a esgotar o timeout e falhar — mas no formato de IMAGEM (usado por
	// CurrentSnapshot durante PREPARING) reporta um digest real já em uso, simulando uma
	// instalação que já tinha uma versão anterior rodando, com algo utilizável para rollback.
	previousDigest := "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("9", 64)
	fr := &rollbackSucceedsRunner{fakeRunner: &fakeRunner{
		stdout:         map[string]string{"docker ps": "container-123"},
		stdoutByFormat: map[string]string{"{{.Config.Image}}": previousDigest},
	}}
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	migrate := &fakeMigrateRunner{}
	backup := &fakeBackupRunner{}
	exec, ledger, h := testExecutor(t, migrate, backup, compose)
	exec.HealthTimeout = 50 * time.Millisecond // curto, para o teste não demorar

	manifest := testManifest("expand_only")
	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Execute() retorna nil aqui: a falha do health check FOI tratada corretamente (decidiu e
	// gravou ROLLED_BACK) — isso é um sucesso operacional do mecanismo de reconciliação, não
	// um erro de infraestrutura. A asserção real é o ESTADO FINAL gravado no ledger, abaixo.
	if err := exec.Execute(ctx, h, run.RunID, manifest); err != nil {
		t.Fatalf("Execute não deveria retornar erro quando a recuperação em si é bem-sucedida: %v", err)
	}
	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateRolledBack {
		t.Fatalf("database_strategy=expand_only deveria permitir ROLLED_BACK, veio %s", got.State)
	}
}

func TestExecute_FailureWithIncompatibleStrategy_RecoveryRequired(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{stdout: map[string]string{"docker ps": "container-123", "docker inspect": "true|starting"}}
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	migrate := &fakeMigrateRunner{}
	backup := &fakeBackupRunner{}
	exec, ledger, h := testExecutor(t, migrate, backup, compose)
	exec.HealthTimeout = 50 * time.Millisecond

	// Nenhuma estratégia declarada (string vazia) nunca é tratada como segura por omissão.
	manifest := testManifest("")
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
	if got.State != domain.StateRecoveryRequired {
		t.Fatalf("database_strategy desconhecido nunca deveria permitir rollback automático, veio %s (esperava RECOVERY_REQUIRED)", got.State)
	}
}

func TestExecute_BackupFails_NeverReachesMigrate(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{}
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	migrate := &fakeMigrateRunner{}
	backup := &fakeBackupRunner{err: errUnavailable}
	exec, ledger, h := testExecutor(t, migrate, backup, compose)

	manifest := testManifest("expand_only")
	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := exec.Execute(ctx, h, run.RunID, manifest); err != nil {
		t.Fatalf("Execute não deveria retornar erro quando a recuperação em si é bem-sucedida: %v", err)
	}
	if migrate.upCalls != 0 {
		t.Fatalf("migrate up NUNCA deveria ter sido chamado sem um backup verificado antes, upCalls=%d", migrate.upCalls)
	}
	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	// Falha ANTES de qualquer deploy real — nenhum snapshot foi capturado ainda, então nenhuma
	// migração/deploy pode ter ocorrido (a sequência de estados é estritamente linear): isto é
	// R0 (especificação §8 "antes de qualquer mutação: abortar, manter versão"), não
	// RECOVERY_REQUIRED — não há nada para desfazer, e bloquear futuras tentativas desta
	// instalação por uma falha de backup já resolvida (ex. registry temporariamente fora do
	// ar) seria desproporcional.
	if got.State != domain.StateRolledBack {
		t.Fatalf("esperava ROLLED_BACK (R0: falha antes de qualquer mutação), veio %s", got.State)
	}
}

// TestExecute_ResumeFromRecovering_CompletesDecision: achado Codex — se o agente morre DEPOIS
// de persistir RECOVERING mas ANTES de decidir ROLLED_BACK/RECOVERY_REQUIRED, uma chamada de
// Execute numa "nova vida" do processo precisa COMPLETAR essa decisão, nunca deixar o run
// preso para sempre em RECOVERING.
func TestExecute_ResumeFromRecovering_CompletesDecision(t *testing.T) {
	ctx := context.Background()
	previousDigest := "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("9", 64)
	// Este teste nunca chama Apply antes de chegar em RECOVERING (salta direto para
	// FAILED/RECOVERING, simulando um crash), então o Rollback dentro de recover() é a
	// ÚNICA chamada de "docker compose" — precisa reportar saudável imediatamente, sem a
	// lógica sequencial de rollbackSucceedsRunner (que é para quando HÁ um deploy real
	// anterior que falhou).
	fr := &fakeRunner{
		stdout: map[string]string{"docker ps": "fake-container-id"},
		stdoutByFormat: map[string]string{
			"{{.Config.Image}}":  previousDigest,
			"{{.State.Running}}": "true|healthy",
		},
	}
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)

	manifest := testManifest("expand_only")
	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	// Chega em PREPARING "manualmente" para que o snapshot exista no journal — depois disso,
	// SIMULA o crash saltando direto para RECOVERING sem passar por handleFailure (como se um
	// processo anterior já tivesse feito essa primeira parte e morrido logo depois).
	for _, target := range []domain.RunState{domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying, domain.StatePreparing} {
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
	if err := ledger.TransitionRun(ctx, h, run.RunID, domain.StateFailed); err != nil {
		t.Fatalf("TransitionRun FAILED: %v", err)
	}
	if err := ledger.TransitionRun(ctx, h, run.RunID, domain.StateRecovering); err != nil {
		t.Fatalf("TransitionRun RECOVERING: %v", err)
	}

	// "Nova vida" do processo: Execute precisa detectar RECOVERING e completar a decisão.
	if err := exec.Execute(ctx, h, run.RunID, manifest); err != nil {
		t.Fatalf("Execute (retomada de RECOVERING): %v", err)
	}
	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateRolledBack {
		t.Fatalf("esperava ROLLED_BACK (snapshot disponível, strategy compatível), veio %s", got.State)
	}
}

// TestExecute_ResumeFromRecovering_StaleFencing_RefusesBeforeTouchingDocker: achado Codex P1
// — quando o run já está em RECOVERING (retomada: crash do agente, ou uma 2ª chamada de
// Reconcile), `recover()` pulava o bloco de transição FAILED->RECOVERING (que valida fencing
// via TransitionRun) e ia direto para a decisão, só descobrindo um handle obsoleto quando um
// checkpoint/transição era gravado DEPOIS de Compose.Rollback já ter rodado — tarde demais.
// Prova: um handle `h0` que perdeu o lock (outro holder assumiu) chamando Execute/recover
// numa retomada precisa falhar com ErrFencingLost SEM NUNCA chamar "docker compose" nenhuma
// vez.
func TestExecute_ResumeFromRecovering_StaleFencing_RefusesBeforeTouchingDocker(t *testing.T) {
	ctx := context.Background()
	previousDigest := "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("9", 64)
	fr := &fakeRunner{
		stdout: map[string]string{"docker ps": "fake-container-id"},
		stdoutByFormat: map[string]string{
			"{{.Config.Image}}":  previousDigest,
			"{{.State.Running}}": "true|healthy",
		},
	}
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h0 := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)

	manifest := testManifest("expand_only")
	run, err := ledger.CreateRun(ctx, h0, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, target := range []domain.RunState{domain.StateBackingUp, domain.StateBackupVerified, domain.StateDownloading, domain.StateVerifying, domain.StatePreparing} {
		next, action := exec.actionFor(stateBefore(target), run.RunID, manifest)
		if action != nil {
			if err := exec.runStep(ctx, h0, run.RunID, stepForState(next), action); err != nil {
				t.Fatalf("runStep %s: %v", target, err)
			}
		}
		if err := ledger.TransitionRun(ctx, h0, run.RunID, target); err != nil {
			t.Fatalf("TransitionRun %s: %v", target, err)
		}
	}
	if err := ledger.TransitionRun(ctx, h0, run.RunID, domain.StateFailed); err != nil {
		t.Fatalf("TransitionRun FAILED: %v", err)
	}
	if err := ledger.TransitionRun(ctx, h0, run.RunID, domain.StateRecovering); err != nil {
		t.Fatalf("TransitionRun RECOVERING: %v", err)
	}

	// h0 é liberado (simula o processo original encerrando/crashando) e outro holder assume
	// a instalação — h0 agora é um handle OBSOLETO: released=1 E um fencing_token novo foi
	// emitido, nenhum dos dois bate mais com o que h0 carrega.
	if err := ledger.ReleaseLock(ctx, h0); err != nil {
		t.Fatalf("ReleaseLock h0: %v", err)
	}
	if _, err := ledger.AcquireLock(ctx, "inst-1", "holder-b", time.Minute); err != nil {
		t.Fatalf("AcquireLock (holder-b assume a instalação): %v", err)
	}
	// Zera as chamadas já feitas durante o setup (resolução de container no PREPARING
	// anterior) — só interessa o que acontece DEPOIS do fencing ter sido perdido.
	fr.calls = nil

	err = exec.Execute(ctx, h0, run.RunID, manifest)
	if !errors.Is(err, ErrFencingLost) {
		t.Fatalf("esperava ErrFencingLost com um handle obsoleto, veio: %v", err)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("Compose NUNCA deveria ter sido tocado com fencing já perdido — chamadas reais: %v", fr.calls)
	}
	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateRecovering {
		t.Fatalf("o estado do run não deveria ter avançado com fencing perdido, veio %s", got.State)
	}
}

// TestExecute_ResumeFromFailed_CompletesDecision: mesmo achado, mas a retomada encontra o run
// ainda em FAILED (a transição para RECOVERING nem chegou a ser persistida).
func TestExecute_ResumeFromFailed_CompletesDecision(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{}
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)

	manifest := testManifest("") // strategy nunca segura -> RECOVERY_REQUIRED
	run, err := ledger.CreateRun(ctx, h, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	// Grava um snapshot de PREPARING concluído ANTES de FAILED — simula que a falha ocorreu
	// DEPOIS de alguma mutação real (ex. durante o deploy), não antes. Achado Codex P2: R0
	// (sem snapshot) agora tem prioridade sobre a classe de recuperação declarada — sem este
	// snapshot, este teste cairia em ROLLED_BACK/R0 em vez de exercitar o caminho que
	// realmente quer provar (strategy insegura bloqueando o rollback automático).
	digestsJSON, err := json.Marshal(map[Unit]string{UnitAPI: "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("9", 64)})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := ledger.RecordCheckpointDone(ctx, h, run.RunID, string(StepSnapshot), string(digestsJSON)); err != nil {
		t.Fatalf("RecordCheckpointDone: %v", err)
	}
	if err := ledger.TransitionRun(ctx, h, run.RunID, domain.StateFailed); err != nil {
		t.Fatalf("TransitionRun FAILED: %v", err)
	}

	if err := exec.Execute(ctx, h, run.RunID, manifest); err != nil {
		t.Fatalf("Execute (retomada de FAILED): %v", err)
	}
	got, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateRecoveryRequired {
		t.Fatalf("esperava RECOVERY_REQUIRED, veio %s", got.State)
	}
}

// TestReconcile_HeartbeatKeepsLockAliveDuringLongObservation: achado Codex P1 — um lease bem
// mais curto que a duração real da execução (aqui, a janela de observação) não pode permitir
// que outro holder roube a instalação ENQUANTO a reconciliação ainda está em andamento. Prova
// positiva, contra o ledger REAL (SQLite em disco): outro processo tentando adquirir o lock no
// MEIO da execução deve ser recusado — só o heartbeat em background, renovando o lease
// periodicamente, explica isso.
func TestReconcile_HeartbeatKeepsLockAliveDuringLongObservation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	ledgerA, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger A: %v", err)
	}
	defer ledgerA.Close()
	ledgerB, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger B: %v", err)
	}
	defer ledgerB.Close()
	// F9, achado Codex [P1] rodada 10 (fatia de retenção/GC): "nenhuma sincronização ainda"
	// agora é fail-closed por padrão — este teste não é sobre revogação/frescor, então
	// sincroniza explicitamente (mesmo espírito do default de testExecutor).
	if err := ledgerA.MarkRevocationSynced(context.Background()); err != nil {
		t.Fatalf("MarkRevocationSynced: %v", err)
	}

	manifest := testManifest("expand_only")
	fr := fakeRunnerForManifest(manifest)
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	exec := &Executor{
		Ledger: ledgerA, Compose: compose, Migrate: &fakeMigrateRunner{}, Backup: &fakeBackupRunner{}, BackupVerify: &fakeBackupVerifier{},
		HealthTimeout: time.Second,
		ObserveWindow: 400 * time.Millisecond, // bem maior que o lease abaixo
	}

	// lease curto o bastante para expirar várias vezes ao longo da janela de observação SE o
	// heartbeat não estivesse renovando-o (hbInterval = lease/3 ≈ 27ms).
	lease := 80 * time.Millisecond
	installationID := "inst-heartbeat-test"

	h0, err := ledgerA.AcquireLock(context.Background(), installationID, "holder-a", lease)
	if err != nil {
		t.Fatalf("AcquireLock inicial: %v", err)
	}
	run, err := ledgerA.CreateRun(context.Background(), h0, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	reconcileDone := make(chan error, 1)
	go func() {
		reconcileDone <- exec.Reconcile(context.Background(), installationID, "holder-a", lease, map[string]*domain.ReleaseManifest{run.RunID: manifest})
	}()

	// No MEIO da janela de observação (bem depois do lease original ter "vencido" sem
	// heartbeat), um segundo processo tenta assumir a instalação.
	time.Sleep(200 * time.Millisecond)
	_, err = ledgerB.AcquireLock(context.Background(), installationID, "holder-b", time.Minute)
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("holder-b deveria ser recusado enquanto o heartbeat de holder-a mantém o lock vivo, veio %v", err)
	}

	if err := <-reconcileDone; err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, err := ledgerA.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StateSucceeded {
		t.Fatalf("esperava SUCCEEDED, veio %s", got.State)
	}
}

// TestExecMigrateRunner_InheritsProcessEnvironment: achado Codex — um *exec.Cmd novo tem
// Env=nil; `append(c.Env, ...)` sem partir de os.Environ() descartaria TODO o ambiente do
// processo do agente (PATH, APP_DB_PASSWORD, OMNIRA_SESSION_CTX_KEY, SESSION_CTX_MODE).
// Comprova contra um script real (não tools/migrate-sql.sh, só um fake de teste) que uma
// variável do ambiente do processo PAI é visível dentro do subprocesso, e que MIGRATE_TARGET
// (achado Codex: restringir migrations ao alvo aprovado do manifesto) é de fato propagado.
func TestExecMigrateRunner_InheritsProcessEnvironment(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-migrate.sh")
	// Imprime o valor de uma variável que o TESTE define no ambiente do processo PAI — nunca
	// passada explicitamente pelo execMigrateRunner — e sai com erro se estiver vazia, para
	// que Up() falhe audivelmente caso o ambiente não tenha sido herdado. Também confirma
	// MIGRATE_TARGET, que SÓ o próprio execMigrateRunner.Up injeta, e responde a `status`
	// (chamado por Up DEPOIS do `up` para confirmar que o target foi de fato alcançado).
	script_content := "#!/bin/sh\n" +
		"[ -n \"$OMNIRA_TEST_INHERITED_VAR\" ] || { echo MISSING >&2; exit 1; }\n" +
		"case \"$1\" in\n" +
		"  up) [ \"$MIGRATE_TARGET\" = \"000172\" ] || { echo \"MIGRATE_TARGET=$MIGRATE_TARGET\" >&2; exit 1; } ;;\n" +
		"esac\n" +
		// Formato REAL de `migrate-sql.sh status` (nome completo do arquivo, nunca só o
		// prefixo) — achado Codex: manifest.Migrations.Target é só o prefixo ("000172"),
		// então migrationApplied precisa comparar por PREFIXO, não por igualdade exata.
		"case \"$1\" in\n" +
		"  status) echo 'applied  000172_external_start_conversation  checksum=abc' ;;\n" +
		"  *) echo 'up ok' ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(script_content), 0o755); err != nil {
		t.Fatalf("escrever script fake: %v", err)
	}
	t.Setenv("OMNIRA_TEST_INHERITED_VAR", "present")

	m := execMigrateRunner{ScriptPath: script, MigrationsDir: testMigrationsDir(t), DatabaseURL: "postgres://x"}
	if err := m.Up(context.Background(), "000172", ""); err != nil {
		t.Fatalf("Up() deveria ter herdado OMNIRA_TEST_INHERITED_VAR e recebido MIGRATE_TARGET=000172: %v", err)
	}
}

// testMigrationsDir cria um diretório de migrations REAL e temporário com um *.up.sql
// mínimo — migrationsInventoryChecksum precisa de pelo menos um arquivo real para não falhar
// antes mesmo de chegar ao script, e os testes deste arquivo não precisam do diretório
// migrations/ real do repositório (que pertence a F4, fora do escopo do que se testa aqui).
func testMigrationsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "000001_initial.up.sql"), []byte("SELECT 1;\n"), 0o644); err != nil {
		t.Fatalf("escrever migration fake: %v", err)
	}
	return dir
}

// TestExecMigrateRunner_RefusesEmptyTarget: achado Codex — nunca aplicar migrations sem um
// alvo explícito declarado pela release (isso reabriria exatamente o problema que
// MIGRATE_TARGET existe para fechar).
func TestExecMigrateRunner_RefusesEmptyTarget(t *testing.T) {
	m := execMigrateRunner{ScriptPath: "/nonexistent.sh", MigrationsDir: testMigrationsDir(t), DatabaseURL: "postgres://x"}
	if err := m.Up(context.Background(), "", ""); err == nil {
		t.Fatal("Up() com target vazio deveria recusar antes de invocar o script")
	}
}

// TestExecMigrateRunner_NeverInheritsBaselineUpTo: achado Codex P1 — BASELINE_UP_TO é uma
// ferramenta EXCLUSIVAMENTE operacional, manual, de uso único ("adotar um banco cujo schema
// foi aplicado à mão"). Se o PROCESSO DO AGENTE (um daemon de longa duração) tivesse essa
// variável no seu próprio ambiente (um operador configurou no systemd unit uma vez e
// esqueceu de remover), herdá-la silenciosamente faria `up` marcar migrations PENDENTES como
// "aplicadas" sem rodar o SQL real — o agente reportaria o schema como migrado e implantaria
// código novo sobre um banco nunca de fato atualizado. Prova: o PROCESSO DE TESTE define
// BASELINE_UP_TO (simulando o agente a tendo herdado); o script fake FALHA explicitamente se
// a vir — sem a remoção, o teste falharia.
func TestExecMigrateRunner_NeverInheritsBaselineUpTo(t *testing.T) {
	t.Setenv("BASELINE_UP_TO", "999999")
	script := filepath.Join(t.TempDir(), "fake-migrate.sh")
	scriptContent := "#!/bin/sh\n" +
		"[ -z \"${BASELINE_UP_TO:-}\" ] || { echo \"BASELINE_UP_TO=$BASELINE_UP_TO NUNCA deveria chegar aqui\" >&2; exit 1; }\n" +
		"case \"$1\" in\n" +
		"  status) echo 'applied  000172_fake  checksum=abc' ;;\n" +
		"  *) echo 'up ok' ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("escrever script fake: %v", err)
	}
	m := execMigrateRunner{ScriptPath: script, MigrationsDir: testMigrationsDir(t), DatabaseURL: "postgres://x"}
	if err := m.Up(context.Background(), "000172", ""); err != nil {
		t.Fatalf("Up() nunca deveria propagar BASELINE_UP_TO herdado ao script: %v", err)
	}
}

// TestExecMigrateRunner_ChecksumBundle: achado Codex P1 — um arquivo de migration PENDENTE
// (nunca aplicado, então o verify interno do script nunca o cobre) alterado localmente antes
// de rodar deve ser recusado se não bater com manifest.Migrations.ChecksumBundle.
func TestExecMigrateRunner_ChecksumBundle(t *testing.T) {
	dir := testMigrationsDir(t)
	want, err := migrationsInventoryChecksum(dir)
	if err != nil {
		t.Fatalf("migrationsInventoryChecksum: %v", err)
	}
	script := filepath.Join(t.TempDir(), "fake-migrate.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncase \"$1\" in\n  status) echo \"applied  000001_initial  checksum=x\" ;;\n  *) echo up ok ;;\nesac\n"), 0o755); err != nil {
		t.Fatalf("escrever script fake: %v", err)
	}
	m := execMigrateRunner{ScriptPath: script, MigrationsDir: dir, DatabaseURL: "postgres://x"}

	// Target é só o PREFIXO numérico (formato real de manifest.Migrations.Target, ex.
	// "000172" em release/fixtures/manifest-good.json) — nunca o nome completo do arquivo.
	if err := m.Up(context.Background(), "000001", want); err != nil {
		t.Fatalf("Up() deveria aceitar quando o checksum do inventário bate: %v", err)
	}

	// Simula um arquivo pendente alterado DEPOIS que a release foi aprovada (want foi
	// calculado sobre o conteúdo ORIGINAL).
	if err := os.WriteFile(filepath.Join(dir, "000001_initial.up.sql"), []byte("DROP TABLE users;\n"), 0o644); err != nil {
		t.Fatalf("adulterar migration: %v", err)
	}
	if err := m.Up(context.Background(), "000001", want); err == nil {
		t.Fatal("Up() deveria recusar quando o inventário local não bate mais com o checksum_bundle aprovado")
	}
}

// TestKillOnCancel_TerminatesDescendantProcess: achado Codex P1 — exec.CommandContext por si
// só, ao cancelar, mata apenas o processo DIRETO — um filho que esse processo lançou (como
// `psql`/`pg_dump` lançado por tools/migrate-sql.sh/scripts/backup-omnira-db.sh) ficaria órfão
// e continuaria rodando, mutando o Postgres mesmo depois do agente ter perdido o fencing.
// Prova real: um script que lança um `sleep` em background, grava o PID dele, e então espera
// indefinidamente — cancelar o contexto precisa matar TANTO o shell quanto o `sleep`.
func TestKillOnCancel_TerminatesDescendantProcess(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "spawn-child.sh")
	if err := os.WriteFile(script, []byte(
		"#!/bin/sh\nsleep 100 & echo $! > "+pidFile+"\nwait\n",
	), 0o755); err != nil {
		t.Fatalf("escrever script: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	c := exec.CommandContext(ctx, "sh", script)
	killOnCancel(c)
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(pidFile)
		if err == nil && len(strings.TrimSpace(string(data))) > 0 {
			fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &childPID)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("o script nunca gravou o PID do filho — teste não pôde verificar nada")
	}
	if err := syscall.Kill(childPID, 0); err != nil {
		t.Fatalf("o processo filho deveria estar vivo antes do cancelamento: %v", err)
	}

	cancel()
	_ = c.Wait()

	// syscall.Kill(pid, 0) só verifica existência/permissão, não envia sinal nenhum.
	// Margem generosa (sob -race, ou numa máquina/CI sob carga, a entrega do SIGKILL e a
	// atualização do estado do processo no kernel podem demorar bem mais que o normal) —
	// nunca um sono fixo: sai assim que detectar a morte do filho, só o TETO é alto.
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			return // filho morreu — achado Codex corrigido
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("o processo filho (PID %d) continuou vivo depois do cancelamento do contexto", childPID)
}

// TestExecMigrateRunner_RejectsWhenTargetNeverApplied: achado Codex P1 — se o alvo declarado
// pela release não aparece como "applied" depois de `up` (ex. diretório local incompleto),
// Up() deve falhar explicitamente em vez de reportar sucesso com o schema abaixo do esperado.
func TestExecMigrateRunner_RejectsWhenTargetNeverApplied(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fake-migrate.sh")
	// `status` nunca reporta o target como applied — simula um diretório local sem esse
	// arquivo (migração removida/nunca sincronizada).
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncase \"$1\" in\n  status) echo 'applied  000001_initial  checksum=x' ;;\n  *) echo up ok ;;\nesac\n"), 0o755); err != nil {
		t.Fatalf("escrever script fake: %v", err)
	}
	m := execMigrateRunner{ScriptPath: script, MigrationsDir: testMigrationsDir(t), DatabaseURL: "postgres://x"}
	if err := m.Up(context.Background(), "000999", ""); err == nil {
		t.Fatal("Up() deveria falhar quando o target declarado nunca aparece como applied")
	}
}

func TestMigrationApplied(t *testing.T) {
	status := "applied  000001_initial  checksum=abc\npending  000002_next\n"
	// Caso real (achado Codex): manifest.Migrations.Target é só o PREFIXO numérico
	// ("000001"), status sempre reporta o nome COMPLETO ("000001_initial") — precisa
	// reconhecer isso como match, não só uma igualdade exata improvável na prática.
	if !migrationApplied(status, "000001") {
		t.Fatal("deveria reconhecer 000001_initial como applied para target=000001 (comparação por prefixo)")
	}
	if migrationApplied(status, "000002") {
		t.Fatal("000002_next está pending, nunca applied")
	}
	if migrationApplied(status, "0000") {
		t.Fatal("não deveria reconhecer um prefixo parcial do PRÓPRIO prefixo como applied (0000 != 000001)")
	}
}

// writeFakeBackup grava, em `dir`, um par <db>.dump/<db>.meta.json reais no disco — o mesmo
// par que scripts/backup-omnira-db.sh produz — com `content` como o conteúdo do dump
// (determina o tamanho real) e `sizeBytes` como o valor que o .meta.json REGISTRA (deliberadamente
// desacoplado de len(content), para poder simular uma divergência).
func writeFakeBackup(t *testing.T, dir string, content []byte, sizeBytes int64, schemaVersion, format string) string {
	t.Helper()
	dumpPath := filepath.Join(dir, "omnira_20260101T000000Z.dump")
	if err := os.WriteFile(dumpPath, content, 0o600); err != nil {
		t.Fatalf("escrever dump fake: %v", err)
	}
	meta := backupMeta{SchemaVersion: schemaVersion, SizeBytes: sizeBytes, Format: format}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("serializar meta fake: %v", err)
	}
	metaPath := strings.TrimSuffix(dumpPath, ".dump") + ".meta.json"
	if err := os.WriteFile(metaPath, data, 0o600); err != nil {
		t.Fatalf("escrever meta fake: %v", err)
	}
	return dumpPath
}

// TestMetaFileBackupVerifier_RealEvidence: achado Codex — exercita metaFileBackupVerifier
// contra arquivos REAIS no disco (não um fake), incluindo o achado P2 de que o dump em si
// (não só o .meta.json) precisa ser confirmado.
func TestMetaFileBackupVerifier_RealEvidence(t *testing.T) {
	v := metaFileBackupVerifier{MinSizeBytes: 10}

	t.Run("evidência consistente passa", func(t *testing.T) {
		dir := t.TempDir()
		content := []byte("conteúdo de dump fake grande o suficiente")
		dumpPath := writeFakeBackup(t, dir, content, int64(len(content)), "000172", "pg_dump -Fc")
		if err := v.Verify(context.Background(), dumpPath); err != nil {
			t.Fatalf("Verify não deveria falhar com evidência consistente: %v", err)
		}
	})

	t.Run("dump truncado depois do backup é rejeitado mesmo com meta.json intacto", func(t *testing.T) {
		dir := t.TempDir()
		content := []byte("conteúdo original completo")
		dumpPath := writeFakeBackup(t, dir, content, int64(len(content)), "000172", "pg_dump -Fc")
		// Simula truncamento/corrupção POSTERIOR ao backup — o .meta.json nunca é reescrito.
		if err := os.WriteFile(dumpPath, content[:5], 0o600); err != nil {
			t.Fatalf("truncar dump: %v", err)
		}
		if err := v.Verify(context.Background(), dumpPath); err == nil {
			t.Fatal("Verify deveria rejeitar um dump cujo tamanho real não bate com a evidência registrada")
		}
	})

	t.Run("dump ausente é rejeitado", func(t *testing.T) {
		dir := t.TempDir()
		dumpPath := writeFakeBackup(t, dir, []byte("conteúdo irrelevante aqui"), 25, "000172", "pg_dump -Fc")
		if err := os.Remove(dumpPath); err != nil {
			t.Fatalf("remover dump: %v", err)
		}
		if err := v.Verify(context.Background(), dumpPath); err == nil {
			t.Fatal("Verify deveria rejeitar um dump que não existe mais")
		}
	})

	t.Run("schema_version vazio é rejeitado", func(t *testing.T) {
		dir := t.TempDir()
		content := []byte("conteúdo de dump fake grande o suficiente")
		dumpPath := writeFakeBackup(t, dir, content, int64(len(content)), "", "pg_dump -Fc")
		if err := v.Verify(context.Background(), dumpPath); err == nil {
			t.Fatal("Verify deveria rejeitar evidência sem schema_version")
		}
	})
}

// TestExecBackupRunner_FindsMostRecentDump: execBackupRunner nunca parseia a saída textual do
// script para achar o caminho do dump (frágil) — localiza pelo PADRÃO DE NOME real que
// scripts/backup-omnira-db.sh documenta, pegando o mais recente por ordem lexicográfica de
// timestamp.
// TestExecBackupRunner_FindsTheDumpCreatedByThisInvocation: achado Codex P1 — identificar o
// dump pela ORDEM LEXICOGRÁFICA DO NOME entre pré-existentes é frágil contra um relógio do
// host que anda para trás (um dump antigo com timestamp "no futuro" pareceria o mais recente
// sem nenhuma relação com ESTA chamada) e, mais grave, aceitava silenciosamente um script que
// não criasse dump nenhum (desde que já houvesse QUALQUER dump no diretório). O script fake
// aqui de fato CRIA um novo arquivo .dump, como o script real faz — a prova é que o runner
// devolve ESSE arquivo novo, nunca um dos pré-existentes, mesmo quando o pré-existente
// "ordenaria depois" por nome.
func TestExecBackupRunner_FindsTheDumpCreatedByThisInvocation(t *testing.T) {
	backupDir := t.TempDir()
	dbDir := filepath.Join(backupDir, "omnira")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Pré-existente com um nome que ordenaria DEPOIS do dump que o script real vai criar —
	// prova que a seleção nunca depende de comparação de nomes/timestamps entre arquivos.
	staleButLaterName := filepath.Join(dbDir, "omnira_99999999T000000Z.dump")
	if err := os.WriteFile(staleButLaterName, []byte("x"), 0o600); err != nil {
		t.Fatalf("escrever dump pré-existente: %v", err)
	}
	script := filepath.Join(t.TempDir(), "fake-backup.sh")
	newDumpPath := filepath.Join(dbDir, "omnira_20260101T120000Z.dump")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho conteudo_do_dump_novo > "+newDumpPath+"\necho backup ok\n"), 0o755); err != nil {
		t.Fatalf("escrever script fake: %v", err)
	}
	b := execBackupRunner{ScriptPath: script, BackupDir: backupDir, DBName: "omnira"}
	got, err := b.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != newDumpPath {
		t.Fatalf("esperava o dump CRIADO por esta invocação (%s), veio %s", newDumpPath, got)
	}
}

// TestExecBackupRunner_RejectsWhenNoNewDumpAppears: achado Codex P1 — "o teste do runner
// aceitava sucesso mesmo quando o script não cria dump nenhum". Um script que sai com código
// 0 mas nunca produz um dump novo (bug silencioso, ou um BACKUP_DIR que o script resolveu
// diferente do que este runner está olhando) precisa falhar explicitamente, nunca "ter
// sucesso" por acidente porque já havia algum dump antigo no diretório.
func TestExecBackupRunner_RejectsWhenNoNewDumpAppears(t *testing.T) {
	backupDir := t.TempDir()
	dbDir := filepath.Join(backupDir, "omnira")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "omnira_20251231T000000Z.dump"), []byte("x"), 0o600); err != nil {
		t.Fatalf("escrever dump pré-existente: %v", err)
	}
	script := filepath.Join(t.TempDir(), "fake-backup.sh")
	// Sai com sucesso (exit 0) mas NUNCA escreve nenhum arquivo novo — simula o bug que este
	// achado existe para detectar.
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho backup ok\n"), 0o755); err != nil {
		t.Fatalf("escrever script fake: %v", err)
	}
	b := execBackupRunner{ScriptPath: script, BackupDir: backupDir, DBName: "omnira"}
	if _, err := b.Run(context.Background()); err == nil {
		t.Fatal("Run deveria recusar quando nenhum dump NOVO aparece, mesmo que o script saia com sucesso")
	}
}

// TestExecBackupRunner_PassesBackupDirAndDBNameToScript: achado Codex P2 — sem passar
// BACKUP_DIR/DB_NAME para o script, um BackupDir/DBName configurado DIFERENTE dos defaults
// do próprio script real ("backups"/"omnira_dev") faria o script escrever num lugar e este
// runner procurar em outro, rejeitando um backup que de fato teve sucesso. Prova: um script
// fake que, como o REAL, só sabe onde escrever através das env vars BACKUP_DIR/DB_NAME
// (nenhum caminho hardcoded aqui, ao contrário dos outros testes deste arquivo) — se o
// runner não as propagasse, o dump nunca apareceria onde o script escreveu de verdade.
func TestExecBackupRunner_PassesBackupDirAndDBNameToScript(t *testing.T) {
	backupDir := t.TempDir()
	script := filepath.Join(t.TempDir(), "fake-backup.sh")
	// Replica a resolução REAL de scripts/backup-omnira-db.sh:
	// BACKUP_DIR=${BACKUP_DIR:-backups}/${DB_NAME}, escrevendo lá — nunca um caminho
	// conhecido de antemão pelo teste, só o que as env vars dizem.
	scriptContent := "#!/bin/sh\n" +
		"dir=\"${BACKUP_DIR:-backups}/${DB_NAME:-omnira_dev}\"\n" +
		"mkdir -p \"$dir\"\n" +
		"echo conteudo > \"$dir/${DB_NAME}_20260101T000000Z.dump\"\n" +
		"echo backup ok\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("escrever script fake: %v", err)
	}
	// DB_NAME deliberadamente DIFERENTE do default do script ("omnira_dev") — prova que o
	// runner não está só coincidindo com o default, está de fato propagando o valor
	// configurado.
	b := execBackupRunner{ScriptPath: script, BackupDir: backupDir, DBName: "omnira_homolog"}
	got, err := b.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := filepath.Join(backupDir, "omnira_homolog", "omnira_homolog_20260101T000000Z.dump")
	if got != want {
		t.Fatalf("esperava %s (BACKUP_DIR/DB_NAME propagadas ao script), veio %s", want, got)
	}
}

// TestExecBackupRunner_BackupDirAndDBNameAlwaysWinOverExtraEnv: achado Codex P2 — `env` usa
// a ÚLTIMA atribuição de cada variável quando há duplicatas; se b.Env TAMBÉM contivesse um
// BACKUP_DIR/DB_NAME (configuração extra colidindo por acidente), eles NUNCA podem vencer
// sobre os valores derivados de b.BackupDir/b.DBName — são os MESMOS que
// resolvedBackupDBDir usa para procurar o dump depois; se o script escrevesse no lugar
// "errado" de b.Env, um backup bem-sucedido pareceria "sem dump encontrado".
func TestExecBackupRunner_BackupDirAndDBNameAlwaysWinOverExtraEnv(t *testing.T) {
	correctDir := t.TempDir()
	wrongDir := t.TempDir()
	script := filepath.Join(t.TempDir(), "fake-backup.sh")
	scriptContent := "#!/bin/sh\n" +
		"dir=\"$BACKUP_DIR/$DB_NAME\"\n" +
		"mkdir -p \"$dir\"\n" +
		"echo conteudo > \"$dir/${DB_NAME}_20260101T000000Z.dump\"\n" +
		"echo backup ok\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("escrever script fake: %v", err)
	}
	b := execBackupRunner{
		ScriptPath: script,
		BackupDir:  correctDir,
		DBName:     "correct_db",
		// Simula uma colisão: b.Env tenta impor outro BACKUP_DIR/DB_NAME — nunca deve
		// vencer sobre os campos autoritativos acima.
		Env: []string{"BACKUP_DIR=" + wrongDir, "DB_NAME=wrong_db"},
	}
	got, err := b.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := filepath.Join(correctDir, "correct_db", "correct_db_20260101T000000Z.dump")
	if got != want {
		t.Fatalf("esperava %s (BackupDir/DBName autoritativos), veio %s (b.Env venceu por engano)", want, got)
	}
}

// requireSudoForArbitraryScript confirma que esta máquina não só tem `sudo -n` liberado em
// geral (sudo -n true), mas que a política de sudoers também permite rodar um CAMINHO
// ARBITRÁRIO de script via sudo — o que estes testes precisam, já que geram o script fake em
// t.TempDir(). Uma regra de sudoers de produção, corretamente restrita por Cmnd_Alias a um
// caminho canônico (ex.: scripts/backup-omnira-db.sh), nega exatamente isso por design — sudo
// -n true ainda passa (o binário sudo responde), mas a execução do script é recusada. Pular
// nesse caso é honesto: a prova real já foi feita num sandbox com sudo amplo (ver
// ACCEPTANCE-REPORT.md §2.2); isto só evita falso-FAIL de build num ambiente de CI/produção
// com sudoers propositalmente restrito, sem nunca fingir PASS.
func requireSudoForArbitraryScript(t *testing.T) {
	t.Helper()
	if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
		t.Skip("sudo -n não disponível sem senha nesta máquina — pulando prova real de sudo")
	}
	probe := filepath.Join(t.TempDir(), "sudo-probe.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("escrever script de sondagem de sudo: %v", err)
	}
	if err := exec.Command("sudo", "-n", probe).Run(); err != nil {
		t.Skipf("sudo -n não permite executar um caminho arbitrário de script nesta máquina "+
			"(sudoers restrito por caminho, como numa produção endurecida) — pulando prova real: %v", err)
	}
}

// TestExecBackupRunner_EnvSurvivesSudoEnvReset: achado Codex P1 — scripts/backup-omnira-db.sh
// usa `docker ps`/`docker exec` de verdade (precisa do MESMO acesso privilegiado que o
// ComposeAdapter), mas CmdPrefix=["sudo","-n"] sob a política padrão `env_reset` do sudo
// DESCARTA o ambiente herdado do processo chamador — setar c.Env não sobreviveria a essa
// fronteira. Prova REAL (sudo de verdade, nunca simulado): BACKUP_DIR/DB_NAME continuam
// chegando ao script mesmo com sudo no meio, porque são transportados como ARGUMENTOS do
// comando `env`, não como ambiente herdado.
func TestExecBackupRunner_EnvSurvivesSudoEnvReset(t *testing.T) {
	requireSudoForArbitraryScript(t)
	backupDir := t.TempDir()
	script := filepath.Join(t.TempDir(), "fake-backup.sh")
	scriptContent := "#!/bin/sh\n" +
		"[ -n \"$BACKUP_DIR\" ] || { echo 'BACKUP_DIR não chegou ao script' >&2; exit 1; }\n" +
		"[ -n \"$DB_NAME\" ] || { echo 'DB_NAME não chegou ao script' >&2; exit 1; }\n" +
		"dir=\"$BACKUP_DIR/$DB_NAME\"\n" +
		"mkdir -p \"$dir\"\n" +
		"echo conteudo > \"$dir/${DB_NAME}_20260101T000000Z.dump\"\n" +
		"echo backup ok\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("escrever script fake: %v", err)
	}
	b := execBackupRunner{ScriptPath: script, BackupDir: backupDir, DBName: "omnira_sudo_test", CmdPrefix: []string{"sudo", "-n"}}
	// O script roda como root (via sudo) e cria arquivos ali — t.TempDir() não consegue
	// removê-los depois sem privilégio. Limpa com sudo ANTES do cleanup padrão (defers de
	// t.Cleanup correm em LIFO: registrado DEPOIS de t.TempDir(), este roda PRIMEIRO).
	t.Cleanup(func() { _ = exec.Command("sudo", "-n", "rm", "-rf", backupDir).Run() })
	got, err := b.Run(context.Background())
	if err != nil {
		t.Fatalf("Run com sudo -n: %v", err)
	}
	want := filepath.Join(backupDir, "omnira_sudo_test", "omnira_sudo_test_20260101T000000Z.dump")
	if got != want {
		t.Fatalf("esperava %s, veio %s", want, got)
	}
}

// TestExecBackupRunner_SudoBackup_RunsAsRootAndStaysReadableByCaller: achado Codex, duas
// propriedades que tiveram que ser provadas JUNTAS, cada uma quebrando a outra se corrigida
// isoladamente:
//  1. [P2] sudo PRESERVA o umask do processo chamador (nunca o reseta); um daemon systemd
//     com UMask restrito (0077, como docs/release/systemd/omnira-update-agent.service
//     declara) fazia o script, rodando como root via sudo, criar o dump root:root 0700/0600
//     — ilegível para o próprio agente (sem privilégio) tentar ler depois via
//     BackupVerifier, mesmo o backup tendo sido criado com sucesso.
//  2. [P1] a correção de (1) (sudo -g <gid do agente>) SEM -u explícito NUNCA eleva o UID —
//     sudo -g troca só o GID efetivo, mantendo o UID do chamador (confirmado ao testar:
//     `sudo -n -g "#<gid>" id` devolve uid=<chamador>) — quebrando inteiramente o propósito
//     de --compose-sudo: o backup real (docker exec) voltaria a rodar SEM privilégio.
//
// Prova real: o script fake grava o PRÓPRIO `id -u` visto (prova que rodou como root, não
// como o chamador) dentro do dump; simula o UMask do systemd (syscall.Umask real); e
// confirma que o PRÓPRIO processo de teste — nunca com sudo — consegue listar o diretório E
// ler esse conteúdo depois.
func TestExecBackupRunner_SudoBackup_RunsAsRootAndStaysReadableByCaller(t *testing.T) {
	requireSudoForArbitraryScript(t)
	old := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(old) })

	backupDir := t.TempDir()
	script := filepath.Join(t.TempDir(), "fake-backup.sh")
	scriptContent := "#!/bin/sh\n" +
		"dir=\"$BACKUP_DIR/$DB_NAME\"\n" +
		"mkdir -p \"$dir\"\n" +
		"id -u > \"$dir/${DB_NAME}_20260101T000000Z.dump\"\n" +
		"echo backup ok\n"
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("escrever script fake: %v", err)
	}
	b := execBackupRunner{ScriptPath: script, BackupDir: backupDir, DBName: "omnira_perm_test", CmdPrefix: []string{"sudo", "-n"}}
	t.Cleanup(func() { _ = exec.Command("sudo", "-n", "rm", "-rf", backupDir).Run() })

	got, err := b.Run(context.Background())
	if err != nil {
		t.Fatalf("Run com sudo -n sob UMask restritivo: %v", err)
	}

	// Sem sudo a partir daqui — exatamente o que BackupVerifier faz de verdade.
	dumpDir := filepath.Dir(got)
	if _, err := os.ReadDir(dumpDir); err != nil {
		t.Fatalf("o processo chamador (sem privilégio) não conseguiu listar %s criado via sudo: %v", dumpDir, err)
	}
	content, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("o processo chamador (sem privilégio) não conseguiu ler %s criado via sudo: %v", got, err)
	}
	if strings.TrimSpace(string(content)) != "0" {
		t.Fatalf("script deveria ter rodado como root (uid 0) via sudo -u root, viu uid=%s", strings.TrimSpace(string(content)))
	}
}

// TestExecBackupRunner_RelativeBackupDir_ResolvesAgainstScriptRepoNotProcessCwd: achado Codex
// P2 — scripts/backup-omnira-db.sh sempre faz `cd "$(dirname "$0")/.."` antes de resolver um
// BACKUP_DIR relativo (seu próprio default documentado é "backups"); resolver isso relativo ao
// cwd do PROCESSO DO AGENTE (que pode rodar de qualquer diretório) divergiria do que o script
// realmente escreveu. Prova: um repositório fake em t.TempDir() com scripts/fake-backup.sh,
// um BackupDir RELATIVO ("backups"), e o dump real escrito em <repoRoot>/backups/<db>/... —
// precisa ser encontrado mesmo que o processo de teste nunca mude seu próprio cwd para lá.
func TestExecBackupRunner_RelativeBackupDir_ResolvesAgainstScriptRepoNotProcessCwd(t *testing.T) {
	repoRoot := t.TempDir()
	scriptsDir := filepath.Join(repoRoot, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll scripts: %v", err)
	}
	dbDir := filepath.Join(repoRoot, "backups", "omnira")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatalf("MkdirAll backups: %v", err)
	}
	newDumpPath := filepath.Join(dbDir, "omnira_20260101T000000Z.dump")
	script := filepath.Join(scriptsDir, "fake-backup.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho conteudo > "+newDumpPath+"\necho backup ok\n"), 0o755); err != nil {
		t.Fatalf("escrever script fake: %v", err)
	}

	// BackupDir RELATIVO, exatamente o default documentado do script.
	b := execBackupRunner{ScriptPath: script, BackupDir: "backups", DBName: "omnira"}
	got, err := b.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := newDumpPath
	if got != want {
		t.Fatalf("esperava %s (resolvido contra a raiz do repositório do script), veio %s", want, got)
	}
}

// TestRolloutUnits_OnlyDeclaredWorkloads: achado Codex — um manifesto com artifacts para TODOS
// os componentes mas Rollout.Workloads declarando só um subconjunto deve tocar SÓ esse
// subconjunto. Testado end-to-end: PullByDigest só é chamado para a unit declarada.
func TestRolloutUnits_OnlyDeclaredWorkloads(t *testing.T) {
	ctx := context.Background()
	manifest := testManifest("expand_only")
	// Artifacts continuam com os 3 (um build conjunto) mas só a API é de fato alterada nesta
	// release — exatamente o cenário real que o schema do manifesto permite.
	manifest.Rollout.Workloads = []domain.BuildUnit{domain.UnitAPI}
	fr := fakeRunnerForManifest(manifest)
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)

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
	if got.State != domain.StateSucceeded {
		t.Fatalf("esperava SUCCEEDED, veio %s", got.State)
	}

	pulls := 0
	for _, c := range fr.calls {
		if strings.HasPrefix(c, "docker pull ") {
			pulls++
			if !strings.Contains(c, "omnira-api") {
				t.Fatalf("pull inesperado fora do rollout declarado: %s", c)
			}
		}
	}
	if pulls != 1 {
		t.Fatalf("esperava exatamente 1 pull (só a API, declarada em Rollout.Workloads), veio %d: %v", pulls, fr.calls)
	}
}

// TestBackupVerified_RejectsInsufficientEvidence: achado Codex — "o processo de backup saiu
// com sucesso" não é prova de recuperabilidade. BACKUP_VERIFIED precisa de uma verificação
// própria, e uma evidência insuficiente deve bloquear o avanço (RECOVERY_REQUIRED, já que
// nenhum snapshot existe ainda nesse ponto do fluxo).
func TestBackupVerified_RejectsInsufficientEvidence(t *testing.T) {
	ctx := context.Background()
	fr := &fakeRunner{}
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)
	exec.BackupVerify = &fakeBackupVerifier{err: fmt.Errorf("dump não tem schema_version")}

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
	// R0 (especificação §8): falha ainda em BACKUP_VERIFIED, antes de qualquer snapshot —
	// nenhuma mutação ocorreu, então ROLLED_BACK (nada a desfazer) é o terminal correto,
	// nunca RECOVERY_REQUIRED. O que este teste precisa garantir é que o fluxo NUNCA avança
	// com uma evidência de backup insuficiente, não que ele fique bloqueado para sempre.
	if got.State != domain.StateRolledBack {
		t.Fatalf("evidência de backup insuficiente nunca deveria avançar o fluxo (R0 esperado), veio %s", got.State)
	}
	// MIGRATING_EXPAND nunca deveria ter sido alcançado sem um backup verificado antes.
	mr := exec.Migrate.(*fakeMigrateRunner)
	if mr.upCalls != 0 {
		t.Fatalf("migrate up não deveria ter sido chamado, upCalls=%d", mr.upCalls)
	}
}

// TestRecover_RollbackAppliedButStillUnhealthy_RecoveryRequired: achado Codex — se a versão
// ANTERIOR também não sobe de verdade (não é só a versão nova que tem problema; algo mais
// profundo, ex. infraestrutura), marcar ROLLED_BACK mentiria sobre uma recuperação que não
// aconteceu. Usa um fakeRunner estático (nunca fica "healthy", nem antes nem depois do
// rollback) para provar que a verificação pós-rollback detecta isso de verdade.
func TestRecover_RollbackAppliedButStillUnhealthy_RecoveryRequired(t *testing.T) {
	ctx := context.Background()
	previousDigest := "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("9", 64)
	fr := &fakeRunner{
		stdout: map[string]string{"docker ps": "container-123"},
		stdoutByFormat: map[string]string{
			"{{.Config.Image}}":  previousDigest,
			"{{.State.Running}}": "true|starting", // NUNCA fica healthy, nem antes nem depois do rollback
		},
	}
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)
	exec.HealthTimeout = 30 * time.Millisecond

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
	if got.State != domain.StateRecoveryRequired {
		t.Fatalf("rollback aplicado mas nunca saudável deveria resultar em RECOVERY_REQUIRED, veio %s", got.State)
	}
}

// TestReconcile_StopsQueueAfterRecoveryRequired: achado Codex — múltiplos runs ativos na
// mesma instalação; se o primeiro termina em RECOVERY_REQUIRED, o segundo NUNCA deve ser
// processado (a instalação está num estado que exige atenção manual).
func TestReconcile_StopsQueueAfterRecoveryRequired(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	ledger, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()
	// F9, achado Codex [P1] rodada 10 (fatia de retenção/GC): "nenhuma sincronização ainda"
	// agora é fail-closed por padrão — este teste não é sobre revogação/frescor, então
	// sincroniza explicitamente (mesmo espírito do default de testExecutor).
	if err := ledger.MarkRevocationSynced(ctx); err != nil {
		t.Fatalf("MarkRevocationSynced: %v", err)
	}

	// "docker inspect" sempre reporta "starting" (nunca "healthy") — faz o deploy chegar a
	// PREPARING/DEPLOYING (snapshot concluído) e então falhar no health check, DEPOIS de
	// mutação real ter ocorrido (achado Codex P2: R0 agora é checado ANTES da classe de
	// recuperação declarada — uma falha pré-mutação com backup indisponível, usada antes,
	// cairia em ROLLED_BACK/R0 em vez de RECOVERY_REQUIRED, o que não serviria para provar o
	// bloqueio da fila que este teste existe para testar).
	fr := &fakeRunner{stdout: map[string]string{"docker ps": "container-123", "docker inspect": "true|starting"}}
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	exec := &Executor{
		Ledger: ledger, Compose: compose, Migrate: &fakeMigrateRunner{}, Backup: &fakeBackupRunner{}, BackupVerify: &fakeBackupVerifier{},
		HealthTimeout: 50 * time.Millisecond, ObserveWindow: 10 * time.Millisecond,
	}

	installationID := "inst-stop-queue"
	h0, err := ledger.AcquireLock(ctx, installationID, "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	// database_strategy incompatível (nunca na lista de isRollbackSafe) força
	// RECOVERY_REQUIRED, já que há mutação real (snapshot existe) mas a release declara uma
	// estratégia nunca segura para reverter código automaticamente.
	manifest := testManifest("unknown_incompatible_strategy")
	run1, err := ledger.CreateRun(ctx, h0, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run1: %v", err)
	}
	run2, err := ledger.CreateRun(ctx, h0, "run-2", "idem-2", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run2: %v", err)
	}

	err = exec.Reconcile(ctx, installationID, "holder-a", time.Minute, map[string]*domain.ReleaseManifest{
		run1.RunID: manifest, run2.RunID: manifest,
	})
	if err == nil {
		t.Fatal("Reconcile deveria reportar que a fila foi interrompida por RECOVERY_REQUIRED")
	}

	got1, err := ledger.GetRun(ctx, run1.RunID)
	if err != nil {
		t.Fatalf("GetRun run1: %v", err)
	}
	if got1.State != domain.StateRecoveryRequired {
		t.Fatalf("run1 deveria estar em RECOVERY_REQUIRED, veio %s", got1.State)
	}
	got2, err := ledger.GetRun(ctx, run2.RunID)
	if err != nil {
		t.Fatalf("GetRun run2: %v", err)
	}
	if got2.State != domain.StateLocked {
		t.Fatalf("run2 NUNCA deveria ter sido processado (instalação com RECOVERY_REQUIRED pendente), veio %s", got2.State)
	}

	// Achado Codex P1: o bloqueio precisa sobreviver a uma chamada SUBSEQUENTE de Reconcile —
	// RECOVERY_REQUIRED é terminal, então ActiveRuns por si só nunca mostraria run1 de novo;
	// sem o gate explícito, run2 (ainda em LOCKED) seria processado normalmente aqui.
	err = exec.Reconcile(ctx, installationID, "holder-a", time.Minute, map[string]*domain.ReleaseManifest{
		run2.RunID: manifest,
	})
	if err == nil {
		t.Fatal("uma segunda chamada de Reconcile com RECOVERY_REQUIRED ainda não reconhecido deveria recusar, nunca processar run2")
	}
	got2Again, err := ledger.GetRun(ctx, run2.RunID)
	if err != nil {
		t.Fatalf("GetRun run2 (2ª chamada): %v", err)
	}
	if got2Again.State != domain.StateLocked {
		t.Fatalf("run2 ainda não deveria ter sido processado na 2ª chamada, veio %s", got2Again.State)
	}

	// Só depois de um AcknowledgeRecovery explícito (operador) a instalação pode seguir.
	hAck, err := ledger.AcquireLock(ctx, installationID, "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock para reconhecer: %v", err)
	}
	if err := ledger.AcknowledgeRecovery(ctx, hAck, run1.RunID); err != nil {
		t.Fatalf("AcknowledgeRecovery: %v", err)
	}
	// O backup indisponível (errUnavailable) era o que fazia run1 falhar — troca para um
	// backup funcional antes desta última chamada para provar que o DESBLOQUEIO em si
	// funciona ponta a ponta (run2 agora progride), não só que o erro de gate desaparece.
	// fr não tinha nenhum container/health configurado (irrelevante enquanto só se provava o
	// bloqueio); agora run2 precisa de fato completar um deploy, então fr ganha o mínimo para
	// isso ter sucesso.
	exec.Backup = &fakeBackupRunner{}
	fr.stdout = map[string]string{"docker ps": "fake-container-id"}
	// Reconstrói com os IDs/digests corretos por unit (achado Codex P1: a revalidação final
	// antes de SUCCEEDED agora também confere o digest real, não só saúde/running).
	fr.stdoutByFormat = fakeRunnerForManifest(manifest).stdoutByFormat
	if err := exec.Reconcile(ctx, installationID, "holder-a", time.Minute, map[string]*domain.ReleaseManifest{
		run2.RunID: manifest,
	}); err != nil {
		t.Fatalf("Reconcile após AcknowledgeRecovery não deveria mais ser bloqueado: %v", err)
	}
	got2Final, err := ledger.GetRun(ctx, run2.RunID)
	if err != nil {
		t.Fatalf("GetRun run2 (final): %v", err)
	}
	if got2Final.State != domain.StateSucceeded {
		t.Fatalf("run2 deveria ter progredido normalmente após o desbloqueio, veio %s", got2Final.State)
	}
}

// TestReconcile_StopsQueueWhenEarlierRunMissingManifest: achado Codex P1 — se o chamador não
// forneceu manifesto para um run ATIVO mais antigo da mesma instalação, processar os runs
// seguintes mesmo assim poderia implantar uma release mais nova por cima de um run que pode
// estar no meio de um deploy/recuperação (snapshot de rollback inválido depois, ou uma
// retomada futura desfazendo a release mais nova). Precisa parar a fila inteira, igual ao
// caso de erro de Execute.
// TestReconcile_ReleasesLockOnReturn: achado Codex P2 — sem liberar o lock explicitamente ao
// final, ele ficava detido pelo holder que chamou Reconcile até o LEASE EXPIRAR
// NATURALMENTE, mesmo com a execução já totalmente terminada (sucesso, erro, ou bloqueada por
// RECOVERY_REQUIRED). Isso atrasaria sem necessidade um operador tentando agir imediatamente
// depois (ex. `updatectl` chamando AcknowledgeRecovery, que também precisa adquirir o lock).
// Prova: um lease LONGO (nunca expiraria na duração do teste) — só a liberação explícita
// explica outro holder conseguir adquirir o lock imediatamente após Reconcile retornar.
func TestReconcile_ReleasesLockOnReturn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	ledger, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()

	manifest := testManifest("expand_only")
	fr := fakeRunnerForManifest(manifest)
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	exec := &Executor{
		Ledger: ledger, Compose: compose, Migrate: &fakeMigrateRunner{}, Backup: &fakeBackupRunner{}, BackupVerify: &fakeBackupVerifier{},
		HealthTimeout: time.Second, ObserveWindow: 10 * time.Millisecond,
	}

	installationID := "inst-release-on-return"
	longLease := time.Hour // nunca expira durante o teste — só ReleaseLock explica o que segue
	h0, err := ledger.AcquireLock(ctx, installationID, "holder-a", longLease)
	if err != nil {
		t.Fatalf("AcquireLock inicial: %v", err)
	}
	run, err := ledger.CreateRun(ctx, h0, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := ledger.ReleaseLock(ctx, h0); err != nil {
		t.Fatalf("ReleaseLock do handle de setup: %v", err)
	}

	if err := exec.Reconcile(ctx, installationID, "holder-a", longLease, map[string]*domain.ReleaseManifest{
		run.RunID: manifest,
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if _, err := ledger.AcquireLock(ctx, installationID, "holder-b", time.Minute); err != nil {
		t.Fatalf("holder-b deveria conseguir adquirir o lock imediatamente após Reconcile retornar (lock deveria ter sido liberado, não esperar o lease de 1h expirar): %v", err)
	}
}

func TestReconcile_StopsQueueWhenEarlierRunMissingManifest(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ledger.db")
	ledger, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	defer ledger.Close()

	fr := &fakeRunner{stdout: map[string]string{"docker ps": "fake-container-id"}, stdoutByFormat: map[string]string{"{{.State.Running}}": "true|healthy"}}
	compose := &ComposeAdapter{r: fr, Project: "test", OverrideDir: t.TempDir()}
	exec := &Executor{
		Ledger: ledger, Compose: compose, Migrate: &fakeMigrateRunner{}, Backup: &fakeBackupRunner{}, BackupVerify: &fakeBackupVerifier{},
		HealthTimeout: time.Second, ObserveWindow: 10 * time.Millisecond,
	}
	installationID := "inst-missing-manifest"
	h0, err := ledger.AcquireLock(ctx, installationID, "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	manifest := testManifest("expand_only")
	run1, err := ledger.CreateRun(ctx, h0, "run-1", "idem-1", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run1: %v", err)
	}
	run2, err := ledger.CreateRun(ctx, h0, "run-2", "idem-2", "{}", domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun run2: %v", err)
	}

	// Só run2 recebe manifesto — run1 (mais antigo, created_at menor, processado primeiro)
	// fica sem.
	err = exec.Reconcile(ctx, installationID, "holder-a", time.Minute, map[string]*domain.ReleaseManifest{
		run2.RunID: manifest,
	})
	if err == nil {
		t.Fatal("Reconcile deveria reportar o manifesto faltante de run1 e interromper a fila")
	}
	got1, err := ledger.GetRun(ctx, run1.RunID)
	if err != nil {
		t.Fatalf("GetRun run1: %v", err)
	}
	if got1.State != domain.StateLocked {
		t.Fatalf("run1 (sem manifesto) nunca deveria mudar de estado, veio %s", got1.State)
	}
	got2, err := ledger.GetRun(ctx, run2.RunID)
	if err != nil {
		t.Fatalf("GetRun run2: %v", err)
	}
	if got2.State != domain.StateLocked {
		t.Fatalf("run2 NUNCA deveria ter sido processado enquanto run1 (mais antigo) não tem manifesto, veio %s", got2.State)
	}
}

// TestIsRollbackSafe_RespectsDeclaredRecoveryClass: achado Codex P1 — ParseManifest valida
// database_strategy e rollout.recovery_class INDEPENDENTEMENTE, então uma release pode (de
// forma válida pelo schema) declarar R3/R4 mesmo com um database_strategy compatível. Nesses
// casos o rollback automático de código NUNCA é seguro, independente do schema.
// TestExecute_RejectsRunStuckInPreLockState: achado Codex P2 — CreateRun aceita qualquer
// estado inicial (não só LOCKED), incluindo os estados PRÉ-trava (DISCOVERED, PLANNED,
// PREFLIGHTING, READY, AWAITING_APPROVAL — responsabilidade do backend/planner F6, nunca
// deste agente), e ActiveRuns inclui todos os não-terminais. Sem a checagem explícita,
// actionFor devolvia next="" para esses estados e Execute retornava nil (sucesso) sem nunca
// ter tocado o run — Reconcile seguiria para o PRÓXIMO run da instalação como se este
// estivesse resolvido.
func TestExecute_RejectsRunStuckInPreLockState(t *testing.T) {
	ctx := context.Background()
	compose := &ComposeAdapter{r: &fakeRunner{}, Project: "test", OverrideDir: t.TempDir()}
	exec, ledger, h := testExecutor(t, &fakeMigrateRunner{}, &fakeBackupRunner{}, compose)
	manifest := testManifest("expand_only")

	for _, preLock := range []domain.RunState{
		domain.StateDiscovered, domain.StatePlanned, domain.StatePreflighting,
		domain.StateReady, domain.StateAwaitingApproval,
	} {
		run, err := ledger.CreateRun(ctx, h, "run-"+string(preLock), "idem-"+string(preLock), "{}", preLock)
		if err != nil {
			t.Fatalf("CreateRun (%s): %v", preLock, err)
		}
		if err := exec.Execute(ctx, h, run.RunID, manifest); err == nil {
			t.Fatalf("Execute deveria recusar explicitamente um run em %s, nunca reportar sucesso silencioso", preLock)
		}
		got, err := ledger.GetRun(ctx, run.RunID)
		if err != nil {
			t.Fatalf("GetRun (%s): %v", preLock, err)
		}
		if got.State != preLock {
			t.Fatalf("run em %s nunca deveria ter seu estado alterado por este agente, veio %s", preLock, got.State)
		}
	}
}

func TestIsRollbackSafe_RespectsDeclaredRecoveryClass(t *testing.T) {
	for _, rc := range []domain.RecoveryClass{domain.RecoveryR0, domain.RecoveryR1, domain.RecoveryR2} {
		m := testManifest("expand_only")
		m.Rollout.RecoveryClass = rc
		if !isRollbackSafe(m) {
			t.Fatalf("recovery_class=%s com strategy compatível deveria permitir rollback automático", rc)
		}
	}
	for _, rc := range []domain.RecoveryClass{domain.RecoveryR3, domain.RecoveryR4} {
		m := testManifest("expand_only")
		m.Rollout.RecoveryClass = rc
		if isRollbackSafe(m) {
			t.Fatalf("recovery_class=%s NUNCA deveria permitir rollback automático, mesmo com strategy compatível", rc)
		}
	}
}
