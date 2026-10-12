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

## 16. Fase 04c (transferir, devolver à fila e anotações) — desenho e estado em 2026-10-10 (local, NÃO implantada)

**O que a pessoa do Hub passa a poder:** transferir a conversa que ela ASSUMIU a outra pessoa do Hub (chave `conversation.transfer`), devolvê-la à fila (basta `conversation.claim`, como soltar a própria conversa) e ler/escrever anotações do contato (chave `contact.note`). **Continua fora:** o diretório dos MEMBROS da instância (a pessoa do Hub só escolhe entre colegas do Hub com acesso para responder), histórico/pendências do contato, anexos.

**Decisões de desenho**
1. **Duas chaves novas, opcionais por concessão** (nenhum papel de empresa as recebe): `conversation.transfer` (`conversation`/escrita) e `contact.note` (`contact`/**leitura**: a chave só precisa ser delegável e deixar ver o contato; mapeá-la para escrita daria a quem só anota as políticas de UPDATE/INSERT do domínio `contact` da 110 no banco). Preset novo `completo` = `chamados` + as duas.
2. **Transferir e devolver reusam o caminho de escrita do Hub (ADR-0037)** já revisado (`replying.Service.Transfer`): autoriza na sessão RLS de quem pede, executa em sessão de sistema da instância que trava a linha, prova de novo a delegação do ator e da pessoa escolhida (`distribution.CanTake`), grava histórico de atribuição (`hub_transfer` / `hub_release`) e auditoria. **Nenhuma política abre `conversations` para escrita.** Só o titular transfere ou devolve (409 caso contrário). A rota do membro (`conversation.manage`, qualquer conversa) segue intacta; o wrapper só age no contexto delegado.
3. **Diretório** `GET .../inbox/conversations/{id}/transfer-candidates` (só no contexto delegado; membro recebe 404): pessoas do Hub com concessão viva e COM capacidade de responder na instância e na fila da conversa, menos o próprio titular, menos carga. Não oferece membros da instância.
4. **Anotações pelas rotas do membro** (`GET/POST .../contacts/{id}/notes`, `PUT/DELETE .../notes/{note_id}`), agora atrás de `Delegable("contact.note")`; o handler pede `contact.note` ao agente do Hub (e `contact.classify` ao membro, como antes). Somente o AUTOR edita ou remove (o banco e o handler dizem o mesmo). O texto da anotação nunca vai para a auditoria.
5. **Migration 113, políticas de `contact_notes` pela CHAVE** (desvio consciente do "banco por domínio" do §4.1, só aqui): a anotação é interna da empresa e quem tem o domínio `contact` (ler/classificar) não deve lê-la. Leitura exige `actor_has_permission(..., 'contact.note')` + contato visível (escopo de fila herdado das conversas); inclusão/alteração/remoção só em nome próprio; presa à instância.
6. **Rotas** (lista fechada em `delegable_routes_test.go`): `POST .../inbox/conversations/{id}/unassign` (`conversation.claim`), `POST .../transfer` e `GET .../transfer-candidates` (`conversation.transfer`), as quatro de anotações (`contact.note`).
7. **Front:** o cartão do atendimento delegado mostra "Transferir ou devolver" só quando a conversa é da própria pessoa e o servidor lhe deu a chave (`/me/access`); sem `conversation.transfer` o diálogo só devolve à fila; com `contact.note` aparece o bloco "Anotações" (o mesmo componente do membro).

**Provas (POSTGRES + HTTP, papel real `omnira_app`, handlers reais):** `serving_transfer_notes_integration_test.go` — diretório certo (exclui o titular, concessão sem capacidade de responder, só outra instância, sem concessão, membros), só o titular transfere, escolhido sem direito → 409, escolhido revogado entre a lista e o POST → 409, histórico e auditoria, devolução uma vez, chave faltando → 403, outra instância → 404, membro chega ao handler original, pedido direto ao banco recusado; anotações: chave exigida na rota E no banco, só o autor, em nome de outro recusado, outra instância e contato fora do escopo invisíveis, texto fora da auditoria. `scripts/test-hub-transfer-mutations.sh`: 17 mutantes mortos (equivalentes documentados no script). Front: `DelegatedTransferNotes.test.tsx` (8).

**Não provado:** login real do Keycloak; `EXPLAIN` das políticas novas; implantação (migration 113 não aplicada); o atendimento real com duas pessoas do Hub.

## 17. Fase 04d (custo das políticas do contexto delegado, migration 114) — medido e corrigido em 2026-10-10 (local, NÃO implantada)

**Medição** (`scripts/bench-hub-policies.sh`: dois bancos de instância com 30 mil conversas e 300 mil mensagens cada, papel real `omnira_app`, `EXPLAIN (ANALYZE, BUFFERS)`, mediana de 5 execuções): com as políticas das migrations 108-113, a **lista da caixa levava 10,3 s** no contexto delegado (membro: 0,46 s) e a contagem de abertas 3,7 s (membro: 0,14 s). Duas causas: (1) cada linha de conversa/mensagem chamava `has_active_hub_access(...)` — função `SECURITY DEFINER` de seis tabelas, que o PostgreSQL não consegue inlinar — até três vezes (política legada, política restritiva "só este Hub" e, nas mensagens, a conversa de que dependem); nada nela depende da linha **exceto a fila** da conversa; (2) o custo estimado dessas chamadas aninhadas fazia o PostgreSQL ligar o **JIT** (compilar ~250 funções levava 1,2 s por consulta, mais do que a própria consulta).

**Correção (só custo; a RESPOSTA é a mesma):**
1. `acting_scope()` responde **uma vez por comando** o que a relação diz para (pessoa, instância e Hub do contexto): concessão e contrato vivos, escopo de filas do contrato. Usada em subconsultas não correlacionadas (InitPlan) nas políticas; por linha sobram só o pino de instância e "minha fila é permitida".
2. As políticas legadas da 095 (`conversations_read_hub_delegation`, `messages_read_hub_delegation`) passam a valer **só fora** do contexto delegado (onde a aba do próprio Hub segue lendo por elas, sem mudança); dentro dele, `conversations_read_acting` / `messages_read_acting` dão a MESMA visibilidade para a instância do contexto. As restritivas de 110 (soma de Hubs) ficam, com o predicado barato.
3. Os pinos `*_acting_one_instance` e a nota delegada usam `(SELECT acting_hub())` / `(SELECT acting_tenant())` (uma avaliação por comando, em vez de uma chamada de função por linha). As leituras de membro das seis tabelas varridas pela caixa ganham, antes de `has_active_membership()`, a mesma checagem de contexto em linha (a função já devolvia falso no contexto delegado; o resultado não muda).
4. `acting_scope()` declara `ROWS 1`; `lock_served_tenant` desliga o JIT **na transação delegada** (`set_config('jit','off',true)`).

**Resultado** (mesmo banco, mesmas consultas): lista **10 280 → 178 ms** (membro 405 ms), contagem **3 679 → 10 ms** (membro 80 ms), consultas pontuais ~5-6 ms (custo fixo da relação, 4× ~1 ms) contra 0,3 ms do membro. **O membro não piora** (lista 455→405 ms medido na mesma sequência; um desvio intermediário de 1,1 s foi o JIT ligando por estimativa de custo e foi eliminado com `ROWS 1`).

**Provas:** `serving_policy_cost_integration_test.go` compara, linha a linha, o que o contexto delegado vê com o que o predicado ORIGINAL diz, em 17 estados (escopo de fila em todos os formatos — ausente, lista vazia, uma/duas filas, `null`, não-lista, números, id em maiúsculas —, concessão expirada/futura/revogada, contrato encerrado/suspenso, Hub e empresa suspensos, conta inativa, membership removida) e prova que dois Hubs na mesma instância continuam sem se somar; `TestTheDelegatedTransactionDoesNotJITCompile`. `scripts/test-hub-cost-mutations.sh`: 8 mutantes mortos (equivalentes documentados no script). A suíte inteira do hub e `scripts/test-hub-migrations.sh` (093..114) seguem verdes; o benchmark vira um gate (`FAIL` se um pedido delegado passar de 3× o do membro quando este leva ≥ 20 ms, de 250 ms em listas ou de 25 ms em leituras pontuais).

**Não provado:** volume e carga reais de produção (a medição é sintética, 60 mil conversas); outras tabelas varridas no contexto delegado além das medidas (mídia, tickets, vínculos), que seguem com as políticas por domínio já em subconsulta; a aba do próprio Hub (políticas legadas, fora do contexto delegado) não foi otimizada nem medida aqui.

## 18. Fase 05 (tempo real delegado e resumo por IA, migration 115) — decisões e estado em 2026-10-10 (local, NÃO implantada)

O dono mandou seguir na fase 05 sem responder às três perguntas que ficaram em aberto; foram adotados os padrões **mais conservadores** abaixo, todos revisáveis.

**Decisões**
1. **Latência de revogação do tempo real: 15 s ponta a ponta** (`RealtimeOptions.DelegatedRecheck`; o membro segue 30 s). Igual ao refresh de `/me/access` da tela. Concessão revogada, chave perdida, instância/Hub suspensos ou conversa fora do escopo de filas encerram o stream em até 15 s (a verificação começa `DatabaseTimeout` = 5 s antes, para que o pior caso, com a consulta esgotando o tempo, ainda caiba em 15 s); **o mesmo vale para uma mudança do escopo de filas do contrato** (o atalho "contrato sem lista de filas" só pula o filtro por evento até a próxima verificação); a reconexão recebe 404 e a tela mostra o aviso.
2. **Streams SSE (`GET inbox/events`, `GET inbox/conversations/{id}/events`) aceitam o contexto delegado** (`Delegable("conversation.read")`). A admissão usa a MESMA porta dos pedidos (`lock_served_tenant` + chave perguntada ao vivo) em **transação curta que termina antes do stream** (`tenancyadapters.AuthorizeServing`): nenhuma transação ou conexão fica presa durante o stream. A flag `OMNIRA_HUB_SERVE_ENABLED` também vale aqui.
3. **O stream da instância só avisa sobre conversas que a pessoa pode ver:** os eventos citam a conversa (id e estado, nunca texto), e um contrato limitado a algumas filas não deve vazar a existência das outras. Cada evento passa por um filtro de visibilidade no contexto delegado, com cache de 10 s (falha fechada: na dúvida, descarta; o cliente rebusca ao reconectar). O stream de uma conversa recusa (404) conversa fora do escopo e se encerra se ela sair do escopo.
4. **Resumo por IA (`POST conversations/{id}/ai/summary`) exige a chave nova `ai.assist`**, de propósito **fora de qualquer preset antigo** (preset novo `completo_ia`): o texto da conversa sai da instância para o provedor de IA da plataforma, então é escolha explícita do contrato (teto) e da pessoa. Domínio `ai` (reservado desde a 108), leitura; nenhuma política usa o domínio. O handler é o mesmo do membro (conversa e mensagens vêm do que a pessoa já enxerga, escopo de filas incluído; limite de 5/min por pessoa; auditoria antes e depois da chamada, agora com `acting_as = hub:<id>`).
5. **Fora do escopo desta fase, por decisão (nada foi aberto):**
   - **Fluxos:** não existe rota de pessoa que "execute" um fluxo — eles rodam sozinhos nas mensagens recebidas, sem ator humano; portanto não há o que delegar e **nenhuma chave de fluxo foi criada**. Ver/editar/publicar fluxos continua só para membros.
   - **Tópicos, copiloto de resposta e ferramentas de IA** (`/topics/...`): agem sobre ERP/CRM com aprovação; exigem desenho próprio (qual credencial, quem aprova). Seguem não delegáveis (404).
   - **Finalizar atendimento** (ADR-0020: libera o bot, encerra chamados locais, grava memória): é escrita sobre conversa/ticket/memória e precisa passar pelo caminho de escrita do Hub com chave própria. **Próxima fatia.**
   - **Trabalhos assíncronos iniciados por pessoa do Hub:** nenhum existe nas rotas delegadas; o resumo é síncrono.

**Provas (POSTGRES + NATS + HTTP, papel real `omnira_app`, handlers e middleware reais):** `serving_realtime_integration_test.go` (9 casos: eventos só da instância, filtro por fila, conversa dentro/fora do escopo/outra instância, admissão negada sem contexto/sem chave/sem concessão/outra instância/rota não delegável/chave `conversation.read` ausente/flag desligada/cabeçalho malformado, membro sem cabeçalho igual a antes, encerra em 3 s após revogação, perda da chave e saída do escopo, escopo que estreita com o stream aberto); `scripts/test-hub-realtime-mutations.sh` 11/11 mortos (equivalentes documentados; as corridas entre o vigia e um evento enfileirado não têm mutante determinístico e foram cobertas por revisão e pelo detector de corridas `-race`); `serving_ai_integration_test.go` (chave e teto exigidos, só o texto da conversa pedida chega ao provedor, conversa fora do escopo/outra instância nunca chega, as duas auditorias nomeiam a pessoa e o Hub, membro inalterado). Front: o hook abre o stream com o cabeçalho de contexto (só para a instância atendida); o cartão mostra o resumo só com `ai.assist`; o polling de 15 s fica como rede de segurança.

**Não provado:** carga com muitos streams delegados abertos (cada um faz uma transação curta a cada 15 s e uma por evento novo, com cache); login real do Keycloak; chamada real ao provedor de IA (o teste usa um gerador de mentira); implantação.

**Vigia do stream (revisões do Codex da fase 05, 3 passadas):** a verificação periódica do Hub roda em goroutine PRÓPRIA (não no laço de eventos), com prazo de escrita armado antes de CADA escrita e prazo de consulta (`DatabaseTimeout`), de modo que cliente que não lê ou banco travado não adiam a revogação; ao negar, marca `closing`, cancela o contexto e move o prazo de escrita para "agora" (uma escrita bloqueada falha na hora); nenhuma escrita acontece depois disso e o vigia é aguardado antes de o handler retornar. Instância com contrato SEM lista de filas dispensa o filtro por evento (leitura de `acting_scope().unrestricted`, que exige `conversation.read`); com lista, cada evento passa pelo filtro (cache 10 s, orçamento de 50 consultas/s, falha fechada). O resumo por IA exige `conversation.read` no próprio handler (o banco não pergunta pela chave em `conversations`/`messages`, de propósito: ADR §4.1).

## 19. Fase 05b (finalizar o atendimento pelo Hub, migration 116) — decisões e estado em 2026-10-10 (local, NÃO implantada)

**O que a pessoa do Hub passa a poder:** encerrar o atendimento de uma conversa que ELA assumiu (chave nova `conversation.finalize`, opcional por concessão, em preset novo `completo_fin`): conversa fechada, chamados locais fechados (ligados ao ERP ou de assunto ficam e são contados), ficha de encerramento e pendências/promessas gravadas, aviso de encerramento ao cliente pelo caminho de resposta já delegado.

**Decisões de desenho**
1. **Caminho de escrita do Hub (ADR-0037), sem política nova de escrita:** `POST .../inbox/conversations/{id}/finalize` fica atrás de `Delegable("conversation.finalize")`; para o contexto delegado o decorador `DelegatedWrites.Finalize` (i) autoriza na sessão RLS de quem pede (item persistido do Hub, concessão viva com capacidade de responder), (ii) entra em `replying.Service.Act` — sessão de sistema da instância, delegação reprovada DENTRO da transação, linha da conversa travada — e (iii) executa as regras de atendimento do membro (`attendance.Service.FinalizeAsHubAgent`). O handler original nunca roda para um delegado; o do membro segue igual.
2. **Só quem SEGURA a conversa encerra:** o delegado não tem o caminho de "supervisor" do membro (`conversation.manage`); conversa solta ou de colega → 403. A conversa vem do item persistido, nunca do corpo (campos desconhecidos → 400).
3. **Responsáveis de pendências devem ser membros da instância** (a pessoa do Hub não pode ser dona de pendência: não é membro); repetir é idempotente (mesma ficha, sem segunda linha nem segunda auditoria).
4. **Auditoria `hub.conversation.finalized`** com `hub_id`, concessão, contrato, motivo, origem, contagens — nunca o texto do resumo, da observação ou das pendências.
5. **Fora do escopo desta fatia (continuam fechados):** "Sugerir com IA" no encerramento (lê a conversa pelo módulo de inteligência; precisa da chave `ai.assist` e de desenho próprio), histórico e pendências do contato (`attendance-context`, `attendance-history`, `history-search`, `follow-ups/{id}/resolve`) — próxima fatia, provavelmente atrás de uma chave de histórico.
6. **Front:** o botão "Finalizar" aparece para o delegado só com a chave e só na conversa dele; o diálogo é o do membro sem a sugestão por IA e sem consultar o canal (o aviso ao cliente é oferecido quando a pessoa pode responder; janela de 24 h fechada volta como erro e a pessoa pode finalizar sem avisar).

**Provas (POSTGRES + HTTP, papel real `omnira_app`, rotas e serviço de atendimento reais):** `serving_finalize_integration_test.go` — sem chave 403; só o titular (colega e conversa solta 403); corpo com tenant, motivo inválido e dono de pendência que não é membro 400; efeitos do membro (conversa fechada, chamado local fechado, ficha com a pessoa e origem "agent", pendência gravada); chamado do ERP deixado e contado; repetição idempotente; fora das filas/outra instância/concessão revogada 404 e sem capacidade de responder 403; texto fora da auditoria; escrita direta recusada pelo banco; rota do membro inalterada. `scripts/test-hub-finalize-mutations.sh`: 10 mutantes mortos. Front: `DelegatedFinalize.test.tsx` (3). E2E em navegador real: cenário novo (Bruno finaliza pela tela).

**Não provado:** login real do Keycloak; aviso real ao cliente por WhatsApp; implantação.

**Revisão do Codex (somente leitura, não executou testes) da fase 05b:** CRITICAL 0, HIGH 0, MEDIUM 1, LOW 0. O MEDIUM: o diálogo manda o aviso de encerramento ao cliente ANTES de finalizar; se a conversa for fechada por outro lado entre as duas chamadas, o aviso sai e a finalização falha. **Aceito, é o desenho do diálogo do membro (ADR-0020), não algo introduzido aqui:** depois de fechada a conversa o envio é recusado, então o aviso precisa vir antes; a chave de idempotência e o "um envio por atendimento" evitam duplicar na repetição, e o erro oferece "finalizar sem avisar". Tornar o aviso uma ação de servidor com retentativa por outbox é melhoria do fluxo do membro e dos delegados juntos, fora desta fatia.



## 20. Fase 06 (memória do contato: atendimentos anteriores e pendências, migration 117) — decisões e estado em 2026-10-10 (local, NÃO implantada)

**O que a pessoa do Hub passa a poder:** ver, no painel da conversa, o que aconteceu antes com o contato (atendimentos anteriores com o resumo e o motivo, e as pendências/promessas ainda abertas) com a chave nova `attendance.history`; e concluir ou descartar uma pendência aberta com a chave `followup.resolve`. Ambas opcionais por concessão, em nenhum preset anterior; preset novo `completo_hist` (= `completo` + as duas).

**Decisões de desenho**
1. **Chave própria, não reaproveitar `contact.note`:** o resumo de um encerramento e as promessas feitas ao cliente são dado interno da empresa, mais sensível que ler a ficha ou uma conversa. As políticas de `conversation_closures` e `follow_up_items` perguntam pela CHAVE (`actor_has_permission`), como `contact_notes` na 113; o domínio das duas chaves é `contact`/leitura (um mapeamento para escrita entregaria as políticas de escrita do domínio contato a quem só deveria resolver pendência).
2. **O banco pede a chave no contexto delegado para qualquer comando, também de quem é membro:** políticas RESTRITIVAS (`*_acting_history`, `follow_up_items_acting_resolve`) valem sempre que `acting_hub()` não é nulo. Uma pessoa que é membro da instância E agente do Hub não lê o histórico "como membro" enquanto atua como agente. A política legada (membro) fica como está e vale quando não há contexto delegado. Uma instância por vez (`tenant_id = acting_tenant()` na mesma política).
3. **Visibilidade herdada do contato** (e, portanto, das conversas dele, filas do contrato incluídas): um contato sem conversa nas filas do contrato não mostra memória nenhuma.
4. **Concluir uma pendência é escrita pequena e direta, não o caminho de escrita do Hub:** `follow_up_items_resolve_delegated` só abre uma pendência ABERTA, só a deixa `done`/`dropped` e assinada pela própria pessoa, e exige as DUAS chaves (um UPDATE lê o que altera). O serviço ganhou `ResolveFollowUpAsHubAgent` (sem a matriz de papéis, que não diz nada sobre quem não é membro; a rota `Delegable("followup.resolve")` e o banco provam a chave); a auditoria `followup.resolved` já nomeia o Hub pelo repositório de auditoria.
5. **Nenhum delegado insere fechamento ou pendência:** `*_acting_no_insert` (restritivas). Fechar continua no caminho de escrita do Hub (116, sessão de sistema).
6. **[SUPERADO pela fase 09, §23]** A busca em mensagens anteriores (`history-search`) ficou FECHADA ao delegado na fase 06 e foi aberta na fase 09 com `attendance.history` + `conversation.read` (as políticas de leitura de `messages` respondem a qualquer concessão viva, então a chave de leitura é cobrada no handler). Texto original desta decisão: **A busca em mensagens anteriores (`history-search`) continua FECHADA ao delegado** (fora da lista de rotas delegáveis; teste prova a recusa): ela lê o texto das conversas, já coberto por `conversation.read`, e não faz parte desta fatia.
7. **Front:** o painel de memória (o mesmo do membro) aparece no painel delegado só com `attendance.history`; os botões Concluir/Descartar só com `followup.resolve`; sem a chave a tela diz que o histórico não está liberado. Cache de consulta separado do do membro.
8. **Colunas travadas no banco (achado do Codex, corrigido):** a política de UPDATE controla linhas e estado final, não colunas; o gatilho `follow_up_items_acting_columns` recusa, no contexto delegado, qualquer mudança além da resolução (status, quem, quando, nota): texto, contato, conversa, fechamento, dono, prazo e origem ficam como estavam. As políticas restritivas leem `(SELECT acting_hub())` (InitPlan, como na 114) para que a leitura de um membro não pague uma chamada por linha.

**Provas (POSTGRES + HTTP, papel real `omnira_app`, handler e serviço de atendimento reais):** `serving_history_integration_test.go` — leitor vê atendimentos e pendências abertas e nada de outra instância; sem a chave 403 e o banco não mostra nada nem a quem também é membro; escopo de filas esconde a memória; concessão revogada 404; busca fechada; resolver exige as duas chaves, pendência aberta, assinatura própria, auditoria com o Hub e sem o texto, não reabre nem reescreve resolvida; rota do membro inalterada; agente que também é membro não resolve nem insere sem a chave; com grants nas duas instâncias o contexto de A não mostra B. `scripts/test-hub-history-mutations.sh`: mutantes mortos (resultado no registro de verificação). Front: `DelegatedHistory.test.tsx` (3).

**Não provado:** login real do Keycloak; uso por uma pessoa real do Hub; implantação.

**Revisão do Codex (somente leitura, NÃO executou testes), 2 passadas sobre a fase 06:**
- 1ª: CRITICAL 0, HIGH 0, MEDIUM 1 (UPDATE sem travar colunas → **corrigido**: gatilho), LOW 1 (custo por linha das políticas para membros → **corrigido**: `(SELECT acting_hub())`).
- 2ª (sobre o gatilho e as políticas): CRITICAL 0, HIGH 2, MEDIUM 1, LOW 0.
  - HIGH — um delegado com a flag de administrador na sessão poderia APAGAR uma pendência (o papel da aplicação tem `DELETE` e a política legada o abre ao administrador): **corrigido** com política restritiva `follow_up_items_acting_no_delete`, teste e mutante.
  - MEDIUM — o gatilho comparava uma lista fixa de colunas (coluna futura ficaria solta): **corrigido**, compara a linha inteira em JSON menos as quatro colunas da resolução.
  - HIGH — `acting_hub() IS NULL` é uma guarda que uma sessão capaz de executar SQL arbitrário pode forjar (limpando `app.acting_hub` e ligando `app.is_system_admin`; as GUCs são graváveis pelo papel da aplicação): **NÃO corrigido nesta fatia e NÃO é novo.** É o modelo de confiança de TODA a RLS do OMNIRA (a aplicação define `app.current_user_id`, `app.is_system_admin` e, no Hub, `app.acting_*` só por `lock_served_tenant`), já registrado como pré-existente em `HUB-VERIFICATION-STATUS.md` ("A RLS do OMNIRA confia que a aplicação define as GUCs…") e na seção de riscos deste ADR; vale igual para as migrations 109 a 116 e para as tabelas legadas. A defesa que a RLS entrega é contra erro da aplicação (tenant errado, rota esquecida), não contra SQL arbitrário na sessão da aplicação. Fechar isso exige mudança de arquitetura (GUCs assinadas ou sessão de banco por papel), decisão do dono e do ADR-0041. **Registrado como achado em aberto.**

## 21. Fase 07 (rascunho de encerramento por IA pelo Hub, sem migration) — decisões e estado em 2026-10-10 (local, NÃO implantada)

**O que a pessoa do Hub passa a poder:** no diálogo de finalizar, pedir "Sugerir com IA" (resumo e pendências em RASCUNHO, que a pessoa confere antes de finalizar), da conversa que ELA segura. Chaves: `ai.assist` (já existente, fase 05) e `conversation.read`; para finalizar depois, `conversation.finalize`. Preset novo `completo_pleno` (todas as chaves opcionais: transferir e anotações, finalizar, memória do contato, IA).

**Decisões de desenho**
1. **Sem migration e sem política nova:** `ai.assist` e `conversation.read` já existem; a leitura das mensagens usa a sessão da própria requisição (RLS do contexto delegado: escopo de filas do contrato incluído). A rota `POST .../inbox/conversations/{id}/finalize/suggest` só é delegável quando o servidor injeta a verificação de `conversation.read` (`WithDelegatedSuggest`); sem isso fica fechada ao delegado (404), de modo que um servidor mal montado falha fechado.
2. **`ai.assist` nunca basta:** o texto da conversa sai para o provedor de IA da plataforma, então o handler exige também `conversation.read` (mesma regra do resumo da fase 05), antes de qualquer leitura ou chamada ao modelo.
3. **Só quem SEGURA a conversa recebe rascunho** (o delegado não tem o caminho de "supervisor" do membro) e a condição vale NO COMANDO que lê o texto: a variante do Hub passa por um sugeridor preso ao titular (`SuggestClosingHeldBy` → `RecentMessagesHeldBy`), cuja consulta de mensagens exige, na mesma instrução, `assigned_to_user_id = titular` e conversa aberta. A leitura anterior (`ReadConversation`) só dá as respostas claras (403/422); o que impede o texto de uma conversa que a pessoa deixou de segurar entre as duas leituras de chegar ao provedor é a própria consulta (achado HIGH do Codex, corrigido; teste de corrida simulada). Um sugeridor que não sabe se prender ao titular nunca é usado para um delegado (falha fechada); conversa de colega, solta ou de outra fila → 403/404; conversa finalizada → 422 `nothing_to_suggest`; conversa interna → 422. O membro segue pela rota original, com a matriz de papéis, sem mudança.
4. **O rascunho nunca é gravado:** não finaliza, não cria pendência, não audita texto. O diálogo marca o que veio da IA ("Sugerido pela IA: confira antes de finalizar") e a verdade do resumo é `ai_inferred` até a pessoa confirmar.
5. **Mesma limitação da fase 05:** a chamada ao provedor corre dentro da transação da requisição delegada (que segura as linhas da delegação por alguns segundos); limite do próprio sugeridor (um pedido por conversa a cada 4 s). Cada pessoa tem limite de 5 rascunhos por minuto, contado pelo serviço DEPOIS de validar o pedido (um pedido recusado antes do provedor não gasta cota; como o resumo por IA; o limitador é do processo, então várias réplicas multiplicam o limite — igual ao resumo existente). Não há medição de custo nem de carga; o desenho do streaming (admissão curta, sem transação aberta) não se aplica a uma chamada de IA pontual.

**Revisão do Codex (somente leitura, NÃO executou testes) da fase 07, 2 passadas:**
- 1ª: CRITICAL 0, HIGH 1, MEDIUM 2, LOW 0. HIGH (janela entre "a pessoa segura a conversa?" e a leitura das mensagens): **corrigido** (consulta presa ao titular, no mesmo comando). MEDIUM (sem limite dedicado de custo): **corrigido** (5 por minuto por pessoa). MEDIUM (a chamada lenta ao provedor segura a transação delegada): **aceito e documentado**, igual ao resumo por IA da fase 05.
- 2ª (sobre as correções): CRITICAL 0, HIGH 1, MEDIUM 0, LOW 1. LOW (o limitador gastava cota antes de validar): **corrigido** (o serviço conta o rascunho só depois de validar). **HIGH residual — aceito, com a garantia dita com precisão:** sob `READ COMMITTED` a consulta que lê o texto vê um instante; se outra transação reatribuir ou encerrar a conversa DEPOIS dessa leitura e ANTES de `Generate`, o texto já lido ainda segue ao provedor. Eliminar isso exigiria segurar uma trava na conversa durante uma chamada de IA de vários segundos — o que o delegado nem consegue (não tem política de UPDATE em `conversations`; o caminho de escrita do Hub usa sessão de sistema) e o que travaria quem mais mexe na conversa. **A garantia é: no instante em que o texto é lido, a pessoa segura a conversa aberta.** Não há ganho de confidencialidade na janela residual: quem a segurava um instante antes lê o mesmo texto com `conversation.read` na instância. Se o dono quiser uma garantia mais forte (por exemplo, só rascunho da conversa nunca atribuída a outro nos últimos N segundos), é decisão de produto, não de banco.

**Provas (POSTGRES + HTTP, papel real `omnira_app`, handler e serviço de atendimento reais, leitor REAL das mensagens; o modelo é um dublê que devolve o que recebeu):** `serving_closing_suggest_integration_test.go` — titular com as duas chaves recebe o rascunho só desta conversa (nada de outra conversa, fila ou instância); sem `ai.assist` ou sem `conversation.read` o provedor nunca é alcançado; colega, solta, finalizada e interna recusadas; fora das filas e outra instância 404; concessão revogada 404; membro inalterado e servidor sem a verificação deixa a rota fechada. `scripts/test-hub-suggest-mutations.sh` (resultado no registro de verificação). Front: `DelegatedFinalize.test.tsx` (4).

**Não provado:** chamada real ao provedor de IA; login real do Keycloak; implantação; custo sob carga.

## 22. Fase 08 (o aviso de encerramento ao cliente como ação de servidor, sem migration) — decisões e estado em 2026-10-10 (local, NÃO implantada)

**O problema (MEDIUM aceito na 05b):** o diálogo mandava o aviso ao cliente por uma chamada separada ANTES de finalizar (depois de fechada, a conversa recusa texto de saída). Se a finalização falhasse, o cliente
já tinha sido avisado de um encerramento que não houve; e uma falha transitória do aviso impedia finalizar.

**Decisões de desenho**
1. **O aviso viaja DENTRO do pedido de finalizar** (`farewell`, opcional, até 1000 caracteres, mesma validação de texto livre: UTF-8 e sem credencial). O servidor enfileira a mensagem e o seu job de entrega **na mesma
   transação que fecha a conversa** (antes do `MarkClosed`): ou existem os dois efeitos ou nenhum. A entrega continua com o worker de sempre; nenhuma tabela nova, nenhuma migration.
2. **O aviso é cortesia, nunca pré-condição.** Recusa clara (janela de 24 h fechada, sem linha, a pessoa não pode escrever ao cliente nesta conversa) vira um ESTADO na resposta (`farewell`: `queued`,
   `window_closed`, `no_channel`, `not_permitted`, `unavailable`, `not_requested`) e a finalização segue; qualquer outro erro desfaz tudo (nada fechado, nada enfileirado). O estado vai para a auditoria; o TEXTO nunca.
3. **Membro:** pelo `Sender` de operador (mesma permissão, titular, canal e janela de qualquer resposta; o supervisor que finaliza conversa alheia também pode avisar). **Agente do Hub:** pelo remetente delegado, e a chave
   `conversation.reply` é provada **na sessão delegada** (a escrita roda em sessão de sistema, que não enxerga chaves delegadas), antes do `Act`; sem a chave o estado é `not_permitted` e a finalização segue.
4. **Uma chave por conversa** (`farewell-<conversa>`; a próxima mensagem do contato abre OUTRA conversa) e uma checagem entre remetentes (o índice de idempotência é por remetente): se a chave já foi usada na
   conversa por qualquer pessoa, nenhuma segunda mensagem é criada e o estado é `unavailable`, nunca `queued`. A mesma consulta serializa finalizações simultâneas (trava consultiva por transação).
5. **Contrato e tela:** `farewell` entra em `FinalizeRequest`/`FinalizeResponse` (OpenAPI). O diálogo deixou de chamar a rota de mensagens; depois de finalizar mostra "O aviso ao cliente não foi enviado: <motivo>"
   quando foi pedido e não saiu; uma repetição idempotente (`changed:false`, a resposta anterior pode ter se perdido) não diz nada sobre o aviso.
6. **Ferramenta do gate (`scripts/test-integration.sh`):** pacotes que chegam ao guard pelo ajudante `flowstest` (`attendance/adapters`, `flows/adapters`, `flows/application`, `worker/flows`) estavam fora do gate
   sem ninguém perceber; a descoberta agora os inclui (33 → 37 pacotes). A criação do banco por clonagem do template é repetida (até 5 vezes) só quando o erro é de conexão derrubada; qualquer outro erro é falha real, com a mensagem.

**Revisão do Codex (somente leitura, NÃO executou testes):** CRITICAL 0, HIGH 0, MEDIUM 1, LOW 2 — todos tratados. MEDIUM (a idempotência era por remetente: outra pessoa podia ter usado a chave, e uma mensagem anterior
`failed` seria devolvida como `queued`): **corrigido** (checagem entre remetentes; chave tomada = `unavailable`). LOW (a repetição idempotente aparecia como "aviso não enviado" na tela): **corrigido**. LOW (a repetição do
clone no script engolia o erro): **corrigido** (só repete erro de conexão e mostra a mensagem).

**2ª passada do Codex (sobre as correções):** CRITICAL 0, HIGH 0, MEDIUM 1, LOW 0. Confirmou: a trava consultiva vem DEPOIS do `FOR UPDATE` da conversa (sem ciclo de deadlock entre duas finalizações simultâneas), `NoticeAlreadySent` é
seguro na sessão de sistema do Hub, e o replay não afirma mais "aviso não enviado". **MEDIUM — o prefixo `farewell-` da chave não é reservado:** quem enviar uma mensagem comum com a chave exata `farewell-<id da conversa>`
faz a cortesia ser pulada na finalização (estado `unavailable`, dito na resposta). **Aceito:** exige montar a chave com o UUID da conversa, o efeito é só não avisar (nenhum ganho de acesso, nenhum dado), e o mesmo desenho já
vale para `ticket-opened-<chamado>`; reservar o prefixo na rota de mensagens mudaria a semântica de uma API existente. O teste de concorrência que o Codex notou faltar foi acrescentado (oito finalizações simultâneas com aviso: exatamente um fecha, uma mensagem, um job, todos veem a mesma ficha).

**Provas (POSTGRES + HTTP, papel real `omnira_app`):** `internal/attendance/adapters/closing_notice_integration_test.go` (membro: mensagem + job + fechamento juntos, repetição sem segunda mensagem, sem pedido nada sai, janela
fechada e sem linha seguem finalizando, supervisor avisa e conversa solta não, falha DEPOIS do enfileiramento desfaz mensagem e job, chave já usada por outra pessoa, credencial/texto longo = 400, quem não pode finalizar
não enfileira) e `internal/hub/adapters/serving_closing_notice_integration_test.go` (rotas REAIS de atendimento: com a chave a mensagem sai com o fechamento e a auditoria só traz o estado; sem `conversation.reply` finaliza e
diz `not_permitted`; janela fechada; falha depois do aviso; chave tomada; 400), `closing_notice_test.go` (unitário: só recusas claras viram estado, o resto propaga). **Mutação 13/13** (`scripts/test-hub-farewell-mutations.sh`;
um mutante equivalente documentado no script).

**Não provado:** entrega real por WhatsApp (o teste vai até a mensagem enfileirada e o job); login real do Keycloak; implantação.

## 23. Fase 09 (busca no histórico do contato pelo Hub, sem migration) — local, ver o estado de implantação no HUB-VERIFICATION-STATUS

**O que a pessoa do Hub passa a poder:** buscar trechos nas mensagens ANTERIORES do mesmo contato (`GET .../inbox/conversations/{id}/history-search`), com as chaves `attendance.history` (a rota) e `conversation.read` (provada pelo handler). Era a última rota de atendimento
deliberadamente fechada ao delegado (fase 06).

**Decisões e achado do teste**
1. **A busca é um `SELECT` comum sobre `messages` ⨝ `conversations` na sessão RLS do pedido**, para UM contato derivado da conversa, excluindo a conversa atual. Então o que ela acha é, no máximo, o que as políticas delegadas já mostram àquela pessoa: instância
   fixada, escopo de filas do contrato, outra instância invisível.
2. **Achado do teste (por isso o handler checa `conversation.read`):** as políticas de leitura de `messages` respondem a QUALQUER concessão viva; a chave `conversation.read` é cobrada na aplicação. Uma pessoa com só `attendance.history` leu texto de outras conversas
   (primeira versão do teste, vermelho). Corrigido: `hubAgentMayRead` (extraído do handler do rascunho de IA, comportamento do rascunho inalterado) exige `conversation.read` para a origem `hub_serve`; e a rota só é delegável quando o servidor injeta essa verificação (como o rascunho:
   sem ela, fechada).
3. **Tela:** o campo de busca aparece no painel do Hub só com `attendance.history` e `conversation.read` (a decisão é do servidor).

**Provas (POSTGRES + HTTP, papel real `omnira_app`, rota real):** `serving_history_search_integration_test.go` — com a chave acha as conversas antigas do contato na instância e nada da outra, deixa a conversa atual de fora, 404/400 nos casos de borda; sem a chave 403; só com `attendance.history` 403 e nenhum texto;
contrato restrito a uma fila não acha texto das conversas fora do escopo; o caminho do membro inalterado. **Mutação** (`scripts/test-hub-history-mutations.sh`, 21 mutantes, os 3 novos mortos): rota não delegável, chave que todo delegado tem, `conversation.read` não cobrada. **Vitest:** 868 de 869 (a falha de sempre,
`SettingsShell`); `DelegatedHistory.test.tsx` cobre a busca com e sem as chaves. A lista exata das rotas delegáveis (`delegable_routes_test.go`) ganhou a rota.

**Não provado:** uso por pessoa real do Hub; implantação.

## 24. Paridade de UX do Hub com o layout aprovado (2026-10-12) — LOCAL, sem migration

**Origem:** o dono apontou que a tela do Hub não tinha a fila de pendentes separada do histórico nem o painel lateral de detalhes recolhível, e depois publicou no protótipo Lovable as abas por empresa (commit `158d710`). A análise (as fases 04–09 entregaram o contexto completo da instância, mas a aba "Todas" ficou como lista única + texto) e o contrato resultante estão em `docs/ux/HUB-LAYOUT-CONTRACT.md`.

**O que mudou (aditivo, sem tocar no shell nem na RLS):**
1. **API** `GET /hubs/{id}/inbox`: `segment` (`open|waiting|mine|closed`, outro valor = 400), `q` (nome do cliente, ≤ 80, `%`/`_` literais), `assignment` relativo a quem chama em cada item, `counts` na primeira página (segmento e busca não os alteram), e `companies[].open/unanswered` (o número da aba da empresa). Tudo só ESTREITA o que a RLS já mostra; contado sob a sessão da pessoa. Uma query string que o Go não consegue ler (ponto e vírgula) é 400: o `net/url` descartaria o parâmetro em silêncio e "waiting;x" viraria "tudo".
2. **Tela:** abas de escopo + filtro + busca na fila; três colunas ≥ 1440 px (gaveta abaixo) com `HubContextPane` (seções recolhíveis; abre a caixa completa da empresa só quando o servidor disse `full_context`); faixa "Contexto ativo"; abas de atendimentos abertos (fechar não escreve nada); contador nas abas de instância; menu à esquerda recolhível; `DelegatedContextPane` com as mesmas seções recolhíveis do painel do membro.
3. **Achados do próprio trabalho:** (a) o `COUNT` com `cardinality(NULL)` dava zero para "sem filtro de empresa" (um array nulo do pgx): corrigido com `COALESCE` e coberto; (b) o `aria-label` com contador no `Tab` mudou o nome acessível da aba e quebrou um seletor do E2E (`getByLabel('NorteNet')` achou o filtro e a aba): o seletor ficou específico.

**Provas:** POSTGRES (`inbox_segments_integration_test.go`: segmentos, busca com curingas literais, SQL hostil, empresa sem concessão não listada nem contada, revogação, contadores por empresa); UNIT (`HubLayoutContract.test.tsx`, `ConversationsTabs.test.tsx`, `HubInboxPage.test.tsx`); E2E em navegador real (cenário "layout aprovado…" em `hub.real.spec.ts`).

**Não feito (dito):** contador no item "Conversas" do menu; mídia, status de entrega e tempo real na aba "Todas" (a caixa completa da instância já os tem); persistência das abas de atendimento entre recargas; telefone/e-mail no painel da aba "Todas" (não estão na projeção do Hub: ficam na caixa completa).
