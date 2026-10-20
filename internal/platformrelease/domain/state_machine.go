// Package domain: máquina de estados de um platform_update_run, reproduzida
// exatamente da especificação normativa (docs/release/spec/..._PACOTE_EXECUCAO.md
// §6.1) e do ADR-0054 §2.5. Este arquivo define SÓ as transições legais —
// nenhuma lógica de execução (download, backup, deploy) vive aqui; isso é
// F5/F6. A razão de isolar isto num tipo próprio, sem dependências: tanto o
// backend (F6) quanto o Update Agent (F5) e a UI (F7) precisam da MESMA
// tabela de transições legais para nunca divergir sobre "o que pode vir
// depois de X".
package domain

import "fmt"

// RunState é um estado de execução de um platform_update_run.
type RunState string

const (
	StateDiscovered       RunState = "DISCOVERED"
	StatePlanned          RunState = "PLANNED"
	StatePreflighting     RunState = "PREFLIGHTING"
	StateReady            RunState = "READY"
	StateAwaitingApproval RunState = "AWAITING_APPROVAL"
	StateLocked           RunState = "LOCKED"
	StateBackingUp        RunState = "BACKING_UP"
	StateBackupVerified   RunState = "BACKUP_VERIFIED"
	StateDownloading      RunState = "DOWNLOADING"
	StateVerifying        RunState = "VERIFYING"
	StatePreparing        RunState = "PREPARING"
	StateMigratingExpand  RunState = "MIGRATING_EXPAND"
	StateDeploying        RunState = "DEPLOYING"
	StateHealthChecking   RunState = "HEALTH_CHECKING"
	StateObserving        RunState = "OBSERVING"
	StateSucceeded        RunState = "SUCCEEDED"

	// Caminho de falha recuperável (especificação §6.1, §8).
	StateFailed           RunState = "FAILED"
	StateRecovering       RunState = "RECOVERING"
	StateRolledBack       RunState = "ROLLED_BACK"
	StateRecoveryRequired RunState = "RECOVERY_REQUIRED"

	// Impossibilidade de executar (preflight BLOCK, especificação §7.1) —
	// terminal, nunca reaberto no mesmo run; um novo platform_update_plan
	// é necessário para tentar de novo.
	StateBlocked RunState = "BLOCKED"

	// Cancelamento ANTES da etapa de mutação (especificação §6.1
	// "cancelamento permitido: antes da etapa de mutação"). Depois de
	// StateLocked, não existe transição direta para este estado — a única
	// saída seguro-fail-closed de um run já em mutação é o caminho
	// FAILED -> RECOVERING -> {ROLLED_BACK, RECOVERY_REQUIRED}, nunca um
	// "cancel" que pule etapas de verificação.
	StateCancelled RunState = "CANCELLED"
)

// terminal: estados que não têm nenhuma transição legal de saída. Um run
// num estado terminal só é reaberto criando um novo
// platform_update_plan/run — nunca mutando o run antigo.
var terminalStates = map[RunState]bool{
	StateSucceeded:        true,
	StateRolledBack:       true,
	StateRecoveryRequired: true,
	StateBlocked:          true,
	StateCancelled:        true,
}

// mutationBoundary: o primeiro estado em que alguma mutação real
// (backup, download, migration, deploy) pode começar. Cancelamento direto
// só é legal a partir de um estado ANTERIOR a este (especificação §6.1).
const mutationBoundary = StateLocked

// preLockStates é a sequência linear antes da trava (nenhuma mutação ainda).
var preLockStates = []RunState{
	StateDiscovered, StatePlanned, StatePreflighting, StateReady, StateAwaitingApproval,
}

// postLockStates é a sequência linear da execução controlada, depois da
// trava (especificação §6.1, ordem exata).
var postLockStates = []RunState{
	StateLocked, StateBackingUp, StateBackupVerified, StateDownloading, StateVerifying,
	StatePreparing, StateMigratingExpand, StateDeploying, StateHealthChecking, StateObserving,
	StateSucceeded,
}

// legalTransitions é a ÚNICA fonte de verdade sobre "de X pode-se ir para Y".
// Construída, não escrita à mão estado a estado, para que a ordem linear de
// preLockStates/postLockStates nunca divirja silenciosamente desta tabela.
var legalTransitions = buildLegalTransitions()

func buildLegalTransitions() map[RunState]map[RunState]bool {
	t := make(map[RunState]map[RunState]bool)
	add := func(from, to RunState) {
		if t[from] == nil {
			t[from] = make(map[RunState]bool)
		}
		t[from][to] = true
	}

	// Avanço linear normal, antes da trava.
	for i := 0; i < len(preLockStates)-1; i++ {
		add(preLockStates[i], preLockStates[i+1])
	}
	// AWAITING_APPROVAL -> LOCKED (entra na sequência pós-trava).
	add(StateAwaitingApproval, postLockStates[0])
	// Avanço linear normal, depois da trava, até SUCCEEDED.
	for i := 0; i < len(postLockStates)-1; i++ {
		add(postLockStates[i], postLockStates[i+1])
	}

	// Cancelamento: só legal a partir de qualquer estado ANTES da trava
	// (especificação §6.1). Depois da trava, cancelar não é uma transição
	// direta — é tratado como falha (ver abaixo).
	for _, s := range preLockStates {
		add(s, StateCancelled)
	}

	// Preflight pode bloquear o run (BLOCK fail-closed, especificação §7.1)
	// a partir de PREFLIGHTING ou de READY (um WARN crítico reavaliado pode
	// virar BLOCK até ser revalidado).
	add(StatePreflighting, StateBlocked)
	add(StateReady, StateBlocked)

	// Qualquer estado da execução controlada (da trava até OBSERVING,
	// exclusive SUCCEEDED) pode falhar. SUCCEEDED é terminal e nunca falha
	// retroativamente — uma degradação DEPOIS do sucesso observado é um
	// novo run (um rollback planejado), não uma transição deste run.
	for _, s := range postLockStates {
		if s == StateSucceeded {
			continue
		}
		add(s, StateFailed)
	}

	// Caminho de recuperação (especificação §6.1, §8): toda falha é
	// investigada antes de qualquer decisão; nunca FAILED -> ROLLED_BACK
	// direto, sempre passando por RECOVERING (onde a classe R0-R4 é
	// decidida com evidência, não adivinhada).
	add(StateFailed, StateRecovering)
	add(StateRecovering, StateRolledBack)
	add(StateRecovering, StateRecoveryRequired)
	// RECOVERY_REQUIRED pode, depois de uma restauração manual aprovada
	// fora deste run (especificação §8 "Recovery CLI" / runbook), levar a
	// um novo ciclo — mas isso é sempre um platform_update_run NOVO, nunca
	// uma transição deste mesmo run: RECOVERY_REQUIRED permanece terminal
	// aqui de propósito.

	return t
}

// IsTerminal diz se nenhuma transição legal parte deste estado.
func IsTerminal(s RunState) bool { return terminalStates[s] }

// IsPastMutationBoundary diz se este estado já está na sequência
// pós-trava (ou é um estado de falha/recuperação) — usado pela API (F6)
// para decidir se um pedido de cancelamento simples ainda é aceito ou se
// precisa do "protocolo seguro" (especificação §6.1).
func IsPastMutationBoundary(s RunState) bool {
	if s == mutationBoundary {
		return true
	}
	for _, p := range postLockStates {
		if p == s {
			return true
		}
	}
	return s == StateFailed || s == StateRecovering
}

// ValidateTransition retorna nil se a transição from->to é legal, ou um
// erro explicando por que não é — nunca permite silenciosamente uma
// transição fora da tabela. O chamador (F6 ao persistir, F5 ao reconciliar
// depois de um crash) usa isto antes de qualquer gravação de estado.
func ValidateTransition(from, to RunState) error {
	if IsTerminal(from) {
		return fmt.Errorf("platform update run: estado %q é terminal, nenhuma transição é legal a partir dele", from)
	}
	if legalTransitions[from] == nil || !legalTransitions[from][to] {
		return fmt.Errorf("platform update run: transição %q -> %q não é legal", from, to)
	}
	return nil
}
