package agent

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// requireComposeIntegration é o mesmo padrão de guarda de
// internal/testhelpers.RequireIntegrationDatabase: sem OMNIRA_COMPOSE_INTEGRATION_TEST=1
// explícito, este teste pula de forma limpa — ele precisa de Docker real e de um registry
// efêmero já publicado com duas imagens de teste (ver
// scripts/release/test-update-agent-compose.sh, que prepara tudo isto e então invoca
// `go test` com as variáveis abaixo).
// requireComposeIntegration devolve baseFiles como a lista ORDENADA real desta instalação de
// teste: um docker-compose.base.yml (equivalente ao docker-compose.yml real) seguido de um
// docker-compose.local-override.yml (equivalente ao docker-compose.override.yml machine-local
// que já existe no host de produção real — ver scripts/release/test-update-agent-compose.sh).
// Isto exercita de propósito o achado Codex: especificar `-f` sem incluir o override local
// faria o Compose ignorá-lo silenciosamente.
func requireComposeIntegration(t *testing.T) (v1Ref, v2Ref, project string, baseFiles []string) {
	t.Helper()
	if os.Getenv("OMNIRA_COMPOSE_INTEGRATION_TEST") != "1" {
		t.Skip("OMNIRA_COMPOSE_INTEGRATION_TEST não é 1 — pulando teste de integração do compose adapter (precisa de Docker real)")
	}
	v1Ref = os.Getenv("OMNIRA_COMPOSE_TEST_IMAGE_V1")
	v2Ref = os.Getenv("OMNIRA_COMPOSE_TEST_IMAGE_V2")
	project = os.Getenv("OMNIRA_COMPOSE_TEST_PROJECT")
	baseFilesRaw := os.Getenv("OMNIRA_COMPOSE_TEST_BASE_FILES")
	if v1Ref == "" || v2Ref == "" || project == "" || baseFilesRaw == "" {
		t.Fatal("OMNIRA_COMPOSE_INTEGRATION_TEST=1 mas faltam OMNIRA_COMPOSE_TEST_IMAGE_V1/V2/PROJECT/BASE_FILES")
	}
	baseFiles = strings.Split(baseFilesRaw, ":")
	// Nunca o projeto Docker real — mesma disciplina de ADR-0054 §2.2 item 7.
	if project == "omnira" || strings.HasPrefix(project, "omnira-hl") {
		t.Fatalf("project %q parece ser uma stack real, recusando usá-lo em teste", project)
	}
	return
}

func newTestAdapter(project string, baseFiles []string) *ComposeAdapter {
	// Diretório de override NOMEADO PELO PROJETO (achado Codex): um caminho fixo compartilhado
	// entre execuções concorrentes deste script (CI paralelo, dois desenvolvedores testando ao
	// mesmo tempo) permitia uma rodada sobrescrever o override de outra entre WriteOverride e
	// Apply, ou seu cleanup apagar o override que a outra rodada ainda estava usando.
	return NewComposeAdapter(baseFiles, "/tmp/omnira-compose-adapter-test-override-"+project, project, true)
}

// TestComposeAdapter_FullLifecycle: pull por digest real, apply real, health check real,
// upgrade real (v1 -> v2, verificado pelo próprio healthcheck da imagem que só fica healthy
// na versão certa), e rollback real de volta para v1 — tudo contra Docker de verdade, nunca
// mockado (regra do programa: nunca mockar o que precisa de infraestrutura real).
func TestComposeAdapter_FullLifecycle(t *testing.T) {
	v1, v2, project, baseFiles := requireComposeIntegration(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	a := newTestAdapter(project, baseFiles)

	t.Log("== validação de referência rejeita tag móvel")
	if err := a.PullByDigest(ctx, "nginx:latest"); err == nil {
		t.Fatal("deveria rejeitar uma tag móvel, nunca tentar baixar")
	}

	t.Log("== pull por digest (v1)")
	if err := a.PullByDigest(ctx, v1); err != nil {
		t.Fatalf("pull v1: %v", err)
	}

	t.Log("== DryRun nunca expõe segredos reais da instalação (achado Codex P1)")
	// O override machine-local de teste (docker-compose.local-override.yml, via
	// OMNIRA_COMPOSE_TEST_BASE_FILES) define FAKE_SECRET_SENTINEL, simulando
	// OMNIRA_DATABASE_URL/OMNIRA_SESSION_CTX_KEY do host real. `docker compose config` sem
	// `--images` interpolaria e devolveria isso em texto plano.
	dryRunOut, err := a.DryRun(ctx, map[Unit]string{UnitAPI: v1, UnitWorker: v1, UnitWeb: v1})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if strings.Contains(dryRunOut, "do-not-leak-this-value-12345") || strings.Contains(dryRunOut, "FAKE_SECRET_SENTINEL") {
		t.Fatalf("DryRun expôs um valor sensível — saída: %s", dryRunOut)
	}
	if !strings.Contains(dryRunOut, v1) {
		t.Fatalf("DryRun deveria pelo menos confirmar a imagem resolvida (%s), veio: %s", v1, dryRunOut)
	}

	t.Log("== snapshot ANTES do primeiro deploy (nenhum digest anterior conhecido)")
	snapBefore, err := a.CurrentSnapshot(ctx)
	if err != nil {
		t.Fatalf("CurrentSnapshot: %v", err)
	}
	for _, u := range AllUnits {
		if snapBefore.Digests[u] != "" {
			t.Fatalf("esperava nenhum digest anterior para %s, veio %q", u, snapBefore.Digests[u])
		}
	}

	t.Log("== apply v1 em todas as units")
	overridePath, err := a.WriteOverride(map[Unit]string{UnitAPI: v1, UnitWorker: v1, UnitWeb: v1})
	if err != nil {
		t.Fatalf("WriteOverride: %v", err)
	}
	if err := a.Apply(ctx, overridePath, AllUnits); err != nil {
		t.Fatalf("Apply v1: %v", err)
	}
	for _, u := range AllUnits {
		if err := a.WaitHealthy(ctx, u, 30*time.Second); err != nil {
			t.Fatalf("%s não ficou healthy na v1: %v", u, err)
		}
	}

	t.Log("== o override machine-local (docker-compose.local-override.yml) foi de fato incluído, não ignorado")
	// Achado Codex: Apply/DryRun especificando `-f` sem incluir TODOS os arquivos reais da
	// instalação faria o Compose ignorar silenciosamente um override machine-local já
	// existente (no host real: porta/volumes customizados). requireComposeIntegration monta
	// BaseFiles com dois arquivos de teste — este label só aparece se o segundo (o
	// "local-override") realmente influenciou o container final.
	for _, u := range AllUnits {
		id, err := a.resolveContainerID(ctx, u)
		if err != nil || id == "" {
			t.Fatalf("resolver container de %s para checar o label do override local: %v", u, err)
		}
		out, err := exec.CommandContext(ctx, "sudo", "-n", "docker", "inspect", "--format",
			`{{index .Config.Labels "com.omnira.test-local-override"}}`, id).Output()
		if err != nil {
			t.Fatalf("docker inspect do label de %s: %v", u, err)
		}
		if got := strings.TrimSpace(string(out)); got != "true" {
			t.Fatalf("%s: esperava o label com.omnira.test-local-override=true (prova de que o override local foi incluído), veio %q", u, got)
		}
	}

	t.Log("== snapshot DEPOIS do deploy v1 reflete os digests reais aplicados")
	snapV1, err := a.CurrentSnapshot(ctx)
	if err != nil {
		t.Fatalf("CurrentSnapshot pós-v1: %v", err)
	}
	for _, u := range AllUnits {
		if snapV1.Digests[u] != v1 {
			t.Fatalf("%s: esperava snapshot=%s, veio %q", u, v1, snapV1.Digests[u])
		}
	}

	t.Log("== pull + upgrade para v2")
	if err := a.PullByDigest(ctx, v2); err != nil {
		t.Fatalf("pull v2: %v", err)
	}
	overridePathV2, err := a.WriteOverride(map[Unit]string{UnitAPI: v2, UnitWorker: v2, UnitWeb: v2})
	if err != nil {
		t.Fatalf("WriteOverride v2: %v", err)
	}
	if err := a.Apply(ctx, overridePathV2, AllUnits); err != nil {
		t.Fatalf("Apply v2: %v", err)
	}
	for _, u := range AllUnits {
		// O healthcheck da própria imagem só reporta "healthy" se o conteúdo /healthz bater
		// com a versão esperada — isto prova que o container realmente trocou de imagem, não
		// apenas que o comando `up` retornou sem erro.
		if err := a.WaitHealthy(ctx, u, 30*time.Second); err != nil {
			t.Fatalf("%s não ficou healthy na v2: %v", u, err)
		}
	}

	t.Log("== rollback para o snapshot v1 (código/config apenas, nunca dados)")
	if err := a.Rollback(ctx, snapV1, AllUnits); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	for _, u := range AllUnits {
		if err := a.WaitHealthy(ctx, u, 30*time.Second); err != nil {
			t.Fatalf("%s não ficou healthy após rollback: %v", u, err)
		}
	}
	snapAfterRollback, err := a.CurrentSnapshot(ctx)
	if err != nil {
		t.Fatalf("CurrentSnapshot pós-rollback: %v", err)
	}
	for _, u := range AllUnits {
		if snapAfterRollback.Digests[u] != v1 {
			t.Fatalf("%s: rollback deveria ter voltado para %s, veio %q", u, v1, snapAfterRollback.Digests[u])
		}
	}
}

// TestComposeAdapter_RollbackWithoutSnapshot_Refuses: nunca "inventa" um estado anterior —
// se o snapshot não tem um digest conhecido para alguma unit, Rollback recusa explicitamente
// em vez de aplicar algo arbitrário.
func TestComposeAdapter_RollbackWithoutSnapshot_Refuses(t *testing.T) {
	_, _, project, baseFiles := requireComposeIntegration(t)
	a := newTestAdapter(project, baseFiles)
	emptySnap := &Snapshot{Digests: map[Unit]string{}}
	if err := a.Rollback(context.Background(), emptySnap, []Unit{UnitAPI}); err == nil {
		t.Fatal("Rollback sem digest anterior conhecido deveria recusar, nunca aplicar algo arbitrário")
	}
}
