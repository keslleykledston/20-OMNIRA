package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunLoop_ReconcilesImmediatelyOnStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	go func() {
		// Cancela logo depois da primeira chamada ter certamente ocorrido (runLoop chama
		// reconcile() ANTES de entrar no select) — sem esperar nenhum tick do poll
		// interval, que aqui é deliberadamente longo (nunca dispara durante o teste).
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	runLoop(ctx, time.Hour, func() { calls.Add(1) })
	if calls.Load() == 0 {
		t.Fatal("runLoop deveria reconciliar imediatamente ao iniciar, antes de qualquer tick do poll interval")
	}
}

func TestRunLoop_ReconcilesPeriodically(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	var calls atomic.Int32
	runLoop(ctx, 20*time.Millisecond, func() { calls.Add(1) })
	// 1 chamada imediata + pelo menos mais algumas dentro de 150ms com poll de 20ms.
	if calls.Load() < 3 {
		t.Fatalf("esperava várias reconciliações periódicas em 150ms com poll_interval=20ms, veio %d", calls.Load())
	}
}

func TestRunLoop_StopsImmediatelyWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // já cancelado ANTES de runLoop começar
	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		runLoop(ctx, time.Hour, func() { calls.Add(1) })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runLoop deveria retornar quase imediatamente com um contexto já cancelado")
	}
	// A chamada inicial (antes do primeiro select) ainda acontece — é o loop PERIÓDICO que
	// nunca deveria rodar depois disso.
	if calls.Load() != 1 {
		t.Fatalf("esperava exatamente 1 chamada (a inicial) com ctx já cancelado, veio %d", calls.Load())
	}
}

func TestRunLoop_NeverCallsReconcileAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	runLoop(ctx, 10*time.Millisecond, func() { calls.Add(1) })
	afterStop := calls.Load()
	// Espera mais tempo que vários poll_intervals — se runLoop não tivesse de fato
	// retornado, calls continuaria subindo (mas a própria chamada de runLoop já retornou
	// aqui, então isto só confirma que a contagem final é estável).
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != afterStop {
		t.Fatalf("reconcile foi chamado DEPOIS de runLoop já ter retornado (contagem subiu de %d para %d)", afterStop, calls.Load())
	}
}
