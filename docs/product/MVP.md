# MVP — OMNIRA

**Status:** Proposed v0.1  
**Objetivo:** validar Hub BPO + isolamento multi-tenant + resolução via ERP.

## Hipótese do MVP

Se um operador BPO puder atender múltiplos CNPJs em uma única sessão, com contexto visual inequívoco e autorização forte, e resolver solicitações comuns de ISP via integração IXC, então o OMNIRA demonstra seu diferencial operacional e técnico.

## Recorte

### P0 — obrigatório

#### Plataforma
- autenticação;
- Tenant;
- usuários e memberships;
- RBAC básico;
- Hub BPO;
- grants Hub→Tenant;
- auditoria.

#### Atendimento
- WhatsApp Oficial via adapter;
- Webchat próprio;
- inbox;
- conversa;
- ticket;
- envio/recebimento de texto;
- anexos básicos;
- notas internas;
- tags;
- atribuição;
- transferência;
- encerrar/reabrir;
- fila;
- round-robin simples.

#### Hub
- inbox consolidada dos Tenants autorizados;
- filtro por Tenant;
- badge/nome/cor do Tenant ativo;
- troca segura de contexto;
- bloqueio de deep links sem autorização;
- busca somente no escopo autorizado.

#### ERP IXC
- configurar credencial por Tenant;
- localizar assinante;
- consultar faturas;
- obter segunda via;
- abrir chamado;
- consultar status de chamado;
- timeout/retry/circuit breaker básico;
- auditoria da ação.

#### Supervisor
- fila aguardando;
- em atendimento;
- TME;
- TMA;
- operadores conectados;
- SLA simples.

#### Engenharia
- migrations;
- testes unitários;
- testes de integração;
- testes de isolamento;
- E2E dos fluxos críticos;
- logs estruturados;
- métricas;
- tracing mínimo;
- CI.

### P1 — entra se P0 estiver estabilizado
- respostas rápidas;
- horário de atendimento;
- mensagem de ausência;
- macros;
- CSAT simples;
- export CSV limitado e auditado;
- chatbot linear com handoff;
- websocket presence melhorado.

### Fora do MVP
- Instagram;
- Telegram;
- E-mail;
- múltiplos ERPs em produção;
- Kanban comercial completo;
- campanhas outbound;
- discador/voz;
- IA generativa autônoma;
- marketplace;
- white-label avançado;
- billing por uso;
- SSO/SAML;
- mobile nativo;
- relatórios BI avançados.

## Jornadas críticas

### J1 — Operador BPO atende dois Tenants

1. operador autentica;
2. Hub carrega Tenants autorizados;
3. inbox consolidada exibe tickets A e B;
4. operador abre Ticket A;
5. UI fixa identidade do Tenant A;
6. resposta é enviada pela conexão do Tenant A;
7. operador abre Ticket B;
8. backend revalida grant/membership;
9. resposta usa exclusivamente recursos do Tenant B.

**Aceite:** nenhum dado ou credencial de A aparece na requisição, resposta, busca ou ações de B.

### J2 — Segunda via no IXC

1. operador abre contato;
2. seleciona ação “Faturas”;
3. adapter IXC identifica assinante;
4. lista faturas;
5. operador solicita segunda via;
6. resultado pode ser enviado ao consumidor;
7. ação fica auditada.

### J3 — Abertura de chamado

1. operador seleciona “Abrir chamado”;
2. informa categoria/resumo;
3. backend chama adapter;
4. registra external id;
5. mostra confirmação;
6. consulta posterior retorna status atualizado.

### J4 — Mensagem recebida

1. webhook válido chega;
2. conexão de canal identifica Tenant;
3. payload é normalizado;
4. contato/conversa/ticket são localizados/criados;
5. roteamento coloca o ticket em fila;
6. evento realtime atualiza a inbox;
7. auditoria técnica mantém correlation id.

## Critérios de aceite P0

- 100% das entidades tenant-owned possuem tenant boundary testada.
- Não existe endpoint de negócio que aceite `tenant_id` como autoridade suficiente.
- Operador sem grant recebe 403/404 seguro ao tentar acessar outro Tenant.
- Webhooks são idempotentes.
- Mensagens não são duplicadas em retries conhecidos.
- A troca de Tenant invalida seleção sensível no frontend.
- Segredos são criptografados em repouso.
- Logs não expõem token de canal/ERP.
- E2E cobre J1–J4.
- `CHANGELOG.md` e documentação são atualizados para release.

## SLOs iniciais

- API própria: 99,5% mensal.
- processamento webhook interno: p95 < 2 s até publicação realtime, sem contar indisponibilidade externa.
- erro interno de envio de mensagem: < 0,5%, excluindo erros do provedor.
- incidente confirmado cross-tenant: 0.

## Kill criteria / sinal de replanejamento

Replanejar antes de expandir canais se:
- o modelo de autorização do Hub exigir bypass de TenantContext;
- o adapter IXC contaminar o domínio com campos específicos;
- a inbox consolidada exigir queries cross-tenant não controláveis;
- testes de isolamento forem frágeis ou dependentes só de convenção.
