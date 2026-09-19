# Local AI — camada auxiliar opcional

**Status:** Accepted (opcional)
**Data:** 2026-09-18

## O que é

Uma camada fina sobre o llama.cpp já instalado no host, usada por agentes
(Claude/Codex) para **comprimir entrada** antes de gastar contexto caro:
triagem de logs, triagem de testes, busca de contexto, pré-review de diff.

**Não é** parte do produto OMNIRA. Nenhum binário da aplicação (`omnira-api`,
`omnira-worker`) fala com o modelo local. É ferramental de engenharia.

## Limite de autoridade — regra dura

O modelo local **nunca** é evidência final para:

- isolamento de Tenant;
- RLS;
- RBAC / authz;
- segurança;
- migration destrutiva;
- disaster recovery;
- exposição de secrets;
- aprovação final de código crítico.

Nesses casos ele faz **apenas pré-análise**. A validação final continua sendo:

```text
testes determinísticos  +  tools/checks  +  Claude/Codex
```

As tools reforçam isso na prática: `triage-tests.sh` calcula o veredito
pass/fail por `grep` determinístico e o imprime marcado como `authoritative`,
antes e independentemente de qualquer resposta do modelo.

## Arquitetura

```text
Claude / Codex
      |
      v
tools/ai/*.sh          <- tools de triagem
      |
      v
tools/ai/lib.sh        <- client único (ai_json, ai_sanitize, ai_metrics)
      |
      v
llama-server (host, OpenAI-compatible /v1/chat/completions)
```

`tools/ai/lib.sh` é o **único** ponto que fala HTTP com o llama.cpp. Nenhuma
outra parte do repositório deve chamar o endpoint diretamente.

## Ambiente detectado neste host

Dois `llama-server` já rodando — **nenhum foi alterado ou movido**:

| Porta | Modelo | ctx | GPU | Uso |
|-------|--------|-----|-----|-----|
| 18088 | `hermes-3-llama-3.1-8b` | 8192 | sim (`-ngl 99`) | **default** |
| 18090 | `Devstral-Small-2507` | 32768 | não (CPU) | não usar por ora |

Hermes é o default por latência: ~0,25 s por chamada de triagem contra ~40 s
do Devstral, que roda em CPU. O Devstral tem contexto 4× maior e é
especializado em código — vale reavaliar se ganhar offload de GPU.

O llama.cpp **permanece no host**, fora do Docker, para não complicar o acesso
à GPU. Os containers OMNIRA o alcançam por endpoint interno configurável.

## Rede

- **Não** exposto pelo Nginx público.
- **Não** acessível em `omnira.devops.k3gsolutions.com.br`.
- Somente rede interna/host.

## Configuração

Nada é hardcoded. Veja `.env.example`:

```text
LOCAL_AI_ENABLED=true|false
LOCAL_AI_BASE_URL=http://127.0.0.1:18088
LOCAL_AI_MODEL=hermes-3-llama-3.1-8b
LOCAL_AI_API_KEY=            # só se o servidor exigir
LOCAL_AI_TIMEOUT=45
LOCAL_AI_MAX_INPUT_CHARS=12000
```

## Fallback

Com `LOCAL_AI_ENABLED=false`, endpoint fora do ar, timeout ou resposta
não-JSON, cada tool cai para um caminho `grep` determinístico e **sai com
código 0**. Consequência:

- build continua funcionando;
- testes continuam funcionando;
- a aplicação continua funcionando;
- CI não depende do modelo local para validação crítica.

## Privacidade

`ai_sanitize()` roda em toda entrada antes do envio, redigindo connection
strings, `Bearer`/JWT, `api_key`, `password`/`secret`/`token` e chaves
privadas PEM. O log bruto **nunca** é descartado: fica em
`$LOCAL_AI_RAW_DIR` (padrão `/tmp/omnira-{logs,tests}`) e o caminho aparece
na saída para consulta quando o resumo não bastar.

Não enviar ao modelo: secrets, credenciais, dados pessoais reais, payloads de
clientes ou dados de produção.

## Observabilidade

`ai_metrics()` grava uma linha JSON por chamada em `.local-ai-metrics.jsonl`:
timestamp, tool, outcome (`ok`/`unavailable`/`request_error`/`parse_error`),
latência, bytes de entrada e saída. **Prompts nunca são gravados.**

## Tools disponíveis

Só existem as que já deram ganho real. Não criar as demais por formalidade.

### `tools/ai/triage-logs.sh <source>`

```bash
docker compose logs otel-collector 2>&1 | tools/ai/triage-logs.sh otel
```

Centenas de linhas → `{status, summary, probable_root_cause,
relevant_errors, recommended_next_checks}`.

### `tools/ai/triage-tests.sh <kind>`

```bash
go test ./... 2>&1 | tools/ai/triage-tests.sh unit
tools/test-isolation.sh 2>&1 | tools/ai/triage-tests.sh isolation
```

Imprime o veredito determinístico (autoritativo) e, em caso de falha,
`{category, probable_root_cause, relevant_errors, relevant_files,
recommended_next_checks}`. Em sucesso sai imediatamente sem chamar o modelo.

## Backlog (criar só quando houver ganho comprovado)

`find-context`, `review-diff`, `draft-changelog`, `classify-task`.

## Fora de escopo

Sem banco vetorial externo, framework de agentes, orchestration framework,
serviço Python separado, Kubernetes ou novo banco. Esta camada é
intencionalmente fina: um client, algumas tools, configuração e fallback.
