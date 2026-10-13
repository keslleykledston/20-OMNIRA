# Motor de roteamento (ADR-0044) — guia de operação

Estado: **LAB**. A branch `feat/sector-scheduling-overflow` NÃO está mesclada nem implantada; a flag `OMNIRA_ROUTING_ENGINE_ENABLED` não existe em produção. Este guia é para quando o dono mandar mesclar e implantar.

## 1. O que muda quando a flag liga
Com a flag **desligada** nada muda: o rodízio legado continua atribuindo, nenhuma tela nova altera comportamento, nenhum aviso é enviado. Com ela **ligada** (API e worker, mesmo valor):
- toda conversa nova passa por **uma só decisão** (`internal/routing/engine`): horário do setor → escala do atendente → presença → capacidade;
- fila **manual** também passa pelo motor (ele não atribui, mas aplica horário, aviso, SLA e transbordo);
- timers duráveis (`routing_engine_timers`) cuidam de abertura do setor, esperas de etapa e SLA — sobrevivem a reinício do worker.

## 2. Pré-requisitos e ordem de implantação
1. Ordem do dono (merge/implantação não são automáticos). Confirmar que `main` não ganhou migrations novas; se ganhou, **renumerar 124–133** antes de mesclar (`tools/validate-schema.sh`, `scripts/test-hub-migrations.sh`).
2. `scripts/backup-omnira-db.sh` e tags de volta `rollback-pre-*` (padrão dos deploys anteriores).
3. Gate completo da branch (`scripts/test-integration.sh`, de `git archive HEAD`) e `web`: `vitest run`, `tsc --noEmit`.
4. Implantar **api + worker + web juntos** (imagens de `git archive HEAD`, `docker compose up -d --no-deps --no-build --force-recreate`), migrations pelo serviço `migrate` com o diretório da árvore arquivada.
5. Com a flag ainda **desligada**: conferir `/healthz`, abrir *Atendimento* nas configurações (as telas leem e gravam; nada roteia).
6. Ligar `OMNIRA_ROUTING_ENGINE_ENABLED=true` na API **e** no worker e **reiniciar os dois juntos**. **Nunca** manter worker legado e worker do motor ao mesmo tempo numa fila: o SQL legado conta capacidade antes de travar a linha e dois workers no mesmo instante podem estourar a capacidade em 1 (achado pré-existente, `docs/delivery/ROUTING-ENGINE-STATE.md` §3b).
7. Retroalimentar conversas já abertas (opcional, ver §6).

## 3. Padrões entregues (decisão do dono, 2026-10-13)
Todos os atendentes ficam **disponíveis 24 h** (sem escala = sempre de turno) e entram no **Suporte** (fila padrão de transbordo; capacidade 5 — a confirmar). Depois se ajusta, caso a caso, horário, plantão e setor por atendente. Os setores Comercial, Financeiro e Suporte já existem (migration 123) e viram setores homônimos (124).

## 4. Configuração (Configurações › Atendimento)
| Tela | Para quê |
|---|---|
| **Setores** | agrupar grupos (filas); horário do setor; “Aviso fora do expediente” por grupo (opt-in; `{{proxima_abertura}}`) |
| **Horários** | comercial / 24 h / personalizado; fuso IANA (horário de verão automático); faixas por dia (faixa que termina antes de começar atravessa a meia-noite); feriados; exceções por período; **suspensão de emergência** (vence tudo) |
| **Escalas e plantões** | escala do atendente (global, por setor ou por grupo); listas de plantão/contingência com períodos de até 31 dias |
| **Transbordo e SLA** | cadeia de etapas por grupo (gatilhos, espera, destino fila/setor/plantão/atendente/Hub/fluxo), “quem pode atender agora”, metas de SLA (empresa/setor/grupo) e relógios |

Na conversa: painel **Roteamento e SLA** (setor, grupo, responsável, por que espera, próxima tentativa, SLA, histórico). Permissões: `agent.read` vê, `agent.manage` altera, `routing.override` libera “entregar com destino fechado / atendente fora da escala”, `tenant.manage` define o horário padrão da empresa. **Operador do Hub não administra nada disso** (404 — rotas não delegáveis); só vê o painel da conversa que já pode ler.

## 5. Monitorar (consultas somente leitura, como dono do banco)
```sql
-- onde estão as conversas
SELECT state, count(*) FROM routing_engine_state GROUP BY 1;
-- timers atrasados (deveria ser 0 por mais de alguns minutos)
SELECT kind, state, count(*), min(due_at) FROM routing_engine_timers
 WHERE state IN ('pending','leased') AND due_at < now() - interval '5 minutes' GROUP BY 1,2;
-- timers que desistiram (precisam de uma pessoa)
SELECT count(*) FROM routing_engine_state WHERE state = 'manual_required';
-- decisões recentes de uma conversa
SELECT created_at, trigger, decision, reason, destination, attempts
  FROM routing_engine_decisions WHERE conversation_id = '<id>' ORDER BY created_at;
-- SLA em risco / violado agora
SELECT metric, state, count(*) FROM conversation_sla WHERE state IN ('running','breached') GROUP BY 1,2;
```
Logs do worker: `routing engine: timer ... attempt N failed` (falha de timer; reentrega automática, desiste após o limite e a conversa vira `manual_required`), `routing: closed notice not sent` (aviso não saiu; a decisão não é afetada), `routing: flow ... could not be started` (etapa de fluxo pulada).

## 6. Retroalimentar conversas abertas ao ligar (requer aprovação do dono; escreve no banco)
Conversas abertas e sem dono só entram no motor quando algo as toca. Para colocá-las já, em janela combinada:
```sql
INSERT INTO outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, payload)
SELECT uuid_generate_v4(), c.tenant_id, 'job.routing.assign.v1', 'conversation', c.id::text, uuid_generate_v4(), '{}'::jsonb
  FROM conversations c
 WHERE c.status = 'open' AND c.assigned_to_user_id IS NULL AND c.queue_id IS NOT NULL AND c.internal_user_id IS NULL
   AND NOT EXISTS (SELECT 1 FROM routing_engine_state s WHERE s.tenant_id = c.tenant_id AND s.conversation_id = c.id);
```
Idempotente (o motor recusa o que já tem dono e repete a mesma decisão para o mesmo gatilho).

## 7. Diagnóstico rápido
| Sintoma | Causa provável | O que fazer |
|---|---|---|
| Conversa “aguardando abrir” depois do horário | timer atrasado ou worker parado | consulta de timers (§5); worker em pé? o timer tem lease de 2 min e é retomado por outro worker depois que ele expira; após 8 tentativas a conversa vira `manual_required` |
| Ninguém recebe, setor aberto | ninguém elegível: escala, offline, capacidade | tela *Transbordo e SLA* › “Situação agora” lista o motivo de cada atendente |
| Cliente não recebeu aviso fora do expediente | grupo sem texto, ou janela de 24 h da Meta fechada, ou fluxo já avisa | “Aviso fora do expediente” do grupo; o envio é melhor esforço e idempotente por episódio |
| Etapa de fluxo pulada | já havia fluxo em execução (`flow_busy`), fluxo despublicado ou filtro de canal | histórico da conversa mostra o motivo |
| Transbordo para Hub não ocorre | contrato inativo/fora do escopo ou nenhum operador do Hub apto a responder (`hub_not_ready`) | a empresa vê o contrato, não os operadores; falar com o Hub |
| `Menor carga` recusado (409) | motor desligado nesse ambiente | ligar o motor (API e worker) |

## 8. Reverter
Desligar a flag (API e worker) e reiniciar: o rodízio legado volta a atribuir. Dados e telas permanecem (nada é apagado); conversas esperando abertura deixam de ter timer, mas continuam na fila e visíveis. Reverter a implantação: tags `rollback-pre-*`. **Não** rodar `down` de migration em produção sem o dono (as tabelas novas guardam histórico de decisões e SLA).

## 9. Limites conhecidos
- Feriados são cadastro manual (sem calendário nacional) — decisão do dono pendente.
- `operational_failure` não é gatilho de cadeia: presença (Valkey) fora do ar é reenfileirada, nunca transbordada.
- O corte de “conversa já atribuída ao fechar o expediente” é: nada muda (só conversas sem dono seguem `after_hours`).
- O motor decide uma conversa por vez (uma transação por decisão); candidatos por consulta limitados a 2000 pessoas (reportado como `candidates_truncated`).
