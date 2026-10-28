# Revogação de uma release

Cobre: revogar uma release já publicada/instalada (classe de recuperação R4 — "release revogada/comprometida: isolar, nunca downgrade cego"). Implementado em F9 (`internal/platformrelease/updatemgr`, `release/policies/signing.yaml revocation`/`anti_replay`).

## 1. Revogar via a Update Manager API

```sh
curl -X POST "$BASE_URL/api/v1/platform/releases/<release_id>/revoke" \
  -H "Idempotency-Key: <chave única desta operação>" \
  -H "Content-Type: application/json" \
  -d '{"reason": "<motivo auditável>"}'
```

Exige sessão autenticada de `platform_operator` (ver `06-aprovacao-producao.md`). `Idempotency-Key` é obrigatório: reenviar a mesma chave com o **mesmo** motivo repete o registro original (`200`); a mesma chave com um motivo **diferente** é `422` (nunca silenciosamente sobrescrito).

**Fato append-only:** `platform_release_revocations` nunca suporta `UPDATE`/`DELETE` — revogar por engano se corrige com uma **nova** decisão de operador, separadamente auditada, nunca uma reversão silenciosa desta linha.

## 2. O que acontece depois de revogar

1. **Bloqueia toda aplicação futura.** Qualquer novo `updatectl start`/`reconcile` (ou dispatch via API) para esse `release_id` é recusado.
2. **Força R4 em qualquer run ATIVO (não-terminal) despachado para essa release** — mesmo que o manifesto tivesse declarado uma classe de recuperação diferente (ex. R1). Revogação sempre vence a classe declarada. `Executor.Execute` checa `isRevokedOrUnsafe` a cada iteração do loop de execução **enquanto o run não é terminal** (`internal/platformrelease/agent/reconcile.go` — `if domain.IsTerminal(run.State) { return nil }` é o primeiro desvio do loop).
3. **Revalidação em múltiplos pontos ENQUANTO o run ainda está ativo, não só no preflight inicial** — achado real de Codex (F9, rodada 16): uma revogação entregue **durante** a janela de observação pós-deploy, ou durante o `rollback`/health-check de `recover()` (ambos ainda não-terminais), precisa ser revalidada antes de confirmar `SUCCEEDED` e depois do rollback/health-check — sem isto, uma revogação entregue bem no meio dessas janelas podia escapar e o run terminar `SUCCEEDED`/`ROLLED_BACK` mesmo já revogado.
4. **NUNCA isola automaticamente uma release já `SUCCEEDED` (já instalada e com containers rodando).** Para um run que já terminou (estado terminal) ANTES da revogação existir, `Execute` retorna imediatamente sem tocar nada — revogar não para, substitui nem isola os containers já em execução dessa release. Isto é **intencional** (R4 é "isolar, nunca downgrade cego": a decisão de o que fazer com uma instalação já comprometida é sempre humana, nunca automática) mas significa que revogar uma release já instalada **não basta por si só** — ver §4 abaixo para o passo manual real de conter uma instalação já rodando uma release revogada.
5. **Entrega confiável ao Update Agent, independente de tráfego do operador.** A revogação é espelhada no ledger local do agente com semântica de retry-até-entregue — sobrevive a um crash da API no meio da entrega (vale tanto para o bloqueio de futuras aplicações quanto para a revalidação de runs ainda ativos, acima).

## 3. Anti-replay e frescor da sincronização

O agente **nunca confia** numa revogação mais antiga que `release/policies/signing.yaml anti_replay.revocation.max_cache_age_seconds` (300s) sem uma sincronização fresca — e **se recusa a rodar (fail-closed)** até essa sincronização ter acontecido **pelo menos uma vez**, a menos que a instalação esteja explicitamente marcada `standalone` (`OMNIRA_AGENT_STANDALONE`, ver `03-rollback-e-recovery.md`). Um manifesto "stable" baixado antes de uma revogação não pode ser aplicado depois dela só porque já estava em disco local — a revogação é reconsultada no momento do `apply`, não só no momento do download (`revalidate_before_apply: true`).

**Anti-downgrade:** uma release revogada nunca pode ser contornada reinstalando uma versão anterior que reintroduza a mesma vulnerabilidade (`anti_downgrade_enforced: true`, `release/policies/signing.yaml`) — isso é tratado junto da política de compatibilidade (`release/policies/compatibility.yaml`), nunca como um `downgrade: true` simples.

## 4. Conter uma instalação que já está rodando a release revogada

Se `GET /api/v1/platform/installation` (ou `updatectl status`) mostra que a release revogada já está instalada e rodando (run terminal `SUCCEEDED`), a chamada de revogação em si **não para nada** — é preciso uma ação humana explícita de contenção:

1. **Nunca** tente "reverter" simplesmente reaplicando uma versão anterior sem revisar — isso é exatamente o downgrade cego que R4 proíbe (`anti_downgrade_enforced`, §3 acima); a versão anterior pode reintroduzir a mesma vulnerabilidade, ou ter seu próprio schema incompatível com o estado atual do banco.
2. **Caminho normal:** construa e aplique uma release NOVA e explicitamente segura (uma correção real, ou uma versão anterior conhecida como segura e compatível) pelo fluxo normal de `02-release-e-promocao.md` (`updatectl start --approved-out-of-band` com o manifesto da versão de contenção escolhida) — isto roda pelo mesmo `Executor`/gates de compatibilidade, nunca contorna a validação.
3. **Caminho de emergência, se a release revogada estiver ativamente explorada e não houver tempo para preparar uma release de correção:** isolar manualmente via Docker (`docker compose stop <serviço>` ou equivalente) é uma decisão **fora** deste programa de release engineering — documente a ação e o motivo no mesmo lugar onde a revogação foi registrada (`reason` do `POST .../revoke`), e trate a reconciliação automática subsequente do agente (que vai recusar reaplicar a release revogada, mas também não vai "corrigir" um stack parado manualmente) como esperada até a release de contenção real ser aplicada.

## 5. Verificar o estado de revogação sem mutar nada

```sh
curl "$BASE_URL/api/v1/platform/monitoring/status"      # sinal agregado, inclui frescor da sincronização
curl "$BASE_URL/api/v1/platform/installation"             # estado singleton desta instalação
```

Localmente (sem a API, ver `03-rollback-e-recovery.md`):
```sh
/opt/omnira/update-agent/bin/omnira-updatectl status --installation-id=<id>
```

## 6. BLOCKED_EXTERNAL relevante a este runbook

A lista de revogação em si (`list_format: signed_json_array`) e a verificação de assinatura real do manifesto permanecem `BLOCKED_EXTERNAL` nesta sessão (sem `gh`/GHCR/Cosign — ver `05-ci-ghcr.md`). O que **foi** implementado e testado de verdade aqui é o **enforcement local** da revogação (gate em `Executor`, anti-replay, fail-closed, standalone) — ele é independente de a lista remota de revogação já estar assinada e publicada; quando esse mecanismo externo existir, ele alimenta o mesmo `RevokeRelease`/ledger já construídos, sem reescrever a lógica de enforcement.
