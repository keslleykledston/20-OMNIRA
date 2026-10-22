# CI/GHCR

Cobre: o fluxo real de `.github/workflows/release-build.yml`, e o que fica `BLOCKED_EXTERNAL` nesta sessão por falta de credenciais (`gh` CLI, GHCR, Cosign).

## 1. Fluxo real (quando as credenciais existirem)

1. `ci-main.yml` roda em cada push/PR (gates G0-G5: build, unit, integração, RLS/Postgres, web, contratos).
2. `release-build.yml` é **disparado por `workflow_run` de "CI (main)" concluído com sucesso** — nunca por `push` direto; todo job se recusa a rodar a menos que essa conclusão tenha sido `success`. Isto cobre o caminho automático: um commit que falha em `ci-main.yml` nunca chega a ser buildado/assinado/publicado como candidate por esse gatilho.
   **Exceção explícita e deliberada: `workflow_dispatch` (disparo manual) NÃO verifica o resultado de `ci-main.yml`** — ele é travado só pela verificação de ref confiável (`trusted_identity.allowed_ref_patterns`), e o próprio humano que dispara é o gate desse caminho (comentário no topo do workflow: "a human triggering it IS the gate for that path"). Um operador com acesso de disparo manual **pode** publicar um candidate para um commit que falhou em CI — nunca trate "existe um candidate publicado" como prova de que os gates de qualidade passaram; confira sempre se o build veio do caminho automático (`workflow_run`) ou de um disparo manual antes de assumir isso.
3. `vars` job resolve e **falha alto e explicitamente** se o ref não for `refs/heads/main` ou uma tag `omnira-*` (`release/policies/signing.yaml trusted_identity.allowed_ref_patterns`) — nunca um skip silencioso. Todo input de `workflow_dispatch` é lido via `env:` (nunca interpolado direto num `run:`, que seria um vetor de injeção clássico do GitHub Actions) e validado quanto ao formato.
4. `build-push-sign` builda as três workloads reais (`omnira-api`, `omnira-worker`, `omnira-web`) pelo commit SHA exato que `ci-main.yml` validou (todo `git checkout` fixa `ref: ${{ needs.vars.outputs.resolved_sha }}`), gera SBOM + attestation de proveniência + assinatura, e publica no GHCR por **digest OCI**, nunca `:latest` nem outra tag móvel.
5. `manifest` job monta e **assina** o manifesto de release (`apps/releasectl build-manifest`, ver `02-release-e-promocao.md`) — o manifesto em si é atestado/assinado, não só as imagens (o `release/policies/signing.yaml` cobre "o conteúdo exato" da release; metadados de compatibilidade/migração/recuperação/aprovação vivem no manifesto, não numa camada de imagem).
6. O resultado é publicado no canal **`candidate`** — este workflow **nunca** publica `stable`. Promover um candidate a stable é um passo humano separado (ver `02-release-e-promocao.md` e `06-aprovacao-producao.md`), que depende da evidência de staging/fault-injection (F8) existir.

Mecanismo de assinatura (`release/policies/signing.yaml`): primário `github_artifact_attestation`, fallback `sigstore_cosign_keyless` (OIDC do próprio workflow). **Nunca** uma assinatura simétrica local "provisória" tratada como equivalente.

## 2. Estado real desta sessão — `BLOCKED_EXTERNAL`

Confirmado em F0 (`docs/release/00-audit.md §7`) e reconfirmado em cada fase subsequente, nunca mudou:

| Item | Estado | Afeta |
|---|---|---|
| `gh` CLI | ausente neste host | F2, F3, F11 |
| Credenciais GHCR (push) | não verificadas/não presentes nesta sessão | F3, F9 |
| Cosign | sem chave/identidade OIDC configurada para uso real | F3 |

**O que isso bloqueia:** publicação real no GHCR, assinatura real via attestation/Cosign contra um registry real, e qualquer verificação de assinatura além da validação de **forma** (`domain.ParseManifest`). `apps/updatectl start` exige `--approved-out-of-band` justamente por isto (ver `02-release-e-promocao.md`).

**O que isso NÃO bloqueia:** builds OCI locais sem push (F3), os contratos/scripts/testes locais do pipeline inteiro, e toda a lógica de enforcement real que o verificador de assinatura vai gatear quando as credenciais existirem — revogação (F9), anti-replay, canais, retenção/GC foram implementados e testados contra o enforcement real, nunca simulados "no papel" (ver `docs/release/IMPLEMENTATION-STATUS.md` seções F3/F9).

## 3. Quando o operador tiver as credenciais: passos exatos para desbloquear

1. Instalar/autenticar `gh` CLI (`gh auth login`) com um token que tenha `packages:write` no repositório.
2. Confirmar acesso de push ao GHCR: `echo $TOKEN | docker login ghcr.io -u <user> --password-stdin`.
3. Decidir o mecanismo de assinatura real (`github_artifact_attestation` é o primário — já disponível nativamente em Actions com `id-token: write` e `attestations: write`; `sigstore_cosign_keyless` exige o binário `cosign` no runner, já referenciado pelo workflow).
4. Confirmar, com uma execução real de `release-build.yml` via `workflow_run`, a limitação **ainda não verificada** documentada no próprio workflow (linhas 45-65): se o claim `sha` do token OIDC do job corresponde a `workflow_run.head_sha` (o que foi de fato buildado) ou à ponta de `main` no momento da emissão do token — isto é um comportamento de plataforma do GitHub, não algo que uma mudança de código aqui resolva por si só. Só depois disso confiar na saída de proveniência deste workflow para algo além dos próprios testes locais deste programa.
5. Nenhum destes passos é executado por este programa — são decisões/credenciais do operador humano.
