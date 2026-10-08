# Smoke com telefone real — resultado (Recepção inteligente, linha Meta)

Tenant de produção, fluxo "Recepção inteligente" (slug `smart-reception`, `flows.id` `b2f00ae1-…`), restrito à linha WhatsApp oficial (`trigger_filter.connection_ids`). Roteiro: `SMOKE-REAL-PHONE.md`. Nenhum dado de cliente real: o telefone é o de teste do dono.

## Rodada de 2026-10-07 20:48 (versão 2 do tenant)
O fluxo percorreu menu → "Suporte técnico" → subfluxo `existing-ticket` → resposta "1" → `h_exist` (`waiting_human`, fila `Default`). Dois defeitos:

| # | Sintoma | Causa | Correção |
|---|---|---|---|
| 1 | Pergunta `chamado aberto: ""` e resumo `existe: .` | Chamado aberto no CRM pelo Inbox é "real" (tem número do CRM) mas o assunto local é vazio; `find_open_tickets` expunha o assunto cru | `fix(flows)` `e7a9772`: rótulo = assunto, senão `nº <número do CRM>`, senão `sem assunto registrado`. Implantado no `worker` |
| 2 | Silêncio depois do "1" | A versão 2 do tenant foi instalada antes de o modelo ganhar os nós `say_*` (`27ca0a9`); publicada, é imutável | Versão 3 do tenant, ver abaixo |

## Versão 3 do tenant (publicada pelo dono em 2026-10-07 21:32)
Mudança só de dados (não está em migration): cinco nós `send_message` antes de cada `human_handoff` — `say_exist`, `say_tech`, `say_fin`, `say_com`, `say_fb` — com os mesmos textos do modelo da biblioteca (`library_starter.go`), nós de transferência reposicionados no editor. Rascunho preparado como revisão 3 sem publicar; validado com `domain.Validate` (0 erros; avisos de portas opcionais `error`/`window_closed` desconectadas, iguais às do nó `hello`). Hashes: v2 `caf26b25…`, v3 `1cf67743…`. Rollback: republicar o conteúdo da versão 2 como nova versão (as versões são imutáveis).

## Rodada de 2026-10-07 21:39 (versão 3)
Mesmo caminho: `find_open_tickets` → `chamado aberto: "nº 28276"`; resposta "1" → `say_exist` enviado ("Vi que você já tem um chamado em andamento…", status `read`) → `h_exist` com resumo `…fala de um chamado que já existe: nº 28276.` Execução `waiting_human`, sem erro.

## Não exercitado ainda
Ramos suporte sem chamado aberto (`say_tech`), financeiro, comercial e outro assunto; o aviso de abertura de chamado com o protocolo (CHANGELOG, 2026-10-08) — exige um chamado aberto por um operador no CRM.
