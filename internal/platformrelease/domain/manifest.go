// Package domain contém o núcleo do módulo platformrelease: nenhum import de
// HTTP, pgx ou NATS aqui (regra de dependência de docs/architecture/GO-PROJECT-STRUCTURE.md).
package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
)

// Um manifesto de release inválido nunca deve ser "corrigido" silenciosamente
// pelo parser — cada rejeição carrega o motivo exato, porque é isso que a UI
// de preflight (F7) e os logs de auditoria mostram ao operador. Nunca
// mascarar um campo ausente como zero-value "ok".

// Channel é o canal declarado de uma release. A lista é fechada de propósito:
// um novo canal exige decisão de política (release/policies/channels.yaml),
// não um valor solto aparecendo num manifesto.
type Channel string

const (
	ChannelDev       Channel = "dev"
	ChannelCandidate Channel = "candidate"
	ChannelStable    Channel = "stable"
)

func (c Channel) valid() bool {
	switch c {
	case ChannelDev, ChannelCandidate, ChannelStable:
		return true
	default:
		return false
	}
}

// RecoveryClass espelha as classes R0-R4 do ADR-0054 §2.5. Nunca adicionar um
// valor aqui sem atualizar o ADR primeiro — a UI e os runbooks dependem desta
// lista ser a mesma em todo lugar.
type RecoveryClass string

const (
	RecoveryR0 RecoveryClass = "R0"
	RecoveryR1 RecoveryClass = "R1"
	RecoveryR2 RecoveryClass = "R2"
	RecoveryR3 RecoveryClass = "R3"
	RecoveryR4 RecoveryClass = "R4"
)

func (r RecoveryClass) valid() bool {
	switch r {
	case RecoveryR0, RecoveryR1, RecoveryR2, RecoveryR3, RecoveryR4:
		return true
	default:
		return false
	}
}

// BuildUnit é fechado à lista real de workloads do repositório (ver
// release/components.yaml). Um manifesto referenciando uma unit desconhecida
// é inválido, nunca "ignorado".
type BuildUnit string

const (
	UnitAPI    BuildUnit = "omnira-api"
	UnitWorker BuildUnit = "omnira-worker"
	UnitWeb    BuildUnit = "omnira-web"
)

func (u BuildUnit) valid() bool {
	switch u {
	case UnitAPI, UnitWorker, UnitWeb:
		return true
	default:
		return false
	}
}

var (
	// Full git SHA apenas — um SHA abreviado não identifica univocamente um
	// commit sob colisão de prefixo e não é aceito.
	commitSHARe = regexp.MustCompile(`^[0-9a-f]{40}$`)

	// Sempre por digest OCI. Uma referência "ghcr.io/x/y:latest" ou
	// "ghcr.io/x/y:v2.4.0" (tag móvel) falha este regex de propósito: tag
	// nunca é uma referência de deploy válida, mesmo que pareça imutável.
	imageDigestRe = regexp.MustCompile(`^ghcr\.io/[A-Za-z0-9_./-]+@sha256:[0-9a-f]{64}$`)

	checksumRe  = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	releaseIDRe = regexp.MustCompile(`^omnira-[0-9]+\.[0-9]+\.[0-9]+(-[a-z0-9.-]+)?$`)
	semverRe    = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	repoRe      = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

// Source identifica de onde o build veio: repositório, commit completo e a
// run de CI que o produziu. Os três juntos são o que a cadeia de
// proveniência (F3) assina — nenhum deles sozinho basta.
type Source struct {
	Repository    string `json:"repository"`
	CommitSHA     string `json:"commit_sha"`
	WorkflowRunID string `json:"workflow_run_id"`
}

// Artifact liga um build_unit real a uma imagem OCI por digest.
type Artifact struct {
	Unit  BuildUnit `json:"unit"`
	Image string    `json:"image"`
}

// Compatibility declara, de forma explícita, de quais versões esta release
// aceita fazer upgrade direto — nunca inferido por comparação numérica de
// versão (um salto de duas minor pode não ter sido testado).
type Compatibility struct {
	FromVersions     []string `json:"from_versions"`
	MinAgentProtocol int      `json:"min_agent_protocol"`
	DatabaseStrategy string   `json:"database_strategy"`
}

var validDatabaseStrategies = map[string]bool{
	"expand_then_contract_later": true,
	"expand_only":                true,
	"no_schema_change":           true,
}

// Migrations referencia versões de tools/migrate-sql.sh, nunca de
// `make migrate-up`/`migrate-down` (ver docs/release/00-audit.md §2 — esses
// alvos do Makefile são perigosos e nunca são a fonte de verdade de uma
// release).
type Migrations struct {
	StartAfter     string `json:"start_after"`
	Target         string `json:"target"`
	ChecksumBundle string `json:"checksum_bundle"`
}

// Rollout declara quais workloads mudam e qual classe de recuperação se
// aplica SE o deploy falhar — decidida em tempo de build, não improvisada
// durante o incidente.
type Rollout struct {
	Workloads       []BuildUnit   `json:"workloads"`
	PreflightPolicy string        `json:"preflight_policy"`
	RecoveryClass   RecoveryClass `json:"recovery_class"`
}

// ReleaseManifest é o manifesto completo e imutável de uma release. O
// conteúdo canônico exato deste struct (serializado de volta) é o que a
// assinatura cobre — ver release/policies/signing.yaml; este arquivo não
// verifica a assinatura em si, só a forma e o conteúdo do manifesto.
type ReleaseManifest struct {
	SchemaVersion   int           `json:"schema_version"`
	ReleaseID       string        `json:"release_id"`
	PlatformVersion string        `json:"platform_version"`
	Channel         Channel       `json:"channel"`
	Source          Source        `json:"source"`
	Artifacts       []Artifact    `json:"artifacts"`
	ModulesChanged  []string      `json:"modules_changed"`
	Compatibility   Compatibility `json:"compatibility"`
	Migrations      Migrations    `json:"migrations"`
	Rollout         Rollout       `json:"rollout"`
	ReleaseNotesRef string        `json:"release_notes_ref,omitempty"`
	ApprovedBy      string        `json:"approved_by"`
}

// SupportedSchemaVersion é a única versão de schema que este binário entende.
// Um manifesto com outro valor é rejeitado inteiro — nunca interpretado
// "o melhor possível". Subir este número é uma mudança deliberada, nunca
// incidental.
const SupportedSchemaVersion = 1

// requiredTopLevelFields espelha exatamente o array "required" de
// release/schemas/release-manifest.schema.json. As duas listas precisam
// concordar sempre — um campo adicionado a uma sem o outro é uma
// divergência entre o schema JSON (usado por verificadores externos) e este
// validador Go (usado em runtime), exatamente o tipo de inconsistência que
// a revisão independente (Codex) deste programa aponta como P2.
var requiredTopLevelFields = []string{
	"schema_version", "release_id", "platform_version", "channel", "source",
	"artifacts", "modules_changed", "compatibility", "migrations", "rollout", "approved_by",
}

// rejectDuplicateKeys escaneia o JSON cru, token a token, e rejeita QUALQUER
// objeto (em qualquer profundidade: raiz, "source", cada elemento de
// "artifacts", etc.) que repita a mesma chave duas vezes. Isto existe porque
// decodificar em map[string]json.RawMessage (usado pelas checagens de
// presença/chaves exatas abaixo) e decodificar no struct Go **concordam em
// escondido o problema de formas diferentes**: o map silenciosamente fica só
// com a ÚLTIMA ocorrência; o struct, para uma chave aninhada repetida,
// decodifica a segunda ocorrência POR CIMA do mesmo valor de campo já
// populado pela primeira — campos ausentes na segunda ocorrência do objeto
// mantêm o valor da primeira em vez de serem tratados como ausentes. Um
// manifesto com `"source": {...válido...}` seguido de uma segunda
// `"source": {"workflow_run_id": null}` passava por isso antes desta função
// (achado da revisão independente Codex, F1, quinta rodada) — o schema JSON
// (que não permite propriedade repetida dentro do mesmo objeto, por
// definição de um objeto JSON bem-formado em termos canônicos) rejeitaria,
// mas nem o map nem o struct Go, isolados, pegavam isso de forma confiável.
// Esta função roda ANTES de qualquer outra checagem em ParseManifest.
func rejectDuplicateKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))

	type frame struct {
		isObject  bool
		expectKey bool // só relevante quando isObject
		seen      map[string]bool
	}
	var stack []*frame

	// afterValue é chamado sempre que um valor completo (primitivo, ou um
	// objeto/array que acabou de fechar) termina — se o pai for um objeto,
	// o próximo token nele volta a ser uma chave.
	afterValue := func() {
		if n := len(stack); n > 0 && stack[n-1].isObject {
			stack[n-1].expectKey = true
		}
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("release manifest: json inválido ao escanear chaves duplicadas: %w", err)
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				stack = append(stack, &frame{isObject: true, expectKey: true, seen: map[string]bool{}})
			case '[':
				stack = append(stack, &frame{isObject: false})
			case '}', ']':
				stack = stack[:len(stack)-1]
				afterValue()
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].isObject && stack[n-1].expectKey {
				top := stack[n-1]
				if top.seen[v] {
					return fmt.Errorf("release manifest: chave duplicada %q (manifestos canônicos nunca repetem uma propriedade no mesmo objeto)", v)
				}
				top.seen[v] = true
				top.expectKey = false
				continue // era uma CHAVE, não um valor — não chama afterValue()
			}
			afterValue()
		default:
			// número, bool ou null como valor de uma chave/elemento de array.
			afterValue()
		}
	}
	return nil
}

// allowedNestedKeys declara, por objeto aninhado do manifesto, exatamente
// os nomes de propriedade que release-manifest.schema.json permite — as
// mesmas listas que os "required"/"properties" de cada objeto no schema.
// Mantidas aqui, lado a lado, porque Go's encoding/json faz correspondência
// de campo CASE-INSENSITIVE mesmo com DisallowUnknownFields (um achado da
// revisão independente Codex, F1, terceira rodada): um manifesto com
// "channel" E "CHANNEL" (chaves distintas em JSON puro) nunca é rejeitado
// pelo decode no struct — a segunda silenciosamente sobrescreve o valor do
// mesmo campo Go, enquanto o schema JSON (case-sensitive por definição)
// rejeitaria "CHANNEL" como additionalProperty. A verificação abaixo opera
// sobre um map[string]json.RawMessage, que preserva cada chave exatamente
// como está no JSON — isso sim é case-sensitive, ao contrário do struct.
var (
	allowedTopLevelKeys  = append(append([]string{}, requiredTopLevelFields...), "release_notes_ref")
	allowedSourceKeys    = []string{"repository", "commit_sha", "workflow_run_id"}
	allowedArtifactKeys  = []string{"unit", "image"}
	allowedCompatKeys    = []string{"from_versions", "min_agent_protocol", "database_strategy"}
	allowedMigrationKeys = []string{"start_after", "target", "checksum_bundle"}
	allowedRolloutKeys   = []string{"workloads", "preflight_policy", "recovery_class"}
)

// validateExactKeys rejeita qualquer chave em raw (um objeto JSON) que não
// esteja, byte-a-byte, em allowed. label só identifica o objeto na mensagem
// de erro.
func validateExactKeys(raw json.RawMessage, allowed []string, label string) error {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("release manifest: %s inválido: %w", label, err)
	}
	ok := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		ok[k] = true
	}
	for k := range m {
		if !ok[k] {
			return fmt.Errorf("release manifest: %s tem propriedade desconhecida %q (nomes de campo são case-sensitive)", label, k)
		}
	}
	return nil
}

// rejectNullArrayElements rejeita um array JSON (como veio no manifesto cru,
// antes de qualquer decode em tipos Go) que contenha um elemento `null`
// literal — um campo ausente dentro de uma lista que o schema declara como
// lista de strings. fieldLabel só identifica o campo na mensagem de erro.
func rejectNullArrayElements(raw json.RawMessage, fieldLabel string) error {
	if len(raw) == 0 {
		return nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return fmt.Errorf("release manifest: %s inválido: %w", fieldLabel, err)
	}
	for i, e := range elems {
		if string(e) == "null" {
			return fmt.Errorf("release manifest: %s[%d] não pode ser null", fieldLabel, i)
		}
	}
	return nil
}

// ParseManifest decodifica e valida um manifesto de release. additionalProperties:false
// do schema JSON é refletido aqui via DisallowUnknownFields: um campo extra
// não documentado é um manifesto INVÁLIDO, não um campo ignorado — um
// manifesto adulterado/malicioso frequentemente carrega metadado extra que
// um parser tolerante aceitaria sem perceber.
func ParseManifest(raw []byte) (*ReleaseManifest, error) {
	// Primeiro de tudo: nenhuma chave repetida em nenhum objeto, em nenhuma
	// profundidade — ver comentário de rejectDuplicateKeys acima sobre por
	// que isto precisa vir antes de qualquer decode em map ou struct.
	if err := rejectDuplicateKeys(raw); err != nil {
		return nil, err
	}
	// Primeira passada: um mapa cru para checar PRESENÇA e não-nulidade dos
	// campos exigidos pelo schema. Decodificar direto no struct nunca
	// distingue "campo ausente" de "campo presente com valor zero" (ex.:
	// modules_changed ausente e modules_changed:[] ficam indistinguíveis
	// como nil slice) — um manifesto que omite um campo obrigatório do
	// schema precisa ser rejeitado aqui, não silenciosamente aceito com o
	// zero-value do Go.
	var presence map[string]json.RawMessage
	if err := json.Unmarshal(raw, &presence); err != nil {
		return nil, fmt.Errorf("release manifest: json inválido: %w", err)
	}
	for _, field := range requiredTopLevelFields {
		v, ok := presence[field]
		if !ok {
			return nil, fmt.Errorf("release manifest: campo obrigatório %q ausente", field)
		}
		if string(v) == "null" {
			return nil, fmt.Errorf("release manifest: campo obrigatório %q não pode ser null", field)
		}
	}
	// release_notes_ref é OPCIONAL (pode estar ausente), mas se presente o
	// schema exige que seja string — `null` explícito não é uma string e o
	// decode direto no struct Go o aceitaria silenciosamente como "" (achado
	// da revisão independente Codex, F1, quarta rodada).
	if v, ok := presence["release_notes_ref"]; ok && string(v) == "null" {
		return nil, fmt.Errorf("release manifest: release_notes_ref, quando presente, não pode ser null")
	}
	// modules_changed e compatibility.from_versions são arrays de string no
	// schema JSON ("items": {"type": "string"}); um elemento `null` dentro
	// deles não é uma string vazia — é um tipo errado, e o schema o
	// rejeitaria. O decode direto no struct Go NÃO detecta isso: um `null`
	// dentro de um []string é silenciosamente convertido para "" sem erro
	// (achado da revisão independente Codex, F1, segunda rodada). Checa os
	// elementos crus antes de confiar no struct decodificado.
	if err := rejectNullArrayElements(presence["modules_changed"], "modules_changed"); err != nil {
		return nil, err
	}
	var compat struct {
		FromVersions json.RawMessage `json:"from_versions"`
	}
	if err := json.Unmarshal(presence["compatibility"], &compat); err != nil {
		return nil, fmt.Errorf("release manifest: compatibility inválido: %w", err)
	}
	if err := rejectNullArrayElements(compat.FromVersions, "compatibility.from_versions"); err != nil {
		return nil, err
	}

	// Chaves exatas (case-sensitive) em cada nível — ver comentário de
	// allowedTopLevelKeys acima sobre por que isto não pode depender só de
	// DisallowUnknownFields no decode do struct.
	if err := validateExactKeys(raw, allowedTopLevelKeys, "manifesto"); err != nil {
		return nil, err
	}
	if err := validateExactKeys(presence["source"], allowedSourceKeys, "source"); err != nil {
		return nil, err
	}
	if err := validateExactKeys(presence["compatibility"], allowedCompatKeys, "compatibility"); err != nil {
		return nil, err
	}
	if err := validateExactKeys(presence["migrations"], allowedMigrationKeys, "migrations"); err != nil {
		return nil, err
	}
	if err := validateExactKeys(presence["rollout"], allowedRolloutKeys, "rollout"); err != nil {
		return nil, err
	}
	var rawArtifacts []json.RawMessage
	if err := json.Unmarshal(presence["artifacts"], &rawArtifacts); err != nil {
		return nil, fmt.Errorf("release manifest: artifacts inválido: %w", err)
	}
	for i, a := range rawArtifacts {
		if err := validateExactKeys(a, allowedArtifactKeys, fmt.Sprintf("artifacts[%d]", i)); err != nil {
			return nil, err
		}
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	var m ReleaseManifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("release manifest: json inválido ou campo desconhecido: %w", err)
	}
	// Um segundo valor no stream (manifesto concatenado com lixo depois, OU
	// um delimitador de fechamento solto como "}"/"]") é rejeitado.
	// dec.More() NÃO detecta um delimitador de fechamento solto — ele só
	// diz se há mais um VALOR no contexto atual, não se o stream de fato
	// terminou. Em vez disso, tenta decodificar mais um valor e exige
	// io.EOF explicitamente: qualquer outro resultado (valor decodificado
	// com sucesso, ou um erro de sintaxe por um delimitador solto) é
	// conteúdo extra, e o manifesto não corresponde byte-a-byte ao que
	// teria sido assinado.
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("release manifest: conteúdo extra após o objeto JSON")
		}
		return nil, fmt.Errorf("release manifest: conteúdo extra inválido após o objeto JSON: %w", err)
	}

	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate aplica as regras que um schema JSON genérico não expressa bem
// (consistência entre campos, não só forma de cada campo isolado).
func (m *ReleaseManifest) Validate() error {
	if m.SchemaVersion != SupportedSchemaVersion {
		return fmt.Errorf("release manifest: schema_version %d não suportado (esperado %d)", m.SchemaVersion, SupportedSchemaVersion)
	}
	if !releaseIDRe.MatchString(m.ReleaseID) {
		return fmt.Errorf("release manifest: release_id %q inválido", m.ReleaseID)
	}
	if !semverRe.MatchString(m.PlatformVersion) {
		return fmt.Errorf("release manifest: platform_version %q não é semver MAJOR.MINOR.PATCH", m.PlatformVersion)
	}
	if !m.Channel.valid() {
		return fmt.Errorf("release manifest: channel %q desconhecido", m.Channel)
	}
	if !repoRe.MatchString(m.Source.Repository) {
		return fmt.Errorf("release manifest: source.repository %q inválido", m.Source.Repository)
	}
	if !commitSHARe.MatchString(m.Source.CommitSHA) {
		return fmt.Errorf("release manifest: source.commit_sha precisa ser o SHA completo (40 hex), recebido %q", m.Source.CommitSHA)
	}
	if m.Source.WorkflowRunID == "" {
		return fmt.Errorf("release manifest: source.workflow_run_id vazio")
	}
	if len(m.Artifacts) == 0 {
		return fmt.Errorf("release manifest: artifacts vazio")
	}
	seenUnits := make(map[BuildUnit]bool, len(m.Artifacts))
	for _, a := range m.Artifacts {
		if !a.Unit.valid() {
			return fmt.Errorf("release manifest: artifacts[].unit %q não é um build_unit conhecido", a.Unit)
		}
		if !imageDigestRe.MatchString(a.Image) {
			return fmt.Errorf("release manifest: artifacts[].image %q não é uma referência ghcr.io por digest sha256 (tags móveis nunca são aceitas)", a.Image)
		}
		if seenUnits[a.Unit] {
			return fmt.Errorf("release manifest: artifacts[].unit %q duplicado", a.Unit)
		}
		seenUnits[a.Unit] = true
	}
	if len(m.Compatibility.FromVersions) == 0 {
		return fmt.Errorf("release manifest: compatibility.from_versions vazio")
	}
	if m.Compatibility.MinAgentProtocol < 1 {
		return fmt.Errorf("release manifest: compatibility.min_agent_protocol precisa ser >= 1")
	}
	if !validDatabaseStrategies[m.Compatibility.DatabaseStrategy] {
		return fmt.Errorf("release manifest: compatibility.database_strategy %q desconhecida", m.Compatibility.DatabaseStrategy)
	}
	if m.Migrations.StartAfter == "" || m.Migrations.Target == "" {
		return fmt.Errorf("release manifest: migrations.start_after/target não podem ser vazios")
	}
	if !checksumRe.MatchString(m.Migrations.ChecksumBundle) {
		return fmt.Errorf("release manifest: migrations.checksum_bundle %q não é sha256:<64 hex>", m.Migrations.ChecksumBundle)
	}
	if len(m.Rollout.Workloads) == 0 {
		return fmt.Errorf("release manifest: rollout.workloads vazio")
	}
	for _, w := range m.Rollout.Workloads {
		if !w.valid() {
			return fmt.Errorf("release manifest: rollout.workloads[] %q não é um build_unit conhecido", w)
		}
		// Todo workload que o rollout diz que muda precisa ter um artifact
		// correspondente — do contrário o Update Agent não teria o digest
		// para implantar, e um manifesto assim só poderia vir de erro ou
		// de adulteração.
		if !seenUnits[w] {
			return fmt.Errorf("release manifest: rollout.workloads[] %q não tem artifact correspondente em artifacts[]", w)
		}
	}
	if m.Rollout.PreflightPolicy != "strict" {
		return fmt.Errorf("release manifest: rollout.preflight_policy %q não suportada (só 'strict' hoje — novo valor exige ADR)", m.Rollout.PreflightPolicy)
	}
	if !m.Rollout.RecoveryClass.valid() {
		return fmt.Errorf("release manifest: rollout.recovery_class %q desconhecida", m.Rollout.RecoveryClass)
	}
	if m.ApprovedBy == "" {
		return fmt.Errorf("release manifest: approved_by vazio — uma release sem política de aprovação identificada nunca é válida")
	}
	return nil
}
