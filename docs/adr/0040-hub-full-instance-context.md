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
o cache da instância é descartado e a aba some na próxima leitura. Até a fase 03 concluir, quem tem acesso **só** pelo Hub continua com a visão de texto (aviso na aba).

## 10. Divergências conscientes em relação ao parecer

1. Granularidade do banco por **domínio** e não por operação/tabela (§4.1), porque o RLS não enxerga operação de negócio (classificar e editar são o mesmo `UPDATE contacts`) e porque nem membros têm essa separação no banco.
2. Vocabulário de capacidades = o já existente em `permissions`, não nomes novos (`conversation.read`, `contact.update`...): as chaves do parecer não existem no modelo real (só há `contact.classify` para contato); novas chaves entram como dados quando o produto pedir.
3. A Fase 01 já tem uma primeira matriz; a classificação é proposta e precisa da revisão do parecerista e do dono.
4. A política ilustrativa com `auth.current_tenant_id()` não se aplica: o OMNIRA decide por usuário e relacionamento, não por um GUC de tenant.

## 11. O que NÃO muda

RLS permanece (nenhuma política removida), `tenant_id` do payload/URL nunca autoriza, nada de segredo em fila, nenhuma membership fictícia, delegação não dá acesso implícito a equipe/segurança/chaves de IA,
suspensão continua esperando o trabalho já admitido, e membros mantêm o comportamento atual.
