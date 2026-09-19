# Decisões em Aberto — Grill Queue

## BLOCKER 1 — Nível de isolamento comercial

A promessa “banco isolado por Tenant” significa:

- A. isolamento lógico forte em banco compartilhado;
- B. schema por Tenant;
- C. database por Tenant;
- D. tiers, com compartilhado padrão e dedicado enterprise.

**Recomendação inicial:** D como visão de produto, A para o MVP.

## BLOCKER 2 — Backend principal

- A. FastAPI/Python
- B. NestJS/TypeScript

**Recomendação:** escolher pela força real da equipe. Não usar ambos no core do MVP.

## HIGH 3 — Provedor WhatsApp

- Meta Cloud API direta;
- BSP específico;
- camada que aceita ambos desde o início.

**Recomendação:** adapter canônico, uma implementação de produção no MVP.

## HIGH 4 — Contrato IXC

Definir ambiente de teste, versão/API usada, rate limits e conjunto exato de endpoints do piloto.

## HIGH 5 — Estrutura organizacional BPO

Confirmar se um operador pode:
- pertencer a mais de um Hub;
- ter papéis diferentes por Tenant;
- ser simultaneamente operador interno e BPO.

## MEDIUM 6 — Identidade do contato

Definir regra canônica de deduplicação dentro de Tenant: telefone, documento, external id ou merge manual.

## MEDIUM 7 — Retenção/LGPD

Definir prazos de retenção por tipo de dado e capacidade de configuração por Tenant.

## MEDIUM 8 — Chatbot

Confirmar se o MVP precisa de builder visual drag-and-drop ou se um editor estruturado/flow simples já valida a hipótese.
