// omnira-update-agent é o daemon independente da API/workers (F5, ADR-0054 §2.1) que de
// fato executa um platform_update_run: nenhum endpoint remoto de shell, nenhum socket Docker
// público, nenhum script dinâmico injetado pelo manifesto. Ele só sabe reconciliar os runs
// que JÁ EXISTEM no seu ledger SQLite local (internal/platformrelease/agent) contra o Docker
// real desta instalação — nunca decide por si só QUAL release aplicar (isso é
// responsabilidade de quem cria o run: hoje, só apps/updatectl; quando o backend F6 existir,
// também ele, sempre pelo MESMO ledger local, nunca por um caminho separado).
//
// Loop: reconcilia uma vez ao iniciar (retomando qualquer run que já estivesse em andamento
// quando o processo anterior morreu) e depois a cada --poll-interval. Um erro de
// Reconcile (RECOVERY_REQUIRED pendente, run com manifesto ausente, falha de infraestrutura)
// nunca derruba o daemon — ele loga e tenta de novo no próximo ciclo; só um sinal
// SIGTERM/SIGINT encerra o processo, de forma limpa (a reconciliação em andamento é
// cancelada, o que mata qualquer subprocesso real via killOnCancel, e o processo só sai
// depois dela retornar).
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/omnira/omnira/internal/platformrelease/agentconfig"
)

func main() {
	fs := flag.NewFlagSet("omnira-update-agent", flag.ExitOnError)
	cfg := agentconfig.FlagSet(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	// O daemon reconcilia continuamente e PODE avançar qualquer run até MIGRATING_EXPAND de
	// verdade — exige DATABASE_URL explicitamente (achado Codex P2: Build()/Validate() não
	// exigem mais, para que apps/updatectl rollback, que nunca migra, não fique bloqueado
	// sem credenciais de banco num cenário de emergência).
	if err := cfg.RequireDatabaseURL(); err != nil {
		log.Fatalf("omnira-update-agent: %v", err)
	}
	exec, ledger, err := cfg.Build()
	if err != nil {
		log.Fatalf("omnira-update-agent: %v", err)
	}
	defer ledger.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log.Printf("omnira-update-agent: iniciado (installation_id=%s holder=%s ledger=%s poll_interval=%s)",
		cfg.InstallationID, cfg.Holder, cfg.LedgerPath, cfg.PollInterval)

	reconcile := func() {
		// F9 (monitoramento "status do agente"): grava AQUI, nunca dentro de
		// agentconfig.ReconcileOnce — essa função também é chamada por apps/updatectl sob
		// demanda (uma invocação manual, nunca o daemon de longa duração), e misturar os
		// dois faria uma chamada única de updatectl parecer "o daemon está vivo e fazendo
		// poll". Grava a CADA ciclo, mesmo sem nenhum run ativo (ReconcileOnce pode retornar
		// sem fazer nada) — o sinal de liveness nunca pode depender de haver trabalho.
		if err := ledger.RecordAgentHeartbeat(ctx, cfg.InstallationID, cfg.Holder); err != nil {
			log.Printf("omnira-update-agent: registrar heartbeat: %v", err)
		}
		if err := agentconfig.ReconcileOnce(ctx, exec, ledger, cfg.InstallationID, cfg.Holder, cfg.Lease); err != nil {
			log.Printf("omnira-update-agent: reconcile: %v", err)
		}
	}

	runLoop(ctx, cfg.PollInterval, reconcile)
	log.Printf("omnira-update-agent: sinal recebido, encerrando")
}

// runLoop chama reconcile() uma vez imediatamente e depois a cada pollInterval, até ctx ser
// cancelado (sinal recebido, ou — nos testes — um contexto com deadline/cancel explícito).
// Extraído de main() para ser testável sem precisar de um processo real recebendo sinais: o
// contrato que importa (reconcilia ao iniciar, reconcilia periodicamente, para
// IMEDIATAMENTE quando ctx é cancelado, nunca continua chamando reconcile depois disso) é
// verificável com um context.WithCancel/WithTimeout comum.
func runLoop(ctx context.Context, pollInterval time.Duration, reconcile func()) {
	reconcile()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcile()
		}
	}
}
