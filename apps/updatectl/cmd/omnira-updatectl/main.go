// omnira-updatectl é a CLI local e segura do Update Agent (F5, especificação §8 "CLI local
// segura para observar status/reconciliar/reinstalar digest anterior compatível quando a
// API estiver indisponível"). Opera inteiramente sobre o ledger SQLite local e o Docker real
// desta instalação — nenhuma chamada de rede, nenhuma dependência do backend/API da
// aplicação estar disponível. É também, hoje (antes do backend F6 existir), o único ponto de
// entrada para CRIAR um run: um operador confirma um manifesto já aprovado e `start` o
// registra no ledger local, de onde omnira-update-agent (ou `updatectl reconcile`) o
// executa.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/omnira/omnira/internal/platformrelease/agent"
	"github.com/omnira/omnira/internal/platformrelease/agentconfig"
	"github.com/omnira/omnira/internal/platformrelease/domain"
)

const usage = `usage: omnira-updatectl <command> [flags]

  status [--run ID]             sem --run: lista todos os runs desta instalação (histórico
                                 completo, inclusive terminais) e avisa se há uma recuperação
                                 pendente; com --run: detalha o estado e o journal de
                                 checkpoints de um run específico
  start --manifest PATH --approved-out-of-band
                                 valida a FORMA de um manifesto de release e cria um run
                                 LOCKED no ledger local, pronto para reconciliação (pelo
                                 daemon ou por 'reconcile' abaixo). --approved-out-of-band é
                                 OBRIGATÓRIA: domain.ParseManifest nunca verifica assinatura/
                                 proveniência/revogação (isso está BLOCKED_EXTERNAL nesta
                                 instalação — ver release/policies/signing.yaml); confirme
                                 que revisou o manifesto por outro meio antes de usar a flag
  reconcile                     dispara uma reconciliação imediata de todos os runs ativos
                                 desta instalação, sem esperar o próximo ciclo do daemon
  acknowledge-recovery --run ID confirma, fora de banda, que um run em RECOVERY_REQUIRED foi
                                 investigado e resolvido manualmente — única forma de
                                 destravar novos runs desta instalação depois disso
  rollback [--run ID] [--yes]   reinstala o snapshot de código/config (nunca dados) do
                                 PREPARING mais recente de um run — o do run indicado, ou o
                                 mais recente desta instalação se omitido; pede confirmação a
                                 menos que --yes seja dado

Flags comuns a todos os comandos (--installation-id, --ledger-path, --compose-*,
--migrate-*, --backup-*, --database-url, ...): rode qualquer comando com -h para a lista
completa, lida de OMNIRA_AGENT_* quando a flag correspondente não é dada.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "status":
		err = runStatus(os.Args[2:], os.Stdout)
	case "start":
		err = runStart(os.Args[2:], os.Stdout)
	case "reconcile":
		err = runReconcile(os.Args[2:], os.Stdout)
	case "acknowledge-recovery":
		err = runAcknowledgeRecovery(os.Args[2:], os.Stdout)
	case "rollback":
		err = runRollback(os.Args[2:], os.Stdout, os.Stdin)
	case "-h", "--help", "help":
		fmt.Fprint(os.Stdout, usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "omnira-updatectl:", err)
		os.Exit(1)
	}
}

func runStatus(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	cfg := agentconfig.FlagSet(fs)
	runID := fs.String("run", "", "ID do run a detalhar (default: lista todos)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.InstallationID == "" {
		return fmt.Errorf("status: --installation-id (ou OMNIRA_AGENT_INSTALLATION_ID) é obrigatório")
	}
	ledger, err := agent.OpenLedger(cfg.LedgerPath)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	defer ledger.Close()
	ctx := context.Background()

	if *runID != "" {
		return printRunDetail(ctx, ledger, *runID, out)
	}

	runs, err := ledger.AllRuns(ctx, cfg.InstallationID)
	if err != nil {
		return fmt.Errorf("status: listar runs: %w", err)
	}
	if len(runs) == 0 {
		fmt.Fprintln(out, "nenhum run registrado para esta instalação")
	}
	for _, r := range runs {
		fmt.Fprintf(out, "%s\t%s\tatualizado em %s\n", r.RunID, r.State, r.UpdatedAt.Format(time.RFC3339))
	}
	pending, err := ledger.HasUnresolvedRecoveryRequired(ctx, cfg.InstallationID)
	if err != nil {
		return fmt.Errorf("status: verificar recuperação pendente: %w", err)
	}
	if pending {
		fmt.Fprintln(out, "\nATENÇÃO: há um RECOVERY_REQUIRED não reconhecido nesta instalação — nenhum run novo será processado até 'acknowledge-recovery'")
	}
	return nil
}

func printRunDetail(ctx context.Context, ledger *agent.Ledger, runID string, out io.Writer) error {
	run, err := ledger.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("status: %w", err)
	}
	fmt.Fprintf(out, "run_id:                %s\n", run.RunID)
	fmt.Fprintf(out, "installation_id:       %s\n", run.InstallationID)
	fmt.Fprintf(out, "state:                 %s\n", run.State)
	fmt.Fprintf(out, "recovery_acknowledged: %v\n", run.RecoveryAcknowledged)
	fmt.Fprintf(out, "created_at:            %s\n", run.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(out, "updated_at:            %s\n", run.UpdatedAt.Format(time.RFC3339))
	fmt.Fprintln(out, "\ncheckpoints:")
	cps, err := ledger.Checkpoints(ctx, runID)
	if err != nil {
		return fmt.Errorf("status: checkpoints: %w", err)
	}
	for _, c := range cps {
		fmt.Fprintf(out, "  %s  %-10s %-9s %s\n", c.RecordedAt.Format(time.RFC3339), c.Step, c.Status, c.Detail)
	}
	return nil
}

func runStart(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	cfg := agentconfig.FlagSet(fs)
	manifestPath := fs.String("manifest", "", "caminho do manifesto JSON da release já aprovado")
	approvedOutOfBand := fs.Bool("approved-out-of-band", false,
		"obrigatória: confirma que este manifesto foi revisado/aprovado por outro meio (release/policies/signing.yaml) — ver aviso")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.InstallationID == "" {
		return fmt.Errorf("start: --installation-id (ou OMNIRA_AGENT_INSTALLATION_ID) é obrigatório")
	}
	if *manifestPath == "" {
		return fmt.Errorf("start: --manifest é obrigatório")
	}
	// start chama AcquireLock mais abaixo sem passar por cfg.Build()/Validate() completo
	// (não usa Compose/Migrate/Backup) — precisa desta checagem isolada (achado Codex P2:
	// --lease=0s/negativo/<1ms deixaria qualquer outro processo roubar o lock
	// imediatamente).
	if err := cfg.ValidateLease(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	// Achado Codex P1: domain.ParseManifest valida só a FORMA do manifesto (campos,
	// digest/por-ghcr, release_id, etc.) — nunca assinatura, proveniência ou revogação
	// (release/policies/signing.yaml; o verificador real de assinatura é BLOCKED_EXTERNAL
	// nesta sessão, sem credenciais GHCR/gh CLI/Cosign). Aceitar qualquer JSON
	// estruturalmente válido como "já aprovado" (como o comentário anterior deste
	// parâmetro erroneamente sugeria) deixaria um manifesto FORJADO mas sintaticamente
	// correto criar um run executável de verdade. Em vez de fingir uma verificação que não
	// existe, este gate exige uma confirmação EXPLÍCITA e nunca-default do operador — nunca
	// substitui a verificação real quando ela existir (F3), só impede o uso acidental
	// enquanto ela não existe.
	if !*approvedOutOfBand {
		return fmt.Errorf("start: --approved-out-of-band é obrigatória — domain.ParseManifest valida só a FORMA do manifesto, nunca assinatura/proveniência/revogação (a verificação real está BLOCKED_EXTERNAL nesta instalação, sem credenciais GHCR/Cosign; ver release/policies/signing.yaml); confirme que revisou este manifesto por outro meio antes de prosseguir")
	}
	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("start: ler manifesto: %w", err)
	}
	manifest, err := domain.ParseManifest(raw)
	if err != nil {
		return fmt.Errorf("start: manifesto inválido: %w", err)
	}

	ledger, err := agent.OpenLedger(cfg.LedgerPath)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	defer ledger.Close()
	ctx := context.Background()
	h, err := ledger.AcquireLock(ctx, cfg.InstallationID, cfg.Holder, cfg.Lease)
	if err != nil {
		return fmt.Errorf("start: adquirir lock: %w", err)
	}
	defer ledger.ReleaseLock(context.Background(), h)

	runID := uuid.NewString()
	// Achado Codex P2: usar manifest.ReleaseID como idempotency_key SEMPRE fixa (sem esta
	// checagem) deduplicava permanentemente — se a release falhasse por um problema
	// TRANSITÓRIO (backup/registry fora do ar) e terminasse em ROLLED_BACK, uma nova
	// chamada de 'start' para a MESMA release (depois do operador corrigir o problema)
	// encontraria esse MESMO run terminal e o devolveria como se fosse novo, mas
	// ActiveRuns/Reconcile excluem runs terminais — a release nunca seria tentada de novo
	// sem editar o ledger manualmente ou fabricar outra release. idempotencyKeyForRetry
	// preserva a deduplicação real (dois cliques simultâneos, ou uma chamada repetida
	// enquanto o run anterior ainda está ativo OU já teve sucesso), mas permite uma nova
	// tentativa genuína depois de uma falha.
	idempotencyKey, err := idempotencyKeyForRetry(ctx, ledger, cfg.InstallationID, manifest.ReleaseID)
	if err != nil {
		return fmt.Errorf("start: verificar tentativas anteriores desta release: %w", err)
	}
	run, err := ledger.CreateRun(ctx, h, runID, idempotencyKey, string(raw), domain.StateLocked)
	if err != nil {
		return fmt.Errorf("start: criar run: %w", err)
	}
	fmt.Fprintf(out, "run %s criado para release %s (state=%s)\n", run.RunID, manifest.ReleaseID, run.State)
	return nil
}

// idempotencyKeyForRetry devolve releaseID como está (deduplicação normal: duas chamadas
// simultâneas, ou uma repetida enquanto o run anterior ainda está ativo ou já SUCCEEDED
// acabam no MESMO run) — a menos que já exista, para esta instalação, um run TERMINAL mas
// SEM sucesso para essa mesma releaseID (ROLLED_BACK, RECOVERY_REQUIRED, CANCELLED,
// BLOCKED), caso em que devolve uma key derivada mas nova, permitindo que esta chamada seja
// uma tentativa genuína em vez de reabrir/retornar o run antigo já resolvido.
func idempotencyKeyForRetry(ctx context.Context, ledger *agent.Ledger, installationID, releaseID string) (string, error) {
	runs, err := ledger.AllRuns(ctx, installationID)
	if err != nil {
		return "", fmt.Errorf("listar runs: %w", err)
	}
	// Achado Codex P2 (duas rodadas): comparar só contra a key EXATA original (releaseID)
	// ignorava qualquer retry já gerado por uma chamada anterior deste mesmo loop — depois
	// do PRIMEIRO retry (idempotency_key = "<releaseID>-retry-<timestamp>"), uma NOVA
	// chamada de 'start' nunca encontrava esse run e gerava um TERCEIRO run duplicado. A
	// correção seguinte (comparar por PREFIXO de string "<releaseID>-retry-") introduziu
	// uma colisão de namespace real: releaseIDRe permite um sufixo livre
	// (ex. "omnira-1.2.3-retry-rc1" é um release_id LEGÍTIMO e DISTINTO de "omnira-1.2.3"),
	// então processar releaseID="omnira-1.2.3" casaria por acidente com o run de uma
	// release totalmente diferente que só por coincidência começa com esse prefixo —
	// devolvendo a key errada e fazendo CreateRun silenciosamente reaproveitar o run da
	// OUTRA release em vez de agendar a que foi pedida. A família correta nunca é definida
	// por um padrão textual na idempotency_key — é definida pelo manifest.ReleaseID REAL,
	// desserializado do manifest_json congelado de cada run (um campo estruturado, nunca
	// ambíguo com decoração textual). AllRuns já ordena por created_at ASC, então o último
	// match no loop é sempre a tentativa mais recente desta família.
	var latest *agent.RunRecord
	for i := range runs {
		r := &runs[i]
		m, err := domain.ParseManifest([]byte(r.ManifestJSON))
		if err != nil {
			continue // manifesto congelado que não valida mais nunca participa de nenhuma família
		}
		if m.ReleaseID == releaseID {
			latest = r
		}
	}
	if latest == nil {
		return releaseID, nil // nenhuma tentativa anterior — usa a key normal
	}
	// Achado Codex P2: tratar SUCCEEDED como "sempre reusar a key" deduplicava
	// PERMANENTEMENTE até uma release que já teve sucesso — se a release A tivesse sucesso,
	// depois B tivesse sucesso (promovendo além de A), e o operador quisesse reaplicar A de
	// novo (ex. um downgrade de volta, ou simplesmente repetir o mesmo deploy por alguma
	// razão operacional), 'start --manifest A' devolveria o run ANTIGO de A — já terminal,
	// nunca reconciliado de novo — reportando "run criado" sem nada realmente acontecer. A
	// deduplicação REAL só faz sentido ENQUANTO a tentativa anterior ainda está ATIVA (dois
	// cliques simultâneos da MESMA intenção, ou uma chamada repetida antes do run anterior
	// terminar) — uma vez que ele TERMINA, SEJA COM SUCESSO OU FALHA, qualquer nova chamada
	// de 'start' para esta release é uma NOVA tentativa genuína, nunca a mesma requisição.
	if !domain.IsTerminal(latest.State) {
		return latest.IdempotencyKey, nil // tentativa mais recente ainda ativa — dedup real
	}
	return fmt.Sprintf("%s-retry-%s", releaseID, time.Now().UTC().Format("20060102T150405.000000000Z")), nil
}

func runReconcile(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	cfg := agentconfig.FlagSet(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	// 'reconcile' pode avançar um run até MIGRATING_EXPAND de verdade — exige DATABASE_URL
	// explicitamente aqui (achado Codex P2: Build()/Validate() não exigem mais, para que
	// 'rollback', que nunca migra, não fique bloqueado sem credenciais de banco).
	if err := cfg.RequireDatabaseURL(); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	exec, ledger, err := cfg.Build()
	if err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	defer ledger.Close()
	// Achado Codex P1: um context.Background() comum nunca reage a SIGINT/SIGTERM — se o
	// operador interromper o CLI no meio de uma migração/backup, killOnCancel (que já
	// isola esses subprocessos em seus próprios grupos de processo) nunca é acionado, eles
	// ficam ÓRFÃOS mutando containers/dados, e o heartbeat do lock para junto com o
	// processo principal — quando o lease expirar, o daemon poderia assumir a instalação e
	// reconciliar CONCORRENTEMENTE com a operação órfã ainda em andamento. Mesmo padrão
	// signal-aware que apps/update-agent já usa; o processo só termina depois que a
	// reconciliação (e seu cancelamento, se houver) retornar.
	ctx, stop := signalContext()
	defer stop()
	if err := agentconfig.ReconcileOnce(ctx, exec, ledger, cfg.InstallationID, cfg.Holder, cfg.Lease); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	fmt.Fprintln(out, "reconciliação concluída")
	return nil
}

// signalContext devolve um contexto cancelado ao primeiro SIGINT/SIGTERM — usado por
// 'reconcile' e 'rollback' (as duas operações que podem demorar e mutam containers/dados sob
// o lock da instalação), nunca por 'status'/'start'/'acknowledge-recovery' (rápidas,
// read-only ou de mutação trivial do ledger, sem subprocessos privilegiados em andamento).
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
}

func runAcknowledgeRecovery(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("acknowledge-recovery", flag.ContinueOnError)
	cfg := agentconfig.FlagSet(fs)
	runID := fs.String("run", "", "ID do run em RECOVERY_REQUIRED a reconhecer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.InstallationID == "" {
		return fmt.Errorf("acknowledge-recovery: --installation-id (ou OMNIRA_AGENT_INSTALLATION_ID) é obrigatório")
	}
	if *runID == "" {
		return fmt.Errorf("acknowledge-recovery: --run é obrigatório")
	}
	// Mesmo achado Codex P2 de runStart: acknowledge-recovery chama AcquireLock sem passar
	// por Build()/Validate() completo.
	if err := cfg.ValidateLease(); err != nil {
		return fmt.Errorf("acknowledge-recovery: %w", err)
	}
	ledger, err := agent.OpenLedger(cfg.LedgerPath)
	if err != nil {
		return fmt.Errorf("acknowledge-recovery: %w", err)
	}
	defer ledger.Close()
	ctx := context.Background()
	h, err := ledger.AcquireLock(ctx, cfg.InstallationID, cfg.Holder, cfg.Lease)
	if err != nil {
		return fmt.Errorf("acknowledge-recovery: adquirir lock: %w", err)
	}
	defer ledger.ReleaseLock(context.Background(), h)
	if err := ledger.AcknowledgeRecovery(ctx, h, *runID); err != nil {
		return fmt.Errorf("acknowledge-recovery: %w", err)
	}
	fmt.Fprintf(out, "run %s reconhecido — novos runs desta instalação podem prosseguir\n", *runID)
	return nil
}

// runRollback força a CONCLUSÃO manual de uma decisão de recuperação travada — nunca uma
// ferramenta genérica de "reinstalar qualquer snapshot escolhido". Achados Codex P1 de uma
// versão anterior deste comando (que chamava Compose.Rollback diretamente): (1) nunca lia o
// manifesto nem verificava database_strategy/recovery_class, permitindo um downgrade
// inseguro para uma release R3/R4 ou com schema incompatível; (2) sem heartbeat durante a
// operação, o daemon podia assumir a instalação e reconciliar CONCORRENTEMENTE enquanto este
// comando ainda mutava containers; (3) nunca atualizava o estado/journal do run — a próxima
// reconciliação automática retomaria o MESMO run a partir do estado congelado, podendo
// reimplantar a release indesejada ou declarar SUCCEEDED com as imagens antigas rodando; (4)
// nunca derivava as units do Rollout.Workloads da release, podendo tocar workloads não
// relacionados ou excluir silenciosamente um que devia ter sido revertido. Delegar para
// Executor.Execute (que despacha para recover()) corrige todos os quatro de uma vez: é a
// MESMA lógica, já testada e revisada, que o daemon usaria automaticamente — só que acionada
// manualmente, para o caso real que a especificação §8 descreve ("API indisponível"), onde o
// daemon não está rodando ou não consegue decidir.
func runRollback(args []string, out io.Writer, in io.Reader) error {
	fs := flag.NewFlagSet("rollback", flag.ContinueOnError)
	cfg := agentconfig.FlagSet(fs)
	runID := fs.String("run", "", "ID do run em FAILED/RECOVERING cuja decisão forçar (default: o run mais recente desta instalação)")
	yes := fs.Bool("yes", false, "aplicar sem pedir confirmação interativa")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.InstallationID == "" {
		return fmt.Errorf("rollback: --installation-id (ou OMNIRA_AGENT_INSTALLATION_ID) é obrigatório")
	}
	exec, ledger, err := cfg.Build()
	if err != nil {
		return fmt.Errorf("rollback: %w", err)
	}
	defer ledger.Close()
	// Mesmo achado Codex P1 de runReconcile: cancela killOnCancel/subprocessos privilegiados
	// e para o heartbeat de forma limpa se o operador interromper o rollback no meio.
	ctx, stop := signalContext()
	defer stop()

	run, err := resolveRollbackTarget(ctx, ledger, cfg.InstallationID, *runID)
	if err != nil {
		return fmt.Errorf("rollback: %w", err)
	}
	if run.State != domain.StateFailed && run.State != domain.StateRecovering {
		return fmt.Errorf("rollback: run %s está em %s — 'rollback' só força a conclusão de uma decisão de recuperação travada (FAILED ou RECOVERING); para um run já SUCCEEDED/ROLLED_BACK, não há nada pendente, e um RECOVERY_REQUIRED precisa de 'acknowledge-recovery' seguido de uma nova release explícita de downgrade (nunca um rollback automático de dados)", run.RunID, run.State)
	}
	manifest, err := domain.ParseManifest([]byte(run.ManifestJSON))
	if err != nil {
		return fmt.Errorf("rollback: manifesto do run %s não valida: %w", run.RunID, err)
	}

	// Mostra ao operador o que SERIA reinstalado antes de pedir confirmação — puramente
	// informativo; a decisão de segurança real (compatibilidade, units do rollout, health
	// check pós-rollback) é feita por Execute/recover, nunca por esta exibição.
	cps, err := ledger.Checkpoints(ctx, run.RunID)
	if err != nil {
		return fmt.Errorf("rollback: checkpoints de %s: %w", run.RunID, err)
	}
	if snap, err := agent.SnapshotFromCheckpoints(cps); err == nil && snap != nil {
		fmt.Fprintf(out, "forçando a decisão de recuperação do run %s (snapshot de PREPARING, release %s):\n", run.RunID, manifest.ReleaseID)
		for _, u := range snapshotUnits(snap) {
			fmt.Fprintf(out, "  %s -> %s\n", u, snap.Digests[u])
		}
	} else {
		fmt.Fprintf(out, "forçando a decisão de recuperação do run %s (release %s) — sem snapshot de PREPARING, a decisão provável é RECOVERY_REQUIRED\n", run.RunID, manifest.ReleaseID)
	}
	if !*yes && !confirm(out, in) {
		fmt.Fprintln(out, "rollback cancelado")
		return nil
	}

	err = exec.WithLockAndHeartbeat(ctx, cfg.InstallationID, cfg.Holder, cfg.Lease, func(execCtx context.Context, h *agent.LockHandle) error {
		return exec.Execute(execCtx, h, run.RunID, manifest)
	})
	if err != nil {
		return fmt.Errorf("rollback: %w", err)
	}

	final, err := ledger.GetRun(ctx, run.RunID)
	if err != nil {
		return fmt.Errorf("rollback: ler estado final do run %s: %w", run.RunID, err)
	}
	fmt.Fprintf(out, "run %s resolvido: %s\n", run.RunID, final.State)
	if final.State == domain.StateRolledBack {
		fmt.Fprintln(out, "rollback aplicado e confirmado saudável")
	} else if final.State == domain.StateRecoveryRequired {
		fmt.Fprintln(out, "RECOVERY_REQUIRED: rollback automático não é seguro para este run — preservar evidências e seguir o runbook; use 'acknowledge-recovery' depois de resolver manualmente")
	}
	return nil
}

// confirm pede "s/N" em `out` e lê uma linha de `in` — nunca por fmt.Scanln contra os.Stdin
// fixo, para que runRollback seja testável com um buffer no lugar de um terminal real.
func confirm(out io.Writer, in io.Reader) bool {
	fmt.Fprint(out, "\naplicar este rollback agora? [s/N] ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	resp := strings.ToLower(strings.TrimSpace(line))
	return resp == "s" || resp == "sim" || resp == "y" || resp == "yes"
}

// resolveRollbackTarget devolve o run indicado, ou — se nenhum --run foi dado — o run MAIS
// RECENTE desta instalação (AllRuns já ordena por created_at ASC).
func resolveRollbackTarget(ctx context.Context, ledger *agent.Ledger, installationID, runID string) (*agent.RunRecord, error) {
	if runID != "" {
		run, err := ledger.GetRun(ctx, runID)
		if err != nil {
			return nil, err
		}
		// Achado Codex P1: um --run explícito nunca pode pertencer a OUTRA instalação — sem
		// isto, "--installation-id A --run <run-de-B>" adquiria o lock de A mas aplicaria o
		// snapshot de B no projeto Compose configurado para A. Checado aqui (a ÚNICA forma
		// de resolver um run alvo), nunca deixado para o chamador lembrar de verificar.
		if run.InstallationID != installationID {
			return nil, fmt.Errorf("run %s pertence à instalação %s, não a %s", run.RunID, run.InstallationID, installationID)
		}
		return run, nil
	}
	runs, err := ledger.AllRuns(ctx, installationID)
	if err != nil {
		return nil, fmt.Errorf("listar runs: %w", err)
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("nenhum run registrado para a instalação %s", installationID)
	}
	return &runs[len(runs)-1], nil
}

// snapshotUnits devolve as units presentes em snap.Digests, em ordem determinística (um map
// nunca itera na mesma ordem duas vezes) — nunca inclui uma unit com digest "" (sem valor
// utilizável, a mesma regra que ComposeAdapter.Rollback já aplica internamente; filtrar aqui
// só evita listar ao operador uma unit que de qualquer forma seria recusada).
func snapshotUnits(snap *agent.Snapshot) []agent.Unit {
	units := make([]agent.Unit, 0, len(snap.Digests))
	for u, ref := range snap.Digests {
		if ref != "" {
			units = append(units, u)
		}
	}
	sort.Slice(units, func(i, j int) bool { return units[i] < units[j] })
	return units
}
