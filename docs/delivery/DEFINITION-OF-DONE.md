# Definition of Done

Uma mudança só está pronta quando:

## Funcionalidade

- critérios de aceite atendidos;
- testes relevantes automatizados;
- teste de isolamento incluído quando toca dados tenant-owned;
- tratamento de erro implementado;
- logs/métricas adicionados quando operação crítica;
- migrations testadas;
- ADR atualizado/criado se necessário;

## Documentação e Observabilidade

- docs atualizadas;
- `.changes/` criado para mudança visível;
- contrato OpenAPI/AsyncAPI atualizado quando aplicável;
- telemetry/correlation implementada em operação crítica;
- runbook atualizado quando introduz nova dependência operacional;

## Segurança

- segurança revisada para auth, secrets, export, integration ou Hub;
- nenhum segredo/PII indevido nos logs;

## Docker-First (Obrigatório para componentes executáveis)

- **Build Docker reproduzível** existente ou implementado.
- **Imagem multi-stage** quando aplicável (Go, Node.js).
- **Usuário não-root** na imagem final.
- **Configuração externalizada** via env vars.
- **Nenhum segredo** embutido em imagem/Dockerfile.
- **Healthcheck** implementado quando apropriado.
- **Versão/metadata** disponível (ldflags para Go, etc).
- Testado com `docker build` e `docker compose` se houver mudanças.

## Assíncrono

- retry/idempotência definidos para trabalho assíncrono;
