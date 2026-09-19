# Protocolo de Construção para Agentes

## 1. Trabalhar por release

O agente não recebe a instrução “construa o OMNIRA”.
Ele recebe “construa o próximo ticket da release atual”.

## 2. Branch lógica

Cada ticket deve ser suficientemente pequeno para revisão isolada.

Exemplo R0.1:
- T01 bootstrap;
- T02 infra local;
- T03 schema;
- T04 auth;
- ...

## 3. Gates obrigatórios

Antes de marcar ticket pronto:

### Código
- compila;
- lint/static checks;
- sem dead code relevante.

### Dados
- migration validada;
- constraints;
- rollback/forward-fix considerado.

### Segurança
- autorização backend;
- tenant boundary;
- secrets.

### Operação
- health;
- telemetry;
- erros observáveis;
- retry/idempotência quando assíncrono.

### Contratos
- OpenAPI;
- AsyncAPI quando aplicável.

### Docs
- changelog;
- ADR se necessário;
- runbook se introduziu nova dependência operacional.

## 4. Não antecipar complexidade

Implementar apenas a infraestrutura necessária para a release atual, mantendo os contratos definidos para evolução.

Exemplos:
- definir `WorkflowEngine` não significa instalar Temporal;
- definir `TenantPlacement` não significa provisionar dedicated DB no R0.1;
- definir `Realtime` não significa separar serviço no primeiro deploy.

## 5. Definition of complete

Um slice está completo quando pode ser demonstrado de ponta a ponta sem intervenção manual no banco.
