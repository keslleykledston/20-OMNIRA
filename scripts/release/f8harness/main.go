// omnira-f8harness — binário SOMENTE de staging/teste para a fase F8 ("Staging e testes
// realistas de atualização/falha"). NUNCA buildado/empacotado para produção, nunca referenciado
// por nenhum outro cmd real. Existe para exercitar agent.Executor/agent.Ledger/ComposeAdapter
// REAIS (os mesmos do F5, nunca reimplementados) com manifestos que apontam para um registry
// OCI local descartável — domain.ParseManifest exige literalmente "ghcr.io/..." em
// artifacts[].image (internal/platformrelease/domain/manifest.go:88), um controle de segurança
// deliberado e correto em produção, verificado em QUALQUER entrada sancionada (API, updatectl
// start, updatectl reconcile via ReconcileOnce) — mas que torna impossível testar um PULL REAL
// BEM-SUCEDIDO contra um registry que esta sessão controla sem credenciais de publish no GHCR
// real do projeto (BLOCKED_EXTERNAL) ou alterar DNS/daemon Docker do host (proibido: afetaria a
// stack real). Este harness desserializa o manifesto via json.Unmarshal DIRETO em
// *domain.ReleaseManifest (nunca via domain.ParseManifest), construindo o Executor pela MESMA
// agentconfig.Build() que omnira-update-agent/omnira-updatectl usam — nenhuma lógica de
// reconciliação/compose/migração é reimplementada aqui.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/omnira/omnira/internal/platformrelease/agent"
	"github.com/omnira/omnira/internal/platformrelease/agentconfig"
	"github.com/omnira/omnira/internal/platformrelease/domain"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("uso: f8harness <create-run|reconcile> [flags]")
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "create-run":
		err = runCreateRun(args)
	case "reconcile":
		err = runReconcile(args)
	case "status":
		err = runStatus(args)
	default:
		err = fmt.Errorf("subcomando desconhecido: %s", cmd)
	}
	if err != nil {
		log.Fatalf("f8harness %s: %v", cmd, err)
	}
}

func runCreateRun(args []string) error {
	fs := flag.NewFlagSet("create-run", flag.ExitOnError)
	cfg := agentconfig.FlagSet(fs)
	manifestPath := fs.String("manifest", "", "caminho do manifesto JSON (não validado por domain.ParseManifest — ver comentário do pacote)")
	idemKey := fs.String("idempotency-key", "", "idempotency key do run (default: gerado)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("ler manifesto: %w", err)
	}
	var m domain.ReleaseManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("decodificar manifesto: %w", err)
	}
	ledger, err := agent.OpenLedger(cfg.LedgerPath)
	if err != nil {
		return err
	}
	defer ledger.Close()
	ctx := context.Background()
	h, err := ledger.AcquireLock(ctx, cfg.InstallationID, cfg.Holder, cfg.Lease)
	if err != nil {
		return fmt.Errorf("adquirir lock: %w", err)
	}
	defer ledger.ReleaseLock(context.Background(), h)
	key := *idemKey
	if key == "" {
		key = m.ReleaseID
	}
	run, err := ledger.CreateRun(ctx, h, fmt.Sprintf("f8-%s-%d", m.ReleaseID, time.Now().UnixNano()), key, string(raw), domain.StateLocked)
	if err != nil {
		return fmt.Errorf("criar run: %w", err)
	}
	fmt.Printf("run_id=%s state=%s\n", run.RunID, run.State)
	return nil
}

func runStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	cfg := agentconfig.FlagSet(fs)
	runID := fs.String("run", "", "run a detalhar")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ledger, err := agent.OpenLedger(cfg.LedgerPath)
	if err != nil {
		return err
	}
	defer ledger.Close()
	ctx := context.Background()
	run, err := ledger.GetRun(ctx, *runID)
	if err != nil {
		return err
	}
	fmt.Printf("state=%s\n", run.State)
	cps, err := ledger.Checkpoints(ctx, *runID)
	if err != nil {
		return err
	}
	for _, c := range cps {
		fmt.Printf("checkpoint %s %s %s %s\n", c.RecordedAt.Format(time.RFC3339), c.Step, c.Status, c.Detail)
	}
	return nil
}

func runReconcile(args []string) error {
	fs := flag.NewFlagSet("reconcile", flag.ExitOnError)
	cfg := agentconfig.FlagSet(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := cfg.RequireDatabaseURL(); err != nil {
		return err
	}
	exec, ledger, err := cfg.Build()
	if err != nil {
		return err
	}
	defer ledger.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	runs, err := ledger.ActiveRuns(ctx, cfg.InstallationID)
	if err != nil {
		return fmt.Errorf("listar runs ativos: %w", err)
	}
	manifests := make(map[string]*domain.ReleaseManifest, len(runs))
	for _, r := range runs {
		var m domain.ReleaseManifest
		if err := json.Unmarshal([]byte(r.ManifestJSON), &m); err != nil {
			log.Printf("f8harness: manifesto do run %s não decodifica: %v", r.RunID, err)
			continue
		}
		manifests[r.RunID] = &m
	}
	if len(runs) == 0 {
		fmt.Println("nenhum run ativo")
		return nil
	}
	if err := exec.Reconcile(ctx, cfg.InstallationID, cfg.Holder, cfg.Lease, manifests); err != nil {
		return err
	}
	fmt.Println("reconciliação concluída")
	return nil
}
