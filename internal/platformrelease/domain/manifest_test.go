package domain

import (
	"strings"
	"testing"
)

// validManifestJSON é um manifesto coerente com release/components.yaml
// (unidades reais) e com o schema em release/schemas/release-manifest.schema.json.
// Cada teste de invalidação parte de uma CÓPIA desta string com exatamente
// um campo alterado — assim cada falha testa uma regra isolada, não uma
// combinação de várias ao mesmo tempo.
const validManifestJSON = `{
  "schema_version": 1,
  "release_id": "omnira-2.4.0",
  "platform_version": "2.4.0",
  "channel": "stable",
  "source": {
    "repository": "keslleykledston/20-OMNIRA",
    "commit_sha": "4f6b5c1e2a9d8f3b7c0a1e4d9f2b6c8a0e1d3f5b",
    "workflow_run_id": "123456789"
  },
  "artifacts": [
    {"unit": "omnira-api", "image": "ghcr.io/omnira/omnira-api@sha256:1111111111111111111111111111111111111111111111111111111111111111"},
    {"unit": "omnira-worker", "image": "ghcr.io/omnira/omnira-worker@sha256:2222222222222222222222222222222222222222222222222222222222222222"},
    {"unit": "omnira-web", "image": "ghcr.io/omnira/omnira-web@sha256:3333333333333333333333333333333333333333333333333333333333333333"}
  ],
  "modules_changed": ["hub", "platform_release"],
  "compatibility": {
    "from_versions": ["2.3.x"],
    "min_agent_protocol": 1,
    "database_strategy": "expand_then_contract_later"
  },
  "migrations": {
    "start_after": "000140",
    "target": "000145",
    "checksum_bundle": "sha256:4444444444444444444444444444444444444444444444444444444444444444"
  },
  "rollout": {
    "workloads": ["omnira-api", "omnira-worker", "omnira-web"],
    "preflight_policy": "strict",
    "recovery_class": "R1"
  },
  "approved_by": "release-policy-stable-v1"
}`

func TestParseManifest_Valid(t *testing.T) {
	m, err := ParseManifest([]byte(validManifestJSON))
	if err != nil {
		t.Fatalf("manifesto válido rejeitado: %v", err)
	}
	if m.ReleaseID != "omnira-2.4.0" {
		t.Errorf("release_id lido errado: %q", m.ReleaseID)
	}
	if m.Rollout.RecoveryClass != RecoveryR1 {
		t.Errorf("recovery_class lido errado: %q", m.Rollout.RecoveryClass)
	}
}

// mustHaveDigestLen garante que os digests fixture acima têm 64 hex chars
// (sha256 real), para que um erro de regex no fixture não disfarce um bug
// no validador.
func TestFixtureDigestsAreWellFormed(t *testing.T) {
	m, err := ParseManifest([]byte(validManifestJSON))
	if err != nil {
		t.Fatalf("fixture base inesperadamente inválida: %v", err)
	}
	for _, a := range m.Artifacts {
		if !imageDigestRe.MatchString(a.Image) {
			t.Errorf("fixture tem digest malformado para %s: %s", a.Unit, a.Image)
		}
	}
}

// replace faz uma única substituição e falha o teste se o alvo não existir —
// evita um erro silencioso onde o teste "passa" porque a substituição nunca
// aconteceu de verdade.
func replace(t *testing.T, s, old, new string) string {
	t.Helper()
	if !strings.Contains(s, old) {
		t.Fatalf("fixture não contém a substring esperada: %q", old)
	}
	return strings.Replace(s, old, new, 1)
}

func TestParseManifest_RejectsUnknownField(t *testing.T) {
	// O caso mais importante de todos: um manifesto adulterado costuma
	// carregar um campo extra (ex.: um "override_signature_check": true)
	// que um parser tolerante aceitaria sem perceber. additionalProperties
	// estrito é a defesa contra isso.
	bad := replace(t, validManifestJSON,
		`"approved_by": "release-policy-stable-v1"`,
		`"approved_by": "release-policy-stable-v1", "override_signature_check": true`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com campo desconhecido foi aceito")
	}
}

func TestParseManifest_RejectsMovableTag(t *testing.T) {
	// Uma tag em vez de digest é exatamente o ataque que o schema existe
	// para impedir: ":latest" apontaria para conteúdo que muda depois da
	// assinatura/aprovação.
	bad := replace(t, validManifestJSON,
		`"image": "ghcr.io/omnira/omnira-api@sha256:1111111111111111111111111111111111111111111111111111111111111111"`,
		`"image": "ghcr.io/omnira/omnira-api:latest"`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com tag móvel (:latest) em vez de digest foi aceito")
	}
}

func TestParseManifest_RejectsUnknownSchemaVersion(t *testing.T) {
	bad := replace(t, validManifestJSON, `"schema_version": 1`, `"schema_version": 2`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com schema_version desconhecida foi aceito")
	}
}

func TestParseManifest_RejectsUnknownChannel(t *testing.T) {
	bad := replace(t, validManifestJSON, `"channel": "stable"`, `"channel": "beta-secreto"`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com canal desconhecido foi aceito")
	}
}

func TestParseManifest_RejectsShortCommitSHA(t *testing.T) {
	bad := replace(t, validManifestJSON,
		`"commit_sha": "4f6b5c1e2a9d8f3b7c0a1e4d9f2b6c8a0e1d3f5b"`,
		`"commit_sha": "4f6b5c1"`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com commit_sha abreviado foi aceito")
	}
}

func TestParseManifest_RejectsEmptyArtifacts(t *testing.T) {
	bad := replace(t, validManifestJSON,
		`"artifacts": [
    {"unit": "omnira-api", "image": "ghcr.io/omnira/omnira-api@sha256:1111111111111111111111111111111111111111111111111111111111111111"},
    {"unit": "omnira-worker", "image": "ghcr.io/omnira/omnira-worker@sha256:2222222222222222222222222222222222222222222222222222222222222222"},
    {"unit": "omnira-web", "image": "ghcr.io/omnira/omnira-web@sha256:3333333333333333333333333333333333333333333333333333333333333333"}
  ]`,
		`"artifacts": []`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto sem nenhum artifact foi aceito")
	}
}

func TestParseManifest_RejectsDuplicateArtifactUnit(t *testing.T) {
	bad := replace(t, validManifestJSON,
		`{"unit": "omnira-worker", "image": "ghcr.io/omnira/omnira-worker@sha256:2222222222222222222222222222222222222222222222222222222222222222"}`,
		`{"unit": "omnira-api", "image": "ghcr.io/omnira/omnira-worker@sha256:2222222222222222222222222222222222222222222222222222222222222222"}`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com unit de artifact duplicada foi aceito")
	}
}

// manifestMissingWebArtifact é uma fixture dedicada (não derivada por edição
// de string da fixture válida) para o caso "rollout cita um workload sem
// artifact correspondente": omnira-web aparece em rollout.workloads mas não
// em artifacts[].
const manifestMissingWebArtifact = `{
  "schema_version": 1,
  "release_id": "omnira-2.4.0",
  "platform_version": "2.4.0",
  "channel": "stable",
  "source": {
    "repository": "keslleykledston/20-OMNIRA",
    "commit_sha": "4f6b5c1e2a9d8f3b7c0a1e4d9f2b6c8a0e1d3f5b",
    "workflow_run_id": "123456789"
  },
  "artifacts": [
    {"unit": "omnira-api", "image": "ghcr.io/omnira/omnira-api@sha256:1111111111111111111111111111111111111111111111111111111111111111"},
    {"unit": "omnira-worker", "image": "ghcr.io/omnira/omnira-worker@sha256:2222222222222222222222222222222222222222222222222222222222222222"}
  ],
  "modules_changed": ["hub"],
  "compatibility": {
    "from_versions": ["2.3.x"],
    "min_agent_protocol": 1,
    "database_strategy": "expand_then_contract_later"
  },
  "migrations": {
    "start_after": "000140",
    "target": "000145",
    "checksum_bundle": "sha256:4444444444444444444444444444444444444444444444444444444444444444"
  },
  "rollout": {
    "workloads": ["omnira-api", "omnira-worker", "omnira-web"],
    "preflight_policy": "strict",
    "recovery_class": "R1"
  },
  "approved_by": "release-policy-stable-v1"
}`

func TestParseManifest_RejectsRolloutWorkloadWithoutArtifact(t *testing.T) {
	// Um workload listado em rollout sem um artifact correspondente não tem
	// digest nenhum para o Update Agent implantar — manifesto inconsistente,
	// e um candidato óbvio a bug de geração (ou adulteração) do manifesto.
	if _, err := ParseManifest([]byte(manifestMissingWebArtifact)); err == nil {
		t.Fatal("manifesto com rollout.workloads citando unit sem artifact correspondente foi aceito")
	}
}

func TestParseManifest_RejectsBadChecksumFormat(t *testing.T) {
	bad := replace(t, validManifestJSON,
		`"checksum_bundle": "sha256:4444444444444444444444444444444444444444444444444444444444444444"`,
		`"checksum_bundle": "md5:aabbcc"`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com checksum_bundle em formato errado foi aceito")
	}
}

func TestParseManifest_RejectsNonStrictPreflightPolicy(t *testing.T) {
	bad := replace(t, validManifestJSON, `"preflight_policy": "strict"`, `"preflight_policy": "skip"`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com preflight_policy != strict foi aceito")
	}
}

func TestParseManifest_RejectsEmptyApprovedBy(t *testing.T) {
	bad := replace(t, validManifestJSON, `"approved_by": "release-policy-stable-v1"`, `"approved_by": ""`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com approved_by vazio foi aceito")
	}
}

func TestParseManifest_RejectsTrailingGarbage(t *testing.T) {
	bad := validManifestJSON + `{"this":"should not be here"}`
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com conteúdo extra após o JSON foi aceito")
	}
}

// Achado da revisão independente (Codex, F1): dec.More() não detecta um
// delimitador de fechamento solto após o objeto — só um valor JSON completo
// extra. Estes dois casos cobrem exatamente o que a correção precisa pegar.
func TestParseManifest_RejectsTrailingClosingDelimiter(t *testing.T) {
	for _, suffix := range []string{"}", "]", " }", "\n}"} {
		bad := validManifestJSON + suffix
		if _, err := ParseManifest([]byte(bad)); err == nil {
			t.Fatalf("manifesto com delimitador de fechamento solto (%q) após o JSON foi aceito", suffix)
		}
	}
}

func TestParseManifest_RejectsMalformedJSON(t *testing.T) {
	if _, err := ParseManifest([]byte(`{not json`)); err == nil {
		t.Fatal("JSON malformado foi aceito")
	}
}

// Achado da revisão independente (Codex, F1): o schema JSON exige
// modules_changed (e os demais campos top-level); o struct Go, decodificado
// direto, não distinguia "ausente"/"null" de "[]" (zero value). Testa os
// dois casos para modules_changed e, por amostragem, para outro campo
// obrigatório (approved_by ausente, não só vazio).
func TestParseManifest_RejectsMissingOrNullRequiredField(t *testing.T) {
	missing := replace(t, validManifestJSON, `"modules_changed": ["hub", "platform_release"],`, ``)
	if _, err := ParseManifest([]byte(missing)); err == nil {
		t.Fatal("manifesto sem modules_changed (campo ausente) foi aceito")
	}

	null := replace(t, validManifestJSON, `"modules_changed": ["hub", "platform_release"]`, `"modules_changed": null`)
	if _, err := ParseManifest([]byte(null)); err == nil {
		t.Fatal("manifesto com modules_changed: null foi aceito")
	}

	// Controle: modules_changed presente e vazio continua válido — a regra
	// é presença/não-nulidade, nunca "a lista precisa ter itens".
	empty := replace(t, validManifestJSON, `"modules_changed": ["hub", "platform_release"]`, `"modules_changed": []`)
	if _, err := ParseManifest([]byte(empty)); err != nil {
		t.Fatalf("manifesto com modules_changed: [] (presente e vazio) deveria ser válido: %v", err)
	}

	missingApprovedBy := replace(t, validManifestJSON, `,
  "approved_by": "release-policy-stable-v1"`, ``)
	if _, err := ParseManifest([]byte(missingApprovedBy)); err == nil {
		t.Fatal("manifesto sem approved_by (campo ausente, não só vazio) foi aceito")
	}
}

// Achado da revisão independente (Codex, F1, segunda rodada): um elemento
// `null` dentro de modules_changed/from_versions é silenciosamente
// convertido para "" pelo decode direto em []string — não é a mesma coisa
// que uma string vazia legítima, e o schema JSON rejeitaria o tipo errado.
func TestParseManifest_RejectsNullElementInStringArrays(t *testing.T) {
	modulesNull := replace(t, validManifestJSON,
		`"modules_changed": ["hub", "platform_release"]`,
		`"modules_changed": ["hub", null]`)
	if _, err := ParseManifest([]byte(modulesNull)); err == nil {
		t.Fatal("manifesto com elemento null em modules_changed foi aceito")
	}

	fromVersionsNull := replace(t, validManifestJSON,
		`"from_versions": ["2.3.x"]`,
		`"from_versions": [null]`)
	if _, err := ParseManifest([]byte(fromVersionsNull)); err == nil {
		t.Fatal("manifesto com elemento null em compatibility.from_versions foi aceito")
	}
}

// Achado da revisão independente (Codex, F1, terceira rodada): Go faz
// correspondência de campo case-insensitive no decode do struct mesmo com
// DisallowUnknownFields — um manifesto com "channel" E "CHANNEL" (chaves
// distintas em JSON puro) nunca seria pego por ali, e a segunda chave
// sobrescreveria silenciosamente o valor da primeira no struct Go, embora
// o schema JSON (case-sensitive) rejeitasse "CHANNEL" como propriedade
// desconhecida.
func TestParseManifest_RejectsCaseVariantDuplicateKey(t *testing.T) {
	bad := replace(t, validManifestJSON,
		`"channel": "stable",`,
		`"channel": "stable", "CHANNEL": "dev",`)
	m, err := ParseManifest([]byte(bad))
	if err == nil {
		t.Fatalf("manifesto com chave variante de caixa (CHANNEL) foi aceito, decodificado como channel=%q", m.Channel)
	}
}

// Mesmo achado, em um objeto aninhado (não só no nível raiz) — confirma que
// a checagem de chaves exatas é recursiva, não só do manifesto como um todo.
func TestParseManifest_RejectsCaseVariantDuplicateKeyInNestedObject(t *testing.T) {
	bad := replace(t, validManifestJSON,
		`"preflight_policy": "strict",`,
		`"preflight_policy": "strict", "Preflight_Policy": "skip",`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com chave variante de caixa em rollout (Preflight_Policy) foi aceito")
	}
}

// Achado da revisão independente (Codex, F1, quarta rodada): release_notes_ref
// é opcional, mas quando presente precisa ser string — null explícito não é
// o mesmo que "ausente", e o decode direto no struct aceitaria silenciosamente.
func TestParseManifest_RejectsNullOptionalReleaseNotesRef(t *testing.T) {
	bad := replace(t, validManifestJSON,
		`"approved_by": "release-policy-stable-v1"`,
		`"approved_by": "release-policy-stable-v1", "release_notes_ref": null`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com release_notes_ref: null foi aceito")
	}
}

// Controle: ausente (não mencionado) continua válido, e presente com um
// valor real continua válido — a regra é só sobre null explícito.
func TestParseManifest_AllowsReleaseNotesRefAbsentOrPresent(t *testing.T) {
	if _, err := ParseManifest([]byte(validManifestJSON)); err != nil {
		t.Fatalf("manifesto sem release_notes_ref (campo opcional ausente) deveria ser válido: %v", err)
	}
	withRef := replace(t, validManifestJSON,
		`"approved_by": "release-policy-stable-v1"`,
		`"approved_by": "release-policy-stable-v1", "release_notes_ref": "sha256:5555555555555555555555555555555555555555555555555555555555555555"`)
	if _, err := ParseManifest([]byte(withRef)); err != nil {
		t.Fatalf("manifesto com release_notes_ref presente e válido deveria ser aceito: %v", err)
	}
}

// Achado da revisão independente (Codex, F1, quinta rodada): uma chave
// repetida no MESMO objeto (ex.: "source" aparecendo duas vezes no nível
// raiz) nem o decode em map (fica só com a última) nem o decode no struct
// (mescla campo a campo, deixando valores da primeira ocorrência sobreviver
// onde a segunda não os menciona) pegavam de forma confiável — só um scan
// de tokens que rastreia cada objeto aberto pega isto. Três profundidades:
// raiz, dentro de "source", e dentro de um elemento de "artifacts".
func TestParseManifest_RejectsDuplicateKeyAtRoot(t *testing.T) {
	bad := replace(t, validManifestJSON,
		`"workflow_run_id": "123456789"
  },
  "artifacts": [`,
		`"workflow_run_id": "123456789"
  },
  "source": {"workflow_run_id": null},
  "artifacts": [`)
	m, err := ParseManifest([]byte(bad))
	if err == nil {
		t.Fatalf("manifesto com \"source\" repetido no nível raiz foi aceito (workflow_run_id lido = %q)", m.Source.WorkflowRunID)
	}
}

func TestParseManifest_RejectsDuplicateKeyInsideNestedObject(t *testing.T) {
	bad := replace(t, validManifestJSON,
		`"workflow_run_id": "123456789"
  },`,
		`"workflow_run_id": "123456789",
    "workflow_run_id": "outra-run-id"
  },`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com workflow_run_id repetido dentro de \"source\" foi aceito")
	}
}

func TestParseManifest_RejectsDuplicateKeyInsideArrayElement(t *testing.T) {
	bad := replace(t, validManifestJSON,
		`{"unit": "omnira-api", "image": "ghcr.io/omnira/omnira-api@sha256:1111111111111111111111111111111111111111111111111111111111111111"}`,
		`{"unit": "omnira-api", "unit": "omnira-worker", "image": "ghcr.io/omnira/omnira-api@sha256:1111111111111111111111111111111111111111111111111111111111111111"}`)
	if _, err := ParseManifest([]byte(bad)); err == nil {
		t.Fatal("manifesto com \"unit\" repetido dentro de um elemento de artifacts foi aceito")
	}
}

// Controle: o manifesto válido (sem nenhuma chave repetida em lugar nenhum)
// continua aceito — a função não pode ser falso-positivo em JSON legítimo
// complexo (objetos aninhados e arrays de objetos).
func TestRejectDuplicateKeys_AllowsValidManifest(t *testing.T) {
	if err := rejectDuplicateKeys([]byte(validManifestJSON)); err != nil {
		t.Fatalf("manifesto válido foi rejeitado pelo scanner de chaves duplicadas: %v", err)
	}
}
