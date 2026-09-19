# Tenancy e Segurança

## Objetivo

O Hub deve oferecer experiência multicontas sem transformar autorização cross-tenant em acesso global.

## Camadas

### 1. Identidade
Sessão representa Usuário, nunca Tenant implícito.

### 2. Grants
Acesso a Tenant deriva de:
- membership direto; ou
- membership no Hub + grant ativo Hub→Tenant + papel permitido.

### 3. TenantContext
Toda operação tenant-owned recebe um contexto construído pelo backend a partir da sessão e do recurso solicitado.

### 4. Persistência
MVP recomendado: PostgreSQL compartilhado com `tenant_id` obrigatório + Row Level Security ou mecanismo equivalente, índices compostos e testes de isolamento.

### 5. Serviços externos
Credenciais sempre são carregadas a partir do TenantContext; nunca fornecidas pelo cliente.

## Padrão proibido

```text
POST /tickets
{ "tenant_id": "A", ... }

backend confia no tenant_id
```

## Padrão esperado

```text
request resource -> resolve tenant ownership
session -> authorization
authorization -> TenantContext
TenantContext -> repository/service
```

## Hub queries

Inbox consolidada pode consultar múltiplos Tenants **somente** por uma lista de grants autorizados gerada no servidor. A camada de aplicação não aceita uma lista arbitrária do frontend como autoridade.

## Busca

Busca global deve ser:
- restrita aos Tenants autorizados;
- paginada;
- auditável quando envolver export;
- sem retornar snippets de Tenant não autorizado.

## Auditoria mínima

Registrar:
- user_id;
- tenant_id;
- hub_id quando aplicável;
- action;
- resource type/id;
- timestamp;
- correlation id;
- outcome;
- metadados não sensíveis.

## Decisão ainda aberta

A expressão comercial “banco isolado por Tenant” precisa ser fechada em contrato:

A. isolamento lógico forte em banco compartilhado;  
B. schema por Tenant;  
C. database por Tenant;  
D. modelo híbrido com tier dedicado.

A recomendação para MVP é **A**, com caminho arquitetural para **D** em clientes enterprise.
