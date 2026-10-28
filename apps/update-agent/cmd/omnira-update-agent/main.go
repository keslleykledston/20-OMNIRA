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
	"encoding/json"
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

	// F9, achado Codex [P1] rodada 10 / [P1] rodada 12: a marca standalone em si é gravada
	// dentro de cfg.Build() (agentconfig), o ÚNICO ponto que monta um Executor/Ledger reais
	// tanto para este daemon quanto para apps/updatectl (CLI sem daemon algum) — só loga
	// aqui, para observabilidade do processo de longa duração.
	if cfg.Standalone {
		log.Printf("omnira-update-agent: instalação marcada standalone (OMNIRA_AGENT_STANDALONE=true) — revogação de releases nunca será sincronizada com o Postgres")
	}

	log.Printf("omnira-update-agent: iniciado (installation_id=%s holder=%s ledger=%s poll_interval=%s)",
		cfg.InstallationID, cfg.Holder, cfg.LedgerPath, cfg.PollInterval)

	// F9 (monitoramento "status do agente"), achado Codex [P2]: um heartbeat gravado só no
	// INÍCIO de cada ciclo de reconcile pararia de avançar durante qualquer reconcile
	// genuinamente longo (um --observe-window de 10 minutos, um backup grande) — marcando um
	// daemon SAUDÁVEL e trabalhando como obsoleto. Por isso este ticker é INDEPENDENTE do
	// ciclo de poll/reconcile, na sua própria goroutine, num intervalo bem menor que qualquer
	// limiar de obsolescência razoável — nunca espera reconcile() terminar para avançar de
	// novo. Nunca chamado de dentro de agentconfig.ReconcileOnce (que apps/updatectl também
	// chama sob demanda): misturar os dois faria uma invocação manual parecer "o daemon está
	// fazendo poll".
	go runLoop(ctx, heartbeatInterval, func() {
		if err := ledger.RecordAgentHeartbeat(ctx, cfg.InstallationID, cfg.Holder); err != nil {
			log.Printf("omnira-update-agent: registrar heartbeat: %v", err)
		}
	})

	reconcile := func() {
		if err := agentconfig.ReconcileOnce(ctx, exec, ledger, cfg.InstallationID, cfg.Holder, cfg.Lease); err != nil {
			log.Printf("omnira-update-agent: reconcile: %v", err)
		}
		// F9 (monitoramento "versão real/digest divergente"), achado Codex [P2]: nem o
		// ledger (histórico de runs) nem o Postgres (platform_installations) observam
		// containers de verdade por si só — sem isto, um container substituído manualmente,
		// ou uma implantação que falhou deixando outra imagem rodando, nunca apareceria em
		// nenhum sinal de monitoramento. Melhor esforço: um Docker indisponível não derruba
		// o ciclo de reconcile (que já rodou, acima) nem o daemon.
		if snap, serr := exec.Compose.CurrentSnapshot(ctx); serr != nil {
			log.Printf("omnira-update-agent: observar digests reais: %v", serr)
		} else if raw, merr := json.Marshal(snap.Digests); merr != nil {
			log.Printf("omnira-update-agent: serializar digests observados: %v", merr)
		} else if err := ledger.RecordObservedDigests(ctx, cfg.InstallationID, cfg.Holder, string(raw)); err != nil {
			log.Printf("omnira-update-agent: registrar digests observados: %v", err)
		}
	}

	runLoop(ctx, cfg.PollInterval, reconcile)
	log.Printf("omnira-update-agent: sinal recebido, encerrando")
}

// heartbeatInterval é bem menor que qualquer limiar razoável de obsolescência de monitoramento
// (minutos) — o ponto é só provar que o processo ainda está respondendo, nunca refletir
// trabalho real (isso é o que --poll-interval e reconcile() fazem).
const heartbeatInterval = 30 * time.Second

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
