# IA da plataforma: provedores de texto (OpenAI e Gemini)

Vale para tudo que a plataforma gera com modelo, pago pela própria plataforma: resumo da conversa (`/ai/summary`), tarefas do
Conversation Intelligence (classificar assunto, resumir assunto, copiloto de resposta, "Sugerir com IA" ao finalizar) e os
nós de IA do Flow Builder. **Não** cobre a análise de imagens/PDF por organização (chave Gemini própria de cada tenant,
ADR-0016): essa continua separada, com consentimento e orçamento.

## Como escolher o provedor

| Variável | Efeito |
|---|---|
| `OMNIRA_AI_ENABLED=true` | interruptor geral; sem ele nada chama modelo |
| `OMNIRA_AI_PROVIDER` | `openai` (padrão) ou `gemini` |
| `OMNIRA_AI_MODEL` | modelo padrão (obrigatório) |
| `OMNIRA_AI_API_KEY` | chave do provedor escolhido |
| `OMNIRA_GEMINI_API_KEY` | só vale com `OMNIRA_AI_PROVIDER=gemini` e só se `OMNIRA_AI_API_KEY` estiver vazia; nunca é usada com OpenAI |
| `OMNIRA_AI_TIMEOUT_SECONDS` | padrão 15 |
| `OMNIRA_AI_MODEL_TOPIC_CLASSIFY`, `_TOPIC_SUMMARY`, `_COPILOT`, `_CLOSING_SUGGEST` | modelo por tarefa (um menor para classificar, um melhor para resumir) |
| `OMNIRA_COPILOT_ENABLED` | copiloto e "Sugerir com IA" |
| `OMNIRA_AI_TOOL_GATEWAY_ENABLED` | ferramentas de leitura do copiloto |
| `OMNIRA_FLOWS_AI_ENABLED` | nós de IA dos fluxos |

Pronta = habilitada + provedor aceito + modelo + chave (`Config.AIReady`). Qualquer falta deixa a IA ausente (503/porta de erro),
nunca meio configurada. Todas essas variáveis são repassadas pelo `docker-compose.yml` à api e ao worker; vazio = padrão do código.

## Gemini

- Endpoint `POST /v1beta/models/{modelo}:generateContent`. A chave vai só no cabeçalho `x-goog-api-key` (nunca na URL), redirecionamentos
  não são seguidos, não há retentativa automática, a resposta é limitada a 4 MiB e o nome do modelo é validado (entra no caminho da URL).
- Sem ferramentas, sem cache e sem estado no provedor; temperatura 0. Partes de "raciocínio" nunca são devolvidas como resposta;
  seus tokens contam como saída.
- Bloqueio de segurança do provedor vira `ErrGeminiBlocked` (não é queda do provedor) e o uso de tokens é mantido para a contabilidade.
- Raciocínio: `gemini-2.5-flash*` roda com raciocínio desligado; `gemini-2.5-pro` usa o mínimo (128) e ganha essa folga no limite de
  saída; **demais modelos ficam no padrão do próprio modelo**. O limite de saída do Gemini inclui o raciocínio, então um modelo que
  raciocina muito pode esgotar um limite pequeno e não responder (erro "no text in response (MAX_TOKENS)").
- Modelos testados ao vivo em 2026-10-07 com a chave da plataforma: `gemini-3.5-flash-lite` e `gemini-3.1-flash-lite` respondem bem sem
  configuração extra (`thinkingBudget:0` é **rejeitado** pela família 3: 400). `gemini-2.5-flash` retornou 404 ("no longer available to
  new users") para esta conta, embora conste na listagem. `gemini-3.5-flash` e `gemini-3.8-flash` deram 503 (alta demanda) no teste.
  Liste os modelos da conta com `GET /v1beta/models` antes de trocar.
- Teste ao vivo opcional (uma chamada de ~50 tokens, sem dado de cliente): `OMNIRA_GEMINI_LIVE_TEST_KEY=... go test ./internal/ai/adapters -run TestGeminiLiveSmoke -v`
  (`OMNIRA_GEMINI_LIVE_TEST_MODEL` troca o modelo). Sem a variável o teste é ignorado.

## Contabilidade e privacidade

- Cada chamada grava uma linha em `ai_usage` com o provedor (`gemini`), o modelo e os tokens. As chamadas da plataforma entram com
  **custo nulo** (`NoCost`): o orçamento mensal do tenant (`aiusage.BudgetProvider="gemini"`) soma apenas `cost_usd`, então elas não
  consomem nem bloqueiam o orçamento de visão do tenant. A fatura do Google é a fonte de verdade do gasto da plataforma.
- Ligar a IA envia trechos de conversas de clientes ao provedor externo. Para o Gemini API pago, os termos do Google dizem que os dados
  não são usados para melhorar os produtos; o plano gratuito tem regra diferente, então use a chave de uma conta com faturamento.
- A chave fica só no `.env` do servidor (permissão restrita) e nunca em log, fila, auditoria ou resposta.

## Trocar de provedor / desligar

Edite `OMNIRA_AI_PROVIDER`/`OMNIRA_AI_MODEL` (ou `OMNIRA_AI_ENABLED=false`) no `.env` e recrie `api` e `worker`
(`docker compose up -d --no-deps --force-recreate api worker`). Nada de banco muda e **não é preciso mexer no `web`**.

> **Incidente de 2026-10-07 (corrigido):** o nginx do contêiner `web` resolvia `api:8080` só ao iniciar. Recriar a `api` (novo IP) sem recriar o `web`
> fazia `/webhooks/v1/whatsapp/meta` (Meta) responder **502** e a mensagem não chegar ao Inbox (o WAHA não era afetado). A correção está em
> `web/nginx.conf`: `resolver 127.0.0.11 valid=5s` e `proxy_pass` por variável (`$api_upstream`), de modo que o nginx consulta o DNS do Docker de novo
> a cada poucos segundos. O teste `scripts/test-web-nginx-resolver.sh` troca o contêiner da api numa rede descartável e exige que `/api/`, o webhook da
> Meta (com a query `hub.*`) e o SSE continuem funcionando sem reload; contra a configuração antiga ele falha com 502. Como o `nginx.conf` vai
> **dentro da imagem** do `web`, uma mudança nele exige `docker compose build web` e recriar o `web`.

