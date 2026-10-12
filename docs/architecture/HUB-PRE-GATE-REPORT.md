# Pré-gate formal do Hub (fase 13) — relatório de 2026-10-12

Estado do projeto: `LAB`. Este relatório **não é o gate**: o gate formal exige o aceite escrito do dono (ver o fim). Ele reúne o que o Codex (revisor independente, somente leitura, que **não** roda testes) encontrou nas três camadas do Hub, o que foi corrigido, o que foi aceito e por quê, e o que só o dono pode decidir. Saída bruta de cada revisão: sessões do Codex citadas; nada aqui amplia o que elas disseram.

## 1. Como foi feito

Três revisões adversariais independentes, cada uma assumindo um atacante diferente, mais as revisões de cada lote anterior do dia:

| Camada | Atacante assumido | Sessão Codex |
|---|---|---|
| Banco (migrations 093..119, RLS, funções `SECURITY DEFINER`, contexto de sessão assinado) | executa **SQL arbitrário** como `omnira_app` | `01a1319d-7df1-71c1-ae3c-8d940dd2282e` |
| Go (autorização, delegação, OIDC, convites, painel de acessos) | pessoa autenticada comum ou administradora tentando passar do que lhe cabe | `01a1319d-7e2e-7450-8b40-223f6b6ed413` |
| Navegador (SPA, nginx, cookies, cache) | outra pessoa no mesmo navegador; site de fora | `01a1319d-…` (3ª tarefa, ver o histórico) |

Resultado bruto: **CRITICAL 0** nas três; **HIGH 4 + 1 + 2**; MEDIUM 4 + 3 + 4; LOW 2 + 2 + 2. Nenhuma camada foi declarada "sem achados".

## 2. Destino de cada achado

### Banco
| Achado | Destino |
|---|---|
| H-01 `audit_events`/`outbox_events` aceitavam INSERT de qualquer pessoa para qualquer empresa e em nome de qualquer ator | **CORRIGIDO** na migration 121 (ator = a pessoa da sessão; empresa = a que ela pertence, atende por concessão viva ou gerencia pelo Hub; sessão de sistema livre). Provado contra as 444 linhas de auditoria de produção (só 3 sem vínculo de membro, todas por Hub/sistema), pelo gate 37/37 e pelos E2E |
| H-02 política delegada de auditoria lia a GUC crua | **NÃO SE APLICA ao estado final**: a política viva usa `acting_hub()` verificado (conferido no banco de produção) |
| H-03 migration 118 instala em modo `off` | **ACEITO com guarda**: produção está em `enforce` (conferido ao vivo), `OMNIRA_SESSION_CTX_REQUIRE_ENFORCE=true` recusa subir o processo fora dele e `scripts/hub-health-check.sh` dá FAIL se algum processo vê o banco fora de `enforce` |
| H-04 quem pertence a duas empresas lê as duas por SQL | **POR DESENHO**: a pessoa é membro das duas; o vínculo requisição→empresa é da aplicação. Fechar isso exigiria a empresa dentro do contexto assinado (decisão de arquitetura, não correção) |
| M-01 `lock_served_tenant` podia trocar o contexto agindo no meio da transação | **CORRIGIDO** (121: um contexto por transação, repetir o mesmo par é idempotente) |
| M-02 `WITH CHECK` delegados sem escopo de fila ao trocar `contact_id` | **ACEITO**: exige a chave delegada **e** SQL arbitrário; só dado da mesma instância que a pessoa já atende |
| M-03 `delegated_materialize_company_account` confia no id externo | **ACEITO**: exige a chave `ticket.*`; a validação com o ERP é da aplicação; efeito limitado à mesma instância |
| M-04 `person_works_in_other_instance` é um oráculo booleano | **ACEITO**: é a função do painel, gateada por permissão de gestão (desenho de ADR-0039) |
| L-01 reutilização do token após `ROLLBACK TO SAVEPOINT` | **ACEITO**: não permite forjar identidade, só repetir dentro da mesma transação |
| L-02 `CREATE` em `public` | **CONFERIDO** (a ACL viva dá só `USAGE` a `omnira_app`) e **REVOGADO explicitamente** na 121 |

### Go
| Achado | Destino |
|---|---|
| H-01 autorização por e-mail aplicada a conta errada | **MITIGADO em código**: a autorização acompanha uma identidade **nova** ou a mesma identidade cujo **mesmo** endereço acabou de ser verificado; uma identidade existente que **mudou** de endereço no provedor não a leva (teste que falha sem a regra). **Pré-condições do provedor, a conferir pelo dono:** o realm de produção tem `registrationAllowed=false` e `duplicateEmailsAllowed=false` (conferido), **mas deixa a própria pessoa editar o e-mail** (`email.edit: [admin, user]` no perfil de usuário, `verifyEmail=false`): restringir a edição a `admin` fecha a porta pela origem (ver §4) |
| M-01 rotas do Hub no balde global anônimo | **CORRIGIDO**: cota por pessoa (`ratelimit.EnforceUser`) em toda rota do Hub e no sinal de mudança |
| M-02 corrida entre revogação do contrato e escrita de administrador de instância / `can_manage` | **CORRIGIDO**: contrato e empresa travados `FOR SHARE` na transação da escrita (teste com revogação em voo) |
| M-03 callback OIDC devolvia o texto do erro interno | **CORRIGIDO** (mensagem genérica; a causa vai ao log) |
| L-01 validade do convite pelo relógio da aplicação | **CORRIGIDO** (relógio do banco) |
| L-02 normalização de e-mail sem NFC/IDNA | **ACEITO**: pode fazer uma autorização válida não casar; não escala privilégio |

### Navegador
| Achado | Destino |
|---|---|
| H-01 cache de consultas sobrevive ao logout | **CORRIGIDO** (`queryClient.clear()` ao entrar, sair e no logout da página "Sem Acesso"; rascunhos em memória e chamados em criação saem com a sessão) |
| H-CSRF só `SameSite=Lax` | **CORRIGIDO como defesa em profundidade**: escrita autenticada por cookie só passa se `Origin` (ou `Referer`) for a própria aplicação (origens de `OMNIRA_AUTH_REDIRECT_URL`/`OMNIRA_WEB_BASE_URL`/`OMNIRA_ALLOWED_ORIGINS`, o `Host`/`X-Forwarded-Host` do pedido ou loopback); sem `Origin` nem `Referer` é recusada; `OMNIRA_ORIGIN_CHECK=false` desliga |
| M-01 chaves de cache sem o Hub agindo | **ACEITO**: trocar de contexto (direto ↔ delegado) é navegação completa, o estado do SPA recomeça |
| M-02 rascunhos/tickets sobrevivem ao logout | **CORRIGIDO** (ver H-01) |
| M-03 JWT de desenvolvimento em `localStorage` | **ACEITO**: só no login de desenvolvimento, que a API recusa fora de ambiente de desenvolvimento |
| M-04 nginx do repositório sem CSP uniforme | **CORRIGIDO** no modelo do host (CSP, HSTS e `nosniff` repetidos no `location` de ativos) |
| L-01 rotas internas sem negação explícita no vhost externo | **CORRIGIDO** (`/internal/`, `/metrics`, `/healthz` → 404; o contêiner web já só repassa `/api/` e o webhook da Meta) |
| L-02 `ConversationPage` legado renderiza URL de mídia | **SEM CAMINHO VIVO** (não é roteado); não reativar sem tratar |

## 3. Revisões dos próprios lotes do dia (resumo)

Lote de 2026-10-12 (migration 119, sinais, abas, selo): 1ª passada sem CRITICAL/HIGH; 2ª com **2 HIGH** (consulta amplificável por `NOTIFY` forjado; afirmação MT-AT-040 sem teste), todos corrigidos. Sinal de mudança do Hub: **2 HIGH** (escopo de fila era o da pessoa e não o do Hub; veredito em cache sobrevivia à mudança de escopo), 5 MEDIUM e 3 LOW, todos corrigidos ou aceitos com motivo (credencial Bearer não é revalidada, igual aos demais fluxos).

## 4. O que só o dono pode fazer (ou decidir)

1. **Restringir a edição do e-mail no Keycloak de produção** (perfil de usuário do realm `omnira`: `email` com `edit: ["admin"]`), e ligar `verifyEmail` se o realm tiver SMTP. É configuração do provedor de identidade; o código já não depende disso para o caso do endereço trocado, mas a pré-condição do desenho de "autorizar por e-mail" é um provedor em que o endereço verificado é de quem o controla.
2. **ACLs de infraestrutura**: Valkey (usuário só da aplicação), NATS (publicar por assunto), TLS/criptografia do JetStream, dono do volume de mídia.
3. **Confirmar em produção** o login de uma conta que só pertence ao Hub (HUB-LIVE-TEST-SCRIPT, B/5) — corrigido e provado num Keycloak descartável com o mesmo realm, mas não no Keycloak de produção.
4. **Testes com aparelho/provedor reais**: WhatsApp (anexos de saída, aviso de abertura/despedida), ERP ao vivo (cria chamado de verdade), IA com provedor real.
5. **O aceite**: o gate formal (`mission.yaml`, fase 13) só fecha com a sua assinatura. Sem ela o estado permanece `LAB`.

## 5. O que este pré-gate não cobre

Revisão humana independente; teste de intrusão por terceiros; carga em produção; o realm e o DNS/TLS de produção; qualquer coisa que dependa de um aparelho. O Codex disse explicitamente que não verificou: estado vivo do banco, ACLs vivas, comportamento de pooler, execução de testes.
