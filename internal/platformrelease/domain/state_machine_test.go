package domain

import "testing"

func TestValidateTransition_FullHappyPath(t *testing.T) {
	path := []RunState{
		StateDiscovered, StatePlanned, StatePreflighting, StateReady, StateAwaitingApproval,
		StateLocked, StateBackingUp, StateBackupVerified, StateDownloading, StateVerifying,
		StatePreparing, StateMigratingExpand, StateDeploying, StateHealthChecking, StateObserving,
		StateSucceeded,
	}
	for i := 0; i < len(path)-1; i++ {
		if err := ValidateTransition(path[i], path[i+1]); err != nil {
			t.Fatalf("passo %d->%d (%s -> %s) deveria ser legal: %v", i, i+1, path[i], path[i+1], err)
		}
	}
}

func TestValidateTransition_RejectsSkippingSteps(t *testing.T) {
	// Pular PREFLIGHTING/READY/AWAITING_APPROVAL direto para LOCKED nunca é
	// legal — preflight fail-closed não pode ser contornado desta forma.
	if err := ValidateTransition(StateDiscovered, StateLocked); err == nil {
		t.Fatal("transição que pula etapas de preflight foi aceita")
	}
}

func TestValidateTransition_RejectsReopeningTerminalState(t *testing.T) {
	for _, terminal := range []RunState{StateSucceeded, StateRolledBack, StateRecoveryRequired, StateBlocked, StateCancelled} {
		if err := ValidateTransition(terminal, StatePlanned); err == nil {
			t.Fatalf("estado terminal %s aceitou uma transição de saída", terminal)
		}
	}
}

func TestValidateTransition_CancelOnlyBeforeLock(t *testing.T) {
	for _, s := range preLockStates {
		if err := ValidateTransition(s, StateCancelled); err != nil {
			t.Errorf("cancelamento antes da trava deveria ser legal a partir de %s: %v", s, err)
		}
	}
	for _, s := range postLockStates {
		if s == StateSucceeded {
			continue
		}
		if err := ValidateTransition(s, StateCancelled); err == nil {
			t.Errorf("cancelamento direto depois da trava (%s) foi aceito; deveria exigir o caminho FAILED->RECOVERING", s)
		}
	}
}

func TestValidateTransition_PreflightCanBlock(t *testing.T) {
	if err := ValidateTransition(StatePreflighting, StateBlocked); err != nil {
		t.Errorf("PREFLIGHTING -> BLOCKED deveria ser legal: %v", err)
	}
	if err := ValidateTransition(StateReady, StateBlocked); err != nil {
		t.Errorf("READY -> BLOCKED (WARN crítico reavaliado) deveria ser legal: %v", err)
	}
}

func TestValidateTransition_AnyExecutionStepCanFailExceptSucceeded(t *testing.T) {
	for _, s := range postLockStates {
		err := ValidateTransition(s, StateFailed)
		if s == StateSucceeded {
			if err == nil {
				t.Error("SUCCEEDED -> FAILED foi aceito; SUCCEEDED é terminal")
			}
			continue
		}
		if err != nil {
			t.Errorf("%s -> FAILED deveria ser legal: %v", s, err)
		}
	}
}

func TestValidateTransition_RecoveryPathNeverSkipsRecovering(t *testing.T) {
	if err := ValidateTransition(StateFailed, StateRolledBack); err == nil {
		t.Fatal("FAILED -> ROLLED_BACK direto (sem RECOVERING) foi aceito")
	}
	if err := ValidateTransition(StateFailed, StateRecoveryRequired); err == nil {
		t.Fatal("FAILED -> RECOVERY_REQUIRED direto (sem RECOVERING) foi aceito")
	}
	if err := ValidateTransition(StateFailed, StateRecovering); err != nil {
		t.Errorf("FAILED -> RECOVERING deveria ser legal: %v", err)
	}
	if err := ValidateTransition(StateRecovering, StateRolledBack); err != nil {
		t.Errorf("RECOVERING -> ROLLED_BACK deveria ser legal: %v", err)
	}
	if err := ValidateTransition(StateRecovering, StateRecoveryRequired); err != nil {
		t.Errorf("RECOVERING -> RECOVERY_REQUIRED deveria ser legal: %v", err)
	}
}

func TestIsPastMutationBoundary(t *testing.T) {
	for _, s := range preLockStates {
		if IsPastMutationBoundary(s) {
			t.Errorf("%s não deveria estar além da fronteira de mutação", s)
		}
	}
	for _, s := range postLockStates {
		if !IsPastMutationBoundary(s) {
			t.Errorf("%s deveria estar além (ou n/a de cancelamento simples) da fronteira de mutação", s)
		}
	}
	if !IsPastMutationBoundary(StateFailed) || !IsPastMutationBoundary(StateRecovering) {
		t.Error("FAILED/RECOVERING deveriam contar como além da fronteira de mutação simples")
	}
}

func TestIsTerminal(t *testing.T) {
	for _, s := range []RunState{StateSucceeded, StateRolledBack, StateRecoveryRequired, StateBlocked, StateCancelled} {
		if !IsTerminal(s) {
			t.Errorf("%s deveria ser terminal", s)
		}
	}
	for _, s := range preLockStates {
		if IsTerminal(s) {
			t.Errorf("%s não deveria ser terminal", s)
		}
	}
}
