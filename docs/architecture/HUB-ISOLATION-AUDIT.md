# Auditoria de isolamento fora do SQL (fases 10 e 11 da missão) — 2026-10-12

Estado: `LAB`. Esta auditoria cobre o que **não** é segurança por linha do PostgreSQL: Valkey, armazenamento de arquivos, IA/ferramentas/conhecimento, NATS/workers/outbox e tempo real (SSE) sob delegação (`X-Omnira-Acting-As`). A RLS tem a sua própria bateria (gate de 37 pacotes, mutação, bench).

## Método e limites

- Revisão **adversarial somente leitura** feita pelo Codex (`task-mv5kyas2-s0tgsb`), sem editar nem rodar testes, com a lista de áreas acima e a pergunta "o tenant pode vir de algum lugar que o servidor não resolveu?". A saída bruta do Codex é a fonte; o resumo abaixo não a amplia.
- Cada correção abaixo tem teste que **falha sem ela** (mutante) e roda no gate.
- **Não coberto** (e dito pelo próprio Codex): ACLs do Valkey e do NATS, permissões reais do volume de mídia, retenção do provedor de IA, política de egress. Isso é infraestrutura do dono; ver "Pendências".

## Resultado

**CRITICAL 0, HIGH 0.** MEDIUM 2, LOW 5 — o Codex não afirma "zero achados" e eu também não.

| # | Sev. | Achado | Decisão |
|---|---|---|---|
| 1 | MEDIUM | `internal/worker/realtime/bridge.go`: o bridge publicava no assunto `inbox.events.<tenant>.<conversa>` com o par vindo do `NOTIFY`, sem conferir que a conversa é do tenant. Quem conseguisse executar `pg_notify` faria eventos (ids, estados) de uma empresa aparecerem no fluxo ao vivo de outra | **CORRIGIDO.** O bridge pergunta ao banco (sessão de sistema do tenant, `EXISTS … WHERE tenant_id AND id`) e só então publica; par verificado é lembrado (relação imutável, limite de 20 mil), resposta negativa ou erro **descarta** (fail-closed). Métrica `omnira_realtime_bridge_dropped_total{reason}`. Testes: unidade (par forjado, cache, erro) + integração com NATS real (par forjado não chega nem ao assunto que nomeia); mutante (verificação desligada) falha nos 3 |
| 2 | MEDIUM | `internal/inbox/adapters/sse.go`: no fluxo de instância delegado, a visibilidade da conversa fica em cache por `VisibilityTTL` (10 s) e a revogação total espera o watchdog (~15 s): um evento pode sair até esse tempo depois de uma revogação ou mudança de fila | **ACEITO, limitado e documentado.** Falha fechada em erro de consulta; o tempo é o preço de não consultar o banco a cada evento; o tempo real é efêmero e o cliente recarrega pela API (que revalida a cada requisição). Reduzir o TTL é um ajuste de configuração, não de desenho |
| 3 | LOW | `presence:expiry` é um ZSET global com ids de tenant/agente/sessão | **ACEITO.** Só o processo com a credencial do Valkey o lê; exige ACL/rede (infra) |
| 4 | LOW | `internal/worker/routing/liveness.go`: o acordar de roteamento por presença confiava no `tenant_id` do payload, sem comparar com o assunto | **CORRIGIDO** (`handleOnSubject`): evento cujo payload nomeia empresa diferente do assunto é descartado. Teste + mutante |
| 5 | LOW | `internal/media/adapters/filestore.go`: `OpenClean` seguia symlink no último componente | **CORRIGIDO** (`O_NOFOLLOW`, como já era no `OpenOutbound`). Teste com symlink plantado |
| 6 | LOW | `internal/aiusage/adapters/ledger.go`: `Record` grava o `tenant_id` que o chamador passa, sob sessão de sistema | **ACEITO (defesa em profundidade).** Todos os chamadores atuais passam o contexto resolvido ou a linha reivindicada no banco; nenhum caminho externo escolhe o tenant. Qualquer novo chamador deve passar `TenantContext` |
| 7 | LOW | Outbox genérico publica qualquer payload no JetStream (7 dias) | **ACEITO.** Os produtores atuais carregam ids/`{}`; regra do projeto (CLAUDE.md): nada de segredo em fila. Um novo produtor que ponha corpo ou token no payload viola a regra e deve falhar na revisão |

## O que o Codex confirmou (arquivos lidos)

- **Valkey**: só presença; chaves `presence:sessions:<tenant>:<perfil>` e `presence:online:<tenant>`, com tenant e perfil vindos do contexto confiável; rotas de presença **não** são `Delegable` (um agente do Hub com `X-Omnira-Acting-As` recebe a recusa padrão); o SSE de presença ignora o cabeçalho de delegação; o roteamento sempre recebe o tenant do contexto de sistema.
- **Arquivos**: caminho = `<raiz>/{quarantine|clean}/<uuid do tenant>/<uuid da mídia>` e `outbound/<tenant>/<anexo>`; nome de upload nunca chega ao caminho; a linha de mídia é lida com `tenant_id` + mensagem antes de abrir o arquivo; `media.read` delegado passa por `DelegatedPath`; a resposta delegada do Hub **recusa** `attachment_id`.
- **IA**: o resumo delegado exige `ai.assist` **e** `conversation.read`; a sugestão de encerramento relê as mensagens enquanto o agente segura a conversa; copiloto, ferramentas de tópico e o plano de controle de fluxos **não** são delegáveis; o modelo e a rota vêm do servidor; OpenAI com `store:false` e lista de ferramentas vazia; nós de IA dos fluxos usam a sessão do tenant do fluxo; o nome da ferramenta é de um registro fechado, argumentos rejeitam campos de tenant.
- **Workers/NATS**: roteamento, fluxos, entrega e inteligência **não** autorizam pelo `tenant_id` do envelope: resolvem o tenant pela linha persistida (conversa, mensagem de saída, job reivindicado).
- **SSE delegado**: assuntos por tenant; admissão exige permissão declarada na rota; filtro de fila por evento; revogação, perda de sessão e perda de escopo encerram o fluxo.

## Pendências (do dono / infraestrutura)

- ACL do Valkey (usuário só da aplicação) e do NATS (permissão de publicar por assunto), TLS e criptografia do JetStream.
- Propriedade e modo do volume de mídia (nada além da API/serviços de mídia escreve em `clean/`); o antivírus já isola `quarantine/`.
- Teste adversarial de injeção de prompt com **modelo real** (os prompts tratam o texto da mensagem como não confiável, e as ferramentas recusam campos de tenant, mas não há prova com provedor ao vivo; custa chamadas reais de IA).
