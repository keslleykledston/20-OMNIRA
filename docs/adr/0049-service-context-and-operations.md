# ADR-0049: Service Context & Operations (atendimento com protocolo, linha do tempo, ocorrências, tarefas e registros externos)

## Status
**Proposto — em implementação na branch `feat/omnira-service-context-operations` (2026-10-15).** Nada implantado. Todas as capacidades nascem atrás de flags desligadas.

## Contexto
O OMNIRA modela a comunicação (conversas/mensagens), o assunto lógico (ADR-0017), o chamado do ERP como projeção (ADR-0013) e o fim de atendimento como fechamento da conversa (ADR-0020). Faltam: protocolo de atendimento, histórico operacional estruturado, ocorrências que atravessam atendimentos, tarefas e vínculo de várias OS externas. Auditoria completa em `docs/service-context/00-audit.md`.

## Decisão
1. **Atendimento (`service_sessions`) é uma camada sobre a conversa**, com protocolo por tenant (`AT-<n>`, contador transacional, nunca `MAX+1`). Suporta N sessões em sequência por conversa (no máximo uma ativa). Não altera a ADR-0020: *Finalizar* continua fechando a conversa e, com flag, fecha também a sessão ativa na mesma transação.
2. **Mensagens não são copiadas nem marcadas**: pertencem à sessão quando seu `created_at` cai num **episódio** da sessão (`[started_at, ended_at)`, meio-aberto, relógio do banco). Reabrir inicia outro episódio e não absorve o tempo fechado.
3. **Linha do tempo (`context_events`)**: append-only, payload versionado só com ids/enums/contagens. **Não** emite ao `outbox_events` (o publisher só tem stream para `job.*`); a entrega a terceiros reaproveita o motor da branch headless.
4. **Ocorrências (`service_cases`) e tarefas (`service_tasks`)** são entidades independentes de conversa, com estados próprios e sem transição cruzada implícita.
5. **ERP é a fonte de verdade das OS** (ADR-0013 estendida): `external_records` guarda referência/projeção/histórico; solicitar OS usa tentativa durável com `outcome_unknown` que bloqueia retry até reconciliar.
6. **Segurança**: RLS forçado, escrita no banco com `actor_has_permission`, 7 permissões `service.*`, domínio `service` para delegação do Hub (`request_external` nunca delegável), IA somente propõe (`pending_approval`).
7. **Migrations 000150–000159** (faixa reservada; a branch headless usa 137–141 e precisará renumerar).

## Consequências
- Relatórios por protocolo, resultado canônico e setor passam a existir sem tocar `messages`/`tickets`.
- Duas coisas parecidas convivem: `follow_up_items` (promessas do contato, ADR-0020) e `service_tasks`; a visão agregada resolve a leitura, não há migração de dados.
- A entrega de eventos a terceiros e a autenticação de aplicação externa dependem da branch headless; até lá os eventos ficam no outbox.

## Alternativas descartadas
Reusar `tickets`, `topic_threads` ou `follow_up_items`; coluna `messages.session_id`; sessão por mensagem; OS própria no OMNIRA; novo motor de webhooks. Motivos em `docs/service-context/01-architecture.md` §9.


## Addendum 2026-10-17 — uma ação só: Finalizar encerra o protocolo (decisão do dono)
Depois do teste ao vivo do aviso na K3G_Solutions (KTG-1006) o dono decidiu: **"Finalizar" é a única ação de encerramento e já fornece o protocolo; não há duas funções distintas. Todo atendimento gera protocolo (rastreabilidade); só é opcional informar o número ao cliente em texto.**
- A tela do protocolo (painel "Protocolo e tarefas") **perdeu o botão "Encerrar atendimento com protocolo"** e diz "O protocolo encerra junto com o atendimento, em Finalizar". A API de encerrar só o protocolo (`POST /service-sessions/{id}/close`) e o nó `close_service_session` dos fluxos continuam existindo para integrações; a tela não os oferece.
- **Finalizar** (membro ou agente do Hub): encerra a conversa **e** o protocolo (resultado escolhido, ou o automático); um atendimento que ninguém assumiu **ganha o protocolo ao terminar** e o cliente é informado desse número (a abertura não foi avisada porque não houve).
- Novo campo opcional `notify_protocol` (padrão `true`) no corpo de finalizar: na tela, a caixa "Informar o número do protocolo ao cliente" (ligada). Desligada → o protocolo é criado/encerrado do mesmo jeito e **nada** é enviado sobre o número. O aviso continua dependendo da capacidade da empresa (`service_protocol_notice`) e de poder enviar texto; **spam e duplicado nunca recebem aviso** (o protocolo fica, para rastrear). O texto de despedida (`farewell`) é outra mensagem, independente.
- Código: `EnsureForFinalize` (cria o protocolo antes do aviso, sem permissão extra e sem anunciar), `FinalizeNotice(pool, notice, svc)`, `SessionNotice` recebe o motivo. Testes: `TestFinalizingAnAttendanceNobodyAssumed…`, `TestTheProtocolIsNotOptionalButTellingTheNumberIs` (4 cenários) com mutação nos três pontos.
- Fora de escopo, por decisão: fechamento por fluxo (`source=flow`) e por IA não enviam aviso (não há pessoa para enviá-lo); o protocolo é criado e encerrado do mesmo jeito.
