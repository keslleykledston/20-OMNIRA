# ADR-0045 — Menu sem resposta: lembretes e encerramento por inatividade

Status: **IMPLEMENTADA e IMPLANTADA em LAB** (2026-10-13; merge `9a26f9c`, imagens `sha-9a26f9c` de api, worker e web; gate de integração com o ajuste da contagem do catálogo). Aplicada na empresa K3G_Solutions (antes "Test Company LTDA"): fluxo "Recepção inteligente" v4 com menu de 120 s, 2 lembretes e, sem resposta, transbordo ao Suporte. Enviada ao GitHub (`main` em 02b6367). Pedido do dono depois do teste do motor de roteamento: se o cliente não escolhe no menu, lembrar; se continuar em silêncio, perguntar se ainda está aí; só então transbordar ao Suporte, ou encerrar por inatividade (configurável).

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
- **Provado em WhatsApp real (2026-10-13, aparelho de teste +5592991882864, K3G_Solutions, fluxo v4):** menu às 16:08:59, 1º lembrete 16:11:06, "ainda está aí?" 16:13:21, mensagem do Suporte 16:15:36 e `ENQUEUE` na fila Suporte às 16:15:37 (resumo "sem resposta após 2 lembretes"). Atrasos de 6 a 15 s por espera vêm do intervalo de leitura do sweeper.
- **Decisão do dono (2026-10-13):** o desfecho padrão continua sendo a fila do Suporte. A conversa fica aberta e sem dono até alguém assumir (fila manual, ADR-0044 §11.9). O encerramento por inatividade (`close_conversation`) segue como opção de configuração, sem uso.
