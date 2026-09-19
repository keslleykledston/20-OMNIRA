# Estratégia de Testes

## Unit
Regras de domínio, roteamento, SLA, normalização e permissions.

## Integration
PostgreSQL real em container; Redis/fila; adapters com mocks de contrato.

## Isolation
Suite obrigatória que:
- cria Tenant A/B;
- cria memberships distintos;
- tenta ler/escrever IDs cruzados;
- testa list/search/export;
- testa jobs/eventos com contexto errado;
- testa Hub com grant revogado.

## Contract
Fixtures por adapter para garantir modelos canônicos.

## E2E
J1 a J4 descritas em `MVP.md`.

## Security
- IDOR/BOLA;
- privilege escalation;
- secret exposure;
- webhook spoof/replay;
- SSRF em integrações configuráveis;
- upload validation.
