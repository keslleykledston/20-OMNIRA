# Finalizar atendimento e memória do contato (ADR-0020)

Estado: **implantado em 2026-10-06 20:29 -0400** (migrations 086 e 087 aplicadas ao `omnira_dev`; `api` `20-omnira-api:1b04d65` e `web` `20-omnira-web:1b04d65`; rollback: tags `rollback-pre-adr0020-20261006`). Flags de IA desligadas. Decisão e justificativas: `docs/adr/0020-attendance-finalization-and-contact-memory.md`.

## O que o atendente vê
- **Finalizar atendimento** (painel de contexto do Inbox): motivo, resumo, e o que ficou **pendente**, foi **prometido** ou vale **lembrar** (com prazo opcional).
  Encerra o atendimento, não o WhatsApp: se o contato escrever de novo, abre-se um atendimento novo (passa pelo roteamento e pelo bot).
- **Sugerir com IA**: preenche um rascunho editável do resumo e dos itens (marcados "Sugerido pela IA"). O resumo só é registrado como da IA enquanto não for editado.
- **Pendências do contato** e **Atendimentos anteriores** no painel de contexto, com Concluir/Descartar por item.
- **Histórico do contato**: busca por trecho nas mensagens anteriores do mesmo contato (só quando a pessoa pede).
- Aba **Encerradas** na lista; a lista padrão mostra só atendimentos abertos. A conversa finalizada não aceita resposta.

## Regras
- Quem pode finalizar: o responsável pela conversa (`conversation.claim`) ou quem tem `conversation.manage` (supervisor, administrador). Conversa sem responsável: só `manage`. Sem permissão nova.
- Chamados **locais** da conversa são encerrados; chamados ligados ao ERP ou escopados a um assunto **não** (a tela avisa quantos ficaram).
- Idempotente: finalizar de novo devolve o fechamento original. Uma conversa finalizada é final (não há "reabrir"; ver reversão abaixo).
- Textos livres recusam padrões de credencial (Bearer, chave PEM, AKIA…): não cole senhas.

## API (contrato `contracts/openapi/attendance-v1.yaml`, 6 operações)
`POST …/inbox/conversations/{id}/finalize` · `POST …/finalize/suggest` · `GET …/attendance-context` · `GET …/history-search?q=` · `GET …/contacts/{id}/attendance-history` · `POST …/follow-ups/{id}/resolve`.
Também: `GET …/inbox/conversations?status=open|closed|all` (padrão `open`).

## Copiloto e ferramentas (IA, tudo desligado por padrão)
| Variável | Efeito |
|---|---|
| `OMNIRA_COPILOT_CONTACT_MEMORY_ENABLED` | o copiloto vê os últimos atendimentos e as pendências abertas **do mesmo contato** (zona não confiável; prompt `copilot-reply-v2` só quando há memória). Precisa de `OMNIRA_COPILOT_ENABLED` |
| `OMNIRA_AI_TOOL_GATEWAY_ENABLED` | libera o gateway; ferramentas de leitura `contact.recent_attendances`, `contact.open_followups`, `contact.search_history` (contato derivado do assunto, nunca por argumento) |
| `OMNIRA_COPILOT_ENABLED` + `OMNIRA_AI_MODEL_CLOSING_SUGGEST` | habilita "Sugerir com IA" (se a variável do modelo faltar, usa o modelo padrão) |

**Estado em 2026-10-07:** IA da plataforma ligada com **Gemini** (`OMNIRA_AI_ENABLED=true`, `OMNIRA_AI_PROVIDER=gemini`, `OMNIRA_AI_MODEL=gemini-3.5-flash-lite`) e `OMNIRA_COPILOT_ENABLED=true`, junto com `OMNIRA_COPILOT_CONTACT_MEMORY_ENABLED=true`, na `api` e no `worker`. Continuam **desligadas**: `OMNIRA_AI_TOOL_GATEWAY_ENABLED` (ferramentas de leitura) e `OMNIRA_FLOWS_AI_ENABLED`. Guia do provedor: `docs/ai/PLATFORM-AI-PROVIDERS.md`. Finalizar foi testado em produção; o copiloto e "Sugerir com IA" aguardam o primeiro uso real no Inbox.

A IA só propõe: nada que ela escreve vale como fato até uma pessoa confirmar; falha da IA nunca impede finalizar à mão.

## Implantação
1. Backup (`scripts/backup-omnira-db.sh`) e migrations `000086` e `000087` (`docker compose run --rm migrate`). Aditiva: duas tabelas (`conversation_closures`, `follow_up_items`).
2. Reconstruir e recriar `api` e `web` (o `worker` não muda). Sem flag para o núcleo: o botão aparece a quem tem as permissões acima.
3. Decidir depois as flags de IA.

## Reversão
- Migrations: `000087_append_only_tables.down.sql` e depois `000086_attendance.down.sql` (apagam os dois registros; as conversas ficam fechadas). A 087 também revoga `UPDATE/DELETE` do `omnira_app` nas tabelas append-only do Flow Builder.
- Reabrir uma conversa por engano (não há tela): `UPDATE conversations SET status='open', closed_at=NULL WHERE id='…' AND status='closed';` e, se quiser, apagar o fechamento (`conversation_closures` é imutável: a aplicação não atualiza nem apaga, e um gatilho impede `UPDATE` até do dono; o dono do banco ainda pode apagar manualmente, e a cascata de conversa/tenant funciona).
- Voltar a imagem anterior: tags `rollback-*` das imagens `20-omnira-api` e `20-omnira-web`.

## Legado e pendências
- As conversas abertas hoje (centenas) **não** são fechadas automaticamente. Fechamento em lote com simulação prévia, finalização por inatividade e o nó `fechar conversa` do Flow Builder ficam para decisões futuras.
- Adiado: resolução automática de tópicos por evento (ver ADR, seção 5).
