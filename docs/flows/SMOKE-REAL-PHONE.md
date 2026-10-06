# Smoke com telefone real: Flow Builder (canal oficial Meta)

**Estado: preparado, NÃO executado.** Duração prevista: 30 a 40 minutos. Condição 5 do piloto em `PRODUCTION-GATE.md`.
Quem faz: o dono (login SSO, telefone, telas). O assistente pode ligar/desligar a flag, coletar evidência e desfazer, **quando o dono mandar**.

## 1. Onde o teste roda e por quê

| Item | Valor |
|---|---|
| Tenant | `Test Company LTDA` (`11111111-1111-1111-1111-111111111111`) |
| Canal | **WhatsApp oficial (Meta Cloud)**, conexão `5e7aba50-3a8d-4c2f-b4d1-e2774653b9be` (`provider=meta_cloud`) |
| Fila de destino | `Default` (manual, `6c9cd0e9-9595-4ffc-9d1d-35d3c5815697`) para os 4 mapeamentos |
| Não usar | linha WAHA `85af82d7-…` (tráfego real intenso: 1.789 mensagens recebidas em 7 dias) e a fila `Pilot Round Robin` (distribuiria a conversa de teste a um atendente real) |

Por que não um tenant novo: o WAHA instalado é **CORE (uma sessão)** e já está ocupado por este tenant; o número Meta também está ligado a ele. Um tenant novo não receberia mensagens de telefone. O tenant vazio `Test Company` serve só para conferir a interface, nunca para este teste.

A linha Meta recebeu só 6 mensagens em 7 dias (as últimas de hoje e de ontem à noite), então o risco para clientes reais é mínimo. Mesmo assim:

- **A flag é global, o efeito é por fluxo.** `OMNIRA_FLOWS_ENABLED=true` não muda nada até existir um fluxo **publicado**; e um fluxo só pega conversas da linha que estiver no filtro dele.
- **Filtro vazio = todas as linhas do tenant, incluindo a WAHA.** O modelo "Recepção inteligente" nasce com `is_default=true` e prioridade 999, e o instalador copia isso. **Por isso o passo 4 (restringir ao canal Meta ANTES de publicar) é obrigatório.**
- Quem escrever para o número Meta durante a janela do teste recebe a recepção do bot (saudação, perguntas, menu) e cai na fila `Default` para os atendentes. Avise a equipe antes.

## 2. Pré-voo (assistente confere)

1. Árvore limpa e imagens: api `a17b69e` ou mais nova, web `4305478`, worker `d12ed5d` (`docker ps`).
2. `OMNIRA_FLOWS_ENABLED=false` hoje; migrations até `000085` aplicadas (`schema_migrations`).
3. **Backup fresco:** `bash scripts/backup-omnira-db.sh` (anotar o nome do dump).
4. NATS saudável, os dois consumidores com 0 pendentes: `bash scripts/nats-jetstream-check.sh`.
5. Linha Meta `active` e sem tráfego recente: última mensagem recebida há mais de 30 min (consulta na seção 7).
6. Zero conversas retidas por bot: `select count(*) from conversations where automation_mode='bot'` deve dar `0`.
7. Telefone de teste escolhido (**diga qual número**) e confirmado que consegue escrever para o número Meta do tenant.

## 3. Ligar a flag (sem fluxo publicado, nada muda)

A API de fluxos só existe com a flag ligada, então ela vem **antes** de instalar/configurar. Worker primeiro, depois a API:

```bash
cd /data/home-moved/Projects/_legacy_lowercase_projects/20-OMNIRA
cp -p .env .env.pre-flows-smoke-$(date +%Y%m%d)        # cópia do .env (contém segredos: manter permissões)
printf '\nOMNIRA_FLOWS_ENABLED=true\n' >> .env
docker compose up -d --no-deps --force-recreate worker
until [ "$(docker inspect -f '{{.State.Health.Status}}' omnira-worker)" = healthy ]; do sleep 3; done
docker compose up -d --no-deps --force-recreate api
until [ "$(docker inspect -f '{{.State.Health.Status}}' omnira-api)" = healthy ]; do sleep 3; done
docker exec omnira-api sh -c 'echo FLOWS=$OMNIRA_FLOWS_ENABLED'     # true
docker logs --since 2m omnira-worker 2>&1 | grep -i flow            # consumidor/sweeper de fluxos iniciados
```
Conferir que o app responde e o menu **Automação** aparece para quem tem permissão `flow.view`.

## 4. Preparar o fluxo (dono, no app, logado como `tenant_admin`)

1. **Automação → Modelos e packs.** Perfil "Atendimento geral". **Instalar pack → OMNIRA Starter Pack.** Deixe os padrões (itens opcionais desmarcados; as dependências vêm sozinhas).
2. **Mapeamento de filas:** as 4 perguntas (suporte técnico, financeiro, comercial, triagem humana/fallback) → **`Default`**. Confirmar. Tudo vira **rascunho**; nada é publicado.
3. **Restringir ao canal Meta (obrigatório, antes de publicar):** abrir **Recepção inteligente → Configurações** e, em linhas de canal, marcar **somente o WhatsApp oficial (Meta)**. Salvar.
   Conferência independente (assistente): o fluxo deve ter o filtro da linha Meta.
   ```sql
   select slug, status, is_default, priority, trigger_filter from flows
   where tenant_id='11111111-1111-1111-1111-111111111111' and slug like 'smart-reception%';
   -- trigger_filter = {"connection_ids":["5e7aba50-3a8d-4c2f-b4d1-e2774653b9be"], ...}
   ```
   **Se o filtro estiver vazio, NÃO publicar.**
4. **Simular** (aba Simular) em "Recepção inteligente": provedor `meta_cloud`, contato "não classificado", eventos: `oi`, `João`, `Acme`, `3`. Esperado: saudação, pergunta do nome, da empresa, menu, e termina entregue a humano (fila comercial → `Default`). Repetir com "janela de 24h fechada": não pode enviar texto livre.
5. **Publicar, nesta ordem:** primeiro os subfluxos **Contato desconhecido**, **Contexto de empresa** e **Chamado existente**; **por último** a **Recepção inteligente** (a publicação fixa a versão de cada subfluxo). Os demais modelos ficam como rascunho.

## 5. Roteiro com o telefone (dono)

Enviar do telefone de teste para o número Meta do tenant. O telefone deve ser **contato novo** (se já existir como contato classificado, o caminho muda: pule a pergunta de nome/empresa).

| # | Faça | Esperado (aparece no telefone e na tela do operador) |
|---|---|---|
| T1 | Enviar `oi` | "Olá …! Bem-vindo ao atendimento." e logo "Para começar, qual é o seu nome?" |
| T2 | Responder um nome; depois uma empresa | "E qual é o nome da sua empresa?" e "Obrigado, …! Vou registrar seu contato…"; em seguida o menu "Como podemos ajudar?" com 1) Suporte técnico 2) Financeiro 3) Comercial 4) Outro assunto |
| T3 | Responder `3` | **Nenhuma mensagem nova no telefone** (o nó de transferência não envia texto; só move a conversa). Na tela: a conversa aparece na fila **Default**, sem responsável, com o resumo do handoff ("Comercial. Contato: …"); no banco `automation_mode='waiting_human'` |
| T4 | No Inbox, **assumir** a conversa e responder de lá; depois escrever de novo do telefone | A resposta humana chega; o **bot não responde mais** (nada de menu/saudação) |
| T5 | Resposta inválida no menu (em outra conversa nova, se quiser) | "Não consegui entender." e repete o menu até 3 vezes (opcional) |
| T6 | Fechar a conversa no app e escrever de novo do telefone | Nova conversa; a recepção recomeça (saudação) |
| T7 | **Desligar** (seção 8) e escrever do telefone | Sem bot: entra na fila padrão como sempre |

Cada mensagem do bot deve chegar **uma vez** (sem duplicata), e as respostas do bot não podem ser atribuídas a nenhum usuário (`sent_by_user_id` nulo).

## 6. Critérios

**PASS** exige T1 a T4 e T7 corretos, nenhuma mensagem duplicada, nenhum erro nos logs e nenhuma conversa de cliente real capturada indevidamente.
**ABORTAR e voltar (seção 8)** se: o bot responder **depois** de um atendente assumir; qualquer mensagem sair duplicada ou fora de ordem; um `flow_runs.status='failed'`; mensagem de cliente real receber o menu sem querer; erro nos logs de api/worker; fila `outbox_events` acumulando.

## 7. Evidência (assistente coleta; somente leitura)

```bash
q(){ docker exec omnira-postgres psql -U omnira -d omnira_dev -x -c "$1"; }
T=11111111-1111-1111-1111-111111111111
# tráfego recente na linha Meta (pré-voo)
q "select direction, created_at from messages m join conversations c on c.id=m.conversation_id where c.channel_connection_id='5e7aba50-3a8d-4c2f-b4d1-e2774653b9be' order by created_at desc limit 5"
# runs do teste
q "select r.id, f.slug, r.status, r.current_node_id, r.started_at, r.completed_at, left(coalesce(r.error,''),120) err from flow_runs r join flows f on f.id=r.flow_id where r.tenant_id='$T' order by r.started_at desc limit 5"
# passos do último run (caminho percorrido)
q "select seq, node_id, node_type, status, port, left(coalesce(error,''),80) err from flow_node_executions where flow_run_id=(select id from flow_runs where tenant_id='$T' order by started_at desc limit 1) order by seq"
# mensagens da conversa de teste (substituir CONV): bot = sent_by_user_id nulo
q "select direction, status, (sent_by_user_id is null) as bot, left(body,70) body, created_at from messages where conversation_id='CONV' order by created_at"
# estado da conversa
q "select automation_mode, status, queue_id, assigned_to_user_id, conversation_kind from conversations where id='CONV'"
# jobs presos / em quarentena
q "select event_type, count(*) filter (where published_at is null) pendentes, count(*) filter (where quarantined_at is not null) quarentena from outbox_events where created_at > now()-interval '1 hour' group by 1"
# conversas retidas pelo bot (deve voltar a 0 ao final)
q "select count(*) from conversations where automation_mode='bot'"
```
Logs: `docker logs --since 30m omnira-api 2>&1 | grep -ciE 'panic|fatal| error'` e o mesmo no `omnira-worker`; `bash scripts/nats-jetstream-check.sh`.
Registrar o resultado em `docs/flows/SMOKE-REAL-PHONE-RESULT.md` (tabela T1 a T7 com PASS/FAIL, ids de run/conversa, trechos de log, hora de início e fim, quem executou).

## 8. Voltar atrás (a qualquer momento)

1. **Terminar ou entregar as conversas de teste** (handoff ou fechar). Conferir `select count(*) from conversations where automation_mode='bot'` = 0: o sweeper só roda com a flag ligada, então conversas retidas pelo bot ficariam sem dono depois de desligar.
2. Opcional, mais fino: **Arquivar** os fluxos no app (para de capturar mesmo com a flag ligada).
3. Desligar: remover a linha `OMNIRA_FLOWS_ENABLED=true` do `.env` (ou restaurar `.env.pre-flows-smoke-AAAAMMDD`) e recriar `worker` e depois `api`:
   ```bash
   docker compose up -d --no-deps --force-recreate worker api
   docker exec omnira-api sh -c 'echo FLOWS=$OMNIRA_FLOWS_ENABLED'    # false
   ```
4. Schema só se for preciso: `down` de 085, 084, 083, 082 (nessa ordem; testados). Os dados dos fluxos ficam inertes e não atrapalham.
5. Se algo foi mexido por engano: restaurar o backup do passo 2 do pré-voo (procedimento em `docs/ops/RUNBOOK-INBOX-WAHA.md`).

## 9. O que este teste NÃO cobre

Linha WAHA, janela de 24h realmente expirada (coberta só por teste automatizado e simulador), carga, IA (`OMNIRA_FLOWS_AI_ENABLED` segue desligada) e a rodada final do Codex com Docker. Passar neste roteiro **não** libera `INTERNAL_PILOT` sozinho: as demais condições do gate continuam valendo.
