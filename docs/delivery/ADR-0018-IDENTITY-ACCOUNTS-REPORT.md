# Relatório de entrega — identidade interna, contatos externos, empresas e classificação (ADR-0018)

Baseline `3bbb879` · tudo em commits **locais** na `main`; **nada** foi enviado (push), implantado, migrado no banco vivo ou
reiniciado. O banco vivo continua na migration 000072.

## Ondas

| Onda | Commit | Resultado |
|---|---|---|
| 0 ADR + discovery | `0de2e18` | PASS |
| 1 empresas (`customer_accounts`, `account_external_links`) | `3db8b8f` | PASS |
| 2 classificação + vínculos | `1fa0043` | PASS |
| 3 API/UI de edição do contato | `afacb68` | PASS |
| 4 identidades internas verificadas | `8e9d6d7` | PASS |
| 5 resolvedor de entrada + `conversation_kind` | `99bcb55` | PASS |
| 6 evidência CRM | `5261a71` | PASS |
| 7 tickets | `8cb678a` | PASS |
| 8 contexto de empresa do assunto | `922da9c` | PASS |
| 9 pessoas unificadas + Inbox | `3b9bac4` | PASS |
| 10 métricas, painel, página Empresas, docs | este commit | PASS |

Gate final: `go build/vet ./...` limpo; `go test ./...` 67 pacotes ok; **27/27** pacotes de integração em Postgres real
(`bash scripts/test-integration.sh`, um banco descartável por pacote); web `tsc` limpo, **548** testes, `vite build` ok;
`git diff --check` limpo; varredura de segredos limpa; nenhum arquivo do dono (`docker-compose*.yml`) entrou nos commits.

## Banco
Migrations 000073–000078, todas com `down` provada up/down/up (`scripts/test-migration-roundtrip.sh`, no momento em que cada
uma era a última) e aplicadas em banco novo pelo gate de integração. FORCE RLS e FK composta por tenant em toda tabela nova
(`customer_accounts`, `account_external_links`, `contact_account_links`, `user_channel_identities`,
`identity_resolution_conflicts`, `topic_account_links`); nenhuma tem política de DELETE exceto `topic_account_links`.
Backfill da 074 provado em Postgres descartável (`scripts/test-migration-074-backfill.sh`): ninguém vira cliente sem prova,
`agent`→`other`, padrão não escolhido→`unclassified`, decisões manuais mantêm o tipo com o ator auditado. Nada destrutivo.

## API (OpenAPI atualizado; o teste de drift rota↔contrato passa)
`/accounts` (+`/{id}`, `/{id}/tickets`), `/contacts/{id}/classification`, `.../accounts` (+`/{link}/end`, `/primary`),
`.../company-suggestions`, `/identities` (+verify/revoke), `/identity-conflicts` (+resolve), `/people`,
`/topics/{id}/account-context`, `/topics/{id}/accounts`; `conversation_kind`/`internal_user_id` no Inbox. Permissões novas:
`account.read`, `account.manage`, `contact.classify`, `identity.manage` (esta só do administrador).

## Frontend
Contato: editor de classificação (várias empresas, principal, remoção guiada da última), seção "Integração (CRM)" separada;
página "Pessoas" (Todos/Clientes/Outros/Não classificados/Internos/Spam; staff sem classificação e com "Gerenciar acesso");
Inbox com filtros "Não classif."/"Internas" e selos; painel do assunto com a empresa; painel com o backlog de não classificados;
a página "Empresas" real substitui o mock legado.

## Chamadas externas
Nenhuma escrita em K3G/WAHA/Meta (esperado 0, observado 0): os testes usam diretório/runtime/conector falsos e o Postgres
descartável. Nenhum contato é criado no CRM (auto-criação agora é opt-in, desligada). A leitura de `FindCustomerByPhone` deixou
de usar `contacts[0]` (`ErrCRMAmbiguous`).

## Cenários de aceite A–J
| | Cenário | Prova |
|---|---|---|
| A | DM de agente interno | `TestStaffMessageBecomesAnInternalConversationWithoutAContact` (sem Contato, sem fila, sem ticket) |
| B | "Olá" de desconhecido | `TestUnknownSenderIsUnclassifiedAndClassificationRecomputesTheKind` |
| C | classificar como cliente ACME | idem + `TestPutClassificationDirectoryCompanyIsValidatedServerSide` |
| D | mesmo contato, 2ª empresa XPTO | idem (2 vínculos, 1 principal) + `TestSecondCompanyKeepsBothAndOnlyOnePrimary` |
| E | dois assuntos/chamados | `TestTwoSubjectsOfOneConversationCarryTheirOwnCompanies` |
| F | grupo misto | `TestGroupKindFollowsEachParticipantIndividually` |
| G | grupo só de internos | idem |
| H | conflito de identidade | `TestIdentityConflictKeepsCustomerAutomationOffUntilAHumanResolvesIt` |
| I | remover última empresa + reclassificar | `TestEndLinkLastCompanyNeedsReclassifyAndIsAudited` |
| J | UUIDs cruzando tenants | testes de isolamento em contas, contatos, vínculos, identidades, tópicos e pessoas (sempre 404/422, nunca vazamento) |

Mutações provadas (o teste falha quando a regra é quebrada): savepoint de atomicidade, pendente/membership revogada casando,
conflito aberto ignorado, revalidação da evidência, adivinhar entre duas empresas.

## Lacunas
- **P0**: nenhuma.
- **P1**: responder numa conversa `internal` (envio devolve 404; o recebimento funciona); o painel de chamados não pré-seleciona
  a empresa do contato (o atendente escolhe, validado no servidor); fluxo de verificação por desafio/`provider_verified`
  (reservado, não construído); tela de gestão das identidades internas e dos conflitos (só API); migração dos contatos já
  existentes para `trusted_crm` (a evidência não guarda o nome da empresa: só sugestão, hoje 0 linhas).
- **P2**: IA sugerindo classificação/empresa (flag existe, nada implementado: por desenho a IA só sugere); backfill de
  `conversation_kind` para grupos pelo telefone de `@lid` (só `@c.us` carrega o telefone); gráficos/analytics por tipo de conversa
  além do painel; `PRODUCT.7B2D` segue bloqueado na unicidade do K3G.
- **BLOCKED**: aplicar no banco vivo / deploy / push (precisam de autorização explícita do dono).
- **OUT**: operadores reais (nomes/e-mails/papéis) para criar usuários, memberships e perfis de agente — ainda não informados.
