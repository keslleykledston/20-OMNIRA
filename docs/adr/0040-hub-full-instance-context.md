# ADR-0040 — Contexto completo da instância dentro do Hub (acesso delegado a dados operacionais)

Status: **PROPOSTA v2 — alternativa A+** (aguarda aceite do dono; nenhuma migration escrita). Vocabulário de evidência: tudo abaixo é desenho (`NOT WIRED`)
salvo onde diz o contrário. v1 (2026-10-09) propunha o predicado único; v2 incorpora o parecer externo "Arquitetura de Acesso Delegado Hub–Instâncias"
(09/10/2026), que aprova a base A e exige separar relacionamento, contexto e capacidade.
Relaciona: ADR-0036, ADR-0038, ADR-0039; `docs/architecture/HUB-DELEGATION-TABLE-MATRIX.md` (Fase 01, gerada do catálogo real).

## 1. O problema

A "Conversas" do Hub tem só texto: sem mídia, sem painel do contato, sem classificar/editar contato e sem abrir chamado no ERP da instância.
O produto exige atender **com o contexto completo da instância, sob privilégio mínimo**. A causa é estrutural: a caixa completa
(`/api/v1/tenants/{tenant_id}/...`, ~40 rotas, e o RLS por baixo) supõe que o ator é **membro** do tenant.

## 2. Fatos medidos (catálogo real, 2026-10-09; só leitura)

- 81 tabelas com RLS forçado. Classificação proposta (matriz): 43 operacionais, 8 administrativas, 2 de segredo, 3 de gestão delegada já existente,
  4 de sistema, 15 de plataforma/Hub, 3 de catálogo global, 3 de grupos WhatsApp (**decidido em 2026-10-09: só membro por enquanto**).
- **O banco não distingue papéis entre membros:** nas ~43 tabelas operacionais, `SELECT/INSERT/UPDATE/DELETE` valem para qualquer membro ativo
  (`has_active_membership`). A capacidade fina (`contact.classify`, `ticket.create`...) vive na aplicação (`PermissionChecker`). O RLS de hoje
  é barreira de isolamento **entre instâncias**, não de privilégio **dentro** da instância.
- Delegação do Hub já presente em 6 tabelas: `conversations`, `messages` (leitura), `audit_events`, `channel_connections`, `channel_credentials`, `tenant_entitlements` (gestão).
- Já existem: contrato Hub–instância, concessão efetiva, avaliação ao vivo (`now()`), trava de suspensão (`LockTenantActive`), `AccessSource` (Direct, HubManage),
  atribuição de auditoria (`via=hub`).

## 3. Pesquisa (mercado) e o que o parecer corrigiu

Padrões: delegação do lado do cliente com papéis mínimos e prazo (Microsoft GDAP); assumir papel com confiança condicional (AWS STS/ExternalId);
convidado/colaborador externo (Azure B2B, GitHub outside collaborators, Slack Connect); plataforma agindo em conta conectada (Stripe Connect); ReBAC (OpenFGA).
Correções do parecer, todas aceitas aqui: `contract_id` **não** é um ExternalId (a proteção é validar a cadeia inteira); o objeto "convidado" do Azure B2B é camada de
identidade, não membership de domínio; a duração máxima do GDAP admite extensão; nada disto foi reverificado em fonte primária (links a conferir na implementação).

## 4. Decisão proposta: A+ (relacionamento + contexto explícito + capacidade + RLS + revogação)

```
permitir = identidade_válida AND contexto_de_atuação_válido AND relacionamento_vigente
           AND capacidade_autorizada AND recurso_no_escopo AND ausência_de_bloqueio
```

1. **Relacionamento (ReBAC, ao vivo):** ator → Hub → contrato → instância → concessão. Sem cópia, sem membership fictícia (B rejeitada).
2. **Contexto de atuação explícito, nunca a soma de privilégios.** O front declara em cada requisição como a pessoa atua
   (`X-OMNIRA-Acting-As: member` ou `hub:<hub_id>`); o middleware resolve **uma** fonte por requisição:
   - sem o cabeçalho: só membership (comportamento atual preservado); sem membership, nega;
   - `hub:<id>`: só a delegação, mesmo que a pessoa seja admin da instância (não herda privilégios administrativos);
   - a revogação do contexto delegado nega ainda que exista membership independente;
   - a troca de contexto é uma nova resolução e fica na trilha de auditoria.
   Substitui a regra da v1 "membership vence".
3. **Capacidade.** Reusa o vocabulário **existente** de permissões (`permissions`/`role_permissions`: `contact.classify`, `ticket.create`...), sem criar um
   segundo. A concessão carrega um conjunto de capacidades limitado pelo teto do contrato (concessão ⊆ contrato; o Hub não amplia). Preset "atendimento":
   ler/responder conversa, mídia, ler/classificar contato. Opcionais por concessão: editar contato, vincular cliente, criar chamado, transferir, executar fluxo, IA.
   Sempre negado à delegação: equipe, contrato, credenciais de canal/ERP, chaves de IA, segurança.
4. **RLS como segunda barreira independente — relação + domínio + ler/escrever no banco; capacidade fina na aplicação, alimentada pela MESMA concessão.**
   Análise comparativa (decisão do dono em 2026-10-09: "ver qual atinge o objetivo e escala"), ver §4.1.
5. **Classes de tabela (matriz):** operacional (migra para o predicado do seu domínio); administrativa e segredo (só membro/admin, nunca delegação);
   gestão delegada (inalterada; revisão abaixo); sistema; plataforma. Teste de catálogo: toda tabela com `tenant_id` tem classe declarada, e o predicado de cada política confere com a classe.
6. **Mesma URL.** `/api/v1/tenants/{tenant_id}/...` segue; a URL identifica o alvo e **não concede**. `tenant_id` de URL/payload nunca autoriza.
7. **Hub Inbox** continua por instância autorizada (projeção `hub_inbox_items` com políticas próprias). Sem conexão privilegiada, sem bypass de RLS.
8. **Identidade de contato:** o mesmo telefone em duas instâncias não compartilha contratos, etiquetas, chamados nem histórico; classificação e vínculo são locais à instância.
   (Já é assim: contatos são por tenant; o teste cruzado fica nos gates.)

### 4.1 Granularidade da capacidade: banco por operação (parecer) × banco por domínio (v2 inicial)

Fatos do repositório que decidem a questão:
1. **O RLS só enxerga LINHA e COMANDO** (`SELECT/INSERT/UPDATE/DELETE`), nunca a operação de negócio. Em `contacts`, classificar (`PUT .../classification`) e editar detalhes (alias, e-mail)
   são o **mesmo** `UPDATE contacts`, em colunas diferentes. Uma política não distingue os dois; separar exigiria gatilhos ou privilégio por coluna (que é por papel, não por requisição).
2. **A capacidade `contact.update`, `contact.read`, `customer.link` do parecer não existe no modelo real.** O vocabulário de permissões tem **uma** chave de contato, `contact.classify`,
   que hoje cobre classificar, editar detalhes e vincular conta (`classification_http.go`, `details.go`). Capacidade fina exige primeiro **criar as chaves na aplicação**; o banco não pode espelhá-las por operação.
3. **Hoje nem membros têm separação no banco** (matriz): exigir capacidade por tabela e operação só para delegados seria mais rígido que para membros e criaria um segundo dicionário de permissões
   que precisa andar junto com o da aplicação a cada funcionalidade nova (deriva).
4. **O que o parecer acerta (e fica):** o delegado é MENOS confiável que um membro, o teto do contrato limita o que o Hub concede, e a capacidade tem de ser verificada em cada operação, inclusive nas indiretas.

| Critério | Banco por operação/tabela (parecer, à letra) | Banco por domínio + capacidade fina na aplicação (adotado) |
|---|---|---|
| Expressável pelo RLS | Parcialmente: só no nível tabela×comando; classificar≠editar não | Sim, é exatamente o que o RLS enxerga |
| Princípio do menor privilégio | Alto no papel; parte ilusória (item 1) | Alto: domínio sem concessão = tabelas inacessíveis ao delegado, mesmo com bug na aplicação; capacidade fina conferida no serviço |
| Escala com funcionalidades novas | Cada capacidade nova = migration de política + permissão + tela | Capacidade nova = linha em `permissions` + checagem no serviço + tela; **sem migration de RLS** |
| Fonte da verdade | Duas (política e permissão) | Uma: o conjunto de permissões da concessão alimenta a aplicação **e**, por um mapa permissão→domínio, o predicado do banco |
| Custo por linha | Um predicado por política (igual) | Igual; forma recomendada: `tenant_id IN (SELECT ...)` avaliado uma vez, medir com `EXPLAIN (ANALYZE, BUFFERS)` (Fase 06) |
| Risco de brecha | Políticas por capacidade divergirem do app | Domínio mal classificado; mitigado pelo teste de catálogo e pela matriz revisada |

**Desenho adotado ("A+ em camadas", une as duas visões):**
- A **concessão** guarda um conjunto de chaves de permissão **existentes** (⊆ teto do contrato). É a única fonte.
- Uma tabela de dados `permission_domains` mapeia chave → (domínio, ler/escrever). O banco responde `has_tenant_access(tenant, user, domínio, necessidade)` consultando a concessão e esse mapa; a aplicação confere a **chave** da operação. Mesma fonte, duas camadas.
- Operações de maior risco com efeito externo (abrir chamado no ERP, qualquer uso de credencial) são **mediadas por serviço**: a capacidade é conferida antes de a credencial ser lida, no executor; o banco nunca é a única barreira nelas.
- Capacidades mais finas que as de hoje (por exemplo separar editar contato de classificar) entram como **novas chaves de permissão** (dados + serviço), quando o produto pedir.
- Se a revisão do parecerista exigir subir a granularidade de uma tabela específica (contato-escrita, chamado), isso é um acréscimo localizado sobre a mesma estrutura, não um redesenho.

## 5. Revisão das três tabelas já delegáveis (parecer §5.1; feita em 2026-10-09 no catálogo vivo)

- **`channel_credentials`** (RLS forçado; colunas: `ciphertext bytea`, sem texto claro). A política de leitura do gestor delegado
  (`channel_credentials_hub_manage_read`) é `EXISTS(conexão pai)` **sem** chamar `has_hub_manage_access` por extensão: hoje o resultado é o correto porque a subconsulta passa pelo RLS de
  `channel_connections` (escopo conferido ali), mas depende de um efeito indireto. **Ação executada em 2026-10-09:** migration `000107` torna o predicado explícito (a leitura exige o escopo do canal da conexão, como as outras três políticas); teste novo abre a política da conexão de propósito e prova que a leitura de credencial se sustenta sozinha; mutante morto. Local, não implantada.
  Achado pré-existente: qualquer **membro** (inclusive agente) pode `SELECT` o `ciphertext` no nível do banco; a confidencialidade vem da chave fora do banco e de nenhuma resposta da API carregar o campo
  (conferido por busca no código: nenhum modelo de resposta o expõe; não é prova por teste). Para delegados o risco é igual ao de um agente membro, nunca maior (escopo da conexão); recomenda-se restringir a leitura do
  `ciphertext` a serviço/admin como endurecimento separado.
- **`channel_connections`:** o gestor delegado lê/escreve só conexões do **escopo** que o contrato delegou (`channel_connection_scope(channel)`); já é a exigência do parecer, não ampliar.
- **`audit_events`:** o gestor delegado lê **somente os próprios** eventos (`actor_id = current_user_id()`), não há visibilidade global (exigência do parecer atendida).
  Achado pré-existente: qualquer membro lê todos os eventos da instância no banco (a API exige `audit.read`). Fica na matriz como item de endurecimento.

## 6. Revogação, suspensão, filas, ERP, seletores, fluxos, IA

- **Garantia-alvo (parecer §7):** após o commit da revogação nenhuma operação **nova** é admitida com a concessão. Já vale para HTTP (avaliação ao vivo); a verificar em
  SSE/realtime, download de mídia (autorização por download, sem URL durável), ERP, copiloto, fluxos e jobs pendentes (revalidar ao executar).
  O refresh de 15 s e a tela borrada são complementares, **não** controle de segurança; acrescentar **push de invalidação** pelo canal realtime existente.
  Limites explícitos: o que já foi baixado não se recolhe; transação em curso vê o próprio snapshot; operações longas têm política própria de admissão/cancelamento.
- **Suspensão com drenagem:** já bloqueia novos trabalhos e espera os admitidos (`LockTenantActive`/`WhileActive`); manter o estado de drenagem na documentação.
- **ERP (abrir chamado):** ator → contexto delegado → concessão → contrato → `ticket.create` → cliente e recurso comprovadamente da instância → integração escolhida **no backend**
  → credencial lida só no executor → ator/contexto/resultado auditados → idempotência. O front nunca escolhe credencial, conta ERP ou instância.
- **Filas e jobs:** só IDs e metadados mínimos; segredo nunca em fila; trabalho iniciado por pessoa carrega ator, contexto e concessão de origem e é revalidado ao executar.
- **Seletores:** diretório de participantes aptos = membros internos + agentes do Hub com concessão e capacidade vigentes; sem membership artificial.
- **Flow Builder:** ver, editar, publicar e **executar** são capacidades separadas; delegação só executa fluxo aprovado.
- **Copiloto de IA:** só dados do contexto delegado; cada ferramenta confere a própria capacidade (`ai.assist` não implica `ticket.create`); o contexto de autorização é decidido no backend e propagado
  nas execuções assíncronas; prompts/argumentos não o substituem.
- **LGPD/transparência:** área "Acessos delegados" para o admin da instância (Hubs e contratos, pessoas e capacidades, vigência, operações sensíveis com ator real e contexto).
  Auditoria com `actor_user_id`, `acting_as`, `hub_id`, `tenant_id`, `grant_id`, `contract_id`, `request_id`. Papéis de controlador/operador avaliados pelo tratamento real (revisão jurídica, fora do código).

## 7. Limite de confiança do RLS (parecer §4.3)

O RLS atual confia em `app.current_user_id`/`app.is_system_admin` escritos pela aplicação via `SET LOCAL`. Isso protege contra consultas que esquecem filtro, **não** contra uma conexão da
aplicação comprometida capaz de forjar essas variáveis. O papel de aplicação não é superusuário nem `BYPASSRLS`; `FORCE ROW LEVEL SECURITY` está ligado nas tabelas revisadas. Cobrir esse modelo de
ameaça exigiria identidades de banco distintas por contexto e fica fora desta ADR. Nota técnica: políticas permissivas se combinam por `OR`; cada migração deve conferir o conjunto existente
(a matriz mostra, por comando, quais fontes já existem) antes de acrescentar uma política.

## 8. Fases (cada uma só avança com acesso cruzado, mutantes, papel real da aplicação e passada do Codex; HIGH/CRITICAL bloqueia)

| Fase | Escopo | Gate |
|---|---|---|
| 01 | Inventário e classificação: **matriz gerada** (feita; classificação a revisar), mapa das ~40 rotas, baseline de testes | Nenhuma mudança geral de RLS antes da revisão da matriz |
| 02 | Núcleo: `ActingContext`, resolução de relacionamento, concessão com capacidades ⊆ teto do contrato, cabeçalho de contexto, auditoria; membros inalterados | Sem união de privilégios; `tenant_id` nunca autoriza |
| 03 | Piloto: conversas, mensagens, **mídia** e leitura de contato, ponta a ponta, com RLS e revogação testada | Isolamento entre instâncias e entre Hubs numa jornada completa |
| 04 | Operações: classificar/editar contato, vincular cliente, diretório de atendentes, transferir, **chamado no ERP** | Nenhuma credencial de canal/ERP exposta |
| 05 | Indiretas: fluxos, copiloto, realtime, mídia, workers/jobs, suspensão, revogação | Nenhum caminho privilegiado alternativo |
| 06 | Segurança e liberação: negativos, concorrência, regressão de membro, `EXPLAIN (ANALYZE, BUFFERS)` do predicado por linha, Codex, rollback | Rollout gradual só após isolamento, privilégio mínimo, latência |

Testes indispensáveis (parecer §12.1) viram a lista de aceite de cada fase, todos com o papel real da aplicação (`omnira_app`), nunca com o dono.

## 9. Interface (decisão de produto já tomada e implementada na parte de front)

"Conversas" tem abas por instância na mesma aba do navegador (`6e670d3`): **Todas** e uma por instância. Revogado o acesso, a aba borra com a mensagem de contato com o administrador do Hub,
o cache da instância é descartado e a aba some na próxima leitura. A **administração** (agentes, empresas, contratos, teto de capacidades, equipes) é tema do ADR-0041 (console independente, proposta). Até a fase 03 concluir, quem tem acesso **só** pelo Hub continua com a visão de texto (aviso na aba).

## 10. Divergências conscientes em relação ao parecer

1. Granularidade do banco por **domínio** e não por operação/tabela (§4.1), porque o RLS não enxerga operação de negócio (classificar e editar são o mesmo `UPDATE contacts`) e porque nem membros têm essa separação no banco.
2. Vocabulário de capacidades = o já existente em `permissions`, não nomes novos (`conversation.read`, `contact.update`...): as chaves do parecer não existem no modelo real (só há `contact.classify` para contato); novas chaves entram como dados quando o produto pedir.
3. A Fase 01 já tem uma primeira matriz; a classificação é proposta e precisa da revisão do parecerista e do dono.
4. A política ilustrativa com `auth.current_tenant_id()` não se aplica: o OMNIRA decide por usuário e relacionamento, não por um GUC de tenant.

## 11. O que NÃO muda

RLS permanece (nenhuma política removida), `tenant_id` do payload/URL nunca autoriza, nada de segredo em fila, nenhuma membership fictícia, delegação não dá acesso implícito a equipe/segurança/chaves de IA,
suspensão continua esperando o trabalho já admitido, e membros mantêm o comportamento atual.

## 12. Fase 03 (piloto de atendimento) — estado em 2026-10-09 (local, NÃO implantada)

IMPLEMENTADO: migration 109 (predicados de membro falsos enquanto se age por um Hub; `acting_hub()` com leitura segura; políticas de leitura delegada de **contatos** e **mídia**),
rotas delegáveis em **lista de permissão** (`Delegable`: tudo o que não foi marcado é 404 no contexto delegado, para que quem é membro e delegado não alcance rotas ainda não migradas pelo caminho da membership),
mídia por caminho do Hub (`/api/v1/hubs/{hub}/serve/{tenant}/messages/{id}/media`, porque `<img>`/`<audio>` não enviam cabeçalho), **escrita** (assumir e responder) pelo caminho de escrita do Hub já revisado
(`DelegatedWrites`; exige a chave da rota **e** `can_reply`), `hubctl serving ceiling|grant`, `full_context` por empresa no inbox do Hub, e o modo "atendendo pelo Hub" no front (mesma caixa da instância, cabeçalho
só para a instância em que a sessão age, menu reduzido, sem tempo real, cartão de detalhes somente leitura).

Achados do próprio trabalho: (1) as políticas legadas de conversa e mensagem aplicam o **escopo de filas do contrato**; uma política nova que o ignorasse abriria filas fora do contrato, por isso conversa/mensagem ficam
com as legadas e contato/mídia **herdam a visibilidade da conversa**; (2) as políticas legadas liberam leitura a **qualquer concessão viva**, independente das chaves novas; a chave `conversation.read` é cobrada na rota e a
convergência (predicado de concessão consciente das chaves + preenchimento retroativo) é item da fase 04; (3) há ~20 módulos com checagem de permissão própria contra `memberships`, por isso a lista de permissão por rota.

Revisão Codex (2026-10-09): CRITICAL 0, HIGH 1, MEDIUM 4, LOW 1. **H-01 improcedente** (`hub_memberships` não tem estado: sair do Hub apaga a linha e a FK apaga as concessões; coberto por teste), **M-03 falso** (a 109 real já usa `NULLIF(...,'')`),
**M-04 corrigido** (`SetServing` segura o contrato com `FOR SHARE` enquanto confere o teto; teste de espera), **L-01 corrigido** (`acting_hub()`; valor malformado = sem contexto, nunca erro; teste), **M-02 aceito** (o catálogo
`permission_domains` só muda por migration; cada decisão é avaliada ao vivo, não há janela útil), **M-01 aceito** (o agente só lê as PRÓPRIAS linhas de auditoria, que ele mesmo gerou).

Pendente para a fase 04: classificar/editar contato, vincular cliente, chamado no ERP (cada um com chave e rota próprias, mediado por serviço), diretório de atendentes, anexos, convergência do predicado legado, tempo real/SSE delegado (fase 05), consulta de desempenho (`EXPLAIN`) das políticas.

## 13. Fase 04a (classificar e editar o contato) — estado em 2026-10-09 (local, NÃO implantada)

Fatiamento da fase 04 (cada fatia com seus testes, mutantes e passada do Codex): **04a** classificar/editar contato e vincular a empresas já cadastradas (esta); **04b** chamado no ERP e diretório de empresas
do ERP (credencial só no executor); **04c** diretório de atendentes e transferência, notas; **04d** convergência do predicado legado de concessão + `EXPLAIN`; leitura do canal da conversa.

IMPLEMENTADO: migration 110 (política de **UPDATE** de `contacts` por domínio `contact`/escrita, herdando a visibilidade da conversa; `contact_account_links` leitura/inclusão/alteração; `customer_accounts` só
**leitura**; nada de INSERT/DELETE de contato nem de escrita em contas; duas funções `SECURITY DEFINER` estreitas para os dois efeitos da reclassificação que tocam conversas, `delegated_recompute_contact_kinds` e
`delegated_dequeue_spam`, que conferem elas mesmas o contexto delegado **e** a chave). Handlers de contato, classificação e contas passam a perguntar `actor_has_permission` (membro: papéis; delegado: só as chaves
delegadas; nunca os dois). Rotas delegáveis (lista de permissão): `PATCH contacts/{id}` e `PUT .../details` e `PUT/POST .../classification|accounts...` com `contact.classify`; `GET .../classification` e
`GET accounts` com `account.read`; `GET me/access` (devolve as chaves DELEGADAS, `role_key: hub_delegate`, nunca as do papel de membro). Recusas deliberadas no contexto delegado: o tipo **interno** (decisão da própria
instância) e **empresa do diretório do ERP** (fase 04b); notas não têm chave no catálogo (rota não delegável). A lista de rotas delegáveis e suas chaves agora é conferida por teste
(`internal/platform/httpserver/delegable_routes_test.go`). Presets do `hubctl`: `classificacao`. Front: o cartão de detalhes oferece "Editar contato" e "Tipo de contato" só se `/me/access` traz `contact.classify`
(sem "Interno", só contas já cadastradas); o batimento de presença não é enviado enquanto se atende pelo Hub.

Decisão de modelagem: a leitura de `customer_accounts` é por domínio no nível da instância (a lista de clientes da empresa não é dado de conversa, então não herda a fila do contrato); a **rota** é que exige `account.read`.

Revisão Codex da 04a (2026-10-10): CRITICAL 0, **HIGH 2**, MEDIUM 0, LOW 1 — todos tratados antes de seguir.
- **HIGH-1 corrigido (e já existia na fase 03):** as políticas legadas de conversa/mensagem (000095) aceitam QUALQUER concessão viva da pessoa, de qualquer Hub; quem é servido por dois Hubs na mesma instância, com escopos de fila
  diferentes, alcançaria (também por contato e arquivo, que herdam essa visibilidade) a fila do outro Hub enquanto age por um. Corrigido na 110 com políticas **RESTRITIVAS** (`conversations_acting_hub_only`,
  `messages_acting_hub_only`: ao agir por um Hub só vale o contrato e a concessão DELE, escopo de fila incluído; fora do contexto delegado nada muda). Teste de duas hubs/uma instância/filas disjuntas
  (`TestAPersonServedByTwoHubsActsForOneAtATime`) e mutante. A política de mensagens só repete o que a de conversas já garante (a de mensagens exige a conversa visível): mantida como segunda barreira, não é mutante.
  Em produção só existe um Hub, então não houve exposição real.
- **HIGH-2 corrigido em parte, parte ACEITA com decisão registrada:** as duas funções `SECURITY DEFINER` agora exigem que o contato tenha conversa DENTRO do escopo do Hub que age (não dá para apontá-las para um contato que o
  delegado não enxerga). O efeito delas continua sendo o do próprio ato de classificar, no contato inteiro: recalcular o tipo derivado das conversas/grupos do contato e, no spam, tirar da fila as conversas abertas sem dono.
  Restringir isso ao escopo deixaria o tipo derivado desatualizado nas conversas de fora e o golpista roteável em outra fila; nada é lido nem devolvido de fora do escopo. É o mesmo efeito de quando um membro classifica.
- **LOW:** o cache de 30 s de `/me/access` deixava os controles visíveis após revogar; no modo Hub o front agora consulta de novo a cada 15 s (o servidor já recusava).
Achado próprio durante a revisão: um agente do Hub podia desfazer um contato que a instância marcou como **interno**; agora é 403 (`ErrInternalIsTheInstancesCall`), com teste e mutante.
O teste da fase 03 `TestNothingCanBeWrittenThroughTheDelegatedReadPolicies` concedia `contact.classify` e esperava UPDATE de contato = 0 linhas; a 110 muda isso de propósito, então o teste passou a conceder só chaves de leitura (a prova do que `contact.classify` permite está em `serving_contacts_integration_test.go`).

## 14. Contexto preso a UMA instância no banco (migration 111) — achado ao implantar a 04a (2026-10-10)

A prova só-leitura em produção (papel da aplicação) mostrou que, agindo por uma instância (Test Company) através do Hub H, a sessão ainda enxergava as `conversations` das outras instâncias servidas pelo mesmo H (172 + 20 linhas), pela política legada `conversations_read_hub_delegation` (acesso de Hub da fase ≤ 02, que a conta já tem pela aba do Hub). Não era acesso novo nem de outra pessoa, e o código sempre filtra pela instância do contexto — mas a segunda barreira não segurava a linha "uma instância por vez" que a primeira segura.
- **Decisão:** `lock_served_tenant` passa a registrar a instância (`app.acting_tenant`, local à transação como `app.acting_hub`; `acting_tenant()` lê com a mesma tolerância a texto inválido) e políticas **restritivas** prendem `conversations` e `customer_accounts` (as duas tabelas cuja visibilidade delegada NÃO pende de outra), além de `contacts` e `contact_account_links` (as duas que o delegado pode ESCREVER; camada redundante hoje, mantida de propósito). `messages` e as três tabelas de mídia herdam pelo `EXISTS` na conversa e **não** ganham política própria (tabelas quentes: custariam uma avaliação por linha em todo pedido de membro, sem proteção a mais). Falha fechada: agindo por um Hub sem instância válida, nada casa. Fora do contexto delegado nada muda.
- **Prova:** `TestTheDelegatedContextIsPinnedToOneInstance` (mesmo Hub, duas instâncias, mesmas chaves nas duas: agindo por A não se vê nem escreve nada de B, em 8 tabelas; vale o inverso; falha fechada com `acting_tenant` vazio/inválido; membro inalterado) + `scripts/test-hub-pin-mutations.sh` (4 mutantes mortos; os de `contacts`/`contact_account_links` são documentados como NÃO mutantes, redundância deliberada). Migrations 093..111 sobem/descem/sobem idênticas; E2E em navegador real 9/9; gate dos pacotes afetados verde (um teste de `hub/provisioning` que conta eventos de auditoria globais falhou uma vez sob carga e passou 3 vezes isoladas).
- **Estado:** local, NÃO implantada (a 110 está em produção; a 111 sobe junto da próxima entrega).

## 15. Fase 04b (diretório de empresas do ERP e chamado no ERP) — desenho e estado em 2026-10-10 (local, NÃO implantada)

**O que a pessoa do Hub passa a poder (chaves `ticket.create`, `ticket.read`, e `contact.classify` para o diretório):** listar as empresas ativas do ERP da instância, marcar "Cliente" com uma empresa do ERP, abrir o chamado no ERP da conversa que ela assumiu e ver o chamado vinculado. **Continua fora:** atualizar a projeção a partir do ERP e mudar o status no ERP (rotas não delegáveis → 404; fase seguinte), anotações, histórico.

**Decisões de desenho**
1. **A credencial do ERP é da instância e nunca chega à pessoa do Hub.** Um agente não lê `channel_connections` nem `channel_credentials`. `delegated_erp_connections(tenant)` (SECURITY DEFINER) devolve ao SERVIDOR a(s) conexão(ões) `k3g_crm`/`erp` da instância e a linha de credencial CIFRADA, só se o pedido age por um Hub NESTA instância (`acting_tenant()`) e a pessoa tem `ticket.create` ou `contact.classify` agora; o servidor decifra em memória com a própria chave (`CiphertextResolver`), como faz para um membro. Toda a lógica de decisão do resolvedor é a do membro (nenhuma / ambígua / sem credencial / inutilizável → 503), no mesmo ponto único (`K3GTicketingRuntimeResolver`), por isso o diretório, a classificação e o chamado ganham o caminho delegado de uma vez.
2. **A conta local de uma empresa validada** (achar-ou-criar-e-ativar conta + vínculo) é feita só por `delegated_materialize_company_account` (a chave certa para o motivo: `ticket.create` no fluxo do chamado, `contact.classify` na classificação). O delegado continua sem INSERT em `customer_accounts`/`account_external_links` (esta só ganha leitura delegada). O handler já validou a empresa no diretório da própria instância; a função só registra.
3. **Políticas por DOMÍNIO `ticket`** em `tickets` (leitura; UPDATE para enriquecer) e `ticket_external_create_attempts` (leitura, inclusão, alteração), cada uma restrita às conversas que o chamador enxerga (escopo de fila do contrato) e presa à instância (`*_acting_one_instance`, 111). **Sem** INSERT/DELETE em `tickets`, **sem** acesso a `ticket_external_status_attempts`. `crm_contact_company_evidence` (a evidência "contato ↔ empresa" gravada após o chamado e lida na classificação): leitura por `contact`, escrita por `ticket`, ambas restritas ao contato visível. `account_external_links`: só leitura delegada.
4. **Mesmos guardas do membro, em ordem:** a conversa é da pessoa (assumida por ela), `ticket.create` (agora perguntado a `actor_has_permission` pelo `PermissionChecker` quando o contexto é `hub_serve`), a empresa é validada no diretório do ERP da instância, UMA chamada ao ERP por intenção, projeção local só depois do registro durável da tentativa. Repetir a criação encontra o chamado já vinculado (409), sem segunda chamada ao ERP e sem segundo aviso. `GET .../ticket` aceita `ticket.read` OU `ticket.create` (um agente que pode criar precisa ver o resultado).
5. **O aviso ao cliente** ("seu chamado foi aberto, protocolo N") sai pelo caminho de escrita do próprio Hub (ADR-0037), nunca pelo remetente do membro: `DelegatedWrites.TicketOpenedNotice` (mesmo texto, mesma chave por chamado, uma vez só mesmo se outro operador repetir depois de uma transferência); exige a chave `conversation.reply` E a concessão com capacidade de resposta; sem elas o aviso é pulado (melhor esforço, nunca falha o chamado).
6. **Rotas** (lista fechada em `delegable_routes_test.go`): `GET crm/companies` (`ticket.create` ou `contact.classify`), `GET/POST conversations/{id}/ticket`, `GET contacts/{id}/company-suggestions` (`account.read`). Preset novo `chamados` = `classificacao` + `ticket.read` + `ticket.create`.

**Provas (POSTGRES + HTTP, papel real `omnira_app`, handlers reais, ERP DE MENTIRA — um servidor HTTP no teste; NENHUMA chamada a ERP real):** `serving_erp_integration_test.go` — o ERP recebe o token da instância e nenhuma resposta o carrega nem o endereço; credencial/conexão ilegíveis ao agente; função recusa fora do contexto e para outra instância; 503 em configuração quebrada (sem ERP, duas conexões, sem referência, credencial indecifrável) e nunca cai para outra; chave certa por rota (403/404); revogação corta na hora; fluxo completo do chamado (não assumida 409, de outro 403, uma chamada ao ERP, projeção, conta, evidência, tentativa, aviso uma vez, repetição 409, leitura); políticas por domínio/escopo/instância; classificação com empresa do diretório (inativa/desconhecida 422; mesma empresa = mesma conta).
`scripts/test-hub-erp-mutations.sh`: mutantes mortos (equivalentes documentados no próprio script).

**Não provado:** ERP real (nenhum chamado real foi criado; o teste ao vivo exige a sua autorização, pois cria chamado de verdade e pode avisar um cliente por WhatsApp); login real do Keycloak; `EXPLAIN` das políticas novas; as rotas de atualizar projeção e mudar status no ERP continuam fechadas.

**Revisão do Codex (2026-10-10, somente leitura, leu o diff `c01d0be..HEAD`; não executou testes):** CRITICAL 0, HIGH 2, MEDIUM 3, LOW 1.
- **HIGH — `account_external_links` sem a trava de instância:** agindo pela instância A, o mesmo Hub servindo B deixava ler os vínculos de B (empresa externa, conexão, nome). **Corrigido:** `account_external_links_acting_one_instance` (a visibilidade dessa tabela não pende de outra) + teste direto entre duas instâncias + mutante.
- **HIGH — o `WITH CHECK` dos UPDATE não revalidava o escopo do NOVO `conversation_id`/`contact_id`:** dava para reapontar um ticket/tentativa/evidência para uma conversa de fila fora do contrato. **Corrigido:** o `EXISTS` do escopo também no `WITH CHECK` (tickets, tentativas, evidência) + testes de reapontamento + 3 mutantes.
- **MEDIUM — erros de transporte carregavam a URL do ERP para os logs** (também no caminho do membro, anterior a esta fase): **corrigido** nos conectores K3G (`transportCause` descarta a URL do `net/http`; a causa — timeout, recusa, DNS — fica).
- **MEDIUM — `delegated_materialize_company_account` confia em empresa/nome/origem do chamador:** **aceito**, mesmo modelo das funções da 110: só dois handlers a chamam, os dois depois de validar a empresa no diretório REAL da instância e com a chave certa; um delegado só com HTTP não a alcança; o efeito é uma conta/vínculo na própria instância (que ela pode arquivar), sem acesso novo ao ERP (criar chamado revalida a empresa no diretório). Um "nonce de observação" persistido seria desenho novo sem ganho de fronteira.
- **MEDIUM — a conta local é materializada antes de `Acquire` (repetição/divergência/bloqueio):** **aceito**, é a ordem que o serviço já tem para o membro ("antes de qualquer escrita no provedor: o pior caso é uma conta local idempotente"), e a empresa foi validada ativa no ERP; não há chamada ao ERP nesses casos.
- **LOW — cobertura:** coberta pelos testes e mutantes acima. O `WITH CHECK` explícito do UPDATE é camada redundante (o PostgreSQL também confere a linha NOVA contra a política de SELECT, que já carrega o escopo): os testes de reapontamento provam o comportamento; não há mutante possível (documentado em `scripts/test-hub-erp-mutations.sh`).
