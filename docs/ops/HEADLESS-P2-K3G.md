# P2 do piloto headless — K3G_Solutions + CRM da K3G (plano preenchido)

Estado: **PLANO, nada executado.** Complementa `HEADLESS-P2-CHECKLIST.md` (pré-requisitos, passos, como desfazer) e `HEADLESS-PILOT-PLAN.md`. As respostas abaixo são do dono, 2026-10-17.

## 1. Dados combinados

| Item | Resposta |
|---|---|
| Responsável pela integração / contato de emergência | Matheus — `sistema2@k3gsolutions.com.br` (administrador da K3G_Solutions) |
| Eventos | **CORRIGIDO 2026-10-17 (dono): não há webhook. O CRM consulta a API do OMNIRA** (`GET /external/events?after=<seq>`, partida em `/sync/checkpoint`). Não existe endpoint no CRM a cadastrar. (A anotação anterior sobre o host `api.k3gsolutions.com.br` fica sem uso) |
| Assinatura `X-Omnira-Signature` | **não se aplica** (sem webhook). Resta o descarte de `event_id` repetido no laço de consulta do CRM |
| O que o CRM fará | ler, assumir, enviar mensagem, transferir, finalizar, "abrir OS e tudo mais" (ver §3: a API externa não cobre tudo isso) |
| Prazo de posse (lease) | **a perguntar** — recomendação abaixo |
| Volume / limite | sem limite hoje no CRM — recomendação abaixo |
| Quem recebe o token e por qual canal | **a perguntar** (o token aparece uma única vez) |

## 2. Recomendação de limites (factível e seguro)

A API externa limita **por instalação** (padrão 600 req/min; faixa 10–6000) e responde `429` + `Retry-After`. Um CRM de uma empresa, uma linha de WhatsApp, consulta o diário (`/external/events`) e age em poucas dezenas de conversas por hora: 10 req/s sobra.

| Fase | Taxa | Lease | Escopos |
|---|---|---|---|
| A — 24 h só leitura | **300/min** | — | `conversation.read`, `message.read`, `contact.read`, `media.read`, `events.read`, `service.read` |
| B — escrita (só depois de 24 h limpas, com o Matheus e o integrador na linha) | **600/min** (padrão) | **900 s (15 min)** com `heartbeat`; o CRM que parar de responder devolve a conversa ao roteador sozinho | A + `conversation.claim`, `message.send`, `conversation.transfer`, `conversation.finalize` |
| C — ampliar fila/linha | subir só com evidência (p95, `429`, atraso do laço de consulta) | idem | idem |

Regra de segurança da fase B: **começar com UMA fila (recomendo Suporte)**, não com a empresa toda. As demais filas entram na fase C. Quem assume a conversa primeiro segura; a pessoa do OMNIRA só a retoma depois que o CRM soltar ou for suspenso (retomada pelo supervisor = ainda a testar na tela).

Pedir ao CRM: backoff exponencial em `429`/`5xx`, `Idempotency-Key` em todo `POST`, `If-Match` com a versão de controle, máximo de 5 chamadas simultâneas.

## 3. "O CRM deve fazer tudo o que o OMNIRA faz" — o que a API externa cobre

Cobre: listar/ler conversas, mensagens, mídia e contato; assumir, soltar, transferir, finalizar; enviar mensagem; diário de eventos (consulta pelo CRM); eventos de serviço (protocolo, ocorrência, tarefa, OS) **somente leitura**.

**Não cobre (hoje), e precisa de decisão se for necessário:**
- **Abrir OS pelo OMNIRA:** não há escopo na API externa e o adaptador de ERP não existe (`503 not_configured`; falta o contrato do fornecedor, `docs/service-context/16`). Se a OS nasce no próprio CRM, o CRM abre lá e o OMNIRA só recebe o fato; vincular essa OS à sessão exigiria um escopo novo (ADR).
- Abrir/alterar chamado, notas do contato, pendências, IA, filas, pessoas, fluxos, configurações: são telas/rotas internas do OMNIRA, não da API externa.
- Isso é desejável por segurança (o CRM é um operador de conversas, não um administrador do tenant). Se o CRM precisar de algo dessa lista, cada item vira escopo próprio com Codex e teste de duas empresas.

## 4. O que falta de cada lado

**Equipe do CRM:** laço de consulta do diário (intervalo 2–3 s, guarda o último `seq`, trata `410 resync_required`) e idempotência por `event_id`; valor de lease; canal seguro e destinatário do token; confirmação do que precisam além do §3.

**Dono:** criar a aplicação na K3G_Solutions (Integrações → Nova aplicação) com os escopos da fase A, taxa 300; entregar o token; cadastrar o endpoint; acompanhar a fase B.

**Eu:** `headless-pilot-check.sh` de hora em hora na fase A, relatório ao fim das 24 h, e os testes de duas empresas para qualquer escopo novo.

## 5. Critérios de abortar e desfazer

Os do `HEADLESS-PILOT-PLAN.md` §4 (fato sem número > 60 s, atraso do laço de consulta crescendo, conversa presa a aplicação inativa, bloqueios de lock, 5xx). Desfazer: suspender a aplicação na tela → `OMNIRA_HEADLESS_WRITE_ENABLED=false` → revogar. O OMNIRA continua atendendo sozinho em qualquer um desses passos.
