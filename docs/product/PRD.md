# PRD — OMNIRA

**Status:** Draft v0.1  
**Data:** 2026-09-18  
**Owner:** Produto/Arquitetura  
**Produto:** OMNIRA

## 1. Visão

OMNIRA é uma plataforma SaaS de atendimento digital multi-tenant que permite a empresas operar atendimento próprio e, simultaneamente, permite a BPOs operar múltiplos CNPJs a partir de um Hub unificado sem quebrar isolamento de dados.

A tese central é: **a produtividade de um call center multicontas não deve exigir abrir mão da segregação de dados e das regras de cada contratante.**

## 2. Problemas

### P1 — Fragmentação de canais

Operadores alternam entre interfaces de WhatsApp, webchat, redes sociais, e-mail e sistemas internos, aumentando TME/TMA e erros de contexto.

### P2 — BPO multicontas ineficiente

Ferramentas orientadas a um único tenant forçam login/logout, múltiplas abas ou múltiplas sessões por contratante.

### P3 — Mensageria sem resolução

Muitas plataformas centralizam mensagens, mas o operador ainda precisa acessar ERP/CRM para segunda via, desbloqueio, chamados e consultas.

### P4 — Risco de exposição entre clientes

Uma experiência de Hub mal desenhada pode permitir vazamento acidental ou sistemático entre Tenants.

## 3. Personas

### Administrador do Tenant
Configura canais, departamentos, integrações, usuários, SLAs e políticas.

### Supervisor do Tenant
Acompanha filas, operadores, SLAs e qualidade.

### Operador Interno
Atende somente Tenants aos quais possui membership direto.

### Gestor BPO
Administra hubs, contratos operacionais, operadores e acessos concedidos.

### Operador BPO
Atende múltiplos Tenants autorizados em uma experiência consolidada.

### Consumidor Final
Interage por WhatsApp/Webchat e espera resolução rápida e consistente.

## 4. Jobs to be Done

- “Quero atender vários clientes empresariais sem trocar de sistema.”
- “Quero ter certeza de que um operador só vê o que foi autorizado para o CNPJ em atendimento.”
- “Quero resolver solicitações comuns sem entrar manualmente no ERP.”
- “Quero visualizar filas e SLA em tempo real.”
- “Quero mudar de fornecedor de canal/ERP sem reescrever o domínio central.”

## 5. Objetivos de produto

1. Provar o Hub BPO multicontas com isolamento verificável.
2. Reduzir troca de contexto do operador.
3. Resolver casos simples de ISP dentro do OMNIRA.
4. Criar uma arquitetura de adapters para canais e ERPs.
5. Construir trilha de auditoria suficiente para investigação e conformidade.

## 6. Não objetivos do MVP

- Ser um CRM completo.
- Substituir ERP financeiro ou OSS/BSS.
- Suportar todos os canais no primeiro release.
- Marketplace de integrações.
- IA generativa autônoma atendendo sem guardrails.
- Workforce Management completo.
- Billing SaaS sofisticado por consumo.
- White-label profundo por domínio/custom app.
- Arquitetura de dezenas de microserviços desde o início.

## 7. Escopo funcional do produto

### 7.1 Identidade, Tenant e Hub
- cadastro de Tenant;
- usuários e memberships;
- Hub BPO e associação de Tenants;
- RBAC;
- sessão e troca explícita de contexto;
- auditoria de acessos e ações.

### 7.2 Caixa de entrada omnichannel
- lista de conversas/tickets;
- mensagens em tempo real;
- anexos;
- notas internas;
- tags;
- atribuição;
- transferência;
- histórico.

### 7.3 Filas e roteamento
- filas por Tenant/departamento;
- round-robin;
- roteamento manual;
- disponibilidade do operador;
- SLA básico.

### 7.4 Hub multicontas
- inbox consolidada;
- filtro por Tenant;
- identidade visual inequívoca do Tenant ativo;
- permissões por Tenant;
- proibição de busca global que retorne dados de Tenants não autorizados.

### 7.5 Contatos
- cadastro por Tenant;
- identificadores externos;
- histórico do Tenant;
- deduplicação somente dentro do Tenant no MVP.

### 7.6 Chatbot/automação
- fluxo simples baseado em nós;
- mensagem, condição, captura de campo, chamada de integração, handoff;
- versionamento publicado;
- execução observável.

### 7.7 Integração ERP
Contrato comum para adapters.

Ações iniciais:
- localizar assinante;
- consultar faturas;
- emitir/obter segunda via;
- abrir chamado;
- consultar chamado.

### 7.8 Supervisão
- tickets aguardando;
- tickets ativos;
- TME;
- TMA;
- operadores online/ocupados;
- violações de SLA.

## 8. Requisitos críticos

### Segurança
- toda query de dados tenant-owned deve possuir TenantContext;
- autorização validada no backend;
- RLS/controle equivalente como segunda barreira;
- auditoria para operações administrativas e cross-tenant;
- segredos de canal/ERP criptografados;
- nenhuma credencial é retornada ao frontend após armazenamento.

### Disponibilidade
Meta inicial: 99,5% mensal para aplicação no MVP; elevar após validação operacional.

### Performance
- atualização de mensagem no painel: alvo p95 < 2 s após recebimento;
- ações usuais da UI: alvo p95 < 500 ms excluindo dependência externa;
- abertura da inbox com paginação, nunca carregamento integral.

### Observabilidade
- correlation id;
- tenant id técnico em logs, sem PII desnecessária;
- métricas por adapter;
- tracing de chamadas externas;
- DLQ/retry para eventos assíncronos.

## 9. Regras de negócio essenciais

1. Um Ticket pertence a exatamente um Tenant.
2. Uma Conversa pertence a exatamente um Tenant.
3. Uma conexão de canal pertence a exatamente um Tenant.
4. Um Hub não transfere propriedade dos dados.
5. Um operador BPO só acessa Tenant quando existir grant ativo Hub→Tenant e membership compatível.
6. Alterar TenantContext encerra qualquer seleção de Ticket/Contato anterior na UI.
7. Search, export, analytics e autocomplete obedecem às mesmas fronteiras de Tenant.
8. Identificadores externos nunca substituem IDs internos como chave de autorização.

## 10. Métricas

### North Star operacional
**Resoluções concluídas dentro do OMNIRA sem abrir sistema externo por operador/dia.**

### Ativação
- Tenant conectou primeiro canal;
- recebeu primeira mensagem;
- respondeu primeira conversa;
- configurou primeira integração;
- BPO atendeu primeiro ticket de dois Tenants distintos na mesma sessão.

### Eficiência
- TME;
- TMA;
- FCR;
- transferências por ticket;
- mensagens por resolução.

### Segurança
- tentativas de acesso cross-tenant bloqueadas;
- incidentes de isolamento confirmados: meta = 0.

## 11. Riscos

- APIs de WhatsApp e ERPs mudarem ou apresentarem rate limit.
- Promessa comercial de “banco isolado” ser interpretada como banco físico por CNPJ.
- Complexidade de relatórios cross-tenant do BPO.
- Diferenças semânticas entre ERPs vazarem para o domínio central.
- Crescimento prematuro do builder de chatbot.

## 12. Critério de sucesso do MVP

O MVP é validado quando uma operação BPO piloto consegue, em produção controlada:

1. operar pelo menos dois Tenants na mesma sessão;
2. receber e responder WhatsApp em ambos;
3. visualizar sempre qual Tenant está ativo;
4. executar ao menos duas ações reais no IXC;
5. comprovar por testes automatizados que acessos cross-tenant são bloqueados;
6. operar por uma semana sem incidente de segregação P1/P0.
