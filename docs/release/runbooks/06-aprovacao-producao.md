# Aprovação de produção

Cobre: quem pode aprovar a promoção de uma release ao canal `stable` e a instalação/execução real contra esta SaaS compartilhada, e como essa autorização é verificada de verdade (nunca contornável pela UI).

## 1. Autoridade: sempre `platform_operator` (ADR-0038), nunca tenant/Hub/API Headless

Toda a Update Manager API (`/api/v1/platform/*`) e as decisões de promoção/instalação/revogação exigem que o usuário autenticado seja um **platform operator ativo** — verificado por `internal/hub/provisioning.IsPlatformOperator`, que consulta `platform_operators` **a cada chamada** (sessão do usuário, nunca cacheado como fato permanente). Nunca `tenant_id` do payload, nunca membership de tenant/Hub, nunca a API Headless.

**Não existe rota HTTP para se autopromover a platform operator** — deliberadamente (comentário em `internal/hub/provisioning/operators.go`): a única forma de criar ou remover um é a CLI local, rodada por quem já tem acesso ao host/banco:

```sh
go run ./apps/hubctl/cmd/omnira-hubctl --operator <sua identidade auditável> platform-operator add --email <email>
go run ./apps/hubctl/cmd/omnira-hubctl --operator <sua identidade auditável> platform-operator revoke --email <email>
go run ./apps/hubctl/cmd/omnira-hubctl --operator <sua identidade auditável> platform-operator list
```

`--operator` é **global e obrigatória antes do subcomando** (inclusive para `list`) — ela é gravada em todo evento de auditoria gerado por `hubctl`, então identifique sempre quem de fato está executando o comando, nunca um valor genérico como "admin".

`add` é idempotente (reativa um operador revogado). `revoke` marca `status='revoked'` e `revoked_at=now()` — a checagem de `IsPlatformOperator` passa a falhar na próxima chamada dessa sessão (RLS/application-level, nunca uma sessão já aberta continuando autorizada por um token antigo não revalidado).

## 2. Negação uniforme (nunca um oráculo de "isso existe mas você não pode")

Toda rota `/api/v1/platform/*` segue o mesmo padrão de `internal/hub/adapters/admin_http.go`: uma negação de autorização responde o mesmo formato que "não existe" (nunca um `403` que confirme a existência de algo que um não-operador não devia saber que existe), e toda mutação exige `Idempotency-Key`.

## 3. Aprovação explícita por release (fora da autenticação de API)

Autenticação como platform operator dá **acesso à API** — não é, por si só, a aprovação de uma release específica:

- Promoção de catálogo `candidate → stable` (`release/policies/channels.yaml requires_platform_operator_approval: true` para `stable`) exige todos os gates G0-G10 verdes **e** a aprovação explícita registrada no manifesto (`--approved-by <id da política que aprovou>`, ver `02-release-e-promocao.md`) — promover o catálogo nunca dispara instalação automática em nenhuma instalação real.
- Instalar/aplicar numa instalação real, enquanto a verificação de assinatura permanece `BLOCKED_EXTERNAL` (`05-ci-ghcr.md`), exige a confirmação `--approved-out-of-band` em `updatectl start` — a confirmação explícita e nunca-default de que um humano revisou aquele manifesto específico por outro meio.
- **BLOCK nunca é contornável pela UI** sem mudar a política e revalidar (especificação §7.1): assinatura ausente/inválida/de identidade não permitida, ou release revogada, bloqueiam incondicionalmente — não existe um botão "forçar mesmo assim" na API nem na CLI.

## 4. Revisão independente obrigatória para mudanças de alto risco

Toda mudança de alto risco neste programa (preflight, revogação, anti-replay, retenção/GC, monitoramento, segurança de GitHub Actions) passou por `codex exec review` até duas rodadas consecutivas limpas antes de ser considerada pronta — ver cada seção de fase em `docs/release/IMPLEMENTATION-STATUS.md` para a contagem real de rodadas e achados corrigidos. Se a revisão independente estiver indisponível no momento de uma mudança real futura, isto deve ser reportado explicitamente (nunca assumido como "passou").

## 5. Nunca "produção atualizada" por este programa

Nenhuma automação deste release engineering instalou o systemd unit do Update Agent em produção, habilitou `OMNIRA_PLATFORM_RELEASE_API_ENABLED` em produção, ou aplicou uma release real contra a instalação SaaS compartilhada. **Entrega** aqui significa capacidade operacional implementada e validada em ambiente seguro (staging real, F8), pronta para homologação/ativação controlada por um operador autorizado — nunca uma afirmação de que isso já aconteceu.
