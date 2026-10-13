# ADR-0045 — Menu sem resposta: lembretes e encerramento por inatividade

Status: **IMPLEMENTADA em LAB** na branch `feat/menu-idle-reminders` (2026-10-13; não mesclada nem implantada até a ordem do dono). Pedido do dono depois do teste do motor de roteamento: se o cliente não escolhe no menu, lembrar; se continuar em silêncio, perguntar se ainda está aí; só então transbordar ao Suporte, ou encerrar por inatividade (configurável).

## 1. Decisão

1. **Nó `choice` ganha `reminders`** (até 3 textos). `timeout_seconds` passa a ser a espera antes de **cada** lembrete (mínimo 30 s, máximo 24 h quando há lembretes). A cada silêncio o motor envia o lembrete **junto com o menu de novo** (botões/lista ou texto numerado, como o menu original) e espera outro `timeout_seconds`. Depois do último lembrete, mais uma espera sem resposta segue a porta `timeout`, como já era.
   - Exemplo do dono: `timeout_seconds: 120`, 2 lembretes ("Você ainda não escolheu…", "Você ainda está aí? Escolha uma opção…") → lembrete aos 2 min, segundo aos 4 min, porta `timeout` aos 6 min.
   - Uma resposta válida em qualquer ponto segue o fluxo normal. Uma resposta inválida mantém a contagem de lembretes (não recomeça).
2. **Novo nó terminal `close_conversation`** (`text` opcional, `reason`: `no_response` por padrão, `resolved` ou `other`). Envia a despedida (se não puder ser entregue, não impede o encerramento) e encerra o atendimento **pelo sistema**: mesmo caso de uso do finalizar de um atendente (chamados locais fechados, ERP intocado), registro `conversation_closures` com `source = system` e o motivo escolhido. Sem transbordo.
3. **A escolha do fim é do autor do fluxo:** ligar a porta `timeout` do menu a `human_handoff`/setor (transbordo ao Suporte) **ou** a `close_conversation` (encerrar por inatividade).
4. **Sem mudança de motor de roteamento nem de SLA.** O relógio `first_response` conta desde a abertura da conversa de propósito (ADR-0044 §SLA: é o que o cliente percebe). Quem quer contar só a espera por um atendente usa as métricas `queue_wait` (fila atual) ou `assignment` (até alguém assumir), que já nascem na entrada na fila.

## 2. O que não muda

- Fluxos já publicados: o campo é opcional; sem `reminders` tudo se comporta como antes. Versões publicadas são imutáveis; para usar, edita-se o fluxo e publica-se uma versão nova.
- O modelo de modelos (`smart-reception` v2/v3) não foi alterado; uma v4 com lembretes é opcional.
- Frontend não decide nada: o backend valida (`too_many_reminders`, `reminder_wait`, `invalid_reason`).

## 3. Evidência

- Unitários (motor com relógio falso): lembrete aos 2 e 4 min com o menu junto, porta `timeout` aos 6 min fechando com `no_response`; transbordo em vez de fechar; resposta depois do lembrete segue; resposta inválida mantém a contagem; fechar sem a capacidade falha a corrida (a conversa volta aos humanos); limites validados. Dois mutantes mortos (lembretes desligados; contagem perdida em resposta inválida).
- PostgreSQL real (`TestSilentMenuIsNudgedByDurableTimersThenClosedForInactivity`): timers duráveis lidos do banco como o sweeper do worker, 3 esperas, conversa `closed`, fechamento `system`/`no_response`.
- Web: 979 testes Vitest, `tsc` limpo; inspetor edita o tempo e os lembretes (máx. 3) e o novo nó.
- **Não provado:** conversa real de WhatsApp com a nova versão do fluxo (exige publicar a versão e uma mensagem do aparelho de teste).
