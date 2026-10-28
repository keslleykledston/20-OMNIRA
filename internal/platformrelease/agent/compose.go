// compose.go: o Adapter Docker Compose do Update Agent (F5, especificação §2.2/§5/§7.2;
// ADR-0054 §2.2.3 "Acesso a Docker é equivalente a root — fica restrito... a um conjunto fixo
// de comandos (docker compose pull/up/ps), nunca comando arbitrário vindo do manifesto").
//
// Nenhum campo do manifesto é interpolado numa string de shell neste arquivo — toda chamada ao
// binário `docker` usa exec.Command/CommandContext com argumentos em slice (nunca `sh -c`), e
// toda referência de imagem é validada contra um formato estrito (repositório@sha256:...) antes
// de alcançar qualquer exec.Command. Os únicos subcomandos que este adapter executa são:
// `docker pull`, `docker inspect`, `docker compose config/pull/up/ps`.
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// imageRefPattern exige SEMPRE um digest sha256 — nunca uma tag móvel (especificação §7.2
// "Baixar imagens... validar... digest"; ADR-0054 §2.2.4 "nunca tag móvel"). Repositório no
// formato usual de registry OCI: host[:porta]/caminho/repo (letras minúsculas, dígitos, '.',
// '-', '_', '/'), sem espaço, sem caracteres de shell.
var imageRefPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]|/[a-z0-9])*(:[0-9]+)?(/[a-z0-9._-]+)*@sha256:[a-f0-9]{64}$`)

// ErrInvalidImageRef é retornado por qualquer operação que receba uma referência de imagem
// fora do formato `repo@sha256:...` — nunca executado, nunca "corrigido" silenciosamente.
var ErrInvalidImageRef = fmt.Errorf("agent: referência de imagem inválida — exige repo@sha256:<64 hex>, nunca uma tag")

func validateImageRef(ref string) error {
	if !imageRefPattern.MatchString(ref) {
		return fmt.Errorf("%w: %q", ErrInvalidImageRef, ref)
	}
	return nil
}

// Unit é um workload real (ver release/components.yaml deploy_units) — fechado à lista que o
// compose.yml base de fato declara. Nunca um nome arbitrário vindo de um manifesto.
type Unit string

const (
	UnitAPI    Unit = "api"
	UnitWorker Unit = "worker"
	UnitWeb    Unit = "web"
)

func (u Unit) valid() bool {
	switch u {
	case UnitAPI, UnitWorker, UnitWeb:
		return true
	default:
		return false
	}
}

// AllUnits na ordem segura de deploy determinada pelas dependências reais do compose.yml
// (migrate -> api -> worker -> web): api e worker dependem só do banco já migrado; web não
// depende de api/worker no nível de container (nginx/proxy decide o roteamento), mas sobe por
// último por ser o que o usuário final toca primeiro, então deve estar saudável.
var AllUnits = []Unit{UnitAPI, UnitWorker, UnitWeb}

// runner executa os poucos comandos docker fixos que este adapter conhece. Uma interface (não
// uma func solta) para que os testes de fault injection possam substituir por um dublê que
// simula falhas de infraestrutura (registry fora do ar, daemon lento) sem precisar provocar
// isso de verdade no host a cada execução de teste — o caminho feliz completo é sempre
// exercitado contra Docker real (ver compose_test.go).
type runner interface {
	run(ctx context.Context, name string, args ...string) (stdout, stderr string, err error)
}

// execRunner chama o binário real, nunca uma string de shell montada por concatenação.
// cmdPrefix permite que os testes rodem como root (sudo -n), nunca usado para injetar nada
// vindo de um manifesto — é uma configuração fixa do adapter, não um dado externo.
type execRunner struct {
	cmdPrefix []string
}

func (r execRunner) run(ctx context.Context, name string, args ...string) (string, string, error) {
	full := append(append([]string{}, r.cmdPrefix...), name)
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// ComposeAdapter orquestra pull/apply/health/rollback para a topologia Docker Compose real
// (ADR-0054 §2.3). BaseFiles é a lista ORDENADA de arquivos compose desta instalação — NUNCA
// só "docker-compose.yml": este host real também tem um docker-compose.override.yml
// machine-local (porta da API, mount de mídia, extra_hosts) que o Docker Compose carrega
// AUTOMATICAMENTE quando nenhum `-f` é passado, mas que desaparece silenciosamente se este
// adapter especificasse só um `-f` próprio sem incluí-lo (achado Codex: aplicar uma release
// restauraria a porta/volumes padrão, quebrando o roteamento e os anexos já existentes no
// disco real). BaseFiles é sempre a lista REAL que a instalação usa, nunca inventada por este
// adapter; o override gerado por release é sempre APENSADO ao final (que vence em caso de
// conflito de chave, pela ordem de precedência do próprio Compose), nunca editado nos arquivos
// alheios (especificação §7.2 "não editar arquivos alheios em lugar não controlado").
// OverrideDir é onde este adapter escreve seus próprios overrides gerados, um por release.
// Project é o nome do projeto Docker Compose (`-p`) — em testes, SEMPRE um nome isolado, nunca
// o da stack real (ADR-0054 §2.2 item 7 "nome de projeto Docker, rede e volumes próprios").
type ComposeAdapter struct {
	BaseFiles   []string
	OverrideDir string
	Project     string
	r           runner
}

// composeFileArgs monta a sequência de `-f <arquivo>` na ORDEM correta: todos os BaseFiles da
// instalação primeiro (na ordem dada), o override gerado por release sempre por último.
func (c *ComposeAdapter) composeFileArgs(overridePath string) []string {
	args := make([]string, 0, 2*(len(c.BaseFiles)+1))
	for _, f := range c.BaseFiles {
		args = append(args, "-f", f)
	}
	return append(args, "-f", overridePath)
}

// NewComposeAdapter cria o adapter. baseFiles é a lista ordenada de arquivos compose reais
// desta instalação (ex. ["docker-compose.yml", "docker-compose.override.yml"]) — nunca um só
// arquivo presumido. sudo controla se os comandos docker são prefixados com `sudo -n` — usado
// pelos testes deste pacote neste host, onde o usuário da sessão não está no grupo docker; em
// produção, o processo do agente normalmente já roda com permissão própria (grupo docker ou
// systemd com AmbientCapabilities), sem precisar de sudo.
func NewComposeAdapter(baseFiles []string, overrideDir, project string, sudo bool) *ComposeAdapter {
	prefix := []string{}
	if sudo {
		prefix = []string{"sudo", "-n"}
	}
	return &ComposeAdapter{BaseFiles: baseFiles, OverrideDir: overrideDir, Project: project, r: execRunner{cmdPrefix: prefix}}
}

// Snapshot é o estado dos 3 workloads ANTES de qualquer mutação — a única coisa que Rollback
// usa para voltar atrás (especificação §7.2 "manter manifesto e backups/referências da versão
// anterior").
type Snapshot struct {
	Digests map[Unit]string // unit -> "repo@sha256:..." atualmente em uso, "" se nunca aplicado
}

// digestInspectFormat usa `docker inspect --format` com um template FIXO, nunca construído a
// partir de dados externos — só {{.Config.Image}} e {{range...}} literais deste arquivo.
const digestInspectFormat = `{{.Config.Image}}`

// resolveContainerID encontra o ID do container REAL de uma unit pelos labels que o Docker
// Compose sempre aplica (com.docker.compose.project/service) — nunca por um padrão de nome
// adivinhado. Achado Codex: a topologia real (docker-compose.yml) usa `container_name:` fixo
// (omnira-api, não <project>-api-1, que é só o padrão default do Compose quando NENHUM nome é
// dado); um ambiente de teste sem container_name mascarava essa incompatibilidade. `docker ps`
// comum (não `docker compose ps`) não precisa resolver nenhum arquivo YAML — só folha pelos
// labels de containers que já existem, então funciona mesmo quando a base de teste não tem
// image/build definido (docker compose ps -f <base sem image> falharia de outra forma).
// Retorna ("", nil) se nenhum container com esses labels existir (nunca implantado ainda) — um
// erro de comando aqui (daemon indisponível, permissão negada) é sempre propagado, nunca
// silenciosamente tratado como "não existe".
func (c *ComposeAdapter) resolveContainerID(ctx context.Context, unit Unit) (string, error) {
	stdout, stderr, err := c.r.run(ctx, "docker", "ps", "-a",
		"--filter", fmt.Sprintf("label=com.docker.compose.project=%s", c.Project),
		"--filter", fmt.Sprintf("label=com.docker.compose.service=%s", unit),
		// Achado Codex: `docker compose run api ...` (ex. um operador investigando algo
		// manualmente, container esquecido sem --rm) carrega os MESMOS labels de
		// project/service que o container de deploy real, só com oneoff=True. Sem excluir
		// isso, CurrentSnapshot/WaitHealthy podiam escolher esse container avulso em vez do
		// workload de verdade.
		"--filter", "label=com.docker.compose.oneoff=False",
		"--format", "{{.ID}}",
	)
	if err != nil {
		return "", fmt.Errorf("agent: resolver container de %s via docker ps: %w (%s)", unit, err, stderr)
	}
	ids := strings.Fields(stdout)
	if len(ids) == 0 {
		return "", nil // nunca implantado — não é um erro
	}
	return ids[0], nil
}

// CurrentSnapshot inspeciona os containers REALMENTE rodando (docker inspect), não um arquivo
// de override que pode estar desatualizado — a fonte de verdade de "o que está em produção
// agora" é o container em si (especificação §7.1 item 4 "versão instalada real detectada via
// image digest... não apenas um campo manual").
func (c *ComposeAdapter) CurrentSnapshot(ctx context.Context) (*Snapshot, error) {
	snap := &Snapshot{Digests: make(map[Unit]string)}
	for _, u := range AllUnits {
		id, err := c.resolveContainerID(ctx, u)
		if err != nil {
			// Achado Codex: um erro de INFRAESTRUTURA (daemon fora do ar, permissão negada,
			// contexto cancelado) nunca pode ser confundido com "nunca implantado" — isso
			// deixaria o chamador seguir adiante achando que tem um snapshot usável para
			// rollback quando na verdade não conseguiu nem ler o estado real.
			return nil, fmt.Errorf("agent: snapshot de %s: %w", u, err)
		}
		if id == "" {
			snap.Digests[u] = ""
			continue
		}
		stdout, _, err := c.r.run(ctx, "docker", "inspect", "--format", digestInspectFormat, id)
		if err != nil {
			// O ID existia agora mesmo (acabamos de resolvê-lo) — se o inspect falha mesmo
			// assim, é uma corrida rara (removido entre as duas chamadas) ou um erro real do
			// daemon, nunca "nunca implantado".
			return nil, fmt.Errorf("agent: inspecionar container de %s (%s): %w", u, id, err)
		}
		ref := trimNewline(stdout)
		// Achado Codex: um container que já existia ANTES do agente gerenciá-lo (criado pelo
		// docker-compose.yml real, que usa `build:` local) tem `.Config.Image` igual a um
		// nome de imagem buildada localmente, nunca um `repo@sha256:...` — WriteOverride
		// rejeitaria isso na hora de um Rollback de verdade. Aceitar silenciosamente essa
		// referência como se fosse um snapshot "utilizável" deixaria o PRIMEIRO upgrade feito
		// pelo agente sem rollback real possível se o deploy falhasse. Em vez de inventar uma
		// referência imutável que não existe, isto fica explicitamente "sem digest anterior
		// UTILIZÁVEL" (igual a "nunca implantado por este agente ainda") — o chamador
		// (reconcile.go) decide se aceita seguir sem rollback de código disponível para esta
		// unit (ex. primeira adoção da topologia).
		if err := validateImageRef(ref); err != nil {
			snap.Digests[u] = ""
			continue
		}
		snap.Digests[u] = ref
	}
	return snap, nil
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// PullByDigest baixa a imagem por digest — a ÚNICA operação de rede que o adapter executa, e
// sempre a mesma forma fixa de comando (`docker pull <ref>`), nunca um argumento adicional
// vindo do manifesto.
func (c *ComposeAdapter) PullByDigest(ctx context.Context, ref string) error {
	if err := validateImageRef(ref); err != nil {
		return err
	}
	_, stderr, err := c.r.run(ctx, "docker", "pull", ref)
	if err != nil {
		return fmt.Errorf("agent: docker pull %s: %w (%s)", ref, err, stderr)
	}
	return nil
}

// overrideTemplate é o ÚNICO template de override que este adapter escreve — gerado inteiro
// por este código a partir de valores já validados (validateImageRef), nunca lido de um
// arquivo fornecido pelo manifesto (especificação §7.2 "arquivos compostos gerados de template
// testado com digests, não editar arquivos alheios").
type overrideSpec struct {
	Services map[string]overrideService `json:"services"`
}
type overrideService struct {
	Image string `json:"image"`
}

// WriteOverride grava (sempre no mesmo caminho, sobrescrevendo) o override desta release — um
// arquivo JSON válido também é YAML válido para o Docker Compose, e evita qualquer ambiguidade
// de indentação na geração.
func (c *ComposeAdapter) WriteOverride(digests map[Unit]string) (string, error) {
	spec := overrideSpec{Services: map[string]overrideService{}}
	for u, ref := range digests {
		if !u.valid() {
			return "", fmt.Errorf("agent: unit desconhecida %q", u)
		}
		if err := validateImageRef(ref); err != nil {
			return "", err
		}
		spec.Services[string(u)] = overrideService{Image: ref}
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("agent: gerar override: %w", err)
	}
	if err := os.MkdirAll(c.OverrideDir, 0o755); err != nil {
		return "", fmt.Errorf("agent: criar diretório de override: %w", err)
	}
	// Nome de arquivo pelo HASH do conteúdo, nunca um caminho fixo (achado Codex): um caminho
	// fixo fazia toda chamada de WriteOverride SOBRESCREVER o override de uma chamada
	// anterior ainda em uso — gerar o override da release B (até só para DryRun/inspeção)
	// mudava silenciosamente o que `Apply(ctx, pathDeA, units)` de fato aplicaria, e um
	// rollback PARCIAL (só algumas units) substituía o override completo de uma release por
	// um que não menciona as units restantes. Determinístico pelo conteúdo: as mesmas
	// referências de imagem sempre produzem o MESMO arquivo (idempotente), mas conjuntos
	// diferentes nunca colidem nem se sobrescrevem.
	sum := sha256.Sum256(data)
	path := fmt.Sprintf("%s/release-override-%x.yml", c.OverrideDir, sum[:8])
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("agent: escrever override: %w", err)
	}
	return path, nil
}

// DryRun gera o override e devolve o YAML resolvido pelo próprio `docker compose config`
// (base + override), para auditoria — nunca aplica nada.
func (c *ComposeAdapter) DryRun(ctx context.Context, digests map[Unit]string) (string, error) {
	overridePath, err := c.WriteOverride(digests)
	if err != nil {
		return "", err
	}
	// `--images` (nunca `config` puro): achado Codex P1 — contra a instalação real, `docker
	// compose config` INTERPOLA todas as variáveis de ambiente e devolve o YAML resolvido
	// completo, incluindo credenciais reais (OMNIRA_DATABASE_URL com a senha do banco,
	// OMNIRA_SESSION_CTX_KEY, OMNIRA_KEYCLOAK_PROVISIONER_SECRET). Um "dry-run para auditoria"
	// nunca pode devolver isso em texto plano. `--images` imprime só o nome de serviço + a
	// imagem resolvida para ele, uma por linha — exatamente a informação que uma auditoria de
	// release precisa ("quais digests vão para quais serviços"), nada mais.
	args := append([]string{"compose", "-p", c.Project}, c.composeFileArgs(overridePath)...)
	args = append(args, "config", "--images")
	stdout, stderr, err := c.r.run(ctx, "docker", args...)
	if err != nil {
		return "", fmt.Errorf("agent: docker compose config --images (dry-run): %w (%s)", err, stderr)
	}
	return stdout, nil
}

// Apply sobe (ou atualiza) as units dadas, pela imagem já baixada por digest. `--no-deps`:
// nunca recria serviços de infraestrutura (postgres/nats/valkey) como efeito colateral de
// atualizar um workload de aplicação.
func (c *ComposeAdapter) Apply(ctx context.Context, overridePath string, units []Unit) error {
	// Achado Codex: `docker compose up -d --no-deps` SEM nenhum nome de serviço no final não
	// significa "nenhuma unit" — o Compose interpreta isso como TODOS os serviços habilitados,
	// o que recriaria infraestrutura/workloads não relacionados. Uma lista vazia tem que ser
	// um erro explícito, nunca silenciosamente "a stack inteira".
	if len(units) == 0 {
		return fmt.Errorf("agent: Apply chamado sem nenhuma unit — recusando (uma lista vazia aplicaria TODOS os serviços do compose, nunca o pretendido)")
	}
	args := append([]string{"compose", "-p", c.Project}, c.composeFileArgs(overridePath)...)
	args = append(args, "up", "-d", "--no-deps")
	for _, u := range units {
		if !u.valid() {
			return fmt.Errorf("agent: unit desconhecida %q", u)
		}
		args = append(args, string(u))
	}
	_, stderr, err := c.r.run(ctx, "docker", args...)
	if err != nil {
		return fmt.Errorf("agent: docker compose up: %w (%s)", err, stderr)
	}
	return nil
}

// WaitHealthy espera até timeout que o container de `unit` reporte healthy via o healthcheck
// já definido no compose.yml (nunca um healthcheck inventado por este adapter — usa o que a
// imagem/serviço já declara).
func (c *ComposeAdapter) WaitHealthy(ctx context.Context, unit Unit, timeout time.Duration) error {
	if !unit.valid() {
		return fmt.Errorf("agent: unit desconhecida %q", unit)
	}
	// Achado Codex: `timeout` só controlava QUANDO desistir de tentar de novo — cada chamada
	// individual a `docker ps`/`docker inspect` ainda usava o ctx do CHAMADOR (com
	// frequência context.Background(), sem prazo nenhum). Se o daemon Docker travasse durante
	// uma dessas chamadas, o subprocesso ficaria pendurado indefinidamente, nunca respeitando
	// o timeout pedido. Derivar um context com este MESMO timeout e usá-lo em toda chamada do
	// loop garante que nenhum subprocesso individual possa travar por mais tempo do que o
	// prazo total já pedido.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline := time.Now().Add(timeout)
	for {
		// Resolvido a CADA iteração (não só uma vez no início): logo após `Apply`, o
		// container antigo pode ainda estar sendo substituído pelo novo (recreate), então o
		// ID correto só existe de forma estável depois de `up -d` terminar de verdade.
		id, rerr := c.resolveContainerID(ctx, unit)
		var status string
		var running bool
		var err error
		if rerr == nil && id != "" {
			// Um único `docker inspect` com os dois campos — Docker PRESERVA o último
			// `.State.Health.Status` mesmo depois que o container parou (achado Codex: só
			// checar health status aceitava um container "healthy" que na verdade já tinha
			// parado/saído, reportando sucesso para um workload indisponível). `.State.Running`
			// precisa ser verdadeiro também.
			var stdout string
			stdout, _, err = c.r.run(ctx, "docker", "inspect", "--format", "{{.State.Running}}|{{.State.Health.Status}}", id)
			parts := strings.SplitN(trimNewline(stdout), "|", 2)
			if len(parts) == 2 {
				running = parts[0] == "true"
				status = parts[1]
			}
		} else {
			err = rerr
		}
		if err == nil && running && status == "healthy" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("agent: %s não ficou healthy em %s (último status: %q)", unit, timeout, status)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("agent: %s não ficou healthy em %s (contexto encerrado: %w)", unit, timeout, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Rollback reaplica um snapshot anterior — SÓ workloads/config, NUNCA dados (especificação §8
// "suportar rollback dos digests/arquivos de configuração somente quando DB compatível"). A
// checagem de compatibilidade de banco é responsabilidade do CHAMADOR (reconcile.go: consulta
// tools/migrate-sql.sh antes de decidir que rollback é seguro) — este método nunca decide isso
// por conta própria, só executa o reaplicar de imagens já aprovado.
func (c *ComposeAdapter) Rollback(ctx context.Context, snap *Snapshot, units []Unit) error {
	digests := make(map[Unit]string)
	for _, u := range units {
		ref := snap.Digests[u]
		if ref == "" {
			return fmt.Errorf("agent: rollback de %s sem digest anterior conhecido no snapshot — nada para reaplicar", u)
		}
		digests[u] = ref
	}
	overridePath, err := c.WriteOverride(digests)
	if err != nil {
		return err
	}
	return c.Apply(ctx, overridePath, units)
}
