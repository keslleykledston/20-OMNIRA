# ADR 0002 — Monólito modular primeiro

**Status:** Proposed  
**Data:** 2026-09-18

## Contexto

O produto tem muitos domínios, mas o MVP ainda não possui evidência de escala ou equipes independentes.

## Decisão

Construir backend como monólito modular e workers assíncronos, com contratos claros entre módulos.

## Consequências

- transações e desenvolvimento inicial mais simples;
- menor custo de observabilidade/distribuição;
- exige disciplina para evitar acoplamento interno;
- módulos podem ser extraídos quando métricas justificarem.
