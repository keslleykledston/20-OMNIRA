# Piloto da API headless (ADR-0048) — plano para ligar, observar e desfazer

Estado em 2026-10-17: código **implantado em produção com as flags desligadas** (`sha-5e06f4c`, migrations até a 164). Nenhuma instalação externa existe. Nada abaixo foi executado em produção: este é o plano, com o ensaio já feito num ambiente descartável. Complementa o `HEADLESS-RUNBOOK.md` (como operar) — aqui está **a ordem do piloto**.

## 1. O que já está provado (e o que não está)

| Prova | Onde | Resultado |
|---|---|---|
| Ensaio do piloto com os **binários reais** (api, worker, simulador de CRM) num Postgres e NATS descartáveis | `scripts/e2e-headless-pilot.sh` | passa: flags desligadas = 404; administrador instala aplicação só-leitura limitada a UMA fila, token aparece uma vez; ela vê só a fila dela (não outra fila, não spam, não outra empresa); o diário entrega a mudança de controle e a mensagem; escrita recusada com a flag desligada; com a flag ligada: assumir → enviar (enfileira) → auditoria; token de outra empresa não toca; suspender corta o acesso na hora e devolve o que segurava; desligar remove a superfície |
| Isolamento, escopos, retentativa, assinatura, concorrência | testes Go em PostgreSQL real (`internal/external/...`, `internal/conversationcontrol`) | passam |
| Eventos de serviço (protocolo, ocorrência, tarefa, ERP) para a aplicação | migration 164 + testes | passam; precisam do escopo `service.read` |
| Restauração de backup com as tabelas novas | `scripts/backup-restore-check.sh` | ver `docs/service-context/11` |
| **Não provado**: entrega de **webhook** por HTTPS a um endpoint público (o guarda SSRF recusa loopback de propósito), envio por **WhatsApp real**, um CRM de verdade | — | é o que o piloto existe para provar |

## 2. Decisões que só você pode tomar (minha recomendação em negrito)

| # | Decisão | Opções | Recomendação |
|---|---|---|---|
| 1 | **Empresa do piloto** | K3G_Solutions (produção real) · Keslley_Pessoal · Test Company | **Começar na Test Company** (P0/P1), só depois uma empresa real (P2) |
| 2 | **Qual "CRM"** | o simulador (`crm-sim`) · um CRM real (qual? quem integra?) | **simulador em P0/P1**; CRM real só em P2, com o integrador identificado |
| 3 | **Fila e linha do piloto** | uma fila e uma linha de WhatsApp | **uma fila nova "Piloto CRM"** e **o número de teste que você já usa** — nunca a fila de clientes reais em P0/P1 |
| 4 | **Escrita** | só leitura primeiro · leitura+escrita | **leitura primeiro (P0), escrita em P1** com o número de teste |
| 5 | **Eventos de serviço** (`service.read`) | incluir · deixar fora do piloto | **fora do P0/P1**; incluir em P2 se o CRM quiser protocolos |
| 6 | **Quem acompanha** | você · supervisor | **você presente em P1 e nas primeiras 24 h de P2** |
| 7 | **Endpoint de webhook** | público https (porta 443/8443) · só leitura por consulta | **sem webhook em P0/P1** (o simulador consulta `/events`); webhook só em P2, com endereço público real |

## 3. Etapas

**P0 — leitura (Test Company, simulador, sem escrita)**
1. Backup (`scripts/backup-omnira-db.sh`) e `scripts/headless-pilot-check.sh` (tudo zerado).
2. `.env`: `OMNIRA_HEADLESS_ENABLED=true` (a de escrita continua `false`). `docker compose up -d --no-deps --force-recreate api worker` (≈10 s de 502 na api; fazer em horário calmo).
3. Na tela: Configurações → Integrações → Aplicações externas → **Nova aplicação** na Test Company, escopos `conversation.read`, `message.read`, `contact.read`, `events.read`, filtrada pela fila "Piloto CRM" e pela linha de teste. O token aparece **uma vez**.
4. `OMNIRA_EXTERNAL_URL=https://omnira.devops.k3gsolutions.com.br/api/v1/external OMNIRA_EXTERNAL_TOKEN=… crm-sim bootstrap` (binário: `go build ./apps/crm-sim`). Deve listar só as conversas da fila.
5. Mandar uma mensagem de teste para o número; `crm-sim list` e `GET /external/events` mostram o fato. `scripts/headless-pilot-check.sh` após cada passo.
6. Critério de saída: 48 h sem nenhum item dos critérios de abortar.

**P1 — escrita com o número de teste (você presente)**
1. `OMNIRA_HEADLESS_WRITE_ENABLED=true` + recriar api. Novo token com `conversation.claim` e `message.send`; revogar o de leitura.
2. `crm-sim claim <conv>` → a conversa aparece no Inbox como «CRM», sem campo de resposta; `crm-sim send <conv> <versão> "texto"` → **a mensagem chega no seu celular** (a primeira prova com WhatsApp real); `crm-sim release`.
3. Testar o retorno: um supervisor com `conversation.takeover_external` retoma a conversa; suspender a aplicação na tela devolve a conversa e corta o token.
4. Lease: definir 120 s, deixar o simulador mudo e ver a conversa voltar para a fila (`lease_expired`).

**P2 — empresa real, CRM real, 24 h supervisionadas** (só depois de P1 limpo)
Conforme a seção 3 do runbook: escopos de leitura primeiro, filtro de fila/linha, endpoint de webhook público (segredo aparece uma vez; conferir assinatura e repetição), depois escrita. Incluir `service.read` se o CRM quiser os fatos de protocolo.

**P3 — segundo CRM** só após 7 dias limpos.

## 4. Critérios de abortar (vêm do runbook, com o comando que mostra cada valor)

`scripts/headless-pilot-check.sh` imprime todos: (a) entregas de webhook `dead` crescendo sem o CRM estar fora do ar; (b) fato sem número há > 60 s; (c) conversa segurada por aplicação que não está ativa, ou mensagem enfileirada depois de perder o controle; (d) 5xx > 1 % por 10 min (métrica `omnira_external_requests_total`); (e) esperas de lock no Postgres crescendo.

## 5. Desfazer (em ordem; nada se perde)
1. `OMNIRA_HEADLESS_WRITE_ENABLED=false` + recriar api.
2. Suspender as aplicações na tela (devolve as conversas ao roteador).
3. `OMNIRA_HEADLESS_ENABLED=false` + recriar api e worker (as rotas somem; o diário para; as tabelas ficam).
Reverter as migrations só com backup e ordem sua (`HEADLESS-RUNBOOK.md` §7).

## 6. Riscos que o piloto herda (declarados)
- **Disco**: `/var` do host está em 97 % (≈8,4 GB livres). O diário de fatos (30 dias) e o NATS crescem pouco num piloto, mas o host precisa de limpeza antes de qualquer crescimento — vale tratar à parte.
- **Linha oficial (Meta)**: fora da janela de 24 h só vai template (422 `window_closed`).
- **Ator do CRM não verificado**: `external_agent_id` é só rótulo (limite declarado no runbook §8).
- **Réplicas**: o limite por minuto é por processo da api (hoje há uma só).
