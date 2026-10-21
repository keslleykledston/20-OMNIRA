package agent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/omnira/omnira/internal/platformrelease/domain"
)

func openTestLedger(t *testing.T) (*Ledger, string) {
	t.Helper()
	// Um arquivo real em disco (nunca :memory:) — este teste precisa simular DOIS PROCESSOS
	// abrindo o MESMO ledger, o que só é um teste honesto se o arquivo realmente existir no
	// disco e for reaberto por um *Ledger separado, exatamente como dois binários do agente o
	// fariam.
	path := filepath.Join(t.TempDir(), "ledger.db")
	l, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l, path
}

func TestAcquireLock_FirstAcquisition(t *testing.T) {
	l, _ := openTestLedger(t)
	h, err := l.AcquireLock(context.Background(), "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if h.FencingToken != 1 {
		t.Fatalf("esperava fencing_token=1 na primeira aquisição, veio %d", h.FencingToken)
	}
}

// TestAcquireLock_SameHolderCallingAgain_TokenAlwaysAdvances: toda chamada BEM-SUCEDIDA de
// AcquireLock gera um token nunca visto antes, mesmo quando é o mesmo holder pedindo de novo
// — a única forma de manter o MESMO token é Heartbeat. Isto é deliberado (achado Codex): um
// design anterior que preservava o token para "o mesmo holder" deixava um handle obtido antes
// de um ReleaseLock+reaquisição continuar parecendo válido depois.
func TestAcquireLock_SameHolderCallingAgain_TokenAlwaysAdvances(t *testing.T) {
	l, _ := openTestLedger(t)
	ctx := context.Background()
	h1, err := l.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("primeira aquisição: %v", err)
	}
	h2, err := l.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("segunda chamada pelo mesmo holder: %v", err)
	}
	if h2.FencingToken <= h1.FencingToken {
		t.Fatalf("token deveria avançar mesmo para o mesmo holder: %d -> %d", h1.FencingToken, h2.FencingToken)
	}
	// O handle ANTIGO (h1) deixou de ser válido — só o handle mais recente (h2) autentica.
	if err := l.Heartbeat(ctx, h1); !errors.Is(err, ErrFencingLost) {
		t.Fatalf("handle antigo do mesmo holder deveria perder o fencing, veio %v", err)
	}
	if err := l.Heartbeat(ctx, h2); err != nil {
		t.Fatalf("handle mais recente deveria continuar válido: %v", err)
	}
}

// TestAcquireLock_RenewalUpdatesLease: renovar com um lease DIFERENTE do original deve valer
// de verdade (achado Codex: a renovação silenciosa não atualizava lease_ms, então um lease
// curto original nunca era substituído por um lease mais longo pedido depois).
func TestAcquireLock_RenewalUpdatesLease(t *testing.T) {
	l, _ := openTestLedger(t)
	ctx := context.Background()
	if _, err := l.AcquireLock(ctx, "inst-1", "holder-a", 50*time.Millisecond); err != nil {
		t.Fatalf("primeira aquisição (lease curto): %v", err)
	}
	// Renova com um lease bem mais longo ANTES do curto expirar.
	if _, err := l.AcquireLock(ctx, "inst-1", "holder-a", time.Minute); err != nil {
		t.Fatalf("renovação com lease longo: %v", err)
	}
	time.Sleep(120 * time.Millisecond) // teria expirado o lease ORIGINAL de 50ms, não o novo de 1min

	if _, err := l.AcquireLock(ctx, "inst-1", "holder-b", time.Minute); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("o lease renovado (1min) ainda deveria estar válido, veio %v", err)
	}
}

func TestAcquireLock_OtherHolderWhileValid_Refused(t *testing.T) {
	l, _ := openTestLedger(t)
	ctx := context.Background()
	if _, err := l.AcquireLock(ctx, "inst-1", "holder-a", time.Minute); err != nil {
		t.Fatalf("primeira aquisição: %v", err)
	}
	_, err := l.AcquireLock(ctx, "inst-1", "holder-b", time.Minute)
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("esperava ErrLockHeld, veio %v", err)
	}
}

func TestAcquireLock_ExpiredLease_StolenWithTokenBump(t *testing.T) {
	l, _ := openTestLedger(t)
	ctx := context.Background()
	h1, err := l.AcquireLock(ctx, "inst-1", "holder-a", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("primeira aquisição: %v", err)
	}
	time.Sleep(120 * time.Millisecond) // deixa o lease de holder-a expirar de verdade

	h2, err := l.AcquireLock(ctx, "inst-1", "holder-b", time.Minute)
	if err != nil {
		t.Fatalf("roubo de lock expirado deveria funcionar: %v", err)
	}
	if h2.FencingToken != h1.FencingToken+1 {
		t.Fatalf("roubo de lock expirado deve incrementar o token: antigo=%d novo=%d", h1.FencingToken, h2.FencingToken)
	}

	// O holder antigo, ao tentar um heartbeat com o token que já perdeu, é recusado.
	if err := l.Heartbeat(ctx, h1); !errors.Is(err, ErrFencingLost) {
		t.Fatalf("holder antigo deveria perder o fencing, veio %v", err)
	}
}

// TestLockAcrossTwoProcesses simula dois PROCESSOS reais do agente (dois *Ledger distintos,
// cada um com seu próprio *sql.DB, abrindo o MESMO arquivo em disco) disputando o lock da
// mesma instalação — o cenário real que o fencing precisa resolver (especificação §6.1,
// acceptance matrix "Dois administradores clicam atualizar simultaneamente").
func TestLockAcrossTwoProcesses(t *testing.T) {
	_, path := openTestLedger(t)
	ctx := context.Background()

	procA, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("abrir ledger como processo A: %v", err)
	}
	defer procA.Close()
	procB, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("abrir ledger como processo B: %v", err)
	}
	defer procB.Close()

	hA, err := procA.AcquireLock(ctx, "inst-1", "proc-a", 80*time.Millisecond)
	if err != nil {
		t.Fatalf("processo A adquire o lock: %v", err)
	}
	if _, err := procB.AcquireLock(ctx, "inst-1", "proc-b", time.Minute); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("processo B deveria ser recusado enquanto A detém o lock válido, veio %v", err)
	}

	// A continua vivo e renovando — B ainda não consegue.
	if err := procA.Heartbeat(ctx, hA); err != nil {
		t.Fatalf("heartbeat de A: %v", err)
	}
	if _, err := procB.AcquireLock(ctx, "inst-1", "proc-b", time.Minute); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("processo B ainda deveria ser recusado após o heartbeat de A, veio %v", err)
	}

	// A "morre" (para de mandar heartbeat). Depois do lease expirar, B assume.
	time.Sleep(150 * time.Millisecond)
	hB, err := procB.AcquireLock(ctx, "inst-1", "proc-b", time.Minute)
	if err != nil {
		t.Fatalf("processo B deveria conseguir assumir após A expirar: %v", err)
	}
	if hB.FencingToken <= hA.FencingToken {
		t.Fatalf("token de B deveria ser maior que o de A: A=%d B=%d", hA.FencingToken, hB.FencingToken)
	}

	// A, "zumbi", tenta continuar operando com o token antigo — deve ser recusado.
	if err := procA.Heartbeat(ctx, hA); !errors.Is(err, ErrFencingLost) {
		t.Fatalf("A (zumbi) deveria perder o fencing ao tentar operar após B assumir, veio %v", err)
	}
}

func TestCreateRun_IdempotentByKey(t *testing.T) {
	l, _ := openTestLedger(t)
	ctx := context.Background()
	h, err := l.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	r1, err := l.CreateRun(ctx, h, "run-1", "idem-key-1", `{"release_id":"r1"}`, domain.StateDiscovered)
	if err != nil {
		t.Fatalf("primeira criação: %v", err)
	}
	// Mesma idempotency_key, run_id diferente proposto — deve retornar o EXISTENTE (run-1),
	// nunca criar run-2 (acceptance matrix: "uma execução, outra recusada/associada via
	// idempotência").
	r2, err := l.CreateRun(ctx, h, "run-2", "idem-key-1", `{"release_id":"r1"}`, domain.StateDiscovered)
	if err != nil {
		t.Fatalf("segunda criação (dedup): %v", err)
	}
	if r2.RunID != r1.RunID {
		t.Fatalf("esperava dedup para run_id=%s, veio run_id=%s", r1.RunID, r2.RunID)
	}
	// Achado Codex: o caminho de dedup deixava FencingToken zerado, inconsistente com o
	// registro real gravado (e com o que GetRun devolveria).
	if r2.FencingToken == 0 || r2.FencingToken != r1.FencingToken {
		t.Fatalf("FencingToken do caminho de dedup deveria bater com o original (%d), veio %d", r1.FencingToken, r2.FencingToken)
	}
}

// TestAcknowledgeRecovery_UnblocksHasUnresolvedRecoveryRequired: achado Codex P1 — o gate que
// impede novos runs enquanto uma recuperação está pendente precisa sobreviver a QUALQUER
// chamada de Reconcile, nunca só a atual; a ÚNICA forma de destravar é um AcknowledgeRecovery
// explícito (operador), nunca o simples fato de RECOVERY_REQUIRED ser um estado terminal.
func TestAcknowledgeRecovery_UnblocksHasUnresolvedRecoveryRequired(t *testing.T) {
	l, _ := openTestLedger(t)
	ctx := context.Background()
	h, err := l.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	r, err := l.CreateRun(ctx, h, "run-1", "idem-1", `{}`, domain.StateLocked)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, to := range []domain.RunState{domain.StateFailed, domain.StateRecovering, domain.StateRecoveryRequired} {
		if err := l.TransitionRun(ctx, h, r.RunID, to); err != nil {
			t.Fatalf("TransitionRun para %s: %v", to, err)
		}
	}

	pending, err := l.HasUnresolvedRecoveryRequired(ctx, "inst-1")
	if err != nil {
		t.Fatalf("HasUnresolvedRecoveryRequired: %v", err)
	}
	if !pending {
		t.Fatal("deveria reportar recuperação pendente logo após RECOVERY_REQUIRED")
	}

	// Reconhecer um run EM OUTRO ESTADO deve ser recusado — nunca um atalho para destravar
	// algo que nunca foi sinalizado.
	r2, err := l.CreateRun(ctx, h, "run-2", "idem-2", `{}`, domain.StateDiscovered)
	if err != nil {
		t.Fatalf("CreateRun run-2: %v", err)
	}
	if err := l.AcknowledgeRecovery(ctx, h, r2.RunID); err == nil {
		t.Fatal("AcknowledgeRecovery deveria recusar um run que não está em RECOVERY_REQUIRED")
	}

	if err := l.AcknowledgeRecovery(ctx, h, r.RunID); err != nil {
		t.Fatalf("AcknowledgeRecovery: %v", err)
	}
	pending, err = l.HasUnresolvedRecoveryRequired(ctx, "inst-1")
	if err != nil {
		t.Fatalf("HasUnresolvedRecoveryRequired (pós-ack): %v", err)
	}
	if pending {
		t.Fatal("deveria estar destravado após AcknowledgeRecovery")
	}

	got, err := l.GetRun(ctx, r.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if !got.RecoveryAcknowledged {
		t.Fatal("GetRun deveria refletir RecoveryAcknowledged=true")
	}
}

func TestTransitionRun_ValidatesAgainstStateMachine(t *testing.T) {
	l, _ := openTestLedger(t)
	ctx := context.Background()
	h, err := l.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	r, err := l.CreateRun(ctx, h, "run-1", "idem-1", `{}`, domain.StateDiscovered)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	if err := l.TransitionRun(ctx, h, r.RunID, domain.StatePlanned); err != nil {
		t.Fatalf("transição legal DISCOVERED->PLANNED deveria funcionar: %v", err)
	}
	got, err := l.GetRun(ctx, r.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.State != domain.StatePlanned {
		t.Fatalf("estado esperado PLANNED, veio %s", got.State)
	}

	// Transição ilegal (pular etapas) deve ser rejeitada e NUNCA gravada.
	if err := l.TransitionRun(ctx, h, r.RunID, domain.StateSucceeded); err == nil {
		t.Fatal("transição ilegal PLANNED->SUCCEEDED deveria falhar")
	}
	got2, err := l.GetRun(ctx, r.RunID)
	if err != nil {
		t.Fatalf("GetRun após transição ilegal: %v", err)
	}
	if got2.State != domain.StatePlanned {
		t.Fatalf("estado não deveria ter mudado após transição ilegal, veio %s", got2.State)
	}
}

func TestTransitionRun_FencingLostBlocksWrite(t *testing.T) {
	_, path := openTestLedger(t)
	ctx := context.Background()

	procA, _ := OpenLedger(path)
	defer procA.Close()
	procB, _ := OpenLedger(path)
	defer procB.Close()

	hA, err := procA.AcquireLock(ctx, "inst-1", "proc-a", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("AcquireLock A: %v", err)
	}
	r, err := procA.CreateRun(ctx, hA, "run-1", "idem-1", `{}`, domain.StateDiscovered)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if _, err := procB.AcquireLock(ctx, "inst-1", "proc-b", time.Minute); err != nil {
		t.Fatalf("B assume após expirar: %v", err)
	}

	// A, com o token velho, tenta transicionar o run — deve ser bloqueado por fencing, nunca
	// aplicar a mutação mesmo que a transição em si seria logicamente válida.
	if err := procA.TransitionRun(ctx, hA, r.RunID, domain.StatePlanned); !errors.Is(err, ErrFencingLost) {
		t.Fatalf("A deveria perder o fencing ao tentar transicionar, veio %v", err)
	}
}

func TestActiveRuns_ExcludesTerminal(t *testing.T) {
	l, _ := openTestLedger(t)
	ctx := context.Background()
	h, err := l.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	active, err := l.CreateRun(ctx, h, "run-active", "idem-active", `{}`, domain.StateDiscovered)
	if err != nil {
		t.Fatalf("CreateRun ativo: %v", err)
	}
	done, err := l.CreateRun(ctx, h, "run-done", "idem-done", `{}`, domain.StateDiscovered)
	if err != nil {
		t.Fatalf("CreateRun terminal: %v", err)
	}
	// Leva run-done até um estado terminal de verdade (CANCELLED, legal a partir de
	// DISCOVERED).
	if err := l.TransitionRun(ctx, h, done.RunID, domain.StateCancelled); err != nil {
		t.Fatalf("transicionar para CANCELLED: %v", err)
	}

	runs, err := l.ActiveRuns(ctx, "inst-1")
	if err != nil {
		t.Fatalf("ActiveRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != active.RunID {
		t.Fatalf("esperava só [%s] como ativo, veio %+v", active.RunID, runs)
	}
}

func TestCheckpoints_StartDoneFailed(t *testing.T) {
	l, _ := openTestLedger(t)
	ctx := context.Background()
	h, err := l.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	r, err := l.CreateRun(ctx, h, "run-1", "idem-1", `{}`, domain.StateDiscovered)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	status, err := l.StepStatus(ctx, r.RunID, "backup")
	if err != nil {
		t.Fatalf("StepStatus (nunca tentado): %v", err)
	}
	if status != "" {
		t.Fatalf("passo nunca tentado deveria ter status vazio, veio %q", status)
	}

	if err := l.RecordCheckpointStart(ctx, h, r.RunID, "backup"); err != nil {
		t.Fatalf("RecordCheckpointStart: %v", err)
	}
	status, err = l.StepStatus(ctx, r.RunID, "backup")
	if err != nil || status != "started" {
		t.Fatalf("esperava started, veio status=%q err=%v", status, err)
	}

	if err := l.RecordCheckpointDone(ctx, h, r.RunID, "backup", "dump sha256:abc"); err != nil {
		t.Fatalf("RecordCheckpointDone: %v", err)
	}
	status, err = l.StepStatus(ctx, r.RunID, "backup")
	if err != nil || status != "completed" {
		t.Fatalf("esperava completed, veio status=%q err=%v", status, err)
	}

	cps, err := l.Checkpoints(ctx, r.RunID)
	if err != nil {
		t.Fatalf("Checkpoints: %v", err)
	}
	if len(cps) != 2 || cps[0].Status != "started" || cps[1].Status != "completed" {
		t.Fatalf("journal inesperado: %+v", cps)
	}
}

func TestReleaseLock_AllowsNewAcquisition(t *testing.T) {
	l, _ := openTestLedger(t)
	ctx := context.Background()
	h, err := l.AcquireLock(ctx, "inst-1", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	if err := l.ReleaseLock(ctx, h); err != nil {
		t.Fatalf("ReleaseLock: %v", err)
	}
	// Liberado de propósito: outro holder pode assumir IMEDIATAMENTE, sem esperar o lease
	// expirar.
	h2, err := l.AcquireLock(ctx, "inst-1", "holder-b", time.Minute)
	if err != nil {
		t.Fatalf("aquisição após release deveria funcionar: %v", err)
	}
	// Achado Codex: o token NUNCA reseta para 1 depois que a linha já existiu uma vez — ele
	// preserva e avança a partir do valor anterior, mesmo através de um release. Resetar para
	// 1 reabriria um handle antigo (de antes do release) com o mesmo token=1.
	if h2.FencingToken <= h.FencingToken {
		t.Fatalf("token pós-release deveria avançar a partir do anterior (%d), veio %d", h.FencingToken, h2.FencingToken)
	}
	// O handle ANTIGO (de antes do release) nunca volta a ser válido.
	if err := l.Heartbeat(ctx, h); !errors.Is(err, ErrFencingLost) {
		t.Fatalf("handle de antes do release não deveria voltar a ser válido, veio %v", err)
	}
}

// TestCrossInstallation_CannotMutateAnothersRun: um handle válido só para a instalação A
// nunca pode transicionar ou gravar checkpoints no run de uma instalação B — mesmo que run_id
// seja conhecido (achado Codex: as mutações só checavam fencing da instalação do handle, não
// que o RUN em si pertencesse a ela).
func TestCrossInstallation_CannotMutateAnothersRun(t *testing.T) {
	l, _ := openTestLedger(t)
	ctx := context.Background()

	hA, err := l.AcquireLock(ctx, "inst-A", "holder-a", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock A: %v", err)
	}
	hB, err := l.AcquireLock(ctx, "inst-B", "holder-b", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLock B: %v", err)
	}

	runB, err := l.CreateRun(ctx, hB, "run-b", "idem-b", `{}`, domain.StateDiscovered)
	if err != nil {
		t.Fatalf("CreateRun para B: %v", err)
	}

	// A, com um handle válido mas de OUTRA instalação, tenta transicionar o run de B.
	if err := l.TransitionRun(ctx, hA, runB.RunID, domain.StatePlanned); err == nil {
		t.Fatal("A não deveria conseguir transicionar um run da instalação B")
	}
	gotB, err := l.GetRun(ctx, runB.RunID)
	if err != nil {
		t.Fatalf("GetRun B: %v", err)
	}
	if gotB.State != domain.StateDiscovered {
		t.Fatalf("run de B não deveria ter mudado de estado, veio %s", gotB.State)
	}

	// A também não pode gravar um checkpoint no run de B.
	if err := l.RecordCheckpointStart(ctx, hA, runB.RunID, "backup"); err == nil {
		t.Fatal("A não deveria conseguir gravar checkpoint no run da instalação B")
	}
	status, err := l.StepStatus(ctx, runB.RunID, "backup")
	if err != nil {
		t.Fatalf("StepStatus: %v", err)
	}
	if status != "" {
		t.Fatalf("nenhum checkpoint deveria ter sido gravado por A, veio status=%q", status)
	}
}

// TestConcurrentHeartbeatAndCheckpoints_NoSpuriousErrors: um heartbeat em loop concorrente com
// gravações de checkpoint não deve nunca falhar com um erro de infraestrutura do SQLite
// (achado Codex: transações DEFERRED que leem antes de escrever podem receber
// SQLITE_BUSY_SNAPSHOT ao tentar fazer upgrade para escritoras se outra transação comitou
// nesse meio-tempo — um erro que busy_timeout não resolve). Com `_txlock=immediate` (a
// correção), toda transação já nasce escritora e serializa normalmente via busy_timeout —
// toda chamada aqui deve ou ter sucesso, ou falhar só com um erro de domínio esperado, nunca
// com um erro bruto do driver.
func TestConcurrentHeartbeatAndCheckpoints_NoSpuriousErrors(t *testing.T) {
	l, _ := openTestLedger(t)
	ctx := context.Background()
	h, err := l.AcquireLock(ctx, "inst-1", "holder-a", 10*time.Second)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}
	r, err := l.CreateRun(ctx, h, "run-1", "idem-1", `{}`, domain.StateDiscovered)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	const iterations = 200
	errs := make(chan error, iterations*2)
	done := make(chan struct{})

	go func() {
		for i := 0; i < iterations; i++ {
			if err := l.Heartbeat(ctx, h); err != nil {
				errs <- fmt.Errorf("heartbeat #%d: %w", i, err)
			}
		}
		close(done)
	}()
	for i := 0; i < iterations; i++ {
		step := fmt.Sprintf("step-%d", i%5)
		if err := l.RecordCheckpointStart(ctx, h, r.RunID, step); err != nil {
			errs <- fmt.Errorf("checkpoint start #%d: %w", i, err)
		}
	}
	<-done
	close(errs)
	for err := range errs {
		t.Errorf("operação concorrente falhou de forma inesperada: %v", err)
	}
}

// TestOpenLedger_MigratesPreExistingDatabaseMissingRecoveryAcknowledged: achado Codex P1 —
// `CREATE TABLE IF NOT EXISTS` nunca altera uma tabela já existente. Simula o caso real de
// produção (o binário do agente é atualizado, mas o arquivo .db no disco é da versão
// ANTERIOR, sem a coluna recovery_acknowledged) criando manualmente um ledger com o schema
// ANTIGO — nunca via OpenLedger, que já inclui a coluna desde o CREATE TABLE — e confirmando
// que abrir esse arquivo com o OpenLedger ATUAL migra, preserva os dados existentes e nunca
// falha ao reabrir de novo (idempotência do ALTER TABLE).
func TestOpenLedger_MigratesPreExistingDatabaseMissingRecoveryAcknowledged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacyDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("abrir banco legado: %v", err)
	}
	const legacySchema = `
	CREATE TABLE update_runs (
	  run_id          TEXT PRIMARY KEY,
	  installation_id TEXT NOT NULL,
	  idempotency_key TEXT NOT NULL,
	  state           TEXT NOT NULL,
	  fencing_token   INTEGER NOT NULL,
	  manifest_json   TEXT NOT NULL,
	  created_at      TEXT NOT NULL,
	  updated_at      TEXT NOT NULL,
	  UNIQUE(installation_id, idempotency_key)
	);`
	if _, err := legacyDB.Exec(legacySchema); err != nil {
		t.Fatalf("criar schema legado: %v", err)
	}
	// Um run real já existente ANTES da migração — prova que migrar não apaga/corrompe dados.
	if _, err := legacyDB.Exec(
		`INSERT INTO update_runs(run_id, installation_id, idempotency_key, state, fencing_token, manifest_json, created_at, updated_at)
		 VALUES ('run-legacy', 'inst-1', 'idem-legacy', 'RECOVERY_REQUIRED', 1, '{}', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("inserir run legado: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("fechar banco legado: %v", err)
	}

	l, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("OpenLedger deveria migrar um banco legado sem erro: %v", err)
	}
	defer l.Close()

	got, err := l.GetRun(context.Background(), "run-legacy")
	if err != nil {
		t.Fatalf("GetRun após migração: %v", err)
	}
	if got.State != domain.StateRecoveryRequired {
		t.Fatalf("dado pré-existente deveria ser preservado, veio state=%s", got.State)
	}
	if got.RecoveryAcknowledged {
		t.Fatal("a nova coluna deveria assumir o default (false) para uma linha pré-existente")
	}
	pending, err := l.HasUnresolvedRecoveryRequired(context.Background(), "inst-1")
	if err != nil {
		t.Fatalf("HasUnresolvedRecoveryRequired após migração: %v", err)
	}
	if !pending {
		t.Fatal("deveria detectar o RECOVERY_REQUIRED pré-existente mesmo após migrar o schema")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reabrir de novo (schema já migrado) nunca deve falhar com "duplicate column name".
	l2, err := OpenLedger(path)
	if err != nil {
		t.Fatalf("reabrir um ledger já migrado deveria ser idempotente, veio: %v", err)
	}
	defer l2.Close()
}

// TestOpenLedger_ConcurrentMigration_NeverFailsWithDuplicateColumn: achado Codex P2 — dois
// PROCESSOS abrindo o MESMO ledger legado ao mesmo tempo (ex. o agente e `updatectl` logo
// depois de um upgrade do próprio binário) podiam ambos observar a coluna ausente antes de
// qualquer um rodar o ALTER — o primeiro tinha sucesso, o segundo falhava com "duplicate
// column name". A migração agora corre dentro de uma transação (serializada pelo próprio
// lock do SQLite via `_txlock=immediate`), então NENHUMA das N aberturas concorrentes deve
// falhar.
func TestOpenLedger_ConcurrentMigration_NeverFailsWithDuplicateColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-concurrent.db")
	legacyDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("abrir banco legado: %v", err)
	}
	const legacySchema = `
	CREATE TABLE update_runs (
	  run_id          TEXT PRIMARY KEY,
	  installation_id TEXT NOT NULL,
	  idempotency_key TEXT NOT NULL,
	  state           TEXT NOT NULL,
	  fencing_token   INTEGER NOT NULL,
	  manifest_json   TEXT NOT NULL,
	  created_at      TEXT NOT NULL,
	  updated_at      TEXT NOT NULL,
	  UNIQUE(installation_id, idempotency_key)
	);`
	if _, err := legacyDB.Exec(legacySchema); err != nil {
		t.Fatalf("criar schema legado: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("fechar banco legado: %v", err)
	}

	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := OpenLedger(path)
			if err != nil {
				errs <- err
				return
			}
			errs <- l.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("OpenLedger concorrente nunca deveria falhar com coluna duplicada ou qualquer outro erro: %v", err)
		}
	}
}
